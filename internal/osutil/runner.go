package osutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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

// PipedProcess 是长驻子进程的裸管道句柄（供二进制协议使用，例如 ssh -s sftp）。
type PipedProcess struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser

	proc *Process

	mu     sync.Mutex
	errBuf []byte // 有界 stderr 环形缓冲（最近 16KB）
}

const pipedStderrKeep = 16 * 1024

// StartPipes 起一个长驻子进程并交出三路裸管道。
// stderr 必须由本原语自己 drain —— 调用方不读也不会把 64KB 管道写满而假死。
func StartPipes(name string, args ...string) (*PipedProcess, error) {
	cmd := exec.Command(name, args...)
	procAttrHideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	p := &PipedProcess{Stdin: stdin, Stdout: stdout, Stderr: stderr, proc: &Process{cmd: cmd, done: make(chan Outcome, 1)}}
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := stderr.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.errBuf = append(p.errBuf, buf[:n]...)
				if len(p.errBuf) > pipedStderrKeep {
					p.errBuf = p.errBuf[len(p.errBuf)-pipedStderrKeep:]
				}
				p.mu.Unlock()
			}
			if rerr != nil {
				break
			}
		}
		_ = stderr.Close()
	}()
	// 与 StartStream 同序：先等 stderr drain 到 EOF，再 cmd.Wait()。
	// 反过来会让 Wait 关闭父端管道、截断 stderr，且与读取并发（-race 会抓）。
	go func() {
		drain.Wait()
		err := cmd.Wait()
		p.proc.done <- Outcome{ExitCode: execResult(err)}
		close(p.proc.done)
	}()
	return p, nil
}

// StderrText 返回后台 drain 到的 stderr 尾部（错误上报的唯一来源）。
func (p *PipedProcess) StderrText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(string(p.errBuf))
}

// Wait 返回子进程的退出结果；按契约只应调用一次，重复调用返回零值 Outcome。
func (p *PipedProcess) Wait() Outcome { return p.proc.Wait() }

func (p *PipedProcess) Kill() error { return p.proc.Kill() }

// Signal 发送优雅中断（SIGINT/CTRL_BREAK），与 Process.Signal 同语义（spec §12.3 g）。
func (p *PipedProcess) Signal() error { return p.proc.Signal() }

// Close 关闭管道并终止子进程；调用方在传输结束后必须调用，避免长驻 ssh 泄漏。
// 幂等：子进程已正常退出时 Kill 返回 os.ErrProcessDone，这不是错误。
//
// F1（Task 3 评审）：**必须也关父端 stderr 读端**。drain 的 Read 只在 stderr 写端
// 全部关闭后才 EOF；若后代进程仍持有 fd 2（sleep &、ProxyCommand、ControlPersist 等），
// 不关读端会把 drain → drain.Wait() → cmd.Wait() 整条链无限挂住，Close/Kill 都解不开。
// 关父端读端会让 Read 立刻返回错误，从而解除阻塞。
// 注意：本原语**不保证杀死后代进程**（只杀直接子进程）。
func (p *PipedProcess) Close() error {
	_ = p.Stdin.Close()
	_ = p.Stdout.Close()
	_ = p.Stderr.Close()
	if err := p.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
