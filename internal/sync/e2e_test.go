package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// 引擎行为，这里用真实 sshd + 真实文件系统再钉一次）。
// 「本地预置一份内部临时文件并断言它保留」曾在这里，但因无判别力已删（Task 15 复审 M2）：
// sync 的候选集只来自远端扫描，本地文件从不进 state，去掉忽略那条断言照样通过。
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

	// Important-1：本用例走门面，门面必须真的按 SSHORE_SFTP_TRANSPORT 选后端，否则两次
	// 迭代跑的是同一后端。harness 会拿下面的 SSHORE_E2E_BACKEND= 行做跨迭代身份断言。
	rawTransport := strings.TrimSpace(os.Getenv("SSHORE_SFTP_TRANSPORT"))
	var wantKind sftp.BackendKind
	switch strings.ToLower(rawTransport) {
	case "batch":
		wantKind = sftp.KindBatch
	case "gosftp":
		wantKind = sftp.KindGo
	default:
		t.Fatalf("SSHORE_SFTP_TRANSPORT 未送达/非法（%q）：后端身份无法证明", rawTransport)
	}
	ctrl := sftp.NewCtrl(osutil.NewRunner(), nil)
	defer ctrl.CloseAll()
	obsName := "unknown"
	switch ctrl.TransportKind() {
	case sftp.KindBatch:
		obsName = "batch"
	case sftp.KindGo:
		obsName = "gosftp"
	}
	t.Logf("SSHORE_E2E_BACKEND=%s", obsName)
	if ctrl.TransportKind() != wantKind {
		t.Fatalf("门面未按 SSHORE_SFTP_TRANSPORT=%q 选后端：实测 %s", rawTransport, obsName)
	}
	transfer, lister := NewSftpAdapter(ctrl)
	local := t.TempDir()
	// 说明（Task 15 复审 M2）：这里曾预置一份**本地**内部临时文件并断言它原样保留，但那条断言
	// 没有判别力 —— sync 的候选集只来自远端扫描，本地文件从不登记进 state，去掉内置忽略它照样
	// 通过。与其留一条恒真的断言冒充防线，不如删掉；真正有判别力的是下面「远端临时文件不得落
	// 到本地」的检查（去掉忽略后远端 .part 会作为新条目被下载，该检查必然失败）。

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

	// Minor-1：app.conf 落地 ≠ 引擎处理完本轮对账。去掉内置忽略后，远端 .part/.bak 排在
	// app.conf 之后，applyOne 可能还没轮到它们，此刻读本地目录就会假绿（batch 迭代 5/5 能抓、
	// gosftp 迭代只 1/5 抓得到）。改成等引擎确定性收敛：AlignTotal>0 且 Done>=AlignTotal，
	// 即本轮快照里的每个条目都已处理过 —— 忽略一旦被移除，那两个 .part 必然已落盘。
	waitSyncSettled(t, c, rule.ID)

	// 远端内部临时文件不得以任何名字、任何内容落到本地。
	entries, err := os.ReadDir(local)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if sftp.IsInternalTemp(e.Name()) {
			t.Fatalf("远端内部临时文件绝不能被同步到本地: %s", e.Name())
		}
		b, _ := os.ReadFile(filepath.Join(local, e.Name()))
		if string(b) == internalContent {
			t.Fatalf("内部临时文件内容被当业务文件同步了: %s", e.Name())
		}
	}
	t.Logf("sync e2e 通过：app.conf=%q 已落地，远端内部临时文件被忽略", appConf)
}

// waitSyncSettled 等到本规则引擎把一轮对账处理完：AlignTotal>0 且 Done>=AlignTotal。
// 这是「远端快照里的每个条目都过了 applyOne」的可观测判据；超时即失败（引擎卡死同样是失败），
// 而不是像以前那样在 app.conf 刚落盘时就下结论。轮询 100ms、上限 20s。
func waitSyncSettled(t *testing.T, c *Ctrl, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last SyncRuleStat
	for time.Now().Before(deadline) {
		last = c.Stats()[id]
		if last.AlignTotal >= 1 && last.Done >= last.AlignTotal {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("sync 引擎 20s 内未完成对账（AlignTotal=%d Done=%d Pending=%d Failed=%d）",
		last.AlignTotal, last.Done, last.Pending, last.Failed)
}
