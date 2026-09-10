package sync

import (
	"path/filepath"
	"strings"
	"testing"
)

// 远端可回传任意文件名，RelPath 是不可信的。
func TestSafeRelPathRejectsTraversal(t *testing.T) {
	bad := []string{
		"", ".", "..", "../etc/passwd", "a/../../b", "/abs/path",
		"a/b/../../..", "C:\\Windows\\x", "a\nb", "a\x00b", "a\r", "\tfile", "a ", " a", "a\u00a0",
	}
	for _, rel := range bad {
		if got, ok := SafeRelPath(rel); ok {
			t.Fatalf("SafeRelPath(%q) 必须拒绝，却得到 %q", rel, got)
		}
	}
	good := map[string]string{"a.txt": "a.txt", "d/b.txt": "d/b.txt", "d/./c.txt": "d/c.txt"}
	for in, want := range good {
		got, ok := SafeRelPath(in)
		if !ok || got != want {
			t.Fatalf("SafeRelPath(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

// 拼出的本地绝对路径必须仍在 root 之内（防穿越的硬防线）。
func TestLocalTargetStaysInsideRoot(t *testing.T) {
	root := filepath.Join(strings.Repeat("x", 1), "root")
	if _, err := LocalTarget(root, "../etc/passwd"); err == nil {
		t.Fatal("必须拒绝逃出 root 的路径")
	}
	got, err := LocalTarget(root, "d/b.txt")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Fatalf("目标必须在 root 内，得到 %q", got)
	}
}
