package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// —— Task 12 hermetic 目录传输测试 ——
// 与 Task 7/8 同样的模式：net.Pipe 接 pkg/sftp 的**真实客户端 + 真实服务端**
// （backendForTestServer），不联网、不起 ssh，但走真实 SFTP 协议。
//
// 目录传输的实现要点（D8/D11/D16）：
//   - 一个会话贯穿整棵树（不像逐文件调 Get/Put 那样每文件重新握手）；
//   - 逐文件复用单文件的 .part + 提交机制（下载 rename，上传 commitRemote）；
//   - 目录项只重试、不续传（P3.1）：req.Resume/req.PartPath 一律忽略；
//   - 枚举超 20000 文件 / 5s ⇒ 降级为 Total=-1 && FilesTotal=-1 && Phase=transfer。

// makeLocalTree 在 root 下造一棵本地树。
func makeLocalTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// writeRemoteTree 在远端根目录下按相对路径造文件（自动建父目录）。
func writeRemoteTree(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatalf("写远端文件: %v", err)
	}
}

// treeParts 返回 dir 树下所有内部临时文件（.part/.bak）的路径。
func treeParts(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && IsInternalTemp(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestGoBackendGetTreeHappyPathCommitsPerFile：目录下载 happy path。
// 断言：嵌套目录逐字节一致、每文件原子提交、无 .part 残留、FilesDone/FilesTotal 真实闭环
// （绝不能像单文件那样恒 0 —— UI 会把 0 读成「0 个文件」）。
func TestGoBackendGetTreeHappyPathCommitsPerFile(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	one := bytes.Repeat([]byte("one-"), 300)
	two := bytes.Repeat([]byte("two-"), 700)
	writeRemoteTree(t, remoteRoot, "src/one.txt", one)
	writeRemoteTree(t, remoteRoot, "src/sub/two.bin", two)

	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	req := TransferRequest{ID: "t12-get", Host: "h", Remote: "src", Local: dst, Atomic: true}
	if err := g.GetTree(req, sink.report); err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	for rel, want := range map[string][]byte{"one.txt": one, "sub/two.bin": two} {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("下载内容不一致 %s: err=%v got=%d want=%d", rel, err, len(got), len(want))
		}
	}
	if parts := treeParts(t, dst); len(parts) != 0 {
		t.Fatalf("成功后不得残留 .part: %v", parts)
	}

	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("目录传输必须有进度帧")
	}
	sum := int64(len(one) + len(two))
	first := frames[0]
	if first.Phase != PhaseTransfer || first.Total != sum || first.FilesTotal != 2 || first.Done != 0 {
		t.Fatalf("首帧应给出完整分母（Total=%d/FilesTotal=2/Phase=transfer），got %+v", sum, first)
	}
	last := frames[len(frames)-1]
	if last.Done != sum || last.FilesDone != 2 || last.FilesTotal != 2 || last.Phase != PhaseTransfer {
		t.Fatalf("末帧应闭环（Done=%d/FilesDone=2/FilesTotal=2），got %+v", sum, last)
	}
	if last.ID != "t12-get" || last.Host != "h" || last.Direction != DirDownload {
		t.Fatalf("末帧字段不完整: %+v", last)
	}
	for _, p := range frames {
		if p.Phase != PhaseTransfer {
			t.Fatalf("目录传输帧必须都是 transfer 相，got %q", p.Phase)
		}
		if p.FilesTotal == 0 {
			t.Fatalf("FilesTotal 绝不能是 0（UI 会读成 0 个文件）: %+v", p)
		}
	}
}

// TestGoBackendGetTreePartialFailureStopsAndKeepsPart：某一个文件短传（done!=total）时，
// 整项必须失败并中止后续文件（目录项失败 = 重试整项，D10/§8），已提交文件保留、
// 失败项的 .part 保留作重试锚点，绝不把不完整文件提交成最终名。
func TestGoBackendGetTreePartialFailureStopsAndKeepsPart(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/a.bin", bytes.Repeat([]byte("a"), 100))
	writeRemoteTree(t, remoteRoot, "src/b.bin", bytes.Repeat([]byte("b"), 200))
	writeRemoteTree(t, remoteRoot, "src/c.bin", bytes.Repeat([]byte("c"), 300))

	g := backendForTestServer(t, remoteRoot)
	calls := 0
	swapCopyStream(t, func(dstW io.Writer, src io.Reader) (int64, error) {
		calls++
		if calls == 2 {
			// 第 2 个文件只搬 5 字节后正常 EOF：库对 EOF 返回 (n,nil)，只能靠 done!=total 兜住。
			return io.Copy(dstW, io.LimitReader(src, 5))
		}
		return io.Copy(dstW, src)
	})

	var te *TransferError
	err := g.GetTree(TransferRequest{ID: "t12-part", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	if err == nil {
		t.Fatal("短传必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if calls != 2 {
		t.Fatalf("失败后必须中止后续文件，copyStream 只应被调 2 次，got %d", calls)
	}
	if te.PartPath == "" || !IsInternalTemp(filepath.Base(te.PartPath)) {
		t.Fatalf("失败时必须保留 .part 作重试锚点，got %q", te.PartPath)
	}
	if _, serr := os.Stat(te.PartPath); serr != nil {
		t.Fatalf("PartPath 指向的 .part 必须真实存在: %v", serr)
	}
	committed := 0
	_ = filepath.WalkDir(dst, func(p string, d os.DirEntry, werr error) error {
		if werr == nil && !d.IsDir() && !IsInternalTemp(d.Name()) {
			committed++
		}
		return nil
	})
	if committed != 1 {
		t.Fatalf("失败时应恰好提交 1 个文件（另一个中止），got %d", committed)
	}
	if parts := treeParts(t, dst); len(parts) != 1 {
		t.Fatalf("失败时应恰好保留 1 个 .part，got %v", parts)
	}
}

// TestGoBackendGetTreeDegradesWhenScanLimitHit（D16）：枚举阈值命中时必须转不定进度
// （Total=-1 && FilesTotal=-1），但**继续传输**已枚举到的文件，FilesDone 仍真实累加。
// 用 scanLimit 注入点把「超 2 万文件/5s」变成确定性，并断言注入点真的被调用（防假覆盖）。
func TestGoBackendGetTreeDegradesWhenScanLimitHit(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	var want int64
	for _, n := range []string{"x.bin", "y.bin", "z.bin"} {
		data := bytes.Repeat([]byte(n), 128)
		writeRemoteTree(t, remoteRoot, "src/"+n, data)
		want += int64(len(data))
	}

	called := false
	orig := scanLimit
	scanLimit = func(files int, elapsed time.Duration) bool {
		called = true
		return true
	}
	t.Cleanup(func() { scanLimit = orig })

	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	if err := g.GetTree(TransferRequest{ID: "t12-deg", Host: "h", Remote: "src", Local: dst, Atomic: true}, sink.report); err != nil {
		t.Fatalf("降级只影响进度字段，传输仍须成功: %v", err)
	}
	if !called {
		t.Fatal("scanLimit 注入点未被调用：降级集成路径是假覆盖")
	}
	for _, n := range []string{"x.bin", "y.bin", "z.bin"} {
		if _, err := os.Stat(filepath.Join(dst, n)); err != nil {
			t.Fatalf("降级后已枚举到的文件仍必须传完: %v", err)
		}
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("降级也必须有进度帧")
	}
	first := frames[0]
	if first.Total != -1 || first.FilesTotal != -1 || first.Phase != PhaseTransfer {
		t.Fatalf("降级首帧必须 Total=-1/FilesTotal=-1/Phase=transfer, got %+v", first)
	}
	last := frames[len(frames)-1]
	if last.Total != -1 || last.FilesTotal != -1 || last.FilesDone != 3 || last.Done != want {
		t.Fatalf("降级末帧应保留真实 Done/FilesDone 但分母未知, got %+v", last)
	}
	for _, p := range frames {
		if p.FilesTotal > 0 || p.Total > 0 {
			t.Fatalf("降级路径不得出现正分母（UI 会误以为枚举完整）: %+v", p)
		}
	}
}

// TestGoBackendGetTreeCancelKeepsPartAndStops：取消目录传输 = 关掉该传输会话（Task 10）。
// 必须让在飞文件立刻失败、整项返回错误、不提交该文件、.part 保留，且二次取消诚实返回 false。
// 用 blockAfterFirstChunk 把「取消发生在传输进行中」变成确定性（不靠 sleep，也不依赖大文件
// 恰好传得慢）。
func TestGoBackendGetTreeCancelKeepsPartAndStops(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/big.bin", bytes.Repeat([]byte("x"), 1<<20))

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockAfterFirstChunk(t)
	done := make(chan error, 1)
	go func() {
		done <- g.GetTree(TransferRequest{ID: "t12-cancel", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块（会话/协议卡死）")
	}
	if !g.Cancel("t12-cancel") {
		t.Fatal("取消在飞目录传输必须返回 true")
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("被取消的目录传输必须返回错误")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 15s 内 GetTree 未返回")
	}
	if _, serr := os.Stat(filepath.Join(dst, "big.bin")); !os.IsNotExist(serr) {
		t.Fatalf("取消后最终名不得存在，stat err=%v", serr)
	}
	if parts := treeParts(t, dst); len(parts) != 1 {
		t.Fatalf("取消后必须保留恰好一个 .part 作重试锚点，got %v", parts)
	}
	if g.Cancel("t12-cancel") {
		t.Fatal("取消必须幂等：已结束的 id 返回 false")
	}
}

// TestGoBackendPutTreeMergesWithoutNesting：目录上传 happy path + D11 合并语义。
// 远端建 <remoteDir>/<base(local)>；第二次上传并入同一目录（同名覆盖），绝不嵌套出 a/a，
// 也不清理远端独有的旧文件；成功后无 .part/.bak 残留。
func TestGoBackendPutTreeMergesWithoutNesting(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/x.txt": "X1", "a/b/y.txt": "Y1"})

	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	put := func(id string) {
		t.Helper()
		req := TransferRequest{ID: id, Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}
		if err := g.PutTree(req, sink.report); err != nil {
			t.Fatalf("PutTree(%s): %v", id, err)
		}
	}
	put("t12-put1")
	if b, err := os.ReadFile(filepath.Join(remoteRoot, "a", "x.txt")); err != nil || string(b) != "X1" {
		t.Fatalf("远端 a/x.txt 应存在且内容正确: err=%v content=%q", err, string(b))
	}
	if b, err := os.ReadFile(filepath.Join(remoteRoot, "a", "b", "y.txt")); err != nil || string(b) != "Y1" {
		t.Fatalf("远端 a/b/y.txt 应存在且内容正确: err=%v content=%q", err, string(b))
	}

	// 第二次：改本地同名文件、远端预置一个独有文件，验证「并入 + 覆盖 + 不删除远端独有」。
	if err := os.WriteFile(filepath.Join(src, "a", "x.txt"), []byte("X2"), 0600); err != nil {
		t.Fatal(err)
	}
	writeRemoteTree(t, remoteRoot, "a/keep.txt", []byte("KEEP"))
	put("t12-put2")
	if b, _ := os.ReadFile(filepath.Join(remoteRoot, "a", "x.txt")); string(b) != "X2" {
		t.Fatalf("并入时同名文件必须被本地内容覆盖, got %q", string(b))
	}
	if b, err := os.ReadFile(filepath.Join(remoteRoot, "a", "keep.txt")); err != nil || string(b) != "KEEP" {
		t.Fatalf("并入不得清理远端独有文件: err=%v content=%q", err, string(b))
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, "a", "a")); !os.IsNotExist(serr) {
		t.Fatalf("第二次上传绝不嵌套出 a/a, stat err=%v", serr)
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 0 {
		t.Fatalf("成功后不得残留 .part/.bak: %v", parts)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("目录上传必须有进度帧")
	}
	last := frames[len(frames)-1]
	if last.Direction != DirUpload || last.FilesDone != 2 || last.FilesTotal != 2 {
		t.Fatalf("上传末帧字段不完整: %+v", last)
	}
}

// TestGoBackendPutTreePartialFailureKeepsPartAndStops：上传方向某文件短传时必须中止整项、
// 不 commit、远端 .part 保留作重试锚点。
func TestGoBackendPutTreePartialFailureKeepsPartAndStops(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/a.bin": "aaaa", "a/b.bin": "bbbb", "a/c.bin": "cccc"})

	g := backendForTestServer(t, remoteRoot)
	calls := 0
	swapCopyStream(t, func(dstW io.Writer, srcR io.Reader) (int64, error) {
		calls++
		if calls == 2 {
			return io.Copy(dstW, io.LimitReader(srcR, 2))
		}
		return io.Copy(dstW, srcR)
	})

	var te *TransferError
	err := g.PutTree(TransferRequest{ID: "t12-putpart", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, nil)
	if err == nil {
		t.Fatal("短传必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if calls != 2 {
		t.Fatalf("失败后必须中止后续文件，copyStream 只应被调 2 次，got %d", calls)
	}
	if te.PartPath == "" || !IsInternalTemp(filepath.Base(te.PartPath)) {
		t.Fatalf("失败时必须保留远端 .part 作重试锚点，got %q", te.PartPath)
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, te.PartPath)); serr != nil {
		t.Fatalf("PartPath 指向的远端 .part 必须真实存在: %v", serr)
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 1 {
		t.Fatalf("失败时应恰好保留 1 个远端 .part，got %v", parts)
	}
}

// TestGoBackendTreesNonAtomicDirectWrite：legacy 面（Atomic=false，Ctrl.GetRecursive/
// PutRecursive 在 gosftp 后端下就是这条路径）必须直写目标、不建我方 .part、不做提交。
func TestGoBackendTreesNonAtomicDirectWrite(t *testing.T) {
	remoteRoot, dst, src := t.TempDir(), t.TempDir(), t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/one.txt", []byte("ONE"))
	makeLocalTree(t, src, map[string]string{"a/two.txt": "TWO"})

	g := backendForTestServer(t, remoteRoot)
	if err := g.GetTree(TransferRequest{ID: "legacy-get", Host: "h", Remote: "src", Local: dst, Atomic: false}, nil); err != nil {
		t.Fatalf("GetTree(Atomic=false): %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "one.txt")); err != nil || string(b) != "ONE" {
		t.Fatalf("legacy 下载内容不一致: err=%v content=%q", err, string(b))
	}
	if parts := treeParts(t, dst); len(parts) != 0 {
		t.Fatalf("legacy 下载不得建我方 .part: %v", parts)
	}
	if err := g.PutTree(TransferRequest{ID: "legacy-put", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: false}, nil); err != nil {
		t.Fatalf("PutTree(Atomic=false): %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(remoteRoot, "a", "two.txt")); err != nil || string(b) != "TWO" {
		t.Fatalf("legacy 上传内容不一致: err=%v content=%q", err, string(b))
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 0 {
		t.Fatalf("legacy 上传不得建我方 .part: %v", parts)
	}
}

// TestGoBackendTreeEmptyDirs：空目录也必须出现（D11「目录本身按合并语义创建」，
// 与 sftp get -r / put -r 一致）——递归传输只搬文件时最容易漏掉空目录。
func TestGoBackendTreeEmptyDirs(t *testing.T) {
	remoteRoot, dst, src := t.TempDir(), t.TempDir(), t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/one.txt", []byte("ONE"))
	if err := os.MkdirAll(filepath.Join(remoteRoot, "src", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeLocalTree(t, src, map[string]string{"a/two.txt": "TWO"})
	if err := os.MkdirAll(filepath.Join(src, "a", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	if err := g.GetTree(TransferRequest{ID: "dirs-get", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil); err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dst, "empty")); err != nil || !st.IsDir() {
		t.Fatalf("下载必须先建出空目录: err=%v st=%v", err, st)
	}
	if err := g.PutTree(TransferRequest{ID: "dirs-put", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, nil); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	if st, err := os.Stat(filepath.Join(remoteRoot, "a", "empty")); err != nil || !st.IsDir() {
		t.Fatalf("上传必须先建出远端空目录: err=%v st=%v", err, st)
	}
}

// TestGoBackendPutTreeFollowsLocalRootSymlink：本地源目录本身是软链时也必须能上传。
// filepath.WalkDir 不跟随根软链（会把它当非目录项，一个文件都枚举不到），若不像
// scanLocalTree 那样先 EvalSymlinks，就会「成功」传出一个空目录 —— 静默丢数据。
func TestGoBackendPutTreeFollowsLocalRootSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建软链需要额外权限")
	}
	remoteRoot, src, linkDir := t.TempDir(), t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"real/one.txt": "ONE"})
	link := filepath.Join(linkDir, "link")
	if err := os.Symlink(filepath.Join(src, "real"), link); err != nil {
		t.Skipf("无法创建软链: %v", err)
	}
	g := backendForTestServer(t, remoteRoot)
	if err := g.PutTree(TransferRequest{ID: "symroot", Host: "h", Remote: ".", Local: link, Atomic: true}, nil); err != nil {
		t.Fatalf("PutTree(软链根): %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(remoteRoot, "link", "one.txt")); err != nil || string(b) != "ONE" {
		t.Fatalf("软链根下的文件必须被上传: err=%v content=%q", err, string(b))
	}
}

// —— Task 12 修复轮 1 新增用例（I1/I2/I3/I4/M1/M2）——

// blockScanCheckpoint 把「扫描检查点」换成「首次进入即卡住，直到 release」的实现，
// 让「取消发生在扫描相」成为确定性（不靠 sleep，也不需要造一个恰好很慢的目录）。
// release 幂等并挂在 t.Cleanup 上：用例中途失败也不会把扫描 goroutine 永久悬挂。
func blockScanCheckpoint(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	var relOnce sync.Once
	orig := scanCheckpoint
	scanCheckpoint = func() {
		once.Do(func() {
			close(enteredCh)
			<-releaseCh
		})
	}
	release = func() { relOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(func() {
		release()
		scanCheckpoint = orig
	})
	return enteredCh, release
}

// TestGoBackendGetTreeCancelDuringScanAborts（I1）：GetTree 必须把真实可取消的 ctx 传进
// scanTree（而不是 context.Background()），并先登记再扫描 —— Cancel(id) 触发 cancel 后，
// 卡在扫描检查点的整项传输必须立刻以 context.Canceled 中止、不提交任何文件，并发额度仍由
// 原传输的 defer Release 恰好归还一次。
func TestGoBackendGetTreeCancelDuringScanAborts(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/a.bin", []byte("AAAA"))

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockScanCheckpoint(t)
	done := make(chan error, 1)
	go func() {
		done <- g.GetTree(TransferRequest{ID: "t12-scan-get", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未进入扫描检查点")
	}
	if got := len(g.pool.queue); got != 0 {
		t.Fatalf("扫描相必须仍占用并发额度，queue=%d want 0", got)
	}
	if !g.Cancel("t12-scan-get") {
		t.Fatal("扫描相取消必须命中登记条目并返回 true（否则用户取消被静默吞掉）")
	}
	release()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 15s 内 GetTree 未返回")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("扫描相取消必须把 context.Canceled 透出（证明 scanTree 的 ctx 检查是活的），got %v", err)
	}
	if entries, rerr := os.ReadDir(dst); rerr != nil {
		t.Fatalf("本地目标目录必须可读: %v", rerr)
	} else if len(entries) != 0 {
		t.Fatalf("扫描相取消后不得提交任何文件，got %v", entries)
	}
	if g.Cancel("t12-scan-get") {
		t.Fatal("扫描相取消必须幂等：第二次返回 false")
	}
	if n := g.regSize(); n != 0 {
		t.Fatalf("取消后注册表必须清空，got %d 条", n)
	}
	if got := len(g.pool.queue); got != 1 {
		t.Fatalf("取消后额度必须恰好归还一次（Cancel 绝不自己归还），queue=%d want 1", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s2, aerr := g.pool.AcquireTransfer(ctx, "h", "")
	if aerr != nil {
		t.Fatalf("取消后额度必须立即可用: %v", aerr)
	}
	g.pool.Release(s2, false)
}

// TestGoBackendPutTreeCancelDuringScanAborts（I1 上传方向）：PutTree 必须**先取会话并登记**
// 再做本地枚举 —— 本地 WalkDir 窗口内 Cancel(id) 必须命中并让扫描以 context.Canceled 中止，
// 不建远端目录、不产生任何 .part，额度恰好归还一次。
func TestGoBackendPutTreeCancelDuringScanAborts(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/one.bin": "ONE"})

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockScanCheckpoint(t)
	done := make(chan error, 1)
	go func() {
		done <- g.PutTree(TransferRequest{ID: "t12-scan-put", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未进入本地扫描检查点")
	}
	if got := len(g.pool.queue); got != 0 {
		t.Fatalf("扫描相必须仍占用并发额度，queue=%d want 0", got)
	}
	// 关键断言：登记必须早于本地枚举 —— 否则这里返回 false，用户的取消被静默吞掉。
	if !g.Cancel("t12-scan-put") {
		t.Fatal("PutTree 在本地枚举窗口内必须已登记：Cancel=false 即取消被吞（I1）")
	}
	release()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 15s 内 PutTree 未返回")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("扫描相取消必须把 context.Canceled 透出（证明 scanLocalTree 的 ctx 检查是活的），got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, "a")); !os.IsNotExist(serr) {
		t.Fatalf("扫描相取消后不得建出远端目录/文件，stat err=%v", serr)
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 0 {
		t.Fatalf("扫描相取消后不得产生任何远端 .part，got %v", parts)
	}
	if g.Cancel("t12-scan-put") {
		t.Fatal("扫描相取消必须幂等：第二次返回 false")
	}
	if got := len(g.pool.queue); got != 1 {
		t.Fatalf("取消后额度必须恰好归还一次，queue=%d want 1", got)
	}
}

// TestGoBackendGetTreeIgnoresSinglePartPathAndResume（I2）：目录项只重试、不续传 ——
// 调用方即使带了单值 PartPath/ResumeOffset，整棵树也**绝不能**把它当每条目的 .part：
// 否则所有条目共用一个 .part、互相覆盖后各自 rename 出错误内容（静默损坏）。
// 这里在 PartPath 位置放一个哨兵文件：正确实现必须原封不动地留着它（逐文件另生成 .part）。
func TestGoBackendGetTreeIgnoresSinglePartPathAndResume(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	one := bytes.Repeat([]byte("one-"), 128)
	two := bytes.Repeat([]byte("two-"), 200)
	writeRemoteTree(t, remoteRoot, "src/one.txt", one)
	writeRemoteTree(t, remoteRoot, "src/sub/two.txt", two)

	sentinel := filepath.Join(dst, "caller-provided-anchor.bin")
	if err := os.WriteFile(sentinel, []byte("SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "t12-onepart", Host: "h", Remote: "src", Local: dst, Atomic: true,
		Resume: true, ResumeOffset: 4, PartPath: sentinel}
	if err := g.GetTree(req, nil); err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "SENTINEL" {
		t.Fatalf("目录传输复用了调用方的单值 PartPath：哨兵被覆盖/改名（err=%v content=%q）", err, string(got))
	}
	for rel, want := range map[string][]byte{"one.txt": one, "sub/two.txt": two} {
		b, rerr := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if rerr != nil || !bytes.Equal(b, want) {
			t.Fatalf("内容不一致 %s: err=%v got=%d want=%d", rel, rerr, len(b), len(want))
		}
	}
	for _, f := range mustReadDir(t, dst) {
		if IsInternalTemp(f.Name()) {
			t.Fatalf("成功提交后不得残留逐文件 .part: %s", f.Name())
		}
	}
}

// TestGoBackendPutTreeIgnoresSinglePartPathAndResume 是上传方向的对称契约用例：请求带单值
// PartPath/ResumeOffset 时，每个远端条目必须各自生成 .part，绝不共用一个；调用方给出的
// 远端 PartPath 位置上的既有文件不得被占用/删除。
func TestGoBackendPutTreeIgnoresSinglePartPathAndResume(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/one.txt": "ONE1", "a/two.txt": "TWO2"})
	const sentinel = "caller-anchor.bin"
	writeRemoteTree(t, remoteRoot, sentinel, []byte("SENTINEL"))

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "t12-onepart-up", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true,
		Resume: true, ResumeOffset: 2, PartPath: sentinel}
	if err := g.PutTree(req, nil); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(remoteRoot, sentinel)); err != nil || string(b) != "SENTINEL" {
		t.Fatalf("目录上传复用了调用方的单值 PartPath：哨兵被覆盖/改名（err=%v content=%q）", err, string(b))
	}
	for name, want := range map[string]string{"one.txt": "ONE1", "two.txt": "TWO2"} {
		b, err := os.ReadFile(filepath.Join(remoteRoot, "a", name))
		if err != nil || string(b) != want {
			t.Fatalf("内容不一致 %s: err=%v content=%q", name, err, string(b))
		}
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 0 {
		t.Fatalf("成功提交后不得残留远端 .part: %v", parts)
	}
}

// TestGoBackendPutTreeDegradesByStoppingLocalWalk（I3）：上传方向的 D16 降级必须是**真的**
// 停止枚举（在本地 WalkDir 过程中评估阈值并丢弃剩余条目），而不是走完整棵树后只把计数
// 报成 -1。注入阈值在第 1 个文件后命中：只应上传该文件，且进度分母必须是 -1（降级）。
// 关掉降级标志（变异 A6）会让首帧分母变正、或把剩余文件也传上去 —— 本用例因此变红。
func TestGoBackendPutTreeDegradesByStoppingLocalWalk(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{
		"a/1.txt": "1", "a/2.txt": "2", "a/3.txt": "3", "a/4.txt": "4", "a/5.txt": "5",
	})
	called := false
	orig := scanLimit
	scanLimit = func(files int, elapsed time.Duration) bool {
		called = true
		return files >= 1 // 第一个文件枚举完就命中阈值
	}
	t.Cleanup(func() { scanLimit = orig })

	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	if err := g.PutTree(TransferRequest{ID: "t12-putdeg", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, sink.report); err != nil {
		t.Fatalf("降级只影响进度字段，传输仍须成功: %v", err)
	}
	if !called {
		t.Fatal("scanLimit 注入点未被调用：上传降级是假覆盖")
	}
	committed := 0
	_ = filepath.WalkDir(filepath.Join(remoteRoot, "a"), func(p string, d os.DirEntry, werr error) error {
		if werr == nil && !d.IsDir() && !IsInternalTemp(d.Name()) {
			committed++
		}
		return nil
	})
	if committed != 1 {
		t.Fatalf("降级必须在阈值处停止枚举：只应上传 1 个文件，got %d", committed)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("降级也必须有进度帧")
	}
	first := frames[0]
	if first.Total != -1 || first.FilesTotal != -1 || first.Phase != PhaseTransfer {
		t.Fatalf("降级首帧必须 Total=-1/FilesTotal=-1/Phase=transfer, got %+v", first)
	}
	last := frames[len(frames)-1]
	if last.Total != -1 || last.FilesTotal != -1 || last.FilesDone != 1 || last.Done != 1 {
		t.Fatalf("降级末帧应保留真实 Done/FilesDone 但分母未知, got %+v", last)
	}
	for _, p := range frames {
		if p.Total > 0 || p.FilesTotal > 0 {
			t.Fatalf("上传降级路径不得出现正分母（UI 会误以为枚举完整）: %+v", p)
		}
	}
}

// TestGoBackendGetTreePartialFailureReportsCountsAndFinalFrame（I4）：部分成功后的失败必须
// 诚实：TransferError 带已提交文件数/字节数与剩余文件数，且失败路径也要发一发**末帧**
// （PartPath=保留的重试锚点、Done 只含已提交文件、不含失败项在飞字节）。
func TestGoBackendGetTreePartialFailureReportsCountsAndFinalFrame(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	// 三个等长文件：失败项与已提交项字节数相同，因此无需依赖 readdir 顺序。
	for _, n := range []string{"a.bin", "b.bin", "c.bin"} {
		writeRemoteTree(t, remoteRoot, "src/"+n, bytes.Repeat([]byte("x"), 100))
	}

	g := backendForTestServer(t, remoteRoot)
	calls := 0
	swapCopyStream(t, func(dstW io.Writer, srcR io.Reader) (int64, error) {
		calls++
		if calls == 2 {
			return io.Copy(dstW, io.LimitReader(srcR, 5))
		}
		return io.Copy(dstW, srcR)
	})

	sink := &progressSink{}
	var te *TransferError
	err := g.GetTree(TransferRequest{ID: "t12-i4-get", Host: "h", Remote: "src", Local: dst, Atomic: true}, sink.report)
	if err == nil {
		t.Fatal("短传必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.CommittedFiles != 1 || te.CommittedBytes != 100 || te.RemainingFiles != 2 {
		t.Fatalf("失败错误必须报诚实计数（1 个已提交/100 字节/2 个剩余），got files=%d bytes=%d remaining=%d",
			te.CommittedFiles, te.CommittedBytes, te.RemainingFiles)
	}
	if !te.TreeCounts {
		t.Fatal("目录失败必须标记 TreeCounts，计数才会进入 Error() 文案（I4 生产消费点）")
	}
	if msg := te.Error(); !strings.Contains(msg, "已传输 1 个文件/100 字节") || !strings.Contains(msg, "剩余 2 个文件") {
		t.Fatalf("失败文案必须带诚实计数供 Task 14 渲染，got %q", msg)
	}
	if te.PartPath == "" {
		t.Fatal("失败项必须保留 .part 作重试锚点")
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("失败路径也必须有进度帧")
	}
	last := frames[len(frames)-1]
	if last.PartPath != te.PartPath {
		t.Fatalf("失败末帧必须由失败路径补发并带重试锚点 PartPath=%q，got %+v", te.PartPath, last)
	}
	if last.Done != 100 || last.FilesDone != 1 || last.FilesTotal != 3 || last.Total != 300 {
		t.Fatalf("失败末帧只应计入已提交字节/文件，got %+v", last)
	}
}

// TestGoBackendPutTreePartialFailureReportsCountsAndFinalFrame 是 I4 的上传对称用例。
func TestGoBackendPutTreePartialFailureReportsCountsAndFinalFrame(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/a.bin": "aaaa", "a/b.bin": "bbbb", "a/c.bin": "cccc"})

	g := backendForTestServer(t, remoteRoot)
	calls := 0
	swapCopyStream(t, func(dstW io.Writer, srcR io.Reader) (int64, error) {
		calls++
		if calls == 2 {
			return io.Copy(dstW, io.LimitReader(srcR, 2))
		}
		return io.Copy(dstW, srcR)
	})

	sink := &progressSink{}
	var te *TransferError
	err := g.PutTree(TransferRequest{ID: "t12-i4-put", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, sink.report)
	if err == nil {
		t.Fatal("短传必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.CommittedFiles != 1 || te.CommittedBytes != 4 || te.RemainingFiles != 2 {
		t.Fatalf("失败错误必须报诚实计数（1 个已提交/4 字节/2 个剩余），got files=%d bytes=%d remaining=%d",
			te.CommittedFiles, te.CommittedBytes, te.RemainingFiles)
	}
	if !te.TreeCounts {
		t.Fatal("目录失败必须标记 TreeCounts，计数才会进入 Error() 文案（I4 生产消费点）")
	}
	if msg := te.Error(); !strings.Contains(msg, "已传输 1 个文件/4 字节") || !strings.Contains(msg, "剩余 2 个文件") {
		t.Fatalf("失败文案必须带诚实计数供 Task 14 渲染，got %q", msg)
	}
	if te.PartPath == "" {
		t.Fatal("失败项必须保留远端 .part 作重试锚点")
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("失败路径也必须有进度帧")
	}
	last := frames[len(frames)-1]
	if last.PartPath != te.PartPath {
		t.Fatalf("失败末帧必须带重试锚点 PartPath=%q，got %+v", te.PartPath, last)
	}
	if last.Done != 4 || last.FilesDone != 1 || last.FilesTotal != 3 || last.Total != 12 {
		t.Fatalf("失败末帧只应计入已提交字节/文件，got %+v", last)
	}
}

// TestScanTreeSkipsInternalTempAndDoesNotFollowDirSymlinks（M1）：远端枚举必须
// ①跳过名字含 PartMarker 的内部临时/备份文件（D18），②不递归目录软链（只当普通项，
// 绝不进入其子树）。两个不变量各自被变异 A3/A7 移除后会分别让断言失败。
func TestScanTreeSkipsInternalTempAndDoesNotFollowDirSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建软链需要额外权限")
	}
	remoteRoot := t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/real/keep.txt", []byte("KEEP"))
	// 内部临时/备份文件：不得进业务清单（否则上次中断的 .part 会被当真实文件再传）。
	writeRemoteTree(t, remoteRoot, "src/junk"+PartMarker+"dead.bin", []byte("JUNK"))
	if err := os.MkdirAll(filepath.Join(remoteRoot, "src", "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 目录软链：readdir 用 lstat 语义把它当非目录项，绝不递归进 real。
	if err := os.Symlink(filepath.Join(remoteRoot, "src", "real"), filepath.Join(remoteRoot, "src", "linkdir")); err != nil {
		t.Skipf("无法创建软链: %v", err)
	}

	g := backendForTestServer(t, remoteRoot)
	s, err := g.pool.AcquireTransfer(context.Background(), "h", "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.pool.Release(s, false)

	files, subdirs, degraded, err := scanTree(context.Background(), s, "src", time.Now())
	if err != nil {
		t.Fatalf("scanTree: %v", err)
	}
	if degraded {
		t.Fatal("小目录不应降级")
	}
	fileSet := map[string]bool{}
	for _, f := range files {
		fileSet[f.rel] = true
		if IsInternalTemp(f.rel) {
			t.Fatalf("内部临时文件不得进清单: %s", f.rel)
		}
	}
	if !fileSet["real/keep.txt"] {
		t.Fatalf("真实文件必须被枚举到，got %v", fileSet)
	}
	for rel := range fileSet {
		if strings.HasPrefix(rel, "linkdir/") {
			t.Fatalf("目录软链子树不得进清单: %s", rel)
		}
	}
	subSet := map[string]bool{}
	for _, d := range subdirs {
		subSet[d] = true
	}
	if subSet["linkdir"] {
		t.Fatal("目录软链不得作为子目录入队（否则会被递归下载/上传）")
	}
	if !subSet["real"] || !subSet["emptydir"] {
		t.Fatalf("真实子目录（含空目录）必须被枚举到，got %v", subSet)
	}
}

// TestScanLocalTreeSkipsInternalTempAndDoesNotFollowDirSymlink 是本地枚举的 M1 对称用例。
func TestScanLocalTreeSkipsInternalTempAndDoesNotFollowDirSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建软链需要额外权限")
	}
	root := t.TempDir()
	makeLocalTree(t, root, map[string]string{
		"real/keep.txt":               "KEEP",
		"junk" + PartMarker + "x.bin": "JUNK",
	})
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("无法创建软链: %v", err)
	}
	files, subdirs, total, degraded, err := scanLocalTree(context.Background(), root, time.Now())
	if err != nil {
		t.Fatalf("scanLocalTree: %v", err)
	}
	if degraded {
		t.Fatal("小目录不应降级")
	}
	if total != 4 {
		t.Fatalf("total 只应统计真实文件（4 字节），got %d", total)
	}
	rels := map[string]bool{}
	for _, f := range files {
		rels[f.rel] = true
		if IsInternalTemp(f.rel) {
			t.Fatalf("内部临时文件不得进清单: %s", f.rel)
		}
	}
	if !rels["real/keep.txt"] {
		t.Fatalf("真实文件必须被枚举到，got %v", rels)
	}
	for rel := range rels {
		if strings.HasPrefix(rel, "linkdir/") {
			t.Fatalf("本地目录软链子树不得进清单: %s", rel)
		}
	}
	for _, d := range subdirs {
		if d == "linkdir" {
			t.Fatal("本地目录软链不得作为子目录（否则会被递归上传）")
		}
	}
}

// TestTreeProgressReconcilesDivergentSources（M2/NEW-2）：分母来自枚举、分子来自**已打开
// 句柄**的 Stat —— 源在枚举与打开之间变大时，分叉帧必须立刻强制上报并被夹取兜住（安全网），
// 而 finishFile 会把分母对账到实际值，末帧 Done==Total 且不再静默少报。旧版只有夹取，末帧
// 会把多出来的字节藏成 Done==枚举分母（本用例的末帧断言会因此变红）。
func TestTreeProgressReconcilesDivergentSources(t *testing.T) {
	var frames []Progress
	tp := newTreeProgress("id", "h", DirDownload, "src", 100, 2, func(p Progress) { frames = append(frames, p) })
	tp.begin()
	// 分叉：当前文件实际已传 150 字节 > 枚举分母 100。
	tp.file(Progress{Done: 150})
	if len(frames) < 2 {
		t.Fatalf("分叉必须立刻强制上报一帧（否则夹取根本不会被观测到），got %d 帧", len(frames))
	}
	if got := frames[len(frames)-1]; got.Done != 100 || got.Total != 100 {
		t.Fatalf("分叉帧必须被夹取兜住（Done<=Total），got %+v", got)
	}
	// 提交后对账：分母跟随实际大小，末帧自洽且不少报。
	tp.finishFile(100, 150)
	tp.frame(true)
	last := frames[len(frames)-1]
	if last.Total != 150 || last.Done != 150 {
		t.Fatalf("末帧分母必须对账到实际大小（Done=Total=150），got %+v", last)
	}
	for _, p := range frames {
		if p.Total >= 0 && p.Done > p.Total {
			t.Fatalf("任何一帧都不得 Done>Total, got %+v", p)
		}
	}

	// FilesDone 夹取仍然有效（提交数超过枚举文件数）。
	frames = nil
	tp2 := newTreeProgress("id2", "h", DirUpload, "src", 30, 2, func(p Progress) { frames = append(frames, p) })
	tp2.begin()
	tp2.finishFile(10, 10)
	tp2.finishFile(10, 10)
	tp2.finishFile(10, 10) // 3 个文件 > FilesTotal=2
	tp2.frame(true)
	last2 := frames[len(frames)-1]
	if last2.FilesDone != 2 || last2.Done > last2.Total {
		t.Fatalf("FilesDone 必须夹到 FilesTotal=2 且 Done<=Total, got %+v", last2)
	}
}

// —— Task 12 修复轮 2 新增用例（NEW-1 / NEW-2 / NEW-3 / I2 / I4 / 缺失的取消用例）——

// TestScanLocalTreeDirectoryOnlyHonorsElapsedBudget（NEW-3）：D16 的预算判定必须对目录也生效。
// 旧实现只在**追加文件之后**检查阈值，于是一棵只有目录、没有文件的树永远碰不到 5s 预算，
// 可以无限期持有并发令牌与传输会话（下载方向的 scanTree 是按目录批检查的）。
// 这里用一个「起点在 2*scanMaxElapsed 之前」的 start 模拟极小预算：走查必须降级并停止。
func TestScanLocalTreeDirectoryOnlyHonorsElapsedBudget(t *testing.T) {
	root := t.TempDir()
	allDirs := 0
	for _, d := range []string{"d1", "d2", "d3", "d4", "d5"} {
		if err := os.MkdirAll(filepath.Join(root, d, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		allDirs += 2
	}
	// 基线：预算充足时，目录-only 树（一个文件都没有）必须被完整枚举。
	files, dirs, _, degraded, err := scanLocalTree(context.Background(), root, time.Now())
	if err != nil {
		t.Fatalf("scanLocalTree: %v", err)
	}
	if degraded {
		t.Fatal("预算充足时目录-only 树不应降级")
	}
	if len(files) != 0 {
		t.Fatalf("目录-only 树不应枚举出文件，got %v", files)
	}
	if len(dirs) != allDirs {
		t.Fatalf("预算充足时应完整枚举 %d 个目录，got %d (%v)", allDirs, len(dirs), dirs)
	}

	// 预算早已耗尽（等价于极小预算）：即使一个文件都没有，也必须停止走查并降级。
	_, dirs2, _, degraded2, err2 := scanLocalTree(context.Background(), root, time.Now().Add(-2*scanMaxElapsed))
	if err2 != nil {
		t.Fatalf("scanLocalTree(耗尽预算): %v", err2)
	}
	if !degraded2 {
		t.Fatal("NEW-3：目录-only 树在预算耗尽后也必须降级并停止枚举（只按文件检查会漏掉这条路径）")
	}
	if len(dirs2) >= allDirs {
		t.Fatalf("枚举未在预算处停止：枚举到 %d/%d 个目录（%v）", len(dirs2), allDirs, dirs2)
	}
}

// TestGoBackendGetTreeScanPhaseFailureEmitsTerminalFrameAndCounts（NEW-1）：远端枚举失败时，
// 必须发出终态进度帧并回填诚实计数（已提交 0、剩余未知），否则 I4 对扫描相路径没有闭合。
func TestGoBackendGetTreeScanPhaseFailureEmitsTerminalFrameAndCounts(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	var te *TransferError
	err := g.GetTree(TransferRequest{ID: "t12-new1-get", Host: "h", Remote: "missing-dir", Local: dst, Atomic: true}, sink.report)
	if err == nil {
		t.Fatal("枚举不存在的远端目录必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if !te.TreeCounts || te.CommittedFiles != 0 || te.CommittedBytes != 0 || te.RemainingFiles != -1 {
		t.Fatalf("扫描相失败必须回填诚实计数（0 个已提交、剩余未知），got %+v", te)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("NEW-1：扫描相失败也必须发末帧，否则调用方无从告诉用户发生了什么")
	}
	last := frames[len(frames)-1]
	if last.Phase != PhaseTransfer || last.Total != -1 || last.FilesTotal != -1 || last.Done != 0 || last.Direction != DirDownload {
		t.Fatalf("扫描相失败末帧必须是「分母未知、0 已传」的 transfer 帧，got %+v", last)
	}
	if msg := te.Error(); !strings.Contains(msg, "已传输 0 个文件/0 字节") || !strings.Contains(msg, "剩余文件数未知") {
		t.Fatalf("错误文案必须带诚实计数供 Task 14 渲染，got %q", msg)
	}
}

// TestGoBackendPutTreeScanPhaseFailureEmitsTerminalFrameAndCounts（NEW-1 上传方向）：
// 本地 WalkDir 中途失败（断链）同样必须发终态帧并回填计数。
func TestGoBackendPutTreeScanPhaseFailureEmitsTerminalFrameAndCounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建软链需要额外权限")
	}
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/ok.bin": "OK"})
	// 断链：WalkDir 枚举到它时 os.Stat 失败 ⇒ scanLocalTree 在扫描相报错。
	if err := os.Symlink(filepath.Join(src, "missing-target"), filepath.Join(src, "a", "broken.link")); err != nil {
		t.Skipf("无法创建软链: %v", err)
	}
	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	var te *TransferError
	err := g.PutTree(TransferRequest{ID: "t12-new1-putfail", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, sink.report)
	if err == nil {
		t.Fatal("本地枚举遇断链必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if !te.TreeCounts || te.CommittedFiles != 0 || te.RemainingFiles != -1 {
		t.Fatalf("扫描相失败必须回填诚实计数（0 个已提交、剩余未知），got %+v", te)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("NEW-1：本地扫描相失败也必须发末帧")
	}
	last := frames[len(frames)-1]
	if last.Phase != PhaseTransfer || last.Total != -1 || last.FilesTotal != -1 || last.Done != 0 || last.Direction != DirUpload {
		t.Fatalf("本地扫描相失败末帧必须是「分母未知、0 已传」的 transfer 帧，got %+v", last)
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, "a")); !os.IsNotExist(serr) {
		t.Fatalf("扫描相失败不得建出远端目录，stat err=%v", serr)
	}
}

// TestGoBackendPutTreeScanPhaseCancelEmitsTerminalFrameAndCounts（NEW-1 取消方向）：
// 扫描相被 Cancel 中止时也必须发终态帧并回填诚实计数（已提交 0、剩余未知）。
func TestGoBackendPutTreeScanPhaseCancelEmitsTerminalFrameAndCounts(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/one.bin": "ONE"})
	g := backendForTestServer(t, remoteRoot)
	entered, release := blockScanCheckpoint(t)
	sink := &progressSink{}
	done := make(chan error, 1)
	go func() {
		done <- g.PutTree(TransferRequest{ID: "t12-new1-putcancel", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, sink.report)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未进入本地扫描检查点")
	}
	if !g.Cancel("t12-new1-putcancel") {
		t.Fatal("扫描相取消必须命中登记条目并返回 true")
	}
	release()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 15s 内 PutTree 未返回")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("扫描相取消必须把 context.Canceled 透出，got %v", err)
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if !te.TreeCounts || te.CommittedFiles != 0 || te.CommittedBytes != 0 || te.RemainingFiles != -1 {
		t.Fatalf("扫描相取消必须回填诚实计数（0 个已提交、剩余未知），got %+v", te)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("NEW-1：扫描相取消也必须发末帧")
	}
	last := frames[len(frames)-1]
	if last.Phase != PhaseTransfer || last.Total != -1 || last.FilesTotal != -1 || last.Done != 0 || last.Direction != DirUpload {
		t.Fatalf("扫描相取消末帧必须是「分母未知、0 已传」的 transfer 帧，got %+v", last)
	}
	if msg := te.Error(); !strings.Contains(msg, "剩余文件数未知") {
		t.Fatalf("错误文案必须带诚实计数供 Task 14 渲染，got %q", msg)
	}
}

// TestGoBackendPutTreeReconcilesFileGrownAfterEnumeration（NEW-2 + M2）：在「枚举完成、共享
// helper 尚未打开本地源」的窗口里把文件改大，构造分母（枚举 100）与分子（已打开句柄 150）
// 的真实分叉。正确实现必须：(a) 分叉帧被夹取兜住（任何帧 Done<=Total）；(b) finishFile 把
// 分母对账到实际值，末帧 Done==Total==150，绝不静默少报成 100。
// 变异「删掉 treeProgress 的夹取」会让分叉帧出现 Done=150 > Total=100，本用例变红；
// 变异「删掉 finishFile 的对账」会让末帧 Total=100，本用例同样变红。
func TestGoBackendPutTreeReconcilesFileGrownAfterEnumeration(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/grow.bin": strings.Repeat("x", 100)})

	g := backendForTestServer(t, remoteRoot)
	grown := 0
	orig := beforeOpenLocalSource
	beforeOpenLocalSource = func(p string) {
		if grown > 0 || !strings.HasSuffix(p, "grow.bin") {
			return
		}
		grown++
		if werr := os.WriteFile(p, []byte(strings.Repeat("x", 150)), 0600); werr != nil {
			t.Errorf("在枚举与打开之间改大源失败: %v", werr)
		}
	}
	t.Cleanup(func() { beforeOpenLocalSource = orig })

	sink := &progressSink{}
	if err := g.PutTree(TransferRequest{ID: "t12-grow", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, sink.report); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	if grown != 1 {
		t.Fatalf("注入点必须被调用恰好一次（否则分叉根本没被构造），got %d", grown)
	}
	// 远端内容必须是改大后的完整 150 字节（提交前置仍以已打开句柄为准）。
	if b, rerr := os.ReadFile(filepath.Join(remoteRoot, "a", "grow.bin")); rerr != nil || len(b) != 150 {
		t.Fatalf("远端内容应为改大后的 150 字节: err=%v len=%d", rerr, len(b))
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("必须有进度帧")
	}
	for _, p := range frames {
		if p.Total >= 0 && p.Done > p.Total {
			t.Fatalf("任何一帧都不得 Done>Total（夹取兜底），got %+v", p)
		}
	}
	last := frames[len(frames)-1]
	if last.Total != 150 || last.Done != 150 {
		t.Fatalf("末帧分母必须对账到实际大小 150（NEW-2/M2），got %+v", last)
	}
}

// TestGoBackendPutTreeUploadClearPreventsSharedPartAnchor（I2 上传侧）：PutTree 必须清空
// 调用方的单值 PartPath/Resume —— putFileAtomic 与下载的 copyFileToLocal 对称，会复用
// req.PartPath（正因如此清空才是真实守卫，而不是死代码）。这里在调用方给的 PartPath 位置
// 放一个哨兵：清空生效则每个文件各自生成 .part、哨兵原封不动；清空被删（b2 变异）则整棵树
// 共用哨兵、互相 rename，哨兵消失 ⇒ 本用例变红。
func TestGoBackendPutTreeUploadClearPreventsSharedPartAnchor(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/one.txt": "ONE1", "a/two.txt": "TWO2", "a/three.txt": "THREE3"})
	const sentinel = "caller-anchor.bin"
	writeRemoteTree(t, remoteRoot, sentinel, []byte("SENTINEL"))

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "t12-i2-up", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true,
		Resume: true, ResumeOffset: 2, PartPath: sentinel}
	if err := g.PutTree(req, nil); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(remoteRoot, sentinel)); err != nil || string(b) != "SENTINEL" {
		t.Fatalf("调用方锚点哨兵必须原封不动（上传侧清空失效会让整棵树共用它）: err=%v content=%q", err, string(b))
	}
	for name, want := range map[string]string{"one.txt": "ONE1", "two.txt": "TWO2", "three.txt": "THREE3"} {
		if b, err := os.ReadFile(filepath.Join(remoteRoot, "a", name)); err != nil || string(b) != want {
			t.Fatalf("内容不一致 %s: err=%v content=%q", name, err, string(b))
		}
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 0 {
		t.Fatalf("成功提交后不得残留远端 .part: %v", parts)
	}
}

// TestGoBackendPutTreeCancelMidTransferKeepsPartAndStops 是评审在仓库外临时验证过的序列，
// 现在必须固化在仓库里：PutTree 传输进行中被 Cancel ⇒ ①取消返回 true、②传输以错误结束、
// ③最终名不存在且恰好保留一个远端 .part、④二次取消 false、⑤注册表清空、⑥并发额度恰好
// 归还一次且立即可再取、⑦同目标去重表清空（该目标不会永久被判为「在传」）。
func TestGoBackendPutTreeCancelMidTransferKeepsPartAndStops(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/big.bin": string(bytes.Repeat([]byte("x"), 1<<20))})

	g := backendForTestServer(t, remoteRoot)
	entered, release := blockAfterFirstChunk(t)
	done := make(chan error, 1)
	go func() {
		done <- g.PutTree(TransferRequest{ID: "t12-putcancel", Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写入首块（会话/协议卡死）")
	}
	if got := len(g.pool.queue); got != 0 {
		t.Fatalf("传输中必须仍占用并发额度，queue=%d want 0", got)
	}
	if !g.Cancel("t12-putcancel") {
		t.Fatal("取消在飞的 PutTree 必须返回 true")
	}
	if g.Cancel("t12-putcancel") {
		t.Fatal("第二次取消必须返回 false（幂等）")
	}
	release()

	var err error
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("被取消的目录上传必须返回错误（取消靠关会话让在飞 IO 立刻失败）")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 15s 内 PutTree 未返回")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.PartPath == "" || !IsInternalTemp(filepath.Base(te.PartPath)) {
		t.Fatalf("取消后必须保留远端 .part 作重试锚点，got %q", te.PartPath)
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, te.PartPath)); serr != nil {
		t.Fatalf("PartPath 指向的远端 .part 必须真实存在: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, "a", "big.bin")); !os.IsNotExist(serr) {
		t.Fatalf("取消后最终名不得存在，stat err=%v", serr)
	}
	if parts := treeParts(t, remoteRoot); len(parts) != 1 {
		t.Fatalf("取消后必须保留恰好一个远端 .part，got %v", parts)
	}
	if n := g.regSize(); n != 0 {
		t.Fatalf("取消后注册表必须清空，got %d 条", n)
	}
	if got := len(g.pool.queue); got != 1 {
		t.Fatalf("取消后额度必须恰好归还一次，queue=%d want 1", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s2, aerr := g.pool.AcquireTransfer(ctx, "h", "")
	if aerr != nil {
		t.Fatalf("取消后额度必须立即可用: %v", aerr)
	}
	g.pool.Release(s2, false)
	if n := g.inflightSize(); n != 0 {
		t.Fatalf("取消后同目标去重表必须清空（否则该目标永久被判为在传），got %d 条", n)
	}
}

// TestTransferErrorTreeCountsReachErrorText（I4 生产消费点）：目录传输的失败计数必须出现在
// Error() 文案里 —— 绑定层把 error 当纯字符串回传前端，Task 14 只能从文案取数。单文件
// （未回填计数）绝不能凭空出现「已传输 0 个、剩余 0 个」。
func TestTransferErrorTreeCountsReachErrorText(t *testing.T) {
	te := &TransferError{Op: "sftp put -r", Path: "dst", Err: errors.New("boom"),
		TreeCounts: true, CommittedFiles: 3, CommittedBytes: 42, RemainingFiles: 7}
	msg := te.Error()
	for _, want := range []string{"已传输 3 个文件/42 字节", "剩余 7 个文件"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Error() 必须包含 %q，got %q", want, msg)
		}
	}
	unknown := &TransferError{Op: "sftp get -r", Path: "src", Err: context.Canceled,
		TreeCounts: true, RemainingFiles: -1}
	if !strings.Contains(unknown.Error(), "剩余文件数未知") {
		t.Fatalf("剩余未知（降级/扫描未完成）必须明说，got %q", unknown.Error())
	}
	single := &TransferError{Op: "sftp put", Path: "a", Err: errors.New("boom")}
	if strings.Contains(single.Error(), "已传输") {
		t.Fatalf("单文件错误不得凭空出现目录计数，got %q", single.Error())
	}
}
