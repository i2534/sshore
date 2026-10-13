package clipboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewTempDirUniqueAndPrefixed(t *testing.T) {
	a, err := NewTempDir()
	if err != nil {
		t.Fatalf("NewTempDir() #1: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(a) })

	b, err := NewTempDir()
	if err != nil {
		t.Fatalf("NewTempDir() #2: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(b) })

	if a == b {
		t.Fatalf("两次调用返回了同一路径 %q", a)
	}
	for _, p := range []string{a, b} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat(%q): %v", p, err)
		}
		if !st.IsDir() {
			t.Errorf("%q 不是目录", p)
		}
		if !strings.HasPrefix(filepath.Base(p), TempDirPrefix) {
			t.Errorf("%q 缺少前缀 %q", filepath.Base(p), TempDirPrefix)
		}
		if filepath.Dir(p) != os.TempDir() {
			t.Errorf("%q 不在 os.TempDir() (%q) 下", p, os.TempDir())
		}
	}
}

func TestCleanStaleTempDirsRemovesOldDirsOnly(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	mk := func(name string, mod time.Time, wantDir bool) string {
		p := filepath.Join(root, name)
		var err error
		if wantDir {
			err = os.Mkdir(p, 0o755)
		} else {
			err = os.WriteFile(p, []byte("x"), 0o644)
		}
		if err != nil {
			t.Fatalf("建 %q: %v", name, err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatalf("Chtimes(%q): %v", name, err)
		}
		return p
	}

	old := mk(TempDirPrefix+"old", now.Add(-2*time.Hour), true)
	fresh := mk(TempDirPrefix+"new", now.Add(-10*time.Minute), true)
	unrelated := mk("other-old", now.Add(-2*time.Hour), true)
	samePrefixFile := mk(TempDirPrefix+"file", now.Add(-2*time.Hour), false)

	n, err := CleanStaleTempDirs(root, time.Hour, now)
	if err != nil {
		t.Fatalf("CleanStaleTempDirs: %v", err)
	}
	if n != 1 {
		t.Errorf("删除数=%d, want 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("陈旧目录 %q 应该被删除, Stat err=%v", old, err)
	}
	for _, p := range []string{fresh, unrelated, samePrefixFile} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%q 不该被删除: %v", p, err)
		}
	}
}

func TestCleanStaleTempDirsMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-there")
	n, err := CleanStaleTempDirs(root, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("root 不存在时不该报错: %v", err)
	}
	if n != 0 {
		t.Errorf("删除数=%d, want 0", n)
	}
}
