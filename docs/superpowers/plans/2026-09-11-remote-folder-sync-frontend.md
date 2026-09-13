# 远程文件夹同步 前端实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给已落地的同步后端补上前端界面：左导航新增第三项「同步」，能建/改/删规则、启停、看状态与统计、处理冲突与待确认删除。

**Architecture:** 纯前端增量。所有可测逻辑放进一个纯函数模块 `frontend/src/utils/sync.js`（仓库没有组件测试基建，只测 utils/stores），三个新 Vue 组件只做渲染与接线：`SyncView.vue`（列表 + 表单 + 实时刷新 + 日志面板）、`SyncCard.vue`（单条规则卡片）、`SyncConflictsDialog.vue`（冲突逐条裁决）。再加两个小后端任务：Task 1 补 spec §9.1 已声明但未实现的 `RetrySyncRuleFailures` 绑定并修正失败/冲突统计，Task 2 修正待确认删除的指纹重算（事件路径也写指纹、集合变化即失效）。

**Tech Stack:** Vue 3 (script setup) + pinia + vite/vitest；后端 Go（Task 1/2 涉及）。

**Spec:** `docs/superpowers/specs/2026-09-10-remote-folder-sync-design.md`（§9 前端与绑定、§12 影响面；§3 决策 14/15）

## Global Constraints

以下每条都来自 spec，逐字或等价转述，所有任务默认包含：

- **不新增任何第三方依赖**（前端只用已有的 Vue 3 + pinia + vite/vitest，不引入 UI 库或组件测试库）。
- **不新增第二个 `EventsOn`**（§9.3）。`App.vue:19-34` 已持有全局 `log` 订阅；视图侧只订 pinia store 后防抖刷新，**不轮询**。
- **`App.vue` 使用 `<KeepAlive>`**，视图卸载不会触发 `onUnmounted`（§9.3）：订阅与定时器必须用 `onActivated`/`onDeactivated` 管理（先例：`SftpView.vue` 的 `onActivated/onDeactivated`）。
- **日志隔离（§9.4 显式决策）**：`SyncView` 传给 `LogPanel` 的 `source-types` 是 `['sync', 'system']`，**不含 `sftp`**。不要指望 sftp 传输日志出现在同步视图：sftp 日志的 `source_id` 是 host，不是规则 id，按规则过滤永远命不中。
- **决策 15**：`kind === 'file'` 时 `mirror_delete` 无效（源消失一律不删本地），UI 必须**置灰并说明**；后端 `ValidateSyncRule` 也会拒。
- **决策 14**：同步视图自带轻量进度区，**不复用** `SftpView` 的 `transfers` 局部状态。
- **主机选择用 `ListHosts`**，不要用 `ListHostsDetailed`（后者对每个 alias 跑 `ssh -G`，放在对话框打开路径上会明显卡顿）。
- **本地目录用 `PickLocalDir`** 选择。
- **统计对象的字段名是 PascalCase**：`sync.SyncRuleStat`（定义见 `internal/sync/ctrl.go:39-56`）在 Go 侧没有 json tag，所以运行时键是 `Mode/Reason/PollIntervalS/Pending/Done/Failed/Conflicts/AlignScanned/AlignTotal/CurrentFile/SourceMissing/DeletePending/DeleteFingerprint/DeletePaths`。**规则对象 `config.SyncRule` 是 snake_case**（它有 json tag）。两者不要混。**`frontend/wailsjs/go/models.ts` 里没有 `sync.SyncRuleStat`**：wails v2.15.0 不为 map 的 value 结构体生成 TS 类型，`App.d.ts` 里的 `Record<string, sync.SyncRuleStat>` 是悬空引用——这是已知缺口，已记入 `2026-09-10-remote-folder-sync-followups.md`。
- **前端 excludes 默认值镜像后端**：`frontend/src/utils/sync.js` 的 `DEFAULT_EXCLUDES` 必须与 `internal/config/store.go:83-85` 的 `DefaultExcludes()` 逐条一致（`.git/`、`node_modules/`、`*.swp`、`*~`、`.DS_Store`）。这是**镜像不是同源**，两份列表必须同步修改；后端改默认值时前端不跟着改，UI 新建的规则就会静默丢默认排除（M1）。
- **提交信息用简体中文**，格式 `type(scope): 描述`。
- **前端验证命令**（在 `frontend/` 下跑）：`npx vitest run`（单测）、`npm run build`（构建即语法/导入检查）。后端任务另需 `go test ./... -race -count=1` 与 `go vet ./...`。
- **环境**：`frontend/node_modules` 只存在于主检出 `/home/lan/workspace/scripts/sshkit`（worktree 里没有），因此**本计划在主检出的一个特性分支上执行**，不要在独立 worktree 里跑（否则需要联网 `npm install`）。

---

## 文件结构

**新增**

| 文件 | 职责 |
|---|---|
| `frontend/src/utils/sync.js` | 纯函数：状态/徽章/计数/对齐进度文案、冲突动作标签、kind 与 mirror_delete 规则、表单默认值与双向转换、前端校验、stats 取值。**本计划唯一被单测覆盖的一层。** |
| `frontend/src/utils/sync.test.js` | 上述纯函数的 vitest 用例 |
| `frontend/src/components/SyncCard.vue` | 单条同步规则卡片：状态圆点、探测徽章、SourceMissing 警示、四组计数、对齐进度、当前文件、按钮（日志/冲突/确认删除/开始停止/重试/编辑/删除） |
| `frontend/src/components/SyncConflictsDialog.vue` | 冲突逐条裁决：两侧大小时间 + 三个动作，支持批量 |
| `frontend/src/views/SyncView.vue` | 列表 + 新建/编辑表单 + 实时刷新 + 自带进度区 + LogPanel + 各对话框接线 |
| `frontend/src/views/sync-smoke.test.js` | 编译冒烟（Task 4 创建，Task 5/6 扩展）：import 三个新组件并断言之定义。`plugin-vue` 在 import 时编译 SFC 模板，故能发现模板/导入错误；这是**编译检查**，不是渲染/行为测试（仓库无组件测试基建） |

**修改**

| 文件 | 改动 |
|---|---|
| `frontend/src/App.vue` | 左导航第三项「同步」+ KeepAlive 里第三个分支 |
| `internal/sync/ctrl.go` | Task 1：新增 `Ctrl.RetryFailed(id) (int, error)`、失败计数与冲突计数修正；Task 2：新增删除指纹重算 helper 并接到所有 `pendingDel` 变更点 |
| `internal/sync/state.go` | Task 1：`FailedItem` 增 `Action` 字段（`json:"action,omitempty"`，供重放 save_as 用） |
| `internal/sync/ctrl_test.go` | Task 1/2 的确定性用例（用 `newManualCtrl`/`attachRuntime`，不启动 goroutine） |
| `app.go` | 新增绑定 `RetrySyncRuleFailures(id string) error`（仅 Task 1） |
| `app_test.go` | 绑定契约：未运行的规则必须报错（Task 1） |
| `frontend/wailsjs/go/main/App.d.ts` / `App.js` | **显式重新生成**（Makefile 所有构建都带 `-skipbindings`，不会自动发生）；**`models.ts` 不变**（不含 `sync.SyncRuleStat`）（仅 Task 1） |
| `frontend/src/stores/settings.js` / `settings.test.js` | 新增 `autoReconnectDefault` 并在 load()/save() 往返（Task 6） |
| `README.md` / `README.zh-CN.md` | 功能特性小节补一条同步（§12） |

## 关于后端任务的一个决定（需你在评审计划时确认）

spec §9.1 声明了 `RetrySyncRuleFailures(id string) error`，但后端计划从未提到它，代码里也不存在（全仓 grep 为空）。引擎**已经**在为失败项记账（`state.go` 的 `FailedItem`、`ctrl.go` 的单文件 1s/4s/16s 重试与 `stats.Failed`），所以补这个绑定成本很低，且「重试失败」按钮确有用。

Task 1 里三项**与按钮无关**的修正必须保留：失败计数在重试后清零（否则卡片上的「失败 N」永久不消失）、`save_as` 失败记录可被原样重放，以及冲突计数反映当前挂起数（否则「冲突」按钮裁决完不消失，spec §9.2）。Task 2（删除指纹重算）同理。**只有 `RetrySyncRuleFailures` 绑定与「重试失败」按钮是可选的**。

**若你不想做这个按钮**：删掉 Task 1 的 `RetryFailed` 方法 / 绑定 / 重试用例，并精确删掉 Task 4（SyncCard.vue）里的四处 —— (a) import 行去掉 `RetrySyncRuleFailures`；(b) 删掉 `const canRetry = computed(...)`； (c) 删掉 `async function retry() { ... }` 整个函数；(d) 删掉模板里 `v-if="canRetry"` 那个按钮。 Task 6 不受影响（它不引用 retry）。同时在 spec §9.1 里注明删除该接口 —— 不要留下一个永不实现、也永不使用的声明。Task 1/2 的统计与指纹修正照旧。

---

### Task 1: 后端补 RetrySyncRuleFailures 绑定 + 失败/冲突统计修正（按钮可选，见上）

**Files:**
- Modify: `internal/sync/ctrl.go`（新增 `RetryFailed`；修正 `ActionConflict`/`ResolveConflict` 的冲突计数）
- Modify: `internal/sync/state.go`（`FailedItem` 增 `Action` 字段）
- Modify: `internal/sync/ctrl_test.go`
- Modify: `app.go`
- Modify: `app_test.go`
- Regenerate: `frontend/wailsjs/go/main/App.d.ts`、`App.js`（`models.ts` 不会变）

**Interfaces:**
- Consumes: 现有 `ruleRuntime.state *StateStore`、`ruleRuntime.queue map[string]watch.Kind`、`ruleRuntime.resolved map[string]Action`、`ruleRuntime.wake chan struct{}`、`StateFile.Failed []FailedItem`
- Produces: `func (c *Ctrl) RetryFailed(id string) (int, error)`；绑定 `func (a *App) RetrySyncRuleFailures(id string) error`（前端调用名 `RetrySyncRuleFailures`）；`FailedItem.Action Action`（`json:"action,omitempty"`）

- [ ] **Step 1: 写失败的测试**

在 `internal/sync/ctrl_test.go` 末尾追加：

~~~go
// M2：RetryFailed 重新入队失败项、清空失败计数与清单，并重放用户当初的 save_as 裁决。
// 用 newManualCtrl/attachRuntime 手动登记运行时（不启动 goroutine），因此不需要
// 300ms 去抖容差，也不是时序相关的。
func TestRetryFailedRequeuesClearsAndReplaysSaveAs(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, _ := newManualCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)

	r.state.With(func(d *StateFile) {
		d.Failed = []FailedItem{
			{RelPath: "a.txt", Err: "boom", At: "2026-09-10T10:00:00Z", Action: ActionGet},
			{RelPath: "b/c.txt", Err: "boom", At: "2026-09-10T10:00:01Z", Action: ActionGet},
			{RelPath: "secret.key", Err: "boom", At: "2026-09-10T10:00:02Z", Action: ActionSaveAs},
		}
	})
	r.mu.Lock()
	// 故意让计数大于失败条数：模拟「摘除 d.Failed 之后、更新 stats 之前引擎又记了新失败」。
	// 重试只应减去实际重排的 3 条（FIX 5），不能硬清零。
	r.stats.Failed = 5
	r.mu.Unlock()

	n, err := c.RetryFailed(rule.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n != 3 {
		t.Fatalf("应重试 3 条，得到 %d", n)
	}
	r.mu.Lock()
	gotA, gotB, gotC := r.queue["a.txt"], r.queue["b/c.txt"], r.queue["secret.key"]
	resolved := r.resolved["secret.key"]
	failedStat := r.stats.Failed
	r.mu.Unlock()
	if gotA != watch.KindWrite || gotB != watch.KindWrite || gotC != watch.KindWrite {
		t.Fatalf("三条都应入队为 KindWrite，得到 %v %v %v", gotA, gotB, gotC)
	}
	if resolved != ActionSaveAs {
		t.Fatalf("save_as 失败项必须重放为 resolved=ActionSaveAs，得到 %v", resolved)
	}
	if failedStat != 2 {
		t.Fatalf("应按实际重排条数扣减（5-3=2）而不是清零，得到 %d", failedStat)
	}
	r.state.With(func(d *StateFile) {
		if len(d.Failed) != 0 {
			t.Fatalf("重试后失败清单必须清空，得到 %#v", d.Failed)
		}
	})
}

func TestRetryFailedErrorsWhenNotRunning(t *testing.T) {
	c := NewCtrl(Deps{StateDir: t.TempDir()})
	if _, err := c.RetryFailed("nope"); err == nil {
		t.Fatal("规则未运行必须报错")
	}
}

// M3：冲突计数必须反映**当前仍挂起**的冲突数（spec §9.2 要求 >0 才显示按钮），
// 而不是只增不减的累加器 —— 否则最后一条裁决完，「冲突」按钮永远不消失。
func TestConflictStatReflectsPendingNotTotal(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)

	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("much longer local content"), 0644); err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["a.txt"] = &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"}
	})
	c.applyOne(r, "a.txt", watch.KindWrite, &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"})
	if got := c.Stats()[rule.ID].Conflicts; got != 1 {
		t.Fatalf("产生一条冲突后计数应为 1，得到 %d", got)
	}
	// 裁决 keep_local：冲突出队，计数必须归零。
	if err := c.ResolveConflict(rule.ID, "a.txt", ConflictKeepLocal, LocalState{Exists: true, Size: 24, ModTime: "local-t"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := c.Stats()[rule.ID].Conflicts; got != 0 {
		t.Fatalf("裁决后计数必须归零，得到 %d", got)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sync/ -run RetryFailed -v`
Expected: 编译失败，`undefined: (\*Ctrl).RetryFailed`

- [ ] **Step 3: 实现**

在 `internal/sync/state.go` 的 `FailedItem` 上补动作字段（M2b）：

~~~go
type FailedItem struct {
	RelPath string `json:"rel_path"`
	Err     string `json:"err"`
	At      string `json:"at"`
	// Action 记录失败时引擎正在执行的动作（ActionGet / ActionSaveAs）。
	// 手动重试必须原样重放用户的 save_as 裁决，而不是重跑 Decide 再推导一次
	// —— 对同一冲突状态 Decide 只会再判一次 Conflict，用户点过的「另存远端版本」
	// 会变成一张冲突卡片（M2c）。
	Action Action `json:"action,omitempty"`
}
~~~

在 `internal/sync/ctrl.go` 的失败追加处（`applyAction` 的 `ActionGet, ActionSaveAs` 分支）记录 `action`（它已在作用域内）：

~~~go
			r.state.With(func(d *StateFile) {
				d.Failed = append(d.Failed, FailedItem{RelPath: rel, Err: lastErr.Error(),
					At: time.Now().Format(time.RFC3339), Action: action})
			})
~~~

在 `internal/sync/ctrl.go` 里 `ConfirmDeletes` 之后新增：

~~~go
// RetryFailed 把状态文件里记录的失败项重新入队并唤醒 loop goroutine。
// 与 ResolveConflict 同一约束：**只入队与唤醒，绝不自己传输** —— UI 线程持状态锁
// 去下载会与引擎的串行传输形成 ABBA 死锁。
func (c *Ctrl) RetryFailed(id string) (int, error) {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return 0, fmt.Errorf("规则未在运行: %s", id)
	}
	type retryItem struct {
		rel    string
		saveAs bool
	}
	var items []retryItem
	r.state.With(func(d *StateFile) {
		if len(d.Failed) == 0 {
			return
		}
		items = make([]retryItem, 0, len(d.Failed))
		for _, f := range d.Failed {
			if f.RelPath != "" {
				items = append(items, retryItem{rel: f.RelPath, saveAs: f.Action == ActionSaveAs})
			}
		}
		d.Failed = nil
	})
	r.mu.Lock()
	for _, it := range items {
		r.queue[it.rel] = watch.KindWrite
		if it.saveAs {
			// 重放用户当初的 save_as 裁决；直接重跑 Decide 会把「另存远端版本」变成冲突卡片。
			if r.resolved == nil {
				r.resolved = map[string]Action{}
			}
			r.resolved[it.rel] = ActionSaveAs
		}
	}
	// 只减去本次真正重排的条数，**不能直接清零**：r.state.With 摘除 d.Failed 之后、
	// 这里更新 stats 之前，引擎 goroutine 可能又记下一个新失败（r.stats.Failed++ 并
	// append 到 d.Failed）。硬清零会把这个新失败的计数抹掉、又不会重新入队，卡片于是
	// 显示「失败 0」而失败清单里仍有条目（-race 看不到这种逻辑竞态）。
	if r.stats.Failed > len(items) {
		r.stats.Failed -= len(items)
	} else {
		r.stats.Failed = 0
	}
	r.mu.Unlock()
	// FIX 4：即使 items 为空（Failed 里全是空 RelPath 被过滤掉），也必须 Flush，
	// 否则刚清空的 d.Failed 不落盘，重启后又冒出来。
	_ = r.state.Flush()
	if len(items) == 0 {
		return 0, nil
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return len(items), nil
}
~~~

把 `applyAction` 的 `ActionConflict` 分支改成"当前挂起数"（M3）：

~~~go
	case ActionConflict:
		// ent 可能为 nil（"远端元信息未知但本地已存在"，见 Decide）；此时远端
		// 字段保持零值，绝不能解引用。
		remoteSize, remoteMTime := int64(0), ""
		if ent != nil {
			remoteSize, remoteMTime = ent.RemoteSize, ent.RemoteMTime
		}
		r.state.With(func(d *StateFile) {
			UpsertConflict(d, Conflict{RelPath: rel, RemoteSize: remoteSize, RemoteMTime: remoteMTime,
				LocalSize: st.Size, LocalMTime: st.ModTime, DetectedAt: time.Now().Format(time.RFC3339)})
			// M3：计数 = 当前仍挂起的冲突数，不是只增不减的累加器。
			r.mu.Lock()
			r.stats.Conflicts = len(d.Conflicts)
			r.mu.Unlock()
		})
		// 原来的 r.stats.Conflicts++ 删除
		c.emit(r.rule.ID, "warn", rel+" 本地已修改，未覆盖")
~~~

`Ctrl.ResolveConflict` 的 `state.With` 回调内同步刷新（M3）：

~~~go
	r.state.With(func(d *StateFile) {
		req, err = ResolveConflict(d, rel, action, local, time.Now().Format(time.RFC3339))
		if err == nil {
			// 裁决即移除一条冲突：计数必须同步下降，卡片上的「冲突」按钮才会消失。
			r.mu.Lock()
			r.stats.Conflicts = len(d.Conflicts)
			r.mu.Unlock()
		}
	})
~~~

在 `app.go` 的 `ConfirmSyncRuleDeletes` 之后新增绑定：

~~~go
// RetrySyncRuleFailures 让引擎重试该规则记录在案的失败项。只入队，不在这里传输。
func (a *App) RetrySyncRuleFailures(id string) error {
	_, err := a.sync.RetryFailed(id)
	return err
}
~~~

在 `app_test.go` 末尾追加绑定契约用例（spec §10.5）：

~~~go
// RetrySyncRuleFailures 对未运行的规则必须返回错误（绑定契约）。
func TestRetrySyncRuleFailuresRejectsNonRunningRule(t *testing.T) {
	a := newTestApp(t)
	if err := a.RetrySyncRuleFailures("nope"); err == nil {
		t.Fatal("未运行的规则必须返回错误")
	}
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sync/ -race -count=1 -run 'RetryFailed|ConflictStat' -v`
Expected: 三个用例（重试入队+重放 save_as、未运行报错、冲突计数）PASS

Run（在仓库根目录）: `go test . -run RetrySyncRuleFailures -v`
Expected: 绑定契约用例 PASS

再跑全量：Run: `go test ./... -race -count=1` / `go vet ./...`
Expected: 全绿、无输出

- [ ] **Step 5: 重新生成 wailsjs 绑定**

Run（**在仓库根目录**）: `wails generate module`
Expected: **只有** `frontend/wailsjs/go/main/App.d.ts` 与 `App.js` 出现改动，且 `App.d.ts` 里能搜到 `RetrySyncRuleFailures`。
**`models.ts` 不会变**：wails v2.15.0 不为 map 的 value 结构体生成 TS 类型，`sync.SyncRuleStat` 本来就不存在（见 Global Constraints 与 follow-ups 的已知缺口）——不要为了「让生成物完整」去手改它。
若 `wails` 不在 PATH：用 `$(go env GOPATH)/bin/wails generate module`。
注意：wails 可能顺手把 `frontend/wailsjs/runtime/*` 的权限位改成 100755，用 `git checkout -- frontend/wailsjs/runtime` 还原，只提交预期变动。

- [ ] **Step 6: 提交**

~~~bash
gofmt -s -w internal/sync/ app.go
git add internal/sync/ app.go app_test.go frontend/wailsjs
git commit -m "feat(sync): 新增 RetrySyncRuleFailures 绑定,重试失败项只入队不传输;修正失败与冲突计数"
~~~

---

### Task 2: 后端修正待确认删除的指纹（事件路径 + 失效）

**Files:**
- Modify: `internal/sync/ctrl.go`（新增 `ruleRuntime.refreshDeleteFingerprint()` 并接到所有 `pendingDel` 变更点）
- Modify: `internal/sync/ctrl_test.go`

**Interfaces:**
- Consumes: 现有 `ruleRuntime.pendingDel []string`、`ruleRuntime.delFP string`、`SyncRuleStat.DeletePending/DeleteFingerprint/DeletePaths`、`Ctrl.ConfirmDeletes` 的等值校验
- Produces: `func (r *ruleRuntime) refreshDeleteFingerprint()`（**必须在持有 `r.mu` 时调用**）

**为什么必须有（M5）**：`align` 会给挂起删除写指纹，但事件路径（`applyOne` 的 `ActionDelete`）只 `append` `pendingDel`、从不写指纹 ⇒ 事件路径挂起的删除永远无法确认（`ConfirmDeletes` 的指纹校验必然不等）；且 `pendingDel` 增大后旧指纹不刷新，用户可能确认一批与 UI 所示不符的删除。

- [ ] **Step 1: 写失败的测试**

在 `internal/sync/ctrl_test.go` 末尾追加（同样用 `newManualCtrl`/`attachRuntime`，不启动 loop）：

~~~go
// M5（性质 1）：事件路径挂起的删除也必须带非空指纹，否则「确认删除」永远确认不了。
func TestEventPathDeleteSuspensionHasFingerprint(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	rule.MirrorDelete = true
	r := attachRuntime(t, c, rule)

	target := filepath.Join(local, "gone.txt")
	if err := os.WriteFile(target, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["gone.txt"] = &Entry{
			RemoteSize: st.Size(), RemoteMTime: "2026-09-10 10:00",
			LocalSize: st.Size(), LocalMTime: FormatModTime(st.ModTime()), HasLocal: true,
		}
	})
	// 事件路径：只说「远端删了」，remote 元信息未知。
	c.applyOne(r, "gone.txt", watch.KindDelete, nil)

	got := c.Stats()[rule.ID]
	if got.DeletePending != 1 {
		t.Fatalf("事件路径挂起删除数应为 1，得到 %d", got.DeletePending)
	}
	if got.DeleteFingerprint == "" {
		t.Fatal("事件路径挂起的删除必须带非空指纹")
	}
}

// M5（性质 2）：pendingDel 变化后，先前发出的指纹必须失效。
func TestDeleteFingerprintInvalidatedWhenPendingGrows(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	rule.MirrorDelete = true
	r := attachRuntime(t, c, rule)

	seedDeletable := func(rel string) {
		target := filepath.Join(local, rel)
		if err := os.WriteFile(target, []byte("abc"), 0644); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		r.state.With(func(d *StateFile) {
			d.Entries[rel] = &Entry{RemoteSize: st.Size(), RemoteMTime: "t",
				LocalSize: st.Size(), LocalMTime: FormatModTime(st.ModTime()), HasLocal: true}
		})
	}
	seedDeletable("a.txt")
	c.applyOne(r, "a.txt", watch.KindDelete, nil)
	r.mu.Lock()
	first := r.stats.DeleteFingerprint
	r.mu.Unlock()
	if first == "" {
		t.Fatal("首轮指纹不应为空")
	}

	seedDeletable("b.txt")
	c.applyOne(r, "b.txt", watch.KindDelete, nil)
	r.mu.Lock()
	second := r.stats.DeleteFingerprint
	r.mu.Unlock()
	if second == first {
		t.Fatal("pendingDel 变化后指纹必须失效")
	}
	// 旧指纹必须被 ConfirmDeletes 拒绝（不会误删新增的那一批）。
	if err := c.ConfirmDeletes(rule.ID, first); err == nil {
		t.Fatal("使用过期指纹确认必须被拒绝")
	}
}

// M5（FIX 3）：pendingDel 清空后必须把指纹一并清掉 —— 空集合不该有可确认的指纹。
func TestDeleteFingerprintClearedWhenPendingEmpty(t *testing.T) {
	c, rule, _ := newManualCtrl(t, nil, nil)
	r := attachRuntime(t, c, rule)

	r.mu.Lock()
	r.pendingDel = []string{"a.txt"}
	r.refreshDeleteFingerprint()
	r.mu.Unlock()
	if got := c.Stats()[rule.ID].DeleteFingerprint; got == "" {
		t.Fatal("非空挂起集必须有指纹")
	}

	r.mu.Lock()
	r.pendingDel = nil
	r.refreshDeleteFingerprint()
	r.mu.Unlock()
	got := c.Stats()[rule.ID]
	if got.DeleteFingerprint != "" {
		t.Fatalf("清空后 DeleteFingerprint 必须为空串，得到 %q", got.DeleteFingerprint)
	}
	if got.DeletePaths != nil {
		t.Fatalf("清空后 DeletePaths 必须为 nil，得到 %#v", got.DeletePaths)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sync/ -run 'EventPathDeleteSuspension|DeleteFingerprintInvalidated|DeleteFingerprintCleared' -v`
Expected: `undefined: refreshDeleteFingerprint` / 事件路径指纹为空

- [ ] **Step 3: 实现**

在 `internal/sync/ctrl.go` 新增（`hash/fnv`、`fmt`、`time` 已在 import 里）：

~~~go
// refreshDeleteFingerprint 从**当前** r.pendingDel 重新计算删除指纹与统计。
// **必须在持有 r.mu 时调用**。
//
// 非空集合的指纹格式与旧 align 内联实现**完全一致**（轮次时间戳 + 路径集合 FNV），
// 因此 ConfirmDeletes 的等值校验无需改动。**空集合是有意的例外**：旧实现即使
// pendingDel 为空也会写一个非空指纹（DeletePaths 为空 slice），本 helper 改为
// delFP="" / DeleteFingerprint="" / DeletePaths=nil ——「当前没有任何挂起删除」
// 本就不该被表示成一个可被确认的指纹。
//
// 统一收口的原因：事件路径（applyOne 的 ActionDelete）也会往 pendingDel 追加，
// 若只有 align 写指纹，事件路径挂起的删除永远无法确认；且 pendingDel 变大后
// 旧指纹不刷新，用户会确认一批与 UI 所示不符的删除。
func (r *ruleRuntime) refreshDeleteFingerprint() {
	if len(r.pendingDel) == 0 {
		r.delFP = ""
		r.stats.DeletePending = 0
		r.stats.DeleteFingerprint = ""
		r.stats.DeletePaths = nil
		return
	}
	h := fnv.New64a()
	for _, rel := range r.pendingDel {
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
	}
	r.delFP = fmt.Sprintf("%d-%x", time.Now().UnixNano(), h.Sum64())
	r.stats.DeletePending = len(r.pendingDel)
	r.stats.DeleteFingerprint = r.delFP
	r.stats.DeletePaths = append([]string{}, r.pendingDel...)
}
~~~

把四处 `pendingDel` 变更点都改为调用它（都在 `r.mu` 内）：

`align`（替换原内联 FNV 与三行赋值）：

~~~go
	r.mu.Lock()
	r.pendingDel = dels
	r.refreshDeleteFingerprint()
	r.logCount = map[string]int{} // 每轮对账后重置节流计数
	// 完整扫描成功即视为已完成对账，主动解除删除暂停。因此 RootGone/Overflow
	// 两个闸门臂只在事件路径（blockDels 仍可能为真、且没有全量扫描背书）上生效。
	r.blockDels = false
	r.stats.Pending = len(snap.Entries)
	r.mu.Unlock()
~~~

`applyAction` 的 `ActionDelete` 分支：

~~~go
	case ActionDelete:
		r.mu.Lock()
		r.pendingDel = append(r.pendingDel, rel)
		r.refreshDeleteFingerprint()
		r.mu.Unlock()
		c.emit(r.rule.ID, "info", "待删除本地 "+rel+"（"+reason+"）")
~~~

`maybeDelete` 放行后的清理：

~~~go
	r.mu.Lock()
	r.pendingDel = nil
	r.refreshDeleteFingerprint()
	r.mu.Unlock()
	c.executeDeletes(r, batch)
~~~

`ConfirmDeletes` 末尾的清理：

~~~go
	final := append([]string{}, r.pendingDel...)
	r.pendingDel = nil
	r.refreshDeleteFingerprint()
	r.mu.Unlock()
	c.executeDeletes(r, final)
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sync/ -race -count=1 -run 'DeleteFingerprint|DeleteSuspension|ConfirmDeletes' -v`
Expected: 全 PASS

再跑全量：Run: `go test ./... -race -count=1` / `go vet ./...`
Expected: 全绿、无输出

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sync/
git add internal/sync/
git commit -m "fix(sync): 待确认删除的指纹在事件路径也生成,pendingDel 变化即失效"
~~~

---

### Task 3: 纯函数模块 utils/sync.js（本计划的可测核心）

**Files:**
- Create: `frontend/src/utils/sync.js`
- Test: `frontend/src/utils/sync.test.js`

**Interfaces:**
- Produces（Task 4/5/6 全部依赖这些名字，不要改名）：
  `SYNC_STATES`、`DEFAULT_EXCLUDES`、`stateLabel(state)`、`dotClass(state)`、`kindLabel(kind)`、`canMirrorDelete(kind)`、`mirrorDeleteHint(kind)`、`probeBadge(stats)`、`countsOf(stats)`、`alignText(stats)`、`currentFileName(stats)`、`statsOf(all, id)`、`deleteSummary(stats)`、`retryable(stats)`、`conflictActionLabel(action)`、`parseExcludes(text)`、`formatExcludes(list)`、`newSyncRuleForm()`、`syncRuleToForm(rule)`、`formToSyncRule(form)`、`validateSyncRuleForm(form)`、`shouldRefresh(sourceType, sourceTypes)`、`ruleLabel(rule)`

- [ ] **Step 1: 写失败的测试**

创建 `frontend/src/utils/sync.test.js`：

~~~js
import { describe, it, expect } from 'vitest'
import {
  DEFAULT_EXCLUDES, stateLabel, dotClass, kindLabel, canMirrorDelete, mirrorDeleteHint,
  probeBadge, countsOf, alignText, currentFileName, statsOf, deleteSummary, retryable,
  conflictActionLabel, parseExcludes, formatExcludes, ruleLabel,
  newSyncRuleForm, syncRuleToForm, formToSyncRule, validateSyncRuleForm, shouldRefresh,
} from './sync'

// 注意：SyncRuleStat 在 Go 侧没有 json tag，故运行时键是 PascalCase。
const stat = {
  Mode: 'poll', Reason: '远端无 inotifywait', PollIntervalS: 3,
  Pending: 4, Done: 10, Failed: 2, Conflicts: 1,
  AlignScanned: 3, AlignTotal: 120, CurrentFile: 'a/b.txt',
  SourceMissing: true, DeletePending: 2, DeleteFingerprint: 'fp-1',
  DeletePaths: ['x.txt', 'y.txt'],
}

describe('sync 展示辅助', () => {
  it('状态映射为中文，未知回退原值', () => {
    expect(stateLabel('connected')).toBe('已连接')
    expect(stateLabel('reconnecting')).toBe('重连中')
    expect(stateLabel('bogus')).toBe('bogus')
    expect(stateLabel('')).toBe('未知')
  })

  it('状态决定圆点样式', () => {
    expect(dotClass('connected')).toBe('on')
    expect(dotClass('connecting')).toBe('warn')
    expect(dotClass('reconnecting')).toBe('warn')
    expect(dotClass('error')).toBe('err')
    expect(dotClass('stopped')).toBe('off')
  })

  it('kind 标签与 mirror_delete 可用性（决策 15）', () => {
    expect(kindLabel('dir')).toBe('目录')
    expect(kindLabel('file')).toBe('单文件')
    expect(canMirrorDelete('dir')).toBe(true)
    expect(canMirrorDelete('file')).toBe(false)
    expect(mirrorDeleteHint('dir')).toBe('')
    expect(mirrorDeleteHint('file')).toContain('无效')
  })

  it('探测徽章：inotify 正常、poll 降级且带 Reason', () => {
    expect(probeBadge({ Mode: 'inotify' })).toEqual({ text: '实时', level: 'ok', title: 'inotify 实时探测' })
    const b = probeBadge(stat)
    expect(b.text).toBe('轮询 3s')
    expect(b.level).toBe('warn')
    expect(b.title).toBe('远端无 inotifywait')
  })

  it('poll_interval_s 缺省回退 5s', () => {
    expect(probeBadge({ Mode: 'poll' }).text).toBe('轮询 5s')
  })

  it('S8：Mode 未定（空串/缺 stats/未知值）时中性徽章，不谎报轮询', () => {
    expect(probeBadge({ Mode: '' })).toEqual({ text: '探测中', level: '', title: '探测方式尚未确定' })
    expect(probeBadge(undefined)).toEqual({ text: '探测中', level: '', title: '探测方式尚未确定' })
    expect(probeBadge({ Mode: 'weird' }).text).toBe('探测中')
  })

  it('四组计数读取 PascalCase 键', () => {
    const c = countsOf(stat)
    expect(c.map(x => x.value)).toEqual([4, 10, 2, 1])
    expect(countsOf(undefined).map(x => x.value)).toEqual([0, 0, 0, 0])
  })

  it('对齐进度只在进行中显示', () => {
    expect(alignText(stat)).toBe('对齐 3/120')
    expect(alignText({ AlignScanned: 120, AlignTotal: 120 })).toBe('')
    expect(alignText({ AlignTotal: 0 })).toBe('')
    expect(currentFileName(stat)).toBe('a/b.txt')
  })

  it('statsOf 容忍缺失', () => {
    expect(statsOf({ r1: stat }, 'r1')).toBe(stat)
    expect(statsOf({}, 'r1')).toEqual({})
    expect(statsOf(undefined, 'r1')).toEqual({})
  })

  it('待确认删除摘要：有挂起才有指纹', () => {
    expect(deleteSummary(stat)).toEqual({ count: 2, fingerprint: 'fp-1', paths: ['x.txt', 'y.txt'] })
    expect(deleteSummary({ DeletePending: 0 })).toBe(null)
    expect(deleteSummary(undefined)).toBe(null)
  })

  it('可重试判断看 Failed 计数', () => {
    expect(retryable(stat)).toBe(true)
    expect(retryable({ Failed: 0 })).toBe(false)
    expect(retryable(undefined)).toBe(false)
  })

  it('冲突动作映射中文', () => {
    expect(conflictActionLabel('keep_local')).toBe('保留本地')
    expect(conflictActionLabel('take_remote')).toBe('用远端覆盖')
    expect(conflictActionLabel('save_as')).toBe('另存远端版本')
    expect(conflictActionLabel('weird')).toBe('weird')
  })

  it('S6：显示名回退 name → remote_path → host', () => {
    expect(ruleLabel({ name: 'conf', remote_path: '/r', host: 'h' })).toBe('conf')
    expect(ruleLabel({ name: '', remote_path: '/r', host: 'h' })).toBe('/r')
    expect(ruleLabel({ name: '', remote_path: '', host: 'h' })).toBe('h')
    expect(ruleLabel(undefined)).toBe('')
  })
})

describe('排除项与表单', () => {
  it('排除项文本解析：去空行/去首尾空白/保序去重', () => {
    expect(parseExcludes('  .git/ \n\nnode_modules/\n.git/\n')).toEqual(['.git/', 'node_modules/'])
    expect(parseExcludes('')).toEqual([])
    expect(parseExcludes(null)).toEqual([])
    expect(formatExcludes(['a', 'b'])).toBe('a\nb')
    expect(formatExcludes(null)).toBe('')
  })

  it('M1：表单默认值镜像 store.go:83-85 的 DefaultExcludes（五条全在，逐行预置）', () => {
    const f = newSyncRuleForm()
    expect(f.kind).toBe('dir')
    expect(f.max_depth).toBe(0)
    expect(f.poll_interval_s).toBe(5)
    expect(f.auto_reconnect).toBe(true) // 纯函数兜底；全局默认由 SyncView 创建时注入（S3）
    expect(f.mirror_delete).toBe(false)
    expect(f.enabled).toBe(false)
    // 关键：表单必须预置五条默认排除项。若提交空数组，后端 Normalize 只在
    // Excludes==nil 时兜底，空 slice 获胜，.git/、node_modules/ 会被同步（M1）。
    expect(f.excludesText).toBe('.git/\nnode_modules/\n*.swp\n*~\n.DS_Store')
    expect(parseExcludes(f.excludesText)).toEqual(['.git/', 'node_modules/', '*.swp', '*~', '.DS_Store'])
    expect(DEFAULT_EXCLUDES).toEqual(['.git/', 'node_modules/', '*.swp', '*~', '.DS_Store'])
  })

  it('规则 <-> 表单往返不丢字段', () => {
    const rule = {
      id: 'r1', name: 'conf', host: 'prod-01', user: 'alice', kind: 'dir',
      remote_path: '/srv/conf', local_path: '/tmp/conf', max_depth: 2,
      excludes: ['.git/'], mirror_delete: true, force_poll: true,
      poll_interval_s: 7, auto_reconnect: false, enabled: true,
    }
    const f = syncRuleToForm(rule)
    expect(f.excludesText).toBe('.git/')
    expect(f.auto_reconnect).toBe(false)
    expect(formToSyncRule(f)).toEqual(rule)
  })

  it('缺 auto_reconnect（undefined）回退 true（真正的全局默认由视图注入，不再声称与后端缺键默认一致）', () => {
    expect(syncRuleToForm({ id: 'x' }).auto_reconnect).toBe(true)
  })

  it('单文件规则提交时强制 mirror_delete=false', () => {
    const f = { ...newSyncRuleForm(), kind: 'file', mirror_delete: true }
    expect(formToSyncRule(f).mirror_delete).toBe(false)
  })

  it('前端校验覆盖明显错误（host 正则与控制字符仍由后端强制，不声称全部对齐）', () => {
    const ok = { ...newSyncRuleForm(), host: 'h', remote_path: '/r', local_path: '/l' }
    expect(validateSyncRuleForm(ok)).toBe('')
    expect(validateSyncRuleForm({ ...ok, host: '' })).toBe('请选择主机')
    expect(validateSyncRuleForm({ ...ok, remote_path: '' })).toBe('远端路径不能为空')
    expect(validateSyncRuleForm({ ...ok, remote_path: 'rel/path' })).toContain('绝对路径')
    expect(validateSyncRuleForm({ ...ok, remote_path: '~/x' })).toBe('')
    expect(validateSyncRuleForm({ ...ok, local_path: '  ' })).toBe('本地路径不能为空')
    expect(validateSyncRuleForm({ ...ok, max_depth: 999 })).toContain('max_depth')
    expect(validateSyncRuleForm({ ...ok, max_depth: -1 })).toBe('')
    expect(validateSyncRuleForm({ ...ok, poll_interval_s: 0 })).toContain('poll_interval_s')
  })

  it('实时刷新只认配置的来源类型', () => {
    expect(shouldRefresh('sync', ['sync', 'system'])).toBe(true)
    expect(shouldRefresh('system', ['sync', 'system'])).toBe(true)
    expect(shouldRefresh('sftp', ['sync', 'system'])).toBe(false)
  })
})
~~~

- [ ] **Step 2: 运行测试确认失败**

Run（在 `frontend/` 下）: `npx vitest run src/utils/sync.test.js`
Expected: FAIL —— 无法解析 `./sync`（模块不存在）

- [ ] **Step 3: 实现**

创建 `frontend/src/utils/sync.js`：

~~~js
// 同步规则的纯展示/表单逻辑：SyncView、SyncCard、SyncConflictsDialog 共用。
// 这里也是本特性唯一被 vitest 覆盖的一层 —— 仓库没有组件测试基建，
// 所以凡是能写成纯函数的一律放这里，组件只做渲染与接线。
//
// 重要：sync.SyncRuleStat 在 Go 侧没有 json tag，运行时键是 **PascalCase**
// （Mode/Pending/AlignTotal/...，定义见 internal/sync/ctrl.go:39-56）；而
// config.SyncRule 有 json tag，是 snake_case。两者不要混。
// 注意：frontend/wailsjs/go/models.ts 里**没有** sync.SyncRuleStat
// （wails v2.15.0 不为 map 的 value 结构体生成 TS 类型），不要照它写。

export const SYNC_STATES = {
  stopped: '已停止',
  connecting: '连接中',
  connected: '已连接',
  reconnecting: '重连中',
  error: '错误',
}

// **镜像** internal/config/store.go:83-85 的 DefaultExcludes()，不是同源。
// UI 新建规则时表单预置这五条：若提交空数组，后端 Normalize 只在 Excludes==nil
// 时兜底，空 slice 获胜，.git/、node_modules/ 等会被同步。
// 后端改默认值时这里必须同步改（见 Global Constraints）。
export const DEFAULT_EXCLUDES = ['.git/', 'node_modules/', '*.swp', '*~', '.DS_Store']

export function stateLabel(state) {
  if (!state) return '未知'
  return SYNC_STATES[state] || state
}

export function dotClass(state) {
  if (state === 'connected') return 'on'
  if (state === 'connecting' || state === 'reconnecting') return 'warn'
  if (state === 'error') return 'err'
  return 'off'
}

export function kindLabel(kind) {
  return kind === 'file' ? '单文件' : '目录'
}

// 决策 15：单文件规则的 mirror_delete 无效（源消失一律不删本地）。
export function canMirrorDelete(kind) {
  return kind !== 'file'
}

export function mirrorDeleteHint(kind) {
  if (canMirrorDelete(kind)) return ''
  return '单文件规则下 mirror_delete 无效：源消失一律不删本地'
}

function intOf(v) {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

// 探测徽章：实时（inotify）或轮询 Ns；降级时黄色并把后端 Reason 作为悬浮说明。
// S8：Mode 尚未由后端确定时（启动瞬间的空串、缺 stats、未知值）返回中性「探测中」，
// 绝不能默认按轮询渲染 —— 那会在探测完成前就谎报「轮询 Ns / 未使用 inotify」。
export function probeBadge(stats) {
  const s = stats || {}
  if (s.Mode === 'inotify') {
    return { text: '实时', level: 'ok', title: 'inotify 实时探测' }
  }
  if (s.Mode !== 'poll') {
    return { text: '探测中', level: '', title: '探测方式尚未确定' }
  }
  const secs = intOf(s.PollIntervalS) > 0 ? intOf(s.PollIntervalS) : 5
  return {
    text: '轮询 ' + secs + 's',
    level: 'warn',
    title: s.Reason || '未使用 inotify，已降级为轮询',
  }
}

export function countsOf(stats) {
  const s = stats || {}
  return [
    { key: 'pending', label: '待处理', value: intOf(s.Pending) },
    { key: 'done', label: '已完成', value: intOf(s.Done) },
    { key: 'failed', label: '失败', value: intOf(s.Failed) },
    { key: 'conflicts', label: '冲突', value: intOf(s.Conflicts) },
  ]
}

export function alignText(stats) {
  const s = stats || {}
  const total = intOf(s.AlignTotal)
  const scanned = intOf(s.AlignScanned)
  if (total <= 0 || scanned >= total) return ''
  return '对齐 ' + scanned + '/' + total
}

export function currentFileName(stats) {
  const s = stats || {}
  return s.CurrentFile || ''
}

export function statsOf(all, id) {
  if (!all || !id) return {}
  return all[id] || {}
}

// S6：单条规则的展示名回退顺序统一为 name → remote_path → host。
// SyncCard、SyncView 的确认/日志文案、冲突对话框标题都必须用它，不能各写一套。
export function ruleLabel(rule) {
  const r = rule || {}
  return r.name || r.remote_path || r.host || ''
}

// 待确认删除：只有真挂起时才有指纹，UI 必须把指纹原样回传给确认绑定。
export function deleteSummary(stats) {
  const s = stats || {}
  const count = intOf(s.DeletePending)
  if (count <= 0 || !s.DeleteFingerprint) return null
  return {
    count,
    fingerprint: s.DeleteFingerprint,
    paths: Array.isArray(s.DeletePaths) ? s.DeletePaths : [],
  }
}

export function retryable(stats) {
  return intOf((stats || {}).Failed) > 0
}

export function conflictActionLabel(action) {
  if (action === 'keep_local') return '保留本地'
  if (action === 'take_remote') return '用远端覆盖'
  if (action === 'save_as') return '另存远端版本'
  return action
}

// 排除项在表单里是多行文本；提交时去空行、去首尾空白、保序去重。
export function parseExcludes(text) {
  const out = []
  const parts = String(text == null ? '' : text).split('\n')
  for (const raw of parts) {
    const s = raw.trim()
    if (s && out.indexOf(s) === -1) out.push(s)
  }
  return out
}

export function formatExcludes(list) {
  if (!Array.isArray(list)) return ''
  return list.join('\n')
}

// excludesText 预置 DEFAULT_EXCLUDES：这是**镜像** internal/config/store.go:83-85，
// 不是「配合 Normalize」——后端只在 Excludes == nil 时才填默认值，UI 提交空数组
// （非 nil）会让默认排除项全部丢失（M1）。两份列表必须同步修改。
//
// auto_reconnect 的纯函数兜底是 true；真正的默认值来自全局 App.AutoReconnectDefault，
// 由 SyncView 在创建时用 settings store 的值覆盖（S3）。这里保持纯函数、无 import。
export function newSyncRuleForm() {
  return {
    id: '',
    name: '',
    host: '',
    user: '',
    kind: 'dir',
    remote_path: '',
    local_path: '',
    max_depth: 0,
    excludesText: DEFAULT_EXCLUDES.join('\n'),
    mirror_delete: false,
    force_poll: false,
    poll_interval_s: 5,
    auto_reconnect: true,
    enabled: false,
  }
}

export function syncRuleToForm(rule) {
  const f = newSyncRuleForm()
  if (!rule) return f
  f.id = rule.id || ''
  f.name = rule.name || ''
  f.host = rule.host || ''
  f.user = rule.user || ''
  f.kind = rule.kind === 'file' ? 'file' : 'dir'
  f.remote_path = rule.remote_path || ''
  f.local_path = rule.local_path || ''
  f.max_depth = Number.isFinite(Number(rule.max_depth)) ? Number(rule.max_depth) : 0
  f.excludesText = formatExcludes(rule.excludes)
  f.mirror_delete = !!rule.mirror_delete
  f.force_poll = !!rule.force_poll
  f.poll_interval_s = Number(rule.poll_interval_s) > 0 ? Number(rule.poll_interval_s) : 5
  // auto_reconnect 是 *bool：缺失（undefined）按 true（对齐后端 Reconnect() 的 nil
  // 兜底）；真正的全局默认由后端 normalize() 从 App.AutoReconnectDefault 灌入，
  // UI 这条只是防御性回退（S3）。
  f.auto_reconnect = rule.auto_reconnect !== false
  f.enabled = !!rule.enabled
  return f
}

export function formToSyncRule(form) {
  const f = form || {}
  return {
    id: f.id || '',
    name: f.name || '',
    host: f.host || '',
    user: f.user || '',
    kind: f.kind === 'file' ? 'file' : 'dir',
    remote_path: String(f.remote_path == null ? '' : f.remote_path).trim(),
    local_path: String(f.local_path == null ? '' : f.local_path).trim(),
    max_depth: Number(f.max_depth) || 0,
    excludes: parseExcludes(f.excludesText),
    // 单文件规则的 mirror_delete 一律落 false：后端会拒，UI 也不该提交它。
    mirror_delete: canMirrorDelete(f.kind) ? !!f.mirror_delete : false,
    force_poll: !!f.force_poll,
    poll_interval_s: Number(f.poll_interval_s) || 5,
    auto_reconnect: !!f.auto_reconnect,
    enabled: !!f.enabled,
  }
}

// 只做前端能判的部分，**不声称与后端全部硬约束对齐**：host 正则
// （forward.ValidateHost）与控制字符检查故意省略，仍由后端 ValidateSyncRule 强制。
// 这里只为拦截明显无效输入、避免无谓往返（S11）。
export function validateSyncRuleForm(form) {
  const f = form || {}
  if (!f.host) return '请选择主机'
  if (f.kind !== 'dir' && f.kind !== 'file') return 'kind 必须是 dir 或 file'
  const remote = String(f.remote_path == null ? '' : f.remote_path).trim()
  if (!remote) return '远端路径不能为空'
  if (remote[0] !== '/' && remote[0] !== '~') return '远端路径必须是绝对路径或以 ~ 开头'
  if (!String(f.local_path == null ? '' : f.local_path).trim()) return '本地路径不能为空'
  const depth = Number(f.max_depth)
  if (!(depth === -1 || (depth >= 0 && depth <= 64))) return 'max_depth 必须是 -1 或 0..64'
  const pi = Number(f.poll_interval_s)
  if (!(pi >= 1 && pi <= 3600)) return 'poll_interval_s 必须在 1..3600'
  return ''
}

export function shouldRefresh(sourceType, sourceTypes) {
  return (sourceTypes || []).indexOf(sourceType) !== -1
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run（在 `frontend/` 下）: `npx vitest run src/utils/sync.test.js`
Expected: 全部 PASS

再跑全量前端测试：Run: `npx vitest run`
Expected: 原有 4 个文件 + 新增 1 个文件全过

- [ ] **Step 5: 提交**

~~~bash
git add frontend/src/utils/sync.js frontend/src/utils/sync.test.js
git commit -m "feat(web): 新增同步规则的纯展示与表单逻辑及其单测"
~~~

---

### Task 4: SyncCard.vue（单条规则卡片）

**Files:**
- Create: `frontend/src/components/SyncCard.vue`
- Create: `frontend/src/views/sync-smoke.test.js`（本任务创建，Task 5/6 扩展）

**Interfaces:**
- Consumes: Task 3 的 `stateLabel/dotClass/probeBadge/countsOf/alignText/currentFileName/deleteSummary/retryable/kindLabel/ruleLabel`；绑定 `StartSyncRule`、`StopSyncRule`、`RetrySyncRuleFailures`（Task 1）
- Produces: 组件 props `{ rule: Object(required), state: String, stats: Object }`；emits `edit`(rule)、`remove`(rule)、`log`(rule)、`conflicts`(rule)、`confirmDeletes`({rule, fingerprint, count})、`changed`

- [ ] **Step 1: 写组件**

创建 `frontend/src/components/SyncCard.vue`：

~~~vue
<script setup>
import { ref, computed } from 'vue'
import { StartSyncRule, StopSyncRule, RetrySyncRuleFailures } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import {
  stateLabel, dotClass, probeBadge, countsOf, alignText, currentFileName,
  deleteSummary, retryable, kindLabel, ruleLabel,
} from '../utils/sync'

const props = defineProps({
  rule: { type: Object, required: true },
  state: { type: String, default: 'stopped' },
  stats: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['edit', 'remove', 'log', 'conflicts', 'confirmDeletes', 'changed'])

const busy = ref(false)
const logStore = useLogStore()

const dot = computed(() => dotClass(props.state))
const badge = computed(() => probeBadge(props.stats))
const counts = computed(() => countsOf(props.stats))
const align = computed(() => alignText(props.stats))
const current = computed(() => currentFileName(props.stats))
const pending = computed(() => deleteSummary(props.stats))
const canRetry = computed(() => retryable(props.stats))
const sourceMissing = computed(() => !!props.stats.SourceMissing)
const conflicts = computed(() => Number(props.stats.Conflicts) || 0)
const logOn = computed(() => logStore.filterSource === props.rule.id)

// 启停失败写入日志面板（同步视图可见）而不是冒泡成全局 fatal，并照常刷新。
// S7：source_id 必须是规则 id —— 卡片的「日志」按钮按规则 id 过滤；写成恒定的
// 'rule' 会让恰好这些错误被过滤掉、永远看不见。
function report(msg) {
  logStore.add({
    source_id: props.rule.id,
    source_type: 'sync',
    level: 'error',
    message: ruleLabel(props.rule) + ': ' + msg,
    ts: new Date().toISOString(),
  })
}

async function toggle() {
  busy.value = true
  try {
    if (props.rule.enabled) await StopSyncRule(props.rule.id)
    else await StartSyncRule(props.rule.id)
  } catch (e) {
    report(String(e))
  } finally {
    emit('changed')
    busy.value = false
  }
}

async function retry() {
  busy.value = true
  try {
    await RetrySyncRuleFailures(props.rule.id)
  } catch (e) {
    report(String(e))
  } finally {
    emit('changed')
    busy.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="head">
      <span :class="['dot', dot]"></span>
      <span class="name">{{ ruleLabel(rule) }}</span>
      <span :class="['badge', badge.level]" :title="badge.title">{{ badge.text }}</span>
      <span v-if="sourceMissing" class="badge err" title="远端根目录不可见，已暂停删除直到对账确认">源缺失</span>
      <span class="meta">{{ kindLabel(rule.kind) }} · {{ rule.host }}:{{ rule.remote_path }} → {{ rule.local_path }}</span>
      <span class="state">{{ stateLabel(state) }}</span>
    </div>

    <div class="counts">
      <span v-for="c in counts" :key="c.key" :class="{ warn: c.key === 'conflicts' && c.value > 0 }">
        {{ c.label }} {{ c.value }}
      </span>
      <span v-if="align" class="align">{{ align }}</span>
      <span v-if="current" class="current" :title="current">· {{ current }}</span>
    </div>

    <div class="actions">
      <button class="ghost" @click="emit('edit', rule)">编辑</button>
      <button class="ghost danger" @click="emit('remove', rule)">删除</button>
      <button class="ghost" :class="{ on: logOn }" @click="emit('log', rule)">日志</button>
      <button v-if="conflicts > 0" class="ghost warn-btn" @click="emit('conflicts', rule)">冲突 {{ conflicts }}</button>
      <button v-if="pending" class="ghost warn-btn"
        @click="emit('confirmDeletes', { rule, fingerprint: pending.fingerprint, count: pending.count })">
        确认删除 {{ pending.count }}
      </button>
      <button v-if="canRetry" class="ghost" :disabled="busy" @click="retry">重试失败</button>
      <button :disabled="busy" @click="toggle">{{ rule.enabled ? '停止' : '启动' }}</button>
    </div>
  </div>
</template>

<style scoped>
.card { border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; margin-bottom: 8px; background: var(--bg-elev); }
.head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.name { font-weight: 600; color: var(--text); }
.meta { color: var(--text-dim); font-size: var(--fs-12); }
.state { margin-left: auto; color: var(--text-dim); font-size: var(--fs-12); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.dot.on { background: var(--success); }
.dot.warn { background: var(--warning); }
.dot.err { background: var(--danger); }
.dot.off { background: var(--text-faint); }
.badge { font-size: var(--fs-12); padding: 1px 6px; border-radius: 10px; border: 1px solid var(--border); color: var(--text-dim); }
.badge.ok { color: var(--text-dim); }
.badge.warn { color: var(--warning); border-color: var(--warning); }
.badge.err { color: var(--danger); border-color: var(--danger); }
.counts { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 6px; color: var(--text-dim); font-size: var(--fs-12); }
.counts .warn { color: var(--warning); }
.counts .align { color: var(--accent); }
.counts .current { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 40ch; }
.actions { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 8px; }
.ghost { background: transparent; }
.ghost.on { color: var(--accent); }
.ghost.danger { color: var(--danger); }
.ghost.warn-btn { color: var(--warning); }
</style>
~~~

- [ ] **Step 2: 新增编译冒烟测试（M4）**

创建 `frontend/src/views/sync-smoke.test.js`：

~~~js
import { describe, it, expect } from 'vitest'
import SyncCard from '../components/SyncCard.vue'
// 后续任务继续 import：Task 5 加 SyncConflictsDialog、Task 6 加 SyncView。

// 编译冒烟，**不是**渲染/行为测试：plugin-vue 在 import 时编译 SFC 模板，
// 因此模板/导入错误会在这里暴露。`npm run build` 只编译 index.html→src/main.js
// 可达的模块，尚未接到 App.vue 的新组件不会被它覆盖（M4）。
describe('sync 组件编译冒烟', () => {
  it('SyncCard 能编译并导出组件', () => {
    expect(SyncCard).toBeDefined()
  })
})
~~~

- [ ] **Step 3: 运行冒烟 + 构建**

Run（在 `frontend/` 下）: `npx vitest run src/views/sync-smoke.test.js`
Expected: 1 passed

Run（在 `frontend/` 下，次要检查）: `npm run build`
Expected: 构建成功

- [ ] **Step 4: 提交**

~~~bash
git add frontend/src/components/SyncCard.vue frontend/src/views/sync-smoke.test.js
git commit -m "feat(web): 新增同步规则卡片组件与编译冒烟"
~~~

---

### Task 5: SyncConflictsDialog.vue（冲突逐条裁决）

**Files:**
- Create: `frontend/src/components/SyncConflictsDialog.vue`
- Modify: `frontend/src/views/sync-smoke.test.js`（扩展，Task 4 已创建）

**Interfaces:**
- Consumes: Task 3 的 `conflictActionLabel`；标题文案由宿主用 `ruleLabel` 计算后经 `ruleName` 传入（S6）；冲突对象形状见 `wailsjs/go/models.ts` 的 `sync.Conflict`（`rel_path/remote_size/remote_mtime/local_size/local_mtime/detected_at`）
- Produces: props `{ visible: Boolean, conflicts: Array, ruleName: String }`；emits `close`、`resolve`({ rel, action })、`resolveAll`(action)

- [ ] **Step 1: 写组件**

创建 `frontend/src/components/SyncConflictsDialog.vue`：

~~~vue
<script setup>
import { computed } from 'vue'
import { conflictActionLabel } from '../utils/sync'

const props = defineProps({
  visible: Boolean,
  conflicts: { type: Array, default: () => [] },
  ruleName: { type: String, default: '' },
})
const emit = defineEmits(['close', 'resolve', 'resolveAll'])

const ACTIONS = ['keep_local', 'take_remote', 'save_as']

// S10：批量按钮会给**每个**冲突各发一次 IPC（N 条 = N 次 ResolveSyncConflict 往返），
// 大队列会把 UI 卡住且毫无提示。超过这个上限就禁用批量，要求分批或逐条处理。
// 50 的取法：一次批量 IPC 串行往返约几十毫秒量级，50 条仍在可接受范围，
// 再大就明显卡顿；宁可让用户分批，也不静默卡死。
const BATCH_LIMIT = 50
const batchDisabled = computed(() => !props.conflicts.length || props.conflicts.length > BATCH_LIMIT)

function fmtSize(n) {
  const v = Number(n)
  return Number.isFinite(v) ? String(v) : '-'
}
</script>

<template>
  <div v-if="visible" class="ui-overlay" @click.self="emit('close')">
    <div class="dialog" role="dialog">
      <div class="dtitle">冲突裁决 · {{ ruleName }}</div>
      <p class="dmsg">
        这些文件在远端与本地都被改过，系统不会自动覆盖。逐条选择如何处理；
        「保留本地」以本地现状为基线、「用远端覆盖」会重新下载、「另存远端版本」把远端那版写成兄弟文件而不动本地。
      </p>

      <div class="bulk">
        <span>批量（每条各发一次 IPC，当前 {{ conflicts.length }} 条 = {{ conflicts.length }} 次）：</span>
        <button v-for="a in ACTIONS" :key="'all-' + a" class="ghost"
          :disabled="batchDisabled" @click="emit('resolveAll', a)">
          全部{{ conflictActionLabel(a) }}
        </button>
        <span v-if="conflicts.length > BATCH_LIMIT" class="over">
          超过 {{ BATCH_LIMIT }} 条已禁用批量，请逐条裁决或分批处理
        </span>
      </div>

      <div class="rows">
        <div v-for="c in conflicts" :key="c.rel_path" class="row">
          <div class="rel" :title="c.rel_path">{{ c.rel_path }}</div>
          <div class="sides">
            <span>远端 {{ fmtSize(c.remote_size) }} · {{ c.remote_mtime || '-' }}</span>
            <span>本地 {{ fmtSize(c.local_size) }} · {{ c.local_mtime || '-' }}</span>
          </div>
          <div class="btns">
            <button v-for="a in ACTIONS" :key="c.rel_path + '-' + a" class="ghost"
              @click="emit('resolve', { rel: c.rel_path, action: a })">
              {{ conflictActionLabel(a) }}
            </button>
          </div>
        </div>
        <p v-if="!conflicts.length" class="empty">当前没有待裁决的冲突</p>
      </div>

      <div class="dbtns">
        <button @click="emit('close')">关闭</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 20px; width: 720px; max-width: 92vw; max-height: 80vh; overflow: auto; box-shadow: 0 8px 32px rgba(0,0,0,0.5); }
.dtitle { font-weight: 600; color: var(--text); margin-bottom: 8px; }
.dmsg { color: var(--text-dim); margin: 0 0 12px; font-size: var(--fs-13); line-height: 1.5; }
.bulk { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-bottom: 10px; color: var(--text-dim); font-size: var(--fs-12); }
.over { color: var(--warning); }
.rows { display: flex; flex-direction: column; gap: 8px; }
.row { border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; }
.rel { color: var(--text); word-break: break-all; margin-bottom: 4px; }
.sides { display: flex; gap: 14px; flex-wrap: wrap; color: var(--text-dim); font-size: var(--fs-12); margin-bottom: 6px; }
.btns { display: flex; gap: 6px; flex-wrap: wrap; }
.ghost { background: transparent; }
.empty { color: var(--text-faint); }
.dbtns { display: flex; justify-content: flex-end; margin-top: 12px; }
</style>
~~~

- [ ] **Step 2: 扩展编译冒烟测试（M4）**

把 `frontend/src/views/sync-smoke.test.js` 改成（新增第二个组件的 import 与用例）：

~~~js
import { describe, it, expect } from 'vitest'
import SyncCard from '../components/SyncCard.vue'
import SyncConflictsDialog from '../components/SyncConflictsDialog.vue'
// Task 6 还要加 SyncView。

describe('sync 组件编译冒烟', () => {
  it('SyncCard 能编译并导出组件', () => {
    expect(SyncCard).toBeDefined()
  })
  it('SyncConflictsDialog 能编译并导出组件', () => {
    expect(SyncConflictsDialog).toBeDefined()
  })
})
~~~

- [ ] **Step 3: 运行冒烟 + 构建**

Run（在 `frontend/` 下）: `npx vitest run src/views/sync-smoke.test.js`
Expected: 2 passed

Run（在 `frontend/` 下，次要检查）: `npm run build`
Expected: 构建成功

- [ ] **Step 4: 提交**

~~~bash
git add frontend/src/components/SyncConflictsDialog.vue frontend/src/views/sync-smoke.test.js
git commit -m "feat(web): 新增冲突裁决对话框组件并扩展编译冒烟"
~~~

---

### Task 6: SyncView.vue（列表 + 表单 + 实时刷新 + 日志面板）

**Files:**
- Create: `frontend/src/views/SyncView.vue`
- Modify: `frontend/src/stores/settings.js`、`frontend/src/stores/settings.test.js`（暴露并往返 `autoReconnectDefault`，S3）
- Modify: `frontend/src/views/sync-smoke.test.js`（扩展，Task 4 已创建）

**Interfaces:**
- Consumes: Task 3 全部（含 `ruleLabel`）；Task 4 的 `SyncCard`；Task 5 的 `SyncConflictsDialog`；既有 `LogPanel`、`AppDialog`、`useSettingsStore().autoReconnectDefault`（Task 6 新增）；绑定 `ListSyncRules/CreateSyncRule/UpdateSyncRule/DeleteSyncRule/SyncRuleStates/SyncRuleStats/SyncRuleConflicts/ResolveSyncConflict/ConfirmSyncRuleDeletes/ListHosts/PickLocalDir`
- Produces: 视图组件 `SyncView`（无 props，无 emits；`App.vue` 直接挂载）；`settings.js` 的 `autoReconnectDefault`

- [ ] **Step 1: 改 settings store，暴露全局 auto_reconnect 默认值（S3）**

`frontend/src/stores/settings.js` 目前不保留 `auto_reconnect_default`，而 `save()` 又不回传该
字段——`SetSettings` 是整体赋值，保存一次设置就会把用户的全局默认静默重置为 false。补上往返：

~~~js
// state 里新增：
    autoReconnectDefault: true,

// load() 里新增（GetSettings 返回的 AppSettings 含 auto_reconnect_default）：
      this.autoReconnectDefault = s.auto_reconnect_default !== false

// save() 里新增（否则 UI 保存设置会丢掉该字段，把它打回 false）：
        auto_reconnect_default: this.autoReconnectDefault,
~~~

并更新 `frontend/src/stores/settings.test.js` 的「出厂默认」用例，补一行
`expect(s.autoReconnectDefault).toBe(true)`。

- [ ] **Step 2: 写视图**

创建 `frontend/src/views/SyncView.vue`：

~~~vue
<script setup>
import { ref, onActivated, onDeactivated, onUnmounted } from 'vue'
import {
  ListSyncRules, CreateSyncRule, UpdateSyncRule, DeleteSyncRule,
  SyncRuleStates, SyncRuleStats, SyncRuleConflicts, ResolveSyncConflict,
  ConfirmSyncRuleDeletes, ListHosts, PickLocalDir,
} from '../../wailsjs/go/main/App'
import SyncCard from '../components/SyncCard.vue'
import SyncConflictsDialog from '../components/SyncConflictsDialog.vue'
import LogPanel from '../components/LogPanel.vue'
import AppDialog from '../components/AppDialog.vue'
import { useLogStore } from '../stores/logs'
import { useSettingsStore } from '../stores/settings'
import {
  newSyncRuleForm, syncRuleToForm, formToSyncRule, validateSyncRuleForm,
  statsOf, ruleLabel,
  canMirrorDelete, mirrorDeleteHint, shouldRefresh,
} from '../utils/sync'

// 只有同步与系统日志进这个视图：sftp 传输日志的 source_id 是 host 而不是规则 id，
// 加进来既污染隔离、又永远命中不了按规则过滤（spec §9.4）。
const SOURCE_TYPES = ['sync', 'system']

const rules = ref([])
const hosts = ref([])
const states = ref({})
const stats = ref({})
const loadError = ref('')
const opError = ref('')
const logStore = useLogStore()
const settingsStore = useSettingsStore()

const dialog = ref({ visible: false, title: '', message: '' })
let dialogResolve = null
function openConfirm(title, message) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, title, message }
    dialogResolve = resolve
  })
}
function onDialogOk() { dialog.value.visible = false; if (dialogResolve) { dialogResolve(true); dialogResolve = null } }
function onDialogCancel() { dialog.value.visible = false; if (dialogResolve) { dialogResolve(null); dialogResolve = null } }

const showForm = ref(false)
const editingId = ref(null)
const form = ref(newSyncRuleForm())

const conflictsVisible = ref(false)
const conflictsFor = ref({ id: '', name: '' })
const conflicts = ref([])

async function refresh() {
  try {
    rules.value = (await ListSyncRules()) || []
    states.value = (await SyncRuleStates()) || {}
    stats.value = (await SyncRuleStats()) || {}
    loadError.value = ''
  } catch (e) { loadError.value = String(e) }
}

async function loadHosts() {
  try { hosts.value = (await ListHosts()) || [] } catch (e) { loadError.value = String(e) }
}

function openCreate() {
  editingId.value = null
  form.value = newSyncRuleForm()
  // S3：auto_reconnect 的纯函数兜底是 true，真正的默认取全局设置
  // （后端 App.AutoReconnectDefault，经 settings store 暴露）。
  form.value.auto_reconnect = settingsStore.autoReconnectDefault
  opError.value = ''
  showForm.value = true
}

function openEdit(rule) {
  editingId.value = rule.id
  form.value = syncRuleToForm(rule)
  opError.value = ''
  showForm.value = true
}

async function submit() {
  const msg = validateSyncRuleForm(form.value)
  if (msg) { opError.value = msg; return }
  opError.value = ''
  try {
    const payload = formToSyncRule(form.value)
    if (editingId.value) await UpdateSyncRule(payload)
    else await CreateSyncRule(payload)
    showForm.value = false
    editingId.value = null
    form.value = newSyncRuleForm()
    await refresh()
  } catch (e) { opError.value = String(e) }
}

async function pickLocalDir() {
  try {
    const dir = await PickLocalDir()
    if (dir) form.value.local_path = dir
  } catch (e) { opError.value = String(e) }
}

async function remove(rule) {
  const ok = await openConfirm('确认删除', '删除同步规则「' + ruleLabel(rule) + '」？这会同时清理它的状态文件与本地临时残留。')
  if (!ok) return
  opError.value = ''
  try {
    await DeleteSyncRule(rule.id)
    await refresh()
  } catch (e) { opError.value = String(e) }
}

function toggleLog(rule) {
  logStore.filterSource = logStore.filterSource === rule.id ? '' : rule.id
}

async function openConflicts(rule) {
  opError.value = ''
  try {
    conflictsFor.value = { id: rule.id, name: ruleLabel(rule) } // S6：与卡片同一回退顺序
    conflicts.value = (await SyncRuleConflicts(rule.id)) || []
    conflictsVisible.value = true
  } catch (e) { opError.value = String(e) }
}

async function resolveOne({ rel, action }) {
  try {
    await ResolveSyncConflict(conflictsFor.value.id, rel, action)
    conflicts.value = (await SyncRuleConflicts(conflictsFor.value.id)) || []
    await refresh()
  } catch (e) { opError.value = String(e) }
}

async function resolveAll(action) {
  try {
    for (const c of conflicts.value.slice()) {
      await ResolveSyncConflict(conflictsFor.value.id, c.rel_path, action)
    }
    conflicts.value = (await SyncRuleConflicts(conflictsFor.value.id)) || []
    await refresh()
  } catch (e) { opError.value = String(e) }
}

async function confirmDeletes({ rule, fingerprint, count }) {
  const ok = await openConfirm('确认删除本地文件', '将按远端状态删除 ' + count + ' 个本地文件，此操作不可撤销。确认继续？')
  if (!ok) return
  opError.value = ''
  try {
    await ConfirmSyncRuleDeletes(rule.id, fingerprint)
    await refresh()
  } catch (e) { opError.value = String(e) }
}

// 状态转换经 'log' 事件流到达；App.vue 已持有全局订阅，这里只订 pinia store
// 后防抖刷新 —— 不二次 EventsOn、不轮询（spec §9.3）。
let unsubLogs = null
let refreshTimer = null
function subscribe() {
  if (unsubLogs) return
  unsubLogs = logStore.$subscribe((mutation, state) => {
    const logs = state.logs
    const last = logs[logs.length - 1]
    if (!last || !shouldRefresh(last.source_type, SOURCE_TYPES)) return
    clearTimeout(refreshTimer)
    refreshTimer = setTimeout(refresh, 300)
  })
  refreshTimer = setTimeout(refresh, 300)
}
function unsubscribe() {
  if (unsubLogs) { unsubLogs(); unsubLogs = null }
  clearTimeout(refreshTimer)
  refreshTimer = null
}

// App.vue 用 <KeepAlive>：切走不会触发 onUnmounted，所以必须用
// onActivated/onDeactivated 管理订阅与定时器（先例 SftpView.vue）。
onActivated(async () => {
  if (!hosts.value.length) await loadHosts()
  await refresh()
  subscribe()
})
onDeactivated(unsubscribe)
onUnmounted(unsubscribe)
</script>

<template>
  <div class="sync">
    <div class="panel ui-panel">
      <div class="toolbar">
        <button @click="openCreate">+ 新建规则</button>
        <button @click="refresh">刷新</button>
      </div>

      <form v-if="showForm" class="form" @submit.prevent="submit">
        <div class="row">
          <label>名称 <input v-model="form.name" placeholder="e.g. prod-conf" /></label>
          <label>主机
            <select v-model="form.host" required>
              <option value="" disabled>选择主机</option>
              <option v-if="form.host && !hosts.includes(form.host)" :value="form.host">{{ form.host }}</option>
              <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
            </select>
          </label>
          <label>SSH 用户 <input v-model="form.user" placeholder="留空用 ssh 配置默认" /></label>
          <label>类型
            <select v-model="form.kind">
              <option value="dir">目录</option>
              <option value="file">单文件</option>
            </select>
          </label>
        </div>

        <div class="row">
          <label class="grow">远端路径 <input v-model="form.remote_path" placeholder="/srv/conf 或 ~/conf" required /></label>
          <label class="grow">本地路径
            <input v-model="form.local_path" placeholder="本地目录" required />
          </label>
          <button type="button" @click="pickLocalDir">选择目录</button>
        </div>

        <div class="row">
          <label>递归深度 <input v-model.number="form.max_depth" type="number" /></label>
          <label>轮询间隔（秒） <input v-model.number="form.poll_interval_s" type="number" min="1" max="3600" /></label>
          <label class="check"><input v-model="form.force_poll" type="checkbox" /> 强制轮询</label>
          <label class="check"><input v-model="form.auto_reconnect" type="checkbox" /> 断线自动重连</label>
          <!-- S4：编辑模式下后端 UpdateSyncRule 强制 Enabled=false，勾选毫无意义；
               只在创建时渲染，编辑时改为说明运行状态由开始/停止按钮控制。 -->
          <label v-if="!editingId" class="check"><input v-model="form.enabled" type="checkbox" /> 启用</label>
          <span v-else class="hint">运行状态由卡片上的开始/停止按钮控制</span>
        </div>

        <div class="row">
          <label class="check">
            <input v-model="form.mirror_delete" type="checkbox" :disabled="!canMirrorDelete(form.kind)" />
            镜像删除（远端删除时同步删除本地）
          </label>
          <span v-if="mirrorDeleteHint(form.kind)" class="hint">{{ mirrorDeleteHint(form.kind) }}</span>
        </div>

        <label class="block">排除项（每行一条，支持 .git/ 或 *.swp）
          <textarea v-model="form.excludesText" rows="3" placeholder=".git/"></textarea>
          <span class="hint">已预填后端默认排除项（.git/、node_modules/、*.swp、*~、.DS_Store，镜像 internal/config/store.go:83-85）。清空文本框表示不排除任何路径，请谨慎。</span>
        </label>

        <div class="row">
          <button type="submit">{{ editingId ? '保存' : '创建' }}</button>
          <button type="button" @click="showForm = false">取消</button>
        </div>
      </form>

      <SyncCard v-for="r in rules" :key="r.id" :rule="r" :state="states[r.id]" :stats="statsOf(stats, r.id)"
        @edit="openEdit" @remove="remove" @log="toggleLog" @conflicts="openConflicts"
        @confirmDeletes="confirmDeletes" @changed="refresh" />

      <p v-if="loadError" class="empty err">加载失败: {{ loadError }}</p>
      <p v-if="opError" class="empty err">{{ opError }}</p>
      <p v-else-if="!rules.length" class="empty">暂无同步规则，点「+ 新建规则」</p>
    </div>

    <div class="panel ui-panel">
      <!-- S9：把规则传给 LogPanel，它的逐步 chips 才能显示规则名而不是 32 位 id；
           LogPanel 内部用 name || host 作 chip 文案。 -->
      <LogPanel :tunnels="rules" :source-types="SOURCE_TYPES" />
    </div>

    <SyncConflictsDialog :visible="conflictsVisible" :conflicts="conflicts"
      :rule-name="conflictsFor.name" @close="conflictsVisible = false"
      @resolve="resolveOne" @resolveAll="resolveAll" />

    <AppDialog :visible="dialog.visible" mode="confirm" :title="dialog.title" :message="dialog.message"
      @ok="onDialogOk" @cancel="onDialogCancel" />
  </div>
</template>

<style scoped>
.sync { display: flex; height: 100%; gap: 12px; }
.panel { flex: 1; padding: 12px; overflow: auto; }
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
.form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-elev); }
.row { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; }
.row label { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-12); color: var(--text-dim); }
.row label.check { flex-direction: row; align-items: center; gap: 6px; }
.row label.grow { flex: 1; min-width: 220px; }
.block { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-12); color: var(--text-dim); }
.hint { color: var(--text-faint); font-size: var(--fs-12); }
.empty { color: var(--text-faint); }
.empty.err { color: var(--danger); }
</style>
~~~

- [ ] **Step 3: 扩展编译冒烟测试（M4）**

把 `frontend/src/views/sync-smoke.test.js` 改成（新增 `SyncView`）：

~~~js
import { describe, it, expect } from 'vitest'
import SyncCard from '../components/SyncCard.vue'
import SyncConflictsDialog from '../components/SyncConflictsDialog.vue'
import SyncView from './SyncView.vue'

describe('sync 组件编译冒烟', () => {
  it('SyncCard 能编译并导出组件', () => {
    expect(SyncCard).toBeDefined()
  })
  it('SyncConflictsDialog 能编译并导出组件', () => {
    expect(SyncConflictsDialog).toBeDefined()
  })
  it('SyncView 能编译并导出组件', () => {
    expect(SyncView).toBeDefined()
  })
})
~~~

- [ ] **Step 4: 运行冒烟 + 构建**

Run（在 `frontend/` 下）: `npx vitest run src/views/sync-smoke.test.js`
Expected: 3 passed

Run（在 `frontend/` 下，次要检查）: `npm run build`
Expected: 构建成功

- [ ] **Step 5: 提交**

~~~bash
git add frontend/src/views/SyncView.vue frontend/src/views/sync-smoke.test.js frontend/src/stores/settings.js frontend/src/stores/settings.test.js
git commit -m "feat(web): 新增同步视图,列表/表单/冲突与删除确认接线,暴露全局重连默认值"
~~~

---

### Task 7: 导航入口 + README

**Files:**
- Modify: `frontend/src/App.vue`
- Modify: `README.md`、`README.zh-CN.md`

**Interfaces:**
- Consumes: Task 6 的 `SyncView.vue`
- Produces: 左导航第三个标签「同步」，`active === 'sync'` 时渲染 `SyncView`

- [ ] **Step 1: 改 App.vue**

在 `frontend/src/App.vue` 的 `<script setup>` 里新增导入（放在 `SftpView` 导入之后）：

~~~js
import SyncView from './views/SyncView.vue'
~~~

在 `<nav class="sidebar">` 里 `SFTP` 按钮之后、设置按钮之前插入第三个标签：

~~~html
      <button :class="{ active: active === 'sync' }" @click="active = 'sync'">同步</button>
~~~

把 `<KeepAlive>` 里的两个分支扩成三个（保持现有 v-if/v-else 链风格）：

~~~html
        <ForwardView v-if="active === 'forward'" key="forward" />
        <SftpView v-else-if="active === 'sftp'" key="sftp" />
        <SyncView v-else key="sync" />
~~~

- [ ] **Step 2: 改 README**

在 `README.md` 与 `README.zh-CN.md` 的功能特性清单里，各补一条与现有条目风格一致的中/英描述，说明新增「远程文件夹同步」：监控远端目录或单个文件，变化同步到本地；默认不删除本地文件（镜像删除需显式开启）；本地被改动时不覆盖，改为进入冲突队列由用户裁决（保留本地/用远端覆盖/另存远端版本）。

**S5：同时更新架构小节里列出的左导航模块**：`README.md:74` 与 `README.zh-CN.md:73` 目前写的是 `Forward / SFTP`（中文「转发 / SFTP」），必须改成含「同步」的三项（`Forward / SFTP / Sync`、「转发 / SFTP / 同步」）。只加功能 bullet 不改这一行，架构描述就与界面不符。

- [ ] **Step 3: 冒烟 + 构建**

Run（在 `frontend/` 下）: `npx vitest run src/views/sync-smoke.test.js`
Expected: 3 passed（三个新组件的编译冒烟）

Run（在 `frontend/` 下）: `npm run build`
Expected: 构建成功

Run（仓库根）: `make linux`
Expected: 构建出 Linux 二进制。启动后**左导航应有第三项「同步」**；点进去能看到规则列表（空则显示「暂无同步规则」）、右半屏是日志面板、点「+ 新建规则」能展开表单且主机下拉来自 `ListHosts`。

- [ ] **Step 4: 提交**

~~~bash
git add frontend/src/App.vue README.md README.zh-CN.md
git commit -m "feat(web): 左导航新增同步入口并补充 README 特性说明"
~~~

---

## 自审记录（写完后按 spec 复查）

**1. spec 覆盖**

- §9.1 绑定：后端已实现 11 个；`RetrySyncRuleFailures` 由 Task 1 补齐（其余 11 个已存在，无需任务）。同任务按评审裁定修正失败计数（M2）与冲突计数（M3）；`ConfirmSyncRuleDeletes` 的指纹闭环由 Task 2 修正（M5）。
- §9.2 界面：左导航第三项（Task 7）、`SyncCard.vue` 且不复用 `RuleCard.vue`（Task 4）、卡片十要素——状态圆点/probe 徽章/SourceMissing 警示/四组计数/首轮对齐进度/当前文件/日志/冲突/确认删除/启停（Task 4 全含）、重试（Task 1+4）、新建编辑对话框字段清单（Task 6 表单：host 用 `ListHosts`、remote/local、`PickLocalDir`、kind、max_depth、excludes、mirror_delete、force_poll、poll_interval_s、auto_reconnect、enabled 全部含；excludes 预置后端默认、启用仅创建时渲染、auto_reconnect 取全局默认）、`SyncConflictsDialog.vue` 逐条两侧大小时间 + 三动作 + 批量（Task 5，批量逐条 IPC 且超 50 条禁用）、进度区自带不复用 transfers（Task 4 的 counts/align/current 即轻量进度，未引入 transfers）。
- §9.3 实时性：不新增 EventsOn、订 pinia 防抖、KeepAlive 用 onActivated/onDeactivated（Task 6）。
- §9.4 日志隔离：`SOURCE_TYPES = ['sync','system']`（Task 6）；卡片错误日志用规则 id（S7），日志 chips 用 `ruleLabel` 显示名（S9，`ruleLabel` 定义在 Task 3）。
- §3 决策 14：无 transfers 复用（Task 4/6）。决策 15：`canMirrorDelete` 置灰 + 提示，且 `formToSyncRule` 强制 false（Task 3/6）。
- §12 影响面：三个前端新文件 + `App.vue` + README（Task 4/5/6/7）；前端 store 的 `autoReconnectDefault` 往返（Task 6）；后端绑定与统计/指纹修正（Task 1/2）。
- **已知未覆盖**：§9.4 提到的 `sftp` 日志量风险（§11 W7 的首要缓解：抑制 sftp 的 done 行）**不在本计划**——它改的是 `internal/sftp` 的日志行为，属另一个主题，已记在 `2026-09-10-remote-folder-sync-followups.md` 的「已知取舍与延后项」。

**2. 占位符扫描**：本计划每个代码步骤都给了完整代码；无 TBD / 「类似 Task N」/ 「适当处理错误」之类字样。组件任务的验证方式是 **`frontend/src/views/sync-smoke.test.js` 编译冒烟**（M4）：`plugin-vue` 在 import 时编译 SFC 模板，因此能覆盖 `npm run build` 因组件尚未接入 `App.vue` 而漏掉的新组件（Task 4/5/6）；这是**编译检查，不是渲染/行为测试**——仓库没有组件测试基建，行为逻辑刻意下沉到 Task 3 的纯函数以便真正被测到。

**3. 类型/命名一致性**：Task 3 产出的 **23 个名字**（`SYNC_STATES`、`DEFAULT_EXCLUDES`、`stateLabel`、`dotClass`、`kindLabel`、`canMirrorDelete`、`mirrorDeleteHint`、`probeBadge`、`countsOf`、`alignText`、`currentFileName`、`statsOf`、`deleteSummary`、`retryable`、`conflictActionLabel`、`parseExcludes`、`formatExcludes`、`newSyncRuleForm`、`syncRuleToForm`、`formToSyncRule`、`validateSyncRuleForm`、`shouldRefresh`、`ruleLabel`）在 Task 4/5/6 中被逐字使用；Task 1 产出的 `RetrySyncRuleFailures` 在 Task 4 中被导入使用。统计字段一律 PascalCase（`stats.Mode/Pending/AlignTotal/DeleteFingerprint/Conflicts`），规则字段一律 snake_case（`rule.remote_path/rule.mirror_delete`），全篇一致。
