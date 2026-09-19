package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitSelfCheckClassifiesPendingVersusBackup(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"sshore", "sshore.v0.6.0", "sshore.v0.6.0-80-gc2d2a36", "sshore.v0.7.0"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0",
		ExePath: filepath.Join(dir, "sshore"),
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	info := svc.Info()
	if info.State != StateReady {
		t.Fatalf("state = %s, want ready", info.State)
	}
	if filepath.Base(info.ReadyPath) != "sshore.v0.7.0" {
		t.Fatalf("ready_path = %s", info.ReadyPath)
	}
	if svc.pending.Size != 0 {
		t.Fatalf("自检进入 ready 时 Size 必须以磁盘为准（置 0），got %d", svc.pending.Size)
	}
}

func TestInitSelfCheckTreatsStaleOKLogAsStale(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "sshore"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sshore-update.log"), []byte("RESULT=ok\n"), 0o644)
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.7.0",
		ExePath: filepath.Join(dir, "sshore"),
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	if _, err := os.Stat(filepath.Join(dir, "sshore-update.log")); !os.IsNotExist(err) {
		t.Fatal("RESULT=ok 的陈旧日志必须被清理")
	}
	if got := svc.Info().PendingLog; got != "" {
		t.Fatalf("陈旧日志不应触发「上次升级未完成」: %s", got)
	}
}

func TestInitSelfCheckSurfacesFailedLog(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "sshore"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sshore-update.log"), []byte("STEP=8 ERR=x\nRESULT=fail:launch\n"), 0o644)
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.7.0",
		ExePath: filepath.Join(dir, "sshore"),
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	if svc.Info().PendingLog == "" {
		t.Fatal("失败日志必须暴露给界面（pending_log）")
	}
	if got := svc.Info().Error; !strings.Contains(got, "上次升级未完成") {
		t.Fatalf("失败日志必须把中文文案写进 Error, got %q", got)
	}
}

func TestReconfigureRebuildsTickerWithNewInterval(t *testing.T) {
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0",
		ExePath: filepath.Join(t.TempDir(), "sshore"),
		Config:  func() Settings { return Settings{Auto: true, Interval: time.Hour} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	svc.Reconfigure() // 只断言不 panic；调度本身由人工/端到端覆盖
	svc.Shutdown()
}

func TestApplyRejectsTamperedPendingSidecar(t *testing.T) {
	// spec §10.1 的第二道校验，也是 DoD#10 变异 ③ 的对应测试：
	// 只要把 ApplyAndRestart 里的 VerifyFile 去掉，本测试就必须失败。
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(plan.Pending, []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(plan.Pending)
	plan.Size = st.Size()
	launched := false
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0", ExePath: exe,
		Acquire: func(string) (func(), error) { return func() {}, nil },
		Launch:  func(string, string, []string, []string) error { launched = true; return nil },
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = plan.Pending
	svc.pending = plan
	svc.mu.Unlock()

	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("sidecar 缺失必须拒绝 apply")
	}
	if launched {
		t.Fatal("被拒时不得启动脚本")
	}
	sidecar := strings.Repeat("0", 64) + "  " + filepath.Base(plan.Pending) + "\n"
	if err := os.WriteFile(plan.Sidecar, []byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.mu.Unlock()
	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("哈希不符必须拒绝 apply")
	}
	if got := svc.Info().State; got != StateVerifyFailed {
		t.Fatalf("state = %s, want verify-failed", got)
	}
	if launched {
		t.Fatal("校验失败时不得启动脚本")
	}
}

// —— 以下为本任务（Task 9）补充的集成测试：完整下载链路、校验失败、取消与限流调度 ——

// stateEvent 记录一次 update:state 事件（含 seq，用于钉死时序）。
type stateEvent struct {
	seq   int64
	state string
}

func TestLastResultProtocol(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{"无日志", "", ""},
		{"成功末行", "STEP=1\nRESULT=ok\n", "ok"},
		{"失败末行", "STEP=8 ERR=x\nRESULT=fail:launch\n", "fail:launch"},
		{"末行非协议行", "RESULT=ok\nlast line\n", ""},
		{"带空白", "  RESULT=ok  \n", "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, "log-"+c.name)
			if c.body != "" {
				if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := lastResult(p); got != c.want {
				t.Fatalf("lastResult = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDownloadSuccessWritesPendingSidecarAndNoResidue(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("NEWBIN\x00\x01\x02v0.7.0\n")
	const archiveName = "sshore-v0.7.0-linux-amd64.tar.gz"
	tgz := tarGzWith(t, "./sshore", body)
	srv := fakeSource(t, "v0.7.0", archiveName, tgz, sha256Hex(tgz))

	var evMu sync.Mutex
	var events []stateEvent
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Doer = srv.Client()
		o.Config = func() Settings { return Settings{Source: srv.URL} }
		o.Emit = func(name string, payload any) {
			if name != "update:state" {
				return
			}
			if info, ok := payload.(UpdateInfo); ok {
				evMu.Lock()
				events = append(events, stateEvent{seq: info.Seq, state: info.State})
				evMu.Unlock()
			}
		}
	})

	_ = os.Remove(partPath())
	if _, err := svc.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := svc.Info().State; got != StateAvailable {
		t.Fatalf("check state = %s, want available", got)
	}
	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, &evMu, &events, StateReady, 5*time.Second)
	info := waitState(t, svc, StateReady, 5*time.Second)

	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if info.ReadyPath != plan.Pending {
		t.Fatalf("ready_path = %s, want %s", info.ReadyPath, plan.Pending)
	}
	got, err := os.ReadFile(plan.Pending)
	if err != nil {
		t.Fatalf("pending 必须存在: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("pending 内容与 fixture 不符: %q", got)
	}
	sidecar, err := os.ReadFile(plan.Sidecar)
	if err != nil {
		t.Fatalf("sidecar 必须存在: %v", err)
	}
	m, err := ParseChecksums(bytes.NewReader(sidecar))
	if err != nil {
		t.Fatalf("sidecar 不可解析: %v", err)
	}
	if want := sha256Hex(body); m[filepath.Base(plan.Pending)] != want {
		t.Fatalf("sidecar 哈希不符: got %q want %q", m[filepath.Base(plan.Pending)], want)
	}
	waitGone(t, partPath(), 3*time.Second)
	if names := strings.Join(dirEntries(t, dir), ","); names != "sshore,sshore.v0.7.0,sshore.v0.7.0.sha256" {
		t.Fatalf("ExeDir 残留不符: %s", names)
	}
	assertStateOrder(t, &evMu, &events, StateDownloading, StateReady)
}

func TestDownloadVerifyFailureLeavesProgramDirClean(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("NEWBIN-v0.7.0\n")
	const archiveName = "sshore-v0.7.0-linux-amd64.tar.gz"
	tgz := tarGzWith(t, "./sshore", body)
	srv := fakeSource(t, "v0.7.0", archiveName, tgz, flipHex(sha256Hex(tgz)))

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Doer = srv.Client()
		o.Config = func() Settings { return Settings{Source: srv.URL} }
	})
	_ = os.Remove(partPath())
	if _, err := svc.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitState(t, svc, StateVerifyFailed, 5*time.Second)

	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if _, err := os.Stat(plan.Pending); !os.IsNotExist(err) {
		t.Fatalf("校验失败后 pending 必须不存在（程序目录零残留）: %v", err)
	}
	if _, err := os.Stat(plan.Sidecar); !os.IsNotExist(err) {
		t.Fatalf("校验失败后不得写 sidecar: %v", err)
	}
	if names := strings.Join(dirEntries(t, dir), ","); names != "sshore" {
		t.Fatalf("校验失败后程序目录必须零残留: %s", names)
	}
	waitGone(t, partPath(), 3*time.Second)
}

// TestDownloadFailsWhenFreeSpaceBelowAssetSize 钉死 spec §7.3.2：可用空间需 ≥
// 资产大小 + 64MiB，否则 io-failed。用一个远大于任何真实磁盘可用空间的假 Size
// （1<<62 字节 ≈ 4 EiB）触发该分支，测试不与真实磁盘容量耦合，也无需注入 FreeSpace。
func TestDownloadFailsWhenFreeSpaceBelowAssetSize(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const archiveName = "sshore-v0.7.0-linux-amd64.tar.gz"
	const hugeSize = int64(1) << 62

	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0", ExePath: exe,
		Config: func() Settings { return Settings{} },
		Emit:   func(string, any) {},
	})
	// 直接塞入带巨大 size 的 release（空间检查发生在任何网络请求之前）。
	svc.mu.Lock()
	svc.rel = Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: archiveName, URL: "https://example.com/a.tar.gz", Size: hugeSize},
		{Name: "checksums.txt", URL: "https://example.com/checksums.txt"},
	}}
	svc.info.State = StateAvailable
	svc.info.Latest = "v0.7.0"
	svc.mu.Unlock()

	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var info UpdateInfo
	for time.Now().Before(deadline) {
		info = svc.Info()
		if info.State == StateIOFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if info.State != StateIOFailed {
		t.Fatalf("state = %s (error=%q), want io-failed", info.State, info.Error)
	}
	if !strings.Contains(info.Error, "磁盘可用空间不足") {
		t.Fatalf("Error = %q, 应含中文空间不足文案", info.Error)
	}
	if want := strconv.FormatInt(hugeSize+64<<20, 10); !strings.Contains(info.Error, want) {
		t.Fatalf("Error = %q, 应含所需字节数 %s", info.Error, want)
	}
	if names := strings.Join(dirEntries(t, dir), ","); names != "sshore" {
		t.Fatalf("空间不足时程序目录必须零残留: %s", names)
	}
}

func TestDownloadCancelReturnsAvailableAndRemovesPart(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const archiveName = "sshore-v0.7.0-linux-amd64.tar.gz"
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tag_name":"v0.7.0","body":"n","published_at":"2026-09-19T00:00:00Z","assets":[`+
				`{"name":"`+archiveName+`","browser_download_url":"`+srv.URL+`/asset"},`+
				`{"name":"checksums.txt","browser_download_url":"`+srv.URL+`/checksums.txt"}]}`)
		case "/asset":
			w.Header().Set("Content-Length", "1048576")
			_, _ = w.Write(bytes.Repeat([]byte("A"), 64*1024))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case "/checksums.txt":
			_, _ = io.WriteString(w, sha256Hex(nil)+"  "+archiveName+"\n")
		}
	}))
	defer srv.Close()
	defer stop()

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Doer = srv.Client()
		o.Config = func() Settings { return Settings{Source: srv.URL} }
	})
	_ = os.Remove(partPath())
	if _, err := svc.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitExists(t, partPath(), 5*time.Second)
	if !svc.CancelDownload() {
		t.Fatal("CancelDownload 应返回 true（在飞下载）")
	}
	waitState(t, svc, StateAvailable, 5*time.Second)
	waitGone(t, partPath(), 3*time.Second)
	if got := svc.Info().Progress; got != 0 {
		t.Fatalf("取消后进度必须清零, got %d", got)
	}
}

// TestAutoCheckSkipsUntilRateLimitReset 钉死裁定 7：rate-limited 记录 x-ratelimit-reset，
// 并在该时刻前不自动重试。
func TestAutoCheckSkipsUntilRateLimitReset(t *testing.T) {
	var calls int32
	reset := time.Now().Add(time.Hour).Unix()
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			h := http.Header{}
			h.Set("X-RateLimit-Remaining", "0")
			h.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     h,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})
	})
	if _, err := svc.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := svc.Info().State; got != StateRateLimited {
		t.Fatalf("state = %s, want rate-limited", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("手动检查应恰好发 1 次请求, calls=%d", n)
	}
	svc.autoCheck(context.Background())
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("限流解除前不得自动重试, calls=%d", n)
	}
	svc.mu.Lock()
	svc.rateReset = time.Now().Add(-time.Minute)
	svc.mu.Unlock()
	svc.autoCheck(context.Background())
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("限流解除后应恢复自动检查, calls=%d", n)
	}
}

// —— 测试工具 ——

// fakeSource 起一个同时提供 /releases/latest、资产与 checksums.txt 的本地源。
// 资产 URL 与源同主机，SameOrigin 通过。
func fakeSource(t *testing.T, tag, archiveName string, asset []byte, sum string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tag_name":"`+tag+`","body":"note","published_at":"2026-09-19T00:00:00Z","assets":[`+
				`{"name":"`+archiveName+`","browser_download_url":"`+srv.URL+`/asset"},`+
				`{"name":"checksums.txt","browser_download_url":"`+srv.URL+`/checksums.txt"}]}`)
		case "/asset":
			_, _ = w.Write(asset)
		case "/checksums.txt":
			_, _ = io.WriteString(w, sum+"  "+archiveName+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// tarGzWith 生成含单个常规文件条目的 tar.gz（真实 fixture，非手工拼接字节）。
func tarGzWith(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// flipHex 改动一个十六进制字符（保持长度），用于构造「哈希改一位」的坏校验文件。
func flipHex(h string) string {
	b := []byte(h)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}

// partPath 与 download 的实现同名同算法（同进程内可确定复现）。
func partPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("sshore-update-%d.part", os.Getpid()))
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func waitState(t *testing.T, svc *Service, want string, timeout time.Duration) UpdateInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last UpdateInfo
	for time.Now().Before(deadline) {
		last = svc.Info()
		if last.State == want {
			return last
		}
		switch last.State {
		case StateIOFailed, StateVerifyFailed, StateCheckFailed:
			t.Fatalf("state = %s (error=%q), want %s", last.State, last.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: state = %s (error=%q), want %s", last.State, last.Error, want)
	return last
}

func waitEvent(t *testing.T, mu *sync.Mutex, events *[]stateEvent, state string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, e := range *events {
			if e.state == state {
				found = true
				break
			}
		}
		mu.Unlock()
		if found {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("未收到 %s 的 update:state 事件", state)
}

func assertStateOrder(t *testing.T, mu *sync.Mutex, events *[]stateEvent, before, after string) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	var bSeq, aSeq int64 = -1, -1
	for _, e := range *events {
		if e.state == before && (bSeq < 0 || e.seq < bSeq) {
			bSeq = e.seq
		}
		if e.state == after && e.seq > aSeq {
			aSeq = e.seq
		}
	}
	if bSeq < 0 || aSeq < 0 {
		t.Fatalf("缺少 %s/%s 事件: %+v", before, after, *events)
	}
	if bSeq >= aSeq {
		t.Fatalf("%s(seq=%d) 必须先于 %s(seq=%d)", before, bSeq, after, aSeq)
	}
}

func waitExists(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("文件未出现: %s", path)
}

func waitGone(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("文件未被删除: %s", path)
}
