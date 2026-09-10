package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

type fakeStreamer struct {
	handlers osutil.StreamHandlers
	proc     *osutil.Process
	started  chan struct{}
}

func (f *fakeStreamer) StartStream(name string, args []string, h osutil.StreamHandlers) (*osutil.Process, error) {
	f.handlers = h
	sp := osutil.NewSpawner()
	p, err := sp.Start("sleep", []string{"30"}, nil)
	if err != nil {
		return nil, err
	}
	f.proc = p
	if f.started != nil {
		close(f.started)
	}
	return p, nil
}

// pty 下的真实输出（含 CR、噪声行、token 列表、目录事件）必须被正确归一。
func TestInotifySourceParsesRealOutput(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	fs.handlers.OnStdout("Setting up watches.  Beware: since -r was given, this may take a while!")
	fs.handlers.OnStdout("Watches established.")
	fs.handlers.OnStdout("/srv/conf/a.txt|CLOSE_WRITE,CLOSE")
	fs.handlers.OnStdout("/srv/conf/d|CREATE,ISDIR")
	fs.handlers.OnStdout("/srv/conf/d|CLOSE_NOWRITE,CLOSE,ISDIR")

	want := []Event{{"a.txt", KindWrite}, {"d", KindDirAdded}}
	for _, w := range want {
		select {
		case got := <-ch:
			if got != w {
				t.Fatalf("got %+v want %+v", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("等待事件 %+v 超时", w)
		}
	}
}

// 远端 watch 配额耗尽 / 新目录补挂失败 → 必须产出 overflow 强制对账，
// 而不是静默漏同步（徽章仍显示 inotify 是最危险的误信状态）。
func TestInotifySourceSignalsWatchFailure(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	var logs []string
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(level, msg string) {
		logs = append(logs, level+":"+msg)
	})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	fs.handlers.OnStderr("Failed to watch /srv/conf/deep; upper limit on watches reached!")
	select {
	case got := <-ch:
		if got.Kind != KindOverflow {
			t.Fatalf("want overflow, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("配额耗尽必须产出 overflow 事件")
	}
	if len(logs) == 0 {
		t.Fatal("必须同时记一条日志说明原因")
	}
}

// HARD REQUIREMENT：远端命令必须保留 --timefmt '%s'（epoch 秒）。解析器的
// 形状判别式依赖"首个 | 之前是纯数字"，换成非数字格式会让每条真实输出被误判为
// 无时间戳形状、过不了 root 前缀校验而被静默丢弃——watch 看起来活着却不出事件。
func TestInotifySourceRemoteCommandKeepsEpochTimefmt(t *testing.T) {
	src := NewInotifySource(&fakeStreamer{}, DetectOpts{RemotePath: "/srv/conf"}, func(string, string) {})
	cmd := src.RemoteCommand()
	if !strings.Contains(cmd, "--timefmt '%s'") {
		t.Fatalf("远端命令必须保留 --timefmt '%%s': %s", cmd)
	}
	if !strings.HasPrefix(cmd, "exec inotifywait ") {
		t.Fatalf("远端命令必须以 exec inotifywait 开头: %s", cmd)
	}
	if !strings.Contains(cmd, sshconn.QuoteRemote("/srv/conf")) {
		t.Fatalf("远端路径必须经 QuoteRemote 转义: %s", cmd)
	}
}

// Start 必须 honour ctx：取消 ctx 后探测进程被杀掉、channel 关闭。
// brief 的实现没有接 ctx，若不补这条守卫，上层只 cancel 不 Close 时会泄漏常驻 ssh
// 与远端 inotifywait。
func TestInotifySourceStartHonoursCtxCancel(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(string, string) {})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := src.Start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	cancel()
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("ctx 取消后 channel 必须已关闭")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后探测进程必须被杀掉并关闭 channel")
	}
}

// 实测事实 #3：启动期 "Couldn't watch ... No such file or directory" 是配置错
// （远端路径写错），必须是致命启动错误（Err() != nil，引擎据此进 error 不重连）。
func TestInotifySourceFatalWatchErrorBeforeEstablished(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/nope"}, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	fs.handlers.OnStdout("Couldn't watch /srv/nope: No such file or directory")
	if src.Err() == nil {
		t.Fatal("启动期远端路径不存在必须是致命错误")
	}
	select {
	case got := <-ch:
		if got.Kind != KindOverflow {
			t.Fatalf("want overflow, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("必须产出 overflow 强制对账")
	}
}

// 同一文案出现在 "Watches established." 之后只是运行期补挂失败（新目录），
// 只能降级为 overflow，不得污染 Err() 把可恢复抖动当致命错。
func TestInotifySourceLateWatchErrorIsOnlyOverflow(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	fs.handlers.OnStdout("Watches established.")
	fs.handlers.OnStdout("Couldn't watch /srv/conf/new: No such file or directory")
	if src.Err() != nil {
		t.Fatalf("运行期补挂失败不应是致命错误: %v", src.Err())
	}
	select {
	case got := <-ch:
		if got.Kind != KindOverflow {
			t.Fatalf("want overflow, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("必须产出 overflow 强制对账")
	}
}

// Close 幂等，且必须关闭 channel，否则引擎的 range 永不退出（goroutine 泄漏）。
func TestInotifySourceCloseClosesChannel(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-fs.started
	if err := src.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close 必须幂等: %v", err)
	}
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("Close 后 channel 必须已关闭")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未关闭 channel")
	}
}
