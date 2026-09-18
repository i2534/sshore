package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
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
	part := req.PartPath
	if part == "" {
		part = PartName(local, req.ID)
	}
	// 续传（Task 11）：req.Resume + req.ResumeOffset 由 Get 的 decideResume 填好。
	// I2/I3：判定用的 Stat 与真正打开读取之间存在窗口 —— 先过注入点（仅续传时）。
	resume := req.Resume && req.ResumeOffset > 0
	offset := int64(0)
	if resume {
		offset = req.ResumeOffset
		beforeOpenResumePart(part, remote, offset)
	}
	rf, err := s.Conn.Open(remote)
	if err != nil {
		return "", 0, 0, err
	}
	// I3：指纹校验与数据读取绑定到**同一个已打开句柄**。下面的 fst 既是续传复核的依据，
	// 也是随后记录锚点的依据；判定阶段那次 Stat 只用于决定「要不要尝试续传」。
	fst, err := rf.Stat()
	if err != nil {
		_ = rf.Close()
		return "", 0, 0, err
	}
	total := fst.Size()
	if resume {
		if !g.resumeSourceUnchanged(downloadAnchorKey(req.Host, req.User, part), remote, fst.Size(), fst.ModTime()) {
			// 源在判定与打开之间被改写（或锚点不可验证/身份不符）：绝不追加 —— 退回整份重传。
			g.forgetResumeAnchor(downloadAnchorKey(req.Host, req.User, part))
			resume, offset = false, 0
		}
	}
	// 续写绝不能 O_TRUNC —— 那会把上一次留下的前缀清零，随后 Seek(offset) 写出「前 offset
	// 字节是 0」的静默损坏文件（事实 5）；只有整份重传才 O_TRUNC。远端也必须 Seek 到同一
	// offset，否则会把源的第 0 字节写到本地 offset 处。
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if resume {
		flags = os.O_WRONLY | os.O_CREATE
	}
	f, err := os.OpenFile(part, flags, 0600)
	if err != nil {
		// 本地 .part 根本没建出来：**必须返回空 part**（Task 7 评审 I2）。
		// 原先这里返回 part，Get 据此填 TransferError.PartPath 给出一个 ENOENT 的假锚点，
		// 违反 api.go「未用到时为空」，也推翻「err!=nil 且 part 非空 ⇒ .part 已保留可续传」
		// 这条给 Task 11 的契约。错误只包一层本地标记，供 Get 判「不要附 RemoteMsg」（M4）。
		_ = rf.Close()
		return "", 0, total, fmt.Errorf("%w: %w", errLocalPart, err)
	}
	if resume {
		// I2：打开后立刻按句柄自身的实际长度复核 offset —— 判定与打开之间的窗口里
		// .part 可能被截断/追加，必须在这里兜住，绝不 Seek 越过 EOF。
		ok, aerr := alignLocalPartHandle(f, offset)
		if aerr != nil {
			_ = rf.Close()
			_ = f.Close()
			return part, 0, total, wrapLocalIO(aerr)
		}
		if !ok {
			// .part 缩水/消失：Truncate(offset) 会把缺口补成零洞，绝不能做 —— 退回整份重传。
			if terr := f.Truncate(0); terr != nil {
				_ = rf.Close()
				_ = f.Close()
				return part, 0, total, wrapLocalIO(terr)
			}
			resume, offset = false, 0
		}
	}
	if offset > 0 {
		if _, serr := rf.Seek(offset, io.SeekStart); serr != nil {
			_ = rf.Close()
			_ = f.Close()
			return part, 0, total, serr
		}
		if _, serr := f.Seek(offset, io.SeekStart); serr != nil {
			_ = rf.Close()
			_ = f.Close()
			return part, 0, total, wrapLocalIO(serr)
		}
	}
	// 记录源指纹（Task 11）：这份 .part 的已落盘字节对应**已打开句柄**看到的远端源
	// (身份, size, mtime)。续传请求带回同一个 PartPath 时用它验证「还是当初那份源吗」；
	// 用句柄 Stat 而非判定时的 Stat，保证锚点与真正读到的字节出自同一份元数据（I3）。
	g.recordResumeAnchor(downloadAnchorKey(req.Host, req.User, part), remote, total, fst.ModTime())
	em := newProgressEmitter(req.ID, report)
	// 首帧 Done 从续传起点起算，UI 不会在续传时先看到 0/total。
	cw := &countingWriter{f: f, e: em, p: Progress{Host: req.Host, Direction: DirDownload, Name: remote, PartPath: part, Done: offset, Total: total, Phase: PhaseTransfer}, n: offset}
	em.send(cw.p, true) // 首帧：立刻让 UI 看到 offset/total 与 partPath
	_, cerr := copyStream(cw, rf)
	_ = rf.Close()
	_ = f.Close()
	done := cw.n
	if cerr != nil {
		return part, done, total, cerr
	}
	if decideCommit(done, total) != commitOK {
		// 唯一提交前置在 helper 里也拦一道：任何调用方（Task 12 的目录传输）都不能把
		// 「字节数不完整」当成成功拿去 rename 提交。截断源不会报错（库对 EOF 返回 nil）。
		return part, done, total, shortReadError(done, total, part)
	}
	return part, done, total, nil
}

// beforeOpenResumePart 是 I2/I3 的确定性注入点：在「续传判定已完成、但本地 .part 与远端源
// 尚未被真正打开读取」的窗口里被调用（part=本地 .part 绝对路径，remote=远端源路径，
// offset=判定出的续传起点）。生产实现是 no-op；单测用它模拟另一进程在这个窗口里
// 截断/追加 .part，或同尺寸改写远端源。做成变量与 copyStream/posixRename 同思路。
var beforeOpenResumePart = func(part, remote string, offset int64) {}

// localPartIfExists 返回确实存在于磁盘上的本地 .part 路径；不存在（或为空）返回空串。
// 用于错误分支如实上报锚点（M4）：判定阶段远端源可能连 Stat 都失败，但本地 .part 可能
// 仍然保留着，调用方不该因此丢掉续传锚点。
func localPartIfExists(part string) string {
	if part == "" {
		return ""
	}
	if st, err := os.Stat(part); err == nil && st.Mode().IsRegular() {
		return part
	}
	return ""
}

// alignLocalPartHandle 在**已打开**的本地 .part 句柄上复核实际长度是否等于续传 offset（I2）
//   - == offset：可续；
//   - >  offset：Truncate(offset) 丢弃陈旧尾部（上次运行遗留的字节）；
//   - <  offset（含 0）：返回 false，调用方退回整份重传 —— 绝不 Truncate(offset) 把缺口
//     补成零洞，也绝不 Seek 越过 EOF（同样会零填充出洞，而 done==total 仍会提交）。
func alignLocalPartHandle(f *os.File, offset int64) (bool, error) {
	if offset <= 0 {
		return true, nil
	}
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	switch {
	case st.Size() == offset:
		return true, nil
	case st.Size() > offset:
		if err := f.Truncate(offset); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
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

// —— Task 12：目录传输（枚举 + 阈值降级 + 聚合进度）——

const (
	// scanMaxFiles / scanMaxElapsed 是目录枚举的降级阈值（D16，本轮定值）：
	// 已枚举文件数 > 20000 或枚举耗时 > 5s ⇒ 放弃继续枚举，转不定进度
	// （Total=-1 && FilesTotal=-1 && Phase=transfer）。具名常量便于按实测调整。
	scanMaxFiles   = 20000
	scanMaxElapsed = 5 * time.Second
)

// scanLimitReached 是 D16 的纯判定：文件数超过 20000 或耗时超过 5s 就该降级。
func scanLimitReached(files int, elapsed time.Duration) bool {
	return files > scanMaxFiles || elapsed > scanMaxElapsed
}

// scanLimit 是 scanTree 实际使用的阈值判定（注入点，与 copyStream/posixRename 同思路）。
// 生产实现就是 scanLimitReached；单测替换它就能在不造 2 万个文件的前提下真正跑通
// 「枚举中途降级」这条路径 —— 否则降级只有纯函数测试，集成路径是假覆盖。
var scanLimit = scanLimitReached

// scanCheckpoint 是「扫描阶段取消」的确定性注入点（与 copyStream/scanLimit 同思路）：
// 生产实现是 no-op；单测把它替换成「卡住直到放行」，把扫描相取消变成确定性 —— 不需要
// 造一个恰好很慢的枚举。scanTree 每批、scanLocalTree 每步都会调用它（Task 12 修复轮 1 / I1）。
var scanCheckpoint = func() {}

// degradedProgress 返回降级后的进度载荷：Total=-1（不定进度条）且 FilesTotal=-1
// （UI 显示「已完成 N 个文件」而不是假装有分母）。Phase 必须是 transfer —— 降级只放弃
// 枚举，不放弃传输。
func degradedProgress(id, host string, d Direction, name string) Progress {
	return Progress{ID: id, Host: host, Direction: d, Name: name, Total: -1, FilesTotal: -1, Phase: PhaseTransfer}
}

// treeFile 是目录枚举出的一个待传文件。rel 是相对根目录的 **POSIX** 相对路径：
// 远端分隔符永远是 /，拼远端路径必须用 path.Join；本地侧再由 filepath.FromSlash 转换。
type treeFile struct {
	rel  string
	size int64
}

// scanTree 用 ReadDirContext（库唯一 ctx 感知 API）递归枚举远端目录树。
// 返回 files（非目录项）与 subdirs（**子目录**的 POSIX 相对路径）——后者供调用方按 D11
// 建出目录本身（含空目录，sftp get -r 同样会建出空目录）。
//
//   - 每处理完一个目录（一批）就检查 ctx：取消/超时 ⇒ 返回 ctx.Err()，只放弃枚举、不关会话（D16）；
//   - 每批之后检查 scanLimit：命中 ⇒ 返回 degraded=true 与**已枚举到的**内容；
//   - 单批用剩余预算的 deadline 调 ReadDirContext（D16「5s = 停止发起下一批」的落地）。
//
// 我方临时/备份文件（PartMarker 中缀）是内部产物，一律不进业务清单（D18）——否则上一次
// 中断留下的 .part 会被当成真实文件再传一遍。
func scanTree(ctx context.Context, s *Session, root string, start time.Time) (files []treeFile, subdirs []string, degraded bool, err error) {
	queue := []string{""}
	for len(queue) > 0 {
		// I1：扫描相取消的确定性注入点（先钩子、后 ctx 检查）—— 单测在这里卡住枚举、
		// 调 Cancel(id)（触发 cancel），放行后 ctx.Err() 立刻中止扫描，绝不发起下一批。
		scanCheckpoint()
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, false, cerr
		}
		rel := queue[0]
		queue = queue[1:]
		remoteDir := root
		if rel != "" {
			remoteDir = path.Join(root, rel)
		}
		remaining := scanMaxElapsed - time.Since(start)
		if remaining <= 0 {
			return files, subdirs, true, nil
		}
		bctx, cancel := context.WithTimeout(ctx, remaining)
		infos, rerr := s.Conn.ReadDirContext(bctx, remoteDir)
		budgetHit := bctx.Err() != nil
		cancel()
		if rerr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, nil, false, cerr
			}
			if budgetHit {
				// 单批就吃光预算：与「超 5s」同一降级语义（D16）。
				return files, subdirs, true, nil
			}
			return nil, nil, false, rerr
		}
		for _, fi := range infos {
			name := fi.Name()
			if IsInternalTemp(name) {
				continue
			}
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if fi.IsDir() {
				subdirs = append(subdirs, childRel)
				queue = append(queue, childRel)
				continue
			}
			// 非目录项当文件处理：OpenSSH readdir 用 lstat，目录软链 IsDir()==false 因此不会
			// 被递归（spec：目录软链不跟随）；文件软链交给 Open/Stat 跟随，与现状一致。
			files = append(files, treeFile{rel: childRel, size: fi.Size()})
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, false, cerr
		}
		if scanLimit(len(files), time.Since(start)) {
			return files, subdirs, true, nil
		}
	}
	return files, subdirs, false, nil
}

// treeProgress 聚合目录传输的进度帧（D7/D16）。
//
// Done 是跨文件累计的真实字节（已完成文件的字节 + 当前文件已传字节），FilesDone 是已提交
// 文件数；降级（枚举被放弃）时 total/files 为 -1，Done/FilesDone 仍真实累加 —— 这正是
// D16 要求的字段闭环：UI 能区分「分母未知但确实在传」与「0 个文件」。
type treeProgress struct {
	id     string
	host   string
	dir    Direction
	name   string
	total  int64  // <0 表示未知（降级）
	files  int    // <0 表示未知（降级）
	base   int64  // 已提交文件的字节总数
	infl   int64  // 当前文件已传字节（来自逐文件帧）
	doneF  int    // 已提交文件数
	part   string // 失败路径回填的重试锚点（失败末帧的 PartPath）；正常进度为空
	report func(Progress)
	now    func() time.Time
	last   time.Time
}

func newTreeProgress(id, host string, d Direction, name string, total int64, files int, report func(Progress)) *treeProgress {
	return &treeProgress{id: id, host: host, dir: d, name: name, total: total, files: files, report: report, now: time.Now}
}

// frame 按节流窗口上报一帧聚合进度；force=true 绕过节流（首帧/末帧必达，D7）。
func (t *treeProgress) frame(force bool) {
	if t.report == nil {
		return
	}
	if !force && t.now().Sub(t.last) < progressInterval {
		return
	}
	t.last = t.now()
	// M2：分母来自枚举、分子来自**已打开句柄**的 Stat，两者在「枚举与打开之间源被改写」
	// 时可能不一致（分子会超过分母）。这里显式把两个分子夹到各自分母内，保证进度帧永不
	// 出现 Done>Total / FilesDone>FilesTotal 的自相矛盾。
	done := t.base + t.infl
	if t.total >= 0 && done > t.total {
		done = t.total
	}
	filesDone := t.doneF
	if t.files >= 0 && filesDone > t.files {
		filesDone = t.files
	}
	t.report(Progress{
		ID: t.id, Host: t.host, Direction: t.dir, Name: t.name, PartPath: t.part,
		Done: done, Total: t.total,
		FilesDone: filesDone, FilesTotal: t.files, Phase: PhaseTransfer,
	})
}

// begin 发首帧：立刻让 UI 拿到分母（或降级的 -1/-1）与 Phase=transfer。
// 降级帧形状由 degradedProgress 单一来源构造，避免两处各写一份 -1 字段而漂移。
func (t *treeProgress) begin() {
	if t.report == nil {
		return
	}
	if t.total < 0 || t.files < 0 {
		t.last = t.now()
		t.report(degradedProgress(t.id, t.host, t.dir, t.name))
		return
	}
	t.frame(true)
}

// file 接收某个文件内部的逐块进度：Done 折算成「已完成文件字节 + 本文件已传字节」。
func (t *treeProgress) file(p Progress) {
	t.infl = p.Done
	t.frame(false)
}

// finishFile 在一个文件**提交成功后**推进累计值。
func (t *treeProgress) finishFile(size int64) {
	t.base += size
	t.infl = 0
	t.doneF++
}

// fail 在失败/取消路径上补发一帧末帧，并返回「已提交/剩余」计数（Task 12 修复轮 1 / I4）。
// 失败项自身的在飞字节（t.infl）不计入 Done —— 只有**提交成功**的文件才配上调分子，否则
// UI 会把半截文件算成已传。失败末帧的 PartPath 填本次保留的重试锚点（part 非空时），
// 调用方据此定位可续传的 .part。RemainingFiles<0 表示枚举已降级（分母未知）。
func (t *treeProgress) fail(part string) (committedFiles int, committedBytes int64, remaining int) {
	t.part = part
	t.infl = 0
	t.frame(true)
	remaining = -1
	if t.files >= 0 {
		remaining = t.files - t.doneF
		if remaining < 0 {
			remaining = 0
		}
	}
	return t.doneF, t.base, remaining
}
