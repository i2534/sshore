package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sshore/internal/osutil"
)

func TestDetectForcePollWins(t *testing.T) {
	called := false
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		called = true
		return osutil.Outcome{}, nil
	}
	info := Detect(context.Background(), r, DetectOpts{ForcePoll: true, PollInterval: 5 * time.Second})
	if info.Mode != "poll" || !strings.Contains(info.Reason, "强制轮询") {
		t.Fatalf("got %+v", info)
	}
	if called {
		t.Fatal("用户已选择轮询，不应再去探测远端")
	}
}

func TestDetectUsesInotifyWhenPresent(t *testing.T) {
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{ExitCode: 0, Stdout: "/usr/bin/inotifywait"}, nil
	}
	if info := Detect(context.Background(), r, DetectOpts{}); info.Mode != "inotify" {
		t.Fatalf("got %+v", info)
	}
}

// 实测：远端没有 inotifywait 时 command -v 退出码是 1（不是 127）。
func TestDetectFallsBackWhenMissing(t *testing.T) {
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{ExitCode: 1}, nil
	}
	info := Detect(context.Background(), r, DetectOpts{PollInterval: 5 * time.Second})
	if info.Mode != "poll" {
		t.Fatalf("got %+v", info)
	}
	if !strings.Contains(info.Reason, "1") {
		t.Fatalf("Reason 必须带具体退出码，得到 %q", info.Reason)
	}
}

// 探测超时必须降级，且不能只是"不等了"——子进程由 CtxRunner 真正回收。
func TestDetectTimeoutFallsBack(t *testing.T) {
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		<-ctx.Done()
		return osutil.Outcome{}, errors.New("context deadline exceeded")
	}
	start := time.Now()
	info := Detect(context.Background(), r, DetectOpts{PollInterval: 5 * time.Second})
	if info.Mode != "poll" || !strings.Contains(info.Reason, "超时") {
		t.Fatalf("got %+v", info)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("探测必须有自己的超时")
	}
}
