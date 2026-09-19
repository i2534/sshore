package sftp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/osutil"
)

// —— 本文件是 Task 6 的 hermetic 单测：不联网、不起真实 ssh ——
// 真实握手/能力探测由 internal/sftp/e2e_test.go（双后端 harness）覆盖。

// TestSftpDialArgs 钉住 ssh 参数构造：Task 0 实测采用 **前置 -s** 形式
// （ssh -s <host> sftp），并带上 BatchMode/IdentitiesOnly/ConnectTimeout/ServerAlive*。
func TestSftpDialArgs(t *testing.T) {
	got := sftpDialArgs("myhost", "")
	want := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-s", "myhost", "sftp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("无 user 的参数不对: got %q want %q", got, want)
	}

	withUser := sftpDialArgs("myhost", "alice")
	if !pairPresent(withUser, "-o", "User=alice") {
		t.Fatalf("有 user 时必须带 -o User=alice: %q", withUser)
	}
	// User 选项必须在 -s 之前，且 -s 之后的固定顺序是 host, subsystem。
	if idxOf(withUser, "User=alice") > idxOf(withUser, "-s") {
		t.Fatalf("-o User= 必须在 -s 之前: %q", withUser)
	}
	if n := len(withUser); n < 3 || withUser[n-3] != "-s" || withUser[n-2] != "myhost" || withUser[n-1] != "sftp" {
		t.Fatalf("末尾必须是 -s <host> sftp: %q", withUser)
	}
}

// TestDialUsesSSHAndPropagatesStartError 用注入的 startSFTPPipes 断言：
// dial 起的进程叫 ssh，参数逐字等于 sftpDialArgs，且启动错误原样冒出来。
func TestDialUsesSSHAndPropagatesStartError(t *testing.T) {
	orig := startSFTPPipes
	var gotName string
	var gotArgs []string
	startSFTPPipes = func(name string, args ...string) (*osutil.PipedProcess, error) {
		gotName, gotArgs = name, args
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { startSFTPPipes = orig })

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	if _, err := g.dial("myhost", "alice"); err == nil {
		t.Fatal("dial 必须把 StartPipes 的错误原样返回")
	}
	if gotName != "ssh" {
		t.Fatalf("dial 必须起 ssh，got %q", gotName)
	}
	if !reflect.DeepEqual(gotArgs, sftpDialArgs("myhost", "alice")) {
		t.Fatalf("dial 传给 ssh 的参数不对: got %q want %q", gotArgs, sftpDialArgs("myhost", "alice"))
	}
}

// TestDialWrapsClientPipeError 覆盖 NewClientPipe 失败路径：错误文案带
// 「建立 SFTP 会话失败」前缀，并走 PipedProcess.StderrText()（绝不直接读 Proc.Stderr）。
func TestDialWrapsClientPipeError(t *testing.T) {
	origStart, origPipe := startSFTPPipes, newSFTPClientPipe
	// 用跨平台替身子进程（helper_test.go）：NewClientPipe 被注入为立即失败，因此不会读管道、
	// 不会挂住；dial 随后必须 Close() 掉它。
	startSFTPPipes = func(string, ...string) (*osutil.PipedProcess, error) {
		return testHelperPipes(t, ""), nil
	}
	newSFTPClientPipe = func(io.Reader, io.WriteCloser) (*sftp.Client, error) {
		return nil, errors.New("handshake failed")
	}
	t.Cleanup(func() { startSFTPPipes, newSFTPClientPipe = origStart, origPipe })

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	_, err := g.dial("myhost", "")
	if err == nil {
		t.Fatal("NewClientPipe 失败时 dial 必须返回错误")
	}
	if want := "建立 SFTP 会话失败"; !strings.Contains(err.Error(), want) {
		t.Fatalf("错误应含 %q, got %v", want, err)
	}
}

// TestCapabilitiesPropagatesDialError 钉住池接线：Capabilities 通过 pool.AcquireList
// 走到 g.dial；ssh 起不来时必须把错误冒出来，而不是静默返回 false。
func TestCapabilitiesPropagatesDialError(t *testing.T) {
	orig := startSFTPPipes
	startSFTPPipes = func(string, ...string) (*osutil.PipedProcess, error) {
		return nil, errors.New("ssh not found")
	}
	t.Cleanup(func() { startSFTPPipes = orig })

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	if _, err := g.Capabilities("myhost", ""); err == nil {
		t.Fatal("dial 失败必须从 Capabilities 冒出来")
	}
}

// TestGoBackendAtomicCapable：GoBackend 的新面一律走 .part + 提交（posix-rename 或
// backup-swap），所以它声明支持原子提交 —— 与 BatchBackend 的 false 形成对照（Task 9）。
func TestGoBackendAtomicCapable(t *testing.T) {
	if !NewGoBackend(nil, nil).AtomicCapable() {
		t.Fatal("GoBackend 必须声明 AtomicCapable（.part + 提交）")
	}
}

// TestNewGoBackendWiresPoolAndDeclaredFields 钉住 NewGoBackend 的接线与
// 「字段一次性声明」约定（Task 7-13 只加方法，不再改结构体）。
func TestNewGoBackendWiresPoolAndDeclaredFields(t *testing.T) {
	sel := func() string { return "gosftp" }
	g := NewGoBackend(sel, nil)
	defer g.CloseAll()
	if g.pool == nil {
		t.Fatal("NewGoBackend 必须建池")
	}
	if g.pool.dial == nil {
		t.Fatal("池必须接上 GoBackend.dial")
	}
	if g.sel == nil || g.sel() != "gosftp" {
		t.Fatalf("sel 未保留: %v", g.sel)
	}

	// 用注入的失败 dial 证明池确实调用 g.dial（而不是某个空实现）。
	orig := startSFTPPipes
	startSFTPPipes = func(string, ...string) (*osutil.PipedProcess, error) {
		return nil, errors.New("wired")
	}
	t.Cleanup(func() { startSFTPPipes = orig })
	if _, err := g.pool.AcquireList(context.Background(), "h", ""); err == nil || !strings.Contains(err.Error(), "wired") {
		t.Fatalf("池没有走 GoBackend.dial: %v", err)
	}

	// 后续 task 的容器字段必须已声明且已初始化（M2）；写入路径见
	// TestGoBackendMapsWritableAfterConstruction，这里只钉住非 nil。
	if g.reg == nil || g.inflight == nil || g.knownParts == nil {
		t.Fatalf("构造后 map 字段不得为 nil: reg=%v inflight=%v knownParts=%v", g.reg, g.inflight, g.knownParts)
	}
}

// TestGoBackendMapsWritableAfterConstruction 是 Task 6 评审 M2 的回归测试：
// Task 10/11/13 会直接写 reg/inflight/knownParts，构造后这些 map 必须已可用，
// 不能再由调用方手工兜底 —— 否则迟到的 task 一写就 panic: assignment to entry in nil map。
func TestGoBackendMapsWritableAfterConstruction(t *testing.T) {
	// 本用例只验证 map 可写。CloseAll → CleanupParts 会把登记表里所有 .part 清掉，对非空
	// host 的那条会真的 AcquireList → dial。这里把子进程启动注入成立刻失败：既保持登记项
	// 是真实形状（host/user/远端路径都非空），又不让用例去连一台真实主机 —— 修复前在
	// Windows 真机上 ssh "" 会一直挂到 test timeout（10m）。
	origStart := startSFTPPipes
	startSFTPPipes = func(string, ...string) (*osutil.PipedProcess, error) {
		return nil, errors.New("test: 不起真实 ssh")
	}
	t.Cleanup(func() { startSFTPPipes = origStart })

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("直接写 map 字段触发 panic（M2 未修）: %v", r)
			}
		}()
		g.regMu.Lock()
		g.reg["id"] = &regEntry{}
		g.regMu.Unlock()
		g.inflightMu.Lock()
		g.inflight["id"] = "key"
		g.inflightMu.Unlock()
		g.partsMu.Lock()
		g.knownParts["id"] = [4]string{"h", "u", "/l", "/r"}
		g.partsMu.Unlock()
	}()
	if len(g.reg) != 1 || g.inflight["id"] != "key" || g.knownParts["id"] != [4]string{"h", "u", "/l", "/r"} {
		t.Fatalf("写入后读数不对: reg=%v inflight=%v knownParts=%v", g.reg, g.inflight, g.knownParts)
	}
}

// —— Task 13：Item 形状与时间格式 ——

// TestItemFromFileInfoKeepsContract 钉住 Item 构造契约：ModTime 逐字保持
// 2006-01-02 15:04（watch/sync 用它做字符串比较），零值 mtime 为空串（前端显示 —），
// Mode 必须填（UI 暂不使用，但字段不可空）。
func TestItemFromFileInfoKeepsContract(t *testing.T) {
	it := itemFrom("a.txt", 123, false, 0644, time.Date(2026, 9, 18, 10, 30, 5, 0, time.UTC))
	if it.Name != "a.txt" || it.Size != 123 || it.IsDir {
		t.Fatalf("基本字段错: %#v", it)
	}
	if it.ModTime != "2026-09-18 10:30" {
		t.Fatalf("ModTime 必须逐字保持 2006-01-02 15:04（watch/sync 用它做字符串比较）, got %q", it.ModTime)
	}
	if it.Mode == "" {
		t.Fatal("Mode 必须填（UI 暂不使用，但字段不可空）")
	}
	zero := itemFrom("b.txt", 0, false, 0644, time.Time{})
	if zero.ModTime != "" {
		t.Fatalf("mtime 缺失/为零 → 空串（前端显示 —）, got %q", zero.ModTime)
	}
}

// —— Task 13：其余能力（List/ListMany/Home/Remove/RemoveRecursive/Mkdir/Rename） ——
//
// 全部对着 backendForTestServer（net.Pipe 接 pkg/sftp 真实客户端 + 真实服务端）跑，
// 走真正的 SFTP 协议而不是 mock —— 语义等价（缺失 key、rename 覆盖、递归失败）才有意义。

func TestGoBackendListAndListMany(t *testing.T) {
	root := t.TempDir()
	writeRemote(t, root, "a.txt", []byte("A"))
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "sub/b.txt", []byte("B"))
	g := backendForTestServer(t, root)

	items, err := g.List("h", "", ".")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]Item{}
	for _, it := range items {
		got[it.Name] = it
		if it.Name == "." || it.Name == ".." {
			t.Fatalf("List 必须过滤 . / ..: %#v", it)
		}
	}
	if len(got) != 2 || got["a.txt"].IsDir || !got["sub"].IsDir {
		t.Fatalf("List 结果不对: %#v", items)
	}
	if got["a.txt"].Size != 1 || got["a.txt"].Mode == "" || got["a.txt"].ModTime == "" {
		t.Fatalf("Item 形状不符合契约: %#v", got["a.txt"])
	}

	res, err := g.ListMany("h", "", []string{".", "sub", "does-not-exist"})
	if err != nil {
		t.Fatalf("ListMany: %v", err)
	}
	// 缺失 key = 未知（绝不返回空切片）—— Search 与 sync 都依赖这条契约。
	if _, ok := res["does-not-exist"]; ok {
		t.Fatalf("列不出来的目录必须缺席于返回值: %#v", res)
	}
	if len(res["."]) != 2 || len(res["sub"]) != 1 {
		t.Fatalf("ListMany 内容不对: %#v", res)
	}
	if _, ok := res[""]; ok {
		t.Fatalf("不得出现空 key: %#v", res)
	}
}

func TestGoBackendHome(t *testing.T) {
	// Windows 下上游 pkg/sftp 的**内存服务端**把远端绝对路径（C:\…）当 POSIX 路径拼到
	// 工作目录后（filepath.Abs(toLocalPath)），REALPATH 返回的是 "/C:/…" 这种非法形态，
	// 这不是本产品的缺陷：生产路径是真实 OpenSSH sshd，其 pwd 语义由 e2e 与 Windows
	// 真机验收清单 §3 覆盖（Wails 客户端本身不可能跑在这个内存服务端上）。
	// Home 只是 s.Conn.Getwd() 的一行转发，平台无关。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下 pkg/sftp 内存服务端不支持绝对远端路径（REALPATH 上游限制）")
	}
	root := t.TempDir()
	g := backendForTestServer(t, root)
	home, err := g.Home("h", "")
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	want, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(home)
	if got != want {
		t.Fatalf("Home 必须是远端工作目录: got %q want %q (root=%q)", got, want, root)
	}
}

func TestGoBackendMkdirRemoveAndRenameOverwrite(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)

	if err := g.Mkdir("h", "", "d"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if st, err := os.Stat(filepath.Join(root, "d")); err != nil || !st.IsDir() {
		t.Fatalf("Mkdir 必须真的建出目录: err=%v st=%v", err, st)
	}
	writeRemote(t, root, "d/a.txt", []byte("A"))
	writeRemote(t, root, "d/b.txt", []byte("BBBB"))

	// rename 覆盖已存在目标必须成功（旧 sftp rename 语义）。
	if err := g.Rename("h", "", "d/a.txt", "d/b.txt"); err != nil {
		t.Fatalf("rename 覆盖已存在目标必须成功: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "d", "b.txt"))
	if err != nil || string(b) != "A" {
		t.Fatalf("rename 后目标内容必须是源内容: err=%v content=%q", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "d", "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("rename 后源必须消失, stat err=%v", err)
	}

	// Remove 只删单个文件；目录要用 RemoveRecursive。
	if err := g.Remove("h", "", "d/b.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "d", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("Remove 后文件必须消失, stat err=%v", err)
	}
}

func TestGoBackendRemoveRecursive(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tree", "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "tree/top.txt", []byte("T"))
	writeRemote(t, root, "tree/sub/s.txt", []byte("S"))
	writeRemote(t, root, "tree/sub/deep/d.txt", []byte("D"))

	if err := g.RemoveRecursive("h", "", "/"); err == nil {
		t.Fatal("必须拒绝递归删除根路径")
	}
	if err := g.RemoveRecursive("h", "", ""); err == nil {
		t.Fatal("必须拒绝递归删除空路径")
	}
	// 文件目标：RemoveRecursive 也要能删单个文件（旧界面「删除」对文件走同一条路径）。
	writeRemote(t, root, "lonely.txt", []byte("L"))
	if err := g.RemoveRecursive("h", "", "lonely.txt"); err != nil {
		t.Fatalf("文件目标也必须能删: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lonely.txt")); !os.IsNotExist(err) {
		t.Fatalf("文件目标删除后必须消失, stat err=%v", err)
	}
	// 目录不可读（此处以不存在代替）⇒ 整体失败，绝不静默漏删。
	if err := g.RemoveRecursive("h", "", "does-not-exist"); err == nil {
		t.Fatal("不存在的目录必须整体失败")
	}
	if err := g.RemoveRecursive("h", "", "tree"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Fatalf("RemoveRecursive 后整棵树必须消失, stat err=%v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("root 下不应残留任何条目: %v", entries)
	}
}

// TestGoBackendSearchUsesOwnListMany 钉住 Search 接的是 GoBackend 自己的 ListMany
// （与 BatchBackend 共用 searchBFS 主体）。
func TestGoBackendSearchUsesOwnListMany(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d1", "d2"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "top.txt", []byte("T"))
	writeRemote(t, root, "d1/mid.txt", []byte("M"))
	writeRemote(t, root, "d1/d2/deep.txt", []byte("D"))
	g := backendForTestServer(t, root)

	out, err := g.Search(context.Background(), "h", "", ".", "*.txt", -1, 10, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	paths := map[string]bool{}
	for _, h := range out.Hits {
		paths[h.Path] = true
	}
	for _, want := range []string{"top.txt", "d1/mid.txt", "d1/d2/deep.txt"} {
		if !paths[want] {
			t.Fatalf("Search 缺少命中 %q: %#v", want, out.Hits)
		}
	}
}

// —— Task 13：Connected 粘性语义 ——

// TestGoBackendConnectedIsSticky 钉住 Task 6 重审 I3 的裁决：Connected 表示
// 「连接意图 + 曾经成功握手」，成功 dial 置位，只有显式 Disconnect / CloseAll 清除。
// 池里的会话被逐出（idle 上限 / 会话被关）不能让 UI 翻回未连接。
// （Task 13 取代 Task 6 的 TestGoBackendConnectedTruthful：同样的四态断言在此保留，
//
//	但判据从「池里有没有活会话」改成粘性意图。）
func TestGoBackendConnectedIsSticky(t *testing.T) {
	g := backendForTestServer(t, t.TempDir())
	const host = "prod-01"

	if g.Connected(host) {
		t.Fatal("从未连接过就必须 false（会话惰性建立）")
	}
	if g.Connected("unrelated") {
		t.Fatal("从未连接过的其他 host 必须 false")
	}
	if err := g.Connect(host, ""); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !g.Connected(host) {
		t.Fatal("成功 Connect 后必须 true")
	}
	if g.Connected("unrelated") {
		t.Fatal("未连接过的 host 不得被顺带点亮")
	}
	// 显式 Disconnect 是清除意图的唯一入口（会话自身被关不再改变结论）。
	if err := g.Disconnect(host); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if g.Connected(host) {
		t.Fatal("显式 Disconnect 后必须 false")
	}
	if err := g.Connect(host, ""); err != nil {
		t.Fatalf("重连: %v", err)
	}
	if !g.Connected(host) {
		t.Fatal("重新 Connect 后必须再次 true")
	}
	g.CloseAll()
	if g.Connected(host) {
		t.Fatal("CloseAll 后必须 false")
	}
}

// TestGoBackendConnectedStickyAfterSessionEviction 钉住 I3 的真实场景：会话被
// 池逐出/关闭之后 Connected 仍为 true（连接意图仍在，UI 不该翻回未连接）。
func TestGoBackendConnectedStickyAfterSessionEviction(t *testing.T) {
	g := backendForTestServer(t, t.TempDir())
	const host = "evict-me"
	if err := g.Connect(host, ""); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// 逐出池里该 host 的全部会话（模拟 idle 上限 LRU / 会话被关）。
	g.pool.Disconnect(host)
	if !g.Connected(host) {
		t.Fatal("会话被逐出不得让 Connected 翻回 false（粘性连接意图）")
	}
	if err := g.Disconnect(host); err != nil {
		t.Fatal(err)
	}
	if g.Connected(host) {
		t.Fatal("显式 Disconnect 才是清除点")
	}
}

// —— Task 13：journal 恢复决策表（Task 8 重审 D1） ——

func writeSwapEntry(t *testing.T, dir string, e swapEntry) {
	t.Helper()
	j := newSwapJournal(dir)
	if err := j.Begin(e.Host, e.User, e.Target, e.Bak, e.Part); err != nil {
		t.Fatalf("写 journal 条目: %v", err)
	}
}

func journalEntries(t *testing.T, dir string) []swapEntry {
	t.Helper()
	return newSwapJournal(dir).Recover()
}

// fakeReopenSession 是恢复探测的内存替身：probingErr 为真时所有 exists 都返回错误，
// 用来构造「探测失败 ⇒ 不动作、不删条目」的 W3 场景。
type fakeReopenSession struct {
	mu     sync.Mutex
	files  map[string]string
	closed bool
	err    error
}

func (f *fakeReopenSession) exists(p string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	_, ok := f.files[p]
	return ok, nil
}

func (f *fakeReopenSession) remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, p)
	return nil
}

func (f *fakeReopenSession) rename(o, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[o]
	if !ok {
		return os.ErrNotExist
	}
	delete(f.files, o)
	f.files[n] = b
	return nil
}

func (f *fakeReopenSession) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

// useFakeRecoverSession 替换恢复探测会话注入点（生产实现走池；这里只为让决策表完全
// hermetic 且能确定性构造「探测失败」）。
func useFakeRecoverSession(t *testing.T, f *fakeReopenSession) *fakeReopenSession {
	t.Helper()
	orig := probeRecoverSession
	probeRecoverSession = func(*GoBackend, string, string) (reopenSession, error) { return f, nil }
	t.Cleanup(func() { probeRecoverSession = orig })
	return f
}

// TestRecoverSwapsDecisionTable 钉住 Task 8 重审 D1 的判定表。三种状态的 journal
// 条目字节相同，Recover 单凭条目无法区分，必须靠文件系统探测：
//   - bak 不存在 → 清条目（rename 尚未发生）；
//   - bak 在、target 不在 → 回滚 bak→target；
//   - bak 在、target 也在 → 保留 target（新内容已装好）、删 bak；
//   - 探测失败 → 不动作、不删条目。
func TestRecoverSwapsDecisionTable(t *testing.T) {
	jdir := t.TempDir()

	// W0：bak 尚未产生（target 旧内容仍在原名下）。
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "w0.txt", Bak: "w0.bak", Part: "w0.part"})
	// W1：target→bak 已发生，part→target 未发生 ⇒ 必须回滚。
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "w1.txt", Bak: "w1.bak", Part: "w1.part"})
	// W2：提交已完成（两处都在）⇒ 必须保留新内容、只删 bak。
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "w2.txt", Bak: "w2.bak", Part: "w2.part"})

	fake := &fakeReopenSession{files: map[string]string{
		"w0.txt":    "OLD0", // W0：只有 target
		"w1.bak":    "OLD1", // W1：只有 bak
		"w2.txt":    "NEW2", // W2：target（新内容）与 bak（旧内容）都在
		"w2.bak":    "OLD2",
		"decoy.bak": "FAIL",
	}}
	useFakeRecoverSession(t, fake)

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(jdir)
	// 恢复的探测凭据来自最近一次成功握手：这里直接标记一次（不联网）以走到探测逻辑。
	g.markConnected("h", "")
	n, err := g.RecoverSwaps()
	if err != nil {
		t.Fatalf("RecoverSwaps: %v", err)
	}
	if n != 3 {
		t.Fatalf("只应恢复 3 条: got %d", n)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.files["w0.txt"] != "OLD0" {
		t.Fatalf("W0：bak 不存在时必须保留 target 原内容: %v", fake.files)
	}
	if fake.files["w1.txt"] != "OLD1" {
		t.Fatalf("W1：bak 必须被回滚成 target: %v", fake.files)
	}
	if _, ok := fake.files["w1.bak"]; ok {
		t.Fatalf("W1：回滚后 bak 必须消失: %v", fake.files)
	}
	if fake.files["w2.txt"] != "NEW2" {
		t.Fatalf("W2：绝不能把已提交的新内容回退成旧内容: %v", fake.files)
	}
	if _, ok := fake.files["w2.bak"]; ok {
		t.Fatalf("W2：备份必须被删掉: %v", fake.files)
	}
	if entries := journalEntries(t, jdir); len(entries) != 0 {
		t.Fatalf("三条都应被处理并清条目: %#v", entries)
	}
	if !fake.closed {
		t.Fatal("恢复会话必须被关闭（绝不把半途状态放回 idle）")
	}
}

// TestRecoverSwapsProbeFailureKeepsEntry：探测失败（Stat 非 ENOENT）⇒ 不动作、不删条目。
func TestRecoverSwapsProbeFailureKeepsEntry(t *testing.T) {
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "x.txt", Bak: "x.bak", Part: "x.part"})
	fake := &fakeReopenSession{files: map[string]string{"x.bak": "OLD"}, err: errors.New("permission denied")}
	useFakeRecoverSession(t, fake)
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(jdir)
	g.markConnected("h", "")

	n, err := g.RecoverSwaps()
	if err != nil {
		t.Fatalf("探测失败不得把恢复变成错误: %v", err)
	}
	if n != 0 {
		t.Fatalf("探测失败时不得恢复任何条目: got %d", n)
	}
	fake.mu.Lock()
	_, bakKept := fake.files["x.bak"]
	fake.mu.Unlock()
	if !bakKept {
		t.Fatal("探测失败时绝不动手（bak 必须还在）")
	}
	if entries := journalEntries(t, jdir); len(entries) != 1 || entries[0].Target != "x.txt" {
		t.Fatalf("探测失败必须保留 journal 条目: %#v", entries)
	}
}

// TestRecoverSwapsSessionUnavailableKeepsEntry：会话拿不到（ssh 起不来）时同样
// 不动作、不删条目 —— 恢复是尽力而为，绝不能因为一次 dial 失败就把未完成现场抹掉。
func TestRecoverSwapsSessionUnavailableKeepsEntry(t *testing.T) {
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "x.txt", Bak: "x.bak", Part: "x.part"})

	orig := probeRecoverSession
	probeRecoverSession = func(*GoBackend, string, string) (reopenSession, error) {
		return nil, errors.New("ssh unavailable")
	}
	t.Cleanup(func() { probeRecoverSession = orig })

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(jdir)
	g.markConnected("h", "")

	n, err := g.RecoverSwaps()
	if err != nil {
		t.Fatalf("会话不可用不得把恢复变成错误: %v", err)
	}
	if n != 0 {
		t.Fatalf("会话不可用时不得恢复任何条目: got %d", n)
	}
	if entries := journalEntries(t, jdir); len(entries) != 1 {
		t.Fatalf("会话不可用时必须保留 journal 条目（留给下次启动）: %#v", entries)
	}
}

// TestRecoverSwapsRealSessionPath 用 TestServer 真会话走一遍默认 probe 路径，
// 证明默认接线（池 → Stat/Remove/Rename）在真实 SFTP 协议下真跑（不是只钉替身）。
func TestRecoverSwapsRealSessionPath(t *testing.T) {
	root, jdir := t.TempDir(), t.TempDir()
	g := backendForTestServer(t, root)

	// W1：只有 bak ⇒ 真回滚（真 rename）。
	writeRemote(t, root, "real.bak", []byte("OLD"))
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "real.txt", Bak: "real.bak", Part: "real.part"})
	// W2：新内容已提交 ⇒ 保留 target、真删 bak。
	writeRemote(t, root, "committed.txt", []byte("NEW"))
	writeRemote(t, root, "committed.bak", []byte("OLD"))
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "committed.txt", Bak: "committed.bak", Part: "committed.part"})
	g.SetJournalDir(jdir)
	// 默认 probe 需要一条真实会话：先握手一次（测试服务端），它会记下 host 凭据。
	if err := g.Connect("h", ""); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	n, err := g.RecoverSwaps()
	if err != nil {
		t.Fatalf("RecoverSwaps: %v", err)
	}
	if n != 2 {
		t.Fatalf("应恢复 2 条: got %d", n)
	}
	if b, err := os.ReadFile(filepath.Join(root, "real.txt")); err != nil || string(b) != "OLD" {
		t.Fatalf("W1：真会话下必须回滚 bak→target: err=%v content=%q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(root, "committed.txt")); err != nil || string(b) != "NEW" {
		t.Fatalf("W2：必须保留已提交的新内容: err=%v content=%q", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "committed.bak")); !os.IsNotExist(err) {
		t.Fatalf("W2：备份必须被删掉, stat err=%v", err)
	}
	if entries := journalEntries(t, jdir); len(entries) != 0 {
		t.Fatalf("处理后 journal 必须清空: %#v", entries)
	}
}

// TestRecoverSwapsProbesEntryHostNotLastHandshake 是 reviewer 只读 overlay 复现的仓库用例
// （修复轮 1 / F1）：journal 里有一条属于 hostB 的条目，而进程最近一次握手是 hostA。
// 旧实现拿 hostA 的会话探所有条目；hostA 的视图里没有 b.bak ⇒ 误判 W0 清掉条目，
// 于是 hostB 的 target 名永久缺失、旧内容只剩随机 bak（本地 7 天清理也够不到）。
// 现在恢复只探条目自己的 host：hostB 不可达时条目原样保留（不动作、不删除），
// hostA 根本不会被探 —— 哪怕它能看到一个「没有 bak」的视图。
func TestRecoverSwapsProbesEntryHostNotLastHandshake(t *testing.T) {
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Host: "hostB", User: "ub", Target: "b.txt", Bak: "b.bak", Part: "b.part"})

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(jdir)
	// overlay 场景：最近一次握手是 hostA（旧实现的探测凭据来源）。
	g.markConnected("hostA", "ua")

	var probed []string
	orig := probeRecoverSession
	probeRecoverSession = func(_ *GoBackend, host, user string) (reopenSession, error) {
		probed = append(probed, host+"/"+user)
		if host == "hostB" {
			return nil, errors.New("hostB 不可达")
		}
		// hostA 的视图里只有 a-only.txt：若实现仍用 hostA 探 B 的条目，b.bak 会被判成
		// 「不存在」而清掉条目 —— 正是要防的数据丢失。
		return &fakeReopenSession{files: map[string]string{"a-only.txt": "A"}}, nil
	}
	t.Cleanup(func() { probeRecoverSession = orig })

	n, err := g.RecoverSwaps()
	if err != nil {
		t.Fatalf("RecoverSwaps: %v", err)
	}
	if n != 0 {
		t.Fatalf("hostB 不可达时不得处理任何条目: got %d", n)
	}
	if len(probed) != 1 || probed[0] != "hostB/ub" {
		t.Fatalf("恢复必须只探条目自己的 hostB/ub，got %v", probed)
	}
	entries := journalEntries(t, jdir)
	if len(entries) != 1 || entries[0].Host != "hostB" || entries[0].Target != "b.txt" {
		t.Fatalf("归属 hostB 的条目必须原样保留，got %+v", entries)
	}
}

// TestRecoverSwapsRecoversPerHostAndKeepsUnreachable 钉住分组的完整语义：hostA 可达 →
// 按决策表恢复并清掉它的条目；hostB 不可达 → 它的条目原样保留。两次探测必须各用自己的
// (host,user)，绝不交叉。
func TestRecoverSwapsRecoversPerHostAndKeepsUnreachable(t *testing.T) {
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Host: "hostA", User: "ua", Target: "a.txt", Bak: "a.bak", Part: "a.part"})
	writeSwapEntry(t, jdir, swapEntry{Host: "hostB", User: "ub", Target: "b.txt", Bak: "b.bak", Part: "b.part"})

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(jdir)

	probed := map[string]bool{}
	fakeA := &fakeReopenSession{files: map[string]string{"a.bak": "OLD-A"}}
	orig := probeRecoverSession
	probeRecoverSession = func(_ *GoBackend, host, user string) (reopenSession, error) {
		probed[host+"/"+user] = true
		if host == "hostA" {
			return fakeA, nil
		}
		return nil, errors.New("hostB 不可达")
	}
	t.Cleanup(func() { probeRecoverSession = orig })

	n, err := g.RecoverSwaps()
	if err != nil || n != 1 {
		t.Fatalf("只应恢复 hostA 的 1 条: n=%d err=%v", n, err)
	}
	if !probed["hostA/ua"] || !probed["hostB/ub"] || len(probed) != 2 {
		t.Fatalf("每个分组必须用自己的凭据探测: got %v", probed)
	}
	fakeA.mu.Lock()
	_, bakKept := fakeA.files["a.bak"]
	gotA := fakeA.files["a.txt"]
	fakeA.mu.Unlock()
	if bakKept || gotA != "OLD-A" {
		t.Fatalf("hostA 的 W1 必须被回滚: %+v", fakeA.files)
	}
	entries := journalEntries(t, jdir)
	if len(entries) != 1 || entries[0].Host != "hostB" {
		t.Fatalf("hostB 的条目必须保留、hostA 的必须清掉，got %+v", entries)
	}
}

// TestRecoverSwapsSkipsLegacyEntriesWithoutHost（F1 向后兼容）：归属为空的旧条目
// 绝不动作、绝不删除；即使有可用会话也不去探它（避免误判 W0 清掉无法重建的现场）。
func TestRecoverSwapsSkipsLegacyEntriesWithoutHost(t *testing.T) {
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Target: "old.txt", Bak: "old.bak", Part: "old.part"}) // host 为空

	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(jdir)

	probed := 0
	orig := probeRecoverSession
	probeRecoverSession = func(_ *GoBackend, host, user string) (reopenSession, error) {
		probed++
		return &fakeReopenSession{files: map[string]string{}}, nil
	}
	t.Cleanup(func() { probeRecoverSession = orig })

	n, err := g.RecoverSwaps()
	if err != nil || n != 0 {
		t.Fatalf("归属未知的旧条目不得被处理: n=%d err=%v", n, err)
	}
	if probed != 0 {
		t.Fatalf("归属未知的旧条目不得触发任何探测，got %d 次", probed)
	}
	if entries := journalEntries(t, jdir); len(entries) != 1 || entries[0].Target != "old.txt" {
		t.Fatalf("归属未知的旧条目必须原样保留: %+v", entries)
	}
}

// TestRecoverSwapsNoJournalIsNoop：没有 journal（dir==""）或文件不存在时是 no-op，
// 不 panic、不报错。
func TestRecoverSwapsNoJournalIsNoop(t *testing.T) {
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	if n, err := g.RecoverSwaps(); err != nil || n != 0 {
		t.Fatalf("无 journal 时必须是 no-op: n=%d err=%v", n, err)
	}
	g.SetJournalDir(t.TempDir())
	if n, err := g.RecoverSwaps(); err != nil || n != 0 {
		t.Fatalf("空 journal 时必须是 no-op: n=%d err=%v", n, err)
	}
}

// —— Task 13：退出清理 ——

// TestGoBackendCleanupPartsRemovesKnownParts：CloseAll 在关会话之前 best-effort
// 删掉本进程登记的已知 .part（本地 os.Remove、远端 Remove），随后清空登记表。
func TestGoBackendCleanupPartsRemovesKnownParts(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	localDir := t.TempDir()
	localPart := filepath.Join(localDir, "local"+PartMarker+"t13-abcd")
	// 远端路径必须是 **POSIX 语义**（生产里 GoBackend 的远端路径一律 path 家族）。测试服务端
	// 用 filepath.Abs(toLocalPath(p)) 解析，Windows 上喂绝对路径会被拼成非法名；登记表里
	// 存的也是远端路径，所以这里两处都用同一个相对路径，落到 root 下。
	remoteRel := "remote" + PartMarker + "t13-abcd"
	remotePart := writeRemote(t, root, remoteRel, []byte("half"))
	if err := os.WriteFile(localPart, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.recordPart("h", "u", "t13", localPart, remoteRel)
	g.CleanupParts()
	if _, err := os.Stat(localPart); !os.IsNotExist(err) {
		t.Fatalf("本地已知 .part 必须被清掉, stat err=%v", err)
	}
	if _, err := os.Stat(remotePart); !os.IsNotExist(err) {
		t.Fatalf("远端已知 .part 必须被清掉, stat err=%v", err)
	}
	g.partsMu.Lock()
	n := len(g.knownParts)
	g.partsMu.Unlock()
	if n != 0 {
		t.Fatalf("清理后登记表必须清空, got %d", n)
	}
}

// TestGoBackendCloseAllCleansKnownParts：CloseAll（生产退出路径）同样清理已知 .part。
func TestGoBackendCloseAllCleansKnownParts(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	localDir := t.TempDir()
	localPart := filepath.Join(localDir, "local"+PartMarker+"t13-cd")
	remoteRel := "remote" + PartMarker + "t13-cd"
	remotePart := writeRemote(t, root, remoteRel, []byte("half"))
	if err := os.WriteFile(localPart, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.recordPart("h", "u", "t13cd", localPart, remoteRel)
	g.CloseAll()
	if _, err := os.Stat(localPart); !os.IsNotExist(err) {
		t.Fatalf("CloseAll 后本地 .part 必须消失, stat err=%v", err)
	}
	if _, err := os.Stat(remotePart); !os.IsNotExist(err) {
		t.Fatalf("CloseAll 后远端 .part 必须消失, stat err=%v", err)
	}
}

// TestCleanupStaleLocalPartsOlderThan7Days：启动清理只删「名字含 PartMarker 且
// 超过 7 天」的本地临时文件，绝不误删新临时文件或普通业务文件。
func TestCleanupStaleLocalPartsOlderThan7Days(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	stale := filepath.Join(root, "stale.txt"+PartMarker+"old-aaaa")
	fresh := filepath.Join(root, "fresh.txt"+PartMarker+"new-bbbb")
	keep := filepath.Join(root, "keep.txt")
	for _, p := range []string{stale, fresh, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	n, err := CleanupStaleLocalParts(root, now)
	if err != nil {
		t.Fatalf("CleanupStaleLocalParts: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只清理 1 个陈旧 .part, got %d", n)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("超过 7 天的 .part 必须被清掉, stat err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("未超过 7 天的 .part 不得被动: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("业务文件绝不能被误删: %v", err)
	}
	// 不可读 / 不存在的 root 不得报错、不得 panic。
	if n, err := CleanupStaleLocalParts(filepath.Join(root, "no-such-dir"), now); err != nil || n != 0 {
		t.Fatalf("不存在的 root 应为 no-op: n=%d err=%v", n, err)
	}
}

// TestCtrlDelegatesLifecycleToGoBackend 钉住门面的两个生命周期转发
// （app.go OnShutdown 走它们）。
func TestCtrlDelegatesLifecycleToGoBackend(t *testing.T) {
	root, jdir := t.TempDir(), t.TempDir()
	g := backendForTestServer(t, root)
	writeRemote(t, root, "d.bak", []byte("OLD"))
	writeSwapEntry(t, jdir, swapEntry{Host: "h", Target: "d.txt", Bak: "d.bak", Part: "d.part"})
	g.SetJournalDir(jdir)
	g.markConnected("h", "") // 模拟此前已成功握手（恢复凭据来自最近握手）
	c := NewCtrlForcedBackend(g)
	n, err := c.RecoverSwaps()
	if err != nil || n != 1 {
		t.Fatalf("门面 RecoverSwaps 必须转发到 GoBackend: n=%d err=%v", n, err)
	}
	localPart := filepath.Join(t.TempDir(), "p"+PartMarker+"x")
	if err := os.WriteFile(localPart, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.recordPart("h", "u", "t13-facade", localPart, "")
	c.CleanupParts()
	if _, err := os.Stat(localPart); !os.IsNotExist(err) {
		t.Fatalf("门面 CleanupParts 必须转发到 GoBackend: stat err=%v", err)
	}
}

// —— Task 13 修复轮 1：F2 退出静默 ——

// TestCloseAllQuiescesInflightTransferKeepsPart（F2）：Put 建出远端 .part 并登记之后、
// 提交之前停在注入点；此时 CloseAll 不能删掉这条仍在飞的 .part，也不能在传输结束前返回。
// 旧顺序（先 CleanupParts 再 pool.CloseAll、不等静默）会立刻删 .part 并关会话，
// 让这次提交彻底失败（target 缺席、内容丢在随机 bak/被删的 .part 里）。
func TestCloseAllQuiescesInflightTransferKeepsPart(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	local := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(local, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	t.Cleanup(release)
	origHook := beforeOpenLocalSource
	beforeOpenLocalSource = func(p string) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-unblock
	}
	t.Cleanup(func() { beforeOpenLocalSource = origHook })

	putDone := make(chan error, 1)
	go func() {
		putDone <- g.Put(TransferRequest{ID: "f2a", Host: "h", Local: local, Remote: "dst.bin", Atomic: true}, nil)
	}()
	<-entered // .part 已建出并 recordPart，传输停在注入点

	// 找到磁盘上的远端 .part。
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var part string
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			part = filepath.Join(root, e.Name())
		}
	}
	if part == "" {
		t.Fatal("传输停在注入点时远端必须已有 .part")
	}

	closeDone := make(chan struct{})
	go func() {
		g.CloseAll()
		close(closeDone)
	}()
	// CloseAll 必须等待在飞传输：不能返回、更不能删掉 .part。
	select {
	case <-closeDone:
		t.Fatal("CloseAll 不得在传输仍在飞时返回（必须先静默）")
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("在飞传输的 .part 不得被退出清理删掉: %v", err)
	}

	release() // 让传输完成提交
	if err := <-putDone; err != nil {
		t.Fatalf("放行后传输应成功提交: %v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("静默后 CloseAll 必须返回")
	}
	if b, err := os.ReadFile(filepath.Join(root, "dst.bin")); err != nil || string(b) != "payload" {
		t.Fatalf("目标必须被提交: err=%v content=%q", err, b)
	}
}

// TestShutdownDoesNotClearJournalEntryDuringInflightCommit（F2）：提交停在
// rename(target→bak) 之前（journal intent 已落盘、破坏性改名尚未发生）时发起 CloseAll。
// 修复后的顺序是先静默再返回，因此 app.go 随后的 RecoverSwaps 绝不会把这条在飞 intent
// 当成 W0 清掉（旧顺序下 CloseAll 立即返回，恢复看到 bak 不存在 ⇒ 清条目，target 与
// 条目双失）。
func TestShutdownDoesNotClearJournalEntryDuringInflightCommit(t *testing.T) {
	root, jdir := t.TempDir(), t.TempDir()
	g := backendForTestServer(t, root)
	g.SetJournalDir(jdir)
	local := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(local, []byte("NEW"), 0600); err != nil {
		t.Fatal(err)
	}

	// 强制走 backup-swap：posix 提交一律失败，落到 journal.Begin + rename(target→bak)。
	swapPosixRename(t, func(*sftp.Client, string, string) error { return errors.New("no posix") })

	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	t.Cleanup(release)
	var origRename func(*sftp.Client, string, string) error
	origRename = swapRenameRemote(t, func(c *sftp.Client, oldname, newname string) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-unblock
		return origRename(c, oldname, newname)
	})

	putDone := make(chan error, 1)
	go func() {
		putDone <- g.Put(TransferRequest{ID: "f2b", Host: "h", Local: local, Remote: "dst.bin", Atomic: true}, nil)
	}()
	<-entered
	if entries := journalEntries(t, jdir); len(entries) != 1 {
		t.Fatalf("提交在飞时 journal 必须已落盘 1 条 intent，got %+v", entries)
	}

	closeDone := make(chan struct{})
	go func() {
		g.CloseAll()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("CloseAll 不得在提交仍在飞时返回（必须先静默）")
	case <-time.After(250 * time.Millisecond):
	}
	if entries := journalEntries(t, jdir); len(entries) != 1 {
		t.Fatalf("在飞提交期间 journal 条目不得被清掉，got %+v", entries)
	}

	release()
	if err := <-putDone; err != nil {
		t.Fatalf("放行后提交应成功: %v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("静默后 CloseAll 必须返回")
	}
	if entries := journalEntries(t, jdir); len(entries) != 0 {
		t.Fatalf("提交成功后 journal 条目必须清空，got %+v", entries)
	}
	if b, err := os.ReadFile(filepath.Join(root, "dst.bin")); err != nil || string(b) != "NEW" {
		t.Fatalf("目标必须被提交: err=%v content=%q", err, b)
	}
}

// —— Task 13 修复轮 2：D1 退出宽限期 ——

// TestCloseAllGraceBoundsStuckTransfer（D1）钉住「有界退出」：一个故意卡死、且**既不响应
// ctx 也不经过会话 IO** 的传输（注入点直接阻塞在通道上）不得让 CloseAll 永久等待 ——
// 修复轮 1 的无界 transfersWG.Wait() 会在这里永远挂住。
// 修复后 CloseAll 等满 closeGrace 即取消后端级 transferCtx、关闭全部会话并返回（发 warn）。
func TestCloseAllGraceBoundsStuckTransfer(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	// 单测不真等 5 秒：宽限期可注入，生产默认仍是 shutdownGrace。
	g.closeGrace = 200 * time.Millisecond

	local := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(local, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	t.Cleanup(release)
	origHook := beforeOpenLocalSource
	beforeOpenLocalSource = func(string) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-unblock // 刻意无视 ctx 与会话关闭：最“赖”的卡死形态
	}
	t.Cleanup(func() { beforeOpenLocalSource = origHook })

	stuckDone := make(chan error, 1)
	go func() {
		stuckDone <- g.Put(TransferRequest{ID: "d1-stuck", Host: "h", Local: local, Remote: "stuck.bin", Atomic: true}, nil)
	}()
	<-entered

	closeDone := make(chan struct{})
	start := time.Now()
	go func() {
		g.CloseAll()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseAll 必须在宽限期内返回，绝不能无界等待卡死的传输（D1 回归）")
	}
	elapsed := time.Since(start)
	if elapsed < g.closeGrace {
		t.Fatalf("CloseAll 必须先等满宽限期（先静默、后强停），elapsed=%v grace=%v", elapsed, g.closeGrace)
	}
	if elapsed > g.closeGrace+2*time.Second {
		t.Fatalf("CloseAll 必须在宽限期附近返回，elapsed=%v grace=%v", elapsed, g.closeGrace)
	}
	if g.transferCtx.Err() == nil {
		t.Fatal("宽限期耗尽后必须取消后端级传输上下文（强停入口）")
	}

	// 放行卡死传输：它会因为会话已被关闭而失败返回（绝不报成功），且不得泄漏 goroutine。
	release()
	select {
	case err := <-stuckDone:
		if err == nil {
			t.Fatal("被强停的传输不得报成功")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("放行后卡死传输必须结束")
	}
}

// TestCloseAllGraceInterruptsQueuedTransfer（D1）：取额度排队是**无界**的（池容量 1），
// 若在飞传输卡死，第二个传输会永远卡在 <-p.queue 上。这条用例占住唯一额度，证明
// CloseAll 的宽限期取消后端级 ctx 后，排队的 select 会走 ctx.Done() 分支返回，
// 而不是继续阻塞（这正是修复轮 1 的第二条 liveness 漏洞）。
func TestCloseAllGraceInterruptsQueuedTransfer(t *testing.T) {
	root := t.TempDir()
	writeRemote(t, root, "a.bin", []byte("x"))
	g := backendForTestServer(t, root)
	g.closeGrace = 150 * time.Millisecond
	// 占住唯一传输额度：第二个传输必然在 AcquireTransfer 里无界排队。
	<-g.pool.queue

	local := filepath.Join(t.TempDir(), "a.bin")
	queuedDone := make(chan error, 1)
	go func() {
		queuedDone <- g.Get(TransferRequest{ID: "d1-queued", Host: "h", Remote: "a.bin", Local: local}, nil)
	}()
	// 等到它确实已登记为在飞（acquireInflight 之后、AcquireTransfer 排队中）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		g.inflightMu.Lock()
		n := len(g.inflight)
		g.inflightMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("排队传输未在预期时间内登记为在飞")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	g.CloseAll()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("CloseAll 不得无界等待排队中的传输: %v", elapsed)
	}
	select {
	case err := <-queuedDone:
		if err == nil {
			t.Fatal("排队等待被取消的传输必须报错")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseAll 取消后端级 ctx 必须中断无界的排队等待")
	}
}

// —— Task 13 修复轮 1：F5 符号链接语义 ——

// sftpOpStat / sftpOpLstat 是 SSH_FXP_STAT / SSH_FXP_LSTAT 的包类型号（draft-02 §3 编码）。
const (
	sftpOpStat  = 17
	sftpOpLstat = 7
)

// sftpReqCounter 按 SSH_FXP 帧解析并统计客户端发出的请求类型。
//
// 为什么需要它：pkg/sftp 自带的测试服务端把 RMDIR 实现成 os.Remove（server.go 的
// sshFxpRmdir 分支），对符号链接也会成功 unlink —— 因此「Stat 跟随符号链接」与
// 「Lstat 不跟随」在行为上无法区分（真 OpenSSH 的 RMDIR 才会 ENOTDIR）。
// 计数器提供结构性断言：不跟随符号链接的实现不能对条目发出 SSH_FXP_STAT。
type sftpReqCounter struct {
	mu     sync.Mutex
	buf    []byte
	counts map[byte]int
}

func newSFTPReqCounter() *sftpReqCounter { return &sftpReqCounter{counts: map[byte]int{}} }

// observe 把写入字节追加进缓冲，尽量切出完整帧（uint32 长度 + 1 字节类型 + 负载），
// 只统计类型。解析不出来时只丢弃缓冲，绝不影响字节透传。
func (c *sftpReqCounter) observe(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	for len(c.buf) >= 5 {
		n := int(binary.BigEndian.Uint32(c.buf[:4]))
		if n < 1 || n > 1<<24 {
			c.buf = nil
			return
		}
		if len(c.buf) < 4+n {
			return
		}
		c.counts[c.buf[4]]++
		c.buf = c.buf[4+n:]
	}
}

func (c *sftpReqCounter) count(op byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[op]
}

func (c *sftpReqCounter) reset() {
	c.mu.Lock()
	c.counts = map[byte]int{}
	c.buf = nil
	c.mu.Unlock()
}

// sftpReqSniffer 夹在 sftp 客户端与 net.Pipe 之间：写方向计数后原样透传，读方向透传。
type sftpReqSniffer struct {
	inner net.Conn
	cnt   *sftpReqCounter
}

func (s *sftpReqSniffer) Read(p []byte) (int, error) { return s.inner.Read(p) }
func (s *sftpReqSniffer) Close() error               { return s.inner.Close() }
func (s *sftpReqSniffer) Write(p []byte) (int, error) {
	s.cnt.observe(p)
	return s.inner.Write(p)
}

// backendForTestServerSniffed 与 backendForTestServer 相同，但每次 dial 都把客户端侧包一层
// 请求计数器（共享同一个 counter，跨会话累计）。
func backendForTestServerSniffed(t *testing.T, root string) (*GoBackend, *sftpReqCounter) {
	t.Helper()
	g := NewGoBackend(nil, nil)
	cnt := newSFTPReqCounter()
	var mu sync.Mutex
	var servers []*sftp.Server
	var conns []net.Conn
	g.pool.dial = func(host, user string) (*Session, error) {
		c1, c2 := net.Pipe()
		srv, err := sftp.NewServer(c1, sftp.WithServerWorkingDirectory(root))
		if err != nil {
			return nil, err
		}
		go func() { _ = srv.Serve() }()
		sn := &sftpReqSniffer{inner: c2, cnt: cnt}
		cl, err := sftp.NewClientPipe(sn, sn)
		if err != nil {
			_ = srv.Close()
			return nil, err
		}
		// 跨平台替身子进程：Session.close 会 Kill，nil Proc 会 nil panic；且带 Proc 的
		// 会话才算「真的建立过连接」（GoBackend.Connected 的粘性置位判据）。
		p := testHelperPipes(t, "")
		mu.Lock()
		servers = append(servers, srv)
		conns = append(conns, c1, c2)
		mu.Unlock()
		return &Session{Host: host, User: user, Conn: cl, Proc: p, state: sessBusy}, nil
	}
	t.Cleanup(func() {
		g.CloseAll()
		mu.Lock()
		defer mu.Unlock()
		for _, s := range servers {
			_ = s.Close()
		}
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return g, cnt
}

// TestGoBackendRemoveRecursiveSymlink（F5）：Walker 用 Lstat/ReadDir 语义（不跟随符号
// 链接），条目分类也必须一致 —— 指向目录的符号链接必须当普通条目 unlink，
// 绝不能用 Conn.Stat 跟随它、再对它 RemoveDirectory（真 OpenSSH 会 ENOTDIR，让整次递归失败）。
//
// 自带测试服务端的 RMDIR 是 os.Remove，行为上区分不出 Stat/Lstat，所以这里额外用请求计数器
// 做结构性断言：整个递归期间不得出现任何 SSH_FXP_STAT（跟随符号链接的 stat）。
func TestGoBackendRemoveRecursiveSymlink(t *testing.T) {
	root := t.TempDir()
	g, cnt := backendForTestServerSniffed(t, root)
	if err := os.MkdirAll(filepath.Join(root, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "outside/keep.txt", []byte("KEEP"))
	if err := os.MkdirAll(filepath.Join(root, "tree", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "tree/sub/real.txt", []byte("R"))
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "tree", "sub", "linkdir")); err != nil {
		t.Skipf("本机不支持创建符号链接，无法在本用例中覆盖 F5: %v", err)
	}

	cnt.reset() // 只统计本次 RemoveRecursive 期间的请求
	if err := g.RemoveRecursive("h", "", "tree"); err != nil {
		t.Fatalf("含指向目录的符号链接的树必须被完整删除: %v", err)
	}
	if n := cnt.count(sftpOpStat); n != 0 {
		t.Fatalf("递归删除不得对条目发跟随符号链接的 SSH_FXP_STAT（应用 w.Stat/Lstat），got %d 次", n)
	}
	if n := cnt.count(sftpOpLstat); n == 0 {
		t.Fatal("必须至少用一次 SSH_FXP_LSTAT（Walker 的根 Lstat + 目标自身的 Lstat）")
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Fatalf("tree 必须被删除, stat err=%v", err)
	}
	// 符号链接的目标绝不能被递归删除（语义与 Walker 一致：不跟随）。
	if b, err := os.ReadFile(filepath.Join(root, "outside", "keep.txt")); err != nil || string(b) != "KEEP" {
		t.Fatalf("符号链接的目标不得被删除: err=%v content=%q", err, b)
	}
}

// —— Task 13 修复轮 1：F6 边界 ——

// TestGoBackendRemoveRecursiveRejectsRelativeRoots（F6）："."/".." 也必须拒绝，
// 不能把「当前目录」当成可递归删除的目标。
func TestGoBackendRemoveRecursiveRejectsRelativeRoots(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	writeRemote(t, root, "keep.txt", []byte("K"))
	for _, p := range []string{"", "/", ".", "..", "./", "../"} {
		if err := g.RemoveRecursive("h", "", p); err == nil {
			t.Fatalf("必须拒绝递归删除根/相对根路径 %q", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("拒绝后不得删除任何东西: %v", err)
	}
}

// TestGoBackendRemoveRecursiveBounds（F6）：条目数与深度上限必须在**收集阶段**触发，
// 此时还没删任何文件 —— 绝不留下删一半的树。
func TestGoBackendRemoveRecursiveBounds(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tree", "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "tree/top.txt", []byte("T"))
	writeRemote(t, root, "tree/a/mid.txt", []byte("M"))
	writeRemote(t, root, "tree/a/b/deep.txt", []byte("D"))

	origE, origD := removeMaxEntries, removeMaxDepth
	t.Cleanup(func() { removeMaxEntries, removeMaxDepth = origE, origD })

	// 深度上限：tree/a/b/deep.txt 深度 3 ⇒ 上限 2 必失败。
	removeMaxEntries, removeMaxDepth = 100000, 2
	if err := g.RemoveRecursive("h", "", "tree"); err == nil {
		t.Fatal("超过深度上限必须整体失败")
	}
	if _, err := os.Stat(filepath.Join(root, "tree", "a", "b", "deep.txt")); err != nil {
		t.Fatalf("撞深度上限时不得删除任何文件: %v", err)
	}

	// 条目上限：树里共 5 个条目（top.txt, a, a/mid.txt, a/b, a/b/deep.txt）⇒ 上限 2 必失败。
	removeMaxEntries, removeMaxDepth = 2, 64
	if err := g.RemoveRecursive("h", "", "tree"); err == nil {
		t.Fatal("超过条目上限必须整体失败")
	}
	if _, err := os.Stat(filepath.Join(root, "tree", "top.txt")); err != nil {
		t.Fatalf("撞条目上限时不得删除任何文件: %v", err)
	}

	// 还原后必须成功删掉整棵树。
	removeMaxEntries, removeMaxDepth = origE, origD
	if err := g.RemoveRecursive("h", "", "tree"); err != nil {
		t.Fatalf("还原上限后必须成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Fatalf("tree 必须被删除, stat err=%v", err)
	}
}

// TestCleanupPartsNilConnDoesNotPanic（F7）：CleanupParts 与其它能力方法一样必须先判
// s.Conn。异常/测试替身会话可能没有 SFTP 连接，直接解引用会 panic 并让退出清理崩掉。
func TestCleanupPartsNilConnDoesNotPanic(t *testing.T) {
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.pool.dial = func(host, user string) (*Session, error) {
		return &Session{Host: host, User: user, Conn: nil, Proc: testHelperPipes(t, ""), state: sessBusy}, nil
	}
	g.recordPart("h", "u", "f7", "", "remote.part")
	g.CleanupParts() // 不得 panic
	g.partsMu.Lock()
	n := len(g.knownParts)
	g.partsMu.Unlock()
	if n != 0 {
		t.Fatalf("清理后登记表必须清空, got %d", n)
	}
}

// TestMain 让测试二进制可以在 helper 模式下扮演替身子进程（见 helper_test.go）。
func TestMain(m *testing.M) {
	os.Exit(helperTestMain(m))
}

func idxOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func pairPresent(xs []string, a, b string) bool {
	for i := 0; i+1 < len(xs); i++ {
		if xs[i] == a && xs[i+1] == b {
			return true
		}
	}
	return false
}

var _ = runtime.GOOS
