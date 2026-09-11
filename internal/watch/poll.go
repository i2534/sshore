package watch

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// PollSource 每 interval 扫描一轮远端树，与上一轮比对产出事件。
// **一轮 = 一次事务**：失败或不完整的一轮不产出任何事件。
type PollSource struct {
	list     ListManyFunc
	opts     DetectOpts
	maxDepth int
	excludes []string
	interval time.Duration
	after    func(time.Duration) <-chan time.Time
	log      func(level, msg string)

	mu       sync.Mutex
	prev     map[string]Meta
	hasPrev  bool
	failures int

	ch       chan Event
	stop     chan struct{}
	stopOnce sync.Once
	doneCh   chan struct{} // 轮询 goroutine 退出（channel 已关）后关闭
}

// maxPollFailures：连续 5 轮失败即停止轮询，由引擎判定进入 error（spec §6.3）。
const maxPollFailures = 5

// Failures 供引擎在轮询停止后区分"抖动重连"与"连续失败进 error"。
func (p *PollSource) Failures() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures
}

func NewPollSource(list ListManyFunc, o DetectOpts, maxDepth int, excludes []string,
	after func(time.Duration) <-chan time.Time, log func(level, msg string)) *PollSource {
	if after == nil {
		after = time.After
	}
	if log == nil {
		log = func(string, string) {}
	}
	iv := o.PollInterval
	if iv <= 0 {
		iv = 5 * time.Second
	}
	return &PollSource{list: list, opts: o, maxDepth: maxDepth, excludes: excludes,
		interval: iv, after: after, log: log}
}

func (p *PollSource) Info() Info {
	return Info{Mode: "poll", Interval: p.interval}
}

func (p *PollSource) Start(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, 256)
	stop := make(chan struct{})
	done := make(chan struct{})

	// ch/stop/done 必须先发布再跑首轮：round 里的 emit 要读 stop，
	// 失败阈值路径也要能关掉同一个 stop。
	p.mu.Lock()
	p.ch, p.stop, p.doneCh = ch, stop, done
	p.stopOnce = sync.Once{} // 支持同一实例重新 Start（重连场景）
	p.mu.Unlock()

	// 首轮立即跑：既建立基线，也让"远端路径不存在"在启动阶段就暴露。
	p.round(ctx, ch)

	go func() {
		// 先关 ch 再关 done：Close 等到 done 时，channel 一定已经关好。
		defer close(done)
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-p.after(p.interval):
			}
			p.round(ctx, ch)
		}
	}()
	return ch, nil
}

// Close 幂等：关闭 stop 让轮询 goroutine 退出，并等待它把 channel 关掉。
// **不能只依赖 ctx**——引擎可能传 context.Background()，ctx 永不取消，
// 那样 goroutine 会泄漏、channel 永不关闭，引擎的 range 永不退出。
func (p *PollSource) Close() error {
	p.mu.Lock()
	stop, done := p.stop, p.doneCh
	p.mu.Unlock()
	if stop != nil {
		p.stopOnce.Do(func() { close(stop) })
	}
	if done != nil {
		<-done
	}
	return nil
}

// round 执行一轮扫描与比对。失败或不完整 ⇒ 零事件 + 失败计数。
func (p *PollSource) round(ctx context.Context, ch chan Event) {
	snap, err := ScanTree(ctx, p.list, p.opts.Host, p.opts.User, p.opts.RemotePath, p.maxDepth, p.excludes)
	if err != nil || !snap.Complete {
		p.mu.Lock()
		p.failures++
		n := p.failures
		p.mu.Unlock()
		why := "扫描失败"
		if err == nil {
			why = "扫描不完整（有目录未知）"
		}
		p.log("warn", why+"，已跳过本轮（连续第 "+strconv.Itoa(n)+" 次）")
		if n >= maxPollFailures {
			p.log("error", "连续 "+strconv.Itoa(maxPollFailures)+" 轮扫描失败，停止轮询")
			p.mu.Lock()
			stop := p.stop
			p.mu.Unlock()
			if stop != nil {
				p.stopOnce.Do(func() { close(stop) })
			}
		}
		return
	}
	p.mu.Lock()
	p.failures = 0
	prev, hasPrev := p.prev, p.hasPrev
	p.prev, p.hasPrev = snap.Entries, true
	p.mu.Unlock()

	if !hasPrev {
		return // 首轮仅建立基线
	}
	for rel, cur := range snap.Entries {
		old, existed := prev[rel]
		switch {
		case !existed:
			p.emit(ctx, ch, Event{RelPath: rel, Kind: KindCreate})
		case old.Size != cur.Size || old.ModTime != cur.ModTime:
			// mtime 不同**不能**用来判定"未变化"：精度只到分钟、老文件只到天，
			// 且 parseModTime 用 time.Now().Year() 猜年份（跨年会集体误报）。
			// 这里只把它当作"疑似变化"，宁可多传一次。
			p.emit(ctx, ch, Event{RelPath: rel, Kind: KindWrite})
		}
	}
	for rel := range prev {
		if _, ok := snap.Entries[rel]; !ok {
			p.emit(ctx, ch, Event{RelPath: rel, Kind: KindDelete})
		}
	}
}

// emit 投递事件；Close 或 ctx 取消时立即返回，绝不把轮询 goroutine 永久挂在发送上。
// ctx 与 stop 都要进 select：只等 stop 时，若引擎用 ctx 取消而没 Close，
// 被背压顶住的发送方会永久阻塞，goroutine 也永不退出。
func (p *PollSource) emit(ctx context.Context, ch chan Event, ev Event) {
	p.mu.Lock()
	stop := p.stop
	p.mu.Unlock()
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	select {
	case ch <- ev:
	case <-stop:
	case <-ctxDone:
	}
}

var _ Source = (*PollSource)(nil)
