package forward

import (
	"fmt"
	"net"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"sshkit/internal/config"
	"sshkit/internal/osutil"
)

func base() config.Tunnel {
	return config.Tunnel{
		ID: "abc", Name: "t", Host: "prod-db",
		ListenBind: "127.0.0.1", ListenPort: 5432,
		TargetHost: "127.0.0.1", TargetPort: 5432,
	}
}

func expect(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBuildArgsLocal(t *testing.T) {
	expect(t, BuildArgs(base()),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-L", "127.0.0.1:5432:127.0.0.1:5432", "prod-db"})
}

func TestBuildArgsRemote(t *testing.T) {
	c := base()
	c.Mode = "remote"
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-R", "127.0.0.1:5432:127.0.0.1:5432", "prod-db"})
}

func TestBuildArgsDynamic(t *testing.T) {
	c := base()
	c.Mode = "dynamic"
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-D", "127.0.0.1:5432", "prod-db"})
}

// H1: 导入规则携带 User/Port 时 BuildArgs 必须输出 -l/-p。
func TestBuildArgsUserAndPort(t *testing.T) {
	c := base()
	c.User = "bob"
	c.Port = 2222
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-p", "2222", "-l", "bob",
			"-L", "127.0.0.1:5432:127.0.0.1:5432", "prod-db"})
}

// H1: User/Port 为零值时不得出现 -l/-p 参数。
func TestBuildArgsNoUserNoPort(t *testing.T) {
	got := BuildArgs(base())
	for i, a := range got {
		if a == "-p" || a == "-l" {
			t.Fatalf("unexpected %q at index %d: %v", a, i, got)
		}
	}
}

func TestBuildArgsProxyJump(t *testing.T) {
	c := base()
	c.ProxyJump = "bastion"
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-L", "127.0.0.1:5432:127.0.0.1:5432", "-J", "bastion", "prod-db"})
}

func TestValidateHost(t *testing.T) {
	if !ValidateHost("prod-db") || !ValidateHost("10.0.0.5") || !ValidateHost("a.b.c") {
		t.Fatal("valid host rejected")
	}
	if ValidateHost("-oProxyCommand=evil") || ValidateHost("host with space") {
		t.Fatal("malicious host accepted")
	}
}

func TestStartInvalidHost(t *testing.T) {
	var emitted []Event
	tr := config.Tunnel{ID: "x", Host: "-oProxyCommand=evil", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 1}
	called := false
	sp := fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		called = true
		return &osutil.Process{}, nil
	}}
	ctrl := NewCtrl(sp, func(e Event) { emitted = append(emitted, e) }, nil)
	if err := ctrl.Start(tr); err == nil {
		t.Fatal("expected error for malicious host")
	}
	if called {
		t.Fatal("spawner should not be called for invalid host")
	}
	if ctrl.State("x") != StateError {
		t.Fatalf("state should be error, got %s", ctrl.State("x"))
	}
}

// fakeSpawner implements osutil.Spawner for tests.
type fakeSpawner struct {
	startFunc func(name string, args ...string) (*osutil.Process, error)
}

func (f fakeSpawner) Start(name string, args ...string) (*osutil.Process, error) {
	return f.startFunc(name, args...)
}

// exitProcess 通过真实 spawner 启动一个立即以指定退出码结束的真实进程。
// fakeSpawner 受 osutil.Spawner 接口约束只能返回 *osutil.Process，其 done
// 通道不可注入，故用真实短命进程模拟“立即退出”的进程（同时覆盖真实 Wait 路径）。
func exitProcess(t *testing.T, code int) (*osutil.Process, error) {
	t.Helper()
	name, args := "sh", []string{"-c", fmt.Sprintf("exit %d", code)}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "exit", strconv.Itoa(code)}
	}
	return osutil.NewSpawner().Start(name, args...)
}

// aliveProcess 启动一个存活约 30s 的真实进程，用于测试“仍在运行”的隧道。
func aliveProcess(t *testing.T) (*osutil.Process, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return osutil.NewSpawner().Start("cmd", "/c", "timeout", "/t", "30", "/nobreak")
	}
	return osutil.NewSpawner().Start("sh", "-c", "sleep 30")
}

// freePort 返回一个当前空闲的本地端口（与既有端口测试同款取法）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// H3: spawn 后进程自行退出（如认证失败）必须被监控到——
// 状态迁移到 StateError 并发出 error 事件，而不是永远停留在 connected。
func TestStartMonitorsProcessExit(t *testing.T) {
	var mu sync.Mutex
	var emitted []Event
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			return exitProcess(t, 1)
		}},
		func(e Event) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		},
		nil,
	)
	tr := base()
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := ctrl.State(tr.ID); got != StateConnected {
		t.Fatalf("state right after start: %s", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateError {
		time.Sleep(10 * time.Millisecond)
	}
	if got := ctrl.State(tr.ID); got != StateError {
		t.Fatalf("state should be error after process exit, got %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range emitted {
		if e.SourceID == tr.ID && e.Level == "error" {
			return
		}
	}
	t.Fatalf("no error event emitted: %v", emitted)
}

// H4: 对已在运行的隧道再次 Start 必须直接报错且不动 map 条目——
// 替换条目会丢失正在运行的进程句柄（永远无法 Stop、端口持续被占）。
func TestStartTwiceRejectsSecond(t *testing.T) {
	var mu sync.Mutex
	var emitted []Event
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			return aliveProcess(t)
		}},
		func(e Event) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		},
		nil,
	)
	tr := base()
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if got := ctrl.State(tr.ID); got != StateConnected {
		t.Fatalf("state after first start: %s", got)
	}
	if err := ctrl.Start(tr); err == nil {
		t.Fatal("second start of a running tunnel must fail")
	}
	// 第一个进程仍必须可被 Stop 且状态迁移到 stopped
	if err := ctrl.Stop(tr.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("state after stop: %s", got)
	}
}

func TestCheckLocalPortFreeAndBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if !CheckLocalPort("127.0.0.1", port) {
		t.Fatal("freshly closed port should be free")
	}
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if CheckLocalPort("127.0.0.1", port) {
		t.Fatal("occupied port should report not-free")
	}
}

func TestCheckRemoteConflict(t *testing.T) {
	c1 := config.Tunnel{ID: "1", Host: "h", Mode: "remote", ListenBind: "127.0.0.1", ListenPort: 9000}
	c2 := config.Tunnel{ID: "2", Host: "h", Mode: "remote", ListenBind: "127.0.0.1", ListenPort: 9000}
	c3 := config.Tunnel{ID: "3", Host: "other", Mode: "remote", ListenBind: "127.0.0.1", ListenPort: 9000}
	if err := CheckRemoteConflict([]config.Tunnel{c1}, c2); err == nil {
		t.Fatal("same host+bind+port remote should conflict")
	}
	if err := CheckRemoteConflict([]config.Tunnel{c1}, c3); err != nil {
		t.Fatalf("different host remote should NOT conflict: %v", err)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		in   osutil.Outcome
		want string
	}{
		{osutil.Outcome{Stderr: "bind: Address already in use"}, "port already in use"},
		{osutil.Outcome{Stderr: "root@10.0.0.5: Permission denied (publickey)."}, "authentication failed — configure key/agent auth for this host"},
		{osutil.Outcome{Stderr: "ssh: Could not resolve hostname nodomain."}, "host not resolvable"},
		{osutil.Outcome{ExitCode: 1}, "unknown error (exit 1)"},
	}
	for _, c := range cases {
		if got := classifyError(c.in); got != c.want {
			t.Fatalf("classifyError(%+v)=%q want %q", c.in, got, c.want)
		}
	}
}

// 自动重连：退避序列 1s,2s,4s,8s,16s 之后固定 30s 封顶（spec §3.2）。
func TestBackoffDelaySequence(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := backoffDelay(i + 1); got != w {
			t.Fatalf("backoffDelay(%d)=%s want %s", i+1, got, w)
		}
	}
	if backoffDelay(0) != time.Second {
		t.Fatal("attempt<=0 should fall back to 1s")
	}
}
