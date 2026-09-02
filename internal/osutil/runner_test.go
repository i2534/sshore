package osutil

import (
	"errors"
	"testing"
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

func TestSpawnerStartKill(t *testing.T) {
	sp := NewSpawner()
	p, err := sp.Start("sleep", []string{"30"}, nil)
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
	p, err := sp.Start("sleep", []string{"30"}, nil)
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	if err := p.Signal(); err != nil {
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
