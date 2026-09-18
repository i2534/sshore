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
	g.recordResumeAnchor(downloadAnchorKey(part), "src.bin", st.Size(), st.ModTime())

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

// —— Task 11 修复轮 1：I1/I2/I3/M1/M3/M4 的回归用例 ——

// swapBeforeOpenResumePart 替换下载方向的 I2/I3 注入点，返回还原函数（t.Cleanup 自动还原）。
func swapBeforeOpenResumePart(t *testing.T, fn func(part, remote string, offset int64)) func() {
	t.Helper()
	orig := beforeOpenResumePart
	beforeOpenResumePart = fn
	restore := func() { beforeOpenResumePart = orig }
	t.Cleanup(restore)
	return restore
}

// swapBeforeOpenLocalSource 替换上传方向的 I3 注入点，返回还原函数。
func swapBeforeOpenLocalSource(t *testing.T, fn func(local string)) func() {
	t.Helper()
	orig := beforeOpenLocalSource
	beforeOpenLocalSource = fn
	restore := func() { beforeOpenLocalSource = orig }
	t.Cleanup(restore)
	return restore
}

// TestGoBackendGetResumeRefusesStalePartAndReuploads（I1）是上传同名用例的下载对称版：
// 手造一份与源**同尺寸但内容错位**的本地 .part，且锚点表里没有任何记录（进程重启后的
// 典型形态）。判定必须落到 resumeFull —— 旧 .part 被清掉、整份重传，结果与源逐字节一致。
//
// 为什么必须有这条用例（评审 I1）：下载侧「无锚点 ⇒ 整份重传」原先没有任何路径测试。
// 变异「把锚点查找换成用当前 Stat 自我背书，让 SrcChanged 永不为真」会让错位的同尺寸
// .part 被判成 append，旧垃圾前缀 + 源的新内容被拼成静默损坏的文件 —— 本用例必须 FAIL。
func TestGoBackendGetResumeRefusesStalePartAndReuploads(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("fresh-"), 2000)
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")
	part := PartName(local, "stale")
	// 大小同为 len(data) 但内容完全错位；故意不写任何锚点记录。
	if err := os.WriteFile(part, bytes.Repeat([]byte("XXXXXX"), 2000)[:len(data)], 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	req := TransferRequest{ID: "stale", Host: "h", Remote: "src.bin", Local: local, Atomic: true,
		Resume: true, PartPath: part}
	if err := g.Get(req, nil); err != nil {
		t.Fatalf("不可验证的 .part 应退回整份重传而不是报错: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("整份重传后内容必须与源一致: err=%v got=%d want=%d", err, len(got), len(data))
	}
	if _, serr := os.Stat(part); !os.IsNotExist(serr) {
		t.Fatalf("不可续的旧 .part 必须被清掉，stat err=%v", serr)
	}
	for _, e := range mustReadDir(t, localDir) {
		if IsInternalTemp(e.Name()) {
			t.Fatalf("整份重传成功后不得残留 .part: %s", e.Name())
		}
	}
}

// TestGoBackendGetResumeRevalidatesLocalPartSize（I2）：续传判定用的 Stat 与真正打开本地
// .part 之间存在窗口，另一进程可能在窗口里截断/追加它。若打开后不复核长度：
//   - 被截短 → Seek(offset) 越过 EOF 零填充出洞，而 done==total 仍会提交，产出带洞文件；
//   - 被加长到超过总量 → 陈旧尾部会留在最终文件里。
//
// 注入点 beforeOpenResumePart 精确制造这两种窗口形态，结果都必须逐字节等于源。
func TestGoBackendGetResumeRevalidatesLocalPartSize(t *testing.T) {
	for _, mode := range []string{"truncate", "grow-beyond-total"} {
		t.Run(mode, func(t *testing.T) {
			remoteRoot, localDir := t.TempDir(), t.TempDir()
			data := bytes.Repeat([]byte("revalidate-"), 20000) // 220000 字节
			writeRemote(t, remoteRoot, "src.bin", data)
			local := filepath.Join(localDir, "dst.bin")
			part := PartName(local, "i2")
			const offset = int64(70000)
			if err := os.WriteFile(part, data[:offset], 0600); err != nil {
				t.Fatal(err)
			}

			g := backendForTestServer(t, remoteRoot)
			// 记录与源一致的锚点（模拟上一轮传输留下的合法锚点）。
			s, err := g.pool.AcquireList(context.Background(), "h", "")
			if err != nil {
				t.Fatal(err)
			}
			st, err := s.Conn.Stat("src.bin")
			if err != nil {
				t.Fatal(err)
			}
			g.pool.Release(s, true)
			g.recordResumeAnchor(downloadAnchorKey(part), "src.bin", st.Size(), st.ModTime())

			restore := swapBeforeOpenResumePart(t, func(p, remote string, off int64) {
				if p != part {
					return
				}
				switch mode {
				case "truncate":
					if err := os.Truncate(part, off/2); err != nil {
						t.Fatal(err)
					}
				case "grow-beyond-total":
					f, err := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0600)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = f.Close() }()
					grow := int64(len(data)) - off + 100
					if _, err := f.Write(bytes.Repeat([]byte("J"), int(grow))); err != nil {
						t.Fatal(err)
					}
				}
			})
			defer restore()

			if err := g.Get(TransferRequest{ID: "i2", Host: "h", Remote: "src.bin", Local: local, Atomic: true,
				Resume: true, PartPath: part}, nil); err != nil {
				t.Fatalf("窗口内长度不匹配应退回整份重传而不是报错: %v", err)
			}
			got, err := os.ReadFile(local)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("结果必须逐字节等于源（窗口内 .part 长度变化必须被兜住）: err=%v got=%d want=%d", err, len(got), len(data))
			}
		})
	}
}

// TestGoBackendGetResumeRefusesRewriteInWindow（I3）：下载在 resume.go 里用一次 Stat 校
// mtime，copy.go 里却要重新 Stat + Open；同尺寸改写若发生在这两次调用之间，旧前缀会被拼到
// 新内容上。注入点在「判定完成、句柄打开」之间把远端源同尺寸改写并改 mtime；续传必须拒绝
// 追加、改为整份重传，结果等于**改写后**的源。
func TestGoBackendGetResumeRefusesRewriteInWindow(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("old-"), 20000)
	rewritten := bytes.Repeat([]byte("NEW-"), 20000) // 同尺寸
	srcPath := writeRemote(t, remoteRoot, "src.bin", data)
	oldMT := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(srcPath, oldMT, oldMT); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(localDir, "dst.bin")
	part := PartName(local, "i3")
	const offset = int64(40000)
	if err := os.WriteFile(part, data[:offset], 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	s, err := g.pool.AcquireList(context.Background(), "h", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Conn.Stat("src.bin")
	if err != nil {
		t.Fatal(err)
	}
	g.pool.Release(s, true)
	g.recordResumeAnchor(downloadAnchorKey(part), "src.bin", st.Size(), st.ModTime())

	newMT := time.Now().Truncate(time.Second)
	restore := swapBeforeOpenResumePart(t, func(p, remote string, off int64) {
		if p != part {
			return
		}
		if err := os.WriteFile(srcPath, rewritten, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(srcPath, newMT, newMT); err != nil {
			t.Fatal(err)
		}
	})
	defer restore()

	if err := g.Get(TransferRequest{ID: "i3", Host: "h", Remote: "src.bin", Local: local, Atomic: true,
		Resume: true, PartPath: part}, nil); err != nil {
		t.Fatalf("窗口内改写应退回整份重传而不是报错: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, rewritten) {
		t.Fatalf("窗口内改写后必须整份重传新内容，绝不能旧前缀+新内容拼接: err=%v got=%d want=%d", err, len(got), len(rewritten))
	}
}

// TestGoBackendGetResumeRefusesAnchorForOtherSource（M1）：锚点只记 (size,mtime) 时，
// 另一份同尺寸同 mtime 的无关源会被误当成合法前缀。这里为同一份 .part 记录一个**别的
// 远端源**的锚点，再用当前源发起续传：身份不符必须判不可续、整份重传。
func TestGoBackendGetResumeRefusesAnchorForOtherSource(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("real-"), 1000)  // 5000 字节
	other := bytes.Repeat([]byte("fake-"), 1000) // 同尺寸、内容不同
	writeRemote(t, remoteRoot, "src.bin", data)
	writeRemote(t, remoteRoot, "other.bin", other)
	// 两份源必须同 mtime，否则「只比 size+mtime」的旧实现也会因 mtime 不符而拒绝，
	// 用例就没有牙。显式把 mtime 钉成同一个确定值。
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, n := range []string{"src.bin", "other.bin"} {
		if err := os.Chtimes(filepath.Join(remoteRoot, n), mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	local := filepath.Join(localDir, "dst.bin")
	part := PartName(local, "m1")
	// .part 是 other.bin 的前缀（内容错位），锚点也记在 other.bin 上。
	if err := os.WriteFile(part, other[:3000], 0600); err != nil {
		t.Fatal(err)
	}

	g := backendForTestServer(t, remoteRoot)
	s, err := g.pool.AcquireList(context.Background(), "h", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Conn.Stat("other.bin")
	if err != nil {
		t.Fatal(err)
	}
	g.pool.Release(s, true)
	g.recordResumeAnchor(downloadAnchorKey(part), "other.bin", st.Size(), st.ModTime())

	if err := g.Get(TransferRequest{ID: "m1", Host: "h", Remote: "src.bin", Local: local, Atomic: true,
		Resume: true, PartPath: part}, nil); err != nil {
		t.Fatalf("身份不符的锚点应退回整份重传而不是报错: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("身份不符的锚点被误当成前缀：结果被拼坏（M1）: err=%v got=%d want=%d", err, len(got), len(data))
	}
}

// TestGoBackendPutResumeRefusesRewriteInWindow（I3 上传方向）：Put 开头 os.Stat 拿指纹、
// 稍后才 os.Open 本地源；同尺寸改写若发生在这个窗口里，续写会把旧前缀拼到新内容上，并把
// **错误的指纹**写进锚点。注入点 beforeOpenLocalSource 精确制造这个窗口；Put 必须在打开
// 的句柄上复核指纹并拒绝，既不提交目标，也不留下被拼坏的 .part。
func TestGoBackendPutResumeRefusesRewriteInWindow(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("resume-"), 100000) // 700000 字节
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

	// 窗口内同尺寸改写（内容变、mtime 变）。
	rewritten := bytes.Repeat([]byte("REWRITE"), 100000) // 与源同尺寸 700000
	restore := swapBeforeOpenLocalSource(t, func(p string) {
		if p != local {
			return
		}
		if err := os.WriteFile(local, rewritten, 0600); err != nil {
			t.Fatal(err)
		}
		newMT := time.Now().Add(time.Hour)
		if err := os.Chtimes(local, newMT, newMT); err != nil {
			t.Fatal(err)
		}
	})
	defer restore()

	var te *TransferError
	err := g.Put(TransferRequest{ID: "resume", Host: "h", Remote: target, Local: local, Atomic: true,
		Resume: true, PartPath: part}, nil)
	if err == nil {
		t.Fatal("本地源在判定与打开之间被改写必须拒绝本次传输，绝不能拼接旧前缀+新内容")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.PartPath != part {
		t.Fatalf("拒绝时必须如实给出保留的 .part 锚点，got %q", te.PartPath)
	}
	if _, serr := os.Stat(filepath.Join(remoteRoot, target)); !os.IsNotExist(serr) {
		t.Fatalf("拒绝续传时不得提交目标，stat err=%v", serr)
	}
}

// TestGoBackendInflightDedupIncludesUser（M3）：去重键必须包含 user。不同用户对同一 host、
// 同一目标路径是两条互不相干的传输，绝不能被误判成重复而拒绝；同用户同目标仍必须拒绝。
func TestGoBackendInflightDedupIncludesUser(t *testing.T) {
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()

	rel, err := g.acquireInflight("h", "alice", string(DirUpload), "dst.bin", "id-alice")
	if err != nil {
		t.Fatalf("首个在飞传输不应被拒绝: %v", err)
	}
	defer rel()
	if _, err := g.acquireInflight("h", "alice", string(DirUpload), "dst.bin", "id-dup"); err == nil {
		t.Fatal("同用户同目标并发必须被拒绝（去重仍要生效）")
	}
	relBob, err := g.acquireInflight("h", "bob", string(DirUpload), "dst.bin", "id-bob")
	if err != nil {
		t.Fatalf("不同用户不得被误判为重复（M3 修复点）: %v", err)
	}
	relBob()
}

// TestGoBackendGetResumeErrorKeepsPartPath（M4）：decideDownloadResume 因远端 Stat 失败而
// 报错时，本地 .part 可能仍在磁盘上 —— 错误必须带上 PartPath，否则调用方丢掉续传锚点；
// 本地确实没有 .part 时仍须为空（绝不发布假锚点）。
func TestGoBackendGetResumeErrorKeepsPartPath(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	local := filepath.Join(localDir, "dst.bin")
	part := PartName(local, "m4")
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)

	var te *TransferError
	err := g.Get(TransferRequest{ID: "m4", Host: "h", Remote: "missing.bin", Local: local, Atomic: true,
		Resume: true, PartPath: part}, nil)
	if err == nil {
		t.Fatal("远端源 Stat 失败必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T", err)
	}
	if te.PartPath != part {
		t.Fatalf("存在 .part 时错误必须带 PartPath（M4），got %q", te.PartPath)
	}

	local2 := filepath.Join(localDir, "dst2.bin")
	part2 := PartName(local2, "m4b") // 故意不创建
	err = g.Get(TransferRequest{ID: "m4b", Host: "h", Remote: "missing.bin", Local: local2, Atomic: true,
		Resume: true, PartPath: part2}, nil)
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T", err)
	}
	if te.PartPath != "" {
		t.Fatalf(".part 不存在时 PartPath 必须为空（绝不发布假锚点），got %q", te.PartPath)
	}
}

// TestGoBackendPutResumeStatErrorKeepsPartPath（M4 上传方向）：decideUploadResume 里的远端
// Stat 失败原因**不是** ENOENT（这里用「父路径是普通文件」触发），.part 很可能仍保留着，
// 错误必须带上 req.PartPath 保住锚点。
func TestGoBackendPutResumeStatErrorKeepsPartPath(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(remoteRoot, "blocker"), []byte("not-a-dir"), 0600); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)

	const part = "blocker/x.part"
	var te *TransferError
	err := g.Put(TransferRequest{ID: "m4", Host: "h", Remote: "dst.bin", Local: local, Atomic: true,
		Resume: true, PartPath: part}, nil)
	if err == nil {
		t.Fatal("远端 .part Stat 非 ENOENT 失败必须报错")
	}
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T", err)
	}
	if te.PartPath != part {
		t.Fatalf("Stat 非 ENOENT 失败时错误必须带 PartPath（保住锚点），got %q", te.PartPath)
	}
}
