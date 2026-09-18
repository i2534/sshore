package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// —— Task 10 hermetic 取消测试 ——
// 与 Task 7/8 同一模式：net.Pipe 接 pkg/sftp 的真实客户端 + 真实服务端（见
// copy_download_test.go 的 backendForTestServer）。取消的真实语义是「关掉该传输独占的
// 会话，让在飞的 read/write 立刻失败」，这里必须真跑，而不是断言一个 mock 被调过。

// blockAfterFirstChunk 把 copyStream 换成「写出首块后停住，直到 release」的实现。
// 这样「取消发生在传输进行中」是确定性的（不靠 sleep），并且确实存在一个在飞会话。
// release 幂等且挂在 t.Cleanup 上：用例中途失败也不会把搬运 goroutine 永久悬挂。
func blockAfterFirstChunk(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	var relOnce sync.Once
	swapCopyStream(t, func(dst io.Writer, src io.Reader) (int64, error) {
		buf := make([]byte, 32*1024)
		var total int64
		for {
			n, rerr := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return total, werr
				}
				total += int64(n)
				once.Do(func() { close(enteredCh) })
				<-releaseCh // 首块落盘后停住，等用例取消
			}
			if rerr != nil {
				if rerr == io.EOF {
					return total, nil
				}
				return total, rerr
			}
		}
	})
	release = func() { relOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(release)
	return enteredCh, release
}

// captureSession 包一层 dial，记下本次传输实际使用的会话。
func captureSession(g *GoBackend) (get func() *Session) {
	var mu sync.Mutex
	var s *Session
	orig := g.pool.dial
	g.pool.dial = func(host, user string) (*Session, error) {
		got, err := orig(host, user)
		mu.Lock()
		s = got
		mu.Unlock()
		return got, err
	}
	return func() *Session {
		mu.Lock()
		defer mu.Unlock()
		return s
	}
}

// TestGoBackendCancelInflightDownloadClosesSessionAndRefundsOnce 是本 task 的核心用例：
// 取消一次在飞的下载必须 ①关掉该传输的会话、②让操作以错误结束、③不提交（最终名不存在）、
// ④保留 .part 作续传锚点、⑤幂等（第二次 false）、⑥**恰好归还一次**并发额度。
func TestGoBackendCancelInflightDownloadClosesSessionAndRefundsOnce(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("k"), 1<<20) // 1 MiB：跨多个 maxPacket
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	sessOf := captureSession(g)
	entered, release := blockAfterFirstChunk(t)

	done := make(chan error, 1)
	req := TransferRequest{ID: "t-cancel", Host: "h", Remote: "src.bin", Local: local, Atomic: true}
	go func() { done <- g.Get(req, nil) }()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块：会话/协议卡死")
	}

	// 传输确认在飞：取消必须返回 true（会话被关）。
	if !g.Cancel("t-cancel") {
		t.Fatal("取消在飞传输必须返回 true（关会话）")
	}
	// 幂等：同一 id 第二次取消返回 false，而不是错误、更不是真值。
	if g.Cancel("t-cancel") {
		t.Fatal("第二次取消必须返回 false（幂等）")
	}
	release()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("被取消的传输必须返回错误（库无逐请求 ctx，取消靠关会话）")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 10s 内 Get 仍未返回（取消不生效）")
	}

	if _, serr := os.Stat(local); !os.IsNotExist(serr) {
		t.Fatalf("取消后最终名不得出现，stat err=%v", serr)
	}
	part := ""
	for _, e := range mustReadDir(t, localDir) {
		if IsInternalTemp(e.Name()) {
			part = filepath.Join(localDir, e.Name())
		}
	}
	if part == "" {
		t.Fatal("取消后必须保留 .part（Task 11 的续传锚点）")
	}
	pb, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	if len(pb) == 0 || len(pb) > len(data) || !bytes.Equal(pb, data[:len(pb)]) {
		t.Fatalf(".part 必须是源前缀且非空，got %d 字节", len(pb))
	}

	s := sessOf()
	if s == nil {
		t.Fatal("未捕获会话")
	}
	if !s.Closed() {
		t.Fatal("取消后该传输的会话必须已关闭")
	}
	if g.Connected("h") {
		t.Fatal("取消后 Connected 必须为 false（被取消的会话绝不塞回 idle）")
	}

	// 额度恰好在取消后被归还：len(queue) 必须是 1。任何「关了会话但没 Release」的取消
	// 路径都会让这里变成 0，后续传输永久排队（Task 4 复现过的死锁类）。
	if got := len(g.pool.queue); got != 1 {
		t.Fatalf("取消后必须恰好归还一次并发额度，queue 深度=%d want 1", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s2, err := g.pool.AcquireTransfer(ctx, "h", "")
	if err != nil {
		t.Fatalf("取消后并发额度必须立即可用（否则下一次传输饿死）: %v", err)
	}
	g.pool.Release(s2, false)
}

// TestGoBackendCancelInflightUploadKeepsRemotePartAndNeverCommits 上传方向的对称用例：
// 取消后远端最终名不得出现、远端 .part 保留、错误上报。
func TestGoBackendCancelInflightUploadKeepsRemotePartAndNeverCommits(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	const target = "dst.bin"
	data := bytes.Repeat([]byte("u"), 1<<20)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockAfterFirstChunk(t)

	done := make(chan error, 1)
	req := TransferRequest{ID: "t-up", Host: "h", Remote: target, Local: local, Atomic: true}
	go func() { done <- g.Put(req, nil) }()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块：会话/协议卡死")
	}
	if !g.Cancel("t-up") {
		t.Fatal("取消在飞上传必须返回 true（关会话）")
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("被取消的上传必须返回错误")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 10s 内 Put 仍未返回")
	}

	if _, serr := os.Stat(filepath.Join(remoteRoot, target)); !os.IsNotExist(serr) {
		t.Fatalf("取消后远端最终名不得出现，stat err=%v", serr)
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 1 {
		t.Fatalf("取消后必须保留恰好一个远端 .part（续传锚点），got %v", temps)
	}
	if g.Cancel("t-up") {
		t.Fatal("上传取消同样必须幂等：第二次返回 false")
	}
}

// TestGoBackendCancelUnknownCompletedAndEmptyReturnsFalse 钉住诚实语义：只有「确实取消到
// 一次在飞传输」才返回 true。未知/从未开始/已完成（已注销）一律 false，且完成后注册表
// 不得残留陈旧键（Task 10 事实 4：陈旧键绝不能命中别的会话）。
func TestGoBackendCancelUnknownCompletedAndEmptyReturnsFalse(t *testing.T) {
	fresh := NewGoBackend(nil, nil)
	defer fresh.CloseAll()
	if fresh.Cancel("never-started") {
		t.Fatal("未知 id 必须返回 false")
	}
	if fresh.Cancel("") {
		t.Fatal("空 id 必须返回 false")
	}

	remoteRoot, localDir := t.TempDir(), t.TempDir()
	writeRemote(t, remoteRoot, "src.bin", []byte("done-content"))
	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "t-done", Host: "h", Remote: "src.bin", Local: filepath.Join(localDir, "dst.bin"), Atomic: true}
	if err := g.Get(req, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if g.Cancel("t-done") {
		t.Fatal("已完成的 id 必须返回 false（注册表应在返回时注销）")
	}
	if n := g.regSize(); n != 0 {
		t.Fatalf("传输结束后注册表必须清空，got %d 条", n)
	}
}

// regSize 读取注册表条目数（测试观测点）。
func (g *GoBackend) regSize() int {
	g.regMu.Lock()
	defer g.regMu.Unlock()
	return len(g.reg)
}

// TestGoBackendCancelConcurrentExactlyOneWinsAndRaceFree -race 下并发取消同一在飞 id：
// 必须**恰好一个** true（其余看到条目已被移除，返回 false），绝不双重关会话；
// 并发取消未知 id 则一律 false。
func TestGoBackendCancelConcurrentExactlyOneWinsAndRaceFree(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("m"), 1<<20))
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockAfterFirstChunk(t)

	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: "t-race", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块")
	}

	const n = 16
	results := make([]bool, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = g.Cancel("t-race")
		}(i)
	}
	close(start)
	wg.Wait()
	trues := 0
	for _, ok := range results {
		if ok {
			trues++
		}
	}
	if trues != 1 {
		t.Fatalf("并发取消同一在飞 id 必须恰好一个 true，got %d", trues)
	}

	unknown := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unknown[i] = g.Cancel("t-unknown")
		}(i)
	}
	wg.Wait()
	for i, ok := range unknown {
		if ok {
			t.Fatalf("并发取消未知 id[%d] 必须 false", i)
		}
	}

	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("被取消的传输必须返回错误")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 10s 内 Get 仍未返回")
	}
}

// mustReadDir 读目录，失败即 Fatal（测试辅助）。
func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ents
}

// —— Task 10 修复轮 1 新增用例（I1/I2/I3 + M1）——

// TestGoBackendCancelDoesNotRefundTokenWhileInFlight 钉住 I1：取消只关会话，**绝不**额外
// 归还并发额度。原用例只在传输返回后看 len(queue)==1 —— refill 是非阻塞发送，多还一次会被
// 静默丢弃，因此那断言证明不了「恰好一次」。本用例在 Cancel 返回 true、而原传输仍卡在首帧
// 回调里时，用短超时 ctx 直接断言 AcquireTransfer **拿不到** token：任何在 Cancel 里顺手
// refill 的实现都会让这里成功，断言失败。
func TestGoBackendCancelDoesNotRefundTokenWhileInFlight(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("t"), 1<<20))
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockAfterFirstChunk(t)

	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: "t-token", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块：会话/协议卡死")
	}
	if !g.Cancel("t-token") {
		t.Fatal("取消在飞传输必须返回 true")
	}

	// 此刻原传输仍卡在首帧回调里（Get 尚未返回），它的 defer Release 也尚未执行 ——
	// 额度必须还在被占用。短超时 AcquireTransfer 必须拿不到 token。
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s2, err := g.pool.AcquireTransfer(ctx, "h", "")
	if err == nil {
		g.pool.Release(s2, false)
		t.Fatal("Cancel 提前归还了并发额度：原传输仍卡在首帧回调里，AcquireTransfer 不得拿到 token（I1）")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireTransfer 应因短超时失败（额度未被提前归还），got %v", err)
	}
	if got := len(g.pool.queue); got != 0 {
		t.Fatalf("Cancel 返回后额度不得已归还（原传输仍在飞），queue 深度=%d want 0", got)
	}

	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("被取消的传输必须返回错误")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 10s 内 Get 仍未返回")
	}
	// 传输返回后额度才由 defer Release 归还，且恰好一次。
	if got := len(g.pool.queue); got != 1 {
		t.Fatalf("传输返回后额度必须恰好归还一次，queue 深度=%d want 1", got)
	}
}

// TestGoBackendCancelDuringCommittedFinalFrameReturnsFalse 钉住 I2：Get 在提交成功之后、
// 注销之前**同步**发末帧，UI 的 emitProgress 也是同步回调 —— 这个窗口可以被任意拉宽。
// 若不先置 committed，Cancel 会在文件已落地时返回 true（不诚实）。把 Cancel 放进末帧回调
// 正是把窗口加宽到确定性：必须返回 false，且目标文件完整落地。
//
// 判据：只有当 PartPath 指向**最终目标**（而非 .part）且 Done==Total 时才是提交后的末帧；
// 传输中的节流帧 PartPath 恒为 .part，绝不会误触发。
func TestGoBackendCancelDuringCommittedFinalFrameReturnsFalse(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("f"), 1<<20)
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	var called, result bool
	report := func(p Progress) {
		if !called && p.PartPath == local && p.Done == p.Total {
			called = true
			result = g.Cancel("t-final")
		}
	}
	if err := g.Get(TransferRequest{ID: "t-final", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, report); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !called {
		t.Fatal("未观察到提交后末帧（PartPath=最终目标）")
	}
	if result {
		t.Fatal("提交完成后 Cancel 必须诚实返回 false：文件已落地，不能假装取消成功（I2）")
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("提交后最终文件必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("提交后内容必须与源一致: got %d bytes want %d", len(got), len(data))
	}
	if g.Cancel("t-final") {
		t.Fatal("传输结束后 Cancel 必须返回 false")
	}
}

// TestGoBackendPutCancelDuringCommittedFinalFrameReturnsFalse 是 I2 的上传对称用例：
// Put 的末帧同样在 commitRemote（PosixRename 或 backup-swap）成功之后同步发出。
func TestGoBackendPutCancelDuringCommittedFinalFrameReturnsFalse(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("g"), 1<<20)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	const target = "dst.bin"

	g := backendForTestServer(t, remoteRoot)
	var called, result bool
	report := func(p Progress) {
		// 上传方向的提交后末帧 PartPath 指向最终远端目标（传输中的帧指向远端 .part）。
		if !called && p.PartPath == target && p.Done == p.Total {
			called = true
			result = g.Cancel("t-final-up")
		}
	}
	if err := g.Put(TransferRequest{ID: "t-final-up", Host: "h", Remote: target, Local: local, Atomic: true}, report); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !called {
		t.Fatal("未观察到上传提交后末帧（PartPath=最终远端目标）")
	}
	if result {
		t.Fatal("上传提交完成后 Cancel 必须诚实返回 false（I2）")
	}
	got, err := os.ReadFile(filepath.Join(remoteRoot, target))
	if err != nil {
		t.Fatalf("提交后远端最终名必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("提交后远端内容必须与源一致: got %d bytes want %d", len(got), len(data))
	}
	if g.Cancel("t-final-up") {
		t.Fatal("传输结束后 Cancel 必须返回 false")
	}
}

// TestGoBackendCancelledSessionNeverHandedOut 钉住 I3：Cancel 与 Release 竞争时，已关闭的
// 会话绝不能留在 idle 被后续 AcquireList（Capabilities/Probe/List/ListMany 的入口）交出去。
// 竞态顺序：Release 先看到 Closed()==false 并准备入 idle，Cancel 随后才关会话。
// 用注入点 cancelCloseSession 把「删除注册表条目」与「真正 close」之间的窗口加宽成确定性：
// Cancel 删条目后停住 → 让 Release 把会话放回 idle → 再放行 Cancel 关会话。
// 断言：AcquireList 必须跳过这条 Closed 会话、新建一条，绝不把它交出去。
func TestGoBackendCancelledSessionNeverHandedOut(t *testing.T) {
	var mu sync.Mutex
	dialed := 0
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.pool = NewPool(func(host, user string) (*Session, error) {
		mu.Lock()
		dialed++
		mu.Unlock()
		return &Session{Host: host, User: user}, nil // Conn/Proc 为 nil：close 安全且廉价
	})

	ctx := context.Background()
	s, err := g.pool.AcquireTransfer(ctx, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	entry := g.register("t-race", s, nil)
	if entry == nil {
		t.Fatal("非空 id 必须登记进取消表")
	}

	// 加宽窗口：Cancel 删条目后停在 close 之前。
	closeEntered := make(chan struct{})
	allowClose := make(chan struct{})
	origClose := cancelCloseSession
	cancelCloseSession = func(victim *Session) {
		close(closeEntered)
		<-allowClose
		origClose(victim)
	}
	t.Cleanup(func() { cancelCloseSession = origClose })

	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- g.Cancel("t-race") }()
	select {
	case <-closeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel 未进入关闭窗口")
	}

	// Cancel 已删注册表条目但尚未 close：Release(reuse=true) 认为会话可用，把它放回 idle。
	// 这正是竞态里「Closed 会话进 idle」的成因。
	g.pool.Release(s, true)
	close(allowClose)
	if ok := <-cancelDone; !ok {
		t.Fatal("取消在飞传输必须返回 true")
	}
	if !s.Closed() {
		t.Fatal("Cancel 返回后会话必须已关闭")
	}

	// 关键断言：AcquireList 绝不能交出这条已关闭会话。
	got, err := g.pool.AcquireList(ctx, "h", "")
	if err != nil {
		t.Fatalf("AcquireList: %v", err)
	}
	if got == s {
		t.Fatal("已取消（Closed）的会话被再次交出：后续 Capabilities/Probe/List 必然伪失败（I3）")
	}
	if got.Closed() {
		t.Fatal("AcquireList 交出的会话不得是 Closed")
	}
	mu.Lock()
	n := dialed
	mu.Unlock()
	if n != 2 {
		t.Fatalf("必须跳过 Closed 会话并新建一条，dialed=%d want 2", n)
	}
	g.pool.Release(got, false)
}

// TestGoBackendEmptyIDNeverRegisters 钉住 M1：空 id（legacy 四参面不带 id）不登记进取消表，
// Cancel("") 永远 false —— 绝不把「无身份标识」的传输伪装成可取消。
func TestGoBackendEmptyIDNeverRegisters(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("e"), 1<<20))

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockAfterFirstChunk(t)

	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{Host: "h", Remote: "src.bin", Local: filepath.Join(localDir, "d.bin"), Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块")
	}
	if n := g.regSize(); n != 0 {
		t.Fatalf("空 id 不得登记（legacy 面无身份标识），got %d 条", n)
	}
	if g.Cancel("") {
		t.Fatal("Cancel(\"\") 必须返回 false（空 id 永不命中）")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("未被取消的传输必须正常完成: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内 Get 未返回")
	}
}

// TestGoBackendPutCancelDuringBlockedCommitReturnsTrueAndFails 钉住「!committed ⇒ 可取消」这
// 一方向（Task 10 重审 D1）。既有的 TestGoBackend(Put)CancelDuringCommittedFinalFrameReturnsFalse
// 只钉了「committed ⇒ Cancel=false」；本用例把提交**阻塞在 part→target 这一步**（此时尚未提交
// 成功、committed 仍为 false），在窗口里取消：Cancel 必须诚实返回 true，随后传输失败、
// 最终目标名不存在（绝不出现「答应用户取消，文件却落地」）。
func TestGoBackendPutCancelDuringBlockedCommitReturnsTrueAndFails(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("k"), 64<<10)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	const target = "dst.bin"
	// 刻意不预置目标：无论 commitRemote 走 posix-rename 还是 backup-swap，取消后目标名都不得出现。
	g := backendForTestServer(t, remoteRoot)
	entered := make(chan struct{})
	hold := make(chan struct{})
	var once sync.Once
	block := func(oldname, newname string) {
		// 只卡「part → target」这一步：提交尚未成功，committed 仍为 false。
		if IsInternalTemp(filepath.Base(oldname)) && newname == target {
			once.Do(func() {
				close(entered)
				<-hold
			})
		}
	}
	// 两个提交原语都挂上钩子：pkg/sftp 的服务端会宣告 posix-rename（sftp.go 的扩展列表），
	// 但真实远端未必，两边都覆盖才能在任何分支下钉住同一条不变量。
	var origPosix func(*sftp.Client, string, string) error
	var origRename func(*sftp.Client, string, string) error
	origPosix = posixRename
	swapPosixRename(t, func(c *sftp.Client, oldname, newname string) error {
		block(oldname, newname)
		return origPosix(c, oldname, newname)
	})
	origRename = swapRenameRemote(t, func(c *sftp.Client, oldname, newname string) error {
		block(oldname, newname)
		return origRename(c, oldname, newname)
	})

	done := make(chan error, 1)
	go func() {
		done <- g.Put(TransferRequest{ID: "t-blocked", Host: "h", Remote: target, Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未进入提交窗口（part→target 未被调用）")
	}
	if !g.Cancel("t-blocked") {
		t.Fatal("提交尚未成功时 Cancel 必须诚实返回 true（!committed ⇒ 可取消）")
	}
	close(hold)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("提交窗口里被取消的传输必须失败")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 10s 内 Put 未返回")
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, target)); !os.IsNotExist(serr) {
		t.Fatalf("取消后最终目标名不得存在，stat err=%v", serr)
	}
	if g.Cancel("t-blocked") {
		t.Fatal("第二次取消必须返回 false（幂等）")
	}
}
