# SFTP 双侧刷新 / 右键菜单分级 / 状态持久化 / 连接失效自愈 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 SFTP 面板的刷新覆盖双侧、右键菜单按"有选中/无选中"分级、把每主机最后路径与双侧排序持久化，并在主机休眠/断链后自动探活自愈而不是"假装已连接"。

**Architecture:** 后端新增一个独立配置段 `[sftp_ui]` + 三个绑定（`GetSftpUI`/`SaveSftpUI`/`SftpProbe`），last path 只由"导航成功"触发的前端绑定写入，后端内部 recent 记录一律不写；探活复用既有 `Pool.Probe` 并修掉它的预算缺陷。前端把排序状态从 `FilePane` 上收到 `SftpView` + 新 store，纯逻辑（`nextSort`/`resolveRemoteTarget`）抽进 `utils/`，组件只做渲染与接线。

**Tech Stack:** Go 1.x + Wails v2.15.0（绑定 `wails generate module -tags webkit2_41`）、`internal/preset` 无关；Vue 3.5 + Pinia + Vitest 4（**无 jsdom / 无 @vue/test-utils**）、`@vue/server-renderer` 可做真实 SSR 渲染断言；配置为 BurntSushi/toml v1.6.0。

**Spec:** `docs/superpowers/specs/2026-10-10-sftp-refresh-persist-connloss-design.md`（v1.5；本计划逐节实现它，执行者必须两份一起读）

## Global Constraints

- **Task 0（干净克隆前置，审核 B1 Blocking）**：`main.go:16` 是 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 `.gitignore:3` 忽略 ⇒ **任何** Go 命令（含 T2 的 `go test ./internal/sftp/ ./`、T3/T4 的 `go test ./`）在干净克隆上都会硬失败（隔离实验实测 `go vet ./` → `pattern all:frontend/dist: no matching files found`，EXIT=1）。开工第一步：`cd frontend && npm ci && npm run build`（`package-lock.json` 已入库、`node_modules` 未入库）。这不是 T5 的事，是所有 Go 命令的前提。
- **沙箱构建缓存只读**：所有 Go 命令必须带 `GOCACHE=$PWD/.gocache`（实测 `~/.cache/go-build` 报 `read-only file system`；仓库根已有 gitignore 的 `.gocache/`）。
- **最终门禁**：`GOCACHE=$PWD/.gocache make ci`（= `cd frontend && npm run build` → `go vet ./...` → `go test ./... -race -count=1` → `cd frontend && npx vitest run`）。任何任务收尾前该命令必须全绿，或如实报告哪一步失败。
- **新增 Go 绑定后必须重生成**：仓库根执行 `GOCACHE=$PWD/.gocache wails generate module -tags webkit2_41`，`frontend/wailsjs/go/main/App.{js,d.ts}` 与 `frontend/wailsjs/go/models.ts` **一并提交**。生成失败必须停下报告，**不得手改生成文件**。
- **只本地提交，不 push**（用户未授权推送）；每条提交用中文信息。
- **文案逐字**：日志文案以规格 §10 的表格为准（含"远端连接已失效（主机休眠或网络中断），正在重连…"）。
- **不许动的边界**：`internal/sftp` 的 `Backend` 接口（不新增方法）、`Connected` 的粘性语义与 `SftpConnected` 绑定行为、`internal/forward` 与 `internal/sync` 的任何逻辑。
- **取值口径**：排序键白名单 `name|size|modTime`；排序方向只允许 `1` / `-1`；`[sftp_ui]` 六个 JSON 键逐字为 `last_local_dir`、`last_remote_dirs`、`sort_local_key`、`sort_local_dir`、`sort_remote_key`、`sort_remote_dir`。
- **前端测试手段**（没有 jsdom / @vue/test-utils）：纯函数单测、Pinia store 单测（`vi.mock('../../wailsjs/go/main/App')`）、**SSR 真渲染**（`createSSRApp` + `renderToString`，先例 `frontend/src/components/TransferQueue.render.test.js`）、读源码钉形状（先例 `frontend/src/components/filePaneWiring.test.js`）。需要真实鼠标事件的断言写不出来，不要假装能写。
- **证据要求**：每条"已完成/已通过"必须附实际命令与原始输出；没实测的写"未验证"。

## Review Focus

以下 5 类输入/失效模式最可能咬到真实用户，规格提到但容易被实现者漏掉；每一类都在任务里配了测试：

1. **主机名本身是配置 map 的键**（含点 `192.0.2.10`、空格、引号、中文）——若 TOML 转义或 JSON 键名映射错了，会导致配置写不进去或读到 `nil`，而界面上完全看不出来。测试落在 T1（TOML 往返）与 T7（payload 六个 snake_case 键逐字断言）。
2. **记忆的目录已经不存在**（远程目录被删、本地盘符卸载）——必须回落 `SftpHome`/`Cwd()` 并记一条 warn，而不是停在空列表或反复报错。测试落在 T11。
3. **"断开"之后队列「重试」把远程列表又填出来**——此时 `connected === false` 会让人误以为链路不可用（菜单被置灰、工具栏刷新跳过有内容的远程面板）。测试落在 T13（操作成功回同步 + 置灰接线断言）。
4. **探针进行中反复切标签 / 用户在探活期间切主机**——不明文处理就会叠加探测、把"用户切了主机"误记成"重连失败"。测试落在 T13（`probing`/`recovering` 去重、host 变更静默、`finally` 复位）。
5. **手改配置文件**：把 `sort_local_key` 写成 `bogus`、`sort_local_dir` 写成 `7`，或整段 `[sftp_ui]` 删掉——必须归一到 `name/1` 并回落默认，不得让非法值飘到 UI。测试落在 T1（`Normalize` + `LoadConfig` 挂点）。

---

## Task 1: 配置段 `[sftp_ui]` 与归一

**Files:**
- Modify: `internal/config/store.go`（`AppConfig` 字段块 `:198-208`、`normalize()` `:211`）
- Test: `internal/config/store_test.go`

**Interfaces:**
- Produces（后续任务与前端都依赖这六个字段名）：
  ```go
  type SftpUIState struct {
      LastLocalDir   string            `toml:"last_local_dir" json:"last_local_dir"`
      LastRemoteDirs map[string]string `toml:"last_remote_dirs" json:"last_remote_dirs"`
      SortLocalKey   string            `toml:"sort_local_key" json:"sort_local_key"`
      SortLocalDir   int               `toml:"sort_local_dir" json:"sort_local_dir"`
      SortRemoteKey  string            `toml:"sort_remote_key" json:"sort_remote_key"`
      SortRemoteDir  int               `toml:"sort_remote_dir" json:"sort_remote_dir"`
  }
  func (s *SftpUIState) Normalize()
  // AppConfig 新增字段：
  SftpUI SftpUIState `toml:"sftp_ui" json:"sftp_ui"`
  ```

- [ ] **Step 1: 写失败测试**

在 `internal/config/store_test.go` 追加四个用例（名与判据照抄）：

```go
func TestSftpUIStateNormalize(t *testing.T)      // 表驱动：
//  - {Key:"", Dir:0}      → Key=="name", Dir==1
//  - {Key:"bogus", Dir:7} → Key=="name", Dir==1
//  - {Key:"modTime", Dir:-7} → Key=="modTime", Dir==-1
//  - {Key:"size", Dir:2}  → Dir==1
//  - LastRemoteDirs == nil → Normalize 后非 nil 且 len==0
func TestSftpUITOMLRoundTrip(t *testing.T)       // 见下方键集合
func TestLoadConfigNormalizesSftpUISection(t *testing.T)
func TestLoadConfigWithoutSftpUISection(t *testing.T)
```

`TestSftpUITOMLRoundTrip` 的键集合必须含：`"192.0.2.10"`、`"prod db"`、`"中文主机"`、`quote"host`、`host.with.dots`、`prod-db`；`LastLocalDir` 用 Windows 风格路径，**Go 源码里必须写成反引号 raw string**（`` `D:\工作\虚拟 共享` ``）或双反斜杠转义 —— 双引号串里的 `\工` 是非法转义、编译不过（审核 M7）。判据：`Encode` 后 `Decode` 回来**逐字段相等**（只比解码后的值，不断言落盘字节形态），且 `[app]` 段等其它字段不受影响。

`TestLoadConfigNormalizesSftpUISection`：写一份临时 TOML，其中 `sort_local_key = "bogus"`、`sort_local_dir = 7`、`last_remote_dirs` 只给一个键 → `LoadConfig` 读回后 `SortLocalKey=="name"`、`SortLocalDir==1`。

`TestLoadConfigWithoutSftpUISection`：TOML 里完全没有 `[sftp_ui]` → 读回后 `SortLocalKey=="name"`、`SortLocalDir==1`、`LastRemoteDirs` 非 nil 且空、`LegacyMigrated==false`。

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./internal/config/ -run 'SftpUI|WithoutSftpUI' -v`
Expected: FAIL，编译错误 `undefined: SftpUIState`。

- [ ] **Step 3: 实现**

- 结构体与标签逐字照 Interfaces 块；`Normalize()` 规则：两个 key 走白名单 `name|size|modTime`（非法/空→`name`），两个 dir `<0`→`-1` 否则 `1`，`LastRemoteDirs==nil`→`map[string]string{}`。
- `AppConfig` 里把 `SftpUI` 放在 `RemoteRecent` 之后、`LegacyMigrated` 之前。
- **归一挂点**：`func (c *AppConfig) normalize()` 内新增 `c.SftpUI.Normalize()` —— 注意该函数现在**还会**归一 `App` 与每条 `Syncs`（审核 M1：别写成「只调 `c.App.Normalize()`」）。`LoadConfig` 与 `DefaultAppConfig` 都经过它，这是唯一挂点。
- **实现注释（审核 M1，规格 §6.1 强制）**：在 `SftpUIState` 字段上写明前提 —— `last_remote_dirs` 的键是 **SSH 主机名（不含 user）**，视图内 user 恒 `''`；`internal/sync` 用 `rule.User`，扩展到 sync/watch 前必须先改键（否则 R14 无人承接）。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./internal/config/ -v`
Expected: PASS（含既有用例）。

- [ ] **Step 5: 格式与提交**

Run: `gofmt -l internal/config` → 输出为空。
```bash
git add internal/config/store.go internal/config/store_test.go
git commit -m "feat(config): 新增 [sftp_ui] 配置段（最后路径 + 双侧排序）与归一挂点"
```

---

## Task 2: 探活能力（`Pool.Probe` 预算修复 + `Ctrl.Probe` + `SftpProbe` 绑定）

**Files:**
- Modify: `internal/sftp/session.go`（`Pool.Probe` `:288-307`）、`internal/sftp/ctrl.go`（新增方法，参考 `:118-130` 的类型断言先例）、`app.go`（绑定区，参考 `SftpConnected` `:983-988`）
- Test: `internal/sftp/session_test.go`、`internal/sftp/ctrl_test.go`、`app_test.go`

**Interfaces:**
- Consumes: 无（本任务自成一体）。
- Produces:
  ```go
  func (p *Pool) Probe(ctx context.Context, host, user string) bool   // 行为变更：拨号吃满预算时用新预算复核
  func (g *GoBackend) Probe(ctx context.Context, host, user string) bool
  func (c *Ctrl) Probe(ctx context.Context, host, user string) bool   // GoBackend→pool.Probe；其它后端→goroutine+ctx select+每 host 单飞
  func (a *App) SftpProbe(host string) bool                          // a.sftp == nil → false
  ```

- [ ] **Step 1: 写失败测试**

`internal/sftp/session_test.go`：

```go
func TestPoolProbeDidNotFalseNegativeOnSlowDial(t *testing.T)
// 用 NewPool(fakeDial) 注入一个 dial：sleep(probeTimeout+300ms) 后返回一个带 Conn/Proc 的会话；
// 该会话的 ReadDirContext 实现返回 nil（健康）。
// 断言：Probe(context.Background(), "h", "u") == true
//   且该会话没有被 close（用一个 closed 标志断言）
func TestPoolProbeStillFailsForBrokenSession(t *testing.T)
// dial 立即返回会话，但 ReadDirContext 返回 error ⇒ Probe == false
```

`internal/sftp/ctrl_test.go`（沿用 `cancel_test.go:491` 的 pool 注入先例与 `NewCtrlForcedBackend`；**不要复用 `ctrl_test.go:658-712` 的 `fakeBackend`** —— 它的 `List` 既不阻塞、`called` 字段也无锁，造不出「阻塞」且会与仍在跑的 goroutine 构成 `-race` 报告，见审核 I4）：

```go
// 新写一个专用替身（审核 I4）：
type blockingFakeBackend struct {
    sftp.Backend                       // 嵌入接口占位，只实现被调用到的方法
    listCalls atomic.Int32             // 计数必须原子
    entered   chan struct{}            // 进入 List 的信号
    release   chan struct{}            // 测试用它放行
}
func (b *blockingFakeBackend) List(string, string, string) ([]Item, error) {
    b.listCalls.Add(1)
    close(b.entered)
    <-b.release                        // 阻塞到测试放行
    return nil, errors.New("dead")
}

func TestCtrlProbeUsesPoolForGoBackend(t *testing.T)   // *GoBackend + 慢 dial ⇒ true（见 session_test 那条）
func TestCtrlProbeFallbackIsBoundedAndWaitsForInflight(t *testing.T)
// NewCtrlForcedBackend(&blockingFakeBackend{...})：
//   - 100ms ctx 超时后 Probe 返回 false（不得挂住）
//   - 同 host 并发第二次 Probe **等到同一个结果**（不是立即 false），且 listCalls == 1
func TestSftpProbeFalseWhenListFails(t *testing.T)     // 审核 I6：补规格 §11.1「未连接 host → false」那条
```

**注**：若要让 `*GoBackend` 的 `pool.Probe` 真的走通 `ReadDirContext`，`Session.Conn` 是具体类型 `*sftp.Client`（`session.go:34`），只能照 `internal/sftp/copy_download_test.go:35-75` 的 `net.Pipe + sftp.NewServer(root) + sftp.NewClientPipe` 先例造会话；慢拨号那条用例只需 `Conn != nil` 的桩 + 成功返回即可。

`app_test.go`：

```go
func TestSftpProbeFalseBeforeInit(t *testing.T)   // NewApp()（a.sftp == nil）→ SftpProbe("h") == false，不 panic
```

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./internal/sftp/ ./ -run 'Probe' -v`
Expected: FAIL（`undefined: (*Ctrl).Probe` / 慢拨号用例在旧实现下返回 false）。

- [ ] **Step 3: 实现**

- `Pool.Probe`：`AcquireList` 返回后，若 `cctx.Err() != nil` 且会话确实建出（`s.Conn != nil`）⇒ 用**新预算** `context.WithTimeout(ctx, probeTimeout)` 再跑一次 `ReadDirContext(".")`；只有新预算也失败才判 false。**保留**失败时 `Release(s, false)`（丢死会话）。
- `GoBackend.Probe(ctx, host, user)` = `g.pool.Probe(ctx, host, user)`。
- `Ctrl.Probe`：`if g, ok := c.backend().(*GoBackend); ok { return g.Probe(...) }`，否则走 spec §8.2 的 goroutine + `ctx` select + **每 host 共享在飞结果（审核 I5，v1.7 修正）**：per-host 存一个 `chan bool`，第二个调用者**等同一个结果**，而不是立刻返回 `false`。理由：`false` 在前端被解释成 `'lost'` ⇒ 触发断开+重连；batch 子进程一挂住，「在飞」就会被误判成「链路已死」，对可能健康的链路做无谓 teardown（违反 D14）。共享项必须在 goroutine 结束时清除（`defer` 内 `mu.Lock(); delete(inflight, host); mu.Unlock()`），否则该 host 此后被永久标记「在飞」、自愈静默失效。**不要写 `map[string]bool` + 立即 `return false`**（那正是 I5 的缺陷形态）。
- 实现注释两处必须写：① `Probe` 会经 `markSessionConnected` 置**粘性** `connected[host]=true`（因此调用方降级时必须再调 `SftpDisconnect`）；② batch 分支的残留（挂死子进程靠 ssh 自身超时回收）。
- `app.go` 的 `SftpProbe(host string) bool`：`if a.sftp == nil { return false }`，否则 `a.sftp.Probe(context.Background(), host, "")`（user 恒 `""`，与视图其它调用一致）。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./internal/sftp/ ./ -run 'Probe' -race -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/session.go internal/sftp/ctrl.go internal/sftp/session_test.go internal/sftp/ctrl_test.go app.go app_test.go
git commit -m "feat(sftp): 真探活能力（修 Pool.Probe 拨号吃满预算的误判 + Ctrl.Probe/SftpProbe 绑定）"
```

---

## Task 3: 配置读写绑定 `GetSftpUI` / `SaveSftpUI`

**Files:**
- Modify: `app.go`（绑定区，照 `GetSettings`/`SetSettings` `:477-500` 与 `setAppSettings` `:404-411` 的写法）
- Test: `app_test.go`

**Interfaces:**
- Consumes: T1 的 `config.SftpUIState`。
- Produces:
  ```go
  func (a *App) GetSftpUI() config.SftpUIState   // 读锁内只值拷贝，解锁后 Normalize
  func (a *App) SaveSftpUI(s config.SftpUIState) error
  ```

- [ ] **Step 1: 写失败测试**

```go
func TestSaveSftpUIPersistsAndKeepsOtherSections(t *testing.T)
// newTestApp（app_test.go:884-889 的骨架，cfgPath 在 t.TempDir()）：
// 先造 Tunnels/Syncs/Bookmarks/RemoteRecent 各一条 → SaveSftpUI(全字段) → config.LoadConfig(cfgPath)
// 断言六个字段逐字相等，且 Tunnels/Syncs/Bookmarks/RemoteRecent 逐项未受影响
//   ⚠️ SyncRule 夹具必须写全字段（Kind/PollIntervalS/Excludes/AutoReconnect 等）：
//      LoadConfig 会归一 Syncs（Kind 空→dir、PollIntervalS 0→5、Excludes nil→默认集、
//      AutoReconnect nil→全局默认），只设部分字段会让这条用例因「与被测行为无关」的归一而必红（审核 M2）；
//      或改为只比对显式设置的字段。
func TestSaveSftpUINormalizesInvalidSort(t *testing.T)
// 传 {SortLocalKey:"bogus", SortLocalDir:7, LastRemoteDirs:nil}
// 断言落盘后 SortLocalKey=="name"、SortLocalDir==1（LastRemoteDirs 允许 nil 或空 map，二选一写明）
// nil-cfg 覆盖**并入既有** app_test.go:1282-1315 的 TestSftpBindingsFailFastBeforeInit（规格 §11.1 指定的归属，审核 M5）：
//   a := NewApp() 后**先** GetSftpUI()：断言不 panic 且返回归一化零值（SortLocalKey=="name"、SortLocalDir==1、
//   LastRemoteDirs 非 nil）—— **顺序不能反**（审核 I6）：先调 SaveSftpUI 会把 a.cfg 造出来，nil-cfg 分支就永不执行；
//   **再**断言 SaveSftpUI(全字段) 不 panic、或返回明确错误。
func TestGetSftpUIDoesNotMutateSharedState(t *testing.T)
// newTestApp 后把 a.cfg.SftpUI.LastRemoteDirs 置 nil → GetSftpUI() 返回非 nil map，
// 但 a.cfg.SftpUI.LastRemoteDirs 仍为 nil（证明 Normalize 发生在解锁之后、没有原地写共享结构）
func TestSaveSftpUIWholeStructOverwriteIsPinned(t *testing.T)
// 先 SaveSftpUI(全字段，路径=="/keep") → 再 SaveSftpUI({只给 sort 四字段})
// 断言落盘后 LastLocalDir=="" 且 LastRemoteDirs 为空 —— **把"整结构覆盖会清零路径"这一危险语义钉成测试**，
// 与前端"saveSnapshot 必须先读回再合并"的红线（T7）互为对照；此用例不是缺陷而是契约固化
func TestGetSftpUIAndSaveSftpUIRaceFree(t *testing.T)
// 并发：GetSftpUI() 100 次 ‖ SaveSftpUI(全字段) 100 次（cfgPath 在 t.TempDir()）
// 断言：-race 下无 DATA RACE 报告（专门抓"读锁内就地 Normalize"这类实现）
```

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./ -run 'SftpUI' -race -v`
Expected: FAIL（`undefined: (*App).GetSftpUI`）。

- [ ] **Step 3: 实现**

- `GetSftpUI`：`a.cfgMu.RLock(); if a.cfg == nil { RUnlock; s := config.SftpUIState{}; s.Normalize(); return s }; s := a.cfg.SftpUI; RUnlock; s.Normalize(); return s` —— **锁内只做值拷贝，`Normalize()` 必须在解锁之后**（它会给 nil map 赋新头，锁内调用即原地写共享结构）。
- `SaveSftpUI`：先 `s.Normalize()`；然后写锁内 `if a.cfg == nil { a.cfg = &config.AppConfig{} }; a.cfg.SftpUI = s`；解锁后 `return a.saveConfig()`。锁内不做 IO。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./ -run 'SftpUI' -race -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add app.go app_test.go
git commit -m "feat(app): GetSftpUI/SaveSftpUI 绑定（锁内只赋值、解锁后归一与落盘）"
```

---

## Task 4: 四个配置写入点的并发改造 + last path 写入规则

**Files:**
- Modify: `app.go`（`AddLocalRecent` `:1343-1365`、`AddRemoteRecent` `:1367-1392`、`AddBookmark` `:1312-1326`、`RemoveBookmark` `:1328-1341`、`recordRecentSFTP` `:1394-1410`、`ListLocations` `:1291-1310`、`cfgMu` 注释 `:37-42`）—— 行号仅供定位，**以符号名为准**（审核 M6）
- Test: `app_test.go`

**Interfaces:**
- Consumes: T1（`config.SftpUIState`）、T3（`GetSftpUI`）。
- Produces:
  ```go
  func (a *App) recordRecentSFTP(host, remoteDir, localDir string, rememberLast bool)  // 签名扩展
  // 内部辅助（私有；锁内只构造新值 + 赋值，不落盘；**调用方必须已持写锁**）：
  func (a *App) rememberRemotePathLocked(host, path string) (changed bool)
  func (a *App) rememberLocalPathLocked(path string) (changed bool)
  // recents 的"只更列表"入口（审核 I3）：recordRecentSFTP 现状是**委托** AddRemoteRecent/AddLocalRecent，
  // 若照字面"加个 bool 参数、保持委托"，它会连 last path 一起写（新用例必红）；
  // 若把 recordRecentSFTP 包进写锁再调公开绑定，则**同锁重入死锁**（RWMutex 不可重入，测试 hang 到 timeout）。
  // 正确切法（二选一，推荐前者）：
  //   (a) func (a *App) addRemoteRecentLocked(host, path string, rememberLast bool) (changed bool)
  //       func (a *App) addLocalRecentLocked(path string, rememberLast bool) (changed bool)
  //       —— 公开绑定包 true；recordRecentSFTP 在写锁内调 false；两者都不得在持锁时调用公开绑定。
  //   (b) 把 AddRemoteRecent/AddLocalRecent 降为上面的内部形态，公开绑定只做 Normalize/落盘包装。
  ```

- [ ] **Step 1: 写失败测试**

```go
func TestAddRemoteRecentRecordsLastPathEvenWhenRecentHeadMatches(t *testing.T)
// 手工把 cfg.RemoteRecent 队首设成 {Host:"prod", Path:"/r", TS:"2000-01-01T00:00:00Z"}，
// 且 cfg.SftpUI.LastRemoteDirs 留空 → AddRemoteRecent("prod", "/r")
// 断言：SftpUI.LastRemoteDirs["prod"] == "/r"；且 RemoteRecent 的 TS 未刷新、无重复项
func TestAddLocalRecentRecordsLastPathEvenWhenHeadMatches(t *testing.T)   // 同构，LastLocalDir
func TestRecordRecentSFTPDoesNotTouchLastPath(t *testing.T)
// 先经 AddRemoteRecent 把 LastRemoteDirs["prod"] 设为 "/a"、LastLocalDir 设为 "/l"
// 再调 a.recordRecentSFTP("prod", "/b", "/m", false)
// 断言：LastRemoteDirs["prod"] 仍为 "/a"、LastLocalDir 仍为 "/l"，
//       而 RemoteRecent 队首为 {prod,/b}、LocalRecent 队首为 /m
func TestConfigWritesAreRaceFreeAgainstSaveConfig(t *testing.T)
// 并发跑：AddRemoteRecent / AddLocalRecent / AddBookmark / RemoveBookmark 各自 50 次
//         与 a.saveConfig() 50 次（用 sync.WaitGroup；cfgPath 指向 t.TempDir()）
// 断言：-race 下无报告；结束后 RemoteRecent/LocalRecent 无重复项
```

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./ -run 'LastPath|RecordRecentSFTP|ConfigWritesAreRaceFree' -race -v`
Expected: FAIL —— 注意此时**整个 `package main` 编译失败**（`recordRecentSFTP` 参数个数不符），所以「race 用例报 DATA RACE」这类观察要等实现之后才有意义，别在这里等（审核 M3）。

- [ ] **Step 3: 实现**

- **写入分工（规格 §6.3.1）**：只有 `AddLocalRecent`/`AddRemoteRecent` 写 last path；`recordRecentSFTP(..., false)`（`SftpHome` `:1127` 与传输完成 `:810/823/836/882`）一律不写。`recordRecentSFTP` 的内部调用点全部补 `false` 实参。
- **last path 不受队首短路约束（§6.3.2）**：`if rememberLast { lastChanged = a.rememberRemotePathLocked(host, path) }` —— **门控必须在 helper 之外（审核 I2）**：若写成「先无条件比较 `LastRemoteDirs[host] != path`」，则 `rememberLast == false` 的内部路径（传输完成 `:810/823/836/882`）会因为 map 永不更新而**每次都判 changed** ⇒ 每个文件一次全量 TOML 编码 + rename，正是 `app.go:1374-1375` 注释要避免的写放大回归。然后只更新需要更新的部分：`lastChanged == false && recents 队首同值` ⇒ `return nil`（不写盘）；否则一次 `saveConfig()`。
- **四个函数统一并发改造（§6.3.4 第 3 条）**：
  - `AddLocalRecent`/`AddRemoteRecent`：`kept := a.cfg.X[:0]` → `kept := make([]T, 0, len(a.cfg.X))`；
  - `AddBookmark`：**不能只写 `append(a.cfg.Bookmarks, b)`**（cap 有余量时仍会写进共享底层数组）⇒ 构造新数组 `nb := make([]config.Bookmark, 0, len(a.cfg.Bookmarks)+1)`，先拷旧再追加，最后整结构赋值 `a.cfg.Bookmarks = nb`；
  - `RemoveBookmark`：同构造新数组后逐项过滤；
  - 去重/增删/（recents 两处的）last path 更新整段放进 `a.cfgMu.Lock()` … `Unlock()`，锁内只赋值，解锁后再 `saveConfig()`；**锁内只允许调 `*Locked` 系列，禁止调公开绑定**（审核 I3：同锁重入会死锁）。
  - `recordRecentSFTP` 保持「fire-and-forget」（`_ =`）与既有入参守卫，只在写锁内走 recents-only 入口。
- **读方也纳入本轮（审核 M8）**：`ListLocations()`（`:1291-1310`）现在无锁读 `a.cfg.Bookmarks/LocalRecent/RemoteRecent`，而 Wails 每次调用一个 goroutine ⇒ 给它加 `cfgMu.RLock()`（一处、零成本），否则「这四个写入点并发安全」的宣称是过头的。`localRecentPaths()` 等 startup 期读点保持现状，记入 §13。
  - **保持既有入参守卫与语义**：`AddRemoteRecent` 的 `host == "" || path == ""` 直接 return、`RemoveBookmark` 的 scope/host/path 匹配规则、各函数的去重置顶与 20 条上限，一律不动。
- `cfgMu` 注释（`:37-42`）扩写为"保护 `cfg.App` 与 `cfg.SftpUI`"，并注明四个写入函数的锁规约。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./ -run 'Recent|Bookmark|LastPath|ConfigWrites' -race -v`
Expected: PASS（含既有 `TestAddRecentHeadShortCircuit`、`TestAddRemoteRecentDedupesAndCaps` 不回归）。

- [ ] **Step 5: 源码结构性判据**

Run: `grep -nE 'Recent\[:0\]' app.go; grep -nE 'SftpUI\.LastRemoteDirs\[[^]]*\]\s*=' app.go; grep -n 'make(map\[string\]string' app.go`
Expected: **前两条无输出**（没有原地复用 slice、没有原地**赋值**写 map），**第三条命中**（last path 的 map 由 helper 内 `make` 构造）。
**注意判据口径（审核 I1）**：只匹配**赋值**（`...] =`），因为 §6.3.2 的读比较 `LastRemoteDirs[host] != path` 也会命中裸 `LastRemoteDirs[` —— v1.6 的 grep 与实现互相打架，且对 BF14 要求的「写锁内构造了新 map」毫无判别力。

- [ ] **Step 6: 提交**

```bash
git add app.go app_test.go
git commit -m "fix(app): 配置写入点并发改造（新数组+写锁）与 last path 写入规则（recordRecentSFTP 不再改最后路径）"
```

---

## Task 5: 重生成 Wails 绑定并验证前端构建

**Files:**
- Modify: `frontend/wailsjs/go/main/App.js`、`frontend/wailsjs/go/main/App.d.ts`、`frontend/wailsjs/go/models.ts`（**生成物，禁止手改**）
- Test: 无新增测试；判据是构建与类型可见性

**Interfaces:**
- Consumes: T2（`SftpProbe`）、T3（`GetSftpUI`/`SaveSftpUI`）。
- Produces（前端后续任务 import 的符号）：
  ```js
  import { GetSftpUI, SaveSftpUI, SftpProbe } from '../../wailsjs/go/main/App'
  // models.ts 新增 class SftpUIState；last_remote_dirs 形如 Record<string, string>
  ```

- [ ] **Step 1: 确认 `frontend/dist` 已存在**

Run: `test -d frontend/dist && echo dist-ok`
Expected: `dist-ok`。若不存在：按 Global Constraints 的 Task 0 执行 `cd frontend && npm ci && npm run build` 补齐（**审核 B1**：这个前置属于所有 Go 命令，不属于 T5；T2–T4 的 `go test ./` 就会用到它）。

- [ ] **Step 2: 重生成**

Run（仓库根）: `GOCACHE=$PWD/.gocache wails generate module -tags webkit2_41`
Expected: 退出码 0；`git status --short` 显示三个生成文件被改动。若失败：如实报告（很可能是 `frontend/dist` 缺失或 webkit tag 不对），**不得手改生成文件**。

- [ ] **Step 3: 确认生成物包含新符号**

Run: `grep -n 'GetSftpUI\|SaveSftpUI\|SftpProbe' frontend/wailsjs/go/main/App.d.ts frontend/wailsjs/go/main/App.js && grep -n 'class SftpUIState' frontend/wailsjs/go/models.ts`
Expected: 三个函数各命中 `.d.ts` 与 `.js`；`class SftpUIState` 命中一次。

- [ ] **Step 4: 再编一次前端（**非判别性**，审核 M4）**

Run: `cd frontend && npm run build`
Expected: 构建成功。**说明**：`package.json` 的 build 只是 `vite build`（无类型检查），而此刻 T6–T13 尚未实施、没有任何前端代码 import 新符号 ⇒ 这一步用旧绑定也能过，**不能**证明新绑定的可见性。真正的判据是 Step 3 的 grep；「新符号真被 import 且能编过」由 T7 Step 4 与 T14 的 `make ci` 提供。

- [ ] **Step 5: 提交**

```bash
git add frontend/wailsjs
git commit -m "chore(bindings): 重生成 Wails 绑定（GetSftpUI/SaveSftpUI/SftpProbe + SftpUIState 模型）"
```

---

## Task 6: 纯函数工具 `nextSort` / `resolveRemoteTarget`

**Files:**
- Create: `frontend/src/utils/sort.js`、`frontend/src/utils/remotepath.js`
- Test: `frontend/src/utils/sort.test.js`、`frontend/src/utils/remotepath.test.js`

**Interfaces:**
- Produces:
  ```js
  export function nextSort(current, key)         // current={key,dir} → {key, dir: 1|-1}
  export function resolveRemoteTarget(input)     // {pendingPath, pendingHome, remembered, home}
                                                 // → {path: string, fromMemory: boolean}
  ```

- [ ] **Step 1: 写失败测试**

`sort.test.js`：
```js
it('同列取反', () => { expect(nextSort({ key: 'name', dir: 1 }, 'name')).toEqual({ key: 'name', dir: -1 })
                       expect(nextSort({ key: 'name', dir: -1 }, 'name')).toEqual({ key: 'name', dir: 1 }) })
it('换列归升序', () => { expect(nextSort({ key: 'name', dir: -1 }, 'size')).toEqual({ key: 'size', dir: 1 }) })
```

`remotepath.test.js`（**D15 回归守卫**）：
```js
it('pendingPath 优先于一切', () => expect(resolveRemoteTarget({ pendingPath: '/p', pendingHome: true, remembered: '/r', home: '/h' }))
  .toEqual({ path: '/p', fromMemory: false }))
it('pendingHome 优先于记忆路径', () => expect(resolveRemoteTarget({ pendingHome: true, remembered: '/r', home: '/h' }))
  .toEqual({ path: '/h', fromMemory: false }))
it('记忆路径优先于 home 并被标记', () => expect(resolveRemoteTarget({ remembered: '/r', home: '/h' }))
  .toEqual({ path: '/r', fromMemory: true }))
it('无记忆则回落 home', () => expect(resolveRemoteTarget({ remembered: '', home: '/h' }))
  .toEqual({ path: '/h', fromMemory: false }))
it('pendingPath 空串视为未指定（工具栏「连接」主流程恒为 ""）', () =>   // 审核 F16
  expect(resolveRemoteTarget({ pendingPath: '', remembered: '/r', home: '/h' }))
    .toEqual({ path: '/r', fromMemory: true }))
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/sort.test.js src/utils/remotepath.test.js`
Expected: FAIL（找不到模块）。

- [ ] **Step 3: 实现**

两个文件都是纯函数，无 import 依赖；`nextSort` 的 dir 只产出 `1`/`-1`。`resolveRemoteTarget` 的空值口径：`pendingPath`/`remembered` 都按"非空才生效"判断（空串视为未指定）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/sort.test.js src/utils/remotepath.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/utils/sort.js frontend/src/utils/sort.test.js frontend/src/utils/remotepath.js frontend/src/utils/remotepath.test.js
git commit -m "feat(utils): 排序切换与远程落点决策的纯函数（含 D15 回归用例）"
```

---

## Task 7: `stores/sftpui.js`

**Files:**
- Create: `frontend/src/stores/sftpui.js`
- Test: `frontend/src/stores/sftpui.test.js`

**Interfaces:**
- Consumes: T5 的 `GetSftpUI`/`SaveSftpUI`。
- Produces:
  ```js
  useSftpUIStore() → {
    loaded: boolean,
    lastLocalDir: string,
    lastRemoteDirs: Record<string,string>,
    sort: { local: {key,dir}, remote: {key,dir} },
    load(): Promise<void>,                       // 失败抛出
    lastPathFor(pane, host): string,
    setSort(pane, key, dir): Promise<void>,      // 值未变 → 不写盘；写盘失败抛出
    noteLocalPath(path): void, noteRemotePath(host, path): void,
    saveSnapshot(): Promise<void>,               // 唯一 SaveSftpUI 出口
  }
  ```

- [ ] **Step 1: 写失败测试**

`vi.mock('../../wailsjs/go/main/App')`（照 `stores/locations.test.js:1-32` 的写法），用例：

```js
it('load 填充整体快照并归一非法 sort', ...)         // GetSftpUI 返回 {sort_local_key:'bogus', sort_local_dir:7} → sort.local == {key:'name',dir:1}
it('load 失败时抛出且 loaded 保持 false', ...)      // GetSftpUI reject → await expect(store.load()).rejects
it('lastPathFor 两侧分支', ...)                     // remote 取 map、local 取 lastLocalDir，缺失时返回 ''
it('setSort 值未变不写盘', ...)                     // 同 {key,dir} → SaveSftpUI 调用次数 0
it('setSort 先读回再只覆盖 sort 四字段', ...)        // GetSftpUI 返回一个带路径的快照 → SaveSftpUI mock 收到的 payload
                                                    // 必须逐字含六个 snake_case 键，且 last_* 与读回值一致、sort_* 为新值
it('SaveSftpUI 失败时 setSort 抛出且内存不回滚', ...) // reject → rejects，且 store.sort 仍是新值
it('noteRemotePath/noteLocalPath 只改内存不调 IPC', ...) // mock 调用次数为 0
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/stores/sftpui.test.js`
Expected: FAIL（模块不存在）。

- [ ] **Step 3: 实现**

Pinia options store，照 `stores/locations.js` 的风格（中文注释解释"为什么"）。要点：
- `saveSnapshot()`：`const cur = await GetSftpUI()` → 组 payload（**逐字六个 snake_case 键**，`last_*` 取 `cur`、`sort_*` 取本地 state）→ `await SaveSftpUI(payload)`；**禁止**用 `{...state}` 展开。
- `load()`：`GetSftpUI()` → 写入四个 state 字段 → sort 再做一次前端白名单兜底；失败**抛出**（调用方负责记日志），`loaded` 保持 `false`。
- `setSort`：值未变直接 `return`；否则先改内存、再 `await saveSnapshot()`（失败会抛，内存不回滚）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/stores/sftpui.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/stores/sftpui.js frontend/src/stores/sftpui.test.js
git commit -m "feat(store): sftpui store（整体快照 load / 读回合并的 saveSnapshot / 值未变短路）"
```

---

## Task 8: `locations` store 的最后路径内存镜像

**Files:**
- Modify: `frontend/src/stores/locations.js`（`addLocalRecent` `:43-50`、`addRemoteRecent` `:51-57`）
- Test: `frontend/src/stores/locations.test.js`

**Interfaces:**
- Consumes: T7 的 `useSftpUIStore().noteLocalPath/noteRemotePath`。
- Produces: 无新 API；副作用是"sftpui 镜像在导航成功后同步更新"。

- [ ] **Step 1: 写失败测试**

```js
it('addRemoteRecent 成功后同步 sftpui 镜像且不额外发 IPC', ...)
// mock App 模块：AddRemoteRecent 调用 1 次；断言 useSftpUIStore().lastPathFor('remote','prod') === '/srv'
it('addLocalRecent 成功后同步本地镜像', ...)
it('队首同值短路时不发 IPC 也不改镜像', ...)   // 预置 localRecents[0].path === path
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/stores/locations.test.js`
Expected: FAIL（镜像仍为空串）。

- [ ] **Step 3: 实现**

在两个 action 的**成功后**（即 `await AddXxx` 之后、更新列表的同时）调用 `useSftpUIStore().noteRemotePath(host, path)` / `noteLocalPath(path)`；依赖方向只允许 `locations.js → sftpui.js`。在短路分支（队首同值）**不改镜像**，并加注释说明"队首同值 ⇒ 镜像值本就相同（`loaded === true` 时成立）"。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/stores/locations.test.js`
Expected: PASS（含既有用例）。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/stores/locations.js frontend/src/stores/locations.test.js
git commit -m "feat(store): locations 导航成功后同步 sftpui 最后路径镜像（零新增 IPC）"
```

---

## Task 9: `FilePane` 排序状态上收（含 `arrow()`）+ SSR 渲染断言

**Files:**
- Modify: `frontend/src/components/FilePane.vue`（`sortKey/sortDir` ref `:124-125`、`sortBy` `:129-136`、`arrow` `:180-182`、`emits` `:29`、`visible` `:138-154`、模板 `:226-228`）
- Test: `frontend/src/components/filePaneWiring.test.js`（扩展）、新建 `frontend/src/components/filePane.render.test.js`

**Interfaces:**
- Consumes: T6 的 `nextSort`。
- Produces（T11 依赖）：
  ```js
  // props: sortKey（默认 'name'）、sortDir（默认 1）
  // emit:  'sort' → { key, dir }
  ```

- [ ] **Step 1: 写失败测试**

`filePaneWiring.test.js` 追加：
```js
it('排序读 props：无本地 ref、无 *.value 残留、emits 含 sort', () => {
  expect(pane).not.toContain('const sortKey = ref(')
  expect(pane).not.toMatch(/sortKey\.value|sortDir\.value/)     // 抓 arrow() 漏改
  expect(pane).toMatch(/defineEmits\(\[[^\]]*'sort'/)
  expect(pane).toContain("from '../utils/sort'")
})
```

`filePane.render.test.js`（SSR 真渲染，仿 `TransferQueue.render.test.js:1-11`）：
```js
const render = async (props) => await renderToString(createSSRApp({ render: () => h(FilePane, { items, ...props }) }))
// 必须 await（先例 TransferQueue.render.test.js 每个用例都 await render(...)）：漏了拿到的是 Promise
it('按 props 排序并渲染箭头与 active 类', () => {
  const html = render({ sortKey: 'size', sortDir: -1 })
  // 断言：行序为 size 降序（用 indexOf 比较两个文件名的位置）、'▼' 出现在 "大小" 列头、该列头带 active
})
it('visible 载荷就是可见顺序', () => {
  const seen = []
  render({ sortKey: 'name', sortDir: 1, onVisible: (keys) => seen.push(keys) })
  expect(seen.at(-1)).toEqual(['a.txt', 'b.txt'])   // items 里同时含一个被过滤的 dotfile 时可再加断言
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/components/filePane.render.test.js`
Expected: FAIL（FilePane 仍用内部 ref；渲染断言在旧实现下拿不到 `▼`）。

- [ ] **Step 3: 实现**

- 删除 `sortKey`/`sortDir` 两个 ref，新增同名 props（默认 `'name'` / `1`）。
- `sortBy(key)` 改为 `emit('sort', nextSort({ key: props.sortKey, dir: props.sortDir }, key))`。
- **`arrow()` 改为读 props**（`props.sortKey === key ? (props.sortDir === 1 ? '▲' : '▼') : ''`）——这是本次最容易漏、漏了就白屏的一处。
- `visible` 的排序体不动，只把读源换成 props；`shown`/`visibleKeys`/`@visible` 链路保持不变（决策 32）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/components/filePane.render.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/FilePane.vue frontend/src/components/filePaneWiring.test.js frontend/src/components/filePane.render.test.js
git commit -m "refactor(FilePane): 排序状态上收为 props/emit（含 arrow 读源修正），补 SSR 渲染断言"
```

---

## Task 10: `FilePane` 右键触发面 + `ContextMenu` 禁用样式

**Files:**
- Modify: `frontend/src/components/FilePane.vue`（`.pane` 根 `:195-197`、`..` 行 `:231-233`、handler 新增）、`frontend/src/components/ContextMenu.vue`（样式 `:13-21`）
- Test: `frontend/src/components/filePaneWiring.test.js`、新建 `frontend/src/components/contextMenuWiring.test.js`

**Interfaces:**
- Consumes: 既有 `emit('context', { item, event })`（行右键 `:241` 不变）。
- Produces（T12 依赖）：空白/列头/过滤栏/`..` 行右键一律 emit `{ item: null, event }`。

- [ ] **Step 1: 写失败测试**

`filePaneWiring.test.js` 追加：
```js
it('空白右键：对可编辑控件与任何 li 放行、对 .pos-wrap 先 closePos 且不弹菜单', () => {
  const fn = pane.match(/function onPaneContext\(e\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(fn).toContain('input, textarea, [contenteditable]:not([contenteditable="false"])')
  expect(fn).toMatch(/closest\('\.pos-wrap'\)[\s\S]*closePos\(\)/)
  expect(fn).toMatch(/closest\('li'\)[\s\S]*return/)
  expect(fn).toContain("emit('context', { item: null, event: e })")
  expect(pane).toMatch(/<div class="pane[^>]*@contextmenu="onPaneContext"/)
  expect(pane).not.toMatch(/@contextmenu\.prevent="onPaneContext"/)   // 必须不带 .prevent
})
it('.. 行新增 contextmenu 处理器并按无选中上抛', () => {
  const up = pane.slice(pane.indexOf('class="up"'), pane.indexOf('class="up"') + 260)
  expect(up).toContain('@contextmenu.prevent="emit(\'context\', { item: null, event: $event })"')
})
```
`contextMenuWiring.test.js`：`expect(css).toMatch(/button:disabled/)` 且含 `color: var(--text-faint)`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/components/contextMenuWiring.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `FilePane.vue`：`.pane` 根加 `@contextmenu="onPaneContext"`（**不带 `.prevent`**）；`onPaneContext` 分支顺序照 spec §5.2 代码块（① 可编辑放行 ② `.pos-wrap` → `closePos()` + `preventDefault()` + return ③ 任何 `li` → return ④ `preventDefault()` + emit `item: null`）。`..` 行加 `@contextmenu.prevent`（emit `item: null`）。
- 注释写明三件事：为什么不带 `.prevent`（保住路径栏/过滤框的原生复制粘贴）、为什么 `li` 必须无条件 yield（`.prevent` 不 stopPropagation ⇒ `..` 行会双发射）、loading 期间 `.list.busy{pointer-events:none}` 会让行右键落到面板级（已知边界）。
- `ContextMenu.vue`：补 `button:disabled` 与 `button:disabled:hover` 两条规则（照 spec §5.4）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/components/contextMenuWiring.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/FilePane.vue frontend/src/components/ContextMenu.vue frontend/src/components/filePaneWiring.test.js frontend/src/components/contextMenuWiring.test.js
git commit -m "feat(FilePane): 右键触发面分级（空白/.. 行=面板级）与菜单禁用样式"
```

---

## Task 11: `SftpView` 取列表函数抽取 + 启动恢复顺序 + 落点决策 + 失效回落

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（`loadRemote` `:218-228`、`loadLocal` `:394-404`、`connect` `:238-257`、`goRemoteHome` `:306-314`、`onMounted` `:881-889`、`onNavigate` `:316-340`、`pickPosition` `:342-350`、`syncConnection` `:900-911`）
- Test: 新建 `frontend/src/views/sftpRestoreWiring.test.js`

**Interfaces:**
- Consumes: T6 `resolveRemoteTarget`、T7 store、T8 镜像。
- Produces（T12/T13 依赖）：
  ```js
  async function fetchRemote(target) → items   // 抛错
  async function fetchLocal(target) → items    // 抛错
  async function loadRemote()                  // 包装 fetchRemote，吞错记日志（语义不变）
  async function loadLocal()                   // 包装 fetchLocal，吞错记日志
  function onSort(pane, { key, dir })          // try/catch → sftpui.setSort → err(e)
  // connect()/syncConnection() 共用 resolveRemoteTarget；pendingHome 显式标志
  ```

- [ ] **Step 1: 写失败测试**

`sftpRestoreWiring.test.js`（读源码钉形状）：
```js
it('onMounted 顺序硬约束：sftpui.load 在 Cwd/loadLocal 之前', () => {
  const body = src.match(/onMounted\(async \(\) => \{([\s\S]*?)\n\}\)/)[1]
  const i = (s) => body.indexOf(s)
  expect(i('await locations.load()')).toBeGreaterThan(-1)
  expect(i('await sftpui.load()')).toBeGreaterThan(i('await locations.load()'))
  expect(i('await Cwd()')).toBeGreaterThan(i('await sftpui.load()'))
  expect(i('await loadLocal()')).toBeGreaterThan(i('await Cwd()'))
})
it('本地路径用记忆值优先、失败回落 Cwd', () => {
  const body = src.match(/onMounted\(async \(\) => \{([\s\S]*?)\n\}\)/)[1]
  expect(body.indexOf("lastPathFor('local')")).toBeGreaterThan(-1)
  expect(body.indexOf("lastPathFor('local')")).toBeLessThan(body.indexOf('Cwd()'))
  expect(body).toContain('上次的本地目录')          // 回落文案（spec §10 逐字）
  expect(body).toMatch(/warn\(/)                   // 审核 F7：级别是 warn，不能用 err()（它硬编码 error）
})
it('connect/syncConnection 共用 resolveRemoteTarget，且 pendingHome 是显式标志', () => {
  expect(src.match(/resolveRemoteTarget\(/g).length).toBeGreaterThanOrEqual(2)
  expect(src).toContain('pendingHome')
  const home = src.match(/async function goRemoteHome\(\)\s*\{([\s\S]*?)\n\}/)[1]   // 只裁函数体，别用跨边界正则
  expect(home).not.toContain("pendingPath.value = ''")
  expect(home).toContain('pendingHome')
})
it('connect 被 sftpui.loaded 兜住，且 load 失败不会冒泡成 unhandled rejection（审核 F8）', () => {
  expect(src).toMatch(/!sftpui\.loaded/)
  expect(src).toMatch(/sftpui\.load\(\)\.catch\(err\)|try\s*\{[^}]*sftpui\.load\(\)/)
})
it('排序接线（审核 F9，最后一米）', () => {
  expect((src.match(/@sort="onSort\('/g) || []).length).toBe(2)
  expect(src).toContain(':sort-key="sftpui.sort.local.key"')
  expect(src).toContain(':sort-key="sftpui.sort.remote.key"')
  expect(src).toContain(':sort-dir="sftpui.sort.local.dir"')
  expect(src).toContain(':sort-dir="sftpui.sort.remote.dir"')
})
it('loadRemote 成功路径记录远程最后路径（审核 F10）', () => {
  const fn = src.match(/async function loadRemote\(\)[\s\S]*?\n\}/)[0]
  expect(fn).toMatch(/addRemoteRecent\(/)
})
it('记忆路径失效回落 home 只在 fromMemory 时发生', () => {
  const fn = src.match(/async function connect\(\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(fn).toContain('fromMemory')
  expect(fn).toMatch(/warn\('上次的远程目录/)     // 审核 F7：级别是 warn，不是 err()
})
it('取列表函数抽取：loadRemote/loadLocal 包装会抛错的 fetch*', () => {
  expect(src).toContain('async function fetchRemote(')
  expect(src).toContain('async function fetchLocal(')
  expect(src).toMatch(/async function loadRemote\(\)[\s\S]{0,200}fetchRemote\(/)
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/sftpRestoreWiring.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- 抽 `fetchRemote(target)`/`fetchLocal(target)`（**抛错**）；`loadRemote`/`loadLocal` 包一层 catch，**外部语义不变**（seq 守卫、loading、`addRecent` 全部保留）。**审核 F23**：`fetch*` **只做** `List*(target) || []` 并抛错 —— 绝不自增 `localSeq`/`remoteSeq`、不改 loading、不记 recent；这三件事留在 `loadRemote/loadLocal/goRemote/goLocal`，否则 `goRemote` 的「旧响应丢弃 + 失败回滚」（`:289/294/299`）会被破坏。
- **`loadRemote` 成功路径补 `await locations.addRemoteRecent(host.value, remotePath.value)`（审核 F10）**：双击进目录与刷新都只经它；store 有队首短路 ⇒ 不增加写盘。
- `loadRemote` 的 catch 改走 `remoteOpFailed(e)`（**审核 F5**：v1.7 写的「err(e)，语义不变」与 T13 落点 #2 冲突；正确表述是「记原文 + 一次探活复核，可能自愈」）。
- `onMounted` 顺序照 spec §6.4 B3；`localPath` 先取 `sftpui.lastPathFor('local')`，为空或打开失败才回落 `await Cwd()`（回落时写 warn 文案，逐字取 spec §10）。
- `connect()` 首部 `if (!sftpui.loaded) await sftpui.load().catch(err)`（**审核 F8**：`load()` 会 reject，裸 await 会让连接按钮 unhandled rejection）。
- 落点用 `resolveRemoteTarget({ pendingPath: pendingPath.value, pendingHome: pendingHome.value, remembered: sftpui.lastPathFor('remote', h), home })` —— **必须取 `.value`（审核 F17）**：传 ref 对象会恒为真值、把 ref 当路径交给 `SftpList`。
- 若 `fromMemory === true` 且首列失败 ⇒ 回落 `home` 再列一次 + `warn(…)`（审核 F7）；`pendingPath`/`pendingHome` 消费后清空（含失败分支）。
- `onMounted` 里也要容忍 load 失败：`try { await sftpui.load() } catch (e) { err(e) }`，否则后续 `Cwd()`/`loadLocal()` 全不执行（审核 F8）。
- `goRemoteHome()` 未连接分支改为 `pendingHome.value = true; await connect()`。
- `syncConnection()` 未连接分支改为共用同一落点决策（不再无条件 `remotePath = await SftpHome(h)`）。
- `onSort(pane, { key, dir })`：`try { await sftpui.setSort(pane, key, dir) } catch (e) { err(e) }`；两侧 `FilePane` 传 `:sort-key`/`:sort-dir` 并接 `@sort`。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/views/sftpRestoreWiring.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/views/SftpView.vue frontend/src/views/sftpRestoreWiring.test.js
git commit -m "feat(sftp): 启动恢复顺序与落点决策（记忆路径/主目录显式标志/失效回落），抽取可抛错的取列表函数"
```

---

## Task 12: `SftpView` 刷新入口 + 菜单矩阵与置灰

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（工具栏 `:983-993`、`showMenu` `:434-443`、`doAction` `:680-702`、`actionsFor` `:460-473`、`ContextMenu` 模板 `:1028-1042`、`onPaneAction` `:475-485`）
- Test: 新建 `frontend/src/views/sftpMenuWiring.test.js`

**Interfaces:**
- Consumes: T11 的 `loadRemote`/`loadLocal`/`fetch*`、T9 的 `@sort`（已接）。
- Produces:
  ```js
  async function refreshAll()        // loadLocal() ⊕（connected ? loadRemote() : skip），Promise.all
  async function refreshPane(pane)   // 只刷本面板
  // doAction(name) 顶部：name==='refresh' 先 return；name 非 refresh/mkdir 且 names 为空 → return
  // showMenu(pane, { item: null, event }) 也要能接受 null item
  ```

- [ ] **Step 1: 写失败测试**

```js
it('工具栏刷新：调 refreshAll，且该 button 标签内无 disabled', () => {
  const btn = src.match(/<button[^>]*refreshAll[^>]*>[^<]*<\/button>/)[0]
  expect(btn).not.toContain(':disabled')
  expect(src).not.toContain('@click="loadRemote" :disabled="!connected"')   // 旧按钮形态已消失
})
it('右键刷新只刷本面板', () => expect(src).toMatch(/async function refreshPane\(pane\)[\s\S]{0,200}loadLocal\(\)/))
it('doAction 的 refresh 分支在 sel.single 之前', () => {
  const body = src.match(/async function doAction\(name\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(body.indexOf("name === 'refresh'")).toBeLessThan(body.indexOf('sel.single('))
})
it('空选择防御只约束依赖选中的动作，且在 sel.single 之后（审核 F2）', () => {
  const body = src.match(/async function doAction\(name\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(body).toMatch(/SELECTION_DEPENDENT|\['download', 'upload', 'remove', 'rename'\]/)
  expect(body).not.toMatch(/name !== 'refresh' && name !== 'mkdir' && !names\.length/)
  expect(body.indexOf('sel.single(')).toBeLessThan(body.indexOf('SELECTION_DEPENDENT'))
})
it('置灰接线：唯一判据 remoteDep + 批量下拉也置灰（审核 F3/F19）', () => {
  expect(src).toContain('remoteDep')
  expect(src).toMatch(/remoteDep\s*&&\s*!connected/)
  const af = src.match(/function actionsFor\(pane\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(af).toMatch(/upload-picked'[^}]*disabled: !connected\.value/)
  expect(af).toMatch(/upload'[^}]*disabled: !connected\.value/)
})
it('菜单矩阵：单一判据 + 各分支内容差异（审核 F22）', () => {
  expect(src).toMatch(/const menuHasItem = computed\(/)
  expect(src.match(/doAction\('refresh'\)/g).length).toBeGreaterThanOrEqual(2)
  const tpl = src.slice(src.indexOf('<ContextMenu'), src.indexOf('</ContextMenu>'))
  const blank = tpl.slice(0, tpl.indexOf("doAction('download')"))
  const row = tpl.slice(tpl.indexOf("doAction('download')"))
  expect(blank).toContain("doAction('mkdir')")
  expect(blank).toContain("doAction('upload-picked')")
  expect(row).toContain("doAction('download')")
  expect(row).not.toContain("doAction('mkdir')")
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/sftpMenuWiring.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `refreshAll()`/`refreshPane(pane)` 照 spec §5.1；工具栏按钮改 `@click="refreshAll"` 并去掉 `:disabled`。
- `doAction`：顶部先 `if (name === 'refresh') { closeMenu(); return refreshPane(pane) }`（必须在 `sel.single` 之前，否则多选被收成一项）。
- **空选择防御（审核 F2）**：写在 `sel.single(...)` 与 `const names = [...s.keys]` **之后**，只约束 `['download','upload','remove','rename']` —— 不能写成「除 refresh/mkdir 外一律 return」（会误伤空白菜单的 `upload-picked`，并让未选中行的整条行菜单变 no-op）。
- 新增唯一选中判据 `const menuHasItem = computed(() => !!(menu.value.item && menu.value.item.name !== '..'))`（`..` 行按无选中处理），模板以它切两个菜单分支。
- `showMenu` 支持 `item: null`；`ContextMenu` 模板按 §5.3 矩阵渲染：**两个分支都含** 🔄 刷新（空白菜单为「刷新 · 新建文件夹 · 上传文件…」，行菜单为「下载/上传/重命名/删除 + ── + 刷新」）；用 `remoteDep && !connected` 判据置灰（`remoteDep` 含本地面板的上传类动作）。
- `actionsFor('local')` 的 `upload`/`upload-picked` 两项加 `disabled: !connected.value`（第三个入口；**审核 F3**：script 里 `connected` 是 ref，漏 `.value` 会让置灰恒 false 而前缀式断言照样绿）。
- 表格里的置灰点都要在模板/函数里落实，否则 Step 1 的断言不绿。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/views/sftpMenuWiring.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/views/SftpView.vue frontend/src/views/sftpMenuWiring.test.js
git commit -m "feat(sftp): 工具栏双侧刷新 + 右键菜单分级/置灰（含批量下拉与空选择防御）"
```

---

## Task 13: `SftpView` 连接失效自愈（探活 + 自动重连一次 + 12 个落点）

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（`syncConnection` `:900-911`、`cancelTransfer` `:121-138`、`dispatchTransfer` `:81-116`、`loadRemote` `:218-228`、`connect` `:238-257`、`goRemote` `:286-304`、`goRemoteHome` `:306-314`、`cleanItem` `:159-169`、`removeSelected` `:580-614`、`renameItem` `:617-631`、`mkdirIn` `:638-650`、`uploadPicked` `:654-677`、`onMoveDrop` `:752-803`、`onActivated` `:951-960`）
- Test: 新建 `frontend/src/views/sftpRecoverWiring.test.js`

**Interfaces:**
- Consumes: T2 的 `SftpProbe`、T7 store、T11 的 `connect`/落点决策。
- Produces:
  ```js
  // 探针三态（自审修正：不能用布尔表示"未知"，否则超时会被当成"链路失效"而误重连）
  //    'ok'      = 探针确认可用
  //    'lost'    = 探针明确回答 false（后端说链路不可用）
  //    'unknown' = 超时或探针自身抛错（不改状态、不重连、不记 error）
  async function probeTimed(host) → 'ok' | 'lost' | 'unknown'
  async function verifyRemote()          // connected===false 时早退；'lost' ⇒ recoverConnection；其它不动
  async function remoteOpFailed(e)       // err(e) + verifyRemote()
  async function recoverConnection(host) // recovering 幂等；SftpDisconnect → pendingPath=记忆值 → connect()
  ```

- [ ] **Step 1: 写失败测试**

```js
it('syncConnection 已连接分支探活，且不重置路径/不重载列表', () => {
  const fn = src.match(/async function syncConnection\(\)\s*\{([\s\S]*?)\n\}/)[1]
  // 切片不许依赖未规定的注释（审核 F14）：用花括号配对切
  const connectedBranch = fn.match(/if \(connected\.value\) \{([\s\S]*?)\n  \}/)[1]
  expect(connectedBranch).toContain('probeTimed(')
  expect(connectedBranch).toMatch(/===\s*'lost'/)                                     // 审核 F1：三态字符串配 !(...) 永不触发
  expect(connectedBranch).not.toMatch(/SftpHome|loadRemote\(|remotePath\.value =/)
})
it('recoverConnection：幂等 → SftpDisconnect → connect，且不写 pendingPath（审核 F6）', () => {
  const fn = src.match(/async function recoverConnection\([\s\S]*?\n\}/)[0]
  expect(fn.indexOf('if (recovering) return')).toBeLessThan(fn.indexOf('recovering = true'))
  expect(fn.indexOf('recovering = true')).toBeLessThan(fn.indexOf('SftpDisconnect('))
  expect(fn.indexOf('SftpDisconnect(')).toBeLessThan(fn.indexOf('connect()'))
  // F6：把记忆值塞进优先级 1 的 pendingPath ⇒ fromMemory=false ⇒ §6.5 回落永不触发、R3 承诺失效；
  // 正确做法是不写 pendingPath，让 connect() 在优先级 3 自己取（fromMemory=true）。
  expect(fn).not.toMatch(/pendingPath\.value\s*=\s*sftpui\.lastPathFor/)
})
it('只重连一次：recoverConnection 体内无 while/定时器', () => {
  const fn = src.match(/async function recoverConnection\([\s\S]*?\n\}/)[0]
  expect(fn).not.toMatch(/while\s*\(|setInterval/)
})
it('三态契约：只有 lost 触发重连；unknown 与抛错都不改状态', () => {
  const vf = src.match(/async function verifyRemote\(\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(vf).toMatch(/!connected\.value[\s\S]{0,40}return/)
  expect(vf).toMatch(/finally\s*\{[^}]*probing = false/)
  expect(vf).toMatch(/=== 'lost'[\s\S]{0,60}recoverConnection|'lost'/)
  const pf = src.match(/async function probeTimed\([\s\S]*?\n\}/)[0]
  expect(pf).toMatch(/Promise\.race/)
  expect(pf).toMatch(/clearTimeout/)
  expect(pf).toMatch(/'unknown'/)
  expect(pf).not.toMatch(/catch[\s\S]{0,80}'lost'/)      // 探针抛错不得被当成 lost
})
it('13 个远程落点覆盖（可达性口径，可执行断言；审核 F13）', () => {
  const bodyOf = (n) => (src.match(new RegExp('(async )?function ' + n + '\\([^)]*\\)\\s*\\{([\\s\\S]*?)\\n\\}')) || [])[2] || ''
  const remote = ['dispatchTransfer', 'loadRemote', 'goRemote', 'goRemoteHome', 'uploadPicked',
                  'removeSelected', 'renameItem', 'mkdirIn', 'onMoveDrop', 'cleanItem']
  for (const fn of remote) expect(bodyOf(fn), fn).toMatch(/remoteOpFailed\(|verifyRemote\(/)
  for (const fn of ['removeSelected', 'renameItem', 'mkdirIn', 'onMoveDrop'])
    expect(bodyOf(fn), fn).toMatch(/pane === 'local'[\s\S]{0,60}err\(e\)|err\(e\)[\s\S]{0,60}pane === 'local'/)
  expect(bodyOf('cleanItem')).toMatch(/rec\.direction/)
  const mv = bodyOf('onMoveDrop')
  expect(mv.indexOf('try {')).toBeLessThan(mv.indexOf('SftpList('))
  expect(src).toContain('@failed="remoteOpFailed"')                                  // SearchOverlay 归口
  expect(src).not.toMatch(/async function connect\(\)[\s\S]{0,400}remoteOpFailed/)  // connect 保持 err(e)
})
it('远程操作成功后回同步 connected=true，且带 host 守卫（审核 F21）', () => {
  const fn = src.match(/async function dispatchTransfer\([\s\S]*?\n\}/)[0]
  expect(fn).toMatch(/const h = host\.value/)
  expect(fn).toMatch(/host\.value === h && !connected\.value\) connected\.value = true/)
})
it('取消中的失败跳过探活 + 三条自愈文案逐字 + 探活耗时日志（审核 F15/F7/F12）', () => {
  const fn = src.match(/async function dispatchTransfer\([\s\S]*?\n\}/)[0]
  expect(fn).toMatch(/cancelRequested[\s\S]{0,120}remoteOpFailed|remoteOpFailed[\s\S]{0,120}cancelRequested/)
  expect(src).toContain('远端连接已失效（主机休眠或网络中断），正在重连…')
  expect(src).toContain('已重新连接')
  expect(src).toContain('重连失败：')
  expect(src).toContain('探活耗时')
})
it('probing/recovering 是非 ref 的普通 let（审核 F15）', () => {
  expect(src).toMatch(/let probing = false/)
  expect(src).toMatch(/let recovering = false/)
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/sftpRecoverWiring.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `probeTimed(host)` → 三态：`Promise.race([SftpProbe(host), 15s 计时器])`；探针返回 `true` → `'ok'`、`false` → `'lost'`、超时或**探针自身抛错** → `'unknown'`；`setTimeout` 句柄必须 `clearTimeout`。
- `verifyRemote()`：`if (!h || !connected.value || probing) return`；`probing = true` … `finally { probing = false }`；**只有 `'lost'` 才** `recoverConnection(h)`（`'unknown'` 不改状态、不重连、不记 error）。
- `recoverConnection(h)`：入口 `if (recovering) return`；按 spec §8.3 步骤 1–7；成功/失败判据 = `connected.value`；`host.value !== h` 时静默 return（不记「重连失败」）。
  - **不要写 `pendingPath.value = sftpui.lastPathFor('remote', h)`（审核 F6）**：`pendingPath` 是优先级 1 且会让 `resolveRemoteTarget` 返回 `fromMemory === false` ⇒ §6.5「仅来自记忆才回落 home」在自愈路径永不触发（R3 承诺失效）；`connect()` 本来就会在优先级 3 取同一记忆值。
- **`connect` 保持 `err(e)`、不接入 `remoteOpFailed`**（审核 F13：它只在未连接时被调用，守卫必然命中 ⇒ 死接线）。
- **`SearchOverlay.vue` 纳入本轮（审核 F13）**：`emits` 增加 `'failed'`、catch 里 `emit('failed', e)`；`SftpView` 的 `<SearchOverlay>` 加 `@failed="remoteOpFailed"`。
- 回同步 `connected` 时**带 host 守卫**（审核 F21）：await 之前 `const h = host.value`，之后 `if (host.value === h && !connected.value) connected.value = true`。
- `remoteOpFailed(e)` = `err(e)` + `await verifyRemote()`；**按 spec §8.4 的 12 项清单**逐个替换。三处形状要写对：① `dispatchTransfer` 保留取消分支的原文日志 —— `if (rec.cancelRequested) err(e) else await remoteOpFailed(e)`；② `onMoveDrop` 把 `targetItems` 加载与 `askConflict` 挪进 `try`；③ `cleanItem` 按 `rec.direction` 分派（download=本地分支保持 `err`，否则 `remoteOpFailed`）。
- `dispatchTransfer` 成功路径若 `connected.value === false` ⇒ 置 `true`（§8.5 的一致性回同步）。
- 注释写清三处"为什么"：`verifyRemote` 的 `connected===false` 早退（防"连接本身失败"再触发一轮）、`probing` 与 `recovering` 各管一段、探针超时按"未知"处理。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/views/sftpRecoverWiring.test.js`
Expected: PASS。

- [ ] **Step 5: 全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全绿（含既有 `sftpDropWiring.test.js`、`filePaneWiring.test.js`、`locations.test.js` 等）。

- [ ] **Step 6: 提交**

```bash
git add frontend/src/views/SftpView.vue frontend/src/views/sftpRecoverWiring.test.js
git commit -m "feat(sftp): 连接失效自愈（切标签探活/操作失败复核/自动重连一次/12 个落点覆盖）"
```

---

## Task 14: 全量门禁 + 真机验收 + 验收报告

**Files:**
- Create: `docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-acceptance.md`
- Test: 无新增单测；判据是 `make ci` 全绿 + 真机证据

**Interfaces:**
- Consumes: T1–T13 的全部产物。
- Produces: 验收报告（含命令与原始输出、截图路径、逐条判据结论）。

- [ ] **Step 1: 全量门禁**

前置：`frontend/dist` 必须存在（Global Constraints 的 Task 0；`make ci` 第一步也会产它）。
Run: `GOCACHE=$PWD/.gocache make ci`
Expected: 四步全绿；把完整输出保存到报告的证据目录（例如 `.superpowers/sftp-refresh-2026-10-10/make-ci.log`）。

- [ ] **Step 2: Linux 真机验收 A/B/C 组**

按 spec §11.3 的 A1/A2/B1/B2/C1/C2/C3/C4/E1/E2 逐条执行；每条记录"命令/操作 + 可观测判据 + 原始证据"（截图路径或日志原文）。**C4 必须做到"重启后仍落 `/var/log`"**，这是 BF1 那个静默数据损坏的端到端回归。

- [ ] **Step 3: Linux 真机验收 D/F 组（断链自愈）**

按 spec §11.3 的 D1/D2/D3/F1/F2 执行：断链用 `ss -K dst <host> dport = :22` 或 `iptables -A OUTPUT -p tcp --dport 22 -j DROP`（**审核 F20**：两者都需 root/CAP_NET_ADMIN，差内核 `CONFIG_INET_DIAG_DESTROY`/容器 capabilities，不是「有 root/无 root」之别；哪个可用跑哪个并在报告写明）；Windows 侧用 VirtualBox 停用网卡或 `netsh interface set interface <名称> disable`。D1 给出日志原文（含时间戳）+ 自愈前后截图；F1 给出 `探活耗时 <N>ms` 日志汇总 + 无「远端连接已失效」误判；F2 跑 `SSHORE_SFTP_TRANSPORT=batch`。

- [ ] **Step 4: Windows 客户机验收**

在 VirtualBox Windows 客户机上重跑 A1/A2/B1/B2/C1/C4/D1（WebView2 的 contextmenu/`e.target` 语义与 WebKitGTK 可能不同，这是本计划唯一的跨平台未知项）；证据 = 截图 + 日志。

- [ ] **Step 5: 写报告并提交**

报告结构：范围/结论摘要 → 门禁输出 → 逐条判据表（A1…F2）× 平台 → 未验证项与残留风险（照抄 spec §12 的 R 表口径，**不得**用「应该没问题」代替证据）→ 证据文件清单。
**证据必须入库（审核 F11）**：`.superpowers/` 在 `.gitignore` 里 ⇒ 截图/日志放 `docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-evidence/`，或把关键原始输出内联进报告正文；`git add` 时把证据目录一并加上。

```bash
git add docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-acceptance.md
git commit -m "docs(report): SFTP 刷新/持久化/连接自愈的 Linux+Windows 真机验收"
```

---

### 两阶段审核 · 第一阶段（作者自审，9 处已修）

| # | 严重度 | 问题 | 修正 |
|---|---|---|---|
| S1 | **Blocking** | T5 直接跑 `wails generate module`，但 `main.go:16` 是 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 gitignore ⇒ **干净克隆/CI 上生成必失败** | T5 新增 Step 1 产 dist。**二阶段审核 B1 进一步指出该前置放错了任务**（T2/T3/T4 就要跑 `go test ./`）⇒ 已上移到 Global Constraints 的 Task 0；T5 Step 1 改为「确认 dist 已存在」 |
| S2 | Important | T2 的"每 host 单飞"没写清除时机 ⇒ 首次探针后该 host 被永久标记"在飞"，batch 后端自愈静默失效 | 点名 `defer` 内清除 inflight 标记 |
| S3 | Important | T4 对 `AddBookmark` 只说"改新数组"不够：朴素 `append(a.cfg.Bookmarks, b)` 在 cap 有余量时**仍写共享底层数组** | 点名"构造新数组 + 拷旧 + 追加 + 整结构赋值"；并补"保持既有入参守卫与去重/上限语义" |
| S4 | Important | T13 的 `probeTimed` 契约自相矛盾（Interfaces 写"超时返回 false"，正文写返回 `'timeout'` 视为未知）⇒ 实现者会把超时当"链路失效"而误重连 | 改为三态 `'ok' \| 'lost' \| 'unknown'`，**只有 `'lost'` 触发重连**；测试补 `clearTimeout`、`'unknown'`、抛错不得归为 `'lost'` |
| S5 | Important | T13 断言 `syncConnection` 体内 `not.toContain('SftpHome')`，与 T11 的未连接分支（需要 `SftpHome` 取 home）冲突 ⇒ 断言必红 | 只裁 `if (connected.value) {…}` 已连接分支再断言 |
| S6 | Important | T13 的"纯远程函数体内不得出现裸 `err(e)`"与 `dispatchTransfer` 的取消分支（必须保留 `err(e)` 记原文）冲突 ⇒ 测试写不出来 | 改为**可达性口径**（catch 里可达 `remoteOpFailed(`/`verifyRemote(`），并同步修规格 §11.2 与 §16.1 的历史记录 |
| S7 | Minor | T10 的测试文件名 `contextMenu.test.js` 与仓库 `*Wiring.test.js` 约定不一致 | 改名 `contextMenuWiring.test.js` |
| S8 | Minor | T11 的 `function goRemoteHome[\s\S]{0,200}` 是跨函数边界的脆正则；另有一条只带注释的桩断言 | 改切片断言；桩断言写实（顺序 + 回落文案） |
| S9 | Minor | T12 的 `toMatch(/menu\.item|menuHasItem/)` 是空洞断言（`menu.item` 今天就已存在于 `doAction` 里） | 换成"具名判据 `menuHasItem` + 两个菜单分支各含一次刷新 + 行菜单仍含下载" |

### 两阶段审核 · 第二阶段（干净 subagent 双面审核：后端/构建 15 条 + 前端/验收 23 条，共 38 条，全部处置）

审核方式：两个**新上下文、不携带本轮对话背景**的只读 subagent，按模块拆包（后端/构建门禁面审 T1–T5 + Global Constraints；前端/验收面审 T6–T14 + Review Focus）；两者都被要求「不要相信计划里的行号，回当前工作区重新定位符号，行号漂移本身算 finding」。报告的关键事实由 Lead 逐条复核（读源码 + 一次 TOML 探针 + 隔离 `go vet` 实验）后才处置。**裁定：38 条全部接受**（其中 M5 按规格指定归属并入既有用例，属等价改写而非驳回）。

**两个 Blocking（照计划实施就必红/必失效）**

| ID | 问题 | 处置 |
|---|---|---|
| B1 | dist 前置放在 T5，而 T2/T3/T4 已经在跑 package main 的 `go test ./`；`main.go:16` 是 `//go:embed all:frontend/dist`、该目录被 gitignore ⇒ **干净克隆在 T2 就硬失败**（隔离实验 `go vet ./` → `pattern all:frontend/dist: no matching files found`，EXIT=1） | 提到 `## Global Constraints` 的 **Task 0**：`cd frontend && npm ci && npm run build`；T5 Step 1 改为「确认 dist 已存在」 |
| F1 | 规格 §8.3/§8.4 的代码块是 `if (!(await probeTimed(h)))`，而 `probeTimed` 返回三态字符串 ⇒ `!'lost'` 恒 false ⇒ **切标签自愈永不触发**（D1 挂、D2 可能照过） | 规格两处改 `=== 'lost'`；T13 断言补 `expect(connectedBranch).toMatch(/===\s*'lost'/)`；并同步 §11.2 的断言口径 |
| F2 | 空选择防御写成「除 refresh/mkdir 外一律 return」⇒ 空白菜单的「上传文件…」（`upload-picked`，天然无选中）点了永远 no-op；若放在 `sel.single` 之前，未选中行的整条行菜单也变 no-op | 规格与 T12 改为「放在 `sel.single` 与 `names` 之后、只约束 `['download','upload','remove','rename']`」，并补可判别断言 |

**Important（后端 6 条 / 前端 10 条）**

| ID | 问题 | 处置 |
|---|---|---|
| I1 | T4 的结构性 grep 命中「读」而非只命中「写」（§6.3.2 的 `LastRemoteDirs[host] != path`），且对 BF14 的「写锁内构造新 map」无判别力；helper 里 `map[host]=path` 在裸 `&AppConfig{}` 上会 panic | 判据改 `grep -nE 'SftpUI\.LastRemoteDirs\[[^]]*\]\s*='`（只匹配赋值）+ 断言 `make(map[string]string`；补 nil-map 写入用例 |
| I2 | 未写明 `rememberLast == false` 时 `lastChanged` 恒 false ⇒ 每个文件一次全量编码+rename（写放大回归） | 写成 `if rememberLast { lastChanged = a.rememberRemotePathLocked(...) }`；补「内部路径不额外写盘」用例 |
| I3 | 只有 `rememberLast` 参数、没有「只更 recents」的私有 helper：照字面实现会让回归用例必红，或在持写锁时调公开绑定 ⇒ **同锁重入死锁** | Interfaces 补 `addRemoteRecentLocked/addLocalRecentLocked(..., rememberLast)`；明写「锁内禁止调公开绑定」 |
| I4 | `fakeBackend.List` 既不阻塞、计数也无锁 ⇒ 造不出阻塞且 `-race` 会红；且「Conn 非 nil 的会话」需要 `net.Pipe + sftp.NewServer` 先例 | 新增专用 `blockingFakeBackend`（atomic + channel）；补会话构造先例说明 |
| I5 | 每 host 单飞「在飞即 false」把「未知」编码成「已死」⇒ batch 挂住时对健康链路做无谓 teardown（违反 D14） | 改为**共享在飞结果**（per-host `chan bool`，第二个调用者等同一次结果）；规格 §8.2 同步 |
| I6 | 规格 §11.1 两条用例无人承接：`SftpProbe` 未连接 host → false；`GetSftpUI` 的 nil-cfg 分支（被 `SaveSftpUI` 先造出 cfg 而永不执行） | T2 补 `TestSftpProbeFalseWhenListFails`；T3 用例顺序改为「先 `GetSftpUI()` 再 `SaveSftpUI`」 |
| F3 | `actionsFor` 在 script 里写 `disabled: !connected`（`connected` 是 ref，`!connected` 恒 false）⇒ 第三入口置灰静默失效，且前缀式断言照样绿 | 实现改 `!connected.value`；断言改 `!connected\.value` |
| F4 | T10 的 vitest 命令指向**不存在**的 `contextMenu.test.js`，而 vitest 过滤不到文件时退出码 0 ⇒ Step 2「Expected: FAIL」实际 PASS（假绿） | 两处命令改为 `contextMenuWiring.test.js` |
| F5 | T11 说 `loadRemote` 的 catch「err(e)，语义不变」，与 T13 落点 #2「纯远程 ⇒ remoteOpFailed」冲突 | T11 改述为「记原文 + 一次探活复核（本轮要的语义变更）」 |
| F6 | `recoverConnection` 把记忆值塞进优先级 1 的 `pendingPath` ⇒ `fromMemory=false` ⇒ §6.5 回落永不触发（R3 承诺失效），且 `connect()` 先置 `connected=true` ⇒ 会记「已重新连接」而面板停空 | 不写 `pendingPath`，让 `connect()` 在优先级 3 自取（`fromMemory=true`）；断言加 `not.toMatch(/pendingPath\.value\s*=\s*sftpui\.lastPathFor/)` |
| F7 | 规格定为 **warn** 的两条回落文案被计划用 `err()`（硬编码 error）打成 error；三条自愈文案（验收 D1 靠 grep）**没有任何逐字断言** | 新增 `warn()`/`info()` helper；断言改用 `warn(`；补三条文案逐字断言 |
| F8 | `sftpui.load()` 会 reject 却写成裸 `await` ⇒ 连接按钮 unhandled rejection；`onMounted` 里还会中断后续 `Cwd()`/`loadLocal()`（本地面板不加载） | `connect()` 用 `.catch(err)`；`onMounted` 用 try/catch；断言补兜底形态 |
| F9 | SftpView 把 `sftpui.sort.*` 灌进 `FilePane`、接 `@sort` 这「最后一米」**无断言**（漏接 ⇒ 点列头无反应、重启不恢复而测试仍绿） | T11 补 `@sort="onSort(` 命中 2 次 + 四条 `:sort-key`/`:sort-dir` 断言 |
| F10 | `loadRemote`（`:218`）不记 `addRemoteRecent`，只有 `connect`/`goRemote` 记 ⇒ **双击进目录/刷新永不写入远程最后路径**（本地侧 `loadLocal` 反倒记） | `loadRemote` 成功路径补记（store 有队首短路，零额外写盘）；规格 §6.3.1 调用点清单与 C2 手势同步 |
| F11 | 证据目录放 `.superpowers/`（被 gitignore），与规格「`make ci` 日志入库」冲突 ⇒ 验收不可复现 | 证据改放 `docs/superpowers/reports/2026-10-10-…-evidence/` 或内联；`git add` 一并加 |
| F12 | F1 验收要「探活耗时分布」，但没有任何地方产出该数据 ⇒ 判据不可观测 | 规格 §10 新增 info 级 `探活耗时 <N>ms`（前端 `probeTimed` 计时）；F1 判据改为汇总该日志 + 无「远端连接已失效」误判 |
| F13 | 「12 点覆盖」测试块**只有注释**（无可执行断言）；且 4 处清单外 catch 未声明（`connect` 内 `SftpHome` 静默、`syncConnection` 的 `SftpConnected`/`SftpHome`、`SearchOverlay` 的 `SftpSearch`、`StatPaths` 本地） | 换成真实断言（函数体切片 + `remoteOpFailed\(|verifyRemote\(`）；`connect` 注明不替换；`SearchOverlay` **纳入**（新增 `@failed="remoteOpFailed"`）；另 3 处写进规格 §8.4 排除清单并给理由 |
| F14 | `syncConnection` 的切片右界依赖一句**未被规定**的 `// 未连接` 注释（不存在）⇒ `slice(a,-1)` 把未连接分支一起纳入、以看不懂的方式变红 | 改花括号配对切片 |
| F15 | 断言把 `probing`/`recovering` 钉在「非 ref」上，但计划从未声明它们是普通 `let` | 断言补 `let probing = false` / `let recovering = false`，即显式规定形态 |
| F16 | 「空串 = 未指定」这条决定主流程（工具栏「连接」`pendingPath` 恒 `''`）的契约**无用例** | T6 补 `pendingPath: ''` 用例 |
| F17 | `resolveRemoteTarget({ pendingPath, pendingHome, ... })` 传的是 **ref 对象**（恒真值）⇒ 会把 ref 当路径交给 `SftpList`；纯函数单测用字面量测不出 | 明确写 `.value` |
| F18 | 规格 §11.2 的两条要求无任务承接：两面板 `@context` 接到 `showMenu`；`SaveSftpUI` 唯一出口 | 两条断言分别补进 `sftpMenuWiring` |
| F19 | `:disabled="[^"]*!connected[^"]*"` 只能证明「某处有 `!connected`」，钉不住 `remoteDep` 判据、也不覆盖每个远依赖项 | 断言改「`remoteDep` 代号 + `remoteDep && !connected` + 模板逐项」 |
| F20 | 「无 root 用 iptables」的替代理由不成立（两者都要特权）；Windows 侧 D1 断链手段未写 | 改为权限/内核支持口径；Windows 用 VirtualBox 停网卡或 `netsh interface … disable` |
| F21 | 传输在飞时切主机 ⇒ 旧主机的成功回调把 `connected` 置 true，UI 显示「已连接」但新主机未连 | 回同步带 host 守卫（`const h = host.value` + `host.value === h`）；补断言 |
| F22 | 「刷新出现 ≥2 次」钉不住菜单矩阵差异（空白含 mkdir/上传文件、行菜单不含 mkdir） | 改为按 `<ContextMenu>` 模板切片，逐分支断言内容差异 |
| F23 | `fetchRemote/fetchLocal` 的边界未写：若把 seq/loading/recent 也塞进去，`goRemote/goLocal` 的「旧响应丢弃 + 失败回滚」会被破坏 | 明确 `fetch*` 只做 `List*(target) || []` 并抛错 |

**Minor（后端 8 条 / 前端 5 条）**

| ID | 问题 | 处置 |
|---|---|---|
| M1 | T1 说 `normalize()`「现在只调 `c.App.Normalize()`」（事实不符：还归一 Syncs）；§6.1 强制的「键=host、不含 user」前提注释无人承接 | 修正描述；T1 Step 3 补前提注释要求 |
| M2 | T3 的 SyncRule 夹具若只设部分字段，`LoadConfig` 的归一会让用例因无关原因必红 | 夹具写全字段（或只比显式设置的字段） |
| M3 | T4 Step 2 的「race 用例报 DATA RACE」不可达（同包编译失败先发生） | 改述为「整个 package main 编译失败」 |
| M4 | T5 Step 4 的 `npm run build` 无判别力（此刻没有代码 import 新符号，旧绑定也能过） | 标注非判别性，真正判据是 Step 3 的 grep + T7/T14 |
| M5 | T3 新开独立用例，偏离规格指定的「并入 `TestSftpBindingsFailFastBeforeInit`」 | 按规格并入（等价改写） |
| M6 | `Pool.Probe :288-305`、`recordRecentSFTP :1394-1409`、`AddLocalRecent :1343-1366` 等 ±1~2 行漂移 | 校正并注明「以符号名为准」 |
| M7 | 测试里 `D:\工作\虚拟 共享` 写成字面量会因非法转义编译不过 | 明确用反引号 raw string 或双反斜杠 |
| M8 | 加锁只覆盖写方，`ListLocations()` 仍无锁读同字段 ⇒ 「4 个写入点并发安全」的宣称过头 | `ListLocations` 加 `cfgMu.RLock()`；宣称收窄为「写入点之间 + 与 ListLocations 读点之间」 |
| F18 已列 | — | — |
| F13/16/17 已列 | — | — |
| （前端 Minor 其余 2 条：F14/F15 已在 Important 段列明） | — | — |

**两个审核者共同的未验证边界**（保留，不得当成已验证）：未执行 `make ci`/`wails generate module`/真机 A1–F2（只读约束 + 会改仓库）；`models.ts` 对 `map[string]string` 字段的精确产物未生成核对；Windows/WebView2 的 contextmenu/`e.target` 语义未实测；`~/.cache/go-build` 只读的根因未验证（现象已实测）。

## 自查记录（写在计划末尾，供执行者核对）

- **Spec 覆盖**：§5 交互 → T9/T10/T12；§6 持久化 → T1/T3/T4/T7/T8/T11；§7 前端数据流 → T6/T7/T8/T9/T11；§8 自愈 → T2/T13；§9 接口契约 → T1/T2/T3/T5；§10 文案 → T11/T13（逐字）；§11 测试与验收 → 各任务 Step + T14；§12 风险 → Review Focus 5 条 + T14 报告；§6.3.4 的 bookmark 竞态（用户决定扩大范围）→ T4。
- **任务依赖**：T1→T3→T4；T2→T5；T1/T2→T5；T6→T7/T9；T5→T7→T8→T11→T12→T13；T9→T11；T1–T13→T14。
- **类型/命名一致性**（逐条对过）：`config.SftpUIState` 六字段 → T3/T4 复用；`Ctrl.Probe`/`SftpProbe` → T13 调 `SftpProbe`；`nextSort` → T9；`resolveRemoteTarget` → T11；store 的 `lastPathFor/setSort/loaded/load/noteLocalPath/noteRemotePath` → T8/T11/T13；`recordRecentSFTP(...,rememberLast)` → T4 测试同签名；`fetchRemote/fetchLocal` → T11 产出、T12/T13 复用；`probeTimed/verifyRemote/remoteOpFailed/recoverConnection` → 只在 T13 内闭环。
- **Step 自查发现并已修的一处缺口**：Spec §11.1 的两条用例（"只含 sort 的 `SaveSftpUI` 会清零路径"这一危险语义、`GetSftpUI ‖ SaveSftpUI` 并发）原先没有落到任何任务，已补进 T3 Step 1。
- **Review Focus 与测试的对应**：第 1 条→T1 Step 1 + T7 Step 1；第 2 条→T11 Step 1；第 3 条→T13 Step 1 + T12 Step 1；第 4 条→T13 Step 1；第 5 条→T1 Step 1（四个用例）+ T7 Step 1。
- **比例**（实测数字）：本计划 1118 行 / 14 任务 / 73 个步骤；规格 780 行 ⇒ 约 1.43×，属同一量级，没有"比规格长几倍"的抄写式膨胀。代码块基本是**签名 + 测试名/断言**；函数体只出现在规格固定了逐字文案或分支顺序的地方（右键 handler 分支、store 的 payload 键、探针 race 形状）。若执行者发现某步只是把实现抄了一遍，按"步骤只决定实现者无法独自决定的东西"删减。
- **未写进计划的东西**（刻意留给执行者的判断，不是遗漏）：函数体写法、CSS 细节、测试断言的实现手法（切片正则 vs SSR）、提交粒度内的行号微调。
- **环境前提**（实测过的，不是猜测）：`~/.cache/go-build` 只读 ⇒ 一律带 `GOCACHE=$PWD/.gocache`；`wails v2.15.0` 的 `generate module` 支持 `-tags`；前端可用的测试手段只有纯函数/store/SSR/读源码四种。

---

## 执行记录（SDD · Subagent-Driven）

- 分支 `feat/sftp-refresh-persist-connloss`；起点 `master` = 6169102，本计划完成时 HEAD = `538110b`。
- 逐任务 ledger（预检冲突扫描、每条裁定、fix 轮、deferred minor 清单）：`.superpowers/sdd/2026-10-10-sftp-refresh-persist-connloss/progress.md`（gitignore 内，属工作区记录）。
- 每任务 = 全新实现者（TDD，含 RED/GREEN 证据）→ review package → 全新复核者（判定 Spec/质量）→ 定点修复轮 → scoped re-review。**T2/T11/T12 各走了一轮 fix**；T1/T3/T11 的 trivial 修复由控制器直接落并留证。
- 执行期发现的**计划缺陷**：T13 Step 1 的测试正则少一个右括号（照抄即 `Invalid regular expression`）—— 已由实现者修正。
- **门禁**：`GOCACHE=$PWD/.gocache make ci` 退出码 0（build ✓ / `go vet` ✓ / 13 个包 `-race` ok / vitest 38 files·484 tests），完整日志见 `docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-evidence/make-ci.log`。
- **真机验收 A1–F2 未执行**：本环境无图形会话、无 GUI 输入自动化、VBox 内核模块缺失且 `sudo` 不可用。逐项清单与阻塞证据见验收报告 `docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-acceptance.md`。
- 与规格文本的偏离（6 处）记录在规格 §16.4。

### 提交序列（本计划）

```
9e20198 feat(config): 新增 [sftp_ui] 配置段（最后路径 + 双侧排序）与归一挂点
c885a09 test(config): 补 DefaultAppConfig 路径的 SftpUI 归一断言（T1 复核 Minor-1）
3ac1c97 feat(sftp): 真探活能力（修 Pool.Probe 拨号吃满预算的误判 + Ctrl.Probe/SftpProbe 绑定）
db04bb2 test(sftp): 补探活预算修复的坏会话用例与分派判别力（T2 复核 Important/Minor）
7658a39 feat(app): GetSftpUI/SaveSftpUI 绑定（锁内只赋值、解锁后归一与落盘）
e5b1888 test(app): 把未 Init 的 SaveSftpUI 用例落盘路径指到临时目录（T3 复核 Important：macOS 上会覆盖用户真实配置）
a0ed9f1 fix(app): 配置写入点并发改造（新数组+写锁）与 last path 写入规则（recordRecentSFTP 不再改最后路径）
ef3c824 chore(bindings): 重生成 Wails 绑定（GetSftpUI/SaveSftpUI/SftpProbe + SftpUIState 模型）
4999cbb feat(utils): nextSort 与 resolveRemoteTarget 纯函数（排序取反 + 落点优先级/D15 回归守卫）
2976aee feat(store): sftpui store（最后路径/双侧排序 + 读回合并的 saveSnapshot 与无变化短路）
07baa2d feat(store): locations 成功写最后路径后镜像到 sftpui（零新 IPC）
20d80bf refactor(filepane): 排序状态上收为 props + emit('sort')，补真实 SSR 渲染断言
fedcc2c feat(filepane): 空白/行右键分流（item:null）与 ContextMenu 禁用样式
c035806 feat(sftp): 启动恢复顺序与落点决策（记忆路径/主目录显式标志/失效回落），抽取可抛错的取列表函数
a970d22 fix(sftp): 修 Task 11 复核的 2 Important + 2 Minor
82fd960 feat(sftp): 工具栏/右键刷新入口与菜单分级矩阵、远程置灰判据
feff286 fix(sftp): 补空白菜单 upload-picked 分派，收紧刷新入口/动作分派断言（Task 12 复核）
fda2b89 feat(sftp): 连接失效自愈（三态探活 + 幂等重连 + 13 个远程落点归口）
d955146 fix(sftp): 收尾 triage（失败耗时不含探活/重连、记账失败不再误判为连接/导航失败、setSort 忽略非法 pane、注释与断言修正）
2231d43 docs(sftp): T14 验收报告（自动化门禁全绿 + 真机验收阻塞证据与可执行清单）
0fd6836 docs: 同步执行记录（规格 §16.4 执行期偏离 / 计划的 SDD 执行记录与提交序列）
538110b fix(sftp): last path 短路判据纳入内存镜像（§6.3.2 升级场景补写不再被拦）
```
