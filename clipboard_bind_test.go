//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshore/internal/clipboard"
)

func TestAppClipboardFileSupportFalseOnLinux(t *testing.T) {
	if (&App{}).ClipboardFileSupport() {
		t.Error("Linux 上 ClipboardFileSupport() 必须是 false（前端据此不渲染「文件」项）")
	}
}

func TestAppCopyFilesToClipboardDegradesOnLinux(t *testing.T) {
	err := (&App{}).CopyFilesToClipboard([]string{"/tmp"})
	if err == nil {
		t.Fatal("Linux 上 CopyFilesToClipboard 必须返回错误，而不是静默成功")
	}
	if !errors.Is(err, clipboard.ErrUnsupported) {
		t.Errorf("err=%v, want clipboard.ErrUnsupported", err)
	}
}

func TestAppClipboardTempDirCreatesDir(t *testing.T) {
	p, err := (&App{}).ClipboardTempDir()
	if err != nil {
		t.Fatalf("ClipboardTempDir(): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(p) })

	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat(%q): %v", p, err)
	}
	if !st.IsDir() {
		t.Errorf("%q 不是目录", p)
	}
	if !strings.HasPrefix(filepath.Base(p), clipboard.TempDirPrefix) {
		t.Errorf("%q 缺少前缀 %q", filepath.Base(p), clipboard.TempDirPrefix)
	}
}

// TestStartupCleanupIsInjectable 钉住 startup 的陈旧临时目录清理走**包级注入点**：
// 若 startup 直接调真函数，任何调用 a.startup(...) 的测试（既有
// TestStartupDoesNotBlockOnStalePartCleanup 就是）都会真去扫删开发机的 /tmp。
func TestStartupCleanupIsInjectable(t *testing.T) {
	oldClean := cleanupClipboardTempDirsAt
	t.Cleanup(func() { cleanupClipboardTempDirsAt = oldClean })
	oldParts := cleanupStaleLocalPartsAt
	t.Cleanup(func() { cleanupStaleLocalPartsAt = oldParts })
	cleanupStaleLocalPartsAt = func([]string) {} // 本测试不真扫开发机目录

	called := make(chan string, 1)
	cleanupClipboardTempDirsAt = func(root string, minAge time.Duration, now time.Time) (int, error) {
		select {
		case called <- root:
		default:
		}
		return 0, nil
	}

	a := NewApp()
	go a.startup(context.Background())

	select {
	case root := <-called:
		if root != os.TempDir() {
			t.Errorf("清理 root=%q, want %q", root, os.TempDir())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup 未通过 cleanupClipboardTempDirsAt 调用清理（注入点没接上 ⇒ 会真删开发机 /tmp）")
	}
}
