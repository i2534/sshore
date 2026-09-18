package sftp

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// commitDecision 是「是否允许把 .part 提升为最终目标」的判定结果（D15）。
type commitDecision int

const (
	commitOK commitDecision = iota
	commitShortRead
)

// decideCommit：只有 done == total 才允许提交（D15）。
// 多传与少传都不是「这一个源文件的完整内容」：本地 .part 可能混入了上次的残余、
// 远端也可能在传输期间被改写，两者都必须拒绝提交而不是悄悄放行。
func decideCommit(done, total int64) commitDecision {
	if done == total {
		return commitOK
	}
	return commitShortRead
}

const progressInterval = 200 * time.Millisecond

// progressEmitter 负责节流与末帧强制（D7）。
// 每写一个 chunk 都调 send(p,false) 会淹没事件总线（pkg/sftp 的 WriteTo 按
// maxPacket 分块，大文件可达数万次）；首帧/末帧用 force=true 保证必达。
type progressEmitter struct {
	id     string
	report func(Progress)
	now    func() time.Time

	mu   sync.Mutex
	last time.Time
}

func newProgressEmitter(id string, report func(Progress)) *progressEmitter {
	if report == nil {
		report = func(Progress) {}
	}
	return &progressEmitter{id: id, report: report, now: time.Now}
}

// send 盖戳 ID 后按节流窗口决定是否上报；force=true 绕过节流（首帧/末帧）。
// last 初始为零值，所以第一帧一定放行，不需要额外特判。
func (e *progressEmitter) send(p Progress, force bool) {
	p.ID = e.id
	e.mu.Lock()
	now := e.now()
	if !force && now.Sub(e.last) < progressInterval {
		e.mu.Unlock()
		return
	}
	e.last = now
	e.mu.Unlock()
	e.report(p)
}

// copyStream 是唯一的字节搬运入口（变量而非函数，便于测试注入「中途截断的 reader」
// 或「写一半就报错的目标」来验证 done!=total 必须拒绝提交）。
// 生产实现就是 io.Copy：它优先用源的 WriteTo，sftp.File 实现了 WriteTo，
// 而 WriteTo 会为每个 chunk 调 dst.Write —— 计数 writer 仍然收到全部字节。
var copyStream = io.Copy

// countingReader 统计已发送字节（上传方向），base 是续传起点。
type countingReader struct {
	r    *os.File
	e    *progressEmitter
	p    Progress
	base int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.base += int64(n)
	c.p.Done = c.base
	c.e.send(c.p, false)
	return n, err
}

// countingWriter 统计已落盘字节，并按需上报。f 是 io.Writer（不是 *os.File）：
// 测试可以注入「写一半就失败」的目标来模拟传输中断，生产传的是 *os.File。
type countingWriter struct {
	f io.Writer
	e *progressEmitter
	p Progress
	n int64
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.f.Write(b)
	w.n += int64(n)
	w.p.Done = w.n
	w.e.send(w.p, false)
	return n, err
}

// copyFileToLocal 把远端单个文件搬到本地，返回 .part 路径与已传字节数；
// **调用方负责提交**（done==total 后 rename），失败/取消时 .part 保留（续传锚点）。
// Task 12 的目录传输复用它，避免每个文件都新建会话。
// 本地临时名一律用 PartName（filepath 家族）—— 远端 POSIX 路径才用 PartNameRemote。
// 错误发生在远端打开之前时返回 ("", 0, 0, err)：不创建任何本地文件。
func (g *GoBackend) copyFileToLocal(s *Session, req TransferRequest, remote, local string, report func(Progress)) (string, int64, int64, error) {
	st, err := s.Conn.Stat(remote)
	if err != nil {
		return "", 0, 0, err
	}
	total := st.Size()
	rf, err := s.Conn.Open(remote)
	if err != nil {
		return "", 0, 0, err
	}
	part := req.PartPath
	if part == "" {
		part = PartName(local, req.ID)
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		_ = rf.Close()
		return part, 0, total, err
	}
	em := newProgressEmitter(req.ID, report)
	cw := &countingWriter{f: f, e: em, p: Progress{Host: req.Host, Direction: DirDownload, Name: remote, PartPath: part, Total: total, Phase: PhaseTransfer}}
	em.send(cw.p, true) // 首帧：立刻让 UI 看到 0/total 与 partPath
	n, cerr := copyStream(cw, rf)
	_ = rf.Close()
	_ = f.Close()
	if cerr != nil {
		return part, n, total, cerr
	}
	if decideCommit(n, total) != commitOK {
		// 唯一提交前置在 helper 里也拦一道：任何调用方（Task 12 的目录传输）都不能把
		// 「字节数不完整」当成成功拿去 rename 提交。截断源不会报错（库对 EOF 返回 nil）。
		return part, n, total, shortReadError(n, total, part)
	}
	return part, n, total, nil
}

// getNonAtomic 是 legacy 面（req.Atomic=false，internal/sync）：直写目标，
// 不建我方 .part、不做 commit —— 原子性由 sync 自己的 .part+rename 保证。
// 完成后返回已传字节数与远端大小，调用方仍需校验 done==total。
func (g *GoBackend) getNonAtomic(s *Session, remote, local string) (int64, int64, error) {
	st, err := s.Conn.Stat(remote)
	if err != nil {
		return 0, 0, err
	}
	total := st.Size()
	rf, err := s.Conn.Open(remote)
	if err != nil {
		return 0, total, err
	}
	f, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		_ = rf.Close()
		return 0, total, err
	}
	n, cerr := copyStream(f, rf)
	_ = rf.Close()
	_ = f.Close()
	if cerr != nil {
		return n, total, cerr
	}
	return n, total, nil
}

// shortReadError 是「字节数不完整」的统一错误文案：把 done/total 与保留的
// .part 路径一并给出，便于前端展示与 Task 11 续传定位。
func shortReadError(done, total int64, part string) error {
	if part == "" {
		return fmt.Errorf("传输不完整：%d/%d 字节", done, total)
	}
	return fmt.Errorf("传输不完整：%d/%d 字节，已保留 %s", done, total, part)
}
