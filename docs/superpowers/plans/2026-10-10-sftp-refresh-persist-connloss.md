# SFTP 双侧刷新 / 右键菜单分级 / 状态持久化 / 连接失效自愈 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 SFTP 面板的刷新覆盖双侧、右键菜单按"有选中/无选中"分级、把每主机最后路径与双侧排序持久化，并在主机休眠/断链后自动探活自愈而不是"假装已连接"。

**Architecture:** 后端新增一个独立配置段 `[sftp_ui]` + 三个绑定（`GetSftpUI`/`SaveSftpUI`/`SftpProbe`），last path 只由"导航成功"触发的前端绑定写入，后端内部 recent 记录一律不写；探活复用既有 `Pool.Probe` 并修掉它的预算缺陷。前端把排序状态从 `FilePane` 上收到 `SftpView` + 新 store，纯逻辑（`nextSort`/`resolveRemoteTarget`）抽进 `utils/`，组件只做渲染与接线。

**Tech Stack:** Go 1.x + Wails v2.15.0（绑定 `wails generate module -tags webkit2_41`）、`internal/preset` 无关；Vue 3.5 + Pinia + Vitest 4（**无 jsdom / 无 @vue/test-utils**）、`@vue/server-renderer` 可做真实 SSR 渲染断言；配置为 BurntSushi/toml v1.6.0。

**Spec:** `docs/superpowers/specs/2026-10-10-sftp-refresh-persist-connloss-design.md`（v1.5；本计划逐节实现它，执行者必须两份一起读）

## Global Constraints

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

1. **主机名本身是配置 map 的键**（含点 `10.99.152.18`、空格、引号、中文）——若 TOML 转义或 JSON 键名映射错了，会导致配置写不进去或读到 `nil`，而界面上完全看不出来。测试落在 T1（TOML 往返）与 T7（payload 六个 snake_case 键逐字断言）。
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

`TestSftpUITOMLRoundTrip` 的键集合必须含：`"10.99.152.18"`、`"prod db"`、`"中文主机"`、`quote"host`、`host.with.dots`、`prod-db`；`LastLocalDir` 用 Windows 风格 `D:\工作\虚拟 共享`。判据：`Encode` 后 `Decode` 回来**逐字段相等**（只比解码后的值，不断言落盘字节形态），且 `[app]` 段等其它字段不受影响。

`TestLoadConfigNormalizesSftpUISection`：写一份临时 TOML，其中 `sort_local_key = "bogus"`、`sort_local_dir = 7`、`last_remote_dirs` 只给一个键 → `LoadConfig` 读回后 `SortLocalKey=="name"`、`SortLocalDir==1`。

`TestLoadConfigWithoutSftpUISection`：TOML 里完全没有 `[sftp_ui]` → 读回后 `SortLocalKey=="name"`、`SortLocalDir==1`、`LastRemoteDirs` 非 nil 且空、`LegacyMigrated==false`。

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./internal/config/ -run 'SftpUI|WithoutSftpUI' -v`
Expected: FAIL，编译错误 `undefined: SftpUIState`。

- [ ] **Step 3: 实现**

- 结构体与标签逐字照 Interfaces 块；`Normalize()` 规则：两个 key 走白名单 `name|size|modTime`（非法/空→`name`），两个 dir `<0`→`-1` 否则 `1`，`LastRemoteDirs==nil`→`map[string]string{}`。
- `AppConfig` 里把 `SftpUI` 放在 `RemoteRecent` 之后、`LegacyMigrated` 之前。
- **归一挂点**：`func (c *AppConfig) normalize()` 内新增 `c.SftpUI.Normalize()`（现在只调 `c.App.Normalize()`）—— 这是唯一挂点，`LoadConfig` 与 `DefaultAppConfig` 都经过它。

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
- Modify: `internal/sftp/session.go`（`Pool.Probe` `:288-305`）、`internal/sftp/ctrl.go`（新增方法，参考 `:118-130` 的类型断言先例）、`app.go`（绑定区，参考 `SftpConnected` `:983-988`）
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

`internal/sftp/ctrl_test.go`（沿用 `:658-712` 的 `fakeBackend` 与 `cancel_test.go:491` 的 pool 注入先例）：

```go
func TestCtrlProbeUsesPoolForGoBackend(t *testing.T)      // 强制后端为 *GoBackend ⇒ 走 pool.Probe（用注入的慢 dial 断言 true）
func TestCtrlProbeFallbackIsBoundedAndSingleFlight(t *testing.T)
// 用 NewCtrlForcedBackend(fakeBackend)（其 List 阻塞不返回）：
//   - Probe 在 100ms ctx 超时后必须返回 false（不得挂住）
//   - 同 host 并发第二次 Probe 立即返回 false（单飞），且只调用了一次 List
```

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
- `Ctrl.Probe`：`if g, ok := c.backend().(*GoBackend); ok { return g.Probe(...) }`，否则走 spec §8.2 的 goroutine + `ctx` select + **每 host 单飞**（`map[string]bool` + `sync.Mutex`，前一次未结束时直接 false，不重复起 goroutine）。
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
func TestSaveSftpUINormalizesInvalidSort(t *testing.T)
// 传 {SortLocalKey:"bogus", SortLocalDir:7, LastRemoteDirs:nil}
// 断言落盘后 SortLocalKey=="name"、SortLocalDir==1（LastRemoteDirs 允许 nil 或空 map，二选一写明）
func TestSaveSftpUIShrugsOffNilCfg(t *testing.T)
// NewApp()（a.cfg == nil）→ SaveSftpUI(...) 不得 panic；随后 GetSftpUI() 返回归一化零值
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
- Modify: `app.go`（`AddLocalRecent` `:1343-1366`、`AddRemoteRecent` `:1367-1392`、`AddBookmark` `:1312-1326`、`RemoveBookmark` `:1328-1341`、`recordRecentSFTP` `:1394-1409`、`cfgMu` 注释 `:37-42`）
- Test: `app_test.go`

**Interfaces:**
- Consumes: T1（`config.SftpUIState`）、T3（`GetSftpUI`）。
- Produces:
  ```go
  func (a *App) recordRecentSFTP(host, remoteDir, localDir string, rememberLast bool)  // 签名扩展
  // 内部辅助（私有，供上面四个函数复用；锁内只构造新值 + 赋值，不落盘）：
  func (a *App) rememberRemotePathLocked(host, path string) (changed bool)  // 调用方已持写锁
  func (a *App) rememberLocalPathLocked(path string) (changed bool)
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
Expected: FAIL（`LastRemoteDirs["prod"]` 为空；`recordRecentSFTP` 参数个数不符；race 用例在旧实现下报 `DATA RACE`）。

- [ ] **Step 3: 实现**

- **写入分工（规格 §6.3.1）**：只有 `AddLocalRecent`/`AddRemoteRecent` 写 last path；`recordRecentSFTP(..., false)`（`SftpHome` `:1127` 与传输完成 `:810/823/836/882`）一律不写。`recordRecentSFTP` 的内部调用点全部补 `false` 实参。
- **last path 不受队首短路约束（§6.3.2）**：先算 `lastChanged`，再走 recents 去重/短路；仅当"recents 无需变更（队首同值）**且** last path 无需变更"时 `return nil`（不写盘）；否则一次 `saveConfig()`。
- **四个函数统一并发改造（§6.3.4 第 3 条）**：`kept := a.cfg.X[:0]` 一律换成 `kept := make([]T, 0, len(a.cfg.X))`；去重/增删/（recents 两处的）last path 更新整段放进 `a.cfgMu.Lock()` … `Unlock()`，锁内只赋值，解锁后再 `saveConfig()`。
- `cfgMu` 注释（`:37-42`）扩写为"保护 `cfg.App` 与 `cfg.SftpUI`"，并注明四个写入函数的锁规约。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./ -run 'Recent|Bookmark|LastPath|ConfigWrites' -race -v`
Expected: PASS（含既有 `TestAddRecentHeadShortCircuit`、`TestAddRemoteRecentDedupesAndCaps` 不回归）。

- [ ] **Step 5: 源码结构性判据**

Run: `grep -n 'cfg\.[A-Za-z]*Recent\[:0\]\|SftpUI.LastRemoteDirs\[' app.go`
Expected: 无输出（证明没有原地复用 slice、没有原地写 map）。

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

- [ ] **Step 1: 重生成**

Run（仓库根）: `GOCACHE=$PWD/.gocache wails generate module -tags webkit2_41`
Expected: 退出码 0；`git status --short` 显示三个生成文件被改动。

- [ ] **Step 2: 确认生成物包含新符号**

Run: `grep -n 'GetSftpUI\|SaveSftpUI\|SftpProbe' frontend/wailsjs/go/main/App.d.ts frontend/wailsjs/go/main/App.js && grep -n 'class SftpUIState' frontend/wailsjs/go/models.ts`
Expected: 三个函数各命中 `.d.ts` 与 `.js`；`class SftpUIState` 命中一次。

- [ ] **Step 3: 验证前端构建**

Run: `cd frontend && npm run build`
Expected: 构建成功（Rollup 不报 "is not exported by wailsjs/go/main/App.js"）。

- [ ] **Step 4: 提交**

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
const render = (props) => renderToString(createSSRApp({ render: () => h(FilePane, { items, ...props }) }))
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
- Test: `frontend/src/components/filePaneWiring.test.js`、新建 `frontend/src/components/contextMenu.test.js`

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
`contextMenu.test.js`：`expect(css).toMatch(/button:disabled/)` 且含 `color: var(--text-faint)`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/components/contextMenu.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `FilePane.vue`：`.pane` 根加 `@contextmenu="onPaneContext"`（**不带 `.prevent`**）；`onPaneContext` 分支顺序照 spec §5.2 代码块（① 可编辑放行 ② `.pos-wrap` → `closePos()` + `preventDefault()` + return ③ 任何 `li` → return ④ `preventDefault()` + emit `item: null`）。`..` 行加 `@contextmenu.prevent`（emit `item: null`）。
- 注释写明三件事：为什么不带 `.prevent`（保住路径栏/过滤框的原生复制粘贴）、为什么 `li` 必须无条件 yield（`.prevent` 不 stopPropagation ⇒ `..` 行会双发射）、loading 期间 `.list.busy{pointer-events:none}` 会让行右键落到面板级（已知边界）。
- `ContextMenu.vue`：补 `button:disabled` 与 `button:disabled:hover` 两条规则（照 spec §5.4）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/components/contextMenu.test.js`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/FilePane.vue frontend/src/components/ContextMenu.vue frontend/src/components/filePaneWiring.test.js frontend/src/components/contextMenu.test.js
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
it('本地路径用记忆值优先、失败回落 Cwd', () => { /* 断言 lastPathFor('local') 出现在 Cwd 之前；catch 分支写 Cwd */ })
it('connect/syncConnection 共用 resolveRemoteTarget，且 pendingHome 是显式标志', () => {
  expect(src.match(/resolveRemoteTarget\(/g).length).toBeGreaterThanOrEqual(2)
  expect(src).toContain('pendingHome')
  expect(src).not.toMatch(/function goRemoteHome[\s\S]{0,200}pendingPath\.value = ''/)   // 不再用空 pendingPath 表达 home
})
it('connect 被 sftpui.loaded 兜住', () => expect(src).toMatch(/if \(!sftpui\.loaded\) await sftpui\.load\(\)/))
it('记忆路径失效回落 home 只在 fromMemory 时发生', () => {
  const fn = src.match(/async function connect\(\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(fn).toContain('fromMemory')
  expect(fn).toContain("err('上次的远程目录")
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

- 抽 `fetchRemote(target)`/`fetchLocal(target)`（**抛错**）；`loadRemote`/`loadLocal` 包一层 `try/catch → err(e)`，**外部语义不变**（seq 守卫、loading、`addRecent` 全部保留）。
- `onMounted` 顺序照 spec §6.4 B3；`localPath` 先取 `sftpui.lastPathFor('local')`，为空或打开失败才回落 `await Cwd()`（回落时写 warn 文案，逐字取 spec §10）。
- `connect()` 首部 `if (!sftpui.loaded) await sftpui.load()`；落点用 `resolveRemoteTarget({ pendingPath, pendingHome, remembered: sftpui.lastPathFor('remote', h), home })`；若 `fromMemory === true` 且首列失败 ⇒ 回落 `home` 再列一次 + warn 文案；`pendingPath`/`pendingHome` 消费后清空（含失败分支）。
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
it('空选择防御存在', () => expect(src).toMatch(/\/\/ 空选择|name !== 'refresh' && name !== 'mkdir' && !names\.length/))
it('置灰接线：远依赖动作在未连接时 disabled', () => {
  expect(src).toMatch(/:disabled="[^"]*!connected[^"]*"/)
  const af = src.match(/function actionsFor\(pane\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(af).toMatch(/upload-picked'[^}]*disabled: !connected/)
})
it('菜单模板含刷新项与分隔', () => {
  expect(src).toContain("doAction('refresh')")
  expect(src).toMatch(/menu\.item|menuHasItem/)                  // 空白/行两种菜单形态
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/sftpMenuWiring.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `refreshAll()`/`refreshPane(pane)` 照 spec §5.1；工具栏按钮改 `@click="refreshAll"` 并去掉 `:disabled`。
- `doAction`：顶部先 `if (name === 'refresh') { closeMenu(); return refreshPane(pane) }`（必须在 `sel.single` 之前），并加空选择防御（`refresh`/`mkdir` 除外）。
- `showMenu` 支持 `item: null`；`ContextMenu` 模板按 §5.3 矩阵渲染：面板级三项（🔄 刷新 / 📁 新建文件夹 / ⬆ 上传文件…）在空白菜单；选中项动作在行菜单；用 `remoteDep && !connected` 判据置灰（`remoteDep` 含本地面板的上传类动作）。
- `actionsFor('local')` 的 `upload`/`upload-picked` 两项加 `disabled: !connected`（第三个入口）。
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
  async function probeTimed(host)        // Promise.race([SftpProbe(host), 计时器(15s)])；超时返回 false 且不抛
  async function verifyRemote()          // connected===false 时早退；否则 probeTimed → 假 ⇒ recoverConnection
  async function remoteOpFailed(e)       // err(e) + verifyRemote()
  async function recoverConnection(host) // recovering 幂等；SftpDisconnect → pendingPath=记忆值 → connect()
  function onProbeTimeout()              // 只复位 probing（不改状态、不重连、不记 error）
  ```

- [ ] **Step 1: 写失败测试**

```js
it('syncConnection 已连接时也探活（经 probeTimed），且不重置路径', () => {
  const fn = src.match(/async function syncConnection\(\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(fn).toContain('probeTimed(')
  expect(fn).not.toContain('SftpHome')
})
it('recoverConnection：SftpDisconnect → pendingPath → connect 的顺序 + recovering 幂等', () => {
  const fn = src.match(/async function recoverConnection\([\s\S]*?\n\}/)[0]
  expect(fn.indexOf('recovering = true')).toBeLessThan(fn.indexOf('SftpDisconnect('))
  expect(fn.indexOf('SftpDisconnect(')).toBeLessThan(fn.indexOf('connect()'))
  expect(fn).toContain("sftpui.lastPathFor('remote'")
})
it('只重连一次：recoverConnection 体内无 while/定时器', () => {
  const fn = src.match(/async function recoverConnection\([\s\S]*?\n\}/)[0]
  expect(fn).not.toMatch(/while\s*\(|setInterval/)
})
it('probeTimed 超时复位 probing（finally）且超时不改状态', () => {
  const fn = src.match(/async function probeTimed\([\s\S]*?\n\}/)[0]
  expect(fn).toMatch(/Promise\.race/)
  const vf = src.match(/async function verifyRemote\(\)\s*\{([\s\S]*?)\n\}/)[1]
  expect(vf).toMatch(/finally\s*\{[^}]*probing = false/)
  expect(vf).toMatch(/!connected\.value[\s\S]{0,40}return/)
})
it('12 个远程落点覆盖（分类型口径）', () => {
  const pure = ['dispatchTransfer', 'loadRemote', 'goRemote', 'goRemoteHome', 'uploadPicked']
  const mixed = ['removeSelected', 'renameItem', 'mkdirIn', 'onMoveDrop']
  // 纯远程函数体（正则切到函数结束）内不得出现裸 err(e)，且必须出现 remoteOpFailed(
  // 混合面板函数体必须同时出现 remoteOpFailed( 与 pane === 'local' 的 err(e) 分支
  // cleanItem 必须按 rec.direction 分派
  // onMoveDrop 的 SftpList( 必须处于 try 内部（indexOf('try {') < indexOf('SftpList(')）
})
it('远程操作成功后回同步 connected=true', () => {
  const fn = src.match(/async function dispatchTransfer\([\s\S]*?\n\}/)[0]
  expect(fn).toMatch(/if \(!connected\.value\) connected\.value = true/)
})
it('取消中的失败跳过探活', () => expect(src).toMatch(/cancelRequested[\s\S]{0,80}remoteOpFailed|rec\.cancelRequested/))
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/sftpRecoverWiring.test.js`
Expected: FAIL。

- [ ] **Step 3: 实现**

- `probeTimed(host)`：`Promise.race([SftpProbe(host), new Promise(r => setTimeout(() => r('timeout'), 15000))])`；`'timeout'` 视为**未知**（调用方不重连、不记 error）；`setTimeout` 句柄要 `clearTimeout`。
- `verifyRemote()`：`if (!h || !connected.value || probing) return`；`probing = true` … `finally { probing = false }`；`await probeTimed(h)` 为假 ⇒ `recoverConnection(h)`。
- `recoverConnection(h)`：入口 `if (recovering) return`；按 spec §8.3 步骤 1–7；成功/失败判据 = `connected.value`；`host.value !== h` 时静默 return（不记"重连失败"）。
- `remoteOpFailed(e)` = `err(e)` + `await verifyRemote()`；**按 spec §8.4 的 12 项清单**逐个替换（含把 `onMoveDrop` 的 `targetItems` 加载与 `askConflict` 挪进 `try`、`cleanItem` 按 `direction` 分派）；`dispatchTransfer` 在 `rec.cancelRequested` 时跳过探活。
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

Run: `GOCACHE=$PWD/.gocache make ci`
Expected: 四步全绿；把完整输出保存到报告的证据目录（例如 `.superpowers/sftp-refresh-2026-10-10/make-ci.log`）。

- [ ] **Step 2: Linux 真机验收 A/B/C 组**

按 spec §11.3 的 A1/A2/B1/B2/C1/C2/C3/C4/E1/E2 逐条执行；每条记录"命令/操作 + 可观测判据 + 原始证据"（截图路径或日志原文）。**C4 必须做到"重启后仍落 `/var/log`"**，这是 BF1 那个静默数据损坏的端到端回归。

- [ ] **Step 3: Linux 真机验收 D/F 组（断链自愈）**

按 spec §11.3 的 D1/D2/D3/F1/F2 执行：`ss -K dst <host> dport = :22`（无 root 用 `iptables -A OUTPUT -p tcp --dport 22 -j DROP` 再删规则）；D1 必须给出日志原文（含时间戳）与自愈前后截图，F1 必须给出探活耗时分布，F2 跑 `SSHORE_SFTP_TRANSPORT=batch`。

- [ ] **Step 4: Windows 客户机验收**

在 VirtualBox Windows 客户机上重跑 A1/A2/B1/B2/C1/C4/D1（WebView2 的 contextmenu/`e.target` 语义与 WebKitGTK 可能不同，这是本计划唯一的跨平台未知项）；证据 = 截图 + 日志。

- [ ] **Step 5: 写报告并提交**

报告结构：范围/结论摘要 → 门禁输出 → 逐条判据表（A1…F2）× 平台 → 未验证项与残留风险（照抄 spec §12 的 R 表口径，**不得**用"应该没问题"代替证据）→ 证据文件清单。

```bash
git add docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-acceptance.md
git commit -m "docs(report): SFTP 刷新/持久化/连接自愈的 Linux+Windows 真机验收"
```

---

## 自查记录（写在计划末尾，供执行者核对）

- **Spec 覆盖**：§5 交互 → T9/T10/T12；§6 持久化 → T1/T3/T4/T7/T8/T11；§7 前端数据流 → T6/T7/T8/T9/T11；§8 自愈 → T2/T13；§9 接口契约 → T1/T2/T3/T5；§10 文案 → T11/T13（逐字）；§11 测试与验收 → 各任务 Step + T14；§12 风险 → Review Focus 5 条 + T14 报告；§6.3.4 的 bookmark 竞态（用户决定扩大范围）→ T4。
- **任务依赖**：T1→T3→T4；T2→T5；T1/T2→T5；T6→T7/T9；T5→T7→T8→T11→T12→T13；T9→T11；T1–T13→T14。
- **类型/命名一致性**（逐条对过）：`config.SftpUIState` 六字段 → T3/T4 复用；`Ctrl.Probe`/`SftpProbe` → T13 调 `SftpProbe`；`nextSort` → T9；`resolveRemoteTarget` → T11；store 的 `lastPathFor/setSort/loaded/load/noteLocalPath/noteRemotePath` → T8/T11/T13；`recordRecentSFTP(...,rememberLast)` → T4 测试同签名；`fetchRemote/fetchLocal` → T11 产出、T12/T13 复用；`probeTimed/verifyRemote/remoteOpFailed/recoverConnection` → 只在 T13 内闭环。
- **Step 自查发现并已修的一处缺口**：Spec §11.1 的两条用例（"只含 sort 的 `SaveSftpUI` 会清零路径"这一危险语义、`GetSftpUI ‖ SaveSftpUI` 并发）原先没有落到任何任务，已补进 T3 Step 1。
- **Review Focus 与测试的对应**：第 1 条→T1 Step 1 + T7 Step 1；第 2 条→T11 Step 1；第 3 条→T13 Step 1 + T12 Step 1；第 4 条→T13 Step 1；第 5 条→T1 Step 1（四个用例）+ T7 Step 1。
- **比例**：本计划约 630 行，规格 729 行 —— 未出现"计划比规格还长"的抄写式膨胀；函数体只在规格固定了逐字文案/分支顺序处出现。
- **未写进计划的东西**（刻意留给执行者的判断，不是遗漏）：函数体写法、CSS 细节、测试断言的实现手法（切片正则 vs SSR）、提交粒度内的行号微调。
- **环境前提**（实测过的，不是猜测）：`~/.cache/go-build` 只读 ⇒ 一律带 `GOCACHE=$PWD/.gocache`；`wails v2.15.0` 的 `generate module` 支持 `-tags`；前端可用的测试手段只有纯函数/store/SSR/读源码四种。
