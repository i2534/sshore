# 自动重连（Auto-Reconnect）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enabled 且 AutoReconnect 的隧道在 ssh 进程意外退出后按指数退避自动重连，直至成功或用户停止；前端四态圆点实时呈现。

**Architecture:** forward.Ctrl 状态机新增 `reconnecting` 态；`watchExit` 分支进入 `respawnLoop`（无限退避 1s×2 封顶 30s，仅意外退出触发）；spawn 逻辑下沉为 `trySpawn` 供首启/重连共用；所有重连状态迁移**原地变更**（禁用 setState）；spawn 成功后过 **post-spawn 终检闸门**（身份+取消双检，防孤儿/防复活）；前端经新绑定 `TunnelStates()` + logStore 订阅驱动防抖刷新。

**Tech Stack:** Go 1.26（仅标准库）、Vue 3 `<script setup>` + Pinia、Wails v2 手工绑定。

**Spec:** `docs/superpowers/specs/2026-08-26-auto-reconnect-design.md`（本计划的唯一需求来源，执行者须同读）

## Global Constraints

- 全程 `go test ./... -race -count=1` 必须绿；每个行为先写失败测试并观察 RED
- 仅标准库；禁止新依赖
- respawnLoop 内一切状态迁移**原地变更**（c.mu + p.mu），严禁调用 `setState`（setState 仅保留给外部 Start 的失败路径）
- `trySpawn` 成功后必须经过 post-spawn 终检闸门（§3.5）：身份不匹配或 cancel 已关/状态非 reconnecting → **Kill 刚 spawn 的进程**
- Windows 兼容：测试进程一律用既有 `exitProcess`/`aliveProcess` helpers（内含 GOOS 分支）
- 提交信息：中文 conventional commits（`feat(scope): …`），带 Sisyphus 页脚（Co-authored-by trailer）
- 用户可见文案中文；不改 `frontend/wailsjs/runtime/**` 与 `frontend/dist/**`
- 本文档中各 `git commit` 命令为简写：实际提交必须追加 Sisyphus 页脚两个 `-m`（`Ultraworked with [Sisyphus](https://github.com/code-yeongyu/oh-my-openagent)` 与 `Co-authored-by: Sisyphus <clio-agent@sisyphuslabs.ai>`），沿用仓库惯例

---

### Task 1: 退避计算 `backoffDelay` + 可取消等待注入点

**Files:**
- Modify: `internal/forward/ctrl.go`（NewCtrl 签名、Ctrl 字段、backoffDelay）
- Modify: `internal/forward/ctrl_test.go`（3 处 NewCtrl 调用点适配 + 新测试）
- Modify: `app.go:71`（NewCtrl 调用点适配）

**Interfaces:**
- Consumes: 无（起点任务）
- Produces: `backoffDelay(attempt int) time.Duration`；`NewCtrl(sp osutil.Spawner, emit EmitFunc, after func(time.Duration) <-chan struct{}) *Ctrl`（after 为 nil 时用生产默认）；后续任务的测试通过第三参注入即时触发的假时钟

- [ ] **Step 1: 写失败测试**

在 `internal/forward/ctrl_test.go` 末尾追加：

```go
// 自动重连：退避序列 1s,2s,4s,8s,16s 之后固定 30s 封顶（spec §3.2）。
func TestBackoffDelaySequence(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := backoffDelay(i + 1); got != w {
			t.Fatalf("backoffDelay(%d)=%s want %s", i+1, got, w)
		}
	}
	if backoffDelay(0) != time.Second {
		t.Fatal("attempt<=0 should fall back to 1s")
	}
}
```

- [ ] **Step 2: 运行确认 RED**

Run: `go test ./internal/forward/ -run TestBackoffDelaySequence -v`
Expected: FAIL，报 `undefined: backoffDelay`

- [ ] **Step 3: 最小实现**

`internal/forward/ctrl.go` 在 `classifyError` 上方追加：

```go
// backoffDelay 返回第 attempt 次（从 1 计）重试前的等待时长：
// 1s 起倍增，30s 封顶（spec §3.2）。
func backoffDelay(attempt int) time.Duration {
	const max = 30 * time.Second
	if attempt <= 0 {
		return time.Second
	}
	if attempt >= 6 {
		return max
	}
	return time.Second << uint(attempt-1)
}
```

- [ ] **Step 4: 运行确认 GREEN**

Run: `go test ./internal/forward/ -run TestBackoffDelaySequence -v`
Expected: PASS

- [ ] **Step 5: 注入点签名变更（纯机械适配，套件保持绿）**

`ctrl.go` 的 `Ctrl` 结构体与 `NewCtrl` 改为：

```go
type Ctrl struct {
	spawner osutil.Spawner
	emit    EmitFunc
	mu      sync.Mutex
	procs   map[string]*process
	// after 产生可被 cancel 竞争的等待通道；nil 时用生产默认（真实计时）。
	after func(time.Duration) <-chan struct{}
}

func NewCtrl(sp osutil.Spawner, emit EmitFunc, after func(time.Duration) <-chan struct{}) *Ctrl {
	if after == nil {
		after = func(d time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			go func() {
				time.Sleep(d)
				close(ch)
			}()
			return ch
		}
	}
	return &Ctrl{spawner: sp, emit: emit, procs: map[string]*process{}, after: after}
}
```

适配调用点（第三参传 nil）：
- `app.go`: `a.forward = forward.NewCtrl(osutil.NewSpawner(), emit, nil)`
- `ctrl_test.go:100`、`:159`、`:199` 三处 `NewCtrl(sp, …)` → `NewCtrl(sp, func(e Event){…}, nil)`

- [ ] **Step 6: 全量验证**

Run: `go vet ./... && go test ./... -race -count=1`
Expected: 全 PASS

- [ ] **Step 7: Commit**

```bash
git add internal/forward/ctrl.go internal/forward/ctrl_test.go app.go
git commit -m "feat(forward): 退避计算 backoffDelay 与可取消等待注入点（重连基建 1/2）"
```

---

### Task 2: process 扩展字段 + Start 快照/取消语义（行为不变的准备性重构）

**Files:**
- Modify: `internal/forward/ctrl.go`（process 结构体、Start 成功路径、Stop、OnShutdown）
- Test: 既有套件保持绿（本任务无新行为）

**Interfaces:**
- Consumes: Task 1 的 Ctrl.after
- Produces: `process` 新字段 `cancel chan struct{}` / `tunnel config.Tunnel` / `lastStableAt time.Time` / `attempts int` / `lastErr string`；Stop/OnShutdown 会 close cancel（置 nil 防双关）；Task 3+ 的 respawnLoop 依赖这些字段的精确名字

- [ ] **Step 1: process 结构体扩展**

```go
type process struct {
	state        State
	args         []string
	proc         *osutil.Process
	cancel       chan struct{}  // spawn 成功即创建；Stop/OnShutdown close（置 nil 防双关）
	tunnel       config.Tunnel  // spawn 时的配置快照，重连只认快照
	lastStableAt time.Time      // 最近一次进入 connected 的时刻（防抖基准）
	attempts     int            // 连续失败计数
	lastErr      string         // 最近一次意外退出的 classifyError 结果
	mu           sync.Mutex
}
```

- [ ] **Step 2: Start 成功路径填充快照（原地升级，不再新建条目）**

将 Start 中"spawn 成功后"的一段：

```go
	c.mu.Lock()
	entry := &process{state: StateConnected, args: args, proc: proc}
	c.procs[t.ID] = entry
	c.mu.Unlock()
```

连同其上方"Connecting 占位条目"一并改为：

```go
	c.mu.Lock()
	entry := &process{
		state:        StateConnecting,
		args:         args,
		cancel:       make(chan struct{}),
		tunnel:       t,
		lastStableAt: time.Now(),
	}
	c.procs[t.ID] = entry
	c.mu.Unlock()
	c.emitEvent(t.ID, "info", "connecting")

	proc, err := c.trySpawn(t)
```

> 本步同时把"端口预检 + BuildArgs + spawn"下沉为 `trySpawn`（下一 Task 的重连要复用）。
> `trySpawn` 放在 Start 之后：

```go
// trySpawn 执行参数构建、本地端口预检与进程 spawn，返回进程句柄。
// 失败时的状态处理由调用方决定（首启→setError 替换；重连→计数后退避）。
func (c *Ctrl) trySpawn(t config.Tunnel) (*osutil.Process, error) {
	args := BuildArgs(t)
	if args == nil {
		return nil, fmt.Errorf("could not build args for %q", t.Host)
	}
	if t.Mode != "remote" && t.ListenPort > 0 && !CheckLocalPort(t.ListenBind, t.ListenPort) {
		return nil, fmt.Errorf("local port %s:%d already in use", t.ListenBind, t.ListenPort)
	}
	spawnArgs := args[1:]
	return c.spawner.Start("ssh", spawnArgs...)
}
```

Start 相应精简为（保持既有失败替换语义与事件文本）：

```go
func (c *Ctrl) Start(t config.Tunnel) error {
	// H4: 已有存活进程时直接拒绝，绝不替换 map 条目——替换会丢弃正在运行
	// 的进程句柄，导致其永远无法 Stop、端口持续被占。
	c.mu.Lock()
	if p, ok := c.procs[t.ID]; ok {
		p.mu.Lock()
		live := p.proc != nil && (p.state == StateConnecting || p.state == StateConnected)
		p.mu.Unlock()
		if live {
			c.mu.Unlock()
			return fmt.Errorf("tunnel already running: %s", t.ID)
		}
	}
	c.mu.Unlock()

	if !ValidateHost(t.Host) {
		c.setState(t.ID, StateError)
		return fmt.Errorf("invalid host alias %q", t.Host)
	}

	c.mu.Lock()
	entry := &process{
		state:        StateConnecting,
		cancel:       make(chan struct{}),
		tunnel:       t,
		lastStableAt: time.Now(),
	}
	c.procs[t.ID] = entry
	c.mu.Unlock()
	c.emitEvent(t.ID, "info", "connecting")

	proc, err := c.trySpawn(t)
	if err != nil {
		msg := classifyError(osutil.Outcome{Stderr: err.Error(), ExitCode: -1})
		c.setState(t.ID, StateError)
		c.emitEvent(t.ID, "error", msg)
		return fmt.Errorf("%s: %s", t.Name, msg)
	}

	// 原地升级到 connected：身份不变，watchExit 持有同一指针。
	entry.mu.Lock()
	entry.args = BuildArgs(t)
	entry.proc = proc
	entry.state = StateConnected
	entry.lastStableAt = time.Now()
	entry.mu.Unlock()
	c.emitEvent(t.ID, "info", "connected")
	go c.watchExit(t.ID, entry, proc)
	return nil
}
```

> 现有套件对"端口占用"路径无消息文本精确断言（`TestStartInvalidHost` 只断言
> state 与错误返回、`TestCheckLocalPortFreeAndBusy` 不经过 Start），故本统一
> 不会破坏任何既有测试。

- [ ] **Step 3: Stop / OnShutdown 关闭 cancel**

`Stop` 在 `p.mu.Lock()` 之后、`if p.proc != nil` 之前插入：

```go
	// 关闭取消通道：重连循环（若在退避等待）立即收尾为 stopped。
	if p.cancel != nil {
		close(p.cancel)
		p.cancel = nil
	}
```

`OnShutdown` 循环体内 `p.mu.Lock()` 之后插入同样的三行。

- [ ] **Step 4: 全量验证（无新测试，套件必须全绿证明行为不变）**

Run: `go vet ./... && go test ./... -race -count=1`
Expected: 全 PASS（含 `TestStartMonitorsProcessExit`、`TestStartTwiceRejectsSecond`）

- [ ] **Step 5: Commit**

```bash
git add internal/forward/ctrl.go app.go
git commit -m "refactor(forward): trySpawn 下沉 + process 携带快照/取消通道，Stop/OnShutdown 关闭取消"
```

---

### Task 3: watchExit 分支 + 最小重连环（死→活 单次成功路径）

**Files:**
- Modify: `internal/forward/ctrl.go`（StateReconnecting 常量、watchExit 分支、respawnLoop）
- Modify: `internal/forward/ctrl_test.go`（脚本化 spawner + 即时 after + 新测试）

**Interfaces:**
- Consumes: Task 1 `backoffDelay`/`after`；Task 2 的 entry 字段与 `trySpawn`
- Produces: `StateReconnecting State = "reconnecting"`；`respawnLoop(id string, entry *process)`（内部方法，后续任务在其上叠加）

- [ ] **Step 1: 写失败测试（脚本化 死→活）**

`ctrl_test.go` 追加：

```go
// 自动重连：AutoReconnect=true 的隧道意外退出后进入 reconnecting，
// 退避 1s 后首次重试即成功 → 最终 connected，事件含进入重连与 reconnected。
func TestAutoReconnectSuccessAfterFirstRetry(t *testing.T) {
	var mu sync.Mutex
	var emitted []Event
	var slept []time.Duration
	calls := 0
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return exitProcess(t, 1) // 首启进程立即死亡
			}
			return aliveProcess(t) // 第一次重试即成功
		}},
		func(e Event) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		},
		func(d time.Duration) <-chan struct{} { // 假时钟：零等待，记录序列
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	tr := base()
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateConnected {
		time.Sleep(5 * time.Millisecond)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && calls < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if calls < 2 {
		t.Fatalf("expected a retry spawn, calls=%d", calls)
	}
	if got := ctrl.State(tr.ID); got != StateConnected {
		t.Fatalf("state should be connected after successful retry, got %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) < 1 || slept[0] != time.Second {
		t.Fatalf("first backoff sleep should be 1s, got %v", slept)
	}
	var sawWarn, sawReconnected bool
	for _, e := range emitted {
		if e.Level == "warn" && strings.Contains(e.Message, "第 1 次重连") {
			sawWarn = true
		}
		if e.Level == "info" && strings.Contains(e.Message, "reconnected") {
			sawReconnected = true
		}
	}
	if !sawWarn || !sawReconnected {
		t.Fatalf("missing events; warn=%v reconnected=%v all=%+v", sawWarn, sawReconnected, emitted)
	}
}
```

（`strings` 已在该文件 import 列表中则复用，否则补 `"strings"`。）

- [ ] **Step 2: 运行确认 RED**

Run: `go test ./internal/forward/ -run TestAutoReconnectSuccessAfterFirstRetry -v`
Expected: FAIL——进程死后状态停在 `error`（无重连发生），`calls` 恒为 1

- [ ] **Step 3: 实现 StateReconnecting + watchExit 分支 + respawnLoop**

`ctrl.go` 常量区新增：

```go
	StateReconnecting State = "reconnecting"
```

`levelFor` 不变（reconnecting 事件级别由显式字符串控制）。

`watchExit` 整体替换为：

```go
// watchExit 监控一次成功 spawn 的进程：意外退出时按 AutoReconnect 分流——
// false → StateError（H3 行为）；true → 置 reconnecting 并进入退避重连循环。
// 指针身份比较保证：条目已被替换、或 Stop/OnShutdown 已接管时不做任何处理。
func (c *Ctrl) watchExit(id string, entry *process, proc *osutil.Process) {
	out := proc.Wait()
	c.mu.Lock()
	cur, ok := c.procs[id]
	fire := ok && cur == entry
	startLoop := false
	if fire {
		entry.mu.Lock()
		if entry.proc == proc {
			entry.proc = nil
			entry.lastErr = classifyError(out)
			if entry.tunnel.AutoReconnect {
				// 防抖：稳定满阈值才清零计数（spec §3.4）
				if time.Since(entry.lastStableAt) >= stableThreshold {
					entry.attempts = 0
				}
				entry.state = StateReconnecting
				startLoop = true
			} else {
				entry.state = StateError
			}
		} else {
			fire = false
		}
		entry.mu.Unlock()
	}
	c.mu.Unlock()
	if !fire {
		return
	}
	if !startLoop {
		c.emitEvent(id, "error", entry.lastErrSnapshot())
		return
	}
	go c.respawnLoop(id, entry)
}
```

文件顶部常量与两个小助手：

```go
// stableThreshold：连续在线满该时长才清零重连失败计数（防 flapping 风暴）。
const stableThreshold = 60 * time.Second

// lastErrSnapshot 在持 p.mu 的前提下读取最近错误文本（watchExit 已释放锁后使用）。
func (entry *process) lastErrSnapshot() string {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.lastErr
}
```

`respawnLoop`（放在 watchExit 之后）：

```go
// respawnLoop 按指数退避反复尝试重连，直到成功或取消。
// 一切状态迁移原地变更（禁用 setState）；spawn 成功必过 post-spawn 终检闸门。
func (c *Ctrl) respawnLoop(id string, entry *process) {
	for {
		// 等待前检查取消
		if c.loopCancelled(id, entry) {
			return
		}
		entry.mu.Lock()
		attempt := entry.attempts + 1
		entry.attempts = attempt
		reason := entry.lastErr
		entry.mu.Unlock()
		delay := backoffDelay(attempt)
		c.emitEvent(id, "warn", fmt.Sprintf("连接断开（%s），%s 后进行第 %d 次重连", reason, delay, attempt))

		select {
		case <-c.waitCancel(entry):
			c.finishStop(id, entry, "已停止重连")
			return
		case <-c.after(delay):
		}

		if c.loopCancelled(id, entry) {
			return
		}
		c.emitEvent(id, "info", fmt.Sprintf("第 %d 次重连中…", attempt))

		proc, err := c.trySpawn(entry.tunnel)
		if err != nil {
			c.emitEvent(id, "warn", fmt.Sprintf("第 %d 次重连失败: %v", attempt, err))
			continue
		}

		// ── post-spawn 终检闸门（spec §3.5）：身份 + 取消/状态双检 ──
		adopted := false
		c.mu.Lock()
		if cur, _ := c.procs[id]; cur == entry {
			entry.mu.Lock()
			if entry.cancel != nil && entry.state == StateReconnecting {
				entry.proc = proc
				entry.state = StateConnected
				now := time.Now()
				if now.Sub(entry.lastStableAt) >= stableThreshold {
					entry.attempts = 0
				}
				entry.lastStableAt = now
				adopted = true
			}
			entry.mu.Unlock()
		}
		c.mu.Unlock()
		if !adopted {
			_ = proc.Kill() // 条目已被替换或已被停止：绝不让新进程成为孤儿/复活隧道
			return
		}
		c.emitEvent(id, "info", fmt.Sprintf("reconnected（共尝试 %d 次）", attempt))
		go c.watchExit(id, entry, proc)
		return
	}
}

// loopCancelled 报告 entry 是否已被取消或不再是当前条目。
func (c *Ctrl) loopCancelled(id string, entry *process) bool {
	c.mu.Lock()
	cur, _ := c.procs[id]
	c.mu.Unlock()
	if cur != entry {
		return true
	}
	entry.mu.Lock()
	cancelled := entry.cancel == nil || entry.state != StateReconnecting
	entry.mu.Unlock()
	return cancelled
}

// waitCancel 返回 entry 的取消通道（已关/缺失时返回已关闭通道）。
func (c *Ctrl) waitCancel(entry *process) <-chan struct{} {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.cancel == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return entry.cancel
}

// finishStop 由重连循环在收到取消后收尾：置 stopped 并发事件。
func (c *Ctrl) finishStop(id string, entry *process, msg string) {
	c.mu.Lock()
	cur, _ := c.procs[id]
	c.mu.Unlock()
	if cur != entry {
		return
	}
	entry.mu.Lock()
	if entry.cancel != nil {
		close(entry.cancel)
		entry.cancel = nil
	}
	entry.proc = nil
	entry.state = StateStopped
	entry.mu.Unlock()
	c.emitEvent(id, "info", msg)
}
```

- [ ] **Step 4: 运行确认 GREEN**

Run: `go test ./internal/forward/ -race -count=1 -v`
Expected: 全 PASS（新测试 + 既有 H3/H4 回归）

- [ ] **Step 5: Commit**

```bash
git add internal/forward/ctrl.go internal/forward/ctrl_test.go
git commit -m "feat(forward): 意外退出进入 reconnecting 并按指数退避自动重连（含 post-spawn 终检闸门）"
```

---

### Task 3b: 多轮失败退避序列（死→死→活）

**Files:**
- Modify: `internal/forward/ctrl_test.go`

**Interfaces:**
- Consumes: Task 3 的 respawnLoop 完整路径
- Produces: 对 spec §7.1/§7.2 的回归锁定（真实循环的 sleep 序列与计数递增）

- [ ] **Step 1: 写测试**

`ctrl_test.go` 追加：

```go
// 自动重连：死→死→活 两轮失败后退避序列应为 [1s,2s]，
// warn 事件逐次携带尝试序号，最终 connected。
func TestAutoReconnectBackoffSequenceAcrossRetries(t *testing.T) {
	var mu sync.Mutex
	var emitted []Event
	var slept []time.Duration
	calls := 0
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			switch n {
			case 1, 2:
				return exitProcess(t, 1)
			default:
				return aliveProcess(t)
			}
		}},
		func(e Event) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		},
		func(d time.Duration) <-chan struct{} {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	tr := base()
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && calls < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if calls < 3 {
		t.Fatalf("expected third spawn (successful retry), calls=%d", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) < 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("backoff sleeps should be [1s,2s,...], got %v", slept)
	}
	warnCount := 0
	for _, e := range emitted {
		if e.Level == "warn" && strings.Contains(e.Message, "次重连") {
			warnCount++
		}
	}
	if warnCount < 2 {
		t.Fatalf("expected >=2 entering-reconnect warns, got %d in %+v", warnCount, emitted)
	}
}
```

- [ ] **Step 2: 运行确认**

Run: `go test ./internal/forward/ -race -run TestAutoReconnectBackoffSequenceAcrossRetries -v`
Expected: PASS（Task 3 已实现；FAIL 则修实现——本测试锁定 spec §7.1/§7.2）

- [ ] **Step 3: Commit**

```bash
git add internal/forward/ctrl_test.go
git commit -m "test(forward): 锁定跨重试退避序列与逐次 warn 事件"
```

---

### Task 4: 防抖动规则专项（<60s 计数延续 / ≥60s 清零）

**Files:**
- Modify: `internal/forward/ctrl_test.go`

**Interfaces:**
- Consumes: Task 3 的 `stableThreshold`、entry.lastStableAt/attempts 语义
- Produces: 对 spec §3.4 的回归锁定（无新 API）

- [ ] **Step 1: 写失败测试**

`ctrl_test.go` 追加（直接操纵 entry 字段构造前提，聚焦清零判定本身）：

```go
// 自动重连防抖：连续在线 <60s 再次断开 → 计数不清零；
// ≥60s 后再断开 → 计数从 1 重来（spec §3.4）。
func TestAutoReconnectFlappingGuard(t *testing.T) {
	newEntry := func(stable time.Duration, attempts int) *process {
		e := &process{
			state:        StateConnected,
			cancel:       make(chan struct{}),
			tunnel:       config.Tunnel{ID: "f1", AutoReconnect: true},
			lastStableAt: time.Now().Add(-stable),
			attempts:     attempts,
		}
		return e
	}
	// 场景一：在线 59s，历史已有 3 次失败 → 计数保留（不清零）
	e1 := newEntry(59*time.Second, 3)
	got1 := nextAttemptAfterCrash(e1)
	if got1 != 4 {
		t.Fatalf("flapping (<60s) must keep counter: want attempt 4, got %d", got1)
	}
	// 场景二：在线 61s，历史 3 次失败 → 清零，下一次从 1 开始
	e2 := newEntry(61*time.Second, 3)
	got2 := nextAttemptAfterCrash(e2)
	if got2 != 1 {
		t.Fatalf("stable (>=60s) must reset counter: want attempt 1, got %d", got2)
	}
}

// nextAttemptAfterCrash 复刻 watchExit 断开瞬间的计数判定，供防抖断言复用。
func nextAttemptAfterCrash(e *process) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.lastStableAt) >= stableThreshold {
		e.attempts = 0
	}
	e.attempts++
	return e.attempts
}
```

- [ ] **Step 2: 运行确认结果**

Run: `go test ./internal/forward/ -run TestAutoReconnectFlappingGuard -v`
Expected: **PASS**（Task 3 已实现该语义——本测试是规格锁定；若 FAIL 说明 Task 3 实现偏离 spec，修实现而非测试）

- [ ] **Step 3: Commit**

```bash
git add internal/forward/ctrl_test.go
git commit -m "test(forward): 锁定重连防抖规则（<60s 计数延续 / ≥60s 清零）"
```

---

### Task 5: 取消路径（等待期停止 → stopped，无后续尝试）

**Files:**
- Modify: `internal/forward/ctrl_test.go`

**Interfaces:**
- Consumes: Task 3 的 cancel/finishStop 路径
- Produces: 对 spec §6 第 1 行的回归锁定

- [ ] **Step 1: 写测试（受控阻塞的 after，制造"卡在退避等待"窗口）**

`ctrl_test.go` 追加：

```go
// 自动重连取消：退避等待期间 Stop 必须令循环立即收尾为 stopped，
// 且不再发起任何重试 spawn。
func TestAutoReconnectStopDuringBackoff(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{}) // 手控第一段退避等待
	ctrl := NewCtrl(
		fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return exitProcess(t, 1)
			}
			t.Fatal("must not spawn again after stop")
			return nil, nil
		}},
		func(e Event) {},
		func(d time.Duration) <-chan struct{} {
			return release // 第一段退避挂起直到测试放行
		},
	)
	tr := base()
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 等待进入 reconnecting
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateReconnecting {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ctrl.State(tr.ID); got != StateReconnecting {
		t.Fatalf("should be reconnecting, got %s", got)
	}
	if err := ctrl.Stop(tr.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	close(release) // 放行被挂起的等待
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateStopped {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("state should be stopped after cancel, got %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("no retry spawn may happen after stop, calls=%d", calls)
	}
}
```

- [ ] **Step 2: 运行确认**

Run: `go test ./internal/forward/ -race -run TestAutoReconnectStopDuringBackoff -v`
Expected: PASS（Task 3 的 cancel 语义正确；若 FAIL 属实现 bug，修实现）

- [ ] **Step 3: Commit**

```bash
git add internal/forward/ctrl_test.go
git commit -m "test(forward): 锁定退避等待期停止的取消语义"
```

---

### Task 5b: DeleteTunnel during backoff 无泄漏（app 层取消）

**Files:**
- Modify: `app_test.go`

**Interfaces:**
- Consumes: Task 3 的 cancel 路径 + 既有 DeleteTunnel（enabled 隧道先 Stop）
- Produces: 对 spec §7.9 的回归锁定

- [ ] **Step 1: 写失败（锁定型）测试**

`app_test.go` 追加。要点：app 层拿不到 forward 包的测试助手，故本地定义计数包装 spawner 与短命进程助手；`a.Init` 后整体替换 `a.forward` 为受控实例（同包可访问字段）。

```go
// countedSpawner 每次都启动“立即退出”的真实进程并统计次数
// （app 层无 forward 包测试助手，本地自建；进程即死使任何时序下断言确定）。
type countedSpawner struct {
	mu sync.Mutex
	n  int
}

func (s *countedSpawner) Start(name string, args ...string) (*osutil.Process, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return osutil.NewSpawner().Start("sh", "-c", "exit 1")
}

func (s *countedSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// 自动重连：退避等待期删除规则 → DeleteTunnel 先 Stop 触发取消，
// 循环收尾 stopped、配置条目移除，且删除后不再发起任何重试 spawn。
func TestDeleteTunnelDuringBackoffCancelsReconnect(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshkit.toml")

	sp := &countedSpawner{}
	// 整体替换为受控控制器（同包可访问字段）；after 用真实计时
	a.forward = forward.NewCtrl(sp, func(forward.Event) {}, nil)

	tr := config.Tunnel{
		ID: "d1", Name: "del", Host: "prod-db", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: freeTestPort(t),
		AutoReconnect: true, Enabled: true,
	}
	a.cfg = &config.AppConfig{Tunnels: []config.Tunnel{tr}}

	if err := a.forward.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 等待进入 reconnecting（首启即死进程退出后 watchExit 分流）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && a.forward.State("d1") != forward.StateReconnecting {
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.forward.State("d1"); got != forward.StateReconnecting {
		t.Fatalf("should be reconnecting before delete, got %s", got)
	}

	if err := a.DeleteTunnel("d1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := a.forward.State("d1"); got != forward.StateStopped {
		t.Fatalf("state after delete should be stopped, got %s", got)
	}
	if len(a.cfg.Tunnels) != 0 {
		t.Fatalf("tunnel should be removed from config, len=%d", len(a.cfg.Tunnels))
	}

	// 删除后静置跨过至少一个退避周期：不得再有任何 spawn 尝试
	before := sp.count()
	time.Sleep(2500 * time.Millisecond)
	if after := sp.count(); after != before {
		t.Fatalf("no spawn may happen after delete: before=%d after=%d", before, after)
	}
}

// freeTestPort 与 forward 包内 freePort 同款，供 app_test 使用。
func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
```

需要的 import 增补：`sync`、`time`、`sshkit/internal/osutil`（若尚未在列）。

> 说明：本测试使用真实短命进程与真实 1s 退避（app 层无法注入 forward 的
> 假时钟）；"删除后无后续尝试"用静置前后 spawn 计数对比断言。
> ctrl 层的精确取消语义已由 Task 5 锁定。

- [ ] **Step 2: 运行确认**

Run: `go test . -race -run TestDeleteTunnelDuringBackoff -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add app_test.go
git commit -m "test(app): 锁定退避期删除规则的取消与清理语义"
```

---

### Task 6: post-spawn 终检两分支（孤儿 Kill / 复活阻止）

**Files:**
- Modify: `internal/forward/ctrl_test.go`

**Interfaces:**
- Consumes: Task 3 的终检闸门代码路径
- Produces: 对 spec §3.5 双 blocker 修复的回归锁定

- [ ] **Step 1: 孤儿分支测试（spawn 成功后条目已被外部 Start 替换）**

```go
// post-spawn 终检·孤儿分支：重连 spawn 成功瞬间条目已被外部 Start 替换，
// 循环必须 Kill 自己刚 spawn 的进程并退出——绝不允许不可停止的孤儿进程存活。
func TestRespawnOrphanKilledWhenEntryReplaced(t *testing.T) {
	calls := 0
	var secondProc *osutil.Process
	var mu sync.Mutex
	ctrl := NewCtrl(
		fakeSpawner{}, // startFunc 稍后注入（需要引用 ctrl）
		func(e Event) {},
		func(d time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	sp := fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return exitProcess(t, 1)
		}
		p, err := aliveProcess(t)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		secondProc = p
		mu.Unlock()
		// 在重连 spawn 成功的同时，模拟外部 Start 已抢先整体替换了条目
		ctrl.mu.Lock()
		ctrl.procs["o1"] = &process{state: StateError, args: nil}
		ctrl.mu.Unlock()
		return p, nil
	}}
	ctrl.spawner = sp

	tr := base()
	tr.ID = "o1"
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 等待重连循环因身份失配退出
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c, p := calls, secondProc
		mu.Unlock()
		if c >= 2 && p != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	p := secondProc
	mu.Unlock()
	if p == nil {
		t.Fatal("retry spawn never happened")
	}
	// 被 adopt 失败的新进程必须已被 Kill：Wait 通道应在短时间内关闭
	select {
	case <-p.Wait():
		// killed as expected
	case <-time.After(3 * time.Second):
		t.Fatal("orphaned respawn process was not killed")
	}
	if got := ctrl.State("o1"); got != StateError {
		t.Fatalf("map entry should remain the external replacement (error), got %s", got)
	}
}
```

- [ ] **Step 2: 复活分支测试（trySpawn 期间 Stop 关闭 cancel）**

```go
// post-spawn 终检·复活分支：trySpawn 执行期间 Stop 关闭 cancel，
// 循环不得写回 connected——用户刚停掉的隧道不允许原地复活。
func TestRespawnNoResurrectionAfterStopDuringSpawn(t *testing.T) {
	calls := 0
	stopCalled := false
	var mu sync.Mutex
	ctrl := NewCtrl(
		fakeSpawner{},
		func(e Event) {},
		func(d time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			close(ch)
			return ch
		},
	)
	sp := fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return exitProcess(t, 1)
		}
		// 第二次 spawn（重连尝试）进行中触发 Stop：关闭 cancel + 状态改写
		mu.Lock()
		if !stopCalled {
			stopCalled = true
			mu.Unlock()
			_ = ctrl.Stop("r1") // 原地变更：指针不变，仅凭身份校验发现不了
		} else {
			mu.Unlock()
		}
		return aliveProcess(t)
	}}
	ctrl.spawner = sp

	tr := base()
	tr.ID = "r1"
	tr.AutoReconnect = true
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 等 Stop 生效后的最终态
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctrl.State(tr.ID) != StateStopped {
		time.Sleep(5 * time.Millisecond)
	}
	// 给可能错误的"复活写入"留出暴露时间
	time.Sleep(100 * time.Millisecond)
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("stopped tunnel must not resurrect, got %s", got)
	}
}
```

- [ ] **Step 3: 运行确认**

Run: `go test ./internal/forward/ -race -run "TestRespawn" -v`
Expected: 两测皆 PASS（Task 3 已实现闸门；FAIL 则修实现）

- [ ] **Step 4: Commit**

```bash
git add internal/forward/ctrl_test.go
git commit -m "test(forward): 锁定 post-spawn 终检闸门——孤儿 Kill 与停止复活阻止"
```

---

### Task 7: `TunnelStates` 绑定（Go + 手工 wailsjs）

**Files:**
- Modify: `internal/forward/ctrl.go`（新增 `States()`）
- Modify: `app.go`（新增 App 方法 + NewCtrl 第三参 nil 已在 Task 1 完成）
- Test: `app_test.go`
- Modify: `frontend/wailsjs/go/main/App.js`、`App.d.ts`

**Interfaces:**
- Consumes: Ctrl.procs / 各 state 字符串值
- Produces: `func (c *Ctrl) States() map[string]string`；Wails 绑定 `TunnelStates(): Promise<Record<string,string>>`（前端 Task 9 消费）

- [ ] **Step 1: 写失败测试**

`app_test.go` 追加：

```go
// 自动重连配套：TunnelStates 暴露各隧道运行态（id → state 字符串），
// 前端据此渲染四态圆点。用非法 host 路径制造一条 error 态条目（不产生真实 ssh 进程）。
func TestTunnelStates(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	if err := a.forward.Start(config.Tunnel{ID: "s1", Host: "-bad", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 1}); err == nil {
		t.Fatal("invalid host must fail")
	}
	got := a.TunnelStates()
	if got["s1"] != "error" {
		t.Fatalf(`states["s1"]=%q want "error"`, got["s1"])
	}
}
```

- [ ] **Step 2: 运行确认 RED**

Run: `go test . -run TestTunnelStates -v`
Expected: FAIL，报 `a.TunnelStates undefined`

- [ ] **Step 3: 实现**

`internal/forward/ctrl.go` 追加：

```go
// States 返回 id→state 快照（深拷贝，避免调用方触碰内部 map）。
func (c *Ctrl) States() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.procs))
	for id, p := range c.procs {
		p.mu.Lock()
		out[id] = string(p.state)
		p.mu.Unlock()
	}
	return out
}
```

`app.go` 追加（ListTunnels 附近）：

```go
// TunnelStates 返回各隧道运行态（id → state 字符串），供前端四态圆点渲染。
func (a *App) TunnelStates() map[string]string {
	return a.forward.States()
}
```

- [ ] **Step 4: 运行确认 GREEN**

Run: `go test . -run TestTunnelStates -v && go vet ./...`
Expected: PASS / 无告警

- [ ] **Step 5: 手工补 wailsjs 绑定**

`frontend/wailsjs/go/main/App.js`（按字母序插到 ListRecentSFTP 与 ListTunnels 之间）：

```js
export function TunnelStates() {
  return window['go']['main']['App']['TunnelStates']();
}
```

`frontend/wailsjs/go/main/App.d.ts` 同位置：

```ts
export function TunnelStates():Promise<{[key: string]: string}>;
```

- [ ] **Step 6: Commit**

```bash
git add internal/forward/ctrl.go app.go app_test.go frontend/wailsjs/go/main/App.js frontend/wailsjs/go/main/App.d.ts
git commit -m "feat(app): TunnelStates 绑定暴露各隧道运行态（前端四态圆点数据源）"
```

---

### Task 8: importer 导入隧道默认开启 AutoReconnect

**Files:**
- Modify: `internal/importer/parser.go`（makeTunnel 返回字面量）
- Modify: `internal/importer/parser_test.go`

**Interfaces:**
- Consumes: `config.Tunnel.AutoReconnect bool`
- Produces: 导入生成的隧道 `AutoReconnect=true`（与前端 `newTunnel()` 默认一致）

- [ ] **Step 1: 写失败测试**

`parser_test.go` 追加：

```go
// M7 配套：导入的隧道默认开启自动重连，与前端 newTunnel() 默认一致。
func TestParseSetsAutoReconnectDefault(t *testing.T) {
	tunnels, err := Parse(`ssh -L 8080:localhost:80 myhost`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tunnels) != 1 || !tunnels[0].AutoReconnect {
		t.Fatalf("imported tunnel must default AutoReconnect=true, got %+v", tunnels)
	}
}
```

- [ ] **Step 2: 运行确认 RED**

Run: `go test ./internal/importer/ -run TestParseSetsAutoReconnectDefault -v`
Expected: FAIL（当前为零值 false）

- [ ] **Step 3: 实现**

`makeTunnel` 返回的字面量中，在 `ListenBind: "127.0.0.1",` 之后追加一行：

```go
		AutoReconnect: true, // 导入规则与手建规则默认一致（前端 newTunnel 同为 true）
```

- [ ] **Step 4: 运行确认 GREEN**

Run: `go test ./internal/importer/ -race -count=1 -v`
Expected: 全 PASS

- [ ] **Step 5: Commit**

```bash
git add internal/importer/parser.go internal/importer/parser_test.go
git commit -m "fix(importer): 导入隧道默认 AutoReconnect=true，与手建规则对齐"
```

---

### Task 9: 前端——states 拉取、logStore 订阅防抖刷新、RuleCard 四态圆点

**Files:**
- Modify: `frontend/src/views/ForwardView.vue`
- Modify: `frontend/src/components/RuleCard.vue`

**Interfaces:**
- Consumes: Task 7 的 `TunnelStates()` 绑定；logStore 条目字段 `source_type/source_id/level/message/ts`
- Produces: RuleCard 新 prop `state: String`（`connected|reconnecting|error|stopped|…`）

- [ ] **Step 1: ForwardView 拉 states + 订阅刷新**

`ForwardView.vue` `<script setup>` 改动：

```js
import { ref, onMounted, onUnmounted } from 'vue'
import { ListTunnels, ListHosts, CreateTunnel, UpdateTunnel, DeleteTunnel, ImportCommand, TunnelStates } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
// …原有 ref 声明之后追加：
const logStore = useLogStore()
const states = ref({})

async function refresh() {
  try {
    tunnels.value = (await ListTunnels()) || []
    states.value = (await TunnelStates()) || {}
  } catch (e) { loadError.value = String(e) }
}

// 状态转换经 'log' 事件流到达（source_type==='tunnel'）；App.vue 已持有全局订阅，
// 这里只订 pinia store，过滤后防抖刷新——不二次 EventsOn、不轮询。
let unsubLogs = null
let refreshTimer = null
onMounted(() => {
  unsubLogs = logStore.$subscribe((mutation, state) => {
    const logs = state.logs
    const last = logs[logs.length - 1]
    if (!last || last.source_type !== 'tunnel') return
    clearTimeout(refreshTimer)
    refreshTimer = setTimeout(refresh, 300)
  })
})
onUnmounted(() => {
  if (unsubLogs) unsubLogs()
  clearTimeout(refreshTimer)
})
```

模板中 RuleCard 增加 prop：

```html
<RuleCard v-for="t in tunnels" :key="t.id" :tunnel="t" :state="states[t.id]"
  @edit="openEdit" @delete="remove" @changed="refresh" />
```

- [ ] **Step 2: RuleCard 四态圆点**

`RuleCard.vue`：

```js
const props = defineProps({
  tunnel: { type: Object, required: true },
  state: { type: String, default: 'stopped' },
})
```

模板圆点行改为：

```html
<span :class="['dot', dotCls]"></span>
<span class="name">{{ tunnel.name || tunnel.host }}</span>
<span class="meta">{{ tunnel.mode }} {{ tunnel.listen_bind }}:{{ tunnel.listen_port }}<template v-if="props.state === 'reconnecting'"> · 重连中</template></span>
```

script 追加（`computed` 需从 vue 导入）：

```js
import { ref, computed } from 'vue'
const dotCls = computed(() =>
  props.state === 'connected' ? 'on'
  : props.state === 'reconnecting' ? 'warn'
  : props.state === 'error' ? 'err' : 'off')
```

样式追加：

```css
.dot.warn { background: #f0ad4e; }
.dot.err { background: var(--danger); }
```

- [ ] **Step 3: 验证**

Run（frontend/ 目录）: `npm run build && npm run test`
Expected: build 成功、vitest 3/3 通过

- [ ] **Step 4: Commit**

```bash
git add frontend/src/views/ForwardView.vue frontend/src/components/RuleCard.vue
git commit -m "feat(ui): 隧道四态圆点（connected/reconnecting/error/stopped）——TunnelStates 拉取 + 日志驱动防抖刷新"
```

---

### Task 10: 收尾全量验证 + 文档进度更新

**Files:**
- Modify: `docs/superpowers/reviews/2026-08-26-sshkit-review.md`（进度注记）

- [ ] **Step 1: 全量验证**

Run: `go vet ./... && go test ./... -race -count=1 && cd frontend && npm run build && npm run test`
Expected: 全绿

- [ ] **Step 2: 更新审查文档进度注记**

「修复进度」列表追加一行：

```markdown
> - 第四轮: **自动重连**已实现（见 docs/superpowers/specs/2026-08-26-auto-reconnect-design.md）——AutoReconnect 字段正式接线，剩余待办仅 L 级打磨项与 LICENSE 已决（MIT 已入库）
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/reviews/2026-08-26-sshkit-review.md
git commit -m "docs(review): 自动重连落地，进度更新"
```
