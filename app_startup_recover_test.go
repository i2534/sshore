package main

import (
	"context"
	"errors"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"sshore/internal/forward"
	"sshore/internal/sftp"
)

// injectStartupNoopCleanup 把 startup 的两处真实清理注入成 no-op，
// 避免测试扫删开发机目录（与 TestStartupCleanupIsInjectable 同做法）。
// 两个 fake 都发完成信号：cleanup 还原注入点**之前**先等后台 goroutine 读完，
// 否则「还原写」与「goroutine 读注入点」构成数据竞争（-race 实测会报）。
func injectStartupNoopCleanup(t *testing.T) {
	t.Helper()
	oldParts := cleanupStaleLocalPartsAt
	oldClip := cleanupClipboardTempDirsAt
	partsDone := make(chan struct{}, 1)
	clipDone := make(chan struct{}, 1)
	t.Cleanup(func() {
		for _, ch := range []chan struct{}{partsDone, clipDone} {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
			}
		}
		cleanupStaleLocalPartsAt = oldParts
		cleanupClipboardTempDirsAt = oldClip
	})
	cleanupStaleLocalPartsAt = func([]string) { partsDone <- struct{}{} }
	cleanupClipboardTempDirsAt = func(string, time.Duration, time.Time) (int, error) {
		clipDone <- struct{}{}
		return 0, nil
	}
}

// A8 回归：startup 必须接线崩溃恢复 —— Init 装配 a.sftp 后异步调用 RecoverSwaps
// 恰好一次；恢复本身（可能拨号不可达主机）绝不能阻塞 startup。
// 修复前：RecoverSwaps 只在 OnShutdown 调用，启动时不恢复（fake 永不触发）。
func TestStartupWiresRecoverSwapsAsync(t *testing.T) {
	injectStartupNoopCleanup(t)
	oldRecover := recoverSwapsAtStartup
	t.Cleanup(func() { recoverSwapsAtStartup = oldRecover })

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls int32
	recoverSwapsAtStartup = func(c *sftp.Ctrl) (int, error) {
		atomic.AddInt32(&calls, 1)
		entered <- struct{}{}
		<-release // 模拟不可达主机拨号的慢恢复：必须后台执行，不得挡 startup
		return 0, nil
	}

	a := NewApp()
	done := make(chan struct{})
	go func() { a.startup(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startup 被 swap 恢复阻塞：必须后台异步执行")
	}
	a.Init(func(forward.Event) {})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup 后未接线 RecoverSwaps（Init 完成后应异步调用）")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("RecoverSwaps 应恰好调用一次，得到 %d", n)
	}
}

// A8: 恢复失败必须写日志事件（warn，含错误原因），风格参照现有 startup 日志。
func TestStartupRecoverSwapsErrorLogged(t *testing.T) {
	injectStartupNoopCleanup(t)
	oldRecover := recoverSwapsAtStartup
	t.Cleanup(func() { recoverSwapsAtStartup = oldRecover })
	recoverSwapsAtStartup = func(c *sftp.Ctrl) (int, error) { return 0, errors.New("dial failed") }

	var mu stdsync.Mutex
	var events []forward.Event
	a := NewApp()
	a.startup(context.Background())
	a.Init(func(e forward.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		found := false
		for _, e := range events {
			if e.Level == "warn" && strings.Contains(e.Message, "dial failed") {
				found = true
			}
		}
		mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("恢复失败必须经日志事件上报（warn + 错误原因）")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A8: 恢复成功且有条目时写 info/warn 日志（与 OnShutdown 的「恢复了 N 处」同风格）。
func TestStartupRecoverSwapsSuccessLogged(t *testing.T) {
	injectStartupNoopCleanup(t)
	oldRecover := recoverSwapsAtStartup
	t.Cleanup(func() { recoverSwapsAtStartup = oldRecover })
	recoverSwapsAtStartup = func(c *sftp.Ctrl) (int, error) { return 2, nil }

	var mu stdsync.Mutex
	var events []forward.Event
	a := NewApp()
	a.startup(context.Background())
	a.Init(func(e forward.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		found := false
		for _, e := range events {
			if strings.Contains(e.Message, "启动恢复了 2 处中断提交") {
				found = true
			}
		}
		mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("恢复成功必须经日志事件上报条数")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
