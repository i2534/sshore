package forward

import (
	"fmt"
	"regexp"
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
	state State
	args  []string
	proc  *osutil.Process
	mu    sync.Mutex
}

type Ctrl struct {
	spawner osutil.Spawner
	emit    EmitFunc
	mu      sync.Mutex
	procs   map[string]*process
}

func NewCtrl(sp osutil.Spawner, emit EmitFunc) *Ctrl {
	return &Ctrl{spawner: sp, emit: emit, procs: map[string]*process{}}
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
	defer c.mu.Unlock()
	if p, ok := c.procs[sourceID]; ok {
		return p.state
	}
	return StateStopped
}

// Start validates the host; the real spawn lifecycle is completed in Task 6.
func (c *Ctrl) Start(t config.Tunnel) error {
	if !ValidateHost(t.Host) {
		c.setState(t.ID, StateError)
		return fmt.Errorf("invalid host alias %q", t.Host)
	}
	args := BuildArgs(t)
	if args == nil {
		c.setState(t.ID, StateError)
		return fmt.Errorf("could not build args for %q", t.Host)
	}
	// port pre-check for local/dynamic
	if t.Mode != "remote" && t.ListenPort > 0 && !CheckLocalPort(t.ListenBind, t.ListenPort) {
		c.setState(t.ID, StateError)
		err := fmt.Errorf("local port %s:%d already in use", t.ListenBind, t.ListenPort)
		c.emitEvent(t.ID, "error", err.Error())
		return err
	}

	spawnArgs := args[1:] // drop the leading "ssh"
	c.mu.Lock()
	c.procs[t.ID] = &process{state: StateConnecting, args: args}
	c.mu.Unlock()
	c.emitEvent(t.ID, "info", "connecting")

	proc, err := c.spawner.Start("ssh", spawnArgs...)
	if err != nil {
		msg := classifyError(osutil.Outcome{Stderr: err.Error(), ExitCode: -1})
		c.mu.Lock()
		c.procs[t.ID] = &process{state: StateError, args: args}
		c.mu.Unlock()
		c.emitEvent(t.ID, "error", msg)
		return fmt.Errorf("%s: %s", t.Name, msg)
	}

	c.mu.Lock()
	c.procs[t.ID] = &process{state: StateConnected, args: args, proc: proc}
	c.mu.Unlock()
	c.emitEvent(t.ID, "info", "connected")
	return nil
}

func (c *Ctrl) Stop(sourceID string) error {
	c.mu.Lock()
	p, ok := c.procs[sourceID]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	p.mu.Lock()
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
	}
	p.mu.Unlock()
	p.state = StateStopped
	c.emitEvent(sourceID, "info", "stopped")
	return nil
}

// OnShutdown reaps all managed child processes. Called from Wails OnShutdown.
func (c *Ctrl) OnShutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.procs {
		if p.proc != nil {
			_ = p.proc.Kill()
		}
		p.proc = nil
		p.state = StateStopped
	}
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
