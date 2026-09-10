package sshconn

import (
	"context"
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
	if len(calls) != 2 {
		t.Fatalf("应 check 失败后再建一次，实际 %d 次", len(calls))
	}
	built := strings.Join(calls[1], " ")
	for _, want := range []string{"ControlMaster=yes", "-N", "-f", "BatchMode=yes"} {
		if !strings.Contains(built, want) {
			t.Fatalf("建 master 参数缺少 %q: %s", want, built)
		}
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
