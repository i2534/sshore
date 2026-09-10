package sync

import (
	"context"
	"os"
	"os/exec"
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
// 缺少环境变量时跳过并打印原因（不静默通过）。
func TestSyncE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE，跳过真实同步验证")
	}
	if _, err := exec.LookPath("sftp"); err != nil {
		t.Skip("缺少 sftp 二进制")
	}

	ctrl := sftp.NewCtrl(osutil.NewRunner(), nil)
	transfer, lister := NewSftpAdapter(ctrl)
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "e2e", Host: host, Kind: "dir", RemotePath: remote,
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

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(local); err == nil && len(entries) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("15s 内未把 %s 下的文件同步到本地", remote)
}
