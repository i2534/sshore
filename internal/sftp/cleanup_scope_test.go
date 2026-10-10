package sftp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

// TestGoBackendPutTreeCleanupTargetsArePerDir（复核轮 2 / Minor-4 的判据收窄，RED→GREEN）：
// 每个待扫目录只认「**该目录里**本树会写的 basename」，而不是「本树所有目标的 basename 并集」。
//
// 为什么必须收窄：并集判据下，只要某个 basename 在本树任意位置出现，本树写到的**任何**目录里
// 同名 .part 都会被删 —— 于是「同一最终路径」之外仍然可能删到别的任务的锚点（配合 shortID
// 的 8 rune 截断碰撞）。按目录切分后，误删的必要条件正好收敛为「同一目录 + 同一 basename
// = 同一最终目标路径 + 8 rune 碰撞」，也就是真实的同文件冲突。
//
// 本用例：本树 = out/big.bin + out/sub/y.bin。out/sub 里放一个 **big.bin** 的过期 .part
// （big.bin 是本树目标，但本树在 out/sub 里只写 y.bin）—— 并集判据会删掉它，按目录判据必须留下；
// 同一目录里 y.bin 的本项 .part 照旧必须被清掉（完备性不能因为收窄而丢）。
func TestGoBackendPutTreeCleanupTargetsArePerDir(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/big.bin": "BIG", "a/sub/y.bin": "Y"})
	g := backendForTestServer(t, remoteRoot)

	const id, collidingID = "t10000-10", "t10000-11"
	rootRemote := filepath.Join(remoteRoot, "out")
	// 别的任务（id 前 8 rune 与本次相同）在 out/sub 里传 big.bin：本树在 out/sub 只写 y.bin，
	// 这条 .part 与「本树会写的最终路径」无关，必须留下。
	foreignInSub := filepath.Join(rootRemote, "sub", "big.bin"+PartMarker+shortID(collidingID)+"-dead1")
	// 本树自己的过期 .part（out/sub/y.bin）：必须清掉。
	mineInSub := filepath.Join(rootRemote, "sub", "y.bin"+PartMarker+shortID(id)+"-stale2")
	for _, p := range []string{foreignInSub, mineInSub} {
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
	if _, serr := os.Stat(mineInSub); !os.IsNotExist(serr) {
		t.Fatalf("同一目录里本项目标的过期 .part 必须被清掉: %s stat err=%v", mineInSub, serr)
	}
	if _, serr := os.Stat(foreignInSub); serr != nil {
		t.Fatalf("本树在该目录里不写 big.bin，别的任务的 big.bin .part 不得被删: %s err=%v", foreignInSub, serr)
	}
}

// TestPutTreeCleanupScopeIsPerDirTargetSet：直接钉纯函数 —— 扫描面 = {path.Dir(rel) → basename}，
// 每个目录只带**该目录里**会写的目标；空树不产生任何扫描单元。
func TestPutTreeCleanupScopeIsPerDirTargetSet(t *testing.T) {
	files := []treeFile{
		{rel: "x.txt"},
		{rel: "sub/y.txt"},
		{rel: "sub/deep/z.txt"},
		{rel: "sub/another.txt"},
	}
	got := putTreeCleanupScope(files)
	want := []cleanupScope{
		{dir: ".", targets: []string{"x.txt"}},
		{dir: "sub", targets: []string{"another.txt", "y.txt"}},
		{dir: "sub/deep", targets: []string{"z.txt"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("putTreeCleanupScope = %+v, want %+v", got, want)
	}
	if n := len(putTreeCleanupScope(nil)); n != 0 {
		t.Fatalf("空树没有任何会写的文件 ⇒ 不需要清理，got %d 个扫描单元", n)
	}
}

// TestPutTreeCleanupScopeRejectsUnsafeRels（复核轮 3 / Minor 的守卫）：推导侧拒绝
// rel 为空/绝对/含 ".."/basename 为 "." 或 ".." 的条目 —— scanLocalTree 实测不产出这类 rel，
// 这里防的是推导本身出错：宁可不扫（少删），也不能扫到目标树以外的目录。
func TestPutTreeCleanupScopeRejectsUnsafeRels(t *testing.T) {
	files := []treeFile{
		{rel: ""},
		{rel: "/abs/x.txt"},
		{rel: ".."},
		{rel: "../evil/x.txt"},
		{rel: "a/../../b.txt"},
		{rel: "a/.."},
		{rel: "ok/good.txt"},
	}
	got := putTreeCleanupScope(files)
	want := []cleanupScope{{dir: "ok", targets: []string{"good.txt"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("putTreeCleanupScope(含不安全 rel) = %+v, want %+v（不安全条目必须整体丢弃）", got, want)
	}
	for _, sc := range got {
		if sc.dir == ".." || strings.HasPrefix(sc.dir, "../") || path.IsAbs(sc.dir) || strings.Contains(sc.dir, "/../") {
			t.Fatalf("扫描面不得包含越出目标树的目录: %+v", sc)
		}
		for _, n := range sc.targets {
			if n == "." || n == ".." || strings.Contains(n, "/") {
				t.Fatalf("清理目标名必须是非 . / .. 的单段名: %q（scope=%+v）", n, sc)
			}
		}
	}
}
