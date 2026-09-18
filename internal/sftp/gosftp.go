package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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

// beforeOpenLocalSource 是上传方向 I3 的确定性注入点：在 Put 已完成 os.Stat 与续传判定、
// 但尚未 os.Open 本地源时被调用。生产实现是 no-op；单测用它模拟「判定与打开之间本地源被
// 同尺寸改写」，验证 Put 会在已打开句柄上复核指纹并拒绝续传。
//
// Task 12 修复轮 2：putFileAtomic（目录上传的逐文件原语）在 os.Open 本地源之前也调用它，
// 单测据此在「枚举已完成、共享 helper 尚未打开源」的窗口里把文件改大，构造 M2/NEW-2 的
// 真实分母/分子分叉（不是只靠 treeProgress 的夹取单测）。
var beforeOpenLocalSource = func(local string) {}

// regEntry 是一次在飞传输在取消表里的条目。committed 在**提交成功之后、末帧进度上报
// 之前**置位（Task 10 修复轮 1 / I2）：UI 的 emitProgress 是同步回调，若不标记，Cancel
// 会在「文件其实已落地」的窗口里返回 true。一旦置位，Cancel 只答 false。
type regEntry struct {
	sess      *Session
	committed bool
	// cancel 取消本次传输的扫描相上下文（Task 12 修复轮 1 / I1）。目录传输在扫描相
	// （远端枚举 / 本地 WalkDir）不经过会话 IO，关会话拦不住它，必须有一个能被 Cancel
	// 主动触发的 ctx；单文件传输传 nil。
	cancel context.CancelFunc
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

	regMu sync.Mutex // Task 10：id → 在飞传输条目（取消用）
	reg   map[string]*regEntry

	inflightMu sync.Mutex // Task 11：同目标去重
	inflight   map[string]string

	resumeMu      sync.Mutex // Task 11：.part 路径 → 传输开始时的源指纹（内存态）
	resumeAnchors map[string]resumeAnchor

	partsMu    sync.Mutex           // Task 13：已知 .part（退出清理用）
	knownParts map[string][2]string // id → {local, remote}
}

func NewGoBackend(sel TransportSelector, emit forward.EmitFunc) *GoBackend {
	g := &GoBackend{
		emit: emit,
		sel:  sel,
		// Task 6 评审 M2：这些 map 到 Task 10/11/13 才被写入。构造时就初始化，
		// 后续 task 直接写字段（g.reg[id] = e 等）不会 panic: assignment to entry in nil map。
		reg:           map[string]*regEntry{},
		inflight:      map[string]string{},
		resumeAnchors: map[string]resumeAnchor{},
		knownParts:    map[string][2]string{},
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
	// Task 11 去重：同一 (host, 方向, 目标) 只允许一条在飞传输。放在取会话之前 ——
	// 重复触发必须立刻被拒，而不是排队等额度（排队会让「同目标并发」变成隐式串行）。
	release, err := g.acquireInflight(req.Host, req.User, string(DirDownload), req.Local, req.ID)
	if err != nil {
		return err
	}
	defer release()

	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()
	// Task 10：会话一取到就登记进取消表（defer 的注销先于 Release 执行 —— LIFO）。
	// 单文件传输无扫描相，不需要可取消 ctx（取消靠关会话让在飞 IO 立刻失败）。
	entry := g.register(req.ID, s, nil)
	defer g.unregister(req.ID, entry)

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
		// legacy 面没有独立的提交步骤：字节全部落盘即视为已完成。先标记 committed 再置
		// reuse，Cancel 不会在这个收尾窗口里假装取消成功（I2）。
		g.markCommitted(entry)
		reuse = true
		return nil
	}

	// —— Task 11 双向续传：先判定这份 .part 能不能续 ——
	// req.Resume 但无 PartPath（没有锚点）时不进入判定，直接走全新流程。
	if req.Resume && req.PartPath != "" {
		kind, off, derr := g.decideDownloadResume(s, req)
		if derr != nil {
			// M4：判定失败时远端源不可用，但本地 .part 可能仍在磁盘上 —— 如实带上 PartPath，
			// 否则调用方会丢掉 Task 7/8 契约里的续传锚点（本地不存在时仍为空）。
			return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote,
				PartPath: localPartIfExists(req.PartPath), Err: derr, RemoteMsg: s.Proc.StderrText()}
		}
		switch kind {
		case resumeFull:
			// 源已变/不可验证：旧 .part 只会污染结果，删掉再走全新流程。
			g.forgetResumeAnchor(downloadAnchorKey(req.Host, req.User, req.PartPath))
			_ = os.Remove(req.PartPath)
			req.Resume, req.ResumeOffset = false, 0
		case resumeCommit:
			// .part 已完整（== 源大小且指纹相符）：直接提交，绝不「续传 0 字节」。
			if err := os.Rename(req.PartPath, req.Local); err != nil {
				return &TransferError{Op: "sftp get", Path: req.Local, PartPath: req.PartPath, Err: err}
			}
			g.forgetResumeAnchor(downloadAnchorKey(req.Host, req.User, req.PartPath))
			g.markCommitted(entry)
			newProgressEmitter(req.ID, report).send(Progress{
				Host: req.Host, Direction: DirDownload, Name: req.Remote, PartPath: req.Local,
				Done: off, Total: off, Phase: PhaseTransfer,
			}, true)
			reuse = true
			return nil
		case resumeAppend:
			req.ResumeOffset = off
		}
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
	// 提交成功后 .part 已不存在（被 rename 成目标），锚点随之失效。
	g.forgetResumeAnchor(downloadAnchorKey(req.Host, req.User, part))
	// I2：提交已完成，先标记 committed 再发末帧 —— 末帧经 UI 同步回调，无论它多慢，
	// Cancel 都只会看到 false（绝不出现「文件已落地却答应用户取消」）。
	g.markCommitted(entry)
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

// GetTree 下载远端目录树（D8/D11/D16）：先枚举（scanTree）、再逐文件走单文件的
// .part + 本地 rename 提交（copyFileToLocal）。目录项**只重试、不续传**（P3.1）：
// 树内多锚点无法用单值 PartPath 表达，因此 req.Resume/req.PartPath 一律忽略。
//
// 一个会话贯穿整棵树（不像「逐文件调 Get」那样每个文件重新握手 + 重新排队取额度）。
// 取消仍由关会话完成（Task 10）：关会话会让当前 ReadDirContext/copyStream 立刻失败，
// 循环随即带着错误退出 —— 已提交的文件保留，失败项的 .part 保留作续传/重试锚点。
func (g *GoBackend) GetTree(req TransferRequest, report func(Progress)) error {
	const op = "sftp get -r"
	if req.Remote == "" || req.Local == "" {
		return &TransferError{Op: op, Host: req.Host, Path: req.Remote, Err: errors.New("目录传输缺少远端或本地路径")}
	}
	release, err := g.acquireInflight(req.Host, req.User, string(DirDownload), req.Local, req.ID)
	if err != nil {
		return err
	}
	defer release()

	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: op, Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()
	// I1（Task 12 修复轮 1）：扫描相取消上下文。PUT 方向在扫描相不经过会话 IO，
	// 只关会话拦不住本地 WalkDir；GET 方向的 scanTree 两处 ctx.Err() 检查原先因为传
	// context.Background() 而是死代码。Cancel(id) 触发的 cancel 让两处检查都真正生效。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entry := g.register(req.ID, s, cancel)
	defer g.unregister(req.ID, entry)

	// 目录项不接受续传（P3.1）：清空单文件锚点参数。若照搬 req.PartPath，整棵树的所有文件
	// 会共用同一个 .part 路径互相覆盖，最后各自 rename 出别人的内容 —— 静默损坏。
	req.Resume, req.ResumeOffset, req.PartPath = false, 0, ""

	// NEW-1：聚合器与失败收口必须在扫描**之前**就位 —— 扫描相（ReadDirContext 失败或被
	// Cancel 中止）同样是一条「传输已终止」的路径，必须发出末帧并回填诚实计数，否则调用方
	// 无从告诉用户到底发生了什么。扫描前分母未知，先按 -1/-1 占位（与降级帧同形状）。
	tp := newTreeProgress(req.ID, req.Host, DirDownload, req.Remote, -1, -1, report)
	// I4：失败/取消路径也补一发末帧并回填「已提交/剩余」计数 —— 否则调用方无从告诉用户
	// 到底传完了多少（原先 markCommitted/tp.frame 只在成功路径存在）。
	fail := func(te *TransferError) error {
		te.CommittedFiles, te.CommittedBytes, te.RemainingFiles = tp.fail(te.PartPath)
		te.TreeCounts = true
		return te
	}

	files, subdirs, degraded, serr := scanTree(ctx, s, req.Remote, time.Now())
	if serr != nil {
		var remoteMsg string
		if isRemoteError(serr) {
			remoteMsg = s.Proc.StderrText()
		}
		return fail(&TransferError{Op: op, Host: req.Host, Path: req.Remote, Err: serr, RemoteMsg: remoteMsg})
	}
	total, filesTotal := int64(0), len(files)
	if degraded {
		// D16：放弃枚举 ⇒ 分母未知（-1），但传输继续、Done/FilesDone 继续真实累加。
		total, filesTotal = -1, -1
	} else {
		for _, f := range files {
			total += f.size
		}
	}
	// 扫描成功：把真实分母写回聚合器；此后的建目录失败也就能报出诚实的「剩余文件数」。
	tp.total, tp.files = total, filesTotal
	// 目录本身（含空目录）按 D11 建出，与 sftp get -r 一致 —— 空目录也必须在本地出现。
	if merr := os.MkdirAll(req.Local, 0o755); merr != nil {
		return fail(&TransferError{Op: op, Path: req.Local, Err: wrapLocalIO(merr)})
	}
	for _, d := range subdirs {
		p := filepath.Join(req.Local, filepath.FromSlash(d))
		if merr := os.MkdirAll(p, 0o755); merr != nil {
			return fail(&TransferError{Op: op, Path: p, Err: wrapLocalIO(merr)})
		}
	}
	tp.begin()

	for _, f := range files {
		localPath := filepath.Join(req.Local, filepath.FromSlash(f.rel))
		if merr := os.MkdirAll(filepath.Dir(localPath), 0o755); merr != nil {
			return fail(&TransferError{Op: op, Path: filepath.Dir(localPath), Err: wrapLocalIO(merr)})
		}
		freq := req
		freq.Remote = path.Join(req.Remote, f.rel) // 远端 POSIX 路径：path 而非 filepath
		freq.Local = localPath

		if req.Atomic {
			part, _, ftotal, cerr := g.copyFileToLocal(s, freq, freq.Remote, freq.Local, tp.file)
			if cerr != nil {
				// 失败/取消：.part 保留（helper 契约），已提交的文件不回滚（目录项重试 = 整项重传）。
				var remoteMsg string
				if isRemoteError(cerr) {
					remoteMsg = s.Proc.StderrText()
				}
				return fail(&TransferError{Op: op, Host: req.Host, Path: freq.Remote, PartPath: part, Err: cerr, RemoteMsg: remoteMsg})
			}
			if rerr := os.Rename(part, localPath); rerr != nil {
				return fail(&TransferError{Op: op, Path: localPath, PartPath: part, Err: rerr})
			}
			g.forgetResumeAnchor(downloadAnchorKey(req.Host, req.User, part))
			// M2/NEW-2：expected 用枚举大小、actual 用已打开句柄的 Stat —— finishFile 据此对账分母。
			tp.finishFile(f.size, ftotal)
			continue
		}
		// legacy 面（Atomic=false）：直写目标，与 Get 的 legacy 分支同一语义与校验。
		n, ftotal, cerr := g.getNonAtomic(s, freq.Remote, localPath)
		if cerr != nil {
			var remoteMsg string
			if isRemoteError(cerr) {
				remoteMsg = s.Proc.StderrText()
			}
			return fail(&TransferError{Op: op, Host: req.Host, Path: freq.Remote, Err: cerr, RemoteMsg: remoteMsg})
		}
		if decideCommit(n, ftotal) != commitOK {
			return fail(&TransferError{Op: op, Path: freq.Remote, Err: shortReadError(n, ftotal, "")})
		}
		tp.finishFile(f.size, ftotal)
	}
	// 与 Get/Put 同一时序：先标 committed（此后 Cancel 只答 false），再发末帧。
	g.markCommitted(entry)
	tp.frame(true)
	reuse = true
	return nil
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
	// Task 11 去重：与 Get 对称，键里的目标是远端目标路径。
	release, err := g.acquireInflight(req.Host, req.User, string(DirUpload), req.Remote, req.ID)
	if err != nil {
		return err
	}
	defer release()

	st, err := os.Stat(req.Local)
	if err != nil {
		// 本地源不存在/不可读：在建会话之前失败，且是本地错误（不附远端 stderr）。
		return &TransferError{Op: "sftp put", Path: req.Local, Err: err}
	}
	total := st.Size()
	// 本地源指纹（Task 11 续传用）：与 total 一起记录在远端 .part 上。
	srcMtime := st.ModTime()

	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()
	// Task 10：同 Get —— 会话一取到就登记，函数返回时注销。单文件传输无扫描相，ctx 为 nil。
	entry := g.register(req.ID, s, nil)
	defer g.unregister(req.ID, entry)

	if !req.Atomic {
		// legacy 面（internal/sync / app.SftpPut）：直写目标，不建我方 .part、不提交、不登记 journal。
		// I2：直写路径的末帧在 putNonAtomic 内部发出，这里把 committed 标记传进去，
		// 保证末帧（同步回调）之前就已置位。
		if err := g.putNonAtomic(s, req, total, report, func() { g.markCommitted(entry) }); err != nil {
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
	// —— Task 11 双向续传：先判定远端 .part 能不能续 ——
	if req.Resume && req.PartPath != "" {
		kind, off, derr := g.decideUploadResume(s, req, total, srcMtime)
		if derr != nil {
			// Task 12 加固（上传 M4）：与下载方向的 localPartIfExists 对齐 —— 只上报**已核实
			// 存在**的锚点。原先乐观地直接回 req.PartPath，会把一次 Stat 抖动变成发布一个
			// ENOENT 的假锚点（下次续传拿到 ENOENT 才降级为整份，但调用方已经先相信了它）。
			// 注意：这里再 Stat 一次仍是远端操作，失败就如实返回空串（宁可丢锚点也不撒谎）。
			return &TransferError{Op: "sftp put", Host: req.Host, Path: req.PartPath,
				PartPath: remotePartIfExists(s, req.PartPath), Err: derr, RemoteMsg: s.Proc.StderrText()}
		}
		switch kind {
		case resumeFull:
			// 源已变/不可验证：旧 .part 只会污染结果，删掉再走全新流程。
			g.forgetResumeAnchor(uploadAnchorKey(req.Host, req.User, req.Remote, req.PartPath))
			if rerr := removeRemote(s.Conn, req.PartPath); rerr != nil && !os.IsNotExist(rerr) {
				return &TransferError{Op: "sftp put", Host: req.Host, Path: req.PartPath, Err: rerr, RemoteMsg: s.Proc.StderrText()}
			}
			req.Resume, req.ResumeOffset = false, 0
		case resumeCommit:
			// 远端 .part 已完整（== 本地源大小且指纹相符）：直接提交，绝不「续传 0 字节」。
			if err := commitRemote(s, hasPosix, req.PartPath, req.Remote, g.journal, func(f string, a ...any) { g.warnf(s.Host, f, a...) }); err != nil {
				var remoteMsg string
				if isRemoteError(err) {
					remoteMsg = s.Proc.StderrText()
				}
				return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, PartPath: req.PartPath, Err: err, RemoteMsg: remoteMsg}
			}
			g.forgetResumeAnchor(uploadAnchorKey(req.Host, req.User, req.Remote, req.PartPath))
			g.markCommitted(entry)
			newProgressEmitter(req.ID, report).send(Progress{
				Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: req.Remote,
				Done: total, Total: total, Phase: PhaseTransfer,
			}, true)
			reuse = true
			return nil
		case resumeAppend:
			req.ResumeOffset = off
			// 事实 4：Seek 越过 EOF 会零填充出洞，所以续写前必须把远端 .part 的长度对齐到
			// offset（长于则截断丢弃陈旧尾部）；短于 offset 无法凭空补齐 —— 退回整份重传，
			// 绝不 Truncate(offset) 把缺口补成零洞。
			ok, aerr := g.alignRemotePart(s, req.PartPath, off)
			if aerr != nil {
				return &TransferError{Op: "sftp put", Host: req.Host, Path: req.PartPath, Err: aerr, RemoteMsg: s.Proc.StderrText()}
			}
			if !ok {
				g.forgetResumeAnchor(uploadAnchorKey(req.Host, req.User, req.Remote, req.PartPath))
				if rerr := removeRemote(s.Conn, req.PartPath); rerr != nil && !os.IsNotExist(rerr) {
					return &TransferError{Op: "sftp put", Host: req.Host, Path: req.PartPath, Err: rerr, RemoteMsg: s.Proc.StderrText()}
				}
				req.Resume, req.ResumeOffset = false, 0
			}
		}
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
	// I3 注入点：单测在「os.Stat 判定已完成、本地源尚未打开」的窗口里改写本地源。
	beforeOpenLocalSource(req.Local)
	lf, err := os.Open(req.Local)
	if err != nil {
		_ = wf.Close()
		// 本地源打不开：远端 .part 已建出、按约束 4 保留可续传，但不附远端 stderr（本地错误）。
		return &TransferError{Op: "sftp put", Path: req.Local, PartPath: part, Err: wrapLocalIO(err)}
	}
	// I3：把指纹校验与数据读取绑定到**同一个已打开句柄**。Put 开头的 os.Stat（total/srcMtime）
	// 与这里的 os.Open 之间存在窗口；同尺寸改写若发生在这个窗口里，继续续写既会把旧前缀
	// 拼到新内容上，又会把**错误的指纹**写进锚点（下次续传还会再拼一次）。因此打开后立刻
	// 用句柄自身的 Stat 复核：不符则拒绝本次传输，且绝不记锚点 —— 绝不写出静默损坏的目标。
	lst, lerr := lf.Stat()
	if lerr != nil {
		_ = wf.Close()
		_ = lf.Close()
		return &TransferError{Op: "sftp put", Path: req.Local, PartPath: part, Err: wrapLocalIO(lerr)}
	}
	if lst.Size() != total || !lst.ModTime().Equal(srcMtime) {
		_ = wf.Close()
		_ = lf.Close()
		return &TransferError{Op: "sftp put", Path: req.Local, PartPath: part,
			Err: fmt.Errorf("%w: 本地源在判定与打开之间发生变化（size %d→%d）", errLocalIO, total, lst.Size())}
	}
	// 记录本地源指纹（Task 11）：这份远端 .part 的已写字节对应**已打开句柄**看到的本地源
	// (身份, size, mtime)；用句柄 Stat 记录，保证锚点与真正读到的字节出自同一份元数据（I3）。
	// 记录必须晚于上面的复核：绝不给一份会被拼错的 .part 留下「合法」锚点。
	g.recordResumeAnchor(uploadAnchorKey(req.Host, req.User, req.Remote, part), req.Local, lst.Size(), lst.ModTime())
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
	// 提交成功后 .part 已不存在（被 rename 成目标），锚点随之失效。
	g.forgetResumeAnchor(uploadAnchorKey(req.Host, req.User, req.Remote, part))
	// I2：提交已完成，先标记 committed 再发末帧（末帧经 UI 同步回调）—— Cancel 绝不
	// 能在「远端文件已落地」时返回 true。
	g.markCommitted(entry)
	// 末帧强制且只有在提交成功之后才发：末帧 = 提交完成（约束 6 的 PartPath 语义）。
	// PartPath 必须指向**已提交的最终目标**，绝不能填那个已被 rename 掉、不再存在的旧 .part。
	em.send(Progress{
		Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: req.Remote,
		Done: total, Total: total, Phase: PhaseTransfer,
	}, true)
	reuse = true
	return nil
}

// remotePartIfExists 返回确实存在于远端的 .part 路径；不存在（或为空）返回空串。
// 与下载方向的 localPartIfExists 对称（Task 12 加固 M4）：错误分支只上报**已核实存在**的
// 锚点，绝不因为一次 Stat 抖动就发布一个 ENOENT 的假路径。Stat 本身失败同样返回空串。
func remotePartIfExists(s *Session, part string) string {
	if part == "" {
		return ""
	}
	if st, err := s.Conn.Stat(part); err == nil && st.Mode().IsRegular() {
		return part
	}
	return ""
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
// markCommitted 由 Put 传入（I2）：必须在末帧（同步回调）之前调用，否则 Cancel 会在
// 「目标已写完」的窗口里返回 true。可为 nil（测试直调时无注册表条目）。
func (g *GoBackend) putNonAtomic(s *Session, req TransferRequest, total int64, report func(Progress), markCommitted func()) error {
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
	// I2：末帧（同步回调）之前先标记完成，Cancel 不能在收尾窗口里假装取消成功。
	if markCommitted != nil {
		markCommitted()
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

// PutTree 递归上传本地目录（D8/D11/D16）。
//
// 合并语义（D11，与 sftp put -r 实测一致，batch.go PutRecursive 的权威描述同源）：
// 远端建 <remoteDir>/<base(local)>，同名目录已存在则**并入**（同名文件被覆盖），
// 绝不嵌套出 <base>/<base>，也不清理远端独有的旧文件。
//
// 与 GetTree 对称：一个会话贯穿整棵树；每一项走远端 .part + commitRemote 原子提交；
// 目录项只重试不续传（P3.1），req.Resume/req.PartPath 一律忽略。
func (g *GoBackend) PutTree(req TransferRequest, report func(Progress)) error {
	const op = "sftp put -r"
	if req.Remote == "" || req.Local == "" {
		return &TransferError{Op: op, Path: req.Local, Err: errors.New("目录传输缺少远端或本地路径")}
	}
	st, err := os.Stat(req.Local)
	if err != nil {
		return &TransferError{Op: op, Path: req.Local, Err: err}
	}
	if !st.IsDir() {
		return &TransferError{Op: op, Path: req.Local, Err: errors.New("put -r 的本地源必须是目录")}
	}
	release, err := g.acquireInflight(req.Host, req.User, string(DirUpload), req.Remote, req.ID)
	if err != nil {
		return err
	}
	defer release()

	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: op, Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()

	// I1（Task 12 修复轮 1）：**先登记再枚举**。本地 WalkDir 可能长时间运行，在这个窗口里
	// Cancel(id) 必须已经能命中条目（否则用户的取消被静默吞掉、上传照旧进行）。扫描相不经过
	// 会话 IO，光关会话拦不住 WalkDir；注册一个 Cancel 能触发的 ctx，WalkDir 每步检查它。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entry := g.register(req.ID, s, cancel)
	defer g.unregister(req.ID, entry)

	// 目录项不接受续传（P3.1）：清空单文件锚点参数，避免同一个 PartPath 被整棵树复用。
	req.Resume, req.ResumeOffset, req.PartPath = false, 0, ""

	hasPosix := false
	if req.Atomic {
		_, hasPosix = s.Conn.HasExtension(posixRenameExt)
	}

	// NEW-1：聚合器与失败收口必须在扫描**之前**就位 —— 本地 WalkDir 失败或被 Cancel 中止
	// 同样是一条「传输已终止」的路径，必须发出末帧并回填诚实计数，否则调用方无从告诉用户
	// 到底发生了什么。扫描前分母未知，先按 -1/-1 占位（与降级帧同形状）。
	tp := newTreeProgress(req.ID, req.Host, DirUpload, req.Remote, -1, -1, report)
	// I4：失败/取消路径也补一发末帧并回填「已提交/剩余」计数（逐文件原语返回 *TransferError）。
	fail := func(err error) error {
		var te *TransferError
		part := ""
		if errors.As(err, &te) {
			part = te.PartPath
		}
		cf, cb, rem := tp.fail(part)
		if te != nil {
			te.CommittedFiles, te.CommittedBytes, te.RemainingFiles = cf, cb, rem
			te.TreeCounts = true
		}
		return err
	}

	// D16：枚举阈值在本地遍历**过程中**评估（不是走完整棵树再判）—— 命中即停止枚举
	// （停止发现后续条目，与下载方向「停止发起下一批」同义），返回已枚举到的子集并降级。
	scanStart := time.Now()
	files, subdirs, total, degraded, werr := scanLocalTree(ctx, req.Local, scanStart)
	if werr != nil {
		return fail(&TransferError{Op: op, Path: req.Local, Err: werr})
	}
	filesTotal := len(files)
	if degraded {
		// 与下载方向同一条 D16 规则：枚举超阈值 ⇒ 分母未知（-1），Done/FilesDone 继续累加。
		total, filesTotal = -1, -1
	}
	// 扫描成功：把真实分母写回聚合器；此后的建远端目录失败也就能报出诚实的「剩余文件数」。
	tp.total, tp.files = total, filesTotal
	// 合并语义：目标根 = <remoteDir>/<base(local)>。用 path（POSIX）拼接，绝不碰 filepath
	// 的远端语义（Windows 客户端会把 / 变成反斜杠）。
	rootRemote := path.Join(req.Remote, filepath.Base(filepath.Clean(req.Local)))
	// 远端目录（含空目录）按 D11 一次性建出：WalkDir 已给出完整子目录清单，绝不逐文件
	// 重复 MkdirAll（那是每文件一次多余往返）。
	if derr := s.Conn.MkdirAll(rootRemote); derr != nil {
		return fail(&TransferError{Op: op, Host: req.Host, Path: rootRemote, Err: derr, RemoteMsg: s.Proc.StderrText()})
	}
	for _, d := range subdirs {
		p := path.Join(rootRemote, d)
		if derr := s.Conn.MkdirAll(p); derr != nil {
			return fail(&TransferError{Op: op, Host: req.Host, Path: p, Err: derr, RemoteMsg: s.Proc.StderrText()})
		}
	}
	tp.begin()

	for _, f := range files {
		localPath := filepath.Join(req.Local, filepath.FromSlash(f.rel))
		remotePath := path.Join(rootRemote, f.rel)
		// 分子用**实际提交的字节数**（原子路径取已打开句柄的 Stat）；同时把枚举大小 f.size
		// 作为 expected 交给 finishFile，让它把分母对账到同一来源（M2/NEW-2）—— 文件在枚举
		// 与打开之间被改写时分母会跟随实际值，不再自相矛盾、也不再静默少报。
		committed := f.size
		if req.Atomic {
			n, perr := g.putFileAtomic(s, req, localPath, remotePath, hasPosix, tp.file)
			if perr != nil {
				return fail(perr)
			}
			committed = n
		} else {
			freq := req
			freq.Local, freq.Remote = localPath, remotePath
			if perr := g.putNonAtomic(s, freq, f.size, tp.file, nil); perr != nil {
				return fail(perr)
			}
		}
		tp.finishFile(f.size, committed)
	}
	g.markCommitted(entry)
	tp.frame(true)
	reuse = true
	return nil
}

// scanLocalTree 递归枚举本地目录：普通文件 + **子目录**（后者供 PutTree 建出空目录，
// 与 sftp put -r 一致）。rel 统一转成 POSIX 相对路径，便于 path.Join 拼远端路径
// （Windows 客户端的反斜杠绝不能进远端路径）。文件软链跟随（与远端枚举及现状一致），
// 目录软链不递归；我方临时/备份文件（本地 .part）不进清单（D18）。
//
// Task 12 修复轮 1：接受取消上下文与枚举起点时间。WalkDir 不是 ctx 感知 API，靠每一步
// 的 ctx 检查（scanCheckpoint 注入点 + ctx.Err()）实现扫描相取消；D16 阈值在遍历中评估，
// 命中即 degraded=true 并停止枚举（SkipAll 丢弃剩余条目）。
//
// Task 12 修复轮 2（NEW-3）：阈值判定对**每一个条目**（含目录、含根目录）都执行，而不是
// 只在追加文件之后 —— 目录-only 树不能因为「没有文件可追加」而绕过 5s 预算。
func scanLocalTree(ctx context.Context, root string, start time.Time) (files []treeFile, subdirs []string, total int64, degraded bool, err error) {
	// 源目录本身若是软链，WalkDir 不会跟随根（会把它当非目录项、一个文件都枚举不到）：
	// 先解析成真实目录。用户从系统拖入的目录经常是软链。
	if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = resolved
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		// I1：扫描相取消的确定性注入点（先钩子、后 ctx 检查）—— 单测在这里卡住扫描，
		// 调 Cancel(id)（触发 cancel），放行后 ctx.Err() 立刻让整项中止。
		scanCheckpoint()
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			if p != root {
				rel, rerr := filepath.Rel(root, p)
				if rerr != nil {
					return rerr
				}
				if !IsInternalTemp(filepath.Base(rel)) {
					subdirs = append(subdirs, filepath.ToSlash(rel))
				}
			}
			// NEW-3（Task 12 修复轮 2）：目录也必须参与 D16 预算判定 —— 否则「目录-only /
			// 一个文件都没有」的走查永远碰不到原先只在文件分支里的阈值，可以无限期持有并发
			// 令牌与传输会话（下载方向的 scanTree 是按目录批检查的，这里补齐对称性）。
			// 根目录也检查：预算耗尽时连枚举都不再展开。
			if scanLimit(len(files), time.Since(start)) {
				degraded = true
				return fs.SkipAll
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// 软链：跟随。解析成目录 ⇒ 不递归（与远端 readdir 用 lstat 的语义一致）。
			st, serr := os.Stat(p)
			if serr != nil {
				return serr
			}
			info = st
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if IsInternalTemp(filepath.Base(rel)) {
			return nil
		}
		files = append(files, treeFile{rel: filepath.ToSlash(rel), size: info.Size()})
		total += info.Size()
		// D16：阈值在遍历中评估并**停止枚举**（fs.SkipAll 丢弃剩余条目）。这是与下载方向
		// 「停止发起下一批」对称的真实降级语义 —— 走完整棵树再只报 -1 不算降级（评审 I3）。
		if scanLimit(len(files), time.Since(start)) {
			degraded = true
			return fs.SkipAll
		}
		return nil
	})
	if err == fs.SkipAll {
		err = nil
	}
	return files, subdirs, total, degraded, err
}

// putFileAtomic 把一个本地文件原子上传到远端目标：写远端同目录 .part → done==total →
// commitRemote（posix-rename 或 backup-swap + journal）。
//
// 目录上传专用：调用方已持有一个可用的 .part 锚点时会**复用它**（见 part 的取值），但
// PutTree 在进入本函数前已清空 req.PartPath/Resume（目录项只重试，P3.1），因此生产路径
// 每一项都各自新建 .part。这里的取值与下载方向 copyFileToLocal 的 part 处理**对称**，
// 使 PutTree 的清空成为可被观测、可被变异杀死的真实守卫（I2 修复轮 2），而不是死代码。
// 提交仍复用唯一的 commitRemote —— 目录传输不新增第二条提交路径。total 取**已打开句柄**
// 的 Stat，避免「枚举 size 与打开之间被改写」的窗口；done==total 仍是唯一提交前置。
func (g *GoBackend) putFileAtomic(s *Session, req TransferRequest, local, remote string, hasPosix bool, report func(Progress)) (int64, error) {
	const op = "sftp put -r"
	// 远端 POSIX 路径：必须用 Remote 家族（Task 2 评审 Important-2）。与 copyFileToLocal
	// 同一契约：调用方给了锚点就用它（PutTree 必须已清空，否则整棵树会共用一个 .part）。
	part := req.PartPath
	if part == "" {
		part = PartNameRemote(remote, req.ID)
	}
	wf, err := s.Conn.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		// .part 根本没建出来 ⇒ PartPath 必须为空（绝不发布假锚点）。
		return 0, &TransferError{Op: op, Host: req.Host, Path: part, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	// I3/NEW-2 注入点：单测在「枚举完成、本地源尚未打开」的窗口里把文件改大，构造分母/分子
	// 的真实分叉（与 Put 的续传复核共用同一注入点，语义一致）。
	beforeOpenLocalSource(local)
	lf, err := os.Open(local)
	if err != nil {
		_ = wf.Close()
		// 远端 .part 已建出：如实带上作重试锚点；本地错误不附远端 stderr。
		return 0, &TransferError{Op: op, Path: local, PartPath: part, Err: wrapLocalIO(err)}
	}
	lst, lerr := lf.Stat()
	if lerr != nil {
		_ = wf.Close()
		_ = lf.Close()
		return 0, &TransferError{Op: op, Path: local, PartPath: part, Err: wrapLocalIO(lerr)}
	}
	total := lst.Size()
	em := newProgressEmitter(req.ID, report)
	cr := &countingReader{r: lf, e: em, p: Progress{Host: req.Host, Direction: DirUpload, Name: remote, PartPath: part, Total: total, Phase: PhaseTransfer}}
	em.send(cr.p, true)
	n, cerr := copyStream(wf, cr)
	_ = wf.Close()
	_ = lf.Close()
	if cerr != nil {
		var remoteMsg string
		if isRemoteError(cerr) {
			remoteMsg = s.Proc.StderrText()
		}
		return total, &TransferError{Op: op, Host: req.Host, Path: remote, PartPath: part, Err: cerr, RemoteMsg: remoteMsg}
	}
	if decideCommit(n, total) != commitOK {
		// 唯一提交前置：少传/多传都保留 .part、绝不提交（R13/约束 4）。
		return total, &TransferError{Op: op, Path: remote, PartPath: part, Err: shortReadError(n, total, part)}
	}
	if err := commitRemote(s, hasPosix, part, remote, g.journal, func(f string, a ...any) { g.warnf(s.Host, f, a...) }); err != nil {
		var remoteMsg string
		if isRemoteError(err) {
			remoteMsg = s.Proc.StderrText()
		}
		return total, &TransferError{Op: op, Host: req.Host, Path: remote, PartPath: part, Err: err, RemoteMsg: remoteMsg}
	}
	// 末帧：提交成功后才发，PartPath 指向已提交的最终目标（与 Put 同一 PartPath 语义）。
	em.send(Progress{Host: req.Host, Direction: DirUpload, Name: remote, PartPath: remote, Done: total, Total: total, Phase: PhaseTransfer}, true)
	return total, nil
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

// cancelCloseSession 是 Cancel 关会话的唯一入口（注入点，与 copyStream/posixRename
// 同思路）。单测用它把「Cancel 删除注册表条目」与「真正关闭会话」之间的窗口加宽成确定性，
// 从而复现 I3 竞态：Release 已把会话放回 idle，Cancel 随后才关掉它。
var cancelCloseSession = func(s *Session) { s.close() }

// acquireInflight 是同目标去重的唯一入口（Task 11）：键 = host|user|方向|目标，
// 下载的目标是本地路径、上传的目标是远端路径。重复触发立刻报错，绝不排队 ——
// 排队会把「同目标并发」变成隐式串行，且第一个完成后第二个照样覆盖，用户看不到任何提示。
//
// **user 必须进键**（M3）：不同用户对同一 host + 同一目标路径是两条互不相干的传输
// （远端身份不同、可访问的文件不同），按 host|方向|目标 去重会把它们误判成重复而拒绝。
// 返回的 release 必须 defer 调用（含失败路径），否则该目标会永久被判为「在传输中」。
func (g *GoBackend) acquireInflight(host, user, dir, target, id string) (func(), error) {
	key := host + "|" + user + "|" + dir + "|" + target
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	if g.inflight == nil {
		g.inflight = map[string]string{}
	}
	if other, busy := g.inflight[key]; busy {
		return nil, &TransferError{Op: "sftp transfer", Host: host, Path: target,
			Err: fmt.Errorf("同一目标已在传输中（%s）", other)}
	}
	g.inflight[key] = id
	return func() {
		g.inflightMu.Lock()
		delete(g.inflight, key)
		g.inflightMu.Unlock()
	}, nil
}

// register 把一次在飞传输登记进取消表（id → 传输条目），返回条目供 markCommitted /
// unregister 使用。
//
// 空 id（legacy 四参面不带 id）一律不登记，Cancel("") 永远 false —— 绝不把「没有身份标识」
// 的传输暴露成可取消条目，也避免空键被并发无 id 传输互相覆盖（M1）。
//
// 注销必须在传输返回时执行（成功/失败/取消都一样），否则注册表会残留陈旧键。
// 为什么 id 作键是安全的（Task 10 事实 4）：注册表是**进程内**的，进程重启即空，跨重启的
// 陈旧 id 够不到新进程；又因池的传输并发上限为 1（AcquireTransfer 先取额度再登记），任一
// 时刻至多一条在飞条目。
func (g *GoBackend) register(id string, s *Session, cancel context.CancelFunc) *regEntry {
	if id == "" {
		return nil
	}
	e := &regEntry{sess: s, cancel: cancel}
	g.regMu.Lock()
	g.reg[id] = e
	g.regMu.Unlock()
	return e
}

// markCommitted 标记本次传输已成功提交。必须在**末帧进度上报之前**、且在 regMu 下调用：
// 与 Cancel 的「查表 + 判 committed + 删除」互斥，一旦置位，Cancel 只会看到 false（I2）。
// 空条目（id==""）是 no-op。
func (g *GoBackend) markCommitted(e *regEntry) {
	if e == nil {
		return
	}
	g.regMu.Lock()
	e.committed = true
	g.regMu.Unlock()
}

// unregister 幂等注销（由 Get/Put 的 defer 调用，先于 pool.Release 执行 —— LIFO）。
// 只有表里仍是**本次**登记的那条时才删：防止迟到的清理抹掉同 id 的新登记（ABA 防御）。
func (g *GoBackend) unregister(id string, e *regEntry) {
	if e == nil {
		return
	}
	g.regMu.Lock()
	if g.reg[id] == e {
		delete(g.reg, id)
	}
	g.regMu.Unlock()
}

// Cancel 关掉该传输独占的会话（库无逐请求取消，spec §2.3），返回是否真的取消到了一次
// 在飞传输。整批语义（取消当前项 + 停止派发后续项）由前端编排层落实（spec §6.1）。
//
// 诚实契约（Task 14 依赖，绝不为了取悦 UI 返回 true）：
//   - true 仅当注册表里确实有该 id 的**未提交**在飞会话（关会话 ⇒ 在飞 read/write 立刻
//     失败 ⇒ done!=total ⇒ 绝不提交；.part 按约定保留作续传锚点）；
//   - 已提交的传输（committed=true）⇒ false：文件已经落地，绝不能说取消了（I2）；
//   - 未知 id / 空 id / 从未开始 / 已完成（已注销）⇒ false；
//   - 同一 id 第二次取消 ⇒ false（取消时即从注册表移除，天然幂等，不报错）；
//   - batch 后端没有长驻会话 ⇒ 恒 false（见 BatchBackend.Cancel）。
//
// 并发：查表、判 committed、删除都在 regMu 下完成 —— 并发取消同一 id 恰好一个拿到会话返回
// true，其余返回 false（-race 干净）。close 绝不在锁内：Session.close 要关管道并有界等待
// 子进程退出（≤5s），持锁会把清理路径一起堵住。
func (g *GoBackend) Cancel(id string) bool {
	if id == "" {
		return false
	}
	g.regMu.Lock()
	e, ok := g.reg[id]
	if ok && e != nil && !e.committed {
		delete(g.reg, id)
	} else {
		ok = false
	}
	g.regMu.Unlock()
	if !ok {
		return false
	}
	// I3(b)：关闭前先把会话从 idle 摘掉 —— 竞态里 Release 可能已把它放回 idle。
	// 注意仍留一条尾巴：RemoveIdle 之后 Release 才追加的话，会话会以 Closed 状态留在
	// idle；由 AcquireList 出池时的 Closed 兜底拦住（I3(a)），两处缺一不可。
	// I1（Task 12 修复轮 1）：先取消扫描相上下文再关会话。扫描相（远端枚举/本地 WalkDir）
	// 不经过会话 IO，只关会话拦不住它；cancel 让 scanTree/scanLocalTree 的 ctx 检查立刻生效，
	// 传输在扫描阶段就能中止。cancel 幂等，且绝不在此归还并发额度（额度仍由原传输的
	// defer Release 恰好归还一次 —— Task 10 I1）。
	if e.cancel != nil {
		e.cancel()
	}
	g.pool.RemoveIdle(e.sess)
	cancelCloseSession(e.sess)
	return true
}
