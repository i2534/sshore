package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 状态常量（spec §7.1）。
const (
	StateIdle         = "idle"
	StateChecking     = "checking"
	StateUpToDate     = "up-to-date"
	StateAvailable    = "available"
	StateSkipped      = "skipped"
	StateRateLimited  = "rate-limited"
	StateCheckFailed  = "check-failed"
	StateNoAsset      = "no-asset"
	StateNoChecksum   = "no-checksum"
	StateDownloading  = "downloading"
	StateVerifyFailed = "verify-failed"
	StateIOFailed     = "io-failed"
	StateReady        = "ready"
	StateApplying     = "applying"
	StateDisabled     = "disabled"
	HintManualUpgrade = "manual-upgrade"
)

// ErrBusy 表示当前状态不允许该操作（并发守卫）。
var ErrBusy = errors.New("当前状态不允许该操作")

// ErrLocked 是取 ExeDir 排他锁失败时给用户的文案（spec §7.6）。
//
// 底层错误在 Unix 是 errno、在 Windows 可能是 ACCESS_DENIED 等英文文本，
// 一律不原样抛给用户（Task 7 评审 M-4）；原始错误只经 Options.Log 进诊断日志。
var ErrLocked = errors.New("另一个实例正在升级，请稍后重试")

// Settings 是服务需要的配置子集。
type Settings struct {
	Auto     bool
	Interval time.Duration
	Source   string
	Skipped  string
}

// Options 注入全部外部依赖（测试可零网络、零进程）。
type Options struct {
	Goos, Goarch, Version, ExePath string
	Doer                           Doer
	Launch                         func(goos, path string, args, env []string) error
	Acquire                        func(exeDir string) (func(), error)
	Emit                           func(event string, payload any)
	Log                            func(msg string) // 诊断日志（门卫原因 / X-RateLimit / 自动检查失败）→ 前端日志面板
	Config                         func() Settings
	Save                           func(Settings) error
	Now                            func() time.Time
}

// UpdateInfo 是前后端唯一契约（JSON 字段见 spec §5.2）。
type UpdateInfo struct {
	Seq         int64  `json:"seq"`
	State       string `json:"state"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"published_at"`
	Source      string `json:"source"`
	Progress    int    `json:"progress"`
	ReadyPath   string `json:"ready_path"`
	Skipped     bool   `json:"skipped"`
	Manual      bool   `json:"manual"`
	Hint        string `json:"hint"`
	Error       string `json:"error"`
	PendingLog  string `json:"pending_log"`
}

// Service 是更新状态机。
type Service struct {
	opts Options

	mu        sync.Mutex
	info      UpdateInfo
	rel       Release
	checksums map[string]string
	cancelDL  context.CancelFunc
	pending   Plan
	stopCh    chan struct{}
	stopped   bool
}

// New 构造服务；零值 App 场景下也不会 panic（Info 返回 disabled 快照）。
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Acquire == nil {
		o.Acquire = Acquire
	}
	if o.Config == nil {
		o.Config = func() Settings { return Settings{} }
	}
	s := &Service{opts: o, stopCh: make(chan struct{})}
	s.info = UpdateInfo{State: StateIdle, Current: o.Version, Source: DefaultSource}
	return s
}

// Info 返回快照（含 seq，前端据此丢弃迟到载荷）。
func (s *Service) Info() UpdateInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// log 写一行诊断日志；Options.Log 为 nil 时必须安全（零依赖注入场景）。
func (s *Service) log(msg string) {
	if s.opts.Log != nil {
		s.opts.Log(msg)
	}
}

// setLocked 在持锁状态更新快照并发事件；调用方保证 s.mu 已加锁。
func (s *Service) setLocked(state, errMsg string) {
	s.info.Seq++
	s.info.State = state
	s.info.Error = errMsg
	payload := s.info
	if s.opts.Emit != nil {
		go s.opts.Emit("update:state", payload)
	}
}

// resetCapture 包一层 Doer，记录最近一次响应的 X-RateLimit-Reset。
//
// source.go（Task 3 文件）的 Client.Latest 不暴露响应头，而限流退避需要它；
// 在不改 Task 3 文件、不改 Doer 接口的前提下，只能在 Do 外层捕获。
type resetCapture struct {
	inner Doer
	mu    sync.Mutex
	reset string
}

func (c *resetCapture) Do(r *http.Request) (*http.Response, error) {
	resp, err := c.inner.Do(r)
	if resp != nil {
		if v := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); v != "" {
			c.mu.Lock()
			c.reset = v
			c.mu.Unlock()
		}
	}
	return resp, err
}

// Reset 返回捕获到的 X-RateLimit-Reset（没有则为空串）。
func (c *resetCapture) Reset() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reset
}

// resolveSource 按配置与合法性挑源（非法/非 loopback 的 http 源回退默认并记一行日志）。
func (s *Service) resolveSource() (string, string) {
	src := strings.TrimSpace(s.opts.Config().Source)
	if src == "" {
		return DefaultSource, ""
	}
	if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
		return DefaultSource, "更新源非法，已回退默认源"
	}
	if strings.HasPrefix(src, "http://") && !strings.Contains(src, "127.0.0.1") && !strings.Contains(src, "localhost") {
		return DefaultSource, "非 loopback 的更新源必须使用 https，已回退默认源"
	}
	return src, ""
}

// Check 执行一次检查；manual=false 时受门卫与轮询约束。
func (s *Service) Check(ctx context.Context, manual bool) (UpdateInfo, error) {
	s.mu.Lock()
	switch s.info.State {
	case StateDownloading, StateReady, StateApplying:
		s.mu.Unlock()
		return s.Info(), fmt.Errorf("%w: 当前状态 %s", ErrBusy, s.Info().State)
	case StateChecking:
		s.mu.Unlock()
		return s.Info(), nil
	}
	s.mu.Unlock()

	cfg := s.opts.Config()
	if !manual && (!cfg.Auto || !IsRelease(s.opts.Version)) {
		s.log("未自动检查更新（非 release 构建或已关闭自动检查）")
		s.mu.Lock()
		s.info.Manual = false
		s.setLocked(StateDisabled, "")
		s.mu.Unlock()
		return s.Info(), nil
	}
	src, warn := s.resolveSource()
	if warn != "" {
		// spec §6/§10.2：回退默认源也要写一行日志
		s.log(warn)
	}

	s.mu.Lock()
	s.info.Manual = manual
	s.info.Source = src
	if warn != "" {
		s.info.Error = warn
	}
	s.setLocked(StateChecking, warn)
	s.mu.Unlock()

	// 检查请求总超时 10s（spec §7.2.2）
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// X-RateLimit-Reset 只在响应头里；Doer 为 nil 时不包（让 Client 的 nil 检查照常生效）。
	doer := s.opts.Doer
	var rec *resetCapture
	if doer != nil {
		rec = &resetCapture{inner: doer}
		doer = rec
	}
	client := &Client{HTTP: doer, UserAgent: "sshore/" + s.opts.Version}
	rel, err := client.Latest(ctx, src)
	if err != nil {
		state, msg := StateCheckFailed, err.Error()
		if errors.Is(err, ErrRateLimited) {
			state = StateRateLimited
			reset := ""
			if rec != nil {
				reset = rec.Reset()
			}
			if reset == "" {
				reset = "未知"
			}
			// 限流必须留一行日志，并带上退避依据（spec §7.2.3）
			s.log("更新源限流（rate-limited），X-RateLimit-Reset=" + reset)
		} else {
			// 自动检查失败/限流都只在日志面板留一行（spec §7.2.3、§9）
			s.log("检查更新失败：" + msg)
		}
		s.mu.Lock()
		s.setLocked(state, msg)
		s.mu.Unlock()
		return s.Info(), nil
	}

	if IsRelease(s.opts.Version) && Compare(rel.Tag, s.opts.Version) <= 0 {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateUpToDate, "")
		s.mu.Unlock()
		return s.Info(), nil
	}

	if cfg.Skipped != "" && Base(rel.Tag) == Base(cfg.Skipped) {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.info.Skipped = true
		s.info.Notes = rel.Notes
		s.setLocked(StateSkipped, "")
		s.mu.Unlock()
		return s.Info(), nil
	}

	archive, err := PickArchive(rel, s.opts.Goos, s.opts.Goarch)
	if err != nil {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateNoAsset, err.Error())
		s.mu.Unlock()
		return s.Info(), nil
	}
	csAsset, err := PickChecksums(rel)
	if err != nil {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateNoChecksum, err.Error())
		s.mu.Unlock()
		return s.Info(), nil
	}
	if !SameOrigin(src, archive.URL) || !SameOrigin(src, csAsset.URL) {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateCheckFailed, "下载地址不在受信主机集合内")
		s.mu.Unlock()
		return s.Info(), nil
	}

	s.mu.Lock()
	s.rel = rel
	s.info.Latest = rel.Tag
	s.info.Notes = rel.Notes
	s.info.PublishedAt = rel.PublishedAt
	s.info.Skipped = false
	s.setLocked(StateAvailable, "")
	s.mu.Unlock()
	return s.Info(), nil
}

// StartDownload 在 available / verify-failed / io-failed 上启动下载（重试路径）。
func (s *Service) StartDownload(ctx context.Context) error {
	s.mu.Lock()
	switch s.info.State {
	case StateAvailable, StateVerifyFailed, StateIOFailed:
	case StateDownloading:
		s.mu.Unlock()
		return fmt.Errorf("%w: 已在下载", ErrBusy)
	default:
		state := s.info.State
		s.mu.Unlock()
		return fmt.Errorf("%w: 当前状态 %s", ErrBusy, state)
	}
	s.mu.Unlock()
	go s.download(ctx)
	return nil
}

// download 是本任务的最小骨架：置 downloading 后立即返回。
// Task 9 实现完整下载流程（可写性探测 → 磁盘空间 → 流式下载 → 校验 → 解包 → sidecar → ready）。
func (s *Service) download(ctx context.Context) {
	s.mu.Lock()
	s.info.Progress = 0
	s.setLocked(StateDownloading, "")
	s.mu.Unlock()
}

// CancelDownload 取消在飞下载（幂等）。
func (s *Service) CancelDownload() bool {
	s.mu.Lock()
	cancel := s.cancelDL
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// SkipVersion 记录「跳过此版本」；只经 Save 写配置（避免前端全量回写把它清掉）。
func (s *Service) SkipVersion(v string) error {
	s.mu.Lock()
	if s.info.State == StateApplying {
		s.mu.Unlock()
		return ErrBusy
	}
	s.mu.Unlock()
	cfg := s.opts.Config()
	cfg.Skipped = Base(v)
	if s.opts.Save != nil {
		if err := s.opts.Save(cfg); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.info.Skipped = true
	s.setLocked(StateSkipped, "")
	s.mu.Unlock()
	return nil
}

// ClearSkipped 清掉跳过并立刻重查（状态落 checking，保持与 UI 文案一致）。
func (s *Service) ClearSkipped() error {
	cfg := s.opts.Config()
	cfg.Skipped = ""
	if s.opts.Save != nil {
		if err := s.opts.Save(cfg); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.info.Skipped = false
	s.setLocked(StateChecking, "")
	s.mu.Unlock()
	_, err := s.Check(context.Background(), true)
	return err
}

// DiscardPending 删除待安装文件与 sidecar（applying 时拒绝）。
func (s *Service) DiscardPending() error {
	s.mu.Lock()
	if s.info.State == StateApplying {
		s.mu.Unlock()
		return ErrBusy
	}
	plan := s.pending
	s.mu.Unlock()
	if plan.Pending != "" {
		_ = os.Remove(plan.Pending)
		_ = os.Remove(plan.Sidecar)
	}
	s.mu.Lock()
	s.pending = Plan{}
	s.info.ReadyPath = ""
	s.info.Progress = 0
	if s.info.Latest != "" {
		s.setLocked(StateAvailable, "")
	} else {
		s.setLocked(StateIdle, "")
	}
	s.mu.Unlock()
	return nil
}

// ApplyAndRestart 是唯一允许替换二进制的入口（必须显式调用）。
func (s *Service) ApplyAndRestart(ctx context.Context) error {
	s.mu.Lock()
	if s.info.State == StateApplying {
		s.mu.Unlock()
		return ErrBusy
	}
	if s.info.State != StateReady {
		state := s.info.State
		s.mu.Unlock()
		return fmt.Errorf("%w: 需 ready，当前 %s", ErrBusy, state)
	}
	plan := s.pending
	s.mu.Unlock()

	// ① size 守卫：Size>0 时校验，Size==0（重启后由自检进入 ready）以磁盘为准
	st, err := os.Stat(plan.Pending)
	if err != nil {
		return s.failIO(fmt.Errorf("待安装文件不存在：%w", err), "")
	}
	if plan.Size > 0 && st.Size() != plan.Size {
		return s.failVerify(fmt.Errorf("待安装文件大小已变化：%d != %d", st.Size(), plan.Size))
	}
	plan.Size = st.Size()

	// ② sidecar 重算（补上「落盘后被替换」的窗口，spec §10.1）
	want, err := readSidecar(plan.Sidecar)
	if err != nil {
		return s.failVerify(fmt.Errorf("缺少或无法读取哈希 sidecar：%w", err))
	}
	if err := VerifyFile(plan.Pending, want); err != nil {
		return s.failVerify(err)
	}

	// ③ 与当前版本比较：绝不允许降级安装
	toVer, ok := ParsePendingName(filepath.Base(plan.Pending))
	if !ok || Compare(toVer, s.opts.Version) <= 0 {
		return s.failVerify(fmt.Errorf("待安装版本 %q 不高于当前版本 %q，拒绝安装", toVer, s.opts.Version))
	}

	// ④ 排他锁：底层错误（errno / ACCESS_DENIED 等英文文本）只进诊断日志，
	// 给用户的一律是 spec §7.6 的中文文案（Task 7 评审 M-4）。
	release, err := s.opts.Acquire(plan.ExeDir)
	if err != nil {
		s.log("取排他锁失败：" + err.Error())
		return s.failIO(ErrLocked, "")
	}
	defer release()

	// ⑤ 写脚本 + 分离启动
	body, err := ScriptBytes(s.opts.Goos)
	if err != nil {
		return s.failIO(err, "")
	}
	scriptPath := filepath.Join(plan.ExeDir, ScriptName(s.opts.Goos))
	mode := os.FileMode(0o755)
	if s.opts.Goos == "windows" {
		mode = 0o644
	}
	if err := os.WriteFile(scriptPath, body, mode); err != nil {
		return s.failIO(err, "")
	}
	launch := s.opts.Launch
	if launch == nil {
		launch = StartDetached
	}
	pid := os.Getpid()
	var args, env []string
	if s.opts.Goos == "windows" {
		env = ScriptEnv(plan, pid)
	} else {
		args = ScriptArgs(plan, pid)
	}
	if err := launch(s.opts.Goos, scriptPath, args, env); err != nil {
		return s.failIO(err, "")
	}

	s.mu.Lock()
	s.setLocked(StateApplying, "")
	s.mu.Unlock()
	return nil
}

// failIO / failVerify 收敛错误状态（IO 与校验失败要区分，前者可重试）。
func (s *Service) failIO(err error, hint string) error {
	s.mu.Lock()
	s.info.Hint = hint
	s.setLocked(StateIOFailed, err.Error())
	s.mu.Unlock()
	return err
}

func (s *Service) failVerify(err error) error {
	s.mu.Lock()
	s.setLocked(StateVerifyFailed, err.Error())
	s.mu.Unlock()
	return err
}

// readSidecar 读单行 sidecar（<hash> 两个空格 <文件名>）。
func readSidecar(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m, err := ParseChecksums(strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	for _, h := range m {
		return h, nil
	}
	return "", fmt.Errorf("sidecar 为空")
}
