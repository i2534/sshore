package forward

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sshore/internal/config"
	"sshore/internal/osutil"
)

type State string

const (
	StateStopped      State = "stopped"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateError        State = "error"
	StateReconnecting State = "reconnecting"
)

// stableThreshold：连续在线满该时长才清零重连失败计数（防 flapping 风暴）。
const stableThreshold = 60 * time.Second

type Event struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	TS         string `json:"ts"`
	Level      string `json:"level"`
	Message    string `json:"message"`
}

type EmitFunc func(Event)

var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateHost returns true if host is a safe SSH alias (anti-injection).
// It forbids a leading '-', which would be parsed as an ssh option flag.
func ValidateHost(host string) bool {
	return hostRe.MatchString(host)
}

// BuildArgs returns the exec.Command argument array for a tunnel (spec §4.3).
func BuildArgs(t config.Tunnel) []string {
	if !ValidateHost(t.Host) {
		return nil
	}
	args := []string{
		"ssh", "-N",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		// 本地/远程转发绑定失败时直接退出(spec: 默认 no 会静默继续,
		// 造成“进程活着但没有端口”的 connected 假象)。
		"-o", "ExitOnForwardFailure=yes",
	}
	// H1: 导入规则可能携带非默认端口/用户，需在转发参数前生效
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	fwd := fmt.Sprintf("%s:%d", t.ListenBind, t.ListenPort)
	switch t.Mode {
	case "dynamic":
		args = append(args, "-D", fwd)
	case "remote":
		fwd += fmt.Sprintf(":%s:%d", t.TargetHost, t.TargetPort)
		args = append(args, "-R", fwd)
	default: // local
		fwd += fmt.Sprintf(":%s:%d", t.TargetHost, t.TargetPort)
		args = append(args, "-L", fwd)
	}
	if t.ProxyJump != "" {
		args = append(args, "-J", t.ProxyJump)
	}
	args = append(args, t.Host)
	return args
}

type process struct {
	state        State
	args         []string
	proc         *osutil.Process
	cancel       chan struct{} // spawn 成功即创建；Stop/OnShutdown close（置 nil 防双关）
	tunnel       config.Tunnel // spawn 时的配置快照，重连只认快照
	lastStableAt time.Time     // 最近一次进入 connected 的时刻（防抖基准）
	attempts     int           // 连续失败计数
	lastErr      string        // 最近一次意外退出的 classifyError 结果
	mu           sync.Mutex
}

// lastErrSnapshot 在持 p.mu 的前提下读取最近错误文本（watchExit 已释放锁后使用）。
func (entry *process) lastErrSnapshot() string {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.lastErr
}

type Ctrl struct {
	spawner osutil.Spawner
	emit    EmitFunc
	mu      sync.Mutex
	procs   map[string]*process
	// after 产生可被 cancel 竞争的等待通道；nil 时用生产默认（真实计时）。
	after func(time.Duration) <-chan struct{}
}

func NewCtrl(sp osutil.Spawner, emit EmitFunc, after func(time.Duration) <-chan struct{}) *Ctrl {
	if after == nil {
		after = func(d time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			go func() {
				time.Sleep(d)
				close(ch)
			}()
			return ch
		}
	}
	return &Ctrl{spawner: sp, emit: emit, procs: map[string]*process{}, after: after}
}

func (c *Ctrl) emitEvent(id, level, msg string) {
	if c.emit != nil {
		c.emit(Event{
			SourceType: "tunnel",
			SourceID:   id,
			TS:         time.Now().Format(time.RFC3339),
			Level:      level,
			Message:    msg,
		})
	}
}

func levelFor(s State) string {
	if s == StateError {
		return "error"
	}
	return "info"
}

func (c *Ctrl) setState(id string, s State) {
	c.mu.Lock()
	c.procs[id] = &process{state: s}
	c.mu.Unlock()
	c.emitEvent(id, levelFor(s), string(s))
}

func (c *Ctrl) State(sourceID string) State {
	c.mu.Lock()
	p, ok := c.procs[sourceID]
	c.mu.Unlock()
	if !ok {
		return StateStopped
	}
	// 与所有 state 写入端（Start/watchExit/Stop/OnShutdown）保持同一把锁
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// States 返回 id→state 快照（深拷贝，避免调用方触碰内部 map）。
func (c *Ctrl) States() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.procs))
	for id, p := range c.procs {
		p.mu.Lock()
		out[id] = string(p.state)
		p.mu.Unlock()
	}
	return out
}

func (c *Ctrl) Start(t config.Tunnel) error {
	// H4: 已有存活进程时直接拒绝，绝不替换 map 条目——替换会丢弃正在运行
	// 的进程句柄，导致其永远无法 Stop、端口持续被占。
	c.mu.Lock()
	if p, ok := c.procs[t.ID]; ok {
		p.mu.Lock()
		live := p.proc != nil && (p.state == StateConnecting || p.state == StateConnected)
		p.mu.Unlock()
		if live {
			c.mu.Unlock()
			return fmt.Errorf("tunnel already running: %s", t.ID)
		}
	}
	c.mu.Unlock()

	if err := ValidateTunnel(t); err != nil {
		c.setState(t.ID, StateError)
		return fmt.Errorf("%s: %s", t.Name, err)
	}

	c.mu.Lock()
	entry := &process{
		state:        StateConnecting,
		cancel:       make(chan struct{}),
		tunnel:       t,
		lastStableAt: time.Now(),
	}
	c.procs[t.ID] = entry
	c.mu.Unlock()
	c.emitEvent(t.ID, "info", "connecting")

	proc, err := c.trySpawn(t)
	if err != nil {
		msg := classifyError(osutil.Outcome{Stderr: err.Error(), ExitCode: -1})
		c.setState(t.ID, StateError)
		c.emitEvent(t.ID, "error", msg)
		return fmt.Errorf("%s: %s", t.Name, msg)
	}

	// 原地升级到 connected：身份不变，watchExit 持有同一指针。
	entry.mu.Lock()
	entry.args = BuildArgs(t)
	entry.proc = proc
	entry.state = StateConnected
	entry.lastStableAt = time.Now()
	entry.mu.Unlock()
	c.emitEvent(t.ID, "info", "connected")
	go c.watchExit(t.ID, entry, proc)
	return nil
}

// trySpawn 执行参数构建、本地端口预检与进程 spawn，返回进程句柄。
// 失败时的状态处理由调用方决定（首启→setError 替换；重连→计数后退避）。
func (c *Ctrl) trySpawn(t config.Tunnel) (*osutil.Process, error) {
	args := BuildArgs(t)
	if args == nil {
		return nil, fmt.Errorf("could not build args for %q", t.Host)
	}
	if t.Mode != "remote" && t.ListenPort > 0 && !CheckLocalPort(t.ListenBind, t.ListenPort) {
		return nil, fmt.Errorf("local port %s:%d already in use", t.ListenBind, t.ListenPort)
	}
	spawnArgs := args[1:]
	return c.spawner.Start("ssh", spawnArgs, func(line string) {
		c.emitEvent(t.ID, sshLineLevel(line), line)
	})
}

// sshLineLevel 对 ssh 子进程的 stderr 行做粗略分级：
// warning 类 → warn；关键失败词(错误/失败/被拒/不可达) → error；
// 其余(如信息性输出)保持 info。
func sshLineLevel(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "warning"):
		return "warn"
	case strings.Contains(l, "error"),
		strings.Contains(l, "failed"),
		strings.Contains(l, "denied"),
		strings.Contains(l, "refused"),
		strings.Contains(l, "unable"):
		return "error"
	default:
		return "info"
	}
}

// watchExit 监控一次成功 spawn 的进程：意外退出时按 AutoReconnect 分流——
// false → StateError（H3 行为）；true → 置 reconnecting 并进入退避重连循环。
// 指针身份比较保证：条目已被替换、或 Stop/OnShutdown 已接管时不做任何处理。
func (c *Ctrl) watchExit(id string, entry *process, proc *osutil.Process) {
	out := proc.Wait()
	c.mu.Lock()
	cur, ok := c.procs[id]
	fire := ok && cur == entry
	startLoop := false
	if fire {
		entry.mu.Lock()
		if entry.proc == proc {
			entry.proc = nil
			entry.lastErr = classifyError(out)
			if entry.tunnel.AutoReconnect {
				// 防抖：稳定满阈值才清零计数（spec §3.4）
				if time.Since(entry.lastStableAt) >= stableThreshold {
					entry.attempts = 0
				}
				entry.state = StateReconnecting
				startLoop = true
			} else {
				entry.state = StateError
			}
		} else {
			fire = false
		}
		entry.mu.Unlock()
	}
	c.mu.Unlock()
	if !fire {
		return
	}
	if !startLoop {
		c.emitEvent(id, "error", entry.lastErrSnapshot())
		return
	}
	go c.respawnLoop(id, entry)
}

// respawnLoop 按指数退避反复尝试重连，直到成功或取消。
// 一切状态迁移原地变更（禁用 setState）；spawn 成功必过 post-spawn 终检闸门。
func (c *Ctrl) respawnLoop(id string, entry *process) {
	for {
		// 等待前检查取消
		if c.loopCancelled(id, entry) {
			return
		}
		entry.mu.Lock()
		attempt := entry.attempts + 1
		entry.attempts = attempt
		reason := entry.lastErr
		entry.mu.Unlock()
		delay := backoffDelay(attempt)
		c.emitEvent(id, "warn", fmt.Sprintf("连接断开（%s），%s 后进行第 %d 次重连", reason, delay, attempt))

		select {
		case <-c.waitCancel(entry):
			c.finishStop(id, entry, "已停止重连")
			return
		case <-c.after(delay):
		}

		if c.loopCancelled(id, entry) {
			return
		}
		c.emitEvent(id, "info", fmt.Sprintf("第 %d 次重连中…", attempt))

		proc, err := c.trySpawn(entry.tunnel)
		if err != nil {
			c.emitEvent(id, "warn", fmt.Sprintf("第 %d 次重连失败: %v", attempt, err))
			continue
		}

		// ── post-spawn 终检闸门（spec §3.5）：身份 + 取消/状态双检 ──
		adopted := false
		c.mu.Lock()
		if cur, _ := c.procs[id]; cur == entry {
			entry.mu.Lock()
			if entry.cancel != nil && entry.state == StateReconnecting {
				entry.proc = proc
				entry.state = StateConnected
				now := time.Now()
				if now.Sub(entry.lastStableAt) >= stableThreshold {
					entry.attempts = 0
				}
				entry.lastStableAt = now
				adopted = true
			}
			entry.mu.Unlock()
		}
		c.mu.Unlock()
		if !adopted {
			_ = proc.Kill() // 条目已被替换或已被停止：绝不让新进程成为孤儿/复活隧道
			return
		}
		c.emitEvent(id, "info", fmt.Sprintf("reconnected（共尝试 %d 次）", attempt))
		go c.watchExit(id, entry, proc)
		return
	}
}

// loopCancelled 报告 entry 是否已被取消或不再是当前条目。
func (c *Ctrl) loopCancelled(id string, entry *process) bool {
	c.mu.Lock()
	cur, _ := c.procs[id]
	c.mu.Unlock()
	if cur != entry {
		return true
	}
	entry.mu.Lock()
	cancelled := entry.cancel == nil || entry.state != StateReconnecting
	entry.mu.Unlock()
	return cancelled
}

// waitCancel 返回 entry 的取消通道（已关/缺失时返回已关闭通道）。
func (c *Ctrl) waitCancel(entry *process) <-chan struct{} {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.cancel == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return entry.cancel
}

// finishStop 由重连循环在收到取消后收尾：置 stopped 并发事件。
func (c *Ctrl) finishStop(id string, entry *process, msg string) {
	c.mu.Lock()
	cur, _ := c.procs[id]
	c.mu.Unlock()
	if cur != entry {
		return
	}
	entry.mu.Lock()
	if entry.cancel != nil {
		close(entry.cancel)
		entry.cancel = nil
	}
	entry.proc = nil
	entry.state = StateStopped
	entry.mu.Unlock()
	c.emitEvent(id, "info", msg)
}

func (c *Ctrl) Stop(sourceID string) error {
	c.mu.Lock()
	p, ok := c.procs[sourceID]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	p.mu.Lock()
	// 关闭取消通道：重连循环（若在退避等待）立即收尾为 stopped。
	if p.cancel != nil {
		close(p.cancel)
		p.cancel = nil
	}
	if p.proc != nil {
		// On Windows Process.Signal is unsupported (returns an error), so kill
		// immediately rather than waiting the grace period for a signal that
		// will never arrive.
		if err := p.proc.Signal(); err != nil {
			_ = p.proc.Kill()
		} else {
			go func(proc *osutil.Process) {
				<-time.After(5 * time.Second)
				_ = proc.Kill()
			}(p.proc)
		}
		// 清空句柄：退出由我们触发，watchExit 不得再改写状态
		p.proc = nil
	}
	p.state = StateStopped
	p.mu.Unlock()
	c.emitEvent(sourceID, "info", "stopped")
	return nil
}

// OnShutdown reaps all managed child processes. Called from Wails OnShutdown.
func (c *Ctrl) OnShutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.procs {
		p.mu.Lock()
		// 关闭取消通道：重连循环（若在退避等待）立即收尾为 stopped。
		if p.cancel != nil {
			close(p.cancel)
			p.cancel = nil
		}
		if p.proc != nil {
			_ = p.proc.Kill()
			p.proc = nil
		}
		p.state = StateStopped
		p.mu.Unlock()
	}
}

// backoffDelay 返回第 attempt 次（从 1 计）重试前的等待时长：
// 1s 起倍增，30s 封顶（spec §3.2）。
func backoffDelay(attempt int) time.Duration {
	const max = 30 * time.Second
	if attempt <= 0 {
		return time.Second
	}
	if attempt >= 6 {
		return max
	}
	return time.Second << uint(attempt-1)
}

// classifyError maps a ssh/sftp outcome's stderr to a level-appropriate message.
func classifyError(out osutil.Outcome) string {
	s := out.Stderr
	if s == "" {
		s = out.Stdout
	}
	switch {
	case contains(s, "Address already in use"):
		return "port already in use"
	case contains(s, "Permission denied"):
		return "authentication failed — configure key/agent auth for this host"
	case contains(s, "Could not resolve hostname"):
		return "host not resolvable"
	default:
		if s == "" {
			return "unknown error (exit " + fmt.Sprintf("%d", out.ExitCode) + ")"
		}
		return s
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
