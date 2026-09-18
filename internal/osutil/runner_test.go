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

// TestNewRunnerRunsBinary 用跨平台 helper 子进程（helper_test.go）而不是 sh ——
// Windows 上没有 sh，此前这条在真机/CI 上是红的。
func TestNewRunnerRunsBinary(t *testing.T) {
	r := NewRunner()
	out, err := r(helperExe(t)) // helper 模式（TestMain）下立刻以 0 退出
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
	// 用跨平台 helper 子进程（helper_test.go）替代 sh：streams 模式往 stderr 写
	// e1/"  e2  "。断言与原来一致：逐行回调、裁空白、与进程输出顺序一致。
	t.Setenv(osutilHelperEnv, "streams")
	sp := NewSpawner()
	var lines []string
	p, err := sp.Start(helperExe(t), nil, func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	out := p.Wait()
	if out.ExitCode != 0 {
		t.Fatalf("want exit 0 got %d", out.ExitCode)
	}
	if len(lines) != 2 || lines[0] != "e1" || lines[1] != "e2" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

// StartStream 必须把 stdout 与 stderr 分别逐行回调；空行丢弃、行首尾空白裁掉。
func TestStartStreamRoutesBothStreams(t *testing.T) {
	t.Setenv(osutilHelperEnv, "streams") // 跨平台替身：stdout o1/o2、stderr e1/"  e2  "
	sp := NewStreamer()
	var out, errs []string
	p, err := sp.StartStream(helperExe(t), nil, StreamHandlers{
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
	// flood 模式写 1MB 后退出：nil 回调若不建管道，子进程写满 64KB 后会阻塞，本用例
	// 会在下面的 10s 超时里报"子进程被阻塞"（与原先 sh 大输出用例同等强度）。
	t.Setenv(osutilHelperEnv, "flood")
	sp := NewStreamer()
	p, err := sp.StartStream(helperExe(t), nil, StreamHandlers{})
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
	// 跨平台替身：streams 模式写 stdout=o1/o2、stderr=e1/"  e2  "（非 0 退出码另用 exit 模式验）。
	t.Setenv(osutilHelperEnv, "streams")
	out, err := NewCtxRunner()(context.Background(), helperExe(t), "exit", "3")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 注意：streams 分支 return 0，所以要验退出码得用 exit 模式。这里同时验两件事：
	// stdout/stderr 被回传（streams 的输出）+ 非 0 退出不算 err、退出码如实。
	if !strings.Contains(out.Stdout, "o1") || !strings.Contains(out.Stderr, "e1") {
		t.Fatalf("stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
	t.Setenv(osutilHelperEnv, "exit")
	out2, err2 := NewCtxRunner()(context.Background(), helperExe(t), "3")
	if err2 != nil || out2.ExitCode != 3 {
		t.Fatalf("exit 模式：want exit 3/nil，got %d/%v", out2.ExitCode, err2)
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
	// F2（Task 3 评审）：本用例必须有本地超时。drain 失效时子进程会阻塞在 stderr 写满的
	// 管道上，下面的 stdout 读将永久挂住，只能等 go test 默认 10m panic 超时；
	// 与 TestStartStreamNilHandlerDoesNotBlock 对齐，10s 内失败。
	buf := make([]byte, 4)
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(p.Stdout, buf)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = p.Kill()
		t.Fatal("stderr drain 失效：子进程写满 stderr 管道后被阻塞")
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

func TestPipedProcessCloseIsIdempotentAfterExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "echo done")
	if err != nil {
		t.Fatal(err)
	}
	if out := p.Wait(); out.ExitCode != 0 {
		t.Fatalf("exit=%d", out.ExitCode)
	}
	// 子进程已退出：Close 必须成功（吞掉 os.ErrProcessDone），且可重复调用
	if err := p.Close(); err != nil {
		t.Fatalf("已退出后 Close 必须成功，got %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close 必须可重复调用，got %v", err)
	}
}

func TestPipedProcessCloseKillsRunningChild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("关闭运行中的子进程必须成功，got %v", err)
	}
	if out := p.Wait(); out.ExitCode == 0 {
		t.Fatal("被 Kill 的子进程不应以 0 退出")
	}
}

// F1（Task 3 评审）：直接子进程退出后，后台后代仍持有 fd 2。
// Close 若不关闭父端 stderr 读端，drain → drain.Wait() → cmd.Wait() 整条链会挂到后代结束。
func TestPipedProcessCloseUnblocksDescendantHoldingStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	// 直接子进程立刻退出，但后台后代仍持有 fd 2：不关父端读端时 Wait 会挂到后代结束。
	p, err := StartPipes("sh", "-c", "sleep 30 & echo hi")
	if err != nil {
		t.Fatal(err)
	}
	// 必须先读到 "hi"：只有 shell 执行完 "sleep 30 &" 的 fork 才会走到 "echo hi"。
	// 原评审用例在 StartPipes 后立刻 Close，可能赶在 fork 之前就把 sh 杀掉，此时根本没有
	// 后代持有 fd 2，drain 直接 EOF，用例会假绿（本机实测 mutant 也能过）。这个同步点是必需的。
	buf := make([]byte, 2)
	if _, err := io.ReadFull(p.Stdout, buf); err != nil {
		t.Fatalf("等待后代 fork 的同步输出失败: %v", err)
	}
	if string(buf) != "hi" {
		t.Fatalf("want hi, got %q", buf)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	done := make(chan Outcome, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 后 Wait 仍被后代持有的 fd 2 阻塞（Close 未关闭父端 stderr 读端？）")
	}
}

// F3（Task 3 评审）：有界缓冲必须 <=16KB，且只保留最近的字节（尾部）。
// 评审给出的命令三处输出漏了 >&2，实际全写进了 stdout，而断言读的是 StderrText；
// 且只读 4 字节 stdout 后 Wait 会因 stdout 管道写满而永久挂住。此处改为整组输出重定向到
// stderr（stdout 只留 "done" 的 4 字节），语义与断言（HEADMARK 应被挤出、TAILMARK 应保留）一致。
func TestStartPipesStderrBufferIsBoundedAndKeepsTail(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "{ printf HEADMARK; head -c 200000 /dev/zero | tr '\\0' 'x'; printf TAILMARK; } >&2; echo done")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(p.Stdout, buf); err != nil {
		t.Fatal(err)
	}
	_ = p.Wait()
	got := p.StderrText()
	if len(got) > 16*1024 {
		t.Fatalf("stderr 缓冲必须有界（<=16KB），got %d", len(got))
	}
	if !strings.Contains(got, "TAILMARK") {
		t.Fatalf("有界缓冲必须保留最近的字节（TAILMARK 丢失），got %q", got)
	}
	if strings.Contains(got, "HEADMARK") {
		t.Fatal("有界缓冲不应保留最早的字节（HEADMARK 出现说明保留了头部）")
	}
}
