package osutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
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

// CtxRunner 是 Runner 的可取消版本（exec.CommandContext）。任何可能挂住的一次性
// 远端命令都必须用它——超时后子进程必被回收，不泄漏 goroutine 与进程。
type CtxRunner func(ctx context.Context, name string, args ...string) (Outcome, error)

func NewCtxRunner() CtxRunner {
	return func(ctx context.Context, name string, args ...string) (Outcome, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		procAttrHideConsole(cmd)
		isolateAndCancel(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		out := Outcome{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: execResult(err)}
		// 契约：**非 0 退出不算 err**，只看 ExitCode（探测 command -v 返回 1 是
		// 正常结果，不是失败）。只有取消/超时与启动类失败才返回 err。
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, nil
		}
		return out, err
	}
}

// Process is a handle to a running long-lived subprocess (e.g. `ssh -N`).
type Process struct {
	cmd  *exec.Cmd
	done chan Outcome
}

// Spawner abstracts starting and killing a long-lived process (forward only).
// Start is NON-BLOCKING: it launches the process and returns immediately with a Process.
// onLine receives each non-empty trimmed line of the child's stderr, allowing
// callers to surface real ssh/sftp errors in the UI without waiting for exit.
type Spawner interface {
	Start(name string, args []string, onLine func(string)) (*Process, error)
}

// NewSpawner returns a Spawner backed by os/exec.
func NewSpawner() Spawner {
	return realSpawner{}
}

// StreamHandlers 承载流式子进程的逐行回调。某个回调为 nil 表示不接该管道
// ——绝不创建一条没人读的管道：子进程写满缓冲区后会永久阻塞。
type StreamHandlers struct {
	OnStdout func(string)
	OnStderr func(string)
}

// Streamer 启动长驻子进程并消费其 stdout/stderr。forward 用 Spawner（只关心
// stderr），watch 用 Streamer（inotifywait 的事件流在 stdout）。
//
// 注意：NewSpawner() 返回的是只有 Start 的 Spawner 接口，**不能**用来调
// StartStream。需要流式能力的地方一律用 NewStreamer()（同一个 realSpawner 实现）。
type Streamer interface {
	StartStream(name string, args []string, h StreamHandlers) (*Process, error)
}

// NewStreamer 返回同时具备流式能力的启动器。
func NewStreamer() Streamer { return realSpawner{} }

type realSpawner struct{}

// Start 保持既有签名与语义（仅 stderr 回调），内部委托给 StartStream。
func (realSpawner) Start(name string, args []string, onLine func(string)) (*Process, error) {
	var h StreamHandlers
	if onLine != nil {
		h.OnStderr = onLine
	}
	return realSpawner{}.StartStream(name, args, h)
}

func (realSpawner) StartStream(name string, args []string, h StreamHandlers) (*Process, error) {
	cmd := exec.Command(name, args...)
	procAttrHideConsole(cmd)
	p := &Process{cmd: cmd, done: make(chan Outcome, 1)}

	// 先取管道、再 Start：Start 失败时显式关闭管道，不泄漏 fd 与扫描 goroutine。
	var stdout, stderr io.ReadCloser
	if h.OnStderr != nil {
		var err error
		if stderr, err = cmd.StderrPipe(); err != nil {
			return nil, err
		}
	}
	if h.OnStdout != nil {
		var err error
		if stdout, err = cmd.StdoutPipe(); err != nil {
			if stderr != nil {
				_ = stderr.Close()
			}
			return nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		if stdout != nil {
			_ = stdout.Close()
		}
		if stderr != nil {
			_ = stderr.Close()
		}
		return nil, err // 修复：不再返回 done channel 永不关闭的半成品 Process
	}

	var streams sync.WaitGroup
	if stderr != nil {
		streams.Add(1)
		go func() {
			defer streams.Done()
			scanLines(stderr, h.OnStderr)
		}()
	}
	if stdout != nil {
		streams.Add(1)
		go func() {
			defer streams.Done()
			scanLines(stdout, h.OnStdout)
		}()
	}
	go func() {
		// 先等各流扫描到 EOF 再回收进程：cmd.Wait() 会关闭父端管道，若先 Wait
		// 既可能截断尚未读完的缓冲，又会让回调与 Wait() 返回后的读取发生数据竞争。
		streams.Wait()
		err := cmd.Wait()
		p.done <- Outcome{ExitCode: execResult(err)}
		close(p.done)
	}()
	return p, nil
}

// scanLines 逐行读取并回调；行首尾空白裁掉（含 pty 产出的 CR），空行丢弃。
// 缓冲区放大到 1MB：inotifywait 的事件行可能含很长的绝对路径。
func scanLines(rc io.ReadCloser, fn func(string)) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			fn(line)
		}
	}
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
