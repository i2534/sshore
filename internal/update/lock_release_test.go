//go:build !windows

package update

import "testing"

func TestAcquireReleaseIsIdempotent(t *testing.T) {
	release, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatalf("加锁失败: %v", err)
	}
	release()
	release() // 重复调用不得 panic
}
