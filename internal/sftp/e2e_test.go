package sftp

import (
	"os"
	"os/exec"
	"testing"

	"sshore/internal/osutil"
)

// TestGoBackendE2E 需要 e2e/test_local.sh 先起好临时 sshd 并导出 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE。
// 缺环境即 skip（保证 go test ./... 在无网络/无 sshd 时无声通过），
// 而 harness 会显式设置这些变量并把任何 SKIP 当作失败（防假绿）。
func TestGoBackendE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE，跳过真实传输验证")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("缺少 ssh 二进制")
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()

	// 本 task 只验「会话起得来 + 能力探测」；Home/List 要 Task 13 才实现（自审 S7）。
	ok, err := g.Capabilities(host, "")
	if err != nil {
		t.Fatalf("能力探测失败（会话没起来）: %v", err)
	}
	t.Logf("posix-rename 能力: %v", ok)
	// I1：能力探测结果必须断言，不能只 Log。plan 的 Expected 是 true（本地 sshd 与
	// Win32-OpenSSH 9.5p1 都由 internal-sftp 提供该扩展）；只 Log 的话，将来探测
	// 静默变 false（按裁决 1 会让 atomic 提交不可用）harness 仍然全绿。
	if !ok {
		t.Fatalf("远端未提供 posix-rename@openssh.com（预期 true）：能力探测静默降级会让 atomic 提交不可用")
	}
	if _, err := g.List(host, "", remote); err == nil {
		t.Log("提示：List 已实现（若已执行到 Task 13 则正常）")
	}

	// 让 SSHORE_SFTP_TRANSPORT 真正有意义：选中 batch 时也真跑一次 batch 后端，
	// 否则双后端循环的两次迭代执行的是同一段代码 —— 正是本 task 要消灭的假绿。
	// GoBackend 侧的真实执行就是上面的 Capabilities（真 ssh -s sftp 握手 + 探测）。
	if resolveTransport(nil) == KindBatch {
		b := NewBatchBackend(osutil.NewRunner(), nil)
		items, err := b.List(host, "", remote)
		if err != nil {
			t.Fatalf("选中 batch 后端就必须真跑通列表: %v", err)
		}
		t.Logf("batch 后端列出 %d 项", len(items))
	}
}
