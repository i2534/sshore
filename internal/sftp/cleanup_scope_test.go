package sftp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/pkg/sftp"
)

// —— 2026-10-10 复核轮 2：上传侧重试前清理的**扫描面**与**判据边界** ——
//
// 新语义下 PutTree 的 `req.Remote` 是「用户可能已有内容的最终目录」（改前那个目录是刚为
// 本棵树新建的、基本为空）。于是「递归遍历整棵既有子树来清 .part」从『几乎空目录』变成
// 一条会随用户既有数据规模增长的路径：每个目录一次 SFTP ReadDir 往返，且在 shortID 只截
// 8 rune、发生碰撞时可能扫到与本树无关的分支。

// countingReadRemoteDir 把 readRemoteDir 注入点换成计数版本，返回「被扫描过的目录」快照函数。
// 生产实现在每个待扫目录上恰好发一次 ReadDir，因此这个列表就是 SFTP 往返次数的可观测证据。
func countingReadRemoteDir(t *testing.T) func() []string {
	t.Helper()
	orig := readRemoteDir
	var mu sync.Mutex
	var scanned []string
	readRemoteDir = func(c *sftp.Client, ctx context.Context, dir string) ([]os.FileInfo, error) {
		mu.Lock()
		scanned = append(scanned, path.Clean(dir))
		mu.Unlock()
		return orig(c, ctx, dir)
	}
	t.Cleanup(func() { readRemoteDir = orig })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), scanned...)
		sort.Strings(out)
		return out
	}
}

// TestGoBackendPutTreeCleanupScopeIsWriteDirSet（Important-1，RED→GREEN）：清理只允许扫到
// 本棵树**实际会写到**的目录集合 —— 由本次要传的 files 的 rel 推导出父目录集合（含目标根）。
//
// 断言分两半，缺一不可：
//   - 行为收紧：位于本树**不会**写到的子目录（out/unrelated/）里的过期 .part，即使
//     basename 与本树目标同名（big.bin）、且 id 的 8 rune 短前缀与本次 id **完全相同**
//     （shortID 碰撞），也必须原样留下 —— 那是别的任务/别的分支的锚点；
//   - 行为等价：本树会写到的目录（目标根 out、子目录 out/sub）里，本 id 的过期 .part
//     照旧在传送开始前被清掉。
func TestGoBackendPutTreeCleanupScopeIsWriteDirSet(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/big.bin": "BIG", "a/sub/y.bin": "Y"})
	g := backendForTestServer(t, remoteRoot)

	// seq 已 5 位、n 已 2 位：t10000-10 与 t10000-11 的 shortID 都是 "t10000-1"。
	const id, collidingID = "t10000-10", "t10000-11"
	if shortID(id) != shortID(collidingID) {
		t.Fatalf("本用例前提：两个 id 必须共享 8 rune 短前缀, %q vs %q", shortID(id), shortID(collidingID))
	}

	rootRemote := filepath.Join(remoteRoot, "out")
	mineAtRoot := filepath.Join(rootRemote, "big.bin"+PartMarker+shortID(id)+"-stale1")
	mineInSub := filepath.Join(rootRemote, "sub", "y.bin"+PartMarker+shortID(id)+"-stale2")
	unwritten := filepath.Join(rootRemote, "unrelated", "big.bin"+PartMarker+shortID(collidingID)+"-dead1")
	foreign := filepath.Join(rootRemote, "unrelated", "big.bin"+PartMarker+"zzz9-keepme")
	// 既有子树的噪声：让「扫描目录数随既有子树规模增长」有可观测的差异面。
	for i := 0; i < 40; i++ {
		if err := os.MkdirAll(filepath.Join(rootRemote, "unrelated", fmt.Sprintf("noise%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{mineAtRoot, mineInSub, unwritten, foreign} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := g.PutTree(TransferRequest{ID: id, Host: "h", Remote: "out", Local: filepath.Join(src, "a"), Atomic: true}, nil); err != nil {
		t.Fatalf("PutTree: %v", err)
	}

	for _, p := range []string{mineAtRoot, mineInSub} {
		if _, serr := os.Stat(p); !os.IsNotExist(serr) {
			t.Fatalf("本树写入目录里的本项过期 .part 必须被清掉: %s stat err=%v", p, serr)
		}
	}
	for _, p := range []string{unwritten, foreign} {
		if _, serr := os.Stat(p); serr != nil {
			t.Fatalf("本树不会写到的目录里的 .part 不得被扫到/删除: %s err=%v", p, serr)
		}
	}
}

// TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet（Important-1 验收判据 ④）：
// 扫描的目录数（= ReadDir 往返次数）必须与本树写入目录集合同阶，**不随既有目标子树规模增长**。
//
// 这里在同一棵远端树里放 40 个与本树无关的既有子目录：旧实现（递归整棵子树）会扫描 1 + 1 + 41
// 个目录；新实现只扫 out 与 out/sub 两个。
func TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/big.bin": "BIG", "a/sub/y.bin": "Y"})
	g := backendForTestServer(t, remoteRoot)

	rootRemote := filepath.Join(remoteRoot, "out")
	for i := 0; i < 40; i++ {
		if err := os.MkdirAll(filepath.Join(rootRemote, "unrelated", fmt.Sprintf("noise%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scanned := countingReadRemoteDir(t)
	if err := g.PutTree(TransferRequest{ID: "t10000-10", Host: "h", Remote: "out", Local: filepath.Join(src, "a"), Atomic: true}, nil); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	got := scanned()
	want := []string{"out", "out/sub"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("清理扫描的远端目录 = %v, want %v（必须只扫本树写入目录集合，不得递归既有子树）", got, want)
	}
}

// TestPutTreeCleanupDirsIsParentSetOfRel：扫描面由 rel 的父目录推导（去重 + 含目标根）。
// 直接钉纯函数，避免「实现里换回递归遍历」时只有大用例才变红。
func TestPutTreeCleanupDirsIsParentSetOfRel(t *testing.T) {
	files := []treeFile{
		{rel: "x.txt"},
		{rel: "sub/y.txt"},
		{rel: "sub/deep/z.txt"},
		{rel: "sub/another.txt"},
	}
	got := putTreeCleanupDirs(files)
	want := []string{".", "sub", "sub/deep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("putTreeCleanupDirs = %v, want %v", got, want)
	}
	if n := len(putTreeCleanupDirs(nil)); n != 1 {
		t.Fatalf("空树也必须含目标根一个目录（用于清根目录里的过期 .part），got %d", n)
	}
}
