package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "b.txt")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
}

func TestCopyTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(src, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "inner", "x.txt"), []byte("x"), 0o644)
	dst := filepath.Join(dir, "out")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "inner", "x.txt")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
}

func TestCopySkipsSymlink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	if err := os.Symlink(t.TempDir(), filepath.Join(src, "link")); err != nil {
		t.Skip("symlink not supported")
	}
	dst := filepath.Join(dir, "out")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy must not fail on symlinked dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.txt")); err != nil {
		t.Fatalf("regular file must be copied: %v", err)
	}
}

func TestCopyOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	_ = os.WriteFile(src, []byte("new"), 0o644)
	_ = os.WriteFile(dst, []byte("old"), 0o644)
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Fatalf("overwrite failed: %q", got)
	}
}

func TestIsSubPath(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"/a", "/a/b", true},
		{"/a", "/a", true},
		{"/a", "/ab", false},
		{"/a/", "/a/b", true},
	}
	for _, c := range cases {
		if got := IsSubPath(c.parent, c.child); got != c.want {
			t.Fatalf("IsSubPath(%q,%q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}
