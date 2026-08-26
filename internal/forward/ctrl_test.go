package forward

import (
	"fmt"
	"net"
	"reflect"
	"runtime"
	"strconv"
	"strings"
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

// 自动重连：AutoReconnect=true 的隧道意外退出后进入 reconnecting，
// 退避 1s 后首次重试即成功 → 最终 connected，事件含进入重连与 reconnected。
func TestAutoReconnectSuccessAfterFirstRetry(t *testing.T) {
	var mu sync.Mutex
	var emitted []Event
	var slept []time.Duration
	calls := 0
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return exitProcess(t, 1) // 首启进程立即死亡
			}
			return aliveProcess(t) // 第一次重试即成功
		}},
		func(e Event) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		},
		func(d time.Duration) <-chan struct{} { // 假时钟：零等待，记录序列
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	tr := base()
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateConnected {
		time.Sleep(5 * time.Millisecond)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		reached := calls >= 2
		mu.Unlock()
		if reached {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	if calls < 2 {
		t.Fatalf("expected a retry spawn, calls=%d", calls)
	}
	mu.Unlock()
	if got := ctrl.State(tr.ID); got != StateConnected {
		t.Fatalf("state should be connected after successful retry, got %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) < 1 || slept[0] != time.Second {
		t.Fatalf("first backoff sleep should be 1s, got %v", slept)
	}
	var sawWarn, sawReconnected bool
	for _, e := range emitted {
		if e.Level == "warn" && strings.Contains(e.Message, "第 1 次重连") {
			sawWarn = true
		}
		if e.Level == "info" && strings.Contains(e.Message, "reconnected") {
			sawReconnected = true
		}
	}
	if !sawWarn || !sawReconnected {
		t.Fatalf("missing events; warn=%v reconnected=%v all=%+v", sawWarn, sawReconnected, emitted)
	}
}

// 自动重连：死→死→活 两轮失败后退避序列应为 [1s,2s]，
// warn 事件逐次携带尝试序号，最终 connected。
func TestAutoReconnectBackoffSequenceAcrossRetries(t *testing.T) {
	var mu sync.Mutex
	var emitted []Event
	var slept []time.Duration
	calls := 0
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			switch n {
			case 1, 2:
				return exitProcess(t, 1)
			default:
				return aliveProcess(t)
			}
		}},
		func(e Event) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		},
		func(d time.Duration) <-chan struct{} {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	tr := base()
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 轮询与最终断言均在 mu 内快照读取（Task 3 同款锁快照），避免 -race 数据竞争；
	// 额外等待 reconnected 事件落账，保证随后的事件顺序断言读到完整序列。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		reached := calls >= 3
		hasReconnected := false
		for _, e := range emitted {
			if e.Level == "info" && strings.Contains(e.Message, "reconnected") {
				hasReconnected = true
				break
			}
		}
		mu.Unlock()
		if reached && hasReconnected {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Fatalf("expected third spawn (successful retry), calls=%d", calls)
	}
	if len(slept) < 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("backoff sleeps should be [1s,2s,...], got %v", slept)
	}
	warnCount := 0
	firstWarnIdx := -1
	reconnectedIdx := -1
	for i, e := range emitted {
		if e.Level == "warn" && strings.Contains(e.Message, "次重连") {
			if firstWarnIdx == -1 {
				firstWarnIdx = i
			}
			warnCount++
		}
		if e.Level == "info" && strings.Contains(e.Message, "reconnected") && reconnectedIdx == -1 {
			reconnectedIdx = i
		}
	}
	if warnCount < 2 {
		t.Fatalf("expected >=2 entering-reconnect warns, got %d in %+v", warnCount, emitted)
	}
	// 事件顺序（spec §7.1）：首个进入重连 warn 必须先于对应的 reconnected info。
	if firstWarnIdx == -1 || reconnectedIdx == -1 || firstWarnIdx >= reconnectedIdx {
		t.Fatalf("first entering-reconnect warn must precede the reconnected info; warnIdx=%d reconnectedIdx=%d emitted=%+v",
			firstWarnIdx, reconnectedIdx, emitted)
	}
}

// 自动重连防抖：连续在线 <60s 再次断开 → 计数不清零；
// ≥60s 后再断开 → 计数从 1 重来（spec §3.4）。
func TestAutoReconnectFlappingGuard(t *testing.T) {
	newEntry := func(stable time.Duration, attempts int) *process {
		e := &process{
			state:        StateConnected,
			cancel:       make(chan struct{}),
			tunnel:       config.Tunnel{ID: "f1", AutoReconnect: true},
			lastStableAt: time.Now().Add(-stable),
			attempts:     attempts,
		}
		return e
	}
	// 场景一：在线 59s，历史已有 3 次失败 → 计数保留（不清零）
	e1 := newEntry(59*time.Second, 3)
	got1 := nextAttemptAfterCrash(e1)
	if got1 != 4 {
		t.Fatalf("flapping (<60s) must keep counter: want attempt 4, got %d", got1)
	}
	// 场景二：在线 61s，历史 3 次失败 → 清零，下一次从 1 开始
	e2 := newEntry(61*time.Second, 3)
	got2 := nextAttemptAfterCrash(e2)
	if got2 != 1 {
		t.Fatalf("stable (>=60s) must reset counter: want attempt 1, got %d", got2)
	}
}

// nextAttemptAfterCrash 复刻 watchExit 断开瞬间的计数判定，供防抖断言复用。
func nextAttemptAfterCrash(e *process) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.lastStableAt) >= stableThreshold {
		e.attempts = 0
	}
	e.attempts++
	return e.attempts
}

// 自动重连取消：退避等待期间 Stop 必须令循环立即收尾为 stopped，
// 且不再发起任何重试 spawn。
func TestAutoReconnectStopDuringBackoff(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{}) // 手控第一段退避等待
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return exitProcess(t, 1)
			}
			t.Fatal("must not spawn again after stop")
			return nil, nil
		}},
		func(e Event) {},
		func(d time.Duration) <-chan struct{} {
			return release // 第一段退避挂起直到测试放行
		},
	)
	tr := base()
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 等待进入 reconnecting
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateReconnecting {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ctrl.State(tr.ID); got != StateReconnecting {
		t.Fatalf("should be reconnecting, got %s", got)
	}
	if err := ctrl.Stop(tr.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	close(release) // 放行被挂起的等待
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateStopped {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("state should be stopped after cancel, got %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("no retry spawn may happen after stop, calls=%d", calls)
	}
}

// post-spawn 终检·孤儿分支：重连 spawn 成功瞬间条目已被外部 Start 替换，
// 循环必须 Kill 自己刚 spawn 的进程并退出——绝不允许不可停止的孤儿进程存活。
func TestRespawnOrphanKilledWhenEntryReplaced(t *testing.T) {
	calls := 0
	var secondProc *osutil.Process
	var mu sync.Mutex
	ctrl := NewCtrl(
		fakeSpawner{}, // startFunc 稍后注入（需要引用 ctrl）
		func(e Event) {},
		func(d time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	sp := fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return exitProcess(t, 1)
		}
		p, err := aliveProcess(t)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		secondProc = p
		mu.Unlock()
		// 在重连 spawn 成功的同时，模拟外部 Start 已抢先整体替换了条目
		ctrl.mu.Lock()
		ctrl.procs["o1"] = &process{state: StateError, args: nil}
		ctrl.mu.Unlock()
		return p, nil
	}}
	ctrl.spawner = sp

	tr := base()
	tr.ID = "o1"
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 等待重连循环因身份失配退出
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c, p := calls, secondProc
		mu.Unlock()
		if c >= 2 && p != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	p := secondProc
	mu.Unlock()
	if p == nil {
		t.Fatal("retry spawn never happened")
	}
	// 被 adopt 失败的新进程必须已被 Kill：Wait 应在短时间内返回
	// （osutil.Process.Wait 是阻塞函数而非通道，用 goroutine + done 通道转成可超时等待）。
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-done:
		// killed as expected
	case <-time.After(3 * time.Second):
		t.Fatal("orphaned respawn process was not killed")
	}
	if got := ctrl.State("o1"); got != StateError {
		t.Fatalf("map entry should remain the external replacement (error), got %s", got)
	}
}

// post-spawn 终检·复活分支：trySpawn 执行期间 Stop 关闭 cancel，
// 循环不得写回 connected——用户刚停掉的隧道不允许原地复活。
func TestRespawnNoResurrectionAfterStopDuringSpawn(t *testing.T) {
	calls := 0
	stopCalled := false
	var mu sync.Mutex
	ctrl := NewCtrl(
		fakeSpawner{},
		func(e Event) {},
		func(d time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	sp := fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return exitProcess(t, 1)
		}
		// 第二次 spawn（重连尝试）进行中触发 Stop：关闭 cancel + 状态改写
		mu.Lock()
		if !stopCalled {
			stopCalled = true
			mu.Unlock()
			_ = ctrl.Stop("r1") // 原地变更：指针不变，仅凭身份校验发现不了
		} else {
			mu.Unlock()
		}
		return aliveProcess(t)
	}}
	ctrl.spawner = sp

	tr := base()
	tr.ID = "r1"
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 等 Stop 生效后的最终态
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateStopped {
		time.Sleep(5 * time.Millisecond)
	}
	// 给可能错误的"复活写入"留出暴露时间
	time.Sleep(100 * time.Millisecond)
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("stopped tunnel must not resurrect, got %s", got)
	}
}
