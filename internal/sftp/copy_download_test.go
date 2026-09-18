package sftp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/osutil"
)

// —— Task 7 hermetic 下载测试 ——
// 用 net.Pipe 把 pkg/sftp 的**真实客户端与真实服务端**接在一起：不联网、不起 ssh，
// 但走的是真正的 SFTP 协议（Stat/Open/Read），因此 Get 的提交前置、进度上报、
// 取消保留 .part、会话关闭都被真实执行，而不是被 mock 掉。
// 会话的 Proc 用 cat 兜住：Session.close() 会关管道 + Kill，没有真实子进程会 nil panic。

// backendForTestServer 起一个只在内存里的 sftp 会话池，并把 dial 指向它。
func backendForTestServer(t *testing.T, root string) *GoBackend {
	t.Helper()
	c1, c2 := net.Pipe()
	srv, err := sftp.NewServer(c1, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	cl, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	pp, err := osutil.StartPipes("cat")
	if err != nil {
		t.Fatalf("StartPipes(cat): %v", err)
	}
	g := NewGoBackend(nil, nil)
	g.pool.dial = func(host, user string) (*Session, error) {
		return &Session{Host: host, User: user, Conn: cl, Proc: pp, state: sessBusy}, nil
	}
	t.Cleanup(func() {
		g.CloseAll()
		_ = srv.Close()
		_ = c1.Close()
		_ = c2.Close()
	})
	return g
}

// writeRemote 在测试服务端的根目录下造一个远端文件。
func writeRemote(t *testing.T, root, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatalf("写远端文件: %v", err)
	}
	return p
}

// progressSink 收集进度帧；多数用例在同一 goroutine 完成后再读，
// 但取消/关会话用例跨 goroutine，所以统一加锁。
type progressSink struct {
	mu     sync.Mutex
	frames []Progress
}

func (s *progressSink) report(p Progress) {
	s.mu.Lock()
	s.frames = append(s.frames, p)
	s.mu.Unlock()
}

func (s *progressSink) all() []Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Progress(nil), s.frames...)
}

// swapCopyStream 在用例期间替换字节搬运入口，返回还原函数。
func swapCopyStream(t *testing.T, fn func(dst io.Writer, src io.Reader) (int64, error)) {
	t.Helper()
	orig := copyStream
	copyStream = fn
	t.Cleanup(func() { copyStream = orig })
}

func sha256Hex(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件做 sha256: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestGoBackendGetHappyPathCommitsAtomically 覆盖 plan 的「happy path」：
// done==total 才 rename；目标内容与源逐字节一致；本地 .part 全部消失（已提交）；
// 进度末帧强制送达且终值 == total；会话被归还而不是关掉。
func TestGoBackendGetHappyPathCommitsAtomically(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("sshore-task7-"), 1024) // 13 KiB
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	req := TransferRequest{ID: "t1", Host: "h", Remote: "src.bin", Local: local, Atomic: true}
	if err := g.Get(req, sink.report); err != nil {
		t.Fatalf("Get: %v", err)
	}

	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("提交后目标必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("目标内容与源不一致: got %d bytes want %d", len(got), len(data))
	}
	if sum := sha256Hex(t, local); sum != sha256Hex(t, filepath.Join(remoteRoot, "src.bin")) {
		t.Fatal("sha256 不一致")
	}
	// 提交后不得留下 .part（rename 已消费掉）。
	ents, err := os.ReadDir(localDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			t.Fatalf("成功后不得残留临时文件: %s", e.Name())
		}
	}

	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("必须有进度帧")
	}
	if first := frames[0]; first.Done != 0 {
		t.Fatalf("首帧应为 0/total，got %d/%d", first.Done, first.Total)
	}
	last := frames[len(frames)-1]
	if last.Done != int64(len(data)) || last.Total != int64(len(data)) {
		t.Fatalf("末帧必须是完整终值，got %d/%d", last.Done, last.Total)
	}
	if last.ID != "t1" || last.Host != "h" || last.Direction != DirDownload ||
		last.Name != "src.bin" || last.Phase != PhaseTransfer {
		t.Fatalf("末帧字段不完整: %+v", last)
	}
	// M1（Task 7 评审）：末帧在 rename 之后发出，PartPath 必须指向**已提交的最终目标**，
	// 而不是那个已经被 rename 掉、不再存在的旧 .part（探针实测 STALE）。这里不只断言非空，
	// 而是逐字钉住语义 + 目标真实存在，避免以后再退回「随便给个非空 part」的假实现。
	if last.PartPath != local {
		t.Fatalf("末帧 PartPath 应为已提交的目标路径 %q，got %q", local, last.PartPath)
	}
	if _, err := os.Stat(last.PartPath); err != nil {
		t.Fatalf("末帧 PartPath 必须指向真实存在的文件（不得 STALE）: %v", err)
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].Done < frames[i-1].Done {
			t.Fatalf("进度必须单调不减: 第 %d 帧 %d < 第 %d 帧 %d", i, frames[i].Done, i-1, frames[i-1].Done)
		}
	}
	// 会话被归还进 idle（happy path 的 reuse=true），可被列表复用。
	if !g.pool.Connected("h") {
		t.Fatal("成功后会话应仍在池中（reuse=true）")
	}
}

// TestGoBackendGetShortReadNeverCommits 钉住唯一防线（注入短读 reader 版）：
// 源提前 EOF（ReadFrom 返回 nil）时 done != total，必须**不提交**、报错，
// 并保留 .part 作为续传锚点。注入的 copyStream 直接返回 (40, nil)，是危险形状本身。
func TestGoBackendGetShortReadNeverCommits(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("x"), 100)
	writeRemote(t, remoteRoot, "src.bin", data)
	// 预置一个「上次失败留下的旧目标」，证明失败路径不会碰到它。
	local := filepath.Join(localDir, "dst.bin")
	const old = "OLD-CONTENT"
	if err := os.WriteFile(local, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	// 注入「只搬 40 字节后正常 EOF」的搬运实现：这正是 R13 描述的危险路径
	// （库的 WriteTo 对 EOF 返回 (n, nil)），必须由 done!=total 兜住。
	swapCopyStream(t, func(dst io.Writer, src io.Reader) (int64, error) {
		// 注意：src 在这里是 io.LimitReader 包装，不是 io.Closer；由 Get 负责关源。
		return io.Copy(dst, io.LimitReader(src, 40))
	})

	req := TransferRequest{ID: "short", Host: "h", Remote: "src.bin", Local: local, Atomic: true}
	err := g.Get(req, nil)
	if err == nil {
		t.Fatal("少传时必须报错，绝不能把截断文件提交成成功")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("错误应为 *TransferError, got %T: %v", err, err)
	}
	// 目标必须保持旧内容（不提交）。
	if b, rerr := os.ReadFile(local); rerr != nil || string(b) != old {
		t.Fatalf("失败路径不得改写目标: err=%v content=%q", rerr, string(b))
	}
	// .part 必须保留，且内容是真实已落盘的 40 字节前缀（续传锚点）。
	ents, derr := os.ReadDir(localDir)
	if derr != nil {
		t.Fatal(derr)
	}
	var parts []string
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			parts = append(parts, e.Name())
		}
	}
	if len(parts) != 1 {
		t.Fatalf("失败后必须保留恰好一个 .part，got %v", parts)
	}
	partPath := filepath.Join(localDir, parts[0])
	pb, perr := os.ReadFile(partPath)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(pb) != 40 {
		t.Fatalf(".part 应保留已写前缀 40 字节，got %d", len(pb))
	}
}

// TestGoBackendGetTransportErrorKeepsPartAndClosesSession：搬运中途的传输错误
// （等价于「关会话取消」在 Get 内看到的形状）必须保留 .part、不提交、关掉会话。
func TestGoBackendGetTransportErrorKeepsPartAndClosesSession(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("y"), 1<<16) // 64 KiB
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	var mu sync.Mutex
	var seen *Session
	g := backendForTestServer(t, remoteRoot)
	origDial := g.pool.dial
	g.pool.dial = func(host, user string) (*Session, error) {
		s, err := origDial(host, user)
		mu.Lock()
		seen = s
		mu.Unlock()
		return s, err
	}

	boom := errors.New("connection lost")
	swapCopyStream(t, func(dst io.Writer, src io.Reader) (int64, error) {
		buf := make([]byte, 4096)
		n, _ := io.ReadFull(src, buf)
		if _, err := dst.Write(buf[:n]); err != nil {
			return 0, err
		}
		return int64(n), boom
	})

	req := TransferRequest{ID: "cancel", Host: "h", Remote: "src.bin", Local: local, Atomic: true}
	err := g.Get(req, nil)
	if err == nil {
		t.Fatal("传输错误必须上报")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("必须保留原始传输错误（spec D12），got %v", err)
	}
	if _, serr := os.Stat(local); !os.IsNotExist(serr) {
		t.Fatalf("取消/失败后最终名不得出现，stat err=%v", serr)
	}
	mu.Lock()
	s := seen
	mu.Unlock()
	if s == nil {
		t.Fatal("未捕获会话")
	}
	// 失败路径不得复用会话：必须已关闭（Task 10 的 Cancel 会走到同一状态）。
	if !s.Closed() {
		t.Fatal("失败后会话必须被关闭而不是塞回 idle")
	}
	if g.Connected("h") {
		t.Fatal("会话关闭后 Connected 必须为 false")
	}
	// .part 必须存在（续传锚点）。
	ents, _ := os.ReadDir(localDir)
	found := ""
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			found = e.Name()
		}
	}
	if found == "" {
		t.Fatal("失败后必须保留 .part（Task 11 的续传锚点）")
	}
}

// TestGoBackendGetMidTransferSessionCloseCancels：取消的真实实现 = 关该传输的会话
// （库无逐请求 ctx，spec D9 / §2.3）。在**首个字节真正落盘之后**关掉会话，
// 断言：copy 立刻失败、目标不出现、.part 是源的前缀（可续传）。
func TestGoBackendGetMidTransferSessionCloseCancels(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("z"), 2<<20) // 2 MiB：保证跨多个 chunk
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	var mu sync.Mutex
	var sess *Session
	firstWrite := make(chan struct{})
	var once sync.Once
	g := backendForTestServer(t, remoteRoot)
	origDial := g.pool.dial
	g.pool.dial = func(host, user string) (*Session, error) {
		s, err := origDial(host, user)
		mu.Lock()
		sess = s
		mu.Unlock()
		return s, err
	}

	swapCopyStream(t, func(dst io.Writer, src io.Reader) (int64, error) {
		defer src.(io.Closer).Close()
		buf := make([]byte, 32*1024)
		var total int64
		for {
			n, rerr := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return total, werr
				}
				total += int64(n)
				once.Do(func() { close(firstWrite) })
			}
			if rerr != nil {
				if rerr == io.EOF {
					return total, nil
				}
				return total, rerr
			}
		}
	})

	done := make(chan error, 1)
	req := TransferRequest{ID: "cancel-live", Host: "h", Remote: "src.bin", Local: local, Atomic: true}
	go func() { done <- g.Get(req, nil) }()

	// 等首个字节真的落盘，再从另一个 goroutine 关会话（模拟 Task 10 的 Cancel）。
	select {
	case <-firstWrite:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内没有写出首字节：会话/协议卡死")
	}
	mu.Lock()
	s := sess
	mu.Unlock()
	go s.close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("关会话后 Get 必须报错（库无逐请求 ctx，取消靠关会话）")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("关会话后 10s 内 Get 仍未返回（取消不生效）")
	}
	if _, serr := os.Stat(local); !os.IsNotExist(serr) {
		t.Fatalf("取消后最终名不得出现，stat err=%v", serr)
	}
	ents, _ := os.ReadDir(localDir)
	var part string
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			part = e.Name()
		}
	}
	if part == "" {
		t.Fatal("取消后必须保留 .part")
	}
	pb, err := os.ReadFile(filepath.Join(localDir, part))
	if err != nil {
		t.Fatal(err)
	}
	if len(pb) == 0 || len(pb) > len(data) {
		t.Fatalf(".part 大小异常: %d", len(pb))
	}
	if !bytes.Equal(pb, data[:len(pb)]) {
		t.Fatal(".part 必须是源的前缀（Task 11 续传前提）")
	}
}

// TestGoBackendGetLegacyNonAtomicWritesDirectly：Atomic=false 是 legacy 面
// （internal/sync 自带 .part+rename），必须直写目标、不产生我方 .part。
func TestGoBackendGetLegacyNonAtomicWritesDirectly(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("legacy"), 100)
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "legacy", Host: "h", Remote: "src.bin", Local: local}
	if err := g.Get(req, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("legacy 面必须直写目标: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("内容不一致")
	}
	ents, _ := os.ReadDir(localDir)
	for _, e := range ents {
		if IsInternalTemp(e.Name()) {
			t.Fatalf("legacy 面不得产生我方 .part: %s", e.Name())
		}
	}
}

// TestGoBackendGetStatErrorIsWrapped：远端不存在时必须在建任何本地文件之前失败，
// 并带上 *TransferError 形状（spec D12）。
func TestGoBackendGetStatErrorIsWrapped(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	local := filepath.Join(localDir, "dst.bin")
	g := backendForTestServer(t, remoteRoot)
	err := g.Get(TransferRequest{ID: "x", Host: "h", Remote: "nope.bin", Local: local, Atomic: true}, nil)
	if err == nil {
		t.Fatal("远端不存在必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("错误应为 *TransferError, got %T", err)
	}
	if ents, _ := os.ReadDir(localDir); len(ents) != 0 {
		t.Fatalf("stat 失败时不得在本地留下任何文件: %v", ents)
	}
}

// TestCopyFileToLocalReturnsPartWithoutCommitting 钉住 Task 12 依赖的 helper 契约：
// 返回 (part 路径, done, total)，**不**做 rename 提交，.part 留在原处可供续传；
// 且临时名用本地 PartName 家族（IsInternalTemp 命中）。
func TestCopyFileToLocalReturnsPartWithoutCommitting(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("k"), 6000)
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	var sess *Session
	origDial := g.pool.dial
	g.pool.dial = func(host, user string) (*Session, error) {
		s, err := origDial(host, user)
		sess = s
		return s, err
	}
	s, err := g.pool.AcquireTransfer(context.Background(), "h", "")
	if err != nil {
		t.Fatalf("AcquireTransfer: %v", err)
	}
	defer g.pool.Release(s, false)
	if sess == nil {
		t.Fatal("未拿到会话")
	}

	part, done, total, err := g.copyFileToLocal(sess, TransferRequest{ID: "tree", Host: "h"}, "src.bin", local, nil)
	if err != nil {
		t.Fatalf("copyFileToLocal: %v", err)
	}
	if done != int64(len(data)) || total != int64(len(data)) {
		t.Fatalf("字节数不对: done=%d total=%d want %d", done, total, len(data))
	}
	if filepath.Dir(part) != localDir || !IsInternalTemp(filepath.Base(part)) {
		t.Fatalf("part 必须是目标同目录下的本地临时名（Task 12 依赖）: %q", part)
	}
	if _, serr := os.Stat(local); !os.IsNotExist(serr) {
		t.Fatalf("helper 不得自行提交（rename 由调用方做）: %v", serr)
	}
	pb, err := os.ReadFile(part)
	if err != nil {
		t.Fatalf("读取 .part: %v", err)
	}
	if !bytes.Equal(pb, data) {
		t.Fatal(".part 内容应与源一致")
	}
}

// TestCopyFileToLocalLocalOpenErrorIsPathError：本地 .part 建不出来时（只读父目录）
// 必须原样返回 *os.PathError（Get 据此把错误归到本地路径而不是远端）。
func TestCopyFileToLocalLocalOpenErrorIsPathError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下 CAP_DAC_OVERRIDE 会绕过目录权限，0500 不构成只读")
	}
	remoteRoot := t.TempDir()
	roParent := t.TempDir() // 只读父目录 ⇒ 连 .part 也建不出来（父目录不可写）
	if err := os.Chmod(roParent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roParent, 0700) }) // 让 t.TempDir 的清理能删掉它
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("m"), 10))
	local := filepath.Join(roParent, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	var sess *Session
	origDial := g.pool.dial
	g.pool.dial = func(host, user string) (*Session, error) {
		s, err := origDial(host, user)
		sess = s
		return s, err
	}
	s, err := g.pool.AcquireTransfer(context.Background(), "h", "")
	if err != nil {
		t.Fatalf("AcquireTransfer: %v", err)
	}
	defer g.pool.Release(s, false)

	part, _, total, err := g.copyFileToLocal(sess, TransferRequest{ID: "e", Host: "h"}, "src.bin", local, nil)
	if err == nil {
		t.Fatal("本地目标不可写必须报错")
	}
	if total != 10 {
		t.Fatalf("应先 Stat 到远端大小 10，got %d", total)
	}
	// I2（Task 7 评审）：.part 根本没建出来 ⇒ 返回的 part 必须为空。原先返回 part，
	// 调用方会据此填一个 ENOENT 的假锚点；这里从 helper 契约上钉死「空 = 没有可续传文件」。
	if part != "" {
		t.Fatalf(".part 未创建时 helper 必须返回空 part，got %q", part)
	}
	if !errors.Is(err, errLocalPart) {
		t.Fatalf("本地 .part 创建失败必须带 errLocalPart 标记（供 Get 判不要附 RemoteMsg）: %v", err)
	}
	var pe *os.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("应为 *os.PathError, got %T: %v", err, err)
	}
}

// TestGoBackendGetZeroByteFileCommits：0 字节文件是 done==total==0 的合法完整传输。
func TestGoBackendGetZeroByteFileCommits(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	writeRemote(t, remoteRoot, "empty.bin", nil)
	local := filepath.Join(localDir, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	sink := &progressSink{}
	if err := g.Get(TransferRequest{ID: "z", Host: "h", Remote: "empty.bin", Local: local, Atomic: true}, sink.report); err != nil {
		t.Fatalf("0 字节文件应成功提交: %v", err)
	}
	st, err := os.Stat(local)
	if err != nil {
		t.Fatalf("目标必须存在: %v", err)
	}
	if st.Size() != 0 {
		t.Fatalf("0 字节文件结果非空: %d", st.Size())
	}
	frames := sink.all()
	if len(frames) == 0 || frames[len(frames)-1].Done != 0 {
		t.Fatalf("末帧应为 0/0，got %+v", frames)
	}
}

// TestGoBackendGetShortReadPartPathOnError：失败时 TransferError.PartPath 必须指向
// 保留下来的 .part（Task 11 靠它续传，不解析错误文案）。
func TestGoBackendGetShortReadPartPathOnError(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("q"), 100))
	local := filepath.Join(localDir, "dst.bin")
	g := backendForTestServer(t, remoteRoot)
	swapCopyStream(t, func(dst io.Writer, src io.Reader) (int64, error) {
		return io.Copy(dst, io.LimitReader(src, 10))
	})
	var te *TransferError
	if err := g.Get(TransferRequest{ID: "p", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil); !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T", err)
	}
	if te.PartPath == "" {
		t.Fatal("失败时 TransferError.PartPath 必须给出保留的 .part")
	}
	if filepath.Dir(te.PartPath) != localDir || !IsInternalTemp(filepath.Base(te.PartPath)) {
		t.Fatalf("PartPath 形状不对: %q", te.PartPath)
	}
	if _, err := os.Stat(te.PartPath); err != nil {
		t.Fatalf("PartPath 指向的 .part 必须真实存在: %v", err)
	}
}

// TestGoBackendGetReadOnlyDestPartPathIsEmpty（Task 7 评审 I2）：本地 .part 建不出来时
// TransferError.PartPath 必须为空。假锚点会让 Task 11 拿到一条 ENOENT 的「可续传」路径。
func TestGoBackendGetReadOnlyDestPartPathIsEmpty(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下 CAP_DAC_OVERRIDE 会绕过目录权限，0500 不构成只读")
	}
	remoteRoot := t.TempDir()
	roParent := t.TempDir()
	if err := os.Chmod(roParent, 0500); err != nil { // 父目录不可写 ⇒ .part 建不出来
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roParent, 0700) })
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("r"), 64))
	local := filepath.Join(roParent, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "ro", Host: "h", Remote: "src.bin", Local: local, Atomic: true}
	err := g.Get(req, nil)
	if err == nil {
		t.Fatal("只读目标必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.PartPath != "" {
		t.Fatalf(".part 未创建时 PartPath 必须为空（假锚点违反 api.go「未用到时为空」），got %q", te.PartPath)
	}
	if _, serr := os.Stat(filepath.Join(roParent, "dst.bin")); !os.IsNotExist(serr) {
		t.Fatalf("失败后最终名不得出现，stat err=%v", serr)
	}
	if ents, _ := os.ReadDir(roParent); len(ents) != 0 {
		t.Fatalf("只读目标下不该留下任何文件: %v", ents)
	}
}

// TestGoBackendGetLocalErrorNotMaskedByStderr（Task 7 评审 M4）：本地文件系统错误
// 绝不能被非空的远端 stderr 盖掉。api.go 的 Error() 优先打印 RemoteMsg；若本地错误也被
// 套上 stderr，生产上就会看到远端噪音而看不到「本地磁盘/权限」这个真正原因。
func TestGoBackendGetLocalErrorNotMaskedByStderr(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下 CAP_DAC_OVERRIDE 会绕过目录权限，0500 不构成只读")
	}
	remoteRoot := t.TempDir()
	roParent := t.TempDir()
	if err := os.Chmod(roParent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roParent, 0700) })
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("s"), 32))
	local := filepath.Join(roParent, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	// 故意的「远端噪音」：子进程写 stderr，drain goroutine 收进 StderrText。
	// 先起进程再挂到会话上，保证断言时 stderr 一定非空（否则用例空转）。
	const noisy = "ssh-noise-should-not-mask-local-error"
	pp := noisyProcForDial(t, g, "sh", "-c", "printf '%s\\n' \"$1\" >&2; cat >/dev/null", "sh", noisy)
	req := TransferRequest{ID: "mask", Host: "h", Remote: "src.bin", Local: local, Atomic: true}

	// 等 drain 真的收到那行 stderr 再传输 —— 否则断言「没被盖掉」是因为 stderr 恰为空。
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(pp.StderrText(), noisy) {
		if time.Now().After(deadline) {
			t.Fatal("5s 内未收到注入的 stderr（用例无法构成 M4 复现条件）")
		}
		time.Sleep(10 * time.Millisecond)
	}

	err := g.Get(req, nil)
	if err == nil {
		t.Fatal("只读目标必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.RemoteMsg != "" {
		t.Fatalf("本地错误不得附 RemoteMsg（会盖掉真正的本地原因），got %q", te.RemoteMsg)
	}
	if te.PartPath != "" {
		t.Fatalf("本地错误且无 .part 时 PartPath 必须为空，got %q", te.PartPath)
	}
	msg := te.Error()
	if strings.Contains(msg, noisy) {
		t.Fatalf("本地错误被远端 stderr 盖掉: %s", msg)
	}
	if !strings.Contains(msg, "本地 .part 创建失败") {
		t.Fatalf("本地错误必须保留自身原因，got: %s", msg)
	}
	if !strings.Contains(msg, local) {
		t.Fatalf("本地错误应指向本地目标路径 %q，got: %s", local, msg)
	}
}

// noisyProcForDial 把「会写 stderr 的子进程」挂成 dial 出来的会话 Proc，用于错误归属用例。
// 两个细节都为了不漏进程（Task 7 修复轮 2 清理）：
//  1. backendForTestServer 造的 cat 被顶替后不再被任何 Session 持有，CloseAll 收不到它，
//     必须由用例自己收；
//  2. 用 Close 而不是 Kill —— 命令里的 cat 后代只会在父端 stdin 关闭后收 EOF 退出，
//     Kill 只杀直接子进程，会把 cat 留成孤儿。
func noisyProcForDial(t *testing.T, g *GoBackend, name string, args ...string) *osutil.PipedProcess {
	t.Helper()
	pp, err := osutil.StartPipes(name, args...)
	if err != nil {
		t.Fatalf("StartPipes: %v", err)
	}
	origDial := g.pool.dial
	var mu sync.Mutex
	var displaced []*osutil.PipedProcess
	g.pool.dial = func(host, user string) (*Session, error) {
		s, derr := origDial(host, user)
		if derr != nil {
			return nil, derr
		}
		mu.Lock()
		displaced = append(displaced, s.Proc)
		mu.Unlock()
		s.Proc = pp
		return s, nil
	}
	t.Cleanup(func() {
		mu.Lock()
		ds := append([]*osutil.PipedProcess(nil), displaced...)
		mu.Unlock()
		for _, d := range ds {
			if d != nil && d != pp {
				_ = d.Close()
			}
		}
		_ = pp.Close()
	})
	return pp
}

// TestGoBackendGetLegacyLocalErrorNotMaskedByStderr（Task 7 修复轮 2 / M4 剩余）：
// Atomic=false 走 getNonAtomic 时，本地目标文件 OpenFile 失败同样是本地错误，
// 不能被非空远端 stderr 盖掉。该分支经 ctrl.go 的 legacy 四参面被 internal/sync 实际调用，
// 与原子路径共用 isRemoteError(err) 判据。
func TestGoBackendGetLegacyLocalErrorNotMaskedByStderr(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下 CAP_DAC_OVERRIDE 会绕过目录权限，0500 不构成只读")
	}
	remoteRoot := t.TempDir()
	roParent := t.TempDir()
	if err := os.Chmod(roParent, 0500); err != nil { // 父目录不可写 ⇒ 目标文件建不出来
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roParent, 0700) })
	writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("l"), 32))
	local := filepath.Join(roParent, "dst.bin")

	g := backendForTestServer(t, remoteRoot)
	const noisy = "legacy-noise-should-not-mask-local-error"
	pp := noisyProcForDial(t, g, "sh", "-c", "printf '%s\\n' \"$1\" >&2; cat >/dev/null", "sh", noisy)

	// 先等 drain 真的收到 stderr 再传 —— 否则「没被盖掉」可能只是 stderr 恰为空。
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(pp.StderrText(), noisy) {
		if time.Now().After(deadline) {
			t.Fatal("5s 内未收到注入的 stderr（用例无法构成 M4 复现条件）")
		}
		time.Sleep(10 * time.Millisecond)
	}

	req := TransferRequest{ID: "legacy-mask", Host: "h", Remote: "src.bin", Local: local, Atomic: false}
	err := g.Get(req, nil)
	if err == nil {
		t.Fatal("只读目标必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.RemoteMsg != "" {
		t.Fatalf("legacy 本地错误不得附 RemoteMsg（会盖掉真正的本地原因），got %q", te.RemoteMsg)
	}
	if te.PartPath != "" {
		t.Fatalf("legacy 面不使用 .part，PartPath 必须为空，got %q", te.PartPath)
	}
	msg := te.Error()
	if strings.Contains(msg, noisy) {
		t.Fatalf("legacy 本地错误被远端 stderr 盖掉: %s", msg)
	}
	if !strings.Contains(msg, local) {
		t.Fatalf("legacy 本地错误应指向本地目标路径 %q，got: %s", local, msg)
	}
	if _, serr := os.Stat(local); !os.IsNotExist(serr) {
		t.Fatalf("失败后最终名不得出现，stat err=%v", serr)
	}
}
