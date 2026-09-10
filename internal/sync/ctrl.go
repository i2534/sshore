package sync

import (
	"context"
	"fmt"
	"hash/fnv"
	"path"
	"sync"
	"time"

	"sshore/internal/config"
	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sshconn"
	"sshore/internal/watch"
)

// 【锁顺序不变式，违反即 ABBA 死锁】
//   - 允许的嵌套只有一种：state.mu → r.mu（align 里在 state.With 回调内加 r.mu）；
//   - **绝不允许先持 r.mu 再进 state.With / state.Flush**；
//   - 传输与任何 IO 都必须在锁外做（ResolveConflict 只入队就是这个原因）。
//
// Deps 是引擎的全部外部依赖，便于测试注入。
type Deps struct {
	Spawner   osutil.Streamer
	Runner    osutil.CtxRunner
	Transfer  Transferer
	ListMany  watch.ListManyFunc
	Emit      forward.EmitFunc
	After     func(time.Duration) <-chan struct{}
	StateDir  string
	BackoffFn func(attempt int) time.Duration
}

// SyncRuleStat 是卡片的活跃度数据。首轮对齐进度也在这里（这不属于状态圆点）。
type SyncRuleStat struct {
	Mode          string
	Reason        string
	PollIntervalS int
	Pending       int
	Done          int
	Failed        int
	Conflicts     int
	AlignScanned  int
	AlignTotal    int
	CurrentFile   string
	SourceMissing bool
	DeletePending int
	// DeleteFingerprint 是待确认删除的"轮次 + 路径集合指纹"，UI 必须原样回传
	// 给 ConfirmSyncRuleDeletes —— 否则确认闭环断裂（无法解除挂起状态）。
	DeleteFingerprint string
	DeletePaths       []string
}

type ruleRuntime struct {
	rule  config.SyncRule
	state *StateStore

	mu         sync.Mutex
	status     string // stopped|connecting|connected|reconnecting|error
	stats      SyncRuleStat
	cancel     chan struct{}
	src        watch.Source
	queue      map[string]watch.Kind
	wake       chan struct{}
	pendingDel []string
	resolved   map[string]Action // UI 裁决、待 loop 执行的下载动作（不重跑 Decide）
	delFP      string
	logCount   map[string]int
	firstRound bool      // 启动/重连/模式切换后的第一轮：禁止删除
	blockDels  bool      // 收到 root_gone / overflow：暂停删除直到对账确认
	stableAt   time.Time // 最近一次进入 connected 的时刻（防抖基准）
}

type Ctrl struct {
	d   Deps
	mu  sync.Mutex
	run map[string]*ruleRuntime
}

func NewCtrl(d Deps) *Ctrl {
	if d.After == nil {
		d.After = func(dur time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			go func() { time.Sleep(dur); close(ch) }()
			return ch
		}
	}
	if d.BackoffFn == nil {
		d.BackoffFn = backoffDelay
	}
	if d.Emit == nil {
		d.Emit = func(forward.Event) {}
	}
	return &Ctrl{d: d, run: map[string]*ruleRuntime{}}
}

// retryDelay 是**单文件**下载失败的重试节奏（1s / 4s / 16s），与 §8.2 的
// 连接退避是两套参数，不要合并。
func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Second
	case 2:
		return 4 * time.Second
	default:
		return 16 * time.Second
	}
}

// backoffDelay 与 forward 同一序列：1s 起、每次 ×2、30s 封顶。
func backoffDelay(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < 30*time.Second; i++ {
		d *= 2
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func (c *Ctrl) emit(ruleID, level, msg string) {
	c.d.Emit(forward.Event{
		SourceType: "sync",
		SourceID:   ruleID,
		TS:         time.Now().Format(time.RFC3339),
		Level:      level,
		Message:    msg,
	})
}

func (c *Ctrl) States() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for id, r := range c.run {
		r.mu.Lock()
		out[id] = r.status
		r.mu.Unlock()
	}
	return out
}

func (c *Ctrl) Stats() map[string]SyncRuleStat {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]SyncRuleStat{}
	for id, r := range c.run {
		r.mu.Lock()
		out[id] = r.stats
		r.mu.Unlock()
	}
	return out
}

func (c *Ctrl) Conflicts(id string) []Conflict {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return []Conflict{}
	}
	var out []Conflict
	r.state.With(func(d *StateFile) {
		out = append([]Conflict{}, d.Conflicts...)
	})
	if out == nil {
		return []Conflict{}
	}
	return out
}

func (c *Ctrl) fingerprintOf(rule config.SyncRule) Fingerprint {
	return Fingerprint{
		Host: rule.Host, User: rule.User, Kind: rule.Kind,
		RemoteRoot: rule.RemotePath, LocalRoot: rule.LocalPath,
		MaxDepth: rule.MaxDepth, Excludes: rule.Excludes,
	}
}

func (c *Ctrl) Start(rule config.SyncRule) error {
	if err := ValidateSyncRule(rule); err != nil {
		return err
	}
	c.mu.Lock()
	if _, exists := c.run[rule.ID]; exists {
		c.mu.Unlock()
		return fmt.Errorf("规则已在运行: %s", rule.ID)
	}
	st := NewStateStore(path.Join(c.d.StateDir, "sync-"+rule.ID+".json"), c.fingerprintOf(rule))
	c.mu.Unlock()
	if err := st.Load(); err != nil {
		return err
	}
	n, _ := CleanupParts(rule.LocalPath, rule.ID)
	if n > 0 {
		c.emit(rule.ID, "info", fmt.Sprintf("清理了 %d 个残留临时文件", n))
	}
	r := &ruleRuntime{
		rule: rule, state: st, status: "connecting",
		cancel: make(chan struct{}), queue: map[string]watch.Kind{}, wake: make(chan struct{}, 1),
	}
	c.mu.Lock()
	c.run[rule.ID] = r
	c.mu.Unlock()

	c.emit(rule.ID, "info", "监控启动中")
	go c.loop(r)
	return nil
}

func (c *Ctrl) Stop(id string) error {
	c.mu.Lock()
	r := c.run[id]
	if r != nil {
		delete(c.run, id)
	}
	c.mu.Unlock()
	if r == nil {
		return fmt.Errorf("规则未在运行: %s", id)
	}
	r.mu.Lock()
	if r.src != nil {
		_ = r.src.Close()
	}
	r.mu.Unlock()
	close(r.cancel)
	_ = r.state.Flush()
	c.emit(id, "info", "监控已停止")
	return nil
}

// loop 是规则的主 goroutine：建立探测 → 对齐 → 消费事件 → 断开后按 §8.2 退避。
// 所有传输都在本 goroutine 内串行执行。
func (c *Ctrl) loop(r *ruleRuntime) {
	attempt := 0
	for {
		select {
		case <-r.cancel:
			return
		default:
		}
		// spec §4.3：规则启动时确保 ControlMaster 存在。sftp 的 run() 用的是
		// ControlMaster=no（只复用不建立），不在这里建的话所有连接各自建连，
		// "探测与传输共用一条连接"这个设计前提就不成立。
		if c.d.Runner != nil {
			mctx, mcancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := sshconn.EnsureMaster(mctx, c.d.Runner, r.rule.Host, r.rule.User); err != nil {
				// 降级而非失败：功能正确，只是每条命令各自建连。
				c.emitThrottled(r, "master", "warn", "无法建立复用的 SSH 连接，将按命令独立连接："+err.Error())
			}
			mcancel()
		}
		info := watch.Detect(context.Background(), c.d.Runner, watch.DetectOpts{
			Host: r.rule.Host, User: r.rule.User, RemotePath: r.rule.RemotePath,
			ForcePoll: r.rule.ForcePoll, PollInterval: time.Duration(r.rule.PollIntervalS) * time.Second,
		})
		src, err := c.newSource(r, info)
		if err != nil {
			r.mu.Lock()
			r.status = "error"
			r.mu.Unlock()
			c.emit(r.rule.ID, "error", "无法启动探测："+err.Error())
			return
		}
		r.mu.Lock()
		r.src = src
		r.status = "connected"
		r.stableAt = time.Now()
		r.stats.Mode = info.Mode
		r.stats.Reason = info.Reason
		r.stats.PollIntervalS = r.rule.PollIntervalS
		r.mu.Unlock()
		if info.Mode == "poll" {
			c.emit(r.rule.ID, "warn", "已降级为轮询："+info.Reason)
		} else {
			c.emit(r.rule.ID, "info", "监控已启动：inotify")
		}

		// 首轮全量对齐（含重连后的对账：以 entries 的 remote_* 为基线）。
		r.mu.Lock()
		r.firstRound = true
		r.mu.Unlock()
		c.align(r)

		ch, err := src.Start(context.Background())
		if err != nil {
			r.mu.Lock()
			r.status = "error"
			r.mu.Unlock()
			c.emit(r.rule.ID, "error", "探测进程启动失败："+err.Error())
			return
		}
		dropped := c.consume(r, ch)
		_ = src.Close()
		if dropped {
			// 轮询连续失败达到阈值 ⇒ 配置/权限类问题，进 error 而不是无限重连。
			// 启动期致命错误（远端路径不存在 / watch 配额耗尽）：配置错，进 error
			// 且不重连——无限重试只会掩盖问题并刷屏。
			if is, ok := src.(*watch.InotifySource); ok {
				if e := is.Err(); e != nil {
					r.mu.Lock()
					r.status = "error"
					r.mu.Unlock()
					c.emit(r.rule.ID, "error", e.Error())
					return
				}
			}
			if ps, ok := src.(*watch.PollSource); ok && ps.Failures() >= 5 {
				r.mu.Lock()
				r.status = "error"
				r.mu.Unlock()
				c.emit(r.rule.ID, "error", "连续多轮扫描失败，已停止")
				return
			}
			// 通道关闭 = 探测断开。启动失败不重连，运行中断开才重连。
			r.mu.Lock()
			noReconnect := !r.rule.Reconnect()
			r.status = "reconnecting"
			if noReconnect {
				r.status = "error"
			}
			r.mu.Unlock()
			if noReconnect {
				c.emit(r.rule.ID, "error", "监控连接断开，且未开启自动重连")
				return
			}
			// 连续稳定在线满 60s 就清零计数（对齐 forward 的 stableThreshold 语义），
			// 否则多次独立抖动会让退避永久停在 30s。
			r.mu.Lock()
			stable := time.Since(r.stableAt) >= 60*time.Second
			r.mu.Unlock()
			if stable {
				attempt = 0
			}
			attempt++
			delay := c.d.BackoffFn(attempt)
			c.emit(r.rule.ID, "warn", fmt.Sprintf("监控连接断开，%s 后进行第 %d 次重连", delay, attempt))
			select {
			case <-r.cancel:
				return
			case <-c.d.After(delay):
			}
			continue
		}
		return
	}
}

func (c *Ctrl) newSource(r *ruleRuntime, info watch.Info) (watch.Source, error) {
	opts := watch.DetectOpts{
		Host: r.rule.Host, User: r.rule.User, RemotePath: r.rule.RemotePath,
		ForcePoll: r.rule.ForcePoll, PollInterval: time.Duration(r.rule.PollIntervalS) * time.Second,
	}
	logf := func(level, msg string) { c.emit(r.rule.ID, level, msg) }
	// kind=file 强制轮询：inotify 下根路径就是那个文件，%w%f 的 rel 恒为空串，
	// CLOSE_WRITE/MODIFY 会被当作根目录噪声丢弃 —— 写事件永远拿不到。
	if info.Mode == "inotify" && c.d.Spawner != nil && r.rule.Kind != "file" {
		return watch.NewInotifySource(c.d.Spawner, opts, logf), nil
	}
	if info.Mode == "inotify" && r.rule.Kind == "file" {
		c.emit(r.rule.ID, "warn", "单文件规则使用轮询探测（inotify 无法提供该文件的写事件）")
	}
	if c.d.ListMany == nil {
		return nil, fmt.Errorf("缺少 ListMany 依赖")
	}
	return watch.NewPollSource(c.d.ListMany, opts, r.rule.MaxDepth, r.rule.Excludes, nil, logf), nil
}

// consume 消费事件直到通道关闭，返回 true 表示"断开"（需要重连）。
// 它同时承担 300ms 去抖与批量处理；解析侧只做入队，不做 IO。
func (c *Ctrl) consume(r *ruleRuntime, ch <-chan watch.Event) bool {
	timer := time.NewTimer(300 * time.Millisecond)
	if !timer.Stop() {
		<-timer.C
	}
	dirty := false
	for {
		select {
		case <-r.cancel:
			return false
		case ev, ok := <-ch:
			if !ok {
				return true
			}
			r.mu.Lock()
			if ev.Kind == watch.KindOverflow || ev.Kind == watch.KindRootGone {
				// 强制对账：把整棵远端树重新比一遍，并暂停删除直到确认。
				r.stats.SourceMissing = ev.Kind == watch.KindRootGone
				r.blockDels = true
				r.mu.Unlock()
				c.align(r)
				continue
			}
			if ev.Kind == watch.KindDirAdded || ev.Kind == watch.KindDirGone {
				// 目录事件必须触发子树对账（mv 进来的目录内文件是零事件的）。
				r.mu.Unlock()
				c.align(r)
				continue
			}
			r.queue[ev.RelPath] = ev.Kind
			overflowed := len(r.queue) > maxQueuePaths
			if overflowed {
				r.queue = map[string]watch.Kind{}
				r.resolved = map[string]Action{}
			}
			r.mu.Unlock()
			if overflowed {
				c.emit(r.rule.ID, "warn", "待处理路径过多，转为全量对账")
				c.align(r)
				continue
			}
			if !dirty {
				timer.Reset(300 * time.Millisecond)
				dirty = true
			}
		case <-r.wake:
			// UI 裁决 take_remote/save_as 后唤醒：走同一条去抖路径，不另开传输
			// （传输只能在规则 goroutine 内串行发生）。
			if !dirty {
				timer.Reset(300 * time.Millisecond)
				dirty = true
			}
		case <-timer.C:
			dirty = false
			c.drainQueue(r)
		}
	}
}

// drainQueue 取出去抖后的路径集合，补齐远端元信息，逐条决策并串行传输。
func (c *Ctrl) drainQueue(r *ruleRuntime) {
	r.mu.Lock()
	batch := r.queue
	r.queue = map[string]watch.Kind{}
	// 取出与本批对应的用户裁决，并从 map 中摘除（每批只消费一次）。
	resolved := map[string]Action{}
	pending := map[string]watch.Kind{}
	for rel, kind := range batch {
		if act, ok := r.resolved[rel]; ok {
			resolved[rel] = act
			delete(r.resolved, rel)
		} else {
			pending[rel] = kind
		}
	}
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	// UI 裁决不依赖远端元信息，先执行：即使随后补齐元信息失败也不会丢用户的决定。
	for rel, act := range resolved {
		r.mu.Lock()
		r.stats.CurrentFile = rel
		r.mu.Unlock()
		c.applyResolved(r, rel, act)
		r.mu.Lock()
		r.stats.CurrentFile = ""
		r.mu.Unlock()
	}
	if len(resolved) > 0 {
		_ = r.state.Flush()
	}
	if len(pending) > 0 {
		metas, metaErr := c.fetchMeta(r, pending)
		if metaErr != nil {
			c.emit(r.rule.ID, "warn", "远端元信息补齐失败，本批跳过："+metaErr.Error())
			return
		}
		for rel, kind := range pending {
			r.mu.Lock()
			r.stats.CurrentFile = rel
			r.mu.Unlock()
			c.applyOne(r, rel, kind, metas[rel])
			r.mu.Lock()
			r.stats.CurrentFile = ""
			r.mu.Unlock()
		}
	}
	_ = r.state.Flush()
	// 事件路径的删除也必须过闸门 —— 只登记不执行等于 mirror_delete 失效。
	r.mu.Lock()
	hasDels := len(r.pendingDel) > 0
	r.mu.Unlock()
	if hasDels {
		c.maybeDelete(r, -1) // -1：本轮远端条目数未知（事件路径）
	}
}

func (c *Ctrl) fetchMeta(r *ruleRuntime, batch map[string]watch.Kind) (map[string]*Entry, error) {
	out := map[string]*Entry{}
	if c.d.ListMany == nil {
		return out, nil
	}
	// 注意：不做批量路径拼接的猜测——逐个文件 ls 由 ListMany 承担，
	// 它内部仍是一个 sftp 批处理（约定见 Task 3）。
	paths := make([]string, 0, len(batch))
	for rel := range batch {
		paths = append(paths, path.Join(r.rule.RemotePath, rel))
	}
	res, err := c.d.ListMany(r.rule.Host, r.rule.User, paths)
	if err != nil {
		return out, err
	}
	for rel := range batch {
		items, ok := res[path.Join(r.rule.RemotePath, rel)]
		if !ok || len(items) == 0 {
			continue // 未知：Decide 会保守下载
		}
		out[rel] = &Entry{RemoteSize: items[0].Size, RemoteMTime: items[0].ModTime}
	}
	return out, nil
}

// align 做一次全量对账：扫描远端，与 entries 的 remote_* 比对（含删除）。
// 首轮（无 entries）时按决策表逐条走"下载/采纳/冲突"。
// maxQueuePaths 是去重队列的上限。超过就退化为"需要全量对账"，而不是继续堆积
// 或阻塞解析侧——解析侧一旦阻塞，本地 ssh 的 stdout 管道写满会反过来阻塞远端
// inotifywait，最终导致内核队列溢出、事件被静默丢弃。
const maxQueuePaths = 5000

// emitThrottled 抑制重复刷屏：同一 key 的同类消息每 5 次才记一条（spec §8.3）。
func (c *Ctrl) emitThrottled(r *ruleRuntime, key, level, msg string) {
	r.mu.Lock()
	if r.logCount == nil {
		r.logCount = map[string]int{}
	}
	r.logCount[key]++
	n := r.logCount[key]
	r.mu.Unlock()
	if n == 1 || n%5 == 0 {
		c.emit(r.rule.ID, level, msg)
	}
}

func (c *Ctrl) align(r *ruleRuntime) {
	if c.d.ListMany == nil {
		return
	}
	if r.rule.Kind == "file" {
		c.alignFile(r)
		return
	}
	snap, err := watch.ScanTree(context.Background(), c.d.ListMany, r.rule.Host, r.rule.User, r.rule.RemotePath, r.rule.MaxDepth, r.rule.Excludes)
	if err != nil || !snap.Complete {
		c.emit(r.rule.ID, "warn", "对账扫描未完成，跳过本轮")
		return
	}
	r.mu.Lock()
	r.stats.AlignTotal = len(snap.Entries)
	r.mu.Unlock()
	var dels []string
	var changed []string
	r.state.With(func(d *StateFile) {
		for rel, meta := range snap.Entries {
			ent := d.Entries[rel]
			if ent == nil {
				// 远端新增：登记远端字段（Local 字段留空 ⇒ HasLocal=false）
				d.Entries[rel] = &Entry{RemoteSize: meta.Size, RemoteMTime: meta.ModTime}
				changed = append(changed, rel)
			} else if ent.RemoteSize != meta.Size || ent.RemoteMTime != meta.ModTime {
				// **只在远端真的变了时才处理**。原来的写法无条件覆盖 Remote*
				// 再对全部条目调 applyOne，而 Decide 的"有基线且本地未改"分支
				// 恒返回 GET —— 结果是每次对账都重下整棵树，且 §7.8 要求的
				// "本轮快照 vs entries.remote_*" diff 在覆盖后已无从比较。
				ent.RemoteSize, ent.RemoteMTime = meta.Size, meta.ModTime
				changed = append(changed, rel)
			}
			r.mu.Lock()
			r.stats.AlignScanned++
			r.mu.Unlock()
		}
		for rel, ent := range d.Entries {
			if _, ok := snap.Entries[rel]; !ok && ent.HasLocal {
				dels = append(dels, rel)
			}
		}
	})
	r.mu.Lock()
	r.pendingDel = dels
	// 指纹 = 轮次号 + 路径集合摘要：路径集合一变，指纹就变，确认即失效。
	h := fnv.New64a()
	for _, rel := range dels {
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
	}
	r.delFP = fmt.Sprintf("%d-%x", time.Now().UnixNano(), h.Sum64())
	r.stats.DeletePaths = append([]string{}, dels...)
	r.logCount = map[string]int{} // 每轮对账后重置节流计数
	r.blockDels = false           // 完整对账成功 ⇒ 解除删除暂停（firstRound 已由闸门消费）
	r.stats.DeletePending = len(dels)
	r.stats.DeleteFingerprint = r.delFP
	r.stats.Pending = len(snap.Entries)
	r.mu.Unlock()
	_ = r.state.Flush()
	// 只处理新增/变化的条目；remote_* 已在上面写进 entries，applyOne 从状态里读。
	for _, rel := range changed {
		c.applyOne(r, rel, watch.KindWrite, nil)
	}
	c.maybeDelete(r, len(snap.Entries))
}

// alignFile 处理 kind=file：不做目录 BFS，只盯着那一个文件。
// 远端源文件消失时**本地保留不动、规则不进 error**（文件很可能稍后回来），
// 且**不受 mirror_delete 影响**——"源消失"更可能是路径配错或文件被临时挪走。
func (c *Ctrl) alignFile(r *ruleRuntime) {
	res, err := c.d.ListMany(r.rule.Host, r.rule.User, []string{r.rule.RemotePath})
	if err != nil {
		c.emitThrottled(r, "meta", "warn", "远端元信息读取失败："+err.Error())
		return
	}
	items, ok := res[r.rule.RemotePath]
	if !ok || len(items) == 0 {
		r.mu.Lock()
		r.stats.SourceMissing = true
		r.mu.Unlock()
		c.emitThrottled(r, "missing", "warn", "远端源文件缺失，本地保留不动")
		return
	}
	r.mu.Lock()
	r.stats.SourceMissing = false
	r.mu.Unlock()
	c.applyOne(r, path.Base(r.rule.RemotePath), watch.KindWrite,
		&Entry{RemoteSize: items[0].Size, RemoteMTime: items[0].ModTime})
}

// applyOne 走决策表并执行动作。全程在规则 goroutine 内 ⇒ 传输天然串行。
func (c *Ctrl) applyOne(r *ruleRuntime, rel string, kind watch.Kind, remote *Entry) {
	c.applyAction(r, rel, kind, remote, ActionSkip, false)
}

// applyResolved 执行用户在冲突卡片上裁决的动作，**不重跑 Decide**：
// Decide 是纯决策表、没有"用户意图"输入，对同一个冲突状态只会再次返回 Conflict，
// 会让 take_remote / save_as 永远无法落地。
func (c *Ctrl) applyResolved(r *ruleRuntime, rel string, action Action) {
	c.applyAction(r, rel, watch.KindWrite, nil, action, true)
}

// applyAction 是 applyOne / applyResolved 的共同实现：hasForced 为真时直接执行
// forced（用户裁决），否则才走 Decide。
func (c *Ctrl) applyAction(r *ruleRuntime, rel string, kind watch.Kind, remote *Entry, forced Action, hasForced bool) {
	local, err := LocalTarget(r.rule.LocalPath, rel)
	if err != nil {
		c.emit(r.rule.ID, "error", "拒绝非法路径："+rel)
		return
	}
	st, _ := osStat(local)
	var ent *Entry
	r.state.With(func(d *StateFile) {
		if e, ok := d.Entries[rel]; ok {
			ent = e
		}
		if ent == nil && remote != nil {
			ent = remote
		} else if ent != nil && remote != nil {
			ent.RemoteSize, ent.RemoteMTime = remote.RemoteSize, remote.RemoteMTime
		}
		_ = d
	})
	var action Action
	var reason string
	if hasForced {
		action, reason = forced, "用户裁决"
	} else {
		action, reason = Decide(kind, ent, st, r.rule.MirrorDelete)
	}
	switch action {
	case ActionGet, ActionSaveAs:
		remotePath := path.Join(r.rule.RemotePath, rel)
		target := local
		saveAs := action == ActionSaveAs
		if saveAs {
			// 另存为：远端版本写到 <name>.remote-<ts>，**本地原文件保留**，
			// 且**不更新基线**（原文件根本没有变化）。
			target = local + ".remote-" + time.Now().Format("20060102-150405")
		}
		// 单文件失败退避重试 3 次（1s/4s/16s）；仍失败则标记并**继续处理其它文件**，
		// 一个权限错误的文件不该让整条规则停摆。
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-r.cancel:
					return
				case <-c.d.After(retryDelay(attempt)):
				}
			}
			if lastErr = TransferTo(c.d.Transfer, r.rule.Host, r.rule.User, remotePath, target, r.rule.ID, nil); lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			c.emit(r.rule.ID, "error", "下载 "+rel+" 失败（已重试 3 次）："+lastErr.Error())
			r.mu.Lock()
			r.stats.Failed++
			r.mu.Unlock()
			r.state.With(func(d *StateFile) {
				d.Failed = append(d.Failed, FailedItem{RelPath: rel, Err: lastErr.Error(),
					At: time.Now().Format(time.RFC3339)})
			})
			return
		}
		if saveAs {
			r.state.With(func(d *StateFile) { removeConflict(d, rel) })
			c.emit(r.rule.ID, "info", "远端副本已另存为 "+path.Base(target))
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		after, statErr := osStat(local)
		if statErr != nil {
			// 不能把零值当成"基线"写进去：那会让下一次比对恒不等，刷出假冲突。
			c.emit(r.rule.ID, "warn", "下载后无法读取本地状态，本条暂不登记基线："+rel)
			return
		}
		r.state.With(func(d *StateFile) {
			e := d.Entries[rel]
			if e == nil {
				e = &Entry{}
				d.Entries[rel] = e
			}
			e.LocalSize, e.LocalMTime, e.HasLocal, e.WrittenAt = after.Size, after.ModTime, true, now
			removeConflict(d, rel)
		})
		r.mu.Lock()
		r.stats.Done++
		r.mu.Unlock()
		c.emit(r.rule.ID, "info", "下载 "+rel+"（"+reason+"）")
	case ActionAdopt:
		r.state.With(func(d *StateFile) {
			e := d.Entries[rel]
			if e == nil {
				e = &Entry{}
				d.Entries[rel] = e
			}
			e.LocalSize, e.LocalMTime, e.HasLocal, e.Adopted = st.Size, st.ModTime, st.Exists, true
		})
		c.emit(r.rule.ID, "info", rel+" 本地已存在且大小一致，未下载（未校验内容）")
	case ActionConflict:
		r.state.With(func(d *StateFile) {
			UpsertConflict(d, Conflict{RelPath: rel, RemoteSize: ent.RemoteSize, RemoteMTime: ent.RemoteMTime,
				LocalSize: st.Size, LocalMTime: st.ModTime, DetectedAt: time.Now().Format(time.RFC3339)})
		})
		r.mu.Lock()
		r.stats.Conflicts++
		r.mu.Unlock()
		c.emit(r.rule.ID, "warn", rel+" 本地已修改，未覆盖")
	case ActionDelete:
		// 绝不在 applyOne 里直接删：删除必须统一过六重闸门。这里只登记，
		// 由 maybeDelete 在闸门放行后才真正执行。
		r.mu.Lock()
		r.pendingDel = append(r.pendingDel, rel)
		r.stats.DeletePending = len(r.pendingDel)
		r.mu.Unlock()
		c.emit(r.rule.ID, "info", "待删除本地 "+rel+"（"+reason+"）")
	}
}

// maybeDelete 在删除闸门放行时才删本地文件。批次在 r.mu 下取好快照后交给
// executeDeletes —— 后者不再回读 r.pendingDel，避免并发对账换掉清单。
func (c *Ctrl) maybeDelete(r *ruleRuntime, curCount int) {
	r.mu.Lock()
	batch := append([]string{}, r.pendingDel...)
	pending := len(batch)
	first, blocked := r.firstRound, r.blockDels
	r.firstRound = false // 第一轮只放行一次
	r.mu.Unlock()
	res := EvaluateDeleteGate(DeleteGateInput{
		MirrorDelete: r.rule.MirrorDelete, Complete: true,
		FirstRound: first, RootGone: blocked, Overflow: blocked,
		CountKnown: curCount >= 0, PrevCount: curCount + pending, CurCount: curCount, PendingCount: pending,
	})
	if !res.Allowed {
		if res.NeedsConfirm {
			c.emit(r.rule.ID, "warn", fmt.Sprintf("本轮待删 %d 个文件，已挂起等待确认", pending))
		} else if pending > 0 {
			c.emit(r.rule.ID, "warn", "已禁止删除："+res.Reason)
		}
		return
	}
	// 闸门放行：清空清单与确认指纹（批次已快照），再执行这一批。
	r.mu.Lock()
	r.pendingDel = nil
	r.delFP = ""
	r.stats.DeletePending = 0
	r.stats.DeleteFingerprint = ""
	r.mu.Unlock()
	c.executeDeletes(r, batch)
}

// executeDeletes 是两条删除路径（对账/事件、用户确认）的**唯一**收口点。
// batch 必须由调用方在 r.mu 下快照，本方法不回读 r.pendingDel：并发 align 换掉
// 清单时回读会删掉用户从未确认的那一批。每个文件删除前都要重新比对本地与基线，
// 本地被改过的一律保留（与事件路径 Decide 的语义对齐）。
func (c *Ctrl) executeDeletes(r *ruleRuntime, batch []string) {
	for _, rel := range batch {
		local, err := LocalTarget(r.rule.LocalPath, rel)
		if err != nil {
			continue
		}
		// 本地状态必须在锁外读取：StateStore.With 的回调里禁止任何 IO。
		st, statErr := osStat(local)
		if statErr != nil || !st.Exists {
			// 本地也已不存在 ⇒ 对齐 Decide 的 both-gone Skip，条目保留不动。
			continue
		}
		keep := false
		r.state.With(func(d *StateFile) {
			e := d.Entries[rel]
			if e != nil && e.HasLocal && (st.Size != e.LocalSize || st.ModTime != e.LocalMTime) {
				keep = true
			}
		})
		if keep {
			c.emit(r.rule.ID, "warn", "本地文件已被修改，保留不删："+rel)
			continue
		}
		if err := osRemove(local); err != nil {
			// 删除失败绝不能顺手删条目：否则该文件成为永久孤儿，再也不会被对账到。
			c.emit(r.rule.ID, "warn", "删除本地文件失败："+rel+" "+err.Error())
			continue
		}
		r.state.With(func(d *StateFile) { delete(d.Entries, rel) })
	}
	_ = r.state.Flush()
}

// ResolveConflict 只写队列并唤醒规则 goroutine，**绝不自己传输**。
func (c *Ctrl) ResolveConflict(id, rel string, action ConflictAction, local LocalState) error {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return fmt.Errorf("规则未在运行: %s", id)
	}
	var req TransferOrDelete
	var err error
	r.state.With(func(d *StateFile) {
		req, err = ResolveConflict(d, rel, action, local, time.Now().Format(time.RFC3339))
	})
	if err != nil {
		return err
	}
	_ = r.state.Flush()
	if req.Action == ActionGet || req.Action == ActionSaveAs {
		// 记下用户裁决：loop 在 drainQueue 里直接执行它，而不是让 Decide 重判
		//（Decide 对同一冲突状态只会再次返回 Conflict，take_remote/save_as 永不落地）。
		r.mu.Lock()
		if r.resolved == nil {
			r.resolved = map[string]Action{}
		}
		r.resolved[rel] = req.Action
		r.queue[rel] = watch.KindWrite
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// ConfirmDeletes 执行挂起的删除；执行前**逐条重新校验**远端仍不存在，
// 任何一条已恢复即整批作废（挂载点恢复后用户在陈旧卡片上点确认，
// 语义上就是删一批本不该删的文件）。
func (c *Ctrl) ConfirmDeletes(id, fingerprint string) error {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return fmt.Errorf("规则未在运行: %s", id)
	}
	r.mu.Lock()
	if r.delFP != fingerprint {
		r.mu.Unlock()
		return fmt.Errorf("确认已过期：删除清单已变化，请重新查看")
	}
	batch := append([]string{}, r.pendingDel...)
	r.mu.Unlock()
	metas, err := c.fetchMeta(r, func() map[string]watch.Kind {
		m := map[string]watch.Kind{}
		for _, rel := range batch {
			m[rel] = watch.KindDelete
		}
		return m
	}())
	if err != nil {
		// 无法确认远端状态时**绝不删**：一次瞬时 sftp 故障就会删掉远端其实仍在的文件。
		return fmt.Errorf("无法确认远端状态，已取消本批删除: %w", err)
	}
	for rel := range metas {
		if metas[rel] != nil {
			c.emit(r.rule.ID, "warn", "远端文件已恢复，取消整批删除："+rel)
			return nil
		}
	}
	// fetchMeta 是锁外网络往返，期间并发 align 可能换掉清单。删除前**重新**校验
	// 指纹并取最后一份快照，保证删掉的正是用户确认过的那一批。
	r.mu.Lock()
	if r.delFP != fingerprint {
		r.mu.Unlock()
		return fmt.Errorf("确认已过期：删除清单已变化，请重新查看")
	}
	final := append([]string{}, r.pendingDel...)
	r.pendingDel = nil
	r.delFP = ""
	r.stats.DeletePending = 0
	r.stats.DeleteFingerprint = ""
	r.mu.Unlock()
	c.executeDeletes(r, final)
	return nil
}

func removeConflict(d *StateFile, rel string) {
	for i := range d.Conflicts {
		if d.Conflicts[i].RelPath == rel {
			d.Conflicts = append(d.Conflicts[:i], d.Conflicts[i+1:]...)
			return
		}
	}
}
