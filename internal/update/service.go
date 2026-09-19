package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	// rateReset 是限流解除时刻（来自 X-RateLimit-Reset）；在该时刻前不自动重试（spec §7.2.3）。
	rateReset time.Time
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
			// 记录 reset 作为退避依据：在该时刻前不自动重试（裁定 7 / spec §7.2.3）
			s.setRateReset(reset)
			if reset == "" {
				reset = "未知"
			}
			// 限流必须留一行日志，并带上退避依据（spec §7.2.3）
			s.log("更新源限流（rate-limited），X-RateLimit-Reset=" + reset)
		} else {
			s.clearRateReset()
			// 自动检查失败/限流都只在日志面板留一行（spec §7.2.3、§9）
			s.log("检查更新失败：" + msg)
		}
		s.mu.Lock()
		s.setLocked(state, msg)
		s.mu.Unlock()
		return s.Info(), nil
	}
	// 拿到正常响应即清掉限流退避（否则旧的 reset 会永久挡住自动检查）。
	s.clearRateReset()

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

// lastResult 读日志末行：返回 "ok"、"fail:<step>" 或 ""（无日志/空文件）。
func lastResult(logPath string) string {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(last, "RESULT=") {
		return strings.TrimPrefix(last, "RESULT=")
	}
	return ""
}

// Init 做残留自检并按配置调度（ctx 来自 startup）。
//
// 自检只读扫描；唯一允许的写操作是删除 RESULT=ok 的陈旧日志（spec §7.5）。
func (s *Service) Init(ctx context.Context) {
	dir := filepath.Dir(s.opts.ExePath)
	if plan, ok := ResumePending(dir, s.opts.Version); ok {
		ver, _ := ParsePendingName(filepath.Base(plan.Pending))
		s.mu.Lock()
		s.pending = plan
		s.info.ReadyPath = plan.Pending
		s.info.Latest = ver
		s.setLocked(StateReady, "")
		s.mu.Unlock()
	}
	logPath := filepath.Join(dir, "sshore-update.log")
	switch res := lastResult(logPath); {
	case res == "ok":
		_ = os.Remove(logPath)
	case strings.HasPrefix(res, "fail:"):
		s.mu.Lock()
		s.info.PendingLog = logPath
		s.info.Error = "上次升级未完成（" + res + "）"
		s.mu.Unlock()
	}
	go s.loop(ctx)
}

// loop 负责首次延迟 5s 检查与后续轮询。
// 注意：stopCh 会被 Reconfigure 替换，循环里必须每次持锁读一次本地副本，避免与替换竞争。
func (s *Service) loop(ctx context.Context) {
	cfg := s.opts.Config()
	if !cfg.Auto || !IsRelease(s.opts.Version) {
		return
	}
	stopCh := s.currentStop() // 持锁读：Reconfigure 会替换该字段，直接读会与写竞争（-race 会报）
	select {
	case <-time.After(5 * time.Second):
	case <-stopCh:
		return
	}
	s.autoCheck(ctx)
	if cfg.Interval <= 0 {
		return
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		stopCh := s.currentStop()
		select {
		case <-ticker.C:
			s.autoCheck(ctx)
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// autoCheck 是调度路径上的检查：限流未解除前直接跳过，不做任何请求（spec §7.2.3）。
func (s *Service) autoCheck(ctx context.Context) {
	s.mu.Lock()
	until := s.rateReset
	s.mu.Unlock()
	if !until.IsZero() && s.opts.Now().Before(until) {
		s.log("更新源限流未解除，跳过本次自动检查（至 " + until.Format(time.RFC3339) + "）")
		return
	}
	_, _ = s.Check(ctx, false)
}

// setRateReset 记录限流解除时刻（X-RateLimit-Reset 是 Unix 秒；无法解析时不记录）。
func (s *Service) setRateReset(raw string) {
	sec, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || sec <= 0 {
		return
	}
	s.mu.Lock()
	s.rateReset = time.Unix(sec, 0)
	s.mu.Unlock()
}

// clearRateReset 在收到非限流响应后清掉退避时刻。
func (s *Service) clearRateReset() {
	s.mu.Lock()
	s.rateReset = time.Time{}
	s.mu.Unlock()
}

// Reconfigure 在设置保存后重建调度。
func (s *Service) Reconfigure() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	close(s.stopCh)
	s.stopCh = make(chan struct{})
	s.mu.Unlock()
	go s.loop(context.Background())
}

// Shutdown 停掉调度与在飞下载。
func (s *Service) Shutdown() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	if s.cancelDL != nil {
		s.cancelDL()
	}
	close(s.stopCh)
	s.mu.Unlock()
}

// currentStop 在锁内读 stopCh：Reconfigure 会替换该字段，直接读会与写竞争（-race 会报）。
func (s *Service) currentStop() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopCh
}

// download 执行下载 → 校验 → 解包 → sidecar → ready。
func (s *Service) download(ctx context.Context) {
	s.mu.Lock()
	rel := s.rel
	plan := PlanFor(s.opts.Goos, s.opts.ExePath, s.opts.Version, rel.Tag, 0, DefaultWait)
	s.info.Progress = 0
	s.setLocked(StateDownloading, "")
	ctx, cancel := context.WithCancel(ctx)
	s.cancelDL = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancelDL = nil
		s.mu.Unlock()
	}()

	// 错误收敛必须在释放 s.mu 之后：failIO/failVerify 内部会再次加锁，持锁调用会自死锁。
	archiveAsset, err := PickArchive(rel, s.opts.Goos, s.opts.Goarch)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	csAsset, err := PickChecksums(rel)
	if err != nil {
		_ = s.failVerify(err)
		return
	}

	// 可写性探测：在 ExeDir 建一个独占临时文件再删掉（spec §7.3.1）。
	probe, err := os.CreateTemp(plan.ExeDir, ".sshore-write-test-")
	if err != nil {
		_ = s.failIO(fmt.Errorf("安装目录不可写：%w", err), HintManualUpgrade)
		return
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)

	free, err := FreeSpace(plan.ExeDir)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	// spec §7.3.2 要求 ≥ 资产大小 + 64MiB；Asset（Task 3 文件，本任务不得修改）不携带
	// 压缩包大小，因此在无法得知资产大小时退化为 64MiB 下限。
	if free < 64<<20 {
		_ = s.failIO(fmt.Errorf("磁盘可用空间不足：%d 字节", free), "")
		return
	}

	client := &Client{HTTP: s.opts.Doer, UserAgent: "sshore/" + s.opts.Version}
	part := filepath.Join(os.TempDir(), fmt.Sprintf("sshore-update-%d.part", os.Getpid()))
	err = client.Download(ctx, archiveAsset.URL, part, DownloadOpt{
		IdleTimeout: 30 * time.Second,
		Throttle:    200 * time.Millisecond,
		Progress: func(done, total int64) {
			percent := -1
			if total > 0 {
				percent = int(done * 100 / total)
			}
			s.mu.Lock()
			s.info.Progress = percent
			s.mu.Unlock()
			if s.opts.Emit != nil {
				s.opts.Emit("update:progress", map[string]any{"done": done, "total": total, "percent": percent})
			}
		},
	})
	if err != nil {
		_ = os.Remove(part)
		if errors.Is(err, context.Canceled) {
			// 用户取消：删半截文件、回 available（spec §7.3.6），不算失败
			s.mu.Lock()
			s.info.Progress = 0
			s.setLocked(StateAvailable, "")
			s.mu.Unlock()
			return
		}
		_ = s.failIO(err, "")
		return
	}
	defer os.Remove(part)

	csBytes, err := fetchBytes(ctx, s.opts.Doer, csAsset.URL, 1<<20)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	all, err := ParseChecksums(strings.NewReader(string(csBytes)))
	if err != nil {
		_ = s.failVerify(err)
		return
	}
	hash, ok := all[archiveAsset.Name]
	if !ok {
		_ = s.failVerify(fmt.Errorf("校验文件里没有 %s", archiveAsset.Name))
		return
	}
	if err := VerifyFile(part, hash); err != nil {
		_ = s.failVerify(err)
		return
	}
	if err := ExtractBinary(part, s.opts.Goos, plan.Pending); err != nil {
		_ = s.failIO(err, "")
		return
	}
	st, err := os.Stat(plan.Pending)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	plan.Size = st.Size()
	fileHash, err := FileSHA256(plan.Pending)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	line := fileHash + "  " + filepath.Base(plan.Pending) + "\n"
	if err := os.WriteFile(plan.Sidecar, []byte(line), 0o600); err != nil {
		_ = s.failIO(err, "")
		return
	}
	s.mu.Lock()
	s.pending = plan
	s.info.ReadyPath = plan.Pending
	s.info.Progress = 100
	s.setLocked(StateReady, "")
	s.mu.Unlock()
}

// fetchBytes 取小文件（校验文件），带大小上限。
func fetchBytes(ctx context.Context, d Doer, rawURL string, limit int64) ([]byte, error) {
	if d == nil {
		return nil, errors.New("未配置 HTTP 客户端")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取 %s 失败：HTTP %d", rawURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
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

// ClearSkipped 清掉「跳过此版本」并立刻重查（spec §7.1/§12.1）。
//
// 这里不得预置 checking：Check 自身会置 checking 并落到终态；若先置 checking，
// Check 开头的 checking 守卫会直接短路返回，导致这次取消跳过既不发请求、
// 状态又永久停在 checking。
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
