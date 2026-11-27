package sftp

import (
	"sync"
	"testing"

	"sshore/internal/osutil"
)

// —— S3：Ctrl.backend() 懒构造 GoBackend 必须并发安全 ——
//
// Wails 每个绑定调用各起 goroutine，并发首发 backend() 时若无锁会构造两个 GoBackend：
// 取消分派到被覆盖的实例而失效、两个 swapJournal 违反单写者前提、传输并发上限翻倍、
// 指针本身数据竞争。本测试用 50 个 goroutine 同时首发 backend()，断言所有 goroutine
// 拿到**同一个** *GoBackend 实例。修复前在 -race 下应报竞争或指针不一致。

func TestCtrlBackendLazyConstructionIsConcurrencySafe(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "gosftp")
	c := NewCtrlWith(osutil.NewRunner(), nil, nil)
	defer c.CloseAll()

	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	got := make([]Backend, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 同时放行，最大化并发首发窗口
			got[i] = c.backend()
		}(i)
	}
	close(start)
	wg.Wait()

	first, ok := got[0].(*GoBackend)
	if !ok {
		t.Fatalf("gosftp 选择器应解析出 *GoBackend, got %T", got[0])
	}
	for i, b := range got {
		gb, ok := b.(*GoBackend)
		if !ok {
			t.Fatalf("goroutine %d 拿到非 *GoBackend: %T", i, b)
		}
		if gb != first {
			t.Fatalf("goroutine %d 拿到不同的 GoBackend 实例（并发双重构造）: %p != %p", i, gb, first)
		}
	}
}

// TestCtrlSetJournalDirConcurrentWithBackend：SetJournalDir 读写 goBackend，必须与 backend()
// 的懒构造共用同一把锁，并发调用不得触发数据竞争。
func TestCtrlSetJournalDirConcurrentWithBackend(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "gosftp")
	c := NewCtrlWith(osutil.NewRunner(), nil, nil)
	defer c.CloseAll()
	dir := t.TempDir()

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = c.backend()
		}()
	}
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c.SetJournalDir(dir)
		}()
	}
	close(start)
	wg.Wait()
}
