package sftp

import (
	"errors"
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
	// 上传方向的本地侧是**读**：本地源读失败（EIO/EISDIR…）必须带 errLocalIO 标记，
	// 否则 copyStream 的返回值会被判成远端错误、被 sshd stderr 盖掉真因（约束 6）。
	if err != nil && err != io.EOF {
		return n, wrapLocalIO(err)
	}
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
	// 下载方向的本地侧是**写**：本地目标写失败（典型 ENOSPC）必须带 errLocalIO 标记，
	// 否则会被判成远端错误、被 sshd stderr 盖掉真因（Task 7 评审遗留，本 task 收口）。
	if err != nil {
		return n, wrapLocalIO(err)
	}
	return n, nil
}

// copyFileToLocal 把远端单个文件搬到本地，返回 .part 路径与已传字节数；
// **调用方负责提交**（done==total 后 rename），失败/取消时 .part 保留（续传锚点）。
// Task 12 的目录传输复用它，避免每个文件都新建会话。
// 本地临时名一律用 PartName（filepath 家族）—— 远端 POSIX 路径才用 PartNameRemote。
// 返回值契约（Task 11/12 依赖，Task 7 评审 I2 修正）：返回的 part 非空 ⇔ .part 已真实
// 创建在磁盘上、可续传。远端 Stat/Open 失败返回 ("", 0, 0, err)；本地 OpenFile 失败
// 也返回 ("", 0, total, err)（错误里带 errLocalPart 标记），绝不给出不存在的假锚点。
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
		// 本地 .part 根本没建出来：**必须返回空 part**（Task 7 评审 I2）。
		// 原先这里返回 part，Get 据此填 TransferError.PartPath 给出一个 ENOENT 的假锚点，
		// 违反 api.go「未用到时为空」，也推翻「err!=nil 且 part 非空 ⇒ .part 已保留可续传」
		// 这条给 Task 11 的契约。错误只包一层本地标记，供 Get 判「不要附 RemoteMsg」（M4）。
		_ = rf.Close()
		return "", 0, total, fmt.Errorf("%w: %w", errLocalPart, err)
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
		// legacy 面的本地目标文件建不出来，同样是本地文件系统错误：与原子路径的 .part
		// 失败共用 errLocalPart 标记，供 Get 的 legacy 分支判「不要附远端 stderr」（M4）。
		_ = rf.Close()
		return 0, total, fmt.Errorf("%w: %w", errLocalPart, err)
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

// errLocalPart 标记「本地 .part 建不出来」这一类**本地**文件系统错误（Task 7 评审 M4）。
// 它经 errors.Is 穿透 copyFileToLocal 的返回值，让 Get 只对真正的远端失败附 RemoteMsg：
// 否则生产上 sshd stderr 只要非空，api.go 的 Error() 就会优先打印 RemoteMsg，
// 把「本地磁盘/权限问题」这个真正原因盖掉。
var errLocalPart = errors.New("本地 .part 创建失败")

// errLocalIO 标记「字节搬运过程中本地一侧的读写失败」（Task 8 约束 6 收口 Task 7 残留）。
// 两个方向都可能中招：下载是本地写失败（典型 ENOSPC），上传是本地源读失败（EIO/EISDIR…）。
// countingWriter/countingReader 在把错误往外交之前包上它，isRemoteError 因此能把这类
// 失败判成「本地」，不附远端 stderr —— 否则生产上 stderr 非空就会盖掉真正的本地原因。
var errLocalIO = errors.New("本地文件读写失败")

// wrapLocalIO 给本地侧 IO 错误统一加盖 errLocalIO 标记（保留原始错误链，errors.Is 可穿透）。
func wrapLocalIO(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errLocalIO, err)
}

// isLocalError 判「这个错误确定由本地文件系统造成」。
// errLocalPart（.part 建不出来）与 errLocalIO（搬运中的本地读写失败）都算。
func isLocalError(err error) bool {
	return err != nil && (errors.Is(err, errLocalPart) || errors.Is(err, errLocalIO))
}

// isRemoteError 判「这个错误该不该带远端 stderr 原文」。
// 本地文件系统错误绝不带 RemoteMsg：它不是远端的锅。
func isRemoteError(err error) bool {
	return err != nil && !isLocalError(err)
}
