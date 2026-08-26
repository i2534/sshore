package forward

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"sshkit/internal/config"
	"sshkit/internal/osutil"
)

type State string

const (
	StateStopped    State = "stopped"
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateError      State = "error"
)

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

	if !ValidateHost(t.Host) {
		c.setState(t.ID, StateError)
		return fmt.Errorf("invalid host alias %q", t.Host)
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
	return c.spawner.Start("ssh", spawnArgs...)
}

// watchExit 监控一次成功 spawn 的进程（H3）：进程自行退出（认证失败、断网等）
// 时把状态迁移到 StateError 并发 error 事件。指针身份比较保证：条目已被替换、
// 或 Stop/OnShutdown 已接管该进程（proc 句柄被清空）时不做任何处理。
func (c *Ctrl) watchExit(id string, entry *process, proc *osutil.Process) {
	out := proc.Wait()
	c.mu.Lock()
	cur, ok := c.procs[id]
	fire := ok && cur == entry
	if fire {
		cur.mu.Lock()
		if cur.proc == proc {
			cur.proc = nil
			cur.state = StateError
		} else {
			fire = false
		}
		cur.mu.Unlock()
	}
	c.mu.Unlock()
	if fire {
		c.emitEvent(id, "error", classifyError(out))
	}
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
