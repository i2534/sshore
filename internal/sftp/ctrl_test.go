package sftp

import (
	"os"
	"testing"
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
