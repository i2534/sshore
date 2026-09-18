package sftp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	t.Logf("取消验证完成（后端=%v）：直连+门面均关会话、未提交、.part 保留、二次取消 false", be)
}
