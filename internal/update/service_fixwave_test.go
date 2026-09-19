package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// —— I-1 并发守卫回归 ——

// TestConcurrentStartDownloadOnlyOneRequest 覆盖最终评审 I-1：旧实现在锁内只读
// state 就解锁、再 go s.download，两个并发调用都会通过检查并真正并发下载
// （评审 PROBE2 实证 archiveRequests=2）。修复后守卫与置位在同一临界区。
//
// 资产处理器会阻塞到测试显式放行：这样第一个下载必定停在 in-flight，任何
// 「第二次 StartDownload 也被接受」的旧行为都会在服务器侧再计一次请求。
func TestConcurrentStartDownloadOnlyOneRequest(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("NEWBIN-v0.7.0\n")
	const archiveName = "sshore-v0.7.0-linux-amd64.tar.gz"
	tgz := tarGzWith(t, "./sshore", body)
	sum := sha256Hex(tgz)

	var archiveHits int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset":
			atomic.AddInt32(&archiveHits, 1)
			<-release
			_, _ = w.Write(tgz)
		case "/checksums.txt":
			_, _ = io.WriteString(w, sum+"  "+archiveName+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer unblock() // LIFO：先放行再 Close，避免 Close 等阻塞中的 handler

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Doer = srv.Client()
		o.Config = func() Settings { return Settings{} }
	})
	svc.mu.Lock()
	svc.rel = Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: archiveName, URL: srv.URL + "/asset", Size: int64(len(tgz))},
		{Name: "checksums.txt", URL: srv.URL + "/checksums.txt"},
	}}
	svc.info.State = StateAvailable
	svc.info.Latest = "v0.7.0"
	svc.mu.Unlock()
	_ = os.Remove(partPath())

	const n = 8
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = svc.StartDownload(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("并发 StartDownload 必须恰好 1 次被接受，实际 %d 次（errs=%v）", accepted, errs)
	}
	// 等第一次请求真正到达，再给旧实现的第二个 goroutine 一个确定的窗口。
	waitCond(t, func() bool { return atomic.LoadInt32(&archiveHits) >= 1 }, 5*time.Second)
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&archiveHits); got != 1 {
		t.Fatalf("并发 StartDownload 只应产生 1 次下载请求，实际 %d（旧实现会并发下载）", got)
	}
	unblock()
	waitState(t, svc, StateReady, 5*time.Second)
}

// TestConcurrentApplyOnlyOneLaunch 覆盖最终评审 I-1 的第二面：旧实现并发两次
// ApplyAndRestart 都会通过 ready 守卫、各启动一次替换脚本。
func TestConcurrentApplyOnlyOneLaunch(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(plan.Pending, []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := FileSHA256(plan.Pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Sidecar, []byte(hash+"  "+filepath.Base(plan.Pending)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(plan.Pending)
	if err != nil {
		t.Fatal(err)
	}
	plan.Size = st.Size()

	var launches int32
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Launch = func(string, string, []string, []string) error {
			atomic.AddInt32(&launches, 1)
			return nil
		}
	})
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = plan.Pending
	svc.pending = plan
	svc.mu.Unlock()

	const n = 8
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = svc.ApplyAndRestart(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Fatalf("并发 ApplyAndRestart 只允许启动一次脚本，实际 %d 次（errs=%v）", got, errs)
	}
	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("并发 ApplyAndRestart 必须恰好 1 次成功，实际 %d（errs=%v）", accepted, errs)
	}
	if got := svc.Info().State; got != StateApplying {
		t.Fatalf("apply 成功后状态应为 applying, got %s", got)
	}
}

// —— I-2a：dev 构建可升级 ——

// TestApplySucceedsForDevBuild 覆盖最终评审 I-2：spec §2.8/§9 要求非 release
// 构建「手动可查、可升级」，而 Compare(dev, *) 恒为 0，旧实现永远拒绝 apply。
func TestApplySucceedsForDevBuild(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := PlanFor("linux", exe, "dev", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(plan.Pending, []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := FileSHA256(plan.Pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Sidecar, []byte(hash+"  "+filepath.Base(plan.Pending)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(plan.Pending)
	if err != nil {
		t.Fatal(err)
	}
	plan.Size = st.Size()

	launched := false
	svc, _ := newSvc(t, "dev", func(o *Options) {
		o.ExePath = exe
		o.Launch = func(string, string, []string, []string) error { launched = true; return nil }
	})
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = plan.Pending
	svc.pending = plan
	svc.mu.Unlock()

	if err := svc.ApplyAndRestart(context.Background()); err != nil {
		t.Fatalf("dev 构建必须允许升级，got %v", err)
	}
	if !launched {
		t.Fatal("dev 构建 apply 必须启动替换脚本")
	}
	if got := svc.Info().State; got != StateApplying {
		t.Fatalf("state = %s, want applying", got)
	}
}

// —— I-5：非规范 tag 在下载阶段之前就被拒 ——

// TestCheckRejectsNonCanonicalTag 覆盖最终评审 I-5：tag 会经 PlanFor/filepath.Join
// 拼进待安装文件名，`a/../../x` 这类串能洗到 ExeDir 之外。Check 必须先校验
// Class(tag)==KindClean，否则 check-failed 且不得保存 Release。
func TestCheckRejectsNonCanonicalTag(t *testing.T) {
	for _, tag := range []string{"v0.7.0/../../evil", "a/../../x", "v0.8", "nightly"} {
		t.Run(tag, func(t *testing.T) {
			body := fmt.Sprintf(`{"tag_name":%q,"body":"n","published_at":"2026-09-19T00:00:00Z","assets":[`+
				`{"name":"a.tar.gz","browser_download_url":"https://github.com/x/y/releases/download/a/a.tar.gz"},`+
				`{"name":"checksums.txt","browser_download_url":"https://github.com/x/y/releases/download/a/checksums.txt"}]}`, tag)
			svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
				o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				})
			})
			info, err := svc.Check(context.Background(), true)
			if err != nil {
				t.Fatalf("Check 不应向调用方报错: %v", err)
			}
			if info.State != StateCheckFailed {
				t.Fatalf("非规范 tag %q 必须 check-failed, got %s", tag, info.State)
			}
			svc.mu.Lock()
			defer svc.mu.Unlock()
			if svc.rel.Tag != "" {
				t.Fatalf("非规范 tag 不得保存为 Release: %q", svc.rel.Tag)
			}
		})
	}
}

// —— 顺手项⑥：只读 ExeDir 变体（spec §13.5 #5）入库 ——

// TestDownloadFailsWhenExeDirReadOnly 覆盖 §13.5「ExeDir 只读 → io-failed +
// Hint=manual-upgrade，不下载」。POSIX 下用 chmod 0500 构造；root 有
// CAP_DAC_OVERRIDE 时跳过（与既有两条只读用例一致）。
func TestDownloadFailsWhenExeDirReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 目录权限语义，Windows 上无等价构造")
	}
	if os.Geteuid() == 0 {
		t.Skip("root 有 CAP_DAC_OVERRIDE，0500 构造不出只读目录")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0", ExePath: exe,
		Config: func() Settings { return Settings{} },
		Emit:   func(string, any) {},
	})
	svc.mu.Lock()
	svc.rel = Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: "sshore-v0.7.0-linux-amd64.tar.gz", URL: "https://example.com/a.tar.gz", Size: 1024},
		{Name: "checksums.txt", URL: "https://example.com/checksums.txt"},
	}}
	svc.info.State = StateAvailable
	svc.info.Latest = "v0.7.0"
	svc.mu.Unlock()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// 先恢复写权限，t.TempDir 的递归清理才能删掉这个目录（Cleanup 为 LIFO）。
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := waitStateAny(t, svc, StateIOFailed, 5*time.Second)
	if info.Hint != HintManualUpgrade {
		t.Fatalf("Hint = %q, want %q", info.Hint, HintManualUpgrade)
	}
	if !strings.Contains(info.Error, "安装目录不可写") {
		t.Fatalf("Error = %q, 应含「安装目录不可写」", info.Error)
	}
	if got := strings.Join(dirEntries(t, dir), ","); got != "sshore" {
		t.Fatalf("只读探测失败后程序目录必须零残留: %s", got)
	}
}

// —— 顺手项③：成功转移清 Hint / PendingLog ——

// TestSuccessTransitionClearsHintAndPendingLog 覆盖「安装目录不可写」提示在用户
// 修好权限后仍显示的问题：available 与 ready 两个成功终态都要清掉上一轮的
// Hint 与 PendingLog。
func TestSuccessTransitionClearsHintAndPendingLog(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("NEWBIN-v0.7.0\n")
	const archiveName = "sshore-v0.7.0-linux-amd64.tar.gz"
	tgz := tarGzWith(t, "./sshore", body)
	srv := fakeSource(t, "v0.7.0", archiveName, tgz, sha256Hex(tgz))

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Doer = srv.Client()
		o.Config = func() Settings { return Settings{Source: srv.URL} }
	})
	svc.mu.Lock()
	svc.info.State = StateIOFailed
	svc.info.Hint = HintManualUpgrade
	svc.info.PendingLog = filepath.Join(dir, "sshore-update.log")
	svc.mu.Unlock()

	if _, err := svc.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if info := svc.Info(); info.State != StateAvailable || info.Hint != "" || info.PendingLog != "" {
		t.Fatalf("→available 必须清 Hint/PendingLog: state=%s hint=%q log=%q", info.State, info.Hint, info.PendingLog)
	}

	svc.mu.Lock()
	svc.info.Hint = HintManualUpgrade
	svc.info.PendingLog = filepath.Join(dir, "sshore-update.log")
	svc.mu.Unlock()
	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := waitState(t, svc, StateReady, 5*time.Second)
	if info.Hint != "" || info.PendingLog != "" {
		t.Fatalf("→ready 必须清 Hint/PendingLog: hint=%q log=%q", info.Hint, info.PendingLog)
	}
}

// —— 测试工具 ——

// waitCond 轮询直到 cond 成立。
func waitCond(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// waitStateAny 与 waitState 相同，但不把 io-failed/verify-failed 视为立即失败。
func waitStateAny(t *testing.T, svc *Service, want string, timeout time.Duration) UpdateInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last UpdateInfo
	for time.Now().Before(deadline) {
		last = svc.Info()
		if last.State == want {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: state = %s (error=%q), want %s", last.State, last.Error, want)
	return last
}
