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

	// 后续 task 的容器字段必须已声明（编译期即证）；这里钉住可用性。
	g.regMu.Lock()
	if g.reg == nil {
		g.reg = map[string]*Session{}
	}
	g.reg["id"] = nil
	g.regMu.Unlock()
	g.inflightMu.Lock()
	g.inflight = map[string]string{"id": "key"}
	g.inflightMu.Unlock()
	g.partsMu.Lock()
	g.knownParts = map[string][2]string{"id": {"/l", "/r"}}
	g.partsMu.Unlock()
	if got := g.knownParts["id"]; got != [2]string{"/l", "/r"} {
		t.Fatalf("knownParts 字段不可用: %v", got)
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
