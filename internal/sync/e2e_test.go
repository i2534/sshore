package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/osutil"
	"sshore/internal/sftp"
	"sshore/internal/sshconn"
)

// TestSyncE2E 需要 e2e/test_local.sh 先起好临时 sshd 并导出：
//
//	SSHORE_E2E_HOST   ssh 别名
//	SSHORE_E2E_REMOTE 远端存在的目录
//
// 缺少环境变量时跳过并打印原因（不静默通过）。harness 会把任何 SKIP 当失败（防假绿）。
//
// 本用例除了「文件真的同步下来」，还是 D18 内置忽略的**端到端**验证（Task 15 补）：
// 远端业务文件旁边混着内部临时文件（.sshore-sftppart- 中缀的 .part / 备份）时，后者既不
// 能进候选、更不能被下载到本地（unit 级的 TestEngineIgnoresInternalTempRemoteFiles 只钉了
// 引擎行为，这里用真实 sshd + 真实文件系统再钉一次）。本地同时预置一份内部临时文件，
// 断言内置忽略不会误删/误改本地文件（sync 是远端→本地单向，不涉及上传）。
func TestSyncE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE，跳过真实同步验证")
	}
	// 新底座（GoBackend）只依赖 ssh：sshd 通过 `ssh -s <host> sftp` 进入子系统，不再需要
	// 独立的 sftp 客户端。旧写死 LookPath("sftp") 会在只装 openssh-client 的环境里把整条
	// sync e2e 静默 skip —— 那正是 Task 15 要消灭的假绿。
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("缺少 ssh 二进制")
	}

	// 专用子目录：远端从空白开始，避免同一 TMPD 下其它 e2e 用例的残留文件让「谁先同步完」
	// 变得不确定（旧版一旦本地目录出现任何文件就 return，是本用例最弱的一环）。
	syncRemote := filepath.Join(remote, "sync-e2e")
	if err := os.RemoveAll(syncRemote); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(syncRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	const appConf = "v1"
	if err := os.WriteFile(filepath.Join(syncRemote, "app.conf"), []byte(appConf), 0o600); err != nil {
		t.Fatal(err)
	}
	// D18：远端业务文件旁边的内部临时文件绝不能进候选 / 被落盘。
	const internalContent = "HALF-WRITTEN"
	for _, name := range []string{
		"app.conf" + sftp.PartMarker + "e2e-ab", // 常规名（传输中的 .part）
		sftp.PartMarker + "bak-e2e-ef",          // 退化短名 / 备份（同中缀）
	} {
		if err := os.WriteFile(filepath.Join(syncRemote, name), []byte(internalContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctrl := sftp.NewCtrl(osutil.NewRunner(), nil)
	defer ctrl.CloseAll()
	t.Logf("sync e2e backend=%v（门面 TransportKind，便于真机按用例读后端）", ctrl.TransportKind())
	transfer, lister := NewSftpAdapter(ctrl)
	local := t.TempDir()
	// 本地也放一份内部临时文件：内置忽略对本地事件同样生效，它必须原样保留。
	localPart := filepath.Join(local, "stale.conf"+sftp.PartMarker+"e2e-cd")
	if err := os.WriteFile(localPart, []byte(internalContent), 0o600); err != nil {
		t.Fatal(err)
	}

	rule := config.SyncRule{
		ID: "e2e", Host: host, Kind: "dir", RemotePath: syncRemote,
		LocalPath: local, MaxDepth: 0, PollIntervalS: 1, ForcePoll: true, Enabled: true,
	}
	rule.Normalize()

	runner := osutil.NewCtxRunner()
	if err := sshconn.EnsureMaster(context.Background(), runner, host, ""); err != nil {
		t.Logf("EnsureMaster 失败（允许，会退化为每命令独立连接）: %v", err)
	}

	c := NewCtrl(Deps{
		Runner: runner, Transfer: transfer,
		ListMany: lister, StateDir: t.TempDir(),
	})
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	// 等**指定**业务文件落地（不是「本地出现任意文件」），并把内容也比对掉。
	target := filepath.Join(local, "app.conf")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(target); err == nil && string(b) == appConf {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != appConf {
		t.Fatalf("15s 内未把 %s/app.conf 同步到本地: err=%v content=%q stats=%+v", syncRemote, err, b, c.Stats()[rule.ID])
	}

	// 远端内部临时文件不得以任何名字、任何内容落到本地（本地预置那一份除外，见下）。
	entries, err := os.ReadDir(local)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == filepath.Base(localPart) {
			continue // 本地预置的内部临时文件由下面的保留断言负责
		}
		if sftp.IsInternalTemp(e.Name()) {
			t.Fatalf("远端内部临时文件绝不能被同步到本地: %s", e.Name())
		}
		b, _ := os.ReadFile(filepath.Join(local, e.Name()))
		if string(b) == internalContent {
			t.Fatalf("内部临时文件内容被当业务文件同步了: %s", e.Name())
		}
	}
	// 本地预置的内部临时文件必须原样保留（内置忽略不等于误删本地文件）。
	if b, err := os.ReadFile(localPart); err != nil || string(b) != internalContent {
		t.Fatalf("本地内部临时文件必须原样保留: err=%v content=%q", err, b)
	}
	t.Logf("sync e2e 通过：app.conf=%q 已落地，远端/本地内部临时文件均被忽略", appConf)
}
