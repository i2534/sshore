package sftp

import (
	"context"
	"sync"
	"time"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// Ctrl 是门面：legacy 四参面（sync 与既有测试用）+ 新传输面（绑定层用）。
// 两种后端（BatchBackend / GoBackend）都挂在它后面，由 backend() 选择。
type Ctrl struct {
	batch      *BatchBackend
	goBackend  *GoBackend // 懒构造：backend() 按 resolveTransport 分派后缓存
	sel        TransportSelector
	emit       forward.EmitFunc
	journalDir string
	forced     Backend // 仅供测试注入：backend() 优先返回它

	// probeMu 保护 probing：batch 后端探活的每 host 单飞表（规格 §8.2 / 审核 I5）。
	// 只保护 map 本身，绝不在持锁时等结果（等待必须在放锁之后）。
	probeMu sync.Mutex
	probing map[string]*probeFlight // host → 在飞的那次 batch 探活
}

// NewCtrl 保持 spec D3 的两参签名（env + 内置默认），既有调用点无需改动。
// 评审 M1：这里也必须填充 emit —— backend() 懒构造 GoBackend 时会把它传下去，
// 留空会让 GoBackend 的日志静默丢失。
func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{batch: NewBatchBackend(r, emit), emit: emit}
}

// NewCtrlWith 由 app.go 注入懒解析选择器（spec D3）。
func NewCtrlWith(r osutil.Runner, emit forward.EmitFunc, sel TransportSelector) *Ctrl {
	c := NewCtrl(r, emit)
	c.sel = sel
	return c
}

// NewCtrlForcedBackend 返回一个**强制使用给定后端**的门面。forced 字段不可导出，包外
// （package main）用例只能通过它注入测试后端。
//
// ⚠️ 测试专用（test-only）：调用点只有 app_test.go 的 TestSftpBindingsPassAtomicTrueWhenBackendCapable
// 与 TestSftpProbeFalseWhenListFails。生产装配一律走 NewCtrl（batch）或
// NewCtrlWith（按选择器懒解析），**任何生产代码都不得调用本函数** —— 它绕过了
// resolveTransport 的选择器语义。review 可据此核查：grep 调用点时只应命中 _test.go。
//
// 为什么必须导出：Task 9 评审 I1 要求有一条 hermetic 用例钉住绑定层的 Atomic 决策 ——
// 需要一个 AtomicCapable=true 的记录型后端，才能证明完整能力链
// Backend.AtomicCapable ⇒ Ctrl.AtomicCapable ⇒ App.sftpAtomic ⇒ 绑定的 TransferRequest.Atomic。
func NewCtrlForcedBackend(b Backend) *Ctrl { return &Ctrl{forced: b} }

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

// SetJournalDir 由 app.go 装配时用 stateDir() 注入；若 GoBackend 已被懒构造，
// 同步补进去，避免「先发生一次 sftp 操作、后设置 journal 目录」时丢配置。
func (c *Ctrl) SetJournalDir(dir string) {
	c.journalDir = dir
	if c.goBackend != nil {
		// 转发到 GoBackend.SetJournalDir（而不是裸写 journalDir）：这样 journal 实例
		// 会一并建立/清空，Put 的 commitRemote 才真的能落 journal（Task 8 接线）。
		c.goBackend.SetJournalDir(dir)
	}
}

func (c *Ctrl) CloseAll() { c.backend().CloseAll() }

// RecoverSwaps 转发 backup-swap 崩溃恢复（Task 13）：按决策表恢复中断现场，返回处理条目数。
// 只有 GoBackend 有 journal；batch 后端无 journal ⇒ 恒 (0, nil)（诚实 no-op，绝不报错）。
func (c *Ctrl) RecoverSwaps() (int, error) {
	if g, ok := c.backend().(*GoBackend); ok {
		return g.RecoverSwaps()
	}
	return 0, nil
}

// CleanupParts 转发「删掉已知 .part」（Task 13）：只有 GoBackend 维护登记表。
func (c *Ctrl) CleanupParts() {
	if g, ok := c.backend().(*GoBackend); ok {
		g.CleanupParts()
	}
}

// —— Task 2：真探活 ——

// GoBackend.Probe 转发池探针（真实 ReadDirContext(".") 往返 + probeTimeout=5s +
// 失败丢弃死会话）。定义在这里而不是 gosftp.go：它与下面 Ctrl.Probe 的分派是同一处
// 逻辑，放一起便于对照维护（同包内方法可定义在任何文件）。
//
// 调用方必读的两条：
//  1. Probe 经 markSessionConnected 会把**粘性** connected[host] 置 true（gosftp.go 的
//     markConnected）——「探针答 false」与「SftpConnected 答 true」会同时成立，所以降级
//     路径必须再调 SftpDisconnect 清连接意图，Probe 绝非纯只读。
//  2. batch 回退分支（见 Ctrl.Probe）的残留 —— 挂死的 batch 子进程（cmd.Run() 无超时）
//     本身靠 ssh 自身的超时回收，本轮不为 legacy 后端改 runner 装配（规格 R8）。
func (g *GoBackend) Probe(ctx context.Context, host, user string) bool {
	return g.pool.Probe(ctx, host, user)
}

// probeFlight 是 batch 探活的一次「在飞」记录：done 关闭即代表结果就绪，ok 是结果本身。
//
// 为什么不是裸的 chan bool：一个值只能被一个等待者收到，第 2 个之后的等待者会在该 chan 上
// 干等（前端 probeTimed 每 15s 超时后会再发一次探活，同 host 多个等待者是可达的）；
// 而「关闭 done + 读字段」是一次广播，任意多个等待者都能拿到同一个结果，且不存在
// 「先交付结果、后清除条目」之间新来的调用者等一条不会再交付的结果的窗口。
// 语义与规格 §8.2 / 审核 I5 完全一致：共享在飞结果，绝不「在飞即 false」。
type probeFlight struct {
	done chan struct{}
	ok   bool
}

// Probe 是绑定层唯一入口的真探活（规格 §8.2）。
//
// GoBackend 有池化会话 ⇒ 直接走 pool.Probe（真实往返回合 + probeTimeout 预算 + 失败丢弃
// 死会话）。其它后端（legacy batch）没有 ctx 感知的 runner（batch.go 的 osutil.Runner 是
// 无 ctx 的 cmd.Run()，改成 CtxRunner 会牵动整条 legacy 装配、不成比例）⇒ 用 goroutine +
// ctx select 给调用方一个上界：否则子进程一挂住，前端 await 永不返回、probing 恒 true，
// 此后所有切标签探活被静默短路（规格 R8）。
//
// 分派用类型断言而不是给 Backend 接口加方法，理由照 RecoverSwaps/CleanupParts 先例
// （只有 GoBackend 有的能力就在 Ctrl 层断言，避免波及测试替身）。
func (c *Ctrl) Probe(ctx context.Context, host, user string) bool {
	if g, ok := c.backend().(*GoBackend); ok {
		return g.Probe(ctx, host, user)
	}
	return c.probeBatch(ctx, host, user)
}

// probeBatch 是 legacy batch 后端的探活：一次真实列目录（成功即 true），带 ctx 上界，
// 并且同一 host 的并发调用**共享在飞的同一个结果**（审核 I5）——「在飞即 false」会被前端
// 解释成 'lost'，对可能健康的链路做无谓的 SftpDisconnect + 重连（违反 D14：只有探针说
// 失效才算失效）。等待本身不加自己的超时，由前端 probeTimed 的 15s 兜底（超时=未知）。
func (c *Ctrl) probeBatch(ctx context.Context, host, user string) bool {
	c.probeMu.Lock()
	if f, ok := c.probing[host]; ok {
		c.probeMu.Unlock()
		select {
		case <-f.done:
			return f.ok
		case <-ctx.Done():
			return false
		}
	}
	f := &probeFlight{done: make(chan struct{})}
	if c.probing == nil {
		c.probing = map[string]*probeFlight{}
	}
	c.probing[host] = f
	c.probeMu.Unlock()

	go func() {
		_, err := c.List(host, user, "/")
		// 先摘条目再交付结果：新来的调用者会自己另起一次真实探活（不共享一条已交付的
		// 结果、也不会等一条永不再交付的结果）；已在等的等待者靠 close(done) 广播醒来。
		c.probeMu.Lock()
		delete(c.probing, host)
		c.probeMu.Unlock()
		f.ok = err == nil
		close(f.done)
	}()

	select {
	case <-f.done:
		return f.ok
	case <-ctx.Done():
		// 调用方（前端 probeTimed / 绑定层）等不及了：答 false，但在飞的 goroutine 会
		// 自己跑完并清掉条目 —— 绝不留下「永久在飞」的 host（否则自愈静默失效）。
		return false
	}
}

// —— 新传输面：一律走 c.backend()（Task 6 起按 resolveTransport 分派）——

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

// Cancel 转发到当前后端（Task 9）：绑定的 SftpTransferCancel(id) 唯一入口。
// 真实取消（关该传输的会话）由 Task 10 的 GoBackend 注册表实现；batch 恒 false（幂等）。
func (c *Ctrl) Cancel(id string) bool { return c.backend().Cancel(id) }

// AtomicCapable 回答「当前选中的后端是否支持 .part + 提交」。
// 绑定层必须据此填 TransferRequest.Atomic（能力驱动，绝不写死）：batch 传 true 会被
// guardAtomic 硬拒（Task 6 的刻意裁决），写死 true 会让默认传输全盘失败。
func (c *Ctrl) AtomicCapable() bool { return c.backend().AtomicCapable() }

// TransportKind 返回当前选择器解析出的后端种类（评审 I2：让「选了哪个后端」可观测，
// 杜绝开关静默 no-op）。测试直接读它，运行期由 backend() 记录一条日志。
func (c *Ctrl) TransportKind() BackendKind { return resolveTransport(c.sel) }

// backend 返回当前生效的后端。forced 仅供测试注入（见 ctrl_test.go 的门面委派用例）。
// Task 6：按 resolveTransport(c.sel) 分派；env 变化会让下一次 backend() 立即改选，
// GoBackend 首次用到时才构造并缓存。
func (c *Ctrl) backend() Backend {
	if c.forced != nil {
		return c.forced
	}
	if c.TransportKind() == KindGo {
		if c.goBackend == nil {
			c.goBackend = NewGoBackend(c.sel, c.emit)
			c.goBackend.SetJournalDir(c.journalDir)
			if c.emit != nil {
				c.emit(forward.Event{
					SourceType: "sftp",
					TS:         time.Now().Format(time.RFC3339),
					Level:      "info",
					Message:    "sftp transport=gosftp",
				})
			}
		}
		return c.goBackend
	}
	return c.batch
}
