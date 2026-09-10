package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTransferer struct {
	content string
	fail    bool
	lastTmp string
}

func (f *fakeTransferer) Get(host, user, remote, local string) error {
	f.lastTmp = local
	if f.fail {
		return errors.New("boom")
	}
	return os.WriteFile(local, []byte(f.content), 0644)
}

// 成功路径：先写临时文件再原子 rename，目标不会出现半截内容。
func TestTransferToIsAtomicOnSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	ft := &fakeTransferer{content: "hello"}
	if err := TransferTo(ft, "h", "", "/r/a.txt", target, "rule1", func() string { return "rand" }); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if !strings.Contains(ft.lastTmp, "rule1") {
		t.Fatalf("临时文件名必须含规则 id，得到 %q", ft.lastTmp)
	}
	if ft.lastTmp == target {
		t.Fatal("绝不能直接写目标文件")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hello" {
		t.Fatalf("目标内容不对: %q %v", got, err)
	}
	if _, err := os.Stat(ft.lastTmp); !os.IsNotExist(err) {
		t.Fatal("成功后临时文件必须已被 rename 掉")
	}
}

// 失败路径：目标文件不被破坏，临时文件被清理。
func TestTransferToKeepsTargetOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransferer{fail: true}
	if err := TransferTo(ft, "h", "", "/r/a.txt", target, "rule1", func() string { return "rand" }); err == nil {
		t.Fatal("want error")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Fatalf("失败时目标文件被破坏: %q", got)
	}
	if _, err := os.Stat(ft.lastTmp); !os.IsNotExist(err) {
		t.Fatal("失败后必须清理临时文件")
	}
}

// 两条规则各自清理自己的残留，不能互相误删。
func TestCleanupPartsOnlyOwnRule(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "a.txt"+PartSuffix("rule1")+"-x")
	other := filepath.Join(dir, "b.txt"+PartSuffix("rule2")+"-y")
	for _, p := range []string{mine, other} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CleanupParts(dir, "rule1")
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只清理 1 个，得到 %d", n)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("本规则的残留应被清理")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("别的规则的残留不能被误删")
	}
}
