package sftp

import (
	"os"
	"testing"

	"sshkit/internal/osutil"
)

func TestBuildBatchAndWrite(t *testing.T) {
	c := NewCtrl(nil, nil)
	dir := t.TempDir()
	b := c.buildBatch("get", "/remote/path", "/local/path")
	if got := string(b); got != "get /remote/path /local/path\n" {
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
