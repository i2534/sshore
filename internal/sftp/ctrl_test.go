package sftp

import (
	"os"
	"strings"
	"sync"
	"testing"

	"sshkit/internal/osutil"
)

// fakeRunner records every (name, args) invocation so tests can assert on the
// exact ssh/sftp argument list. It also creates the ControlPath socket file on
// demand so Connect's post-spawn poll succeeds without a real ssh master.
type fakeRunner struct {
	mu    sync.Mutex
	calls []runnerCall
}

type runnerCall struct {
	name string
	args []string
}

func (f *fakeRunner) run(name string, args ...string) (osutil.Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{name: name, args: args})
	f.mu.Unlock()
	for _, a := range args {
		if strings.HasPrefix(a, "ControlPath=") {
			_ = os.WriteFile(strings.TrimPrefix(a, "ControlPath="), nil, 0600)
		}
	}
	return osutil.Outcome{ExitCode: 0}, nil
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestBuildBatchAndWrite(t *testing.T) {
	c := NewCtrl(nil, nil)
	dir := t.TempDir()
	b, err := c.buildBatch("get", "/remote/path", "/local/path")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `get "/remote/path" "/local/path"` + "\n" {
		t.Fatalf("get batch wrong: %q", got)
	}
	p, err := c.writeBatch(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("temp file should be 0600, got %o", info.Mode().Perm())
	}
	data, _ := os.ReadFile(p)
	if string(data) != string(b) {
		t.Fatalf("file content mismatch")
	}
}

func TestBuildBatchQuoting(t *testing.T) {
	c := NewCtrl(nil, nil)
	cases := []struct {
		op, remote, local, want string
	}{
		{"ls", "/remote/dir with spaces", "", `ls -l "/remote/dir with spaces"` + "\n"},
		{"get", "/remote/a\"b", "/local/c\\d", `get "/remote/a\"b" "/local/c\\d"` + "\n"},
		{"put", "/remote/path", `C:\Users\foo bar\file.txt`, `put "C:\\Users\\foo bar\\file.txt" "/remote/path"` + "\n"},
		{"rm", "/remote/x", "", `rm "/remote/x"` + "\n"},
		{"mkdir", "/remote/new dir", "", `mkdir "/remote/new dir"` + "\n"},
		{"rename", "/remote/old name", "/remote/new\"name", `rename "/remote/old name" "/remote/new\"name"` + "\n"},
	}
	for _, tc := range cases {
		b, err := c.buildBatch(tc.op, tc.remote, tc.local)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.op, err)
		}
		if got := string(b); got != tc.want {
			t.Fatalf("%s batch wrong: got %q want %q", tc.op, got, tc.want)
		}
	}
}

func TestBuildBatchRejectsControlChars(t *testing.T) {
	c := NewCtrl(nil, nil)
	bad := []string{"/remote/evil\n!calc", "/remote/evil\r!calc", "/remote/evil\x00x"}
	ops := []struct {
		op, remote, local string
	}{
		{"ls", "", ""},
		{"get", "", "/local/ok"},
		{"put", "", "/local/ok"},
		{"rm", "", ""},
		{"mkdir", "", ""},
		{"rename", "", "/remote/ok"},
	}
	for _, op := range ops {
		for _, p := range bad {
			remote := op.remote
			if remote == "" {
				remote = p
			}
			_, err := c.buildBatch(op.op, remote, op.local)
			if err == nil {
				t.Fatalf("%s should reject control char in remote path %q", op.op, p)
			}
			if !strings.Contains(err.Error(), "控制字符") {
				t.Fatalf("%s error should mention 控制字符, got %v", op.op, err)
			}
		}
	}
	for _, p := range bad {
		if _, err := c.buildBatch("get", "/remote/ok", p); err == nil {
			t.Fatalf("get should reject control char in local path %q", p)
		}
		if _, err := c.buildBatch("put", "/remote/ok", p); err == nil {
			t.Fatalf("put should reject control char in local path %q", p)
		}
		if _, err := c.buildBatch("rename", "/remote/ok", p); err == nil {
			t.Fatalf("rename should reject control char in new path %q", p)
		}
	}
	if b, err := c.buildBatch("nope", "/a", "/b"); err != nil || b != nil {
		t.Fatalf("unknown op should return nil,nil; got %q,%v", b, err)
	}
}

func TestConnectArgsIncludeConnectTimeout(t *testing.T) {
	if isWindows {
		t.Skip("ControlMaster connect path is unix-only")
	}
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	if err := c.Connect("myhost", ""); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(fr.calls) == 0 {
		t.Fatal("runner not invoked")
	}
	if fr.calls[0].name != "ssh" {
		t.Fatalf("want ssh, got %q", fr.calls[0].name)
	}
	if !hasArg(fr.calls[0].args, "ConnectTimeout=10") {
		t.Fatalf("ssh args missing ConnectTimeout=10: %v", fr.calls[0].args)
	}
}

func TestRunArgsIncludeConnectTimeout(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	if _, err := c.run("myhost", "", []byte("ls -l \"/\"\n")); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fr.calls) == 0 {
		t.Fatal("runner not invoked")
	}
	if fr.calls[0].name != "sftp" {
		t.Fatalf("want sftp, got %q", fr.calls[0].name)
	}
	if !hasArg(fr.calls[0].args, "ConnectTimeout=10") {
		t.Fatalf("sftp args missing ConnectTimeout=10: %v", fr.calls[0].args)
	}
}

func TestDisconnectArgsIncludeConnectTimeout(t *testing.T) {
	if isWindows {
		t.Skip("Windows is per-command mode; no ssh -O exit call")
	}
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	if err := c.Disconnect("myhost"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if len(fr.calls) == 0 {
		t.Fatal("runner not invoked")
	}
	if fr.calls[0].name != "ssh" {
		t.Fatalf("want ssh, got %q", fr.calls[0].name)
	}
	if !hasArg(fr.calls[0].args, "-O") || !hasArg(fr.calls[0].args, "exit") {
		t.Fatalf("want -O exit, got %v", fr.calls[0].args)
	}
	if !hasArg(fr.calls[0].args, "ConnectTimeout=10") {
		t.Fatalf("ssh -O exit args missing ConnectTimeout=10: %v", fr.calls[0].args)
	}
}

func TestCommandErr(t *testing.T) {
	// stderr takes priority
	if got := commandErr(osutil.Outcome{Stderr: "  Host key verification failed.  ", ExitCode: 255}); got != "Host key verification failed." {
		t.Fatalf("stderr trimmed wrong: %q", got)
	}
	// stderr empty falls back to stdout
	if got := commandErr(osutil.Outcome{Stdout: "Permission denied (publickey).", ExitCode: 255}); got != "Permission denied (publickey)." {
		t.Fatalf("stdout fallback wrong: %q", got)
	}
	// both empty -> exit code
	if got := commandErr(osutil.Outcome{ExitCode: 255}); got != "exit 255" {
		t.Fatalf("exit fallback wrong: %q", got)
	}
}
