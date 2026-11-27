package sftp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

// —— S1：commitRemote 在 Begin 覆盖同键旧条目前必须先恢复待恢复现场 ——
//
// 背景：journal.Begin 的去重（同 host/user/target 替换旧条目）会把一条处于
// 「target→bak 已成功、part→target 失败、回滚也失败」待恢复态（或上次崩溃现场）的旧条目
// 顶掉。旧 bak 是目标旧内容的唯一副本，被顶掉后成永久孤儿（RecoverSwaps 只在退出时跑）。
// 守卫放在 commitRemote 层：Begin 之前先按决策表恢复同键旧条目，恢复成功才允许覆盖；
// 恢复失败则中止本次上传、旧条目原样保留。

// TestCommitRemoteRecoversPendingSwapBeforeBegin：旧条目处于 W1 待恢复态（bak 在、target 缺），
// 同键新上传必须先把旧 bak 回滚成 target（恢复旧内容），再走本次 backup-swap 提交新内容。
// 旧 bak 不得成为孤儿（失去 journal 引用又残留磁盘）。
func TestCommitRemoteRecoversPendingSwapBeforeBegin(t *testing.T) {
	remoteRoot := t.TempDir()
	const target = "dst.bin"
	const oldBak = "dst.bin.sshore-sftppart-bak-old"
	const oldPart = "dst.bin.sshore-sftppart-t0-old"
	newPart := PartNameRemote(target, "t1")
	// W1 现场：target→bak 已成功（旧内容只在 oldBak），part→target 与回滚都失败 ⇒ target 名暂缺。
	writeRemote(t, remoteRoot, oldBak, []byte("OLD"))
	writeRemote(t, remoteRoot, newPart, []byte("NEW"))
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Host: "h", User: "", Target: target, Bak: oldBak, Part: oldPart})

	g := backendForTestServer(t, remoteRoot)
	s := acquireTransferSession(t, g)
	j := newSwapJournal(jdir)
	if err := commitRemote(s, false, newPart, target, j, nil); err != nil {
		t.Fatalf("旧条目可恢复时应先恢复再提交: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(remoteRoot, target)); string(b) != "NEW" {
		t.Fatalf("目标应为新内容, got %q", string(b))
	}
	// 旧 bak 必须已被恢复流程消费（回滚成 target 后再被本次 swap 用掉），绝不残留孤儿。
	if _, err := os.Stat(filepath.Join(remoteRoot, oldBak)); !os.IsNotExist(err) {
		t.Fatalf("旧 bak 不得成为孤儿（失去 journal 引用又残留磁盘）: stat err=%v", err)
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("成功后不得残留 .part/.bak: %v", temps)
	}
	if got := journalEntries(t, jdir); len(got) != 0 {
		t.Fatalf("恢复+提交后 journal 必须清空: %+v", got)
	}
}

// TestCommitRemoteRejectsWhenPendingSwapRecoveryFails：旧条目处于 W1 待恢复态，但恢复回滚
// 失败（rename(bak→target) 被拒）时，必须中止本次上传并原样保留旧条目/旧 bak/新 part，
// 绝不静默顶掉旧条目、绝不动 target。
func TestCommitRemoteRejectsWhenPendingSwapRecoveryFails(t *testing.T) {
	remoteRoot := t.TempDir()
	const target = "dst.bin"
	const oldBak = "dst.bin.sshore-sftppart-bak-old"
	const oldPart = "dst.bin.sshore-sftppart-t0-old"
	newPart := PartNameRemote(target, "t1")
	writeRemote(t, remoteRoot, oldBak, []byte("OLD"))
	writeRemote(t, remoteRoot, newPart, []byte("NEW"))
	jdir := t.TempDir()
	writeSwapEntry(t, jdir, swapEntry{Host: "h", User: "", Target: target, Bak: oldBak, Part: oldPart})

	g := backendForTestServer(t, remoteRoot)
	s := acquireTransferSession(t, g)
	boom := errors.New("rollback denied")
	var orig func(*sftp.Client, string, string) error
	orig = swapRenameRemote(t, func(c *sftp.Client, oldname, newname string) error {
		if oldname == oldBak && newname == target {
			return boom // 恢复回滚失败
		}
		return orig(c, oldname, newname)
	})
	j := newSwapJournal(jdir)
	err := commitRemote(s, false, newPart, target, j, nil)
	if err == nil {
		t.Fatal("旧条目恢复失败时必须中止本次上传")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("错误必须保留恢复失败原因: %v", err)
	}
	got := journalEntries(t, jdir)
	if len(got) != 1 || got[0].Target != target || got[0].Bak != oldBak || got[0].Part != oldPart {
		t.Fatalf("恢复失败必须原样保留旧条目: %+v", got)
	}
	if b, rerr := os.ReadFile(filepath.Join(remoteRoot, oldBak)); rerr != nil || string(b) != "OLD" {
		t.Fatalf("旧 bak 必须保留旧内容: err=%v content=%q", rerr, string(b))
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, target)); !os.IsNotExist(serr) {
		t.Fatalf("恢复失败不得改动 target: stat err=%v", serr)
	}
	if b, rerr := os.ReadFile(filepath.Join(remoteRoot, newPart)); rerr != nil || string(b) != "NEW" {
		t.Fatalf("新 .part 必须原样保留: err=%v content=%q", rerr, string(b))
	}
}
