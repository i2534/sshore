//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireIsExclusiveWithinProcess(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatalf("首次加锁失败: %v", err)
	}
	// 第二次必须失败：两个实例同时 apply 会互相 rename，可能弄丢二进制（spec §7.6）
	if _, err := Acquire(dir); err == nil {
		t.Fatal("同一目录重复加锁必须失败")
	}
	release()
	release2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("释放后应能再次加锁: %v", err)
	}
	release2()
}

func TestAcquireLocksExeDirWithExpectedFileName(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatalf("加锁失败: %v", err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, ".sshore-update.lock")); err != nil {
		t.Fatalf("锁文件应位于 ExeDir 内: %v", err)
	}
}
