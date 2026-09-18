package sftp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
