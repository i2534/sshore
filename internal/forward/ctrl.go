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
	SourceType string
	SourceID   string
	TS         string
	Level      string
	Message    string
}

type EmitFunc func(Event)

var hostRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateHost returns true if host is a safe SSH alias (anti-injection).
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
	c.setState(t.ID, StateConnecting)
	c.setState(t.ID, StateConnected)
	return nil
}

func (c *Ctrl) Stop(sourceID string) error {
	c.setState(sourceID, StateStopped)
	return nil
}
