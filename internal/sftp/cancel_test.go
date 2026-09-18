package sftp

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
