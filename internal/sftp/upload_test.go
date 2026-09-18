package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/osutil"
)

// —— Task 8 hermetic 上传测试 ——
// 与 Task 7 同样的模式：net.Pipe 接 pkg/sftp 的**真实客户端 + 真实服务端**（见
// copy_download_test.go 的 backendForTestServer），不联网、不起 ssh，但走真实 SFTP 协议。
// 注意：pkg/sftp 服务端不宣告 posix-rename 扩展，因此 Put 在这些用例里走的是
// **backup-swap** 分支（正是「扩展缺失路径」）；posix 分支由 commitRemote 直测覆盖。

// acquireTransferSession 拿一个真实（内存）会话，注册清理。
func acquireTransferSession(t *testing.T, g *GoBackend) *Session {
	t.Helper()
	s, err := g.pool.AcquireTransfer(context.Background(), "h", "")
	if err != nil {
		t.Fatalf("AcquireTransfer: %v", err)
	}
	t.Cleanup(func() { g.pool.Release(s, false) })
	return s
}

// swapPosixRename 替换 PosixRename 注入点，返回还原函数（t.Cleanup 自动还原）。
func swapPosixRename(t *testing.T, fn func(*sftp.Client, string, string) error) {
	t.Helper()
	orig := posixRename
	posixRename = fn
	t.Cleanup(func() { posixRename = orig })
}

// remoteTemps 列出远端根目录下的内部临时文件（.part/.bak）。
func remoteTemps(t *testing.T, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestGoBackendPutOverwritesExistingTarget：扩展缺失 → backup-swap；目标旧内容必须被替换成
// 新内容；.part 与 .bak 全部消失；末帧强制送达且 PartPath 指向**已提交的目标**。
func TestGoBackendPutOverwritesExistingTarget(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	const target = "dst.bin"
	writeRemote(t, remoteRoot, target, []byte("OLD-CONTENT"))
	data := bytes.Repeat([]byte("sshore-task8-"), 4096) // 52 KiB：跨多个 maxPacket
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	jdir := t.TempDir()
	g.SetJournalDir(jdir)
	sink := &progressSink{}
	req := TransferRequest{ID: "t8", Host: "h", Remote: target, Local: local, Atomic: true}
	if err := g.Put(req, sink.report); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(remoteRoot, target))
	if err != nil {
		t.Fatalf("提交后目标必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("目标内容与源不一致: got %d bytes want %d", len(got), len(data))
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("成功后不得残留 .part/.bak: %v", temps)
	}
	// 成功提交后 journal 必须清空（Begin→Done），且不留磁盘文件。
	if got := g.journal.Recover(); len(got) != 0 {
		t.Fatalf("成功提交后 journal 不应有未完成项: %v", got)
	}
	if _, err := os.Stat(filepath.Join(jdir, "swap-entries.json")); !os.IsNotExist(err) {
		t.Fatalf("全部提交完成后 journal 文件应被清掉，stat err=%v", err)
	}

	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("必须有进度帧")
	}
	first := frames[0]
	if first.Done != 0 || first.Total != int64(len(data)) || first.Direction != DirUpload {
		t.Fatalf("首帧字段不对: %+v", first)
	}
	if first.PartPath == target || !IsInternalTemp(filepath.Base(first.PartPath)) {
		t.Fatalf("传输中的 PartPath 应是未提交的 .part 路径（不是目标名），got %q", first.PartPath)
	}
	last := frames[len(frames)-1]
	if last.Done != int64(len(data)) || last.Total != int64(len(data)) {
		t.Fatalf("末帧必须是完整终值，got %d/%d", last.Done, last.Total)
	}
	if last.ID != "t8" || last.Host != "h" || last.Direction != DirUpload ||
		last.Name != target || last.Phase != PhaseTransfer {
		t.Fatalf("末帧字段不完整: %+v", last)
	}
	// 约束 6：末帧 PartPath 必须是**已提交的最终目标**，且该路径真实存在。
	if last.PartPath != target {
		t.Fatalf("末帧 PartPath 应为已提交的目标 %q，got %q", target, last.PartPath)
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, last.PartPath)); err != nil {
		t.Fatalf("末帧 PartPath 必须真实存在: %v", err)
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].Done < frames[i-1].Done {
			t.Fatalf("进度必须单调不减: 第 %d 帧 %d < 上一帧 %d", i, frames[i].Done, frames[i-1].Done)
		}
	}
}

// TestGoBackendPutCreatesNewTargetWhenExtensionMissing：目标原本不存在时 backup-swap 的
// Rename(target,bak) 会 ENOENT，此时必须退回直接提交（绝不把 ENOENT 当失败）。
func TestGoBackendPutCreatesNewTargetWhenExtensionMissing(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("new-file-"), 1000)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "new", Host: "h", Remote: "fresh.bin", Local: local, Atomic: true}
	if err := g.Put(req, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(remoteRoot, "fresh.bin"))
	if err != nil {
		t.Fatalf("目标必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("内容不一致")
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("不得残留临时文件: %v", temps)
	}
}

// TestGoBackendPutShortUploadNeverCommitsKeepsPart：done != total 是唯一提交前置 ——
// 必须报错、**绝不**改写旧目标、远端 .part 保留（Task 11 续传锚点）。
func TestGoBackendPutShortUploadNeverCommitsKeepsPart(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	const target = "dst.bin"
	const old = "OLD-CONTENT"
	writeRemote(t, remoteRoot, target, []byte(old))
	data := bytes.Repeat([]byte("x"), 100)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	// 注入「只搬 40 字节后正常 EOF」的搬运实现：库的 ReadFrom 对 EOF 返回 (n,nil)，
	// 这正是 R13 描述的危险形状，必须由 done!=total 兜住。
	swapCopyStream(t, func(dst io.Writer, src io.Reader) (int64, error) {
		return io.Copy(dst, io.LimitReader(src, 40))
	})

	var te *TransferError
	err := g.Put(TransferRequest{ID: "short", Host: "h", Remote: target, Local: local, Atomic: true}, nil)
	if err == nil {
		t.Fatal("少传必须报错，绝不能把不完整文件提交成成功")
	}
	if !errors.As(err, &te) {
		t.Fatalf("错误应为 *TransferError, got %T: %v", err, err)
	}
	// 旧目标保持原样（不提交）。
	if b, rerr := os.ReadFile(filepath.Join(remoteRoot, target)); rerr != nil || string(b) != old {
		t.Fatalf("失败路径不得改写目标: err=%v content=%q", rerr, string(b))
	}
	// .part 保留，内容是真实已落盘的 40 字节前缀。
	if te.PartPath == "" || !IsInternalTemp(filepath.Base(te.PartPath)) {
		t.Fatalf("失败时 PartPath 必须指向保留的远端 .part，got %q", te.PartPath)
	}
	pb, perr := os.ReadFile(filepath.Join(remoteRoot, te.PartPath))
	if perr != nil {
		t.Fatalf("PartPath 指向的 .part 必须真实存在: %v", perr)
	}
	if len(pb) != 40 {
		t.Fatalf(".part 应保留已写前缀 40 字节，got %d", len(pb))
	}
	// 只剩这一个 .part，没有 .bak。
	if temps := remoteTemps(t, remoteRoot); len(temps) != 1 || temps[0] != filepath.Base(te.PartPath) {
		t.Fatalf("失败后应恰好保留一个 .part，got %v", temps)
	}
}

// TestGoBackendPutLocalReadErrorNotMaskedByStderr（约束 6，Task 7 残留）：
// 本地源读失败（这里用目录触发 EISDIR）绝不能被非空远端 stderr 盖掉。
func TestGoBackendPutLocalReadErrorNotMaskedByStderr(t *testing.T) {
	remoteRoot := t.TempDir()
	local := t.TempDir() // 目录：Stat/Open 都成功，Read 必然 EISDIR

	g := backendForTestServer(t, remoteRoot)
	const noisy = "put-noise-should-not-mask-local-error"
	pp := noisyProcForDial(t, g, "sh", "-c", "printf 'put-noise-should-not-mask-local-error\n' >&2; cat >/dev/null")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(pp.StderrText(), noisy) {
		if time.Now().After(deadline) {
			t.Fatal("5s 内未收到注入的 stderr（用例无法构成错误归属复现条件）")
		}
		time.Sleep(10 * time.Millisecond)
	}

	err := g.Put(TransferRequest{ID: "local", Host: "h", Remote: "dst.bin", Local: local, Atomic: true}, nil)
	if err == nil {
		t.Fatal("本地源读失败必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("错误应为 *TransferError, got %T: %v", err, err)
	}
	if te.RemoteMsg != "" {
		t.Fatalf("本地读失败不得附 RemoteMsg（会盖掉真正的本地原因），got %q", te.RemoteMsg)
	}
	// .part 已真实建立（远端 OpenFile 先成功），按契约保留并如实上报。
	if te.PartPath == "" {
		t.Fatalf("远端 .part 已建立时必须给出 PartPath")
	}
	if _, perr := os.Stat(filepath.Join(remoteRoot, te.PartPath)); perr != nil {
		t.Fatalf("PartPath 必须真实存在: %v", perr)
	}
	msg := te.Error()
	if strings.Contains(msg, noisy) {
		t.Fatalf("本地错误被远端 stderr 盖掉: %s", msg)
	}
	if !strings.Contains(msg, local) {
		t.Fatalf("本地错误应指向本地源路径 %q，got: %s", local, msg)
	}
}

// TestGoBackendPutLocalStatErrorIsLocal：本地源不存在时在建会话之前失败，且是本地错误形状。
func TestGoBackendPutLocalStatErrorIsLocal(t *testing.T) {
	g := backendForTestServer(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope.bin")
	err := g.Put(TransferRequest{ID: "s", Host: "h", Remote: "dst.bin", Local: missing, Atomic: true}, nil)
	if err == nil {
		t.Fatal("本地源不存在必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("错误应为 *TransferError, got %T", err)
	}
	if te.Path != missing {
		t.Fatalf("错误应指向本地源 %q，got %q", missing, te.Path)
	}
	if te.RemoteMsg != "" || te.PartPath != "" {
		t.Fatalf("本地 Stat 失败不得带 RemoteMsg/PartPath: %+v", te)
	}
}

// TestGoBackendPutOpenPartErrorGivesEmptyPartPath（约束 5）：远端 .part 建不出来时
// PartPath 必须为空 —— 绝不发布一个没创建成功的假锚点。
func TestGoBackendPutOpenPartErrorGivesEmptyPartPath(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	// blocker 是普通文件：PartNameRemote("blocker/dst.bin") 的父目录不是目录 ⇒ OpenFile 失败。
	if err := os.WriteFile(filepath.Join(remoteRoot, "blocker"), []byte("not-a-dir"), 0600); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)
	var te *TransferError
	err := g.Put(TransferRequest{ID: "o", Host: "h", Remote: "blocker/dst.bin", Local: local, Atomic: true}, nil)
	if err == nil {
		t.Fatal("远端 .part 无法创建必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("错误应为 *TransferError, got %T", err)
	}
	if te.PartPath != "" {
		t.Fatalf(".part 未创建时 PartPath 必须为空，got %q", te.PartPath)
	}
	if b, rerr := os.ReadFile(filepath.Join(remoteRoot, "blocker")); rerr != nil || string(b) != "not-a-dir" {
		t.Fatalf("失败不得改动无关文件: err=%v content=%q", rerr, string(b))
	}
}

// TestGoBackendPutLegacyNonAtomicWritesDirectly：Atomic=false 是 legacy 面
// （internal/sync / app.SftpPut）：直写目标、不建我方 .part、不登记 journal。
func TestGoBackendPutLegacyNonAtomicWritesDirectly(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("legacy-up-"), 100)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)
	g.SetJournalDir(t.TempDir())
	sink := &progressSink{}
	req := TransferRequest{ID: "legacy-up", Host: "h", Remote: "legacy.bin", Local: local}
	if err := g.Put(req, sink.report); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(remoteRoot, "legacy.bin"))
	if err != nil {
		t.Fatalf("legacy 面必须直写目标: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("内容不一致")
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("legacy 面不得产生我方临时文件: %v", temps)
	}
	if got := g.journal.Recover(); len(got) != 0 {
		t.Fatalf("legacy 面不得登记 journal: %v", got)
	}
	frames := sink.all()
	if last := frames[len(frames)-1]; last.PartPath != req.Remote {
		t.Fatalf("legacy 末帧 PartPath 应指向直写目标 %q，got %q", req.Remote, last.PartPath)
	}
}

// TestCommitRemoteUsesPosixRenameWhenAvailable：扩展可用且成功 → 直接原子覆盖，
// 不走 backup-swap（没有 .bak、journal 为空）。
func TestCommitRemoteUsesPosixRenameWhenAvailable(t *testing.T) {
	remoteRoot := t.TempDir()
	const target = "dst.bin"
	part := PartNameRemote(target, "t1")
	writeRemote(t, remoteRoot, target, []byte("OLD"))
	writeRemote(t, remoteRoot, part, []byte("NEW"))

	g := backendForTestServer(t, remoteRoot)
	s := acquireTransferSession(t, g)
	calls := 0
	swapPosixRename(t, func(c *sftp.Client, oldname, newname string) error {
		calls++
		return c.Rename(oldname, newname) // 模拟服务端原子覆盖
	})
	j := newSwapJournal(t.TempDir())
	if err := commitRemote(s, true, part, target, j); err != nil {
		t.Fatalf("commitRemote: %v", err)
	}
	if calls != 1 {
		t.Fatalf("扩展可用时必须走 PosixRename，calls=%d", calls)
	}
	if b, _ := os.ReadFile(filepath.Join(remoteRoot, target)); string(b) != "NEW" {
		t.Fatalf("目标应为新内容，got %q", string(b))
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, part)); !os.IsNotExist(err) {
		t.Fatalf(".part 提交后必须消失，stat err=%v", err)
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("posix 路径不得产生 .bak: %v", temps)
	}
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("posix 路径不应登记 journal: %v", got)
	}
}

// TestCommitRemoteFallsBackToSwapWhenPosixRenameFails（约束 3）：即使 HasExtension 为真，
// PosixRename 返回任何非 nil 都必须回退 backup-swap，绝不把错误当结论。
func TestCommitRemoteFallsBackToSwapWhenPosixRenameFails(t *testing.T) {
	remoteRoot := t.TempDir()
	const target = "dst.bin"
	part := PartNameRemote(target, "t1")
	writeRemote(t, remoteRoot, target, []byte("OLD"))
	writeRemote(t, remoteRoot, part, []byte("NEW"))

	g := backendForTestServer(t, remoteRoot)
	s := acquireTransferSession(t, g)
	swapPosixRename(t, func(*sftp.Client, string, string) error {
		return errors.New("posix-rename unsupported on this server")
	})
	j := newSwapJournal(t.TempDir())
	if err := commitRemote(s, true, part, target, j); err != nil {
		t.Fatalf("PosixRename 失败必须回退 backup-swap 并成功，got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(remoteRoot, target)); string(b) != "NEW" {
		t.Fatalf("回退后目标应为新内容，got %q", string(b))
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, part)); !os.IsNotExist(err) {
		t.Fatalf(".part 提交后必须消失，stat err=%v", err)
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("回退成功后不得残留 .bak: %v", temps)
	}
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("回退成功后 journal 必须清空: %v", got)
	}
}

// TestCommitRemoteSwapFailureLeavesJournalEntryForRecovery：backup-swap 中途失败
// （这里让 part 不存在，Begin 之后的第二次 rename 必失败）必须回滚目标、保留 journal 条目，
// 且**新实例**能从磁盘恢复出这个目标（崩溃恢复契约）。
func TestCommitRemoteSwapFailureLeavesJournalEntryForRecovery(t *testing.T) {
	remoteRoot := t.TempDir()
	const target = "dst.bin"
	missingPart := "dst.bin.sshore-sftppart-t1-dead"
	writeRemote(t, remoteRoot, target, []byte("OLD"))
	// 注意：missingPart 故意不存在。

	g := backendForTestServer(t, remoteRoot)
	s := acquireTransferSession(t, g)
	jdir := t.TempDir()
	if err := commitRemote(s, false, missingPart, target, newSwapJournal(jdir)); err == nil {
		t.Fatal("part 不存在时 backup-swap 必须失败并上报")
	}
	// 目标必须被回滚保住（绝不先删目标后丢失目标）。
	if b, rerr := os.ReadFile(filepath.Join(remoteRoot, target)); rerr != nil || string(b) != "OLD" {
		t.Fatalf("失败后目标必须回滚/仍存在: err=%v content=%q", rerr, string(b))
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("回滚后不得残留 .bak: %v", temps)
	}
	// 崩溃恢复：新实例读磁盘 journal，必须报出这个未完成目标。
	if got := newSwapJournal(jdir).Recover(); len(got) != 1 || got[0] != target {
		t.Fatalf("新实例应从未完成 journal 恢复出目标 %q，got %v", target, got)
	}
}

// TestCommitRemoteTargetMissingCommitsDirectly：目标原本不存在（Rename(target,bak) ENOENT）
// 时直接提交，不登记 journal、不留 .bak。
func TestCommitRemoteTargetMissingCommitsDirectly(t *testing.T) {
	remoteRoot := t.TempDir()
	const target = "fresh.bin"
	part := PartNameRemote(target, "t1")
	writeRemote(t, remoteRoot, part, []byte("NEW"))

	g := backendForTestServer(t, remoteRoot)
	s := acquireTransferSession(t, g)
	j := newSwapJournal(t.TempDir())
	if err := commitRemote(s, false, part, target, j); err != nil {
		t.Fatalf("目标不存在时应直接提交: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(remoteRoot, target)); string(b) != "NEW" {
		t.Fatalf("目标内容不对: %q", string(b))
	}
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("直接提交不得登记 journal: %v", got)
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("直接提交不得残留 .bak: %v", temps)
	}
}

// TestGoBackendSetJournalDir：空目录表示不做崩溃恢复（journal=nil），非空则建实例。
func TestGoBackendSetJournalDir(t *testing.T) {
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	g.SetJournalDir(t.TempDir())
	if g.journal == nil {
		t.Fatal("非空目录必须建立 journal 实例")
	}
	g.SetJournalDir("")
	if g.journal != nil {
		t.Fatal("空目录必须把 journal 置 nil（不做崩溃恢复）")
	}
}

// TestCtrlSetJournalDirReachesGoBackend：门面装配（app.go 的 stateDir()）必须在
// GoBackend 懒构造前后都能把 journal 目录送到 Put 用的实例上。
func TestCtrlSetJournalDirReachesGoBackend(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "gosftp")
	dir := t.TempDir()

	// 构造后注入。
	c := NewCtrlWith(osutil.NewRunner(), nil, nil)
	gb, ok := c.backend().(*GoBackend)
	if !ok {
		t.Fatalf("gosftp 选择器应解析出 *GoBackend, got %T", c.backend())
	}
	if gb.journal != nil {
		t.Fatal("未设置目录前 journal 应为 nil")
	}
	c.SetJournalDir(dir)
	if gb.journal == nil {
		t.Fatal("构造后 SetJournalDir 必须送达已存在的 GoBackend")
	}
	c.CloseAll()

	// 先注入、后懒构造。
	c2 := NewCtrlWith(osutil.NewRunner(), nil, nil)
	c2.SetJournalDir(dir)
	gb2, ok := c2.backend().(*GoBackend)
	if !ok {
		t.Fatal("懒构造应产出 *GoBackend")
	}
	if gb2.journal == nil {
		t.Fatal("懒构造时必须带上此前注入的 journal 目录")
	}
	c2.CloseAll()
}

// TestCountingWriterTagsLocalIOError（约束 6）：本地写失败必须带 errLocalIO 标记，
// 否则下载路径会把本地 ENOSPC 当成远端噪音（Task 7 残留）。
func TestCountingWriterTagsLocalIOError(t *testing.T) {
	boom := errors.New("no space left on device")
	cw := &countingWriter{f: failWriter{err: boom}, e: newProgressEmitter("x", nil), p: Progress{}}
	if _, err := cw.Write([]byte("abc")); !errors.Is(err, errLocalIO) || !errors.Is(err, boom) {
		t.Fatalf("本地写错误必须包 errLocalIO 且保留原错误，got %v", err)
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }
