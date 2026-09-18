package sftp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSession 让测试完全不碰网络。
type fakeSession struct{ host string }

// transferFlag 在测试里只读地窥探 transfer 标记。
// 生产代码只保留消费入口 takeTransfer：只读不清正是 C1 的成因，故已删除 isTransfer。
func transferFlag(s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer
}

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
	if transferFlag(ls) {
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

// TestTransferTokenReturnedOnlyOnceAcrossReuse 复现 Task 4 评审 Critical C1：
// 传输会话 Release(_, true) 停进 idle 后被 AcquireList 复用，再按 list 语义
// Release(_, false) 时不得二次归还 token —— 否则第三个传输会在 t2 仍在飞行时
// 立刻拿到额度，并发 1 被突破。queue 容量 1 看不见在飞行的传输。
func TestTransferTokenReturnedOnlyOnceAcrossReuse(t *testing.T) {
	p, dialed := newTestPool()
	defer p.CloseAll()
	ctx := context.Background()

	t1, err := p.AcquireTransfer(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(t1, true) // 计划形状：传输成功后停进 idle

	reused, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	if reused != t1 {
		t.Fatalf("前置条件：期望复用同一会话，dialed=%d", *dialed)
	}

	t2, err := p.AcquireTransfer(ctx, "h1", "u") // 取走唯一额度，仍在飞行
	if err != nil {
		t.Fatal(err)
	}
	p.Release(reused, false) // 复用来的会话按 list 语义归还：不得释放额度

	done := make(chan struct{})
	go func() {
		s, err := p.AcquireTransfer(ctx, "h1", "u")
		if err == nil {
			p.Release(s, false)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("CRITICAL：复用会话的 Release 二次归还了 token，并发 1 被突破")
	case <-time.After(100 * time.Millisecond):
	}
	p.Release(t2, false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("释放 t2 后第三个传输应立刻拿到额度")
	}
}

// TestIdleCapEvictsOldestReleasedAndKeepsOtherHosts 覆盖 idle 上限 2 + LRU 淘汰。
// 同时持有 3 条列表会话（强制 3 次 dial），按 a、b、c 顺序归还：
//   - 该 (host,user) 键恰好留 2 条 idle；
//   - 最早归还的 a 被关闭并移出 all；b/c 存活；
//   - 下一次 AcquireList 复用最近归还的 c（不新建）；
//   - 另一 host 的 idle 会话完全不受影响。
func TestPoolIdleCapEvictsOldestReleasedAndKeepsOtherHosts(t *testing.T) {
	p, dialed := newTestPool()
	defer p.CloseAll()
	ctx := context.Background()

	a, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.AcquireList(ctx, "h2", "u")
	if err != nil {
		t.Fatal(err)
	}
	if *dialed != 4 {
		t.Fatalf("测试前提错误：并持有 3+1 条会话应 dial 4 次，dialed=%d", *dialed)
	}

	// 按 a、b、c 顺序归还：c 触发上限，最早归还的 a 必须被淘汰。
	p.Release(a, true)
	p.Release(b, true)
	p.Release(c, true)
	p.Release(other, true)

	p.mu.Lock()
	h1Idle := len(p.idle["h1\x00u"])
	h2Idle := len(p.idle["h2\x00u"])
	p.mu.Unlock()
	if h1Idle != maxIdlePerHost {
		t.Fatalf("idle 上限必须为 %d，实际 %d", maxIdlePerHost, h1Idle)
	}
	if h2Idle != 1 {
		t.Fatalf("另一 host 的 idle 不得被牵连，实际 %d", h2Idle)
	}

	if !a.Closed() {
		t.Fatal("LRU 必须关闭最早归还的会话 a")
	}
	if b.Closed() || c.Closed() {
		t.Fatalf("b/c 必须存活，b.Closed=%v c.Closed=%v", b.Closed(), c.Closed())
	}

	// 复用最近归还的 c，且不新建会话。
	beforeDial := *dialed
	got, err := p.AcquireList(ctx, "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatal("AcquireList 必须复用最近归还的 c（LIFO/LRU）")
	}
	if *dialed != beforeDial {
		t.Fatalf("复用不得新建会话，dialed %d -> %d", beforeDial, *dialed)
	}

	// 另一 host 的会话仍可复用，不受 h1 淘汰影响。
	gotOther, err := p.AcquireList(ctx, "h2", "u")
	if err != nil {
		t.Fatal(err)
	}
	if gotOther != other {
		t.Fatal("另一 host 的 idle 会话必须仍可复用")
	}
	p.Release(got, true)
	p.Release(gotOther, true)
}
