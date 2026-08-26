package sftp

import (
	"os"
	"strings"
	"testing"

	"sshkit/internal/osutil"
)

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
