package osutil

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewRunnerRunsBinary(t *testing.T) {
	r := NewRunner()
	out, err := r("sh", "-c", "echo hi; exit 0")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("want exit 0 got %d", out.ExitCode)
	}
}

func TestExecResult(t *testing.T) {
	if got := execResult(nil); got != 0 {
		t.Fatalf("nil err => 0, got %d", got)
	}
	var ee = errors.New("boom")
	if got := execResult(ee); got != -1 {
		t.Fatalf("generic err => -1, got %d", got)
	}
}

// aliveCmd 返回一个能自持数秒的真实命令，且不依赖 Git for Windows 自带的
// sh/sleep：Windows 用系统 ping，Unix 用 sleep。
func aliveCmd() (string, []string) {
	if runtime.GOOS == "windows" {
		return "ping", []string{"-n", "31", "127.0.0.1"}
	}
	return "sleep", []string{"30"}
}

func TestSpawnerStartKill(t *testing.T) {
	sp := NewSpawner()
	name, args := aliveCmd()
	p, err := sp.Start(name, args, nil)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	if p == nil {
		t.Fatal("nil process")
	}
	_ = p.Kill()
	out := p.Wait()
	if out.ExitCode == 0 {
		t.Fatalf("killed process should not exit 0, got %d", out.ExitCode)
	}
}

func TestProcessSignal(t *testing.T) {
	sp := NewSpawner()
	name, args := aliveCmd()
	p, err := sp.Start(name, args, nil)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	err = p.Signal()
	if runtime.GOOS == "windows" {
		// Windows 的 os.Process.Signal 不支持 SIGTERM（sigint_windows 返回 SIGTERM），
		// 返回错误是预期行为；forward.Stop 会在 Signal 失败时回退 Kill。
		if err == nil {
			t.Fatal("windows 上 Signal 应返回不支持的错误")
		}
	} else if err != nil {
		t.Fatalf("signal should not error: %v", err)
	}
	_ = p.Kill()
	_ = p.Wait()
}

// TestSpawnerStderrLines 验证 stderr 逐行回调:每行去空白、空行被丢弃、
// 行顺序与进程输出一致。
func TestSpawnerStderrLines(t *testing.T) {
	sp := NewSpawner()
	var lines []string
	p, err := sp.Start("sh", []string{"-c", "echo one >&2; echo >&2; echo '  two  ' >&2"},
		func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	out := p.Wait()
	if out.ExitCode != 0 {
		t.Fatalf("want exit 0 got %d", out.ExitCode)
	}
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

// StartStream 必须把 stdout 与 stderr 分别逐行回调；空行丢弃、行首尾空白裁掉。
func TestStartStreamRoutesBothStreams(t *testing.T) {
	sp := NewStreamer()
	var out, errs []string
	p, err := sp.StartStream("sh", []string{"-c", "echo o1; echo o2; echo e1 >&2; echo '  e2  ' >&2"}, StreamHandlers{
		OnStdout: func(l string) { out = append(out, l) },
		OnStderr: func(l string) { errs = append(errs, l) },
	})
	if err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}
	if got := p.Wait(); got.ExitCode != 0 {
		t.Fatalf("want exit 0, got %d", got.ExitCode)
	}
	if len(out) != 2 || out[0] != "o1" || out[1] != "o2" {
		t.Fatalf("stdout lines = %#v", out)
	}
	if len(errs) != 2 || errs[0] != "e1" || errs[1] != "e2" {
		t.Fatalf("stderr lines = %#v", errs)
	}
}

// 回调为 nil 表示不接该管道。若接了没人读的管道，子进程写满 64KB 缓冲后会永久阻塞。
func TestStartStreamNilHandlerDoesNotBlock(t *testing.T) {
	sp := NewStreamer()
	p, err := sp.StartStream("sh", []string{"-c", "yes | head -c 1048576"}, StreamHandlers{})
	if err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}
	done := make(chan Outcome, 1)
	go func() { done <- p.Wait() }()
	select {
	case got := <-done:
		if got.ExitCode != 0 {
			t.Fatalf("want exit 0, got %d", got.ExitCode)
		}
	case <-time.After(10 * time.Second):
		_ = p.Kill()
		t.Fatal("子进程被阻塞：nil 回调时不应创建管道")
	}
}

// cmd.Start() 失败必须返回 (nil, err)：半成品 Process 的 done channel 永不关闭。
func TestStartFailureReturnsNilProcess(t *testing.T) {
	sp := NewStreamer()
	p, err := sp.StartStream("sshore-no-such-binary-xyz", nil, StreamHandlers{})
	if err == nil {
		t.Fatal("want error for missing binary")
	}
	if p != nil {
		t.Fatal("want nil Process on start failure")
	}
}

// CtxRunner 必须真正取消子进程（对齐 config/parser.go:84 的既有做法）。
func TestCtxRunnerCancelsProcess(t *testing.T) {
	r := NewCtxRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// 直接用 sleep/ping，不经 shell：Windows 上杀掉经 shell 启动的父进程不会连带杀掉
	// 孙进程，管道要等孙进程自然结束，测不出「取消生效」。
	name, args := "sleep", []string{"30"}
	if runtime.GOOS == "windows" {
		name, args = "ping", []string{"-n", "31", "127.0.0.1"}
	}
	start := time.Now()
	if _, err := r(ctx, name, args...); err == nil {
		t.Fatal("want error from cancelled command")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取消未生效，耗时 %v", elapsed)
	}
}

// CtxRunner 正常路径要带回 stdout/stderr 与退出码。
func TestCtxRunnerCapturesOutput(t *testing.T) {
	r := NewCtxRunner()
	out, err := r(context.Background(), "sh", "-c", "echo hi; echo boom >&2; exit 3")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.ExitCode != 3 {
		t.Fatalf("want exit 3, got %d", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "hi") || !strings.Contains(out.Stderr, "boom") {
		t.Fatalf("stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
}

func TestStartPipesRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Stdin.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = p.Stdin.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(p.Stdout, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("want hello, got %q", buf)
	}
}

// 关键：stderr 写了 200KB 而调用方从不读它，进程仍必须正常结束（否则 64KB 管道写满会让 ssh 假死）。
func TestStartPipesDoesNotBlockWhenStderrUnread(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "head -c 200000 /dev/zero >&2; echo done")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(p.Stdout, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "done" {
		t.Fatalf("want done, got %q", buf)
	}
	out := p.Wait()
	if out.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", out.ExitCode, p.StderrText())
	}
	if len(p.StderrText()) == 0 {
		t.Fatal("stderr 应被后台 drain 到有界缓冲，供错误上报")
	}
}
