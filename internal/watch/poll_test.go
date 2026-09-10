package watch

import (
	"context"
	"sync"
	"testing"
	"time"

	"sshore/internal/sftp"
)

// fakeTicker 让测试自己决定何时推进，不靠 sleep。
type fakeTicker struct{ ch chan time.Time }

func newFakeTicker() *fakeTicker                           { return &fakeTicker{ch: make(chan time.Time, 1)} }
func (f *fakeTicker) after(time.Duration) <-chan time.Time { return f.ch }

// fakeRemote 是可变的远端树 + 轮次计数。
type fakeRemote struct {
	mu     sync.Mutex
	tree   map[string][]sftp.Item
	rounds int
}

func newFakeRemote() *fakeRemote { return &fakeRemote{tree: map[string][]sftp.Item{}} }

func (f *fakeRemote) set(dir string, items ...sftp.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tree[dir] = items
}

// del 让某个目录"列不出来"：仍在父目录列表里，但 ListMany 的 map 会缺这个 key。
func (f *fakeRemote) del(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tree, dir)
}

func (f *fakeRemote) roundsNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rounds
}

func (f *fakeRemote) waitRounds(n int) {
	for i := 0; i < 300; i++ {
		f.mu.Lock()
		done := f.rounds >= n
		f.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeRemote) list(host, user string, paths []string) (map[string][]sftp.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rounds++
	res := map[string][]sftp.Item{}
	for _, p := range paths {
		if v, ok := f.tree[p]; ok {
			res[p] = v
		}
	}
	return res, nil
}

func drainKinds(t *testing.T, ch <-chan Event, n int) []Kind {
	t.Helper()
	var got []Kind
	for i := 0; i < n; i++ {
		select {
		case ev := <-ch:
			got = append(got, ev.Kind)
		case <-time.After(2 * time.Second):
			t.Fatalf("等待第 %d 个事件超时，已收到 %v", i+1, got)
		}
	}
	return got
}

func TestPollSourceFirstRoundIsBaseline(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"))
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, 0, nil, newFakeTicker().after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	select {
	case ev := <-ch:
		t.Fatalf("第一轮只建立基线，不应产事件，得到 %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPollSourceDiffsCompleteRounds(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"))
	tk := newFakeTicker()
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, 0, nil, tk.after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	fs.waitRounds(1)

	fs.set("/r", file("a.txt"), file("b.txt"))
	tk.ch <- time.Now()
	if got := drainKinds(t, ch, 1); got[0] != KindCreate {
		t.Fatalf("want create, got %v", got)
	}

	fs.set("/r", sftp.Item{Name: "a.txt", Size: 9, ModTime: "2026-09-10 11:00"})
	tk.ch <- time.Now()
	if got := drainKinds(t, ch, 1); got[0] != KindWrite {
		t.Fatalf("want write, got %v", got)
	}

	fs.set("/r")
	tk.ch <- time.Now()
	if got := drainKinds(t, ch, 1); got[0] != KindDelete {
		t.Fatalf("want delete, got %v", got)
	}
}

// 不完整轮次必须零事件：否则"列不出来"会被当成"文件都没了"，进而删本地文件。
func TestPollSourceIncompleteRoundEmitsNothing(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"), dir("d1"))
	fs.set("/r/d1", file("x.txt"))
	tk := newFakeTicker()
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, -1, nil, tk.after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	fs.waitRounds(1)

	// d1 仍在 /r 的列表里，但它自己这一层列不出来（ListMany 缺 key）⇒ 本轮不完整。
	fs.del("/r/d1")
	tk.ch <- time.Now()
	select {
	case ev := <-ch:
		t.Fatalf("不完整轮次必须零事件，得到 %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

// Close 必须幂等、真正停掉轮询 goroutine，并且 channel 只关闭一次。
func TestPollSourceCloseIsIdempotentAndStops(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"))
	tk := newFakeTicker()
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, 0, nil, tk.after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	fs.waitRounds(1)

	if err := src.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("close twice: %v", err)
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("基线轮不产事件，Close 后 channel 应已关闭且无缓冲事件")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未关闭 channel")
	}

	// Close 之后 ticker 不应再驱动任何一轮扫描。
	before := fs.roundsNow()
	select {
	case tk.ch <- time.Now():
	default:
	}
	time.Sleep(80 * time.Millisecond)
	if got := fs.roundsNow(); got != before {
		t.Fatalf("Close 后轮询 goroutine 仍在跑：rounds %d -> %d", before, got)
	}
}

// ctx 取消也必须停掉轮询并关闭 channel（Start 必须 honour ctx）。
func TestPollSourceStopsOnContextCancel(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"))
	tk := newFakeTicker()
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, 0, nil, tk.after, func(string, string) {})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := src.Start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	fs.waitRounds(1)

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("基线轮不产事件，ctx 取消后 channel 应已关闭")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消未关闭 channel")
	}
}
