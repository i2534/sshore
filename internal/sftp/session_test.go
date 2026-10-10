package sftp

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/pkg/sftp"
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

	done := make(chan error, 1)
	go func() {
		// Task 4 评审 N1：AcquireTransfer 若因丢 token 永久阻塞，这里必须快速失败，
		// 而不是挂到 go test 的默认 10m panic。
		wctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		s, err := p.AcquireTransfer(wctx, "h1", "u")
		if err != nil {
			done <- err
			return
		}
		p.Release(s, false)
		done <- nil
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("CRITICAL：复用会话的 Release 二次归还了 token，并发 1 被突破")
		}
		t.Fatalf("token 丢失回归：第三次 AcquireTransfer 未在 1s 内拿到额度: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	p.Release(t2, false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("释放 t2 后第三个传输应立刻拿到额度，却失败: %v", err)
		}
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

// —— 探活预算缺陷回归（审核 BF5 / 规格 §8.2）——

// newPipeSession 造一条走**真实 pkg/sftp 协议**的会话：net.Pipe + sftp.NewServer(root)
// + sftp.NewClientPipe，照 internal/sftp/copy_download_test.go 的先例（不联网、不起 ssh，
// 但走真正的 SFTP 往返）。
//
// 为什么必须这样造：Session.Conn 是具体类型 *sftp.Client（session.go），替身换不掉 ——
// 想让 `ReadDirContext(".")` 真的成功/真的失败，只能用真实的协议两端。root 是服务端的
// 工作目录（"." 即 root）。
//
// Proc 用跨平台替身子进程（helper_test.go）：Session.close 会 Close+Wait+Kill，nil Proc
// 会 nil panic；且带 Proc 的会话才算「真的建立过连接」。
// 返回 kill：关掉服务端与两端管道（用于把这条会话变成「断链」）。
func newPipeSession(t *testing.T, host, user, root string) (*Session, func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	srv, err := sftp.NewServer(c1, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		_ = c1.Close()
		_ = c2.Close()
		t.Fatalf("起内存 sftp 服务端失败: %v", err)
	}
	go func() { _ = srv.Serve() }()
	cl, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		_ = srv.Close()
		_ = c1.Close()
		_ = c2.Close()
		t.Fatalf("建内存 sftp 客户端失败: %v", err)
	}
	kill := func() {
		_ = srv.Close()
		_ = c1.Close()
		_ = c2.Close()
	}
	t.Cleanup(func() {
		_ = cl.Close() // 幂等：Session.close 也会关它
		kill()
	})
	return &Session{Host: host, User: user, Conn: cl, Proc: testHelperPipes(t, "")}, kill
}

// TestPoolProbeDidNotFalseNegativeOnSlowDial 钉住审核 BF5（规格 R1）的误判：
// AcquireList（session.go）**完全不用 ctx**，池里没有 idle 会话时会真拨号，最长吃
// ConnectTimeout=10s。拨号一旦慢于 probeTimeout，随后的 ReadDirContext 面对**已过期**的
// cctx 会立刻返回 ctx.Err() ⇒ 修复前会把一条**健康**连接判失效、还会 Release(s,false)
// 关掉它，进而在前端触发「断开 + 重连」的无辜 teardown。
// 修复后：cctx 过期但会话确实建出来了（s.Conn != nil）⇒ 用**新预算**复核一次再判。
func TestPoolProbeDidNotFalseNegativeOnSlowDial(t *testing.T) {
	root := t.TempDir()
	var dialed *Session
	p := NewPool(func(host, user string) (*Session, error) {
		// 拨号比探针预算慢：模拟 ConnectTimeout 吃满 probeTimeout 的真实形态。
		time.Sleep(probeTimeout + 300*time.Millisecond)
		s, _ := newPipeSession(t, host, user, root)
		dialed = s
		return s, nil
	})
	defer p.CloseAll()

	if !p.Probe(context.Background(), "h", "u") {
		t.Fatal("拨号慢于 probeTimeout 但会话健康：Probe 必须返回 true（不得因 cctx 过期把健康连接判死）")
	}
	if dialed == nil {
		t.Fatal("探活必须真的拨号建出会话")
	}
	if dialed.Closed() {
		t.Fatal("健康会话必须被 Release(s,true) 放回池里，绝不能被 Release(s,false) 关掉")
	}
}

// TestPoolProbeStillFailsForBrokenSession 是上一条的反向护栏：预算修复绝不能变成
// 「永远不会失败」。远端不可用（ReadDirContext 拿到真实协议错误）时探活必须答 false，
// 且失败会话必须按既有语义 Release(s,false) 丢弃，不得留在池里坑后续操作。
// 用「服务端工作目录消失」造错误：确定性、不依赖网络时序、也不会挂住。
func TestPoolProbeStillFailsForBrokenSession(t *testing.T) {
	root := t.TempDir()
	var dialed *Session
	p := NewPool(func(host, user string) (*Session, error) {
		s, _ := newPipeSession(t, host, user, root)
		dialed = s
		return s, nil
	})
	defer p.CloseAll()

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("造不可用远端失败: %v", err)
	}
	if p.Probe(context.Background(), "h", "u") {
		t.Fatal("ReadDirContext 失败的会话：Probe 必须返回 false")
	}
	if dialed == nil {
		t.Fatal("测试前提错误：探活必须真的拨号")
	}
	if !dialed.Closed() {
		t.Fatal("探活失败的会话必须被 Release(s,false) 丢弃（否则死会话会留在池里）")
	}
}

// TestPoolProbeSlowDialBrokenSessionStillFails 是上面两条的交叉对照（T2 复核 Important）：
// 「慢拨号 + 健康」只钉住「不得把健康连接判死」，「快拨号 + 坏」的 cctx 又没过期、根本走不到
// 复核分支 —— 于是「cctx 过期即 return true、不复核」这个错法两条用例都绿。本用例把两个条件
// 叠起来：拨号吃满 probeTimeout（回到 Probe 时 cctx 已过期）**且**远端真的不可用。此时第一次
// ReadDirContext 只会拿到「预算没了」，必须用新预算复核后答 false，并把这条坏会话
// Release(s,false) 丢弃（否则死会话留在池里坑后续操作）。
func TestPoolProbeSlowDialBrokenSessionStillFails(t *testing.T) {
	root := t.TempDir()
	var dialed *Session
	p := NewPool(func(host, user string) (*Session, error) {
		s, _ := newPipeSession(t, host, user, root)
		// 会话建出之后再让远端不可用（手法同 TestPoolProbeStillFailsForBrokenSession）：
		// 新预算复核时 ReadDirContext 拿到的是真实协议错误，而不是「预算没了」。
		if err := os.RemoveAll(root); err != nil {
			t.Fatalf("造不可用远端失败: %v", err)
		}
		time.Sleep(probeTimeout + 300*time.Millisecond) // 拨号吃满预算：Probe 拿到的 cctx 已过期
		dialed = s
		return s, nil
	})
	defer p.CloseAll()

	if p.Probe(context.Background(), "h", "u") {
		t.Fatal("慢拨号（cctx 已过期）但远端真的不可用：必须用新预算复核后答 false，不得「过期即 true」")
	}
	if dialed == nil {
		t.Fatal("测试前提错误：探活必须真的拨号")
	}
	if !dialed.Closed() {
		t.Fatal("复核失败的坏会话必须被 Release(s,false) 丢弃（否则死会话会留在池里）")
	}
}
