package osutil

import (
	"bytes"
	"errors"
	"os/exec"
)

type Outcome struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner abstracts a ONE-SHOT (blocking) subprocess invocation. Used by sftp ops.
type Runner func(name string, args ...string) (Outcome, error)

// NewRunner returns a Runner backed by os/exec. Blocking (cmd.Run); for short-lived commands only.
func NewRunner() Runner {
	return func(name string, args ...string) (Outcome, error) {
		cmd := exec.Command(name, args...)
		procAttrHideConsole(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return Outcome{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: execResult(err)}, err
	}
}

// Process is a handle to a running long-lived subprocess (e.g. `ssh -N`).
type Process struct {
	cmd  *exec.Cmd
	done chan Outcome
}

// Spawner abstracts starting and killing a long-lived process (forward only).
// Start is NON-BLOCKING: it launches the process and returns immediately with a Process.
type Spawner interface {
	Start(name string, args ...string) (*Process, error)
}

// NewSpawner returns a Spawner backed by os/exec.
func NewSpawner() Spawner {
	return realSpawner{}
}

type realSpawner struct{}

func (realSpawner) Start(name string, args ...string) (*Process, error) {
	cmd := exec.Command(name, args...)
	procAttrHideConsole(cmd)
	p := &Process{cmd: cmd, done: make(chan Outcome, 1)}
	if err := cmd.Start(); err != nil {
		return p, err
	}
	go func() {
		err := cmd.Wait()
		p.done <- Outcome{ExitCode: execResult(err)}
		close(p.done)
	}()
	return p, nil
}

// Signal sends a graceful interrupt (SIGINT/CTRL_BREAK); returns error if not running.
func (p *Process) Signal() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return errors.New("not running")
	}
	return p.cmd.Process.Signal(sigint())
}

// Kill force-terminates the process.
func (p *Process) Kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return errors.New("not running")
	}
	return p.cmd.Process.Kill()
}

// Wait blocks until the process exits and returns its Outcome.
func (p *Process) Wait() Outcome {
	out, ok := <-p.done
	if !ok {
		return Outcome{}
	}
	return out
}

// execResult extracts the exit code; 0 for nil, -1 for non-exit errors.
func execResult(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
