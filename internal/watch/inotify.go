package watch

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// InotifySource 用一条常驻的远端 inotifywait 进程提供近实时事件。
//
// 【已知弱点 W1，必须保留这段注释】
// inotifywait -r 没有 maxdepth：内核 watch 会覆盖全部层级，规则里的 max_depth
// 只在**解析事件时**过滤，并不能减少内核 watch 配额的开销。大目录（例如下面挂着
// node_modules）可能耗尽配额，后果是启动即失败（Failed to watch）或带残缺 watch
// 静默漏同步。需要真正限量扫描时，请在规则里关闭 inotify（force_poll = true）
// 改用轮询路径。
type InotifySource struct {
	sp   osutil.Streamer
	opts DetectOpts
	log  func(level, msg string)

	mu   sync.Mutex
	proc *osutil.Process
	ch   chan Event
	done chan struct{}

	emitMu sync.Mutex // 串行化 emit 与 close(ch)
	closed bool

	sawEstablished bool  // 是否已收到 "Watches established."（区分启动期/运行期）
	startErr       error // 启动期致命错误（远端路径不存在、watch 配额耗尽）
	unmatched      int   // 无法解析的行数（spec §6.2 要求计数告警）
}

// Err 返回启动期的致命错误。引擎在探测断开后据此判定"进 error 不重连"，
// 而不是把它当成一次可重连的抖动。
func (s *InotifySource) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startErr
}

func NewInotifySource(sp osutil.Streamer, o DetectOpts, log func(level, msg string)) *InotifySource {
	if log == nil {
		log = func(string, string) {}
	}
	return &InotifySource{sp: sp, opts: o, log: log}
}

func (s *InotifySource) Info() Info { return Info{Mode: "inotify"} }

// RemoteCommand 单独导出便于测试断言参数构造。
func (s *InotifySource) RemoteCommand() string {
	return "exec inotifywait -m -r --format '%T|%w%f|%e' --timefmt '%s' " +
		sshconn.QuoteRemote(s.opts.RemotePath)
}

func (s *InotifySource) Start(ctx context.Context) (<-chan Event, error) {
	args := []string{
		"-tt", // 必须是 -tt：不加时杀掉本地 ssh 会在远端留下孤儿进程（已实测）
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlPath=" + sshconn.ControlPath(s.opts.Host, s.opts.User),
	}
	if s.opts.User != "" {
		args = append(args, "-o", "User="+s.opts.User)
	}
	args = append(args, s.opts.Host, s.RemoteCommand())

	ch := make(chan Event, 256)
	proc, err := s.sp.StartStream("ssh", args, osutil.StreamHandlers{
		OnStdout: s.handleStdout(ch),
		OnStderr: s.handleStderr(ch),
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.proc, s.ch, s.done = proc, ch, make(chan struct{})
	done := s.done
	s.mu.Unlock()

	// channel 的唯一关闭点：进程结束（自然退出或 Close 杀掉）后关闭。
	go func() {
		proc.Wait()
		s.emitMu.Lock()
		s.closed = true
		close(ch)
		s.emitMu.Unlock()
		close(done)
	}()

	// ctx 取消必须真正杀掉探测进程（HARD REQUIREMENT：Start 必须 honour ctx）。
	// brief 的实现只依赖显式 Close 与进程自然退出；若上层只 cancel ctx 而不 Close
	// （例如引擎用 select <-ctx.Done() 直接退出），常驻 ssh 会泄漏，远端 inotifywait
	// 也随之残留。这是本任务补的最小守卫：ctx 取消与进程退出谁先到都能收敛。
	go func() {
		select {
		case <-ctx.Done():
			_ = proc.Kill()
		case <-done:
		}
	}()

	return ch, nil
}

// Close 幂等：杀掉探测进程并等待 channel 关闭。
func (s *InotifySource) Close() error {
	s.mu.Lock()
	proc, done := s.proc, s.done
	s.mu.Unlock()
	if proc == nil {
		return nil
	}
	_ = proc.Kill()
	<-done
	return nil
}

func (s *InotifySource) emit(ch chan Event, ev Event, done chan struct{}) {
	// close(ch) 与 stdout/stderr 的 scanLines goroutine 是并发的：cmd.Wait()
	// 返回后立刻 close(ch) 时，回调可能正在执行 ch <- ev —— 往已关闭的 channel
	// 发送会 panic（select 挡不住）。所以用一个 closed 标志把"关闭"与"发送"串起来。
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.closed {
		return
	}
	select {
	case ch <- ev:
	case <-done:
	}
}

func (s *InotifySource) handleStdout(ch chan Event) func(string) {
	return func(line string) {
		// **-tt 下远端 stderr 与 stdout 合并**（实测：Setting up watches... 就出现在
		// stdout 里），所以 watch 故障文案必须在这里也识别一次——只挂在 OnStderr
		// 上等于死路径，徽章会一直显示 inotify 而 watch 其实残缺。
		if s.noteWatchProblem(ch, line) {
			return
		}
		ev, ok := ParseInotifyLine(s.opts.RemotePath, line)
		if !ok {
			s.countUnmatched(line)
			return
		}
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		s.emit(ch, ev, done)
	}
}

// handleStderr 只是 stdout 路径的补充：-tt 下两者已经合并，识别逻辑集中在
// noteWatchProblem，避免两处判断漂移。
func (s *InotifySource) handleStderr(ch chan Event) func(string) {
	return func(line string) { s.noteWatchProblem(ch, line) }
}

// noteWatchProblem 识别 inotifywait 的故障/阶段文案；返回 true 表示该行已消费。
//
// 阶段区分（spec §6.2）："远端路径不存在"只可能发生在 "Watches established."
// 之前 —— 那是配置错，规则要进 error 且**不重连**；运行期补挂失败（新目录）
// 只降级为 overflow（强制对账 + 禁止删除）。
func (s *InotifySource) noteWatchProblem(ch chan Event, line string) bool {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "watches established"):
		s.mu.Lock()
		s.sawEstablished = true
		s.mu.Unlock()
		return true
	case strings.Contains(l, "setting up watches"):
		return true
	case strings.Contains(l, "failed to watch"), strings.Contains(l, "upper limit"):
		err := fmt.Errorf("远端 inotify watch 配额耗尽：%s", line)
		s.mu.Lock()
		s.startErr = err
		s.mu.Unlock()
		s.log("error", err.Error())
		s.signalOverflow(ch)
		return true
	case strings.Contains(l, "couldn't watch"):
		s.mu.Lock()
		fatal := !s.sawEstablished && strings.Contains(l, "no such file")
		if fatal {
			s.startErr = fmt.Errorf("远端路径不存在：%s", line)
		}
		s.mu.Unlock()
		if fatal {
			s.log("error", "远端路径不存在："+line)
		} else {
			s.log("warn", "有目录无法建立 watch，watch 集合不完整："+line)
		}
		s.signalOverflow(ch)
		return true
	}
	return false
}

// countUnmatched 计数无法解析的行，超阈值记一条带样本的 warn（spec §6.2）。
// 只对噪声行静默会让真的格式故障（版本差异、CR 未裁）无从发现。
func (s *InotifySource) countUnmatched(sample string) {
	s.mu.Lock()
	s.unmatched++
	n := s.unmatched
	s.mu.Unlock()
	if n == 100 || n%1000 == 0 {
		s.log("warn", fmt.Sprintf("有 %d 行 inotifywait 输出无法解析，样本: %s", n, sample))
	}
}

func (s *InotifySource) signalOverflow(ch chan Event) {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		s.emit(ch, Event{Kind: KindOverflow}, done)
	}
}

var _ Source = (*InotifySource)(nil)
