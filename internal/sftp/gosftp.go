package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

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
	_, ok := s.Conn.HasExtension("posix-rename@openssh.com")
	g.pool.Release(s, true)
	return ok, nil
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
			return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
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
		if part == "" { // 远端 Stat/Open 失败：不创建本地文件，错误带远端原文（spec D12）
			return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
		}
		// 失败/取消：.part 保留（Task 11 续传锚点），reuse=false 关掉会话。
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, PartPath: part, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	if err := os.Rename(part, req.Local); err != nil {
		return &TransferError{Op: "sftp get", Path: req.Local, PartPath: part, Err: err}
	}
	// 末帧强制：终值必须达（D7）。done==total 已成立，补发带完整计数的终态。
	newProgressEmitter(req.ID, report).send(Progress{
		Host: req.Host, Direction: DirDownload, Name: req.Remote, PartPath: part,
		Done: total, Total: total, Phase: PhaseTransfer,
	}, true)
	reuse = true
	return nil
}
func (g *GoBackend) GetTree(req TransferRequest, report func(Progress)) error {
	return errors.New("未实现")
}
func (g *GoBackend) Put(req TransferRequest, report func(Progress)) error {
	return errors.New("未实现")
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
