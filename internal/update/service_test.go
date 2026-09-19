package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSvc(t *testing.T, version string, opts ...func(*Options)) (*Service, *Options) {
	t.Helper()
	o := Options{
		Goos: "linux", Goarch: "amd64", Version: version,
		ExePath: "/tmp/sshore/sshore",
		Doer:    &http.Client{},
		Launch:  func(string, string, []string, []string) error { return nil },
		Acquire: func(string) (func(), error) { return func() {}, nil },
		Emit:    func(string, any) {},
		Config:  func() Settings { return Settings{Auto: true, Interval: time.Hour, Source: ""} },
		Save:    func(Settings) error { return nil },
		Now:     time.Now,
	}
	for _, f := range opts {
		f(&o)
	}
	return New(o), &o
}

func TestDisabledGateMakesNoRequest(t *testing.T) {
	var calls int32
	svc, o := newSvc(t, "dev", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			return nil, errors.New("不该被调用")
		})
	})
	_ = o
	info, err := svc.Check(context.Background(), false)
	if err != nil {
		t.Fatalf("门卫不应报错: %v", err)
	}
	if info.State != StateDisabled {
		t.Fatalf("非 release 构建自动检查应 disabled, got %s", info.State)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("门卫拦截时不得发请求, calls=%d", n)
	}
}

func TestCheckRejectedWhileDownloadingOrReady(t *testing.T) {
	svc, _ := newSvc(t, "v0.6.0")
	svc.mu.Lock()
	svc.info.State = StateDownloading
	svc.mu.Unlock()
	if _, err := svc.Check(context.Background(), true); !errors.Is(err, ErrBusy) {
		t.Fatalf("下载中必须拒绝检查, got %v", err)
	}
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = "/tmp/sshore/sshore.v0.7.0"
	svc.mu.Unlock()
	if _, err := svc.Check(context.Background(), true); !errors.Is(err, ErrBusy) {
		t.Fatalf("ready 期间必须拒绝检查（否则会抹掉 ReadyPath）")
	}
	// applying 是终态；DoD#10 变异 ④ 就是删掉这条守卫，必须有测试抓住
	svc.mu.Lock()
	svc.info.State = StateApplying
	svc.mu.Unlock()
	if _, err := svc.Check(context.Background(), true); !errors.Is(err, ErrBusy) {
		t.Fatalf("applying 期间必须拒绝检查")
	}
}

func TestApplyRequiresReadyAndIsNotReentrant(t *testing.T) {
	svc, o := newSvc(t, "v0.6.0")
	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("非 ready 状态必须拒绝 apply")
	}
	_ = o
	svc.mu.Lock()
	svc.info.State = StateApplying
	svc.mu.Unlock()
	if err := svc.ApplyAndRestart(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("applying 必须不可重入, got %v", err)
	}
}

func TestSkipAndClearSkippedTransitions(t *testing.T) {
	var saved Settings
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Save = func(s Settings) error { saved = s; return nil }
		// fix round 1：ClearSkipped 现在会真正发起一次检查，用假 Doer 杜绝测试依赖真实网络。
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("测试不访问网络")
		})
	})
	svc.mu.Lock()
	svc.info.State = StateAvailable
	svc.info.Latest = "v0.7.0"
	svc.mu.Unlock()
	if err := svc.SkipVersion("v0.7.0"); err != nil {
		t.Fatal(err)
	}
	if saved.Skipped != "0.7.0" {
		t.Fatalf("跳过版本必须存 Base 形式（与 Check 的 Base(tag)==Base(Skipped) 对齐）: %+v", saved)
	}
	if got := svc.Info().State; got != StateSkipped {
		t.Fatalf("跳过后的状态应为 skipped, got %s", got)
	}
	if err := svc.ClearSkipped(); err != nil {
		t.Fatal(err)
	}
	if saved.Skipped != "" {
		t.Fatalf("取消跳过未清字段: %+v", saved)
	}
}

// fix round 1：ClearSkipped 必须真正触发一次重查（spec §7.1/§12.1）。
//
// 旧实现先 setLocked(StateChecking) 再调 Check，而 Check 开头对 checking 直接
// 短路：0 次请求、状态永久停在 checking。本测试在旧实现下必红。
func TestClearSkippedPerformsExactlyOneCheck(t *testing.T) {
	var calls int32
	saved := Settings{Auto: true, Interval: time.Hour}
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Config = func() Settings { return saved }
		o.Save = func(s Settings) error { saved = s; return nil }
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			return latestOKResponse("v0.7.0"), nil
		})
	})

	// 完整链路：先跳过 v0.7.0（Config 里落 Base 形式），再取消跳过。
	svc.mu.Lock()
	svc.info.State = StateAvailable
	svc.info.Latest = "v0.7.0"
	svc.mu.Unlock()
	if err := svc.SkipVersion("v0.7.0"); err != nil {
		t.Fatal(err)
	}
	if saved.Skipped != "0.7.0" {
		t.Fatalf("SkipVersion 必须存 Base 形式: %+v", saved)
	}
	if got := svc.Info().State; got != StateSkipped {
		t.Fatalf("SkipVersion 后状态应为 skipped, got %s", got)
	}

	if err := svc.ClearSkipped(); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("取消跳过必须恰好触发 1 次检查请求, calls=%d（0 说明 Check 被 checking 守卫短路）", n)
	}
	if got := svc.Info().State; got == StateChecking {
		t.Fatalf("ClearSkipped 结束后不得停在 checking，说明这次重查没有发生")
	}
	if got := svc.Info().State; got != StateAvailable {
		t.Fatalf("假响应最新版 v0.7.0 时应落 available, got %s", got)
	}
	if saved.Skipped != "" {
		t.Fatalf("取消跳过未清字段: %+v", saved)
	}
	if svc.Info().Skipped {
		t.Fatalf("取消跳过时 UpdateInfo.Skipped 必须复位")
	}
}

// latestOKResponse 构造一次 200 的 /releases/latest 响应（含本平台产物与校验文件）。
func latestOKResponse(tag string) *http.Response {
	body := `{"tag_name":"` + tag + `","body":"note","published_at":"2026-09-19T00:00:00Z","assets":[` +
		`{"name":"sshore-` + tag + `-linux-amd64.tar.gz","browser_download_url":"https://github.com/x/y/releases/download/` + tag + `/a.tar.gz"},` +
		`{"name":"checksums.txt","browser_download_url":"https://github.com/x/y/releases/download/` + tag + `/checksums.txt"}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// doerFunc 让测试用函数替代 http.Client。
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// —— 以下为本任务裁定（父级决定 1/2/7）对应的回归测试；brief 正文未包含，但不改动其任何期望 ——

// 裁定 2 / Task 7 评审 M-4：取锁失败给用户的必须是 spec §7.6 的中文文案，
// 底层 errno / ACCESS_DENIED 只能进诊断日志。
func TestApplyLockFailureGivesChineseMessage(t *testing.T) {
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

	var logs []string
	launched := false
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.ExePath = exe
		o.Log = func(m string) { logs = append(logs, m) }
		o.Acquire = func(string) (func(), error) {
			return nil, errors.New("CreateMutex: ACCESS_DENIED (errno 5)")
		}
		o.Launch = func(string, string, []string, []string) error { launched = true; return nil }
	})
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = plan.Pending
	svc.pending = plan
	svc.mu.Unlock()

	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("取锁失败必须报错")
	}
	info := svc.Info()
	if info.State != StateIOFailed {
		t.Fatalf("取锁失败应落 io-failed, got %s", info.State)
	}
	if !strings.Contains(info.Error, "另一个实例正在升级，请稍后重试") {
		t.Fatalf("用户文案必须是 spec §7.6 的中文文案, got %q", info.Error)
	}
	if strings.Contains(info.Error, "ACCESS_DENIED") || strings.Contains(info.Error, "errno") {
		t.Fatalf("不得把英文底层错误原样抛给用户: %q", info.Error)
	}
	if launched {
		t.Fatal("取锁失败不得启动脚本")
	}
	if len(logs) == 0 {
		t.Fatal("取锁失败必须写一行诊断日志")
	}
}

// 裁定 1：rate-limited 必须写一行日志，并记录 x-ratelimit-reset 作为退避依据。
func TestCheckRateLimitedLogsResetHeader(t *testing.T) {
	var logs []string
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			h := http.Header{}
			h.Set("X-RateLimit-Remaining", "0")
			h.Set("X-RateLimit-Reset", "1700000000")
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     h,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})
		o.Log = func(m string) { logs = append(logs, m) }
	})
	info, err := svc.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("限流由状态机承载，不应向调用方报错: %v", err)
	}
	if info.State != StateRateLimited {
		t.Fatalf("state = %s, want rate-limited", info.State)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "rate-limited") {
		t.Fatalf("限流必须写一行日志: %q", joined)
	}
	if !strings.Contains(joined, "1700000000") {
		t.Fatalf("限流日志必须记录 X-RateLimit-Reset: %q", joined)
	}
}

// 裁定 1：Log == nil 时任何路径都必须安全（这里覆盖 rate-limited）。
func TestCheckRateLimitedNilLogIsSafe(t *testing.T) {
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})
		// Log 保持 nil
	})
	info, err := svc.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Log 为 nil 时不得报错: %v", err)
	}
	if info.State != StateRateLimited {
		t.Fatalf("state = %s, want rate-limited", info.State)
	}
}

// 裁定 7 / Task 7 评审 M-3：任何代码都不得删除 .sshore-update.lock。
func TestDiscardPendingKeepsExclusiveLockFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(plan.Pending, []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Sidecar, []byte("x  y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) { o.ExePath = exe })
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.Latest = "v0.7.0"
	svc.pending = plan
	svc.mu.Unlock()

	if err := svc.DiscardPending(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Pending); !os.IsNotExist(err) {
		t.Fatal("DiscardPending 必须删掉待安装文件")
	}
	if _, err := os.Stat(plan.Sidecar); !os.IsNotExist(err) {
		t.Fatal("DiscardPending 必须删掉 sidecar")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("不得删除 .sshore-update.lock（会引入双持锁漏洞）: %v", err)
	}
	if got := svc.Info().State; got != StateAvailable {
		t.Fatalf("state = %s, want available", got)
	}
}
