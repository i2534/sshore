package sftp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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

	// —— Task 7：下载往返（两个后端各验一次）——
	// 通过门面 TransferGet 走**当前选中的后端**，因此双后端循环的两次迭代各验一条真实下载路径：
	//   batch 迭代：BatchBackend.TransferGet（Atomic=false，直写）
	//   gosftp 迭代：GoBackend.Get（Atomic=true，.part + 提交）
	// 判据是同一批：sha256 相等 + 不留临时文件；Atomic 分支另外断言没有 .part 残留。
	// 「目标名在传输中不存在 / .part 在传输中存在」属于时序窗口，改由 hermetic 用例
	// （copy_download_test.go，net.Pipe 真客户端+真服务端）确定性钉住。
	be := resolveTransport(nil)
	dldir := t.TempDir()
	data := bytes.Repeat([]byte("sshore-task7-e2e-"), 40960) // 680 KiB：跨多个 maxPacket（32KiB）分块写
	backendTag := map[BackendKind]string{KindBatch: "batch", KindGo: "gosftp"}[be]
	localName := fmt.Sprintf("t7-%s.bin", backendTag)
	srcName := fmt.Sprintf("t7-src-%s.bin", backendTag)
	if err := os.WriteFile(filepath.Join(remote, srcName), data, 0600); err != nil {
		t.Fatalf("写远端测试文件: %v", err)
	}
	ctrl := NewCtrl(osutil.NewRunner(), nil)
	defer ctrl.CloseAll()
	implName := map[BackendKind]string{KindBatch: "BatchBackend.TransferGet", KindGo: "GoBackend.Get"}[be]
	req := TransferRequest{
		ID:     "t7-" + localName,
		Host:   host,
		Remote: filepath.Join(remote, srcName),
		Local:  filepath.Join(dldir, localName),
		Atomic: be == KindGo,
	}
	t.Logf("下载实现 = %s（backend=%v, Atomic=%v）", implName, be, req.Atomic)
	if err := ctrl.TransferGet(req, nil); err != nil {
		t.Fatalf("%s 下载失败: %v", implName, err)
	}
	got, err := os.ReadFile(req.Local)
	if err != nil {
		t.Fatalf("下载后目标必须存在: %v", err)
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256(data))
	gotHash := fmt.Sprintf("%x", sha256.Sum256(got))
	if gotHash != wantHash {
		t.Fatalf("下载内容 sha256 不一致: got %s want %s（%d/%d 字节）", gotHash, wantHash, len(got), len(data))
	}
	entries, err := os.ReadDir(dldir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if IsInternalTemp(e.Name()) {
			t.Fatalf("下载成功后不得残留临时文件: %s", e.Name())
		}
	}
	if req.Atomic && len(entries) != 1 {
		t.Fatalf("gosftp 原子下载后目录里应只有目标文件: %v", entries)
	}
	t.Logf("下载完成: %d 字节 sha256=%s", len(got), gotHash)
	// —— Task 8：上传往返（两个后端各验一次）——
	// 通过门面 TransferPut 走**当前选中的后端**：
	//   batch 迭代：BatchBackend.TransferPut（Atomic=false，sftp put 直写）
	//   gosftp 迭代：GoBackend.Put（Atomic=true，.part + 提交；本机 OpenSSH 支持
	//                posix-rename ⇒ 真实走 PosixRename 覆盖已存在目标）
	// 判据：字节 sha256 相等 + 旧内容被真正替换 + 远端目录不残留 .part/.bak。
	// （「传输中目标名不存在 / .part 存在」是时序窗口，由 hermetic 用例
	//   upload_test.go 的 net.Pipe 真客户端+真服务端确定性钉住。）
	upData := bytes.Repeat([]byte("sshore-task8-e2e-"), 40960) // 680 KiB：跨多个 maxPacket（32KiB）
	upSrc := filepath.Join(dldir, fmt.Sprintf("t8-src-%s.bin", backendTag))
	if err := os.WriteFile(upSrc, upData, 0600); err != nil {
		t.Fatalf("写本地上传源: %v", err)
	}
	dstName := fmt.Sprintf("t8-dst-%s.bin", backendTag)
	dstRemote := filepath.Join(remote, dstName)
	const oldContent = "OLD-CONTENT-MUST-BE-OVERWRITTEN"
	if err := os.WriteFile(dstRemote, []byte(oldContent), 0600); err != nil {
		t.Fatalf("预置远端旧目标: %v", err)
	}
	upReq := TransferRequest{
		ID:     "t8-" + dstName,
		Host:   host,
		Remote: dstRemote,
		Local:  upSrc,
		Atomic: be == KindGo,
	}
	implPut := map[BackendKind]string{KindBatch: "BatchBackend.TransferPut", KindGo: "GoBackend.Put"}[be]
	t.Logf("上传实现 = %s（backend=%v, Atomic=%v）", implPut, be, upReq.Atomic)
	if err := ctrl.TransferPut(upReq, nil); err != nil {
		t.Fatalf("%s 上传失败: %v", implPut, err)
	}
	gotUp, err := os.ReadFile(dstRemote)
	if err != nil {
		t.Fatalf("上传后远端目标必须存在: %v", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(gotUp)) != fmt.Sprintf("%x", sha256.Sum256(upData)) {
		t.Fatalf("上传内容 sha256 不一致: got %d bytes want %d", len(gotUp), len(upData))
	}
	if string(gotUp) == oldContent {
		t.Fatal("覆盖写没生效：远端仍是旧内容")
	}
	rents, err := os.ReadDir(remote)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rents {
		if IsInternalTemp(e.Name()) {
			t.Fatalf("上传成功后远端不得残留 .part/.bak: %s", e.Name())
		}
	}
	t.Logf("上传完成: %d 字节 sha256=%x", len(gotUp), sha256.Sum256(gotUp))
}

// runAndCancelInFlight 是取消用例的确定性驱动：start 在后台发起一次真实传输，等**首帧
// 进度**到达（会话与 .part 已就绪）后卡住传输 goroutine，再调 cancel(id)，最后 close(hold)
// 放行并等操作返回。返回 cancel 的返回值与操作错误。绝不 sleep —— 卡住传输才保证取消时它
// 确实在飞（自审 S9），32MB 的完成时间与之无关。
func runAndCancelInFlight(t *testing.T, start func(report func(Progress)) error, id string, cancel func(string) bool) (bool, error) {
	t.Helper()
	started := make(chan struct{})
	hold := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- start(func(Progress) {
			once.Do(func() {
				close(started)
				<-hold // 卡住传输 goroutine：Cancel 期间传输必须确实在飞
			})
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("传输未在 10s 内开始（首帧进度未到）")
	}
	ok := cancel(id)
	close(hold)
	select {
	case err := <-done:
		return ok, err
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 15s 内操作未返回")
		return ok, nil
	}
}

// assertCancelledDownload 断言取消后的四个后果：操作必须报错、最终名不存在、恰好保留一个
// .part（续传锚点）、第一次取消 true。幂等由调用方补验第二次 false。
func assertCancelledDownload(t *testing.T, dst string, ok bool, err error) {
	t.Helper()
	if !ok {
		t.Fatal("取消在飞传输必须返回 true（会话被关）")
	}
	if err == nil {
		t.Fatal("被取消的传输必须返回错误")
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatalf("取消后最终文件名不得存在，stat err=%v", serr)
	}
	parts, _ := filepath.Glob(filepath.Dir(dst) + "/*" + PartMarker + "*")
	if len(parts) != 1 {
		t.Fatalf("取消后必须保留恰好一个 .part 供续传，got %v", parts)
	}
}

// TestCancelWholeBatchE2E 是 Task 10 的端到端取消用例。取消 = 关掉该传输独占的会话
// （库无逐请求取消）：必须让在飞操作失败、不提交（最终名不存在）、保留 .part 作续传锚点，
// 且第二次取消返回 false（幂等）。需要 harness 预置的 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE；
// 缺环境即 skip（harness 把任何 SKIP 当失败，防假绿）。
//
// 两条腿覆盖取消路径的两端：
//   - 腿 1（两个后端迭代都跑）：GoBackend 直连 Cancel，钉住注册表 + 关会话 + 诚实返回值；
//   - 腿 2：gosftp 迭代走门面 Ctrl.TransferGet/Ctrl.Cancel（绑定 → 门面 → GoBackend 的完整
//     链路）；batch 迭代没有长驻会话，断言 Ctrl.Cancel 诚实返回 false（不假成功）。
func TestCancelWholeBatchE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_*，跳过取消验证")
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	// 自造 32MB 源文件并上传（不依赖脚本预置）。
	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 32<<20), 0600); err != nil {
		t.Fatal(err)
	}
	src := remote + "/cancel-src.bin"
	if err := g.Put(TransferRequest{ID: "t-seed", Host: host, Local: big, Remote: src, Atomic: true}, nil); err != nil {
		t.Fatalf("种子上传失败: %v", err)
	}
	be := resolveTransport(nil)

	// 腿 1：GoBackend 直连取消。
	dst := filepath.Join(t.TempDir(), "cancel-dst.bin")
	ok, err := runAndCancelInFlight(t, func(report func(Progress)) error {
		return g.Get(TransferRequest{ID: "t-cancel", Host: host, Remote: src, Local: dst, Atomic: true}, report)
	}, "t-cancel", g.Cancel)
	assertCancelledDownload(t, dst, ok, err)
	if g.Cancel("t-cancel") {
		t.Fatal("取消必须幂等：已结束的 id 返回 false")
	}

	// 腿 2：门面链路 / batch 诚实性。
	ctrl := NewCtrl(osutil.NewRunner(), nil)
	defer ctrl.CloseAll()
	if be == KindBatch {
		if ctrl.Cancel("t-cancel") {
			t.Fatal("batch 后端无长驻会话，Cancel 必须诚实返回 false")
		}
	} else {
		dst2 := filepath.Join(t.TempDir(), "cancel-dst-ctrl.bin")
		ok2, err2 := runAndCancelInFlight(t, func(report func(Progress)) error {
			return ctrl.TransferGet(TransferRequest{ID: "t-cancel-ctrl", Host: host, Remote: src, Local: dst2, Atomic: true}, report)
		}, "t-cancel-ctrl", ctrl.Cancel)
		assertCancelledDownload(t, dst2, ok2, err2)
		if ctrl.Cancel("t-cancel-ctrl") {
			t.Fatal("门面取消同样必须幂等：已结束的 id 返回 false")
		}
	}
	// 日志不得夸大 batch 腿：batch 迭代没有长驻会话可关，门面腿只断言 Ctrl.Cancel 诚实
	// 返回 false（M4/M5）。只有 gosftp 迭代才真正验了「门面取消关会话」。
	if be == KindBatch {
		t.Logf("取消验证完成（后端=batch）：直连腿关会话/未提交/.part 保留/二次取消 false；" +
			"门面腿仅断言 batch 无长驻会话、Ctrl.Cancel 诚实 false（未关任何会话）")
	} else {
		t.Logf("取消验证完成（后端=%v）：直连+门面均真关会话、未提交、.part 保留、二次取消 false", be)
	}
}

// e2eSHA256File 读文件算 sha256（十六进制），e2e 续传用例的内容判据。
func e2eSHA256File(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// cancelPartialDownloadE2E 用 blockAfterFirstChunk 确定性造出一份「部分下载」：
// 首块（32 KiB）落盘后卡住 → Cancel 关会话 → 传输报错、最终名不存在、恰好一个 .part。
// 返回该 .part 路径。blockAfterFirstChunk 的 release 在 t.Cleanup 兜底，用例中途失败也不会悬挂。
func cancelPartialDownloadE2E(t *testing.T, g *GoBackend, host, remote, local, id string) string {
	t.Helper()
	entered, release := blockAfterFirstChunk(t)
	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: id, Host: host, Remote: remote, Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("e2e：15s 内未写出首块（会话/协议卡死）")
	}
	if !g.Cancel(id) {
		t.Fatal("e2e：取消在飞下载必须返回 true")
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("e2e：被取消的下载必须报错")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("e2e：取消后 15s 内 Get 未返回")
	}
	if _, serr := os.Stat(local); !os.IsNotExist(serr) {
		t.Fatalf("e2e：取消后最终名不得存在，stat err=%v", serr)
	}
	parts, _ := filepath.Glob(filepath.Dir(local) + "/*" + PartMarker + "*")
	if len(parts) != 1 {
		t.Fatalf("e2e：取消后应恰好保留一个 .part，got %v", parts)
	}
	return parts[0]
}

// e2eSetRemoteMtime 通过 SFTP 把远端源的 mtime 设成确定值。时间戳是续传指纹的一部分，
// 靠真实写入的秒级时间不够可控（同一秒内改写给不出「不同 mtime」），必须显式设置。
func e2eSetRemoteMtime(t *testing.T, g *GoBackend, host, remotePath string, mt time.Time) {
	t.Helper()
	s, err := g.pool.AcquireList(context.Background(), host, "")
	if err != nil {
		t.Fatalf("e2e AcquireList: %v", err)
	}
	defer g.pool.Release(s, true)
	if err := s.Conn.Chtimes(remotePath, mt, mt); err != nil {
		t.Fatalf("e2e Chtimes(%s): %v", remotePath, err)
	}
}

// TestResumeE2E 是 Task 11 的端到端续传用例（harness 的 batch/gosftp 双迭代都会跑）：
//  1. 32MB 往返：取消到中途 → .part 是源前缀 → 用同一 PartPath 续传 → sha256 等于一次性完整下载；
//  2. 同尺寸改写（技术审核 S10）：改前留下的旧 .part 在改后被续传请求撞上 → 必须整份重传，
//     结果等于「用新源完整下载」，绝不是旧前缀 + 新内容拼接；
//  3. 同目标并发去重：第二条同目标传输必须被拒（且第一条完成后同一目标可再传）。
//
// batch 迭代没有 .part/续传语义：额外断言它诚实拒绝 Atomic=true（guardAtomic），绝不用
// 「batch 也跑绿」冒充「batch 支持续传」。
func TestResumeE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_*，跳过续传验证")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("缺少 ssh 二进制")
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	be := resolveTransport(nil)

	// 造 4 MiB 源并上传（首块 32 KiB ⇒ 可控的部分 .part）。
	payload := bytes.Repeat([]byte("sshore-t11-"), 384<<10) // 4 MiB
	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, payload, 0600); err != nil {
		t.Fatal(err)
	}
	src := remote + "/resume-src.bin"
	if err := g.Put(TransferRequest{ID: "t11-seed", Host: host, Local: big, Remote: src, Atomic: true}, nil); err != nil {
		t.Fatalf("种子上传失败: %v", err)
	}
	// 把远端源 mtime 钉在确定的「旧」时间，作为之后续传指纹的基准。
	oldMT := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	e2eSetRemoteMtime(t, g, host, src, oldMT)
	if be == KindGo {
		t.Logf("gosftp 迭代：真实 .part 续传 + 同尺寸改写必须拒绝")
	}

	// 参考：一次性完整下载的 sha256。
	full := filepath.Join(t.TempDir(), "full.bin")
	if err := g.Get(TransferRequest{ID: "t11-full", Host: host, Remote: src, Local: full, Atomic: true}, nil); err != nil {
		t.Fatalf("完整下载失败: %v", err)
	}
	wantSHA := e2eSHA256File(t, full)

	// 1) 取消到中途，断言 .part 是源前缀。
	partDir := t.TempDir()
	part := filepath.Join(partDir, "part.bin")
	partFile := cancelPartialDownloadE2E(t, g, host, src, part, "t11-cut-a")
	cutBytes, err := os.ReadFile(partFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cutBytes) == 0 || len(cutBytes) >= len(payload) {
		t.Fatalf("e2e：需要“部分”的 .part，got %d/%d 字节", len(cutBytes), len(payload))
	}
	if !bytes.Equal(cutBytes, payload[:len(cutBytes)]) {
		t.Fatal("e2e：.part 必须是源的前缀 —— 拼错锚点会静默损坏文件")
	}

	// 续传并断言 sha256 == 一次性完整下载。
	if err := g.Get(TransferRequest{ID: "t11-resume-a", Host: host, Remote: src, Local: part, Atomic: true,
		Resume: true, PartPath: partFile}, nil); err != nil {
		t.Fatalf("续传失败: %v", err)
	}
	if got := e2eSHA256File(t, part); got != wantSHA {
		t.Fatalf("续传结果 sha256 不一致: got %s want %s", got, wantSHA)
	}
	t.Logf("续传成功: 断点 %d 字节，结果 sha256=%s", len(cutBytes), wantSHA)

	// 2) 同尺寸改写：先在改前留下一份旧内容 .part（partB），再改源并重传。
	partB := cancelPartialDownloadE2E(t, g, host, src, filepath.Join(t.TempDir(), "partB.bin"), "t11-cut-b")
	partBDir := filepath.Dir(partB)
	// 同尺寸改写：首字节换掉，其余不变，长度完全一致。
	rewritten := append([]byte("Z"), payload[1:]...)
	if err := os.WriteFile(big, rewritten, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(big, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{ID: "t11-seed2", Host: host, Local: big, Remote: src, Atomic: true}, nil); err != nil {
		t.Fatalf("改写后重传失败: %v", err)
	}
	// 远端 mtime 换成「新」时间：与 partB 记录下的 oldMT 必须不同。
	newMT := time.Now().Truncate(time.Second)
	e2eSetRemoteMtime(t, g, host, src, newMT)

	// 用旧 .part 发起续传：必须整份重传（mtime 指纹不符），结果 = 新源的完整下载。
	freshA := filepath.Join(partBDir, "freshA.bin")
	if err := g.Get(TransferRequest{ID: "t11-resume-b", Host: host, Remote: src, Local: freshA, Atomic: true,
		Resume: true, PartPath: partB}, nil); err != nil {
		t.Fatalf("同尺寸改写后的续传请求必须成功（整份重传）: %v", err)
	}
	freshB := filepath.Join(t.TempDir(), "freshB.bin")
	if err := g.Get(TransferRequest{ID: "t11-fresh", Host: host, Remote: src, Local: freshB, Atomic: true}, nil); err != nil {
		t.Fatalf("新源完整下载失败: %v", err)
	}
	gotA, gotB := e2eSHA256File(t, freshA), e2eSHA256File(t, freshB)
	if gotA != gotB {
		t.Fatalf("同尺寸改写后必须整份重传（而非旧前缀+新内容拼接）: got %s want %s", gotA, gotB)
	}
	if gotA == wantSHA {
		t.Fatal("e2e 自检失败：源已改写，新结果不可能等于旧参考 sha256")
	}
	if _, serr := os.Stat(partB); !os.IsNotExist(serr) {
		t.Fatalf("e2e：不可续的旧 .part 必须被清掉，stat err=%v", serr)
	}
	t.Logf("同尺寸改写被正确拒绝续传: 新 sha256=%s（旧 %s）", gotA, wantSHA)

	// 3) 同目标并发去重：首帧进度回调里卡住第一条传输，第二条同目标必须立刻被拒。
	dupLocal := filepath.Join(t.TempDir(), "dup.bin")
	started := make(chan struct{})
	hold := make(chan struct{})
	var once sync.Once
	doneDup := make(chan error, 1)
	go func() {
		doneDup <- g.Get(TransferRequest{ID: "t11-dup1", Host: host, Remote: src, Local: dupLocal, Atomic: true},
			func(Progress) {
				once.Do(func() {
					close(started)
					<-hold
				})
			})
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("e2e：15s 内未收到首帧进度（去重用例无法确定在飞）")
	}
	errDup := g.Get(TransferRequest{ID: "t11-dup2", Host: host, Remote: src, Local: dupLocal, Atomic: true}, nil)
	if errDup == nil {
		t.Fatal("e2e：同一目标并发传输必须被拒绝（inflight 去重）")
	}
	close(hold)
	select {
	case err := <-doneDup:
		if err != nil {
			t.Fatalf("e2e：未被去重拒绝的那条传输必须正常完成: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("e2e：去重用例的首条传输 30s 内未完成")
	}
	if err := g.Get(TransferRequest{ID: "t11-dup3", Host: host, Remote: src, Local: dupLocal, Atomic: true}, nil); err != nil {
		t.Fatalf("e2e：去重释放后同一目标必须能再传: %v", err)
	}

	// batch 迭代诚实性：batch 无 .part/续传语义，Atomic=true 必须被 guardAtomic 硬拒。
	if be == KindBatch {
		b := NewBatchBackend(osutil.NewRunner(), nil)
		if b.AtomicCapable() {
			t.Fatal("e2e：batch 后端不得声明 AtomicCapable（无 .part/续传语义）")
		}
		if err := b.TransferGet(TransferRequest{Host: host, Remote: src, Local: filepath.Join(t.TempDir(), "batch-x.bin"), Atomic: true}, nil); err == nil {
			t.Fatal("e2e：batch 后端 Atomic=true 必须被硬拒，绝不能静默降级直写")
		}
		t.Log("batch 迭代：已断言 batch 诚实拒绝 Atomic（续传仅 gosftp 提供，绝不用 batch 跑绿冒充）")
	}
}

// e2eEnv 读取 harness 注入的真实 sshd 环境；缺失时 skip 并返回 ok=false。
func e2eEnv(t *testing.T) (host, remote string, ok bool) {
	t.Helper()
	host = os.Getenv("SSHORE_E2E_HOST")
	remote = os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE，跳过目录传输验证")
		return "", "", false
	}
	return host, remote, true
}

// TestTreeE2E 是 Task 12 的目录往返 e2e（harness 的 batch/gosftp 双迭代都会真跑）：
//   - PutTree 上传本地树 a/b/c（batch = sftp put -r；gosftp = 逐文件 .part + 提交）；
//   - 第二次 PutTree 验证 D11 合并语义（并入而非嵌套出 a/a，远端独有文件保留）；
//   - GetTree 下载回新目录，逐文件逐字节比对；
//   - 成功后远端树内不得残留 .part/.bak。
//
// 走门面 Ctrl（按 SSHORE_SFTP_TRANSPORT 选中后端）而不是直连 GoBackend，这样双后端迭代
// 各验一条真实实现，杜绝「两个迭代跑同一段代码」的假绿。
func TestTreeE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("缺少 ssh 二进制")
	}
	be := resolveTransport(nil)
	impl := map[BackendKind]string{KindBatch: "BatchBackend.TransferGetTree/PutTree", KindGo: "GoBackend.GetTree/PutTree"}[be]
	ctrl := NewCtrl(osutil.NewRunner(), nil)
	defer ctrl.CloseAll()
	atomic := ctrl.AtomicCapable()
	t.Logf("目录传输实现 = %s（backend=%v, Atomic=%v）", impl, be, atomic)

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 空目录：递归传输只搬文件时最容易漏掉，两个后端都必须建出目录本身（D11）。
	if err := os.MkdirAll(filepath.Join(src, "a", "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "a/b/c/z.txt"} {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(p)), []byte("payload:"+p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(remote, "tree")
	// 目标父目录先建好：batch 的 sftp put -r 的合并语义要求 remoteDir 已存在（gosftp 会自行 MkdirAll）。
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(target) }()

	localRoot := filepath.Join(src, "a")
	put := func(id string) {
		t.Helper()
		if err := ctrl.TransferPutTree(TransferRequest{ID: id, Host: host, Remote: target, Local: localRoot, Atomic: atomic}, nil); err != nil {
			t.Fatalf("PutTree(%s): %v", id, err)
		}
	}
	put("t12-put1")
	// 远端独有文件：第二次并入不得删掉它（D11 不做整树替换）。
	stale := filepath.Join(target, "a", "keep.txt")
	if err := os.WriteFile(stale, []byte("KEEP"), 0o600); err != nil {
		t.Fatal(err)
	}
	put("t12-put2")
	if _, err := os.Stat(filepath.Join(target, "a", "a")); !os.IsNotExist(err) {
		t.Fatalf("put -r 必须并入而非嵌套（不应出现 target/a/a），stat err=%v", err)
	}
	if b, err := os.ReadFile(stale); err != nil || string(b) != "KEEP" {
		t.Fatalf("并入不得清理远端独有文件: err=%v content=%q", err, string(b))
	}
	if st, err := os.Stat(filepath.Join(target, "a", "emptydir")); err != nil || !st.IsDir() {
		t.Fatalf("递归上传必须建出空目录: err=%v st=%v", err, st)
	}

	dst := t.TempDir()
	if err := ctrl.TransferGetTree(TransferRequest{ID: "t12-get", Host: host, Remote: target, Local: filepath.Join(dst, "tree"), Atomic: atomic}, nil); err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "a/b/c/z.txt"} {
		want, _ := os.ReadFile(filepath.Join(src, filepath.FromSlash(p)))
		got, err := os.ReadFile(filepath.Join(dst, "tree", filepath.FromSlash(p)))
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("文件不一致: %s err=%v got=%d want=%d", p, err, len(got), len(want))
		}
	}
	if st, err := os.Stat(filepath.Join(dst, "tree", "a", "emptydir")); err != nil || !st.IsDir() {
		t.Fatalf("递归下载必须建出空目录: err=%v st=%v", err, st)
	}
	if err := filepath.WalkDir(target, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.IsDir() && IsInternalTemp(d.Name()) {
			return fmt.Errorf("远端残留内部临时文件: %s", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("目录往返完成：3 个文件 + 空目录 + 合并语义（backend=%v, Atomic=%v）", be, atomic)
}

// writeTemp 在临时目录写一个内容确定的小文件（能力用例的本地源）。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCapabilitiesE2E 是 Task 13 的能力迁移 e2e（ListMany/Mkdir/Rename 覆盖/RemoveRecursive）：
//   - Mkdir 真的建出远端目录；
//   - rename 覆盖已存在目标必须成功（保持旧 sftp rename 语义，GoBackend 走 PosixRename）；
//   - ListMany 缺失 key = 未知（绝不当空目录）；
//   - 不可读/不存在的目录 → RemoveRecursive 整体失败；
//   - RemoveRecursive 真的删掉整棵树（含空目录）。
func TestCapabilitiesE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	dir := remote + "/caps"
	_ = g.RemoveRecursive(host, "", dir) // 上次运行残留：忽略失败
	if err := g.Mkdir(host, "", dir); err != nil {
		t.Fatal(err)
	}
	// rename 覆盖已存在目标必须成功（保持旧 sftp rename 语义）。
	if err := g.Put(TransferRequest{ID: "caps-a", Host: host, Local: writeTemp(t, "a"), Remote: dir + "/a.txt", Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{ID: "caps-b", Host: host, Local: writeTemp(t, "b"), Remote: dir + "/b.txt", Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Rename(host, "", dir+"/a.txt", dir+"/b.txt"); err != nil {
		t.Fatalf("rename 覆盖已存在目标必须成功: %v", err)
	}
	items, err := g.List(host, "", dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]Item{}
	for _, it := range items {
		names[it.Name] = it
	}
	if _, present := names["a.txt"]; present {
		t.Fatalf("rename 后源必须消失: %#v", items)
	}
	if it, present := names["b.txt"]; !present || it.Size != 1 {
		t.Fatalf("rename 后目标必须是单字节新内容: %#v", items)
	}
	if it := names["b.txt"]; it.ModTime == "" || it.Mode == "" {
		t.Fatalf("List 返回的 Item 必须带 Mode/ModTime: %#v", it)
	}
	// ListMany 缺失 key = 未知（绝不当空目录）。
	res, err := g.ListMany(host, "", []string{dir, dir + "/does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := res[dir+"/does-not-exist"]; present {
		t.Fatal("列不出来的目录必须缺席于返回值")
	}
	if _, present := res[dir]; !present {
		t.Fatal("可列的目录必须在返回值里")
	}
	// 不可读目录 → RemoveRecursive 整体失败。
	if err := g.RemoveRecursive(host, "", dir+"/does-not-exist"); err == nil {
		t.Fatal("不存在的目录必须整体失败")
	}
	// 含空目录的整棵树：递归删除必须连空目录一起删干净。
	if err := g.Mkdir(host, "", dir+"/empty"); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveRecursive(host, "", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "caps")); !os.IsNotExist(err) {
		t.Fatalf("RemoveRecursive 后目录必须消失, stat err=%v", err)
	}
	t.Logf("能力验证完成：Mkdir/Rename 覆盖/ListMany 缺失 key/RemoveRecursive 整体失败与整树删除")
}

// TestNoLeftoverOnCloseE2E 是 Task 13 的退出清理 e2e：Put 成功后 CloseAll 必须
//  1. 让远端不残留任何内部临时文件（.part/.bak）；
//  2. 回收本次传输起出的 ssh 子进程（Unix 用 pgrep 反查命令行）。
//
// 进程检查只在有 pgrep 的 Unix 上做（Windows 见 Task 16 的 tasklist 断言）；parts 断言
// 在所有平台都跑。注意：pgrep 的 -s 是「按 session id 过滤」而不是搜索字符串，所以这里用
// -f 匹配完整命令行里的 "<host> sftp"，并用 -- 防止模式被当成选项。
func TestNoLeftoverOnCloseE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	g := NewGoBackend(nil, nil)
	src := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(src, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{ID: "p1", Host: host, Local: src, Remote: remote + "/small.bin", Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	// 退出前先确认这条会话确实起出了 ssh 子进程（否则「无残留」的断言没有意义）。
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("pgrep"); err == nil && !hasLiveSftpChild(host) {
			t.Fatalf("CloseAll 之前必须能看到 ssh 子进程（否则用例无法证明清理生效）")
		}
	}
	g.CloseAll()

	// 1) 不得残留 ssh 子进程。**必须先查**：下面的 List 会重新 dial 出一条新会话，
	//    那时 pgrep 看到的是新进程，断言就失去意义（实测踩过这个坑）。
	if hasLiveSftpChild(host) {
		out, _ := exec.Command("pgrep", "-fa", "--", host+" sftp").CombinedOutput()
		t.Fatalf("CloseAll 后仍有 ssh 子进程: %s", out)
	}
	// 2) 远端不得残留内部临时文件。
	if items, err := g.List(host, "", remote); err == nil {
		for _, it := range items {
			if strings.Contains(it.Name, PartMarker) {
				t.Fatalf("CloseAll 后仍有临时文件: %s", it.Name)
			}
		}
	}
	t.Log("CloseAll 后无残留 ssh 子进程、无远端临时文件")
}

// hasLiveSftpChild 报告是否还有本次 sftp 会话起的 ssh 子进程（pgrep 不可用或 Windows 上
// 返回 false，调用方据此只在能做有意义断言的平台上检查）。
func hasLiveSftpChild(host string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		return false
	}
	out, _ := exec.Command("pgrep", "-fa", "--", host+" sftp").CombinedOutput()
	return len(bytes.TrimSpace(out)) > 0
}
