package sftp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBakFilesAreNotCleaned（2026-10-10 复核轮 2 / Minor-3）：backup-swap 的 .bak 名字后缀是
// `bak-` 而不是 `<shortID>-`，永远过不了 isPartForTarget —— 也就是说**清理不删 .bak**。
// 本用例按**生成器真实形状**（BakNameRemote，而不是手写字符串）把这点钉住：
//   - 判据层：常规名与退化短名两种 bak 形状都不得被认成「本次传输的临时文件」；
//   - 清理层：远端真实存在的 bak 在重试清理后必须原样留下。
//
// 这样将来若有人「为了对齐旧注释」而显式加 bak 判据，这里会先变红，必须显式改断言并说明
// 「删除面扩大」的理由（bak 名里没有 id，无法区分自己与别人留下的）。
func TestBakFilesAreNotCleaned(t *testing.T) {
	longTarget := "/data/" + strings.Repeat("z", 250) + ".bin" // 触发退化短名分支
	for _, target := range []string{"big.bin", longTarget} {
		bak := BakNameRemote(target)
		if !IsInternalTemp(filepath.Base(bak)) {
			t.Fatalf("bak 必须仍被识别为内部临时文件（D18 过滤依赖中缀）: %q", bak)
		}
		if isPartForTarget(filepath.Base(bak), target, "t10000-10") {
			t.Fatalf("bak 不得被认成本次传输的 .part（后缀是 bak- 而非 <shortID>-）: %q target=%q", bak, target)
		}
	}

	root := t.TempDir()
	g := backendForTestServer(t, root)
	bakRel := "sub/" + filepath.Base(BakNameRemote("big.bin"))
	writeRemoteTree(t, root, bakRel, []byte("old-version"))
	bak := filepath.Join(root, filepath.FromSlash(bakRel))
	s, err := g.pool.AcquireList(context.Background(), "h", "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.pool.Release(s, false)
	if n := g.cleanupRemoteTreeParts(context.Background(), s, "sub", []cleanupScope{{dir: ".", targets: []string{"big.bin"}}}, "t10000-10"); n != 0 {
		t.Fatalf("清理不得删 backup-swap 的 .bak，got removed=%d", n)
	}
	if _, serr := os.Stat(bak); serr != nil {
		t.Fatalf("bak 必须原样留下: %v", serr)
	}
}
