package sftp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSession 让测试完全不碰网络。
type fakeSession struct{ host string }

func newTestPool() (*Pool, *int) {
	dialed := 0
	p := NewPool(func(host, user string) (*Session, error) {
		dialed++
		return &Session{Host: host, User: user}, nil
	})
	return p, &dialed
}

func TestAcquireTransferSerializesToOne(t *testing.T) {
	p, _ := newTestPool()
	defer p.CloseAll()
	a, err := p.AcquireTransfer(context.Background(), "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		b, err := p.AcquireTransfer(context.Background(), "h1", "u")
		if err == nil {
			p.Release(b, false)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("并发上限为 1：第二个传输必须排队")
	case <-time.After(50 * time.Millisecond):
	}
	p.Release(a, false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("释放后第二个传输应立刻拿到会话")
	}
}

func TestAcquireListNeverQueuesAndCapsIdle(t *testing.T) {
	p, dialed := newTestPool()
	defer p.CloseAll()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s, err := p.AcquireList(ctx, "h1", "u")
		if err != nil {
			t.Fatal(err)
		}
		p.Release(s, true) // 空闲复用
	}
	// 传输占满并发额度时，列表仍必须成功且不排队
	held, _ := p.AcquireTransfer(ctx, "h1", "u")
	defer p.Release(held, false)
	if _, err := p.AcquireList(ctx, "h2", "u"); err != nil {
		t.Fatalf("列表不得因池满失败：%v", err)
	}
	if *dialed > 8 {
		t.Fatalf("idle 池上限 2 应触发回收，dialed=%d", *dialed)
	}
}

func TestDisconnectOnlyClosesIdle(t *testing.T) {
	p, _ := newTestPool()
	defer p.CloseAll()
	busy, _ := p.AcquireTransfer(context.Background(), "h1", "u")
	if err := p.Disconnect("h1"); err != nil {
		t.Fatal(err)
	}
	if busy.Closed() {
		t.Fatal("Disconnect 不得关闭进行中的传输会话")
	}
}

func TestDisconnectMatchesWholeHostComponent(t *testing.T) {
	p, _ := newTestPool()
	defer p.CloseAll()
	ctx := context.Background()

	// 两个前缀相同但不同的 host
	s1, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(s1, true)
	s10, err := p.AcquireList(ctx, "h10", "u")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(s10, true)

	if err := p.Disconnect("h1"); err != nil {
		t.Fatal(err)
	}
	// h10 的 idle 会话必须还在：再 AcquireList("h10") 应当复用而不是新建
	before := s10
	again, err := p.AcquireList(ctx, "h10", "u")
	if err != nil {
		t.Fatal(err)
	}
	if again != before {
		t.Fatal("Disconnect(\"h1\") 不得关掉 \"h10\" 的 idle 会话（前缀误伤）")
	}
	p.Release(again, true)

	// h1 的 idle 确实被关掉了（再取会新建，因此不是同一个指针）
	after, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	if after == s1 {
		t.Fatal("Disconnect(\"h1\") 必须关掉 h1 自己的 idle 会话")
	}
	p.Release(after, true)
}

// (a) 列表会话的 Release 不得归还传输额度（技术审核 S8 的复现用例）：
// 否则第二个 AcquireTransfer 会立刻拿到会话，并发 1 被突破。
func TestListReleaseDoesNotFreeTransferToken(t *testing.T) {
	p, _ := newTestPool()
	defer p.CloseAll()
	held, err := p.AcquireTransfer(context.Background(), "h1", "u")
	if err != nil {
		t.Fatal(err)
	}

	ls, err := p.AcquireList(context.Background(), "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	if ls.isTransfer() {
		t.Fatal("列表会话不得被标记为传输会话")
	}
	p.Release(ls, true)

	done := make(chan struct{})
	go func() {
		b, err := p.AcquireTransfer(context.Background(), "h1", "u")
		if err == nil {
			p.Release(b, false)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("列表 Release 归还了传输额度：并发上限 1 被突破")
	case <-time.After(50 * time.Millisecond):
	}
	p.Release(held, false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("释放传输后第二个传输应立刻拿到会话")
	}
}

// (b) 拿到额度后 ctx 才取消：必须关掉刚建的会话、归还额度并返回 ctx 错误。
// 否则额度与会话双泄漏，后续传播永久阻塞（技术审核 S9）。
func TestAcquireTransferCancelAfterTokenReturnsTokenAndClosesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var made []*Session
	p := NewPool(func(host, user string) (*Session, error) {
		s := &Session{Host: host, User: user}
		made = append(made, s)
		cancel() // 模拟「拿到额度之后、返回之前」用户取消
		return s, nil
	})
	defer p.CloseAll()

	s, err := p.AcquireTransfer(ctx, "h1", "u")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("拿到额度后 ctx 已取消：必须返回 context.Canceled，got s=%v err=%v", s, err)
	}
	if len(made) != 1 {
		t.Fatalf("必须只建一个会话，got %d", len(made))
	}
	if !made[0].Closed() {
		t.Fatal("取消时必须关闭刚建的会话，否则会话泄漏")
	}

	// 额度必须已归还：后续 AcquireTransfer 必须立即成功，而不是永久阻塞。
	done := make(chan error, 1)
	go func() {
		got, err := p.AcquireTransfer(context.Background(), "h1", "u")
		if err == nil {
			p.Release(got, false)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("后续传输失败（额度泄漏）：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("额度未归还：后续 AcquireTransfer 被永久阻塞")
	}
}

// (c) 已取消的 ctx 探活必须返回 false、不得复用/误关 idle 会话；
// 替身 Conn==nil 时探活路径必须确定性失败而不是 panic。
func TestProbeCanceledContextFailsAndDoesNotReuse(t *testing.T) {
	dialed := 0
	p := NewPool(func(host, user string) (*Session, error) {
		dialed++
		return &Session{Host: host, User: user}, nil // Conn=nil：探活必须确定性失败
	})
	defer p.CloseAll()

	// 先留一个 idle 会话作为复用候选（模拟此前一次成功操作）。
	seed, err := p.AcquireList(context.Background(), "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(seed, true)
	if got := len(p.idle["h1\x00u"]); got != 1 {
		t.Fatalf("测试前提错误：idle=%d，应为 1", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p.Probe(ctx, "h1", "u") {
		t.Fatal("ctx 已取消：Probe 必须返回 false")
	}
	if got := len(p.idle["h1\x00u"]); got != 1 {
		t.Fatalf("取消的探活不得取走 idle 会话，idle=%d", got)
	}
	if seed.Closed() {
		t.Fatal("取消的探活不得关闭 idle 会话")
	}
	if dialed != 1 {
		t.Fatalf("取消的探活不得新建会话，dialed=%d", dialed)
	}

	// Conn 为 nil 的替身走真实探测路径：必须返回 false，且失败会话不得回到 idle。
	if p.Probe(context.Background(), "h1", "u") {
		t.Fatal("Conn 为 nil 的替身：Probe 必须返回 false")
	}
	if got := len(p.idle["h1\x00u"]); got != 0 {
		t.Fatalf("探活失败必须丢弃会话，idle=%d", got)
	}
}
