package sftp

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

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
	// 用 cat 作为真实但本地的替身：NewClientPipe 被注入为立即失败，因此不会读管道、
	// 不会挂住；dial 随后必须 Close() 掉它。
	startSFTPPipes = func(string, ...string) (*osutil.PipedProcess, error) {
		return osutil.StartPipes("cat")
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
		g.knownParts["id"] = [2]string{"/l", "/r"}
		g.partsMu.Unlock()
	}()
	if len(g.reg) != 1 || g.inflight["id"] != "key" || g.knownParts["id"] != [2]string{"/l", "/r"} {
		t.Fatalf("写入后读数不对: reg=%v inflight=%v knownParts=%v", g.reg, g.inflight, g.knownParts)
	}
}

// TestGoBackendConnectedTruthful 是 Task 6 评审 I3 的回归测试：Connected 必须真实反映
// 「本 host 是否成功建立过会话」。会话惰性建立 —— 未连过 false；成功 dial 入池（Connect 的
// 握手探测同样走池）后 true（含会话停在 idle 的情况，正是 UI 误显示未连接的场景）；
// 未连过的其他 host 仍 false；Disconnect 关掉后回到 false。
func TestGoBackendConnectedTruthful(t *testing.T) {
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	const host = "prod-01"
	if g.Connected(host) {
		t.Fatal("从未连接过就必须 false（会话惰性建立）")
	}
	if g.Connected("unrelated") {
		t.Fatal("从未连接过的其他 host 必须 false")
	}

	// 用必定成功的 dial 替换池接线，模拟成功握手（不联网、不起真实 ssh）。
	orig := g.pool.dial
	g.pool.dial = func(h, u string) (*Session, error) { return &Session{Host: h, User: u}, nil }
	t.Cleanup(func() { g.pool.dial = orig })

	s, err := g.pool.AcquireList(context.Background(), host, "")
	if err != nil {
		t.Fatalf("AcquireList: %v", err)
	}
	if !g.Connected(host) {
		t.Fatal("成功 dial 入池后必须 true —— 否则 UI 显示未连接（I3 回归）")
	}
	if g.Connected("unrelated") {
		t.Fatal("未连接过的 host 不得被顺带点亮（不得对未连接 host 造 true）")
	}
	g.pool.Release(s, true) // 会话停在 idle：I3 现象正是「池中有 idle 仍显示未连接」
	if !g.Connected(host) {
		t.Fatal("会话在池中 idle 时必须 true")
	}
	if err := g.Disconnect(host); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if g.Connected(host) {
		t.Fatal("Disconnect 关掉会话后必须回到 false")
	}
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
