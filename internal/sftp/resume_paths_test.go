package sftp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/osutil"
)

// —— Task 11 hermetic 续传路径测试 ——
// 与 Task 7/8/10 同一模式：net.Pipe 接 pkg/sftp 的真实客户端 + 真实服务端。取消会关掉
// 该传输的会话，所以想要「取消后再续传」，dial 必须每条会话新建一对管道/服务端 ——
// backendForTestServer 复用同一条 Conn，只适合不需要二次握手的用例。

// backendForResumableTestServer 每次 dial 都新建一条真实（内存）SFTP 会话。
func backendForResumableTestServer(t *testing.T, root string) *GoBackend {
	t.Helper()
	g := NewGoBackend(nil, nil)
	g.pool.dial = func(host, user string) (*Session, error) {
		c1, c2 := net.Pipe()
		srv, err := sftp.NewServer(c1, sftp.WithServerWorkingDirectory(root))
		if err != nil {
			return nil, err
		}
		go func() { _ = srv.Serve() }()
		cl, err := sftp.NewClientPipe(c2, c2)
		if err != nil {
			_ = c1.Close()
			_ = srv.Close()
			return nil, err
		}
		pp, err := osutil.StartPipes("cat")
		if err != nil {
			_ = cl.Close()
			_ = c1.Close()
			_ = srv.Close()
			return nil, err
		}
		t.Cleanup(func() {
			_ = cl.Close()
			_ = srv.Close()
			_ = c1.Close()
			_ = c2.Close()
		})
		return &Session{Host: host, User: user, Conn: cl, Proc: pp, state: sessBusy}, nil
	}
	t.Cleanup(func() { g.CloseAll() })
	return g
}

// onlyInternalTemp 返回 dir 里唯一的内部临时文件绝对路径（多于/少于一个都 Fatal）。
func onlyInternalTemp(t *testing.T, dir string) string {
	t.Helper()
	var parts []string
	for _, e := range mustReadDir(t, dir) {
		if IsInternalTemp(e.Name()) {
			parts = append(parts, filepath.Join(dir, e.Name()))
		}
	}
	if len(parts) != 1 {
		t.Fatalf("应恰好保留一个 .part，got %v", parts)
	}
	return parts[0]
}

// TestGoBackendGetResumeAppendsWithoutTruncating 是下载续传的路径用例（事实 5）：
// 先取消一次下载留下 .part，再用同一个 PartPath 续传 —— copyFileToLocal 必须**不 O_TRUNC**、
// 远端与本地都 Seek 到断点，结果与源逐字节一致；首帧 Done 必须等于断点（证明真的续传，
// 而不是清档重传）。
func TestGoBackendGetResumeAppendsWithoutTruncating(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("r"), 300<<10) // 300 KiB：首块 32 KiB < total
	writeRemote(t, remoteRoot, "src.bin", data)
	g := backendForResumableTestServer(t, remoteRoot)
	local := filepath.Join(localDir, "dst.bin")

	entered, release := blockAfterFirstChunk(t)
	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: "cut", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块：会话/协议卡死")
	}
	if !g.Cancel("cut") {
		t.Fatal("取消在飞下载必须返回 true")
	}
	release()
	if err := <-done; err == nil {
		t.Fatal("被取消的下载必须报错")
	}
	partFile := onlyInternalTemp(t, localDir)
	st, err := os.Stat(partFile)
	if err != nil {
		t.Fatal(err)
	}
	prior := st.Size()
	if prior == 0 || prior >= int64(len(data)) {
		t.Fatalf("需要一份“部分”的 .part 才能测续传，got %d 字节", prior)
	}

	var sink progressSink
	if err := g.Get(TransferRequest{ID: "resume", Host: "h", Remote: "src.bin", Local: local, Atomic: true,
		Resume: true, PartPath: partFile}, sink.report); err != nil {
		t.Fatalf("续传下载失败: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("续传后目标必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("续传结果与源不一致（本地被 O_TRUNC 或某侧没 Seek）: got %d bytes want %d", len(got), len(data))
	}
	frames := sink.all()
	if len(frames) == 0 || frames[0].Done != prior {
		t.Fatalf("续传首帧 Done 必须等于断点 %d（否则就是清档重传）: %+v", prior, frames[:min(1, len(frames))])
	}
	for _, e := range mustReadDir(t, localDir) {
		if IsInternalTemp(e.Name()) {
			t.Fatalf("提交成功后不得残留 .part: %s", e.Name())
		}
	}
}

// TestGoBackendGetResumeCommitsCompletePart：.part 已完整（== 源大小）且指纹相符时直接提交，
// 不重新搬字节（避免「续传 0 字节」既无意义又可能再 Stat 出错）。
func TestGoBackendGetResumeCommitsCompletePart(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("c"), 5000)
	writeRemote(t, remoteRoot, "src.bin", data)
	g := backendForTestServer(t, remoteRoot)
	local := filepath.Join(localDir, "dst.bin")

	s, err := g.pool.AcquireList(context.Background(), "h", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Conn.Stat("src.bin")
	if err != nil {
		t.Fatal(err)
	}
	g.pool.Release(s, true)

	part := PartName(local, "done")
	if err := os.WriteFile(part, data, 0600); err != nil {
		t.Fatal(err)
	}
	g.recordResumeAnchor(downloadAnchorKey(part), st.Size(), st.ModTime())

	var sink progressSink
	if err := g.Get(TransferRequest{ID: "commit", Host: "h", Remote: "src.bin", Local: local, Atomic: true,
		Resume: true, PartPath: part}, sink.report); err != nil {
		t.Fatalf("完整 .part 必须直接提交: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("提交后内容必须与源一致: err=%v got=%d want=%d", err, len(got), len(data))
	}
	if _, serr := os.Stat(part); !os.IsNotExist(serr) {
		t.Fatalf(".part 应已被 rename 成目标，stat err=%v", serr)
	}
	frames := sink.all()
	if len(frames) != 1 || frames[0].PartPath != local || frames[0].Done != int64(len(data)) {
		t.Fatalf("提交帧必须是 PartPath=最终目标/Done=total 的末帧: %+v", frames)
	}
}

// TestGoBackendPutResumeSeeksLocalSourceToo（Task 8 修复轮 1 M，Task 11 改为真实续传）：
// 上传续传必须让远端 .part 与本地源都从同一 offset 续写；只 Seek 远端而本地从 0 读会拼出
// 损坏文件（且 n+offset 对不上总量）。用「取消留下部分 .part → 续传」的真实流程驱动，
// 保证走的是 decideResume=append 分支而不是退化成整份重传。
func TestGoBackendPutResumeSeeksLocalSourceToo(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("resume-"), 100000) // 700000 字节 > 32 KiB
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	g := backendForResumableTestServer(t, remoteRoot)
	const target = "dst.bin"

	entered, release := blockAfterFirstChunk(t)
	done := make(chan error, 1)
	go func() {
		done <- g.Put(TransferRequest{ID: "cut", Host: "h", Remote: target, Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块：会话/协议卡死")
	}
	if !g.Cancel("cut") {
		t.Fatal("取消在飞上传必须返回 true")
	}
	release()
	if err := <-done; err == nil {
		t.Fatal("被取消的上传必须报错")
	}
	temps := remoteTemps(t, remoteRoot)
	if len(temps) != 1 {
		t.Fatalf("取消后应恰好保留一个远端 .part，got %v", temps)
	}
	part := temps[0]
	st, err := os.Stat(filepath.Join(remoteRoot, part))
	if err != nil {
		t.Fatal(err)
	}
	prior := st.Size()
	if prior == 0 || prior >= int64(len(data)) {
		t.Fatalf("需要一份“部分”的远端 .part 才能测续传，got %d 字节", prior)
	}

	var sink progressSink
	req := TransferRequest{ID: "resume", Host: "h", Remote: target, Local: local, Atomic: true,
		Resume: true, PartPath: part}
	if err := g.Put(req, sink.report); err != nil {
		t.Fatalf("续传 Put: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(remoteRoot, target))
	if err != nil {
		t.Fatalf("提交后目标必须存在: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("续传结果与源不一致（本地源未按 offset 续读）: got %d bytes want %d", len(got), len(data))
	}
	frames := sink.all()
	if len(frames) == 0 || frames[0].Done != prior {
		t.Fatalf("续传首帧 Done 必须等于断点 %d: %+v", prior, frames[:min(1, len(frames))])
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("续传成功后不得残留 .part/.bak: %v", temps)
	}
}

// TestGoBackendPutResumeRefusesStalePartAndReuploads：无指纹记录（不可验证）或内容错位的
// 旧 .part 必须被判为不可续 —— 清掉它整份重传，绝不把旧前缀拼到新内容上。
func TestGoBackendPutResumeRefusesStalePartAndReuploads(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("fresh-"), 2000)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	const target = "dst.bin"
	part := PartNameRemote(target, "stale")
	// 大小同为 2000 但内容完全错位，且没有锚点记录（进程重启后的典型形态）。
	writeRemote(t, remoteRoot, part, bytes.Repeat([]byte("XXXXXX"), 2000)[:len(data)])

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "stale", Host: "h", Remote: target, Local: local, Atomic: true,
		Resume: true, PartPath: part}
	if err := g.Put(req, nil); err != nil {
		t.Fatalf("不可验证的 .part 应退回整份重传而不是报错: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(remoteRoot, target))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("整份重传后内容必须与源一致: err=%v got=%d want=%d", err, len(got), len(data))
	}
	if temps := remoteTemps(t, remoteRoot); len(temps) != 0 {
		t.Fatalf("整份重传成功后不得残留旧 .part: %v", temps)
	}
}

// TestGoBackendInflightDedupRejectsSameTarget：同一 (host,方向,目标) 只允许一条在飞传输；
// 释放后同一目标必须能再次传输（避免去重键泄漏把目标永久锁死）。
func TestGoBackendInflightDedupRejectsSameTarget(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("q"), 300<<10)
	writeRemote(t, remoteRoot, "src.bin", data)
	g := backendForResumableTestServer(t, remoteRoot)
	local := filepath.Join(localDir, "dst.bin")

	entered, release := blockAfterFirstChunk(t)
	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: "dup1", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内未写出首块")
	}
	errDup := g.Get(TransferRequest{ID: "dup2", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil)
	if errDup == nil {
		t.Fatal("同一目标并发传输必须被拒绝（inflight 去重）")
	}
	var te *TransferError
	if !errors.As(errDup, &te) || te.Err == nil {
		t.Fatalf("去重错误必须是带原因 TransferError: %v", errDup)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("未被去重拒绝的那条传输必须正常完成: %v", err)
	}
	// 释放后同目标可再次传输。
	if err := g.Get(TransferRequest{ID: "dup3", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, nil); err != nil {
		t.Fatalf("去重释放后同一目标必须能再传: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("再次传输结果必须正确: err=%v got=%d want=%d", err, len(got), len(data))
	}
}
