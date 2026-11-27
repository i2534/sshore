package localfs

import (
	"errors"
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

// A5: copyFile 必须透出 out.Close() 的错误 —— 写满盘/NFS 写回失败恰恰发生在
// Close 时刻，忽略它就是「假成功」。注入点 closeFileOut 仅测试用。
func TestCopyFileSurfacesCloseError(t *testing.T) {
	old := closeFileOut
	t.Cleanup(func() { closeFileOut = old })
	sentinel := errors.New("close: no space left on device")
	closeFileOut = func(f *os.File) error { return sentinel }

	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "b.txt")
	err := Copy(src, dst)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Close 错误必须被透出: got %v", err)
	}
}

// A5: io.Copy 与 Close 都失败时优先返回 Copy 错误（Close 错误不得覆盖首因）。
func TestCopyFilePrefersCopyErrorOverCloseError(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full 不可用")
	}
	old := closeFileOut
	t.Cleanup(func() { closeFileOut = old })
	sentinel := errors.New("close sentinel")
	closeFileOut = func(f *os.File) error { return sentinel }

	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Copy(src, "/dev/full")
	if err == nil {
		t.Fatal("写 /dev/full 必须失败")
	}
	if errors.Is(err, sentinel) {
		t.Fatalf("两者都失败时必须优先返回 Copy 错误: %v", err)
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

// A6: goos=windows 大小写不敏感 + 分隔符统一；goos=linux 保持 POSIX 大小写敏感。
func TestIsSubPathGOOS(t *testing.T) {
	cases := []struct {
		parent, child, goos string
		want                bool
	}{
		{`C:\Data`, `c:\data\sub`, "windows", true},
		{`C:\Data`, `C:\DATA`, "windows", true},             // 相等（忽略大小写）
		{`C:\Data`, `C:\Database`, "windows", false},        // 兄弟目录不是子树
		{`C:\Data`, `c:/data/sub`, "windows", true},         // 分隔符统一
		{`C:\Data\`, `c:\data\sub`, "windows", true},        // 尾部分隔符
		{`\\srv\share`, `\\SRV\Share\sub`, "windows", true}, // UNC
		{"/a", "/A/b", "linux", false},
		{"/a", "/a/b", "linux", true},
		{"/a", "/a", "linux", true},
		{"/a", "/ab", "linux", false},
	}
	for _, c := range cases {
		if got := isSubPathGOOS(c.parent, c.child, c.goos); got != c.want {
			t.Fatalf("isSubPathGOOS(%q,%q,%q) = %v, want %v", c.parent, c.child, c.goos, got, c.want)
		}
	}
}
