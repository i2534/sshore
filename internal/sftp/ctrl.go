package sftp

import (
	"context"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// Ctrl 是门面：legacy 四参面（sync 与既有测试用）+ 新传输面（绑定层用）。
// 两种后端（BatchBackend / GoBackend）都挂在它后面，由 backend() 选择。
type Ctrl struct {
	batch      *BatchBackend
	sel        TransportSelector
	emit       forward.EmitFunc
	journalDir string
	forced     Backend // 仅供测试注入：backend() 优先返回它
}

// NewCtrl 保持 spec D3 的两参签名（env + 内置默认），既有调用点无需改动。
func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{batch: NewBatchBackend(r, emit)}
}

// NewCtrlWith 由 app.go 注入懒解析选择器（spec D3）。
func NewCtrlWith(r osutil.Runner, emit forward.EmitFunc, sel TransportSelector) *Ctrl {
	c := NewCtrl(r, emit)
	c.sel = sel
	c.emit = emit
	return c
}

// —— legacy 四参面：签名逐字不变，但一律走 c.backend() 且 Atomic=false ——
// 自审 S11：若把 legacy 面硬绑 batch，则开关对 internal/sync 失效、后端矩阵名不副实、
// 且 v0.8 删掉 batch 后 sync 无处可去。Atomic=false = 直写目标（sync 自带 .part+rename）。

func (c *Ctrl) List(host, user, path string) ([]Item, error) {
	return c.backend().List(host, user, path)
}

func (c *Ctrl) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	return c.backend().ListMany(host, user, paths)
}

func (c *Ctrl) Home(host, user string) (string, error) { return c.backend().Home(host, user) }

func (c *Ctrl) Get(host, user, remote, local string) error {
	return c.backend().TransferGet(TransferRequest{Host: host, User: user, Remote: remote, Local: local}, nil)
}

func (c *Ctrl) GetRecursive(host, user, remote, local string) error {
	return c.backend().TransferGetTree(TransferRequest{Host: host, User: user, Remote: remote, Local: local}, nil)
}

func (c *Ctrl) Put(host, user, local, remote string) error {
	return c.backend().TransferPut(TransferRequest{Host: host, User: user, Local: local, Remote: remote}, nil)
}

func (c *Ctrl) PutRecursive(host, user, local, remoteDir string) error {
	return c.backend().TransferPutTree(TransferRequest{Host: host, User: user, Local: local, Remote: remoteDir}, nil)
}

func (c *Ctrl) Remove(host, user, path string) error { return c.backend().Remove(host, user, path) }

func (c *Ctrl) RemoveRecursive(host, user, path string) error {
	return c.backend().RemoveRecursive(host, user, path)
}

func (c *Ctrl) Mkdir(host, user, path string) error { return c.backend().Mkdir(host, user, path) }

func (c *Ctrl) Rename(host, user, oldPath, newPath string) error {
	return c.backend().Rename(host, user, oldPath, newPath)
}

// Connect/Search 是 app.go 仍在用的方法（SftpConnect / SftpSearch），门面必须暴露。
func (c *Ctrl) Connect(host, user string) error { return c.backend().Connect(host, user) }

func (c *Ctrl) Connected(host string) bool { return c.backend().Connected(host) }

func (c *Ctrl) Disconnect(host string) error { return c.backend().Disconnect(host) }

func (c *Ctrl) Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int,
	onProgress func(scanned int)) (SearchOutcome, error) {
	return c.backend().Search(ctx, host, user, root, pattern, maxDepth, limit, onProgress)
}

// SetJournalDir 由 app.go 装配时用 stateDir() 注入（goBackend 懒构造时传给 NewGoBackend）。
func (c *Ctrl) SetJournalDir(dir string) { c.journalDir = dir }

func (c *Ctrl) CloseAll() { c.backend().CloseAll() }

// —— 新传输面：Task 6+ 接线到 GoBackend；现在先委派 batch 以保证可编译 ——

func (c *Ctrl) TransferGet(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferGet(req, report)
}

func (c *Ctrl) TransferGetTree(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferGetTree(req, report)
}

func (c *Ctrl) TransferPut(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferPut(req, report)
}

func (c *Ctrl) TransferPutTree(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferPutTree(req, report)
}

func (c *Ctrl) Cancel(id string) bool { return false } // Task 10 接线

// backend 返回当前生效的后端。forced 仅供测试注入（见 ctrl_test.go 的门面委派用例）。
// Task 6 在此按 resolveTransport(c.sel) 分派到 GoBackend；本 task 只有 batch 一种。
func (c *Ctrl) backend() Backend {
	if c.forced != nil {
		return c.forced
	}
	return c.batch
}
