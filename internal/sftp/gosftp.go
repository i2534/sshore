package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// 注入点（与 Task 7 的 ioCopy 同思路）：让 hermetic 单测能在不联网、不起真实 ssh 的
// 前提下断言 dial 构造的 ssh 参数与错误路径。生产路径一律走 osutil.StartPipes /
// sftp.NewClientPipe。
var (
	startSFTPPipes = osutil.StartPipes

	newSFTPClientPipe = func(rd io.Reader, wr io.WriteCloser) (*sftp.Client, error) {
		return sftp.NewClientPipe(rd, wr)
	}
)

// posixRenameExt 是 OpenSSH 的原子覆盖扩展名（spec §2.3）。
const posixRenameExt = "posix-rename@openssh.com"

// posixRename 是远端覆盖提交原语的注入点。Task 0 只在极少样本上观测到它能覆盖
// （6 次运行、零次独立失败），单测必须能分别钉住「成功 → 直接提交」与
// 「返回非 nil → 必须回退 backup-swap」两条分支（约束 3），故做成变量。
var posixRename = func(c *sftp.Client, oldname, newname string) error {
	return c.PosixRename(oldname, newname)
}

// renameRemote / removeRemote 是 backup-swap 两条普通 rename 与 bak 清理的注入点
// （与 posixRename 同思路）。做成变量有两个必要用途：
//  1. 单测能在「破坏性改名动手之前」观测 journal 是否已落盘（C1 顺序不变量）；
//  2. 单测能稳定构造非 ENOENT 的 rename 失败与回滚失败，不必依赖服务端对 rename 的
//     具体 errno（I4/I3）。
var renameRemote = func(c *sftp.Client, oldname, newname string) error {
	return c.Rename(oldname, newname)
}

var removeRemote = func(c *sftp.Client, path string) error {
	return c.Remove(path)
}

// GoBackend 是基于 pkg/sftp 的新传输后端（spec D1）。
// 技术审核 S5：Go 没有 partial struct —— 后续 task 新增字段必须回来改**这一处**声明。
// 这里一次性声明全部字段，后续 task 只实现方法，避免每个 task 都要改结构体。
type GoBackend struct {
	pool *Pool
	emit forward.EmitFunc
	sel  TransportSelector

	journalDir string       // Task 8：journal 目录（app 用 stateDir() 注入）
	journal    *swapJournal // Task 8：backup-swap 崩溃恢复

	regMu sync.Mutex // Task 10：id → 会话（取消用）
	reg   map[string]*Session

	inflightMu sync.Mutex // Task 11：同目标去重
	inflight   map[string]string

	partsMu    sync.Mutex           // Task 13：已知 .part（退出清理用）
	knownParts map[string][2]string // id → {local, remote}
}

func NewGoBackend(sel TransportSelector, emit forward.EmitFunc) *GoBackend {
	g := &GoBackend{
		emit: emit,
		sel:  sel,
		// Task 6 评审 M2：这些 map 到 Task 10/11/13 才被写入。构造时就初始化，
		// 后续 task 直接写字段（g.reg[id] = s 等）不会 panic: assignment to entry in nil map。
		reg:        map[string]*Session{},
		inflight:   map[string]string{},
		knownParts: map[string][2]string{},
	}
	g.pool = NewPool(g.dial)
	return g
}

// sftpDialArgs 构造 ssh 的参数（纯函数，便于单测逐字钉顺序）。
func sftpDialArgs(host, user string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	// Task 0 实测：-s 前置/后置在 Win32-OpenSSH 9.5p1 都可用；采用前置（不依赖 getopt 置换）。
	// 另注（Task 0 的 Session 0 现象）：本函数必须在交互桌面会话里运行 ——
	// Session 0 中 spawn 的 ssh.exe 会卡在 SFTP INIT 之后（裸 ssh 命令同样卡），属环境限制。
	args = append(args, "-s", host, "sftp")
	return args
}

// dial 起 ssh -s sftp，把 stdin/stdout 交给 NewClientPipe。
// stderr 由 osutil.StartPipes 后台 drain，错误上报读 PipedProcess.StderrText()。
func (g *GoBackend) dial(host, user string) (*Session, error) {
	pp, err := startSFTPPipes("ssh", sftpDialArgs(host, user)...)
	if err != nil {
		return nil, err
	}
	cl, err := newSFTPClientPipe(pp.Stdout, pp.Stdin)
	if err != nil {
		// 绝不直接读 Proc.Stderr：会与 primitive 的 drain goroutine 竞争；StderrText 是唯一来源。
		msg := pp.StderrText()
		_ = pp.Close()
		if strings.Contains(msg, "subsystem request failed") {
			return nil, fmt.Errorf("远端未启用 sftp 子系统（检查 sshd_config 的 Subsystem sftp）: %s", msg)
		}
		if msg == "" {
			// dial 失败时子进程可能尚未退出，StderrText 仍为空 —— 报通用错误，
			// 绝不阻塞等待 stderr（那会把失败变成挂起）。
			return nil, fmt.Errorf("建立 SFTP 会话失败: %v", err)
		}
		return nil, fmt.Errorf("建立 SFTP 会话失败: %v (%s)", err, msg)
	}
	return &Session{Host: host, User: user, Conn: cl, Proc: pp, state: sessBusy}, nil
}

// Capabilities 通过会话池真实建一次会话，探测远端 posix-rename 扩展。
func (g *GoBackend) Capabilities(host, user string) (bool, error) {
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return false, err
	}
	if s.Conn == nil {
		g.pool.Release(s, false)
		return false, errors.New("会话没有 SFTP 连接")
	}
	_, ok := s.Conn.HasExtension(posixRenameExt)
	g.pool.Release(s, true)
	return ok, nil
}

// SetJournalDir 注入 backup-swap 崩溃恢复日志目录（app 装配时用 stateDir()）。
// dir=="" 表示不做崩溃恢复（journal=nil）：commitRemote 仍会 backup-swap + 回滚，
// 只是不落 journal。由 Ctrl.SetJournalDir 在 GoBackend 懒构造前后统一转发。
func (g *GoBackend) SetJournalDir(dir string) {
	g.journalDir = dir
	if dir == "" {
		g.journal = nil
		return
	}
	g.journal = newSwapJournal(dir)
}

// warnf 发一条 warn 级 SFTP 日志（SourceType=sftp, SourceID=host）。emit 为 nil（测试/
// 未接线）时静默，与 BatchBackend.logEvent 一致。commitRemote 用它上报「已提交但清理
// 失败」这类不改变成功结论、却必须可观测的异常（I3 修复轮 1）。
func (g *GoBackend) warnf(host, format string, args ...any) {
	if g.emit == nil {
		return
	}
	g.emit(forward.Event{
		SourceType: "sftp",
		SourceID:   host,
		TS:         time.Now().Format(time.RFC3339),
		Level:      "warn",
		Message:    fmt.Sprintf(format, args...),
	})
}

// —— Task 7-13 才实现的正文；本 task 只落引导与能力探测（自审 S7）——

func (g *GoBackend) Home(host, user string) (string, error) { return "", errors.New("未实现") }
func (g *GoBackend) List(host, user, path string) ([]Item, error) {
	return nil, errors.New("未实现")
}
func (g *GoBackend) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	return nil, errors.New("未实现")
}

// Get/GetTree/Put/PutTree 是 GoBackend 的正文方法（后续 task 填充）。
// 接口方法 Transfer* 是薄适配层 —— 门面通过 Backend 接口只看到 Transfer*，
// 与 BatchBackend（legacy 四参 Get/Put 保留、Transfer* 包一层）方向相反。
// Get 下载远端单个文件（spec D15：.part 原子提交）。
//
// 提交前置只有一个：done == total。pkg/sftp 的 WriteTo/ReadFrom 对 EOF 返回 (n, nil)，
// 「源被截断」不会报错 —— 必须靠字节数兜住（技术审核 R13），否则半截文件会被改名成最终名。
//
// 失败与取消一律保留 .part（Task 11 的续传锚点），只有 done==total 才 os.Rename 提交；
// os.Rename 在 Linux 与 Windows 上都覆盖已存在目标。
//
// 取消由调用方关会话完成（库无逐请求 ctx，spec §2.3）：关会话会让 copyStream 立刻返回错误，
// 此时 .part 及其已落盘字节保留，reuse=false 走会话关闭路径。
func (g *GoBackend) Get(req TransferRequest, report func(Progress)) error {
	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()

	if !req.Atomic {
		// legacy 面（internal/sync）：直写目标，原子性由 sync 自己的 .part+rename 保证。
		n, total, err := g.getNonAtomic(s, req.Remote, req.Local)
		if err != nil {
			// 错误归属（M4）：legacy 分支与原子路径同一判据 —— 只有真正的远端失败才附
			// s.Proc.StderrText()。本地目标文件建不出来（errLocalPart）保留自身 error，
			// 否则 sshd stderr 非空就会在 api.go 的 Error() 里盖掉本地权限/磁盘真因。
			var remoteMsg string
			if isRemoteError(err) {
				remoteMsg = s.Proc.StderrText()
			}
			return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: remoteMsg}
		}
		if decideCommit(n, total) != commitOK {
			return &TransferError{Op: "sftp get", Path: req.Local, Err: shortReadError(n, total, "")}
		}
		reuse = true
		return nil
	}

	// 远端 → 本地 .part（Task 12 的目录传输复用同一个 helper，避免逐文件重建会话）。
	// helper 内部已执行唯一提交前置 done==total：远端被截断（库对 EOF 返回 nil）时
	// 返回 shortReadError，绝不能把半截文件改名成最终名（R13）。
	part, _, total, err := g.copyFileToLocal(s, req, req.Remote, req.Local, report)
	if err != nil {
		// 错误归属（Task 7 评审 M4）：只有**真正的远端失败**才附 s.Proc.StderrText()。
		// 本地文件系统错误（如 .part 建不出来）保留自己的 error —— 否则 api.go 的 Error()
		// 会优先打印 RemoteMsg，生产上 stderr 非空就把本地原因盖掉了。
		var remoteMsg string
		if isRemoteError(err) {
			remoteMsg = s.Proc.StderrText()
		}
		// part 非空 ⇔ .part 已真实存在（helper 契约）：失败/取消保留它作 Task 11 续传锚点。
		// part 为空且错误是远端 Stat/Open → 没建任何本地文件；错误是本地 OpenFile 失败 →
		// 同样不给 PartPath，绝不发布一个 ENOENT 的假锚点（评审 I2）。
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, PartPath: part, Err: err, RemoteMsg: remoteMsg}
	}
	if err := os.Rename(part, req.Local); err != nil {
		return &TransferError{Op: "sftp get", Path: req.Local, PartPath: part, Err: err}
	}
	// 末帧强制：终值必须达（D7）。done==total 已成立，补发带完整计数的终态。
	// 时序：**rename 成功之后**才发，保证「末帧 = 提交完成」而不是「字节到齐」——否则
	// rename 失败时 UI 已经收下 done==total 的成功终态，随后却拿到错误。
	// PartPath 语义（Task 7 评审 M1）：这里填**已提交的最终目标路径**，因为发出的这一帧
	// 描述的是「传输已结束、内容在 req.Local」；绝不填已经被 rename 掉的旧 .part，
	// 那是探针实测 STALE 的假路径。失败路径上 PartPath 仍只是保留的 .part（可续传）。
	newProgressEmitter(req.ID, report).send(Progress{
		Host: req.Host, Direction: DirDownload, Name: req.Remote, PartPath: req.Local,
		Done: total, Total: total, Phase: PhaseTransfer,
	}, true)
	reuse = true
	return nil
}
func (g *GoBackend) GetTree(req TransferRequest, report func(Progress)) error {
	return errors.New("未实现")
}

// Put 上传本地单个文件到远端（新面：.part + 原子提交）。
//
// 提交前置同样只有 done == total：短传/截断绝不提交（R13/约束 4）。远端临时名一律用
// Remote 家族（path.Dir）—— Windows 客户端的 filepath 会把远端 / 变成 \，临时文件会落到
// 别的目录（Task 2 评审 Important-2）。失败/取消保留远端 .part 作为 Task 11 续传锚点。
//
// 续传（Task 11 负责 offset 判定与去重）：req.Resume 时远端 .part 用 O_WRONLY
// （**严禁 O_TRUNC**）并 Seek(offset)，**本地源也必须 Seek 到同一 offset** ——
// 只 Seek 远端而本地从 0 读，会把源的第 0 字节写到远端 offset 处，拼出损坏文件。
func (g *GoBackend) Put(req TransferRequest, report func(Progress)) error {
	st, err := os.Stat(req.Local)
	if err != nil {
		// 本地源不存在/不可读：在建会话之前失败，且是本地错误（不附远端 stderr）。
		return &TransferError{Op: "sftp put", Path: req.Local, Err: err}
	}
	total := st.Size()

	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()

	if !req.Atomic {
		// legacy 面（internal/sync / app.SftpPut）：直写目标，不建我方 .part、不提交、不登记 journal。
		if err := g.putNonAtomic(s, req, total, report); err != nil {
			return err
		}
		reuse = true
		return nil
	}

	// 能力探测：HasExtension 返回 (string, bool)（spec §2.3）。
	_, hasPosix := s.Conn.HasExtension(posixRenameExt)
	part := req.PartPath
	if part == "" {
		part = PartNameRemote(req.Remote, req.ID) // 远端 POSIX 路径：必须用 Remote 家族（Task 2 评审 Important-2）
	}
	// 新建：O_WRONLY|O_CREATE|O_TRUNC；续传（Task 11）：O_WRONLY（严禁 O_TRUNC）+ Seek(partSize)。
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	var offset int64
	if req.Resume {
		flags = os.O_WRONLY
		offset = req.ResumeOffset
	}
	wf, err := s.Conn.OpenFile(part, flags)
	if err != nil {
		// .part 根本没建出来 ⇒ PartPath 必须为空（约束 5：绝不发布假锚点）。
		return &TransferError{Op: "sftp put", Host: req.Host, Path: part, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	if req.Resume && offset > 0 {
		if _, err := wf.Seek(offset, io.SeekStart); err != nil {
			_ = wf.Close()
			return &TransferError{Op: "sftp put", Host: req.Host, Path: part, Err: err, RemoteMsg: s.Proc.StderrText()}
		}
	}
	lf, err := os.Open(req.Local)
	if err != nil {
		_ = wf.Close()
		// 本地源打不开：远端 .part 已建出、按约束 4 保留可续传，但不附远端 stderr（本地错误）。
		return &TransferError{Op: "sftp put", Path: req.Local, PartPath: part, Err: wrapLocalIO(err)}
	}
	if req.Resume && offset > 0 {
		// 上传续传：本地源与远端 .part 必须从同一 offset 续写（见上）。offset 的选择由
		// Task 11 的 decideResume 负责；这里只保证两侧对称，避免拼出损坏文件。
		if _, err := lf.Seek(offset, io.SeekStart); err != nil {
			_ = wf.Close()
			_ = lf.Close()
			return &TransferError{Op: "sftp put", Path: req.Local, PartPath: part, Err: wrapLocalIO(err)}
		}
	}
	em := newProgressEmitter(req.ID, report)
	// 首帧 Done 从续传起点起算，UI 不会在续传时先看到 0。
	cr := &countingReader{r: lf, e: em, p: Progress{Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: part, Done: offset, Total: total, Phase: PhaseTransfer}, base: offset}
	em.send(cr.p, true)
	n, cerr := copyStream(wf, cr)
	_ = wf.Close()
	_ = lf.Close()
	if cerr != nil {
		return putCopyError(req, part, cerr, s.Proc.StderrText())
	}
	if decideCommit(n+offset, total) != commitOK {
		return &TransferError{Op: "sftp put", Path: req.Remote, PartPath: part, Err: shortReadError(n+offset, total, part)}
	}
	if err := commitRemote(s, hasPosix, part, req.Remote, g.journal, func(f string, a ...any) { g.warnf(s.Host, f, a...) }); err != nil {
		// 提交失败：只有真正的远端失败才附 stderr；.part 仍在（提交没成功）⇒ 如实给出锚点。
		var remoteMsg string
		if isRemoteError(err) {
			remoteMsg = s.Proc.StderrText()
		}
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, PartPath: part, Err: err, RemoteMsg: remoteMsg}
	}
	// 末帧强制且只有在提交成功之后才发：末帧 = 提交完成（约束 6 的 PartPath 语义）。
	// PartPath 必须指向**已提交的最终目标**，绝不能填那个已被 rename 掉、不再存在的旧 .part。
	em.send(Progress{
		Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: req.Remote,
		Done: total, Total: total, Phase: PhaseTransfer,
	}, true)
	reuse = true
	return nil
}

// putCopyError 给搬运阶段（copyStream）的错误定性：本地源读失败不附远端 stderr（约束 6），
// 远端写失败才附。PartPath 在两条分支上都有值 —— 远端 .part 此时已真实建立。
func putCopyError(req TransferRequest, part string, err error, stderr string) *TransferError {
	if isRemoteError(err) {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, PartPath: part, Err: err, RemoteMsg: stderr}
	}
	return &TransferError{Op: "sftp put", Path: req.Local, PartPath: part, Err: err}
}

// putNonAtomic 是 legacy 面（req.Atomic=false）：直写远端目标，不建我方 .part、
// 不做 PosixRename/backup-swap、不登记 journal —— 原子性由调用方（internal/sync 自己的
// .part+rename）保证。与 Get 的 legacy 分支对称，错误归属同样用 isRemoteError 判据。
// 先开本地源再开远端目标：本地源打不开时绝不先把远端目标截断。
func (g *GoBackend) putNonAtomic(s *Session, req TransferRequest, total int64, report func(Progress)) error {
	lf, err := os.Open(req.Local)
	if err != nil {
		return &TransferError{Op: "sftp put", Path: req.Local, Err: wrapLocalIO(err)}
	}
	defer func() { _ = lf.Close() }()
	wf, err := s.Conn.OpenFile(req.Remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	defer func() { _ = wf.Close() }()

	em := newProgressEmitter(req.ID, report)
	cr := &countingReader{r: lf, e: em, p: Progress{Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: req.Remote, Total: total, Phase: PhaseTransfer}}
	em.send(cr.p, true)
	n, cerr := copyStream(wf, cr)
	if cerr != nil {
		// legacy 面没有我方 .part：PartPath 必须为空（Task 7 同款契约，绝不把直写目标当锚点）。
		return putCopyError(req, "", cerr, s.Proc.StderrText())
	}
	if decideCommit(n, total) != commitOK {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: shortReadError(n, total, "")}
	}
	em.send(Progress{
		Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: req.Remote,
		Done: total, Total: total, Phase: PhaseTransfer,
	}, true)
	return nil
}

// commitRemote 把已完整落盘的远端 .part 提交成 target。
//
// 有 posix-rename 且真的成功 → 原子覆盖；否则（扩展缺失 / PosixRename 返回任何非 nil）
// 一律回退 backup-swap（约束 3：Task 0 只在 6 次运行、零次独立失败上观测到覆盖成功，
// 绝不把「扩展可用」当「覆盖必成」）。
//
// backup-swap 的顺序（C1 修复轮 1 重排）：
//  1. 先把 (target,bak,part) 三元组写进 journal —— **intent 必须先于任何破坏性改名落盘**。
//     旧顺序（先 target→bak 再 Begin）中间的崩溃会留下「target 名已消失、journal 仍空」，
//     旧内容只剩在随机 bak 名里，恢复侧无法从 bak 反推 target（长路径退化名更只剩目录）。
//  2. Rename(target,bak)；3. Rename(part,target)；4. Done 并删 bak。
//
// 只有「目标本来就不存在」的 ENOENT 才走直接提交；其它错误（权限/被占用）必须上报。
//
// 错误策略（I3 修复轮 1，逐条明确）：
//   - journal.Begin 失败 → 立刻返回，**绝不动 target**（否则进入无 journal 保护的破坏窗口）。
//   - Rename(target,bak) 非 ENOENT 失败 → 原样返回该错误；顺手尽力清掉刚写的 intent，
//     清不掉只 warn（主错误照报）。
//   - Rename(part,target) 失败 → 回滚 bak→target：回滚成功则**清条目**（I2）并返回原错误；
//     回滚失败则**保留条目**并把回滚错误一并返回（供下次启动按 journal 恢复）。
//   - 提交已成功后的清理失败（Done / Remove(bak)）**不改变「提交成功」这一事实**，
//     只发 warn 日志：这里返回错误会让 Put 报失败，并发布一个已被 rename 掉、不存在的
//     PartPath（假锚点）。warn 不是静默 —— logf 为 nil（纯单测）时才丢弃，与 emit=nil 一致。
func commitRemote(s *Session, hasPosix bool, part, target string, j *swapJournal, logf func(string, ...any)) error {
	warn := func(format string, args ...any) {
		if logf != nil {
			logf(format, args...)
		}
	}
	if hasPosix {
		if err := posixRename(s.Conn, part, target); err == nil {
			return nil
		}
	}
	bak := BakNameRemote(target) // 远端 POSIX 路径：必须用 Remote 家族
	// 1) intent 先落盘；Begin 失败绝不进入破坏性改名。
	if j != nil {
		if err := j.Begin(target, bak, part); err != nil {
			return fmt.Errorf("登记 swap journal 失败，未改动目标: %w", err)
		}
	}
	// 2) target → bak。绝不先删目标：替换物没落位前目标内容始终存在（原名或 bak 名）。
	if err := renameRemote(s.Conn, target, bak); err != nil {
		if !os.IsNotExist(err) {
			// I4：非 ENOENT（权限/被占用）必须上报，绝不当作「目标不存在」直接提交 ——
			// 那会把失败当成功，还会绕过 journal。target 没被动，清掉已无意义的 intent。
			if j != nil {
				if derr := j.Done(target); derr != nil {
					warn("清理 swap journal 条目失败（目标改名失败时）: %v", derr)
				}
			}
			return err
		}
		// 目标原本不存在：无旧内容可丢，直接提交；intent 已无意义，先清（清不掉只 warn）。
		if j != nil {
			if derr := j.Done(target); derr != nil {
				warn("清理 swap journal 条目失败（目标本不存在时）: %v", derr)
			}
		}
		return renameRemote(s.Conn, part, target)
	}
	// 3) part → target。
	if err := renameRemote(s.Conn, part, target); err != nil {
		// 回滚：替换物没落位，绝不丢目标。回滚成功才清条目；失败必须保留条目并上报。
		if rbErr := renameRemote(s.Conn, bak, target); rbErr != nil {
			return fmt.Errorf("提交失败: %w; 回滚 %s → %s 亦失败（journal 条目已保留，待恢复）: %v",
				err, bak, target, rbErr)
		}
		if j != nil {
			if derr := j.Done(target); derr != nil {
				return fmt.Errorf("%w（回滚成功但清理 journal 失败，条目保留）: %v", err, derr)
			}
		}
		return err
	}
	// 4) 提交成功：清理失败只 warn（见函数头错误策略）。
	if j != nil {
		if err := j.Done(target); err != nil {
			warn("提交成功但清理 swap journal 条目失败: %v", err)
		}
	}
	if err := removeRemote(s.Conn, bak); err != nil && !os.IsNotExist(err) {
		warn("提交成功但删除备份 %s 失败（残留孤儿 bak，由临时文件清理兜底）: %v", bak, err)
	}
	return nil
}

func (g *GoBackend) PutTree(req TransferRequest, report func(Progress)) error {
	return errors.New("未实现")
}

func (g *GoBackend) TransferGet(req TransferRequest, report func(Progress)) error {
	return g.Get(req, report)
}
func (g *GoBackend) TransferGetTree(req TransferRequest, report func(Progress)) error {
	return g.GetTree(req, report)
}
func (g *GoBackend) TransferPut(req TransferRequest, report func(Progress)) error {
	return g.Put(req, report)
}
func (g *GoBackend) TransferPutTree(req TransferRequest, report func(Progress)) error {
	return g.PutTree(req, report)
}

func (g *GoBackend) Remove(host, user, path string) error             { return errors.New("未实现") }
func (g *GoBackend) RemoveRecursive(host, user, path string) error    { return errors.New("未实现") }
func (g *GoBackend) Mkdir(host, user, path string) error              { return errors.New("未实现") }
func (g *GoBackend) Rename(host, user, oldPath, newPath string) error { return errors.New("未实现") }

// Connect 对 GoBackend 而言就是「握手 + 探测」；建立长驻会话由池按需完成。
func (g *GoBackend) Connect(host, user string) error {
	_, err := g.Capabilities(host, user)
	return err
}

// Search 的 Go 实现要等后续 task；先返回未实现，避免静默空结果。
func (g *GoBackend) Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int,
	onProgress func(scanned int)) (SearchOutcome, error) {
	return SearchOutcome{}, errors.New("未实现")
}

// Connected 报告 host 是否有已成功建立的会话（池内 idle 或传输中，跨 user）。
// 会话惰性建立：从未连过 ⇒ false 正确；Connect/Capabilities 真握手入池后 ⇒ true；
// Disconnect/CloseAll 关掉后回到 false。绝不能对未连接过的 host 硬造 true（Task 6 评审 I3）。
func (g *GoBackend) Connected(host string) bool   { return g.pool.Connected(host) }
func (g *GoBackend) Disconnect(host string) error { return g.pool.Disconnect(host) }
func (g *GoBackend) CloseAll()                    { g.pool.CloseAll() }

// AtomicCapable：GoBackend 的新面一律走 .part + 提交（posix-rename 或 backup-swap），
// 因此声明支持原子提交。绑定层据此把 TransferRequest.Atomic 置 true（Task 9）。
func (g *GoBackend) AtomicCapable() bool { return true }

// Cancel 关掉该传输独占的会话（库无逐请求取消）。
//
// Task 9 只接线绑定；真正的会话注册表（regMu/reg）由 Task 10 落地 —— 当前恒返回 false
// 不表示「取消成功」，不表示「传输已完成」，只表示尚未接入。
// 这里刻意不留「假成功」分支：整批语义由前端编排层落实（取消当前项 + 停止后续派发，
// spec §6.1），前端据返回值决定是否把后续项标成已取消，返回 true 会让它撒谎。
func (g *GoBackend) Cancel(string) bool { return false } // Task 10 接线
