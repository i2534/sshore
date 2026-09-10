package watch

import (
	"context"
	"testing"

	"sshore/internal/sftp"
)

func fakeList(tree map[string][]sftp.Item) ListManyFunc {
	return func(host, user string, paths []string) (map[string][]sftp.Item, error) {
		res := map[string][]sftp.Item{}
		for _, p := range paths {
			if v, ok := tree[p]; ok {
				res[p] = v
			}
		}
		return res, nil
	}
}

func file(name string) sftp.Item { return sftp.Item{Name: name, Size: 1, ModTime: "2026-09-10 10:00"} }
func dir(name string) sftp.Item  { return sftp.Item{Name: name, IsDir: true} }

func TestScanTreeRespectsMaxDepth(t *testing.T) {
	tree := map[string][]sftp.Item{
		"/r":       {file("a.txt"), dir("d1")},
		"/r/d1":    {file("b.txt"), dir("d2")},
		"/r/d1/d2": {file("c.txt")},
	}
	one, err := ScanTree(context.Background(), fakeList(tree), "h", "", "/r", 1, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !one.Complete {
		t.Fatal("全部目录可读时应 Complete")
	}
	if _, ok := one.Entries["a.txt"]; !ok {
		t.Fatalf("缺少本层文件，得到 %#v", one.Entries)
	}
	if _, ok := one.Entries["d1/b.txt"]; !ok {
		t.Fatalf("maxDepth=1 应含 d1/b.txt，得到 %#v", one.Entries)
	}
	if _, ok := one.Entries["d1/d2/c.txt"]; ok {
		t.Fatal("maxDepth=1 不应递归到两层")
	}
}

// 缺失的目录 = 未知 ⇒ Complete=false，调用方据此禁止一切 delete。
func TestScanTreeMarksIncompleteOnUnknownDir(t *testing.T) {
	tree := map[string][]sftp.Item{
		"/r":    {dir("d1"), dir("locked")},
		"/r/d1": {file("a.txt")},
	}
	snap, err := ScanTree(context.Background(), fakeList(tree), "h", "", "/r", -1, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if snap.Complete {
		t.Fatal("有目录未知时必须 Complete=false")
	}
}

// 根路径带尾斜杠时 rel 仍须正确（不能算成 "r/d1/b.txt"）。
func TestScanTreeTrailingSlashRoot(t *testing.T) {
	tree := map[string][]sftp.Item{
		"/r":    {file("a.txt"), dir("d1")},
		"/r/d1": {file("b.txt")},
	}
	snap, err := ScanTree(context.Background(), fakeList(tree), "h", "", "/r/", -1, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, ok := snap.Entries["d1/b.txt"]; !ok {
		t.Fatalf("尾斜杠根路径下 rel 必须正确，得到 %#v", snap.Entries)
	}
}

// 契约的另一半：存在的 key + 0 项是"真的空目录"，不是未知。
func TestScanTreePresentButEmptyStaysComplete(t *testing.T) {
	tree := map[string][]sftp.Item{"/r": {}}
	snap, err := ScanTree(context.Background(), fakeList(tree), "h", "", "/r", -1, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !snap.Complete {
		t.Fatal("可列出但为空的目录不是未知，必须仍是 Complete")
	}
	if len(snap.Entries) != 0 {
		t.Fatalf("空目录不应产生条目，得到 %#v", snap.Entries)
	}
}

// 大扫描必须能取消：ctx 已取消时不得再发起任何一批列举。
func TestScanTreeHonoursContext(t *testing.T) {
	calls := 0
	list := func(host, user string, paths []string) (map[string][]sftp.Item, error) {
		calls++
		res := map[string][]sftp.Item{}
		for _, p := range paths {
			res[p] = []sftp.Item{dir("d"), file("f")}
		}
		return res, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanTree(ctx, list, "h", "", "/r", -1, nil); err == nil {
		t.Fatal("已取消的 ctx 必须让 ScanTree 提前返回错误")
	}
	if calls != 0 {
		t.Fatalf("已取消的 ctx 不应再调用 list，calls=%d", calls)
	}
}

func TestMatchExclude(t *testing.T) {
	ex := []string{".git/", "node_modules/", "build*/", "*.swp", "*~"}
	cases := map[string]bool{
		".git": true, ".git/config": true, "node_modules/x.js": true,
		"a/.hidden.swp": true, "b/backup~": true, "src/main.go": false,
		// 目录名模式必须在**任意深度**生效（ruling 1）。
		"x/.git/config":         true,
		"x/.git":                true,
		"a/b/node_modules/c.js": true,
		"deep/a/node_modules":   true,
		// 目录名本身可含 glob。
		"x/buildout/f.js": true,
	}
	for rel, want := range cases {
		if got := MatchExclude(rel, ex); got != want {
			t.Fatalf("MatchExclude(%q) = %v want %v", rel, got, want)
		}
	}
}
