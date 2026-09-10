package sshconn

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sshore/internal/osutil"
)

// socket 路径必须把 user 纳入 key。
func TestControlPathIncludesUser(t *testing.T) {
	a := ControlPath("prod-01", "")
	b := ControlPath("prod-01", "alice")
	c := ControlPath("prod-01", "bob")
	if a == b || b == c || a == c {
		t.Fatalf("socket 路径未按 user 区分: %q %q %q", a, b, c)
	}
	if got := ControlPath("prod-01", "alice"); got != b {
		t.Fatalf("同参数必须幂等: %q vs %q", got, b)
	}
	if !strings.HasPrefix(b, ControlDir()) {
		t.Fatalf("socket 必须落在 ControlDir 内: %q", b)
	}
}

// ssh -O check 退出 0 视为可用，直接返回，不再建连接。
func TestEnsureMasterReusesLiveMaster(t *testing.T) {
	var calls [][]string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		calls = append(calls, append([]string{name}, args...))
		return osutil.Outcome{ExitCode: 0}, nil
	}
	if err := EnsureMaster(context.Background(), fake, "h1", "u1"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("应只调用一次 ssh -O check，实际 %d 次: %v", len(calls), calls)
	}
	if joined := strings.Join(calls[0], " "); !strings.Contains(joined, "-O check") {
		t.Fatalf("判活必须用 ssh -O check，实际: %s", joined)
	}
}

// 陈旧 socket（文件在但 master 已死）⇒ -O check 非 0 ⇒ 必须真正重建 master。
// 重建命令 rc=0 之后再用 -O check 复核一次，因此共 3 次调用。
func TestEnsureMasterRebuildsStaleSocket(t *testing.T) {
	var calls [][]string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(calls) == 1 {
			return osutil.Outcome{ExitCode: 255, Stderr: "Control socket connect: refused"}, nil
		}
		return osutil.Outcome{ExitCode: 0}, nil
	}
	if err := EnsureMaster(context.Background(), fake, "h1", ""); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("应 check 失败 → 重建 → 复核，实际 %d 次", len(calls))
	}
	built := strings.Join(calls[1], " ")
	for _, want := range []string{"ControlMaster=yes", "-N", "-f", "BatchMode=yes"} {
		if !strings.Contains(built, want) {
			t.Fatalf("建 master 参数缺少 %q: %s", want, built)
		}
	}
	if joined := strings.Join(calls[2], " "); !strings.Contains(joined, "-O check") {
		t.Fatalf("重建后必须用 ssh -O check 复核，实际: %s", joined)
	}
}

// 崩溃 master（kill -9）会留下 socket 文件；ssh -o ControlMaster=yes 遇到已存在
// 的 socket 会打印 "already exists, disabling multiplexing" 并以 rc=0 退出——既不
// 建 master 也不报错。所以重建前必须先删掉残留文件，否则会静默失败。
func TestEnsureMasterRemovesStaleSocketBeforeRebuild(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)

	host, user := "h-stale", "u-stale"
	cp := ControlPath(host, user)
	if err := os.WriteFile(cp, []byte("stale"), 0600); err != nil {
		t.Fatalf("预置陈旧 socket 文件失败: %v", err)
	}

	var calls [][]string
	removedBeforeRebuild := false
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		calls = append(calls, append([]string{name}, args...))
		switch len(calls) {
		case 1:
			return osutil.Outcome{ExitCode: 255, Stderr: "Control socket connect: refused"}, nil
		case 2:
			if _, err := os.Stat(cp); errors.Is(err, os.ErrNotExist) {
				removedBeforeRebuild = true
			}
			return osutil.Outcome{ExitCode: 0}, nil
		default:
			return osutil.Outcome{ExitCode: 0}, nil
		}
	}
	if err := EnsureMaster(context.Background(), fake, host, user); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !removedBeforeRebuild {
		t.Fatalf("重建前必须删除残留 socket 文件: %s", cp)
	}
	if len(calls) != 3 {
		t.Fatalf("应为 check(失败) → 重建 → 复核，共 3 次调用，实际 %d 次: %v", len(calls), calls)
	}
}

// 重建命令 rc=0 并不代表 master 真的起来了：重建后复核仍非 0 必须返回错误，
// 绝不能报告成功让调用方以为有 master 可用（否则静默退化为各自建连）。
func TestEnsureMasterFailsWhenRebuildLeavesNoLiveMaster(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)

	host := "h-dead"
	var calls [][]string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		calls = append(calls, append([]string{name}, args...))
		switch len(calls) {
		case 2:
			return osutil.Outcome{ExitCode: 0}, nil
		default:
			return osutil.Outcome{ExitCode: 255, Stderr: "Control socket connect: refused"}, nil
		}
	}
	if err := EnsureMaster(context.Background(), fake, host, ""); err == nil {
		t.Fatalf("重建后 master 仍未存活，必须返回错误而不是成功")
	}
	if len(calls) != 3 {
		t.Fatalf("应为 check(失败) → 重建 → 复核，共 3 次调用，实际 %d 次", len(calls))
	}
}

// Exec 必须把命令原样交给远端 shell，并把 user 变成 -o User=。
func TestExecBuildsArgs(t *testing.T) {
	var got []string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		got = append([]string{name}, args...)
		return osutil.Outcome{ExitCode: 0, Stdout: "ok"}, nil
	}
	out, err := Exec(context.Background(), fake, "h1", "u1", "command -v inotifywait")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Stdout != "ok" {
		t.Fatalf("stdout = %q", out.Stdout)
	}
	if joined := strings.Join(got, " "); !strings.Contains(joined, "-o User=u1") {
		t.Fatalf("缺少 User 选项: %s", joined)
	}
	if got[len(got)-1] != "command -v inotifywait" {
		t.Fatalf("远端命令必须作为最后一个参数: %#v", got)
	}
}

// 远端 shell 单引号转义：含空格/分号/单引号/美元符号的路径不得被远端 shell 解释。
func TestQuoteRemote(t *testing.T) {
	if got, want := QuoteRemote("/srv/app conf"), "'/srv/app conf'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := QuoteRemote("/srv/a; rm -rf /"), "'/srv/a; rm -rf /'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := QuoteRemote("/srv/$HOME"), "'/srv/$HOME'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := QuoteRemote("/srv/it's"), "'/srv/it'\\''s'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
