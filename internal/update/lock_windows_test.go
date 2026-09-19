//go:build windows

package update

import "testing"

// TestAcquireExclusiveOnWindows 验证 Windows 侧命名互斥体的排他语义：
// 同一进程内对同一目录的第二次 Acquire 必须失败（CreateMutex 返回
// ERROR_ALREADY_EXISTS），释放后又能再次加锁。本文件在 Linux 上不参与编译，
// 实际执行发生在 CI 的 go-windows job。
func TestAcquireExclusiveOnWindows(t *testing.T) {
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

// TestReleaseIdempotentOnWindows 验证 release 幂等：连续调用两次不 panic、
// 不因重复关闭句柄出错，且释放后锁确实可以再次获取。
func TestReleaseIdempotentOnWindows(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatalf("加锁失败: %v", err)
	}
	release()
	release() // 重复调用不得 panic，也不得让本进程残留在锁外
	again, err := Acquire(dir)
	if err != nil {
		t.Fatalf("释放后应能再次加锁: %v", err)
	}
	again()
}
