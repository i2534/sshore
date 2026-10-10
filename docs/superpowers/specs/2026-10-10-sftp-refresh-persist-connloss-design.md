# SFTP：双侧刷新 + 右键菜单分级 + 最后路径/排序持久化 + 连接失效自愈 设计

> 状态：v1.4 · **用户已评审通过**（2026-10-10，选定「B. 通过，并把 `AddBookmark`/`RemoveBookmark` 的同类竞态本轮一并修」⇒ 范围扩大见 §15 D21）· 两阶段审核已完成并逐条处置（第一阶段作者自审 9 项 + 第二阶段干净 subagent 双面审核 33 项，见 §16）· 下一步：由 `writing-plans` 出实施计划 · 尚未实施
> 关联：`docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`（选择/批量/拖拽/位置基线，本轮遵其决策 5/6/32）、`docs/superpowers/specs/2026-09-18-sftp-gosftp-transport-design.md`（gosftp 底座与粘性 Connected 的由来）、`docs/superpowers/specs/2026-09-24-sftp-pane-layout-fixes.md`（面板排版基线）
> 路径判定：**架构级**（配置结构变更 + 新增后端绑定 + 前端交互模型调整 + 连接状态机自愈）
> 证据来源：§1 的代码事实均带 `file:line`，本次逐条读过源码后记录（非引用旧文档）
> 行号口径：所有行号以本规格写作时的工作区版本（`git rev-parse HEAD` = 71e7498）为准；实施时若行号已漂移，按**函数名/符号名**定位，不得按数字硬找

---

## 1. 背景与现状（代码事实）

| 位置 | 事实 |
|---|---|
| `frontend/src/views/SftpView.vue:989` | 工具栏只有一个刷新入口：`<button @click="loadRemote" :disabled="!connected">刷新</button>` —— **只刷远程**，`!connected` 时置灰；本地面板无任何刷新入口 |
| `frontend/src/views/SftpView.vue:1028-1042` | 唯一的 `ContextMenu` 实例，按 `menu.pane` 切两组模板；菜单项为 下载/上传/重命名/删除/新建文件夹，**没有刷新** |
| `frontend/src/components/FilePane.vue:230`、`:241` | `contextmenu` 只挂在文件名行（`li`，`:241`）；`<ul class="list" @click.self="onListBlankClick">`（`:230`）与 `.pane` 根都没有 `contextmenu` ⇒ **右键空白处不弹任何菜单**（无选中时目前没有菜单） |
| `frontend/src/views/SftpView.vue:434` | `showMenu(pane, { item, event })` 恒带 `item`；`menu.item` 无 null 形态（`:176`） |
| `frontend/src/components/FilePane.vue:124-136` | 排序状态（`sortKey`/`sortDir`）是 `FilePane` 内部 `ref`，**只活在会话内**，切标签（KeepAlive 缓存）尚可保留、**重启即丢**；排序键仅 `name`/`size`/`modTime` |
| `frontend/src/stores/locations.js:43-57` | 已有 `addLocalRecent(path)` / `addRemoteRecent(host, path)`，各自带"队首同值即短路"；后端写盘由 `app.go:1343` / `app.go:1367` 负责（各自去重置顶、全局上限 20） |
| `app.go:1367-1392` | `AddRemoteRecent` 是"最近列表"语义（全局 20 条、跨主机共享），**不是**"每主机一条最后路径"：任一主机可能被别的导航挤出 |
| `app.go:1367`、`app.go:1343` | 每次成功导航都会调用这两个绑定之一 ⇒ 导航已经是"记录位置"的天然挂点 |
| `internal/config/store.go:198-208` | `AppConfig` 现有字段块（`App`/`Tunnels`/`RecentSFTP`(只读兼容)/`Syncs`/`Bookmarks`/`LocalRecent`/`RemoteRecent`/`LegacyMigrated`），**没有** SFTP UI 状态段 |
| `app.go:649-668` | `snapshotConfig()` 在 `cfgMu` 读锁下**浅拷贝** `AppConfig` 后交给锁外的 TOML 编码；`saveConfig()` 用该快照落盘。⇒ 任何含 map/slice 的字段都不得原地修改 |
| `app.go:983-988` | `SftpConnected(host)` 直接转发 `Ctrl.Connected`；无"真探活"能力 |
| `internal/sftp/gosftp.go:1491-1494` | `Connected` 是**粘性连接意图**（注释明写"刻意不看池里是否有活会话"）：一次成功握手置位，**只有显式 `Disconnect`/`CloseAll` 清除** |
| `internal/sftp/gosftp.go:1430-1445` | `Mkdir` 失败即 `reusable=false` + `&TransferError{Op:"sftp mkdir", …, RemoteMsg: s.Proc.StderrText()}`；`Error()` 拼接为 `sftp mkdir <path>: <stderr>`（与用户日志逐字一致） |
| `internal/sftp/session.go:104` | `Pool` 的会话池：传输并发 1，**idle 上限 2/键（LRU）** |
| `internal/sftp/session.go:288-305` | **已存在** `Pool.Probe(ctx, host, user) bool`：真实 `ReadDirContext(".")` 往返、`probeTimeout = 5s`（`:25`）、失败即 `Release(s,false)` 丢弃会话 |
| `internal/sftp/ctrl.go:92-96` | `Ctrl` 只有 `Connect`/`Connected`/`Disconnect`，**没有** `Probe` ⇒ 前端拿不到探活能力 |
| `internal/sftp/backend.go:19` | `defaultTransport = KindGo`（gosftp 为内置默认）；batch 为 legacy 回退（`Connected` 靠 ControlMaster socket 存在性，`internal/sftp/batch.go:189-191`） |
| `frontend/src/views/SftpView.vue:900-911` | `syncConnection()` 首行 `if (!h || connected.value) return`：**已连接时永不复核** ⇒ 休眠唤醒后"假已连接"不会被纠正 |
| `frontend/src/views/SftpView.vue:238-257` | `connect()` 落点：`pendingPath`（来自"位置"下拉）优先，否则 `SftpHome(h)`；成功后 `addRemoteRecent` |
| `frontend/src/views/SftpView.vue:218-228`、`:394-404` | `loadRemote()`/`loadLocal()` 只改列表与 loading，**不清选中**（⇒ 刷新天然保留选中） |
| `frontend/wailsjs/go/models.ts:3-34` | 前端模型 `AppSettings` 由 `wails generate module` 生成并入库；新增后端结构必须重生成 |
| `Makefile:49,57,63` | 所有构建都带 `-skipbindings`（Linux 目标带 `-tags webkit2_41`）⇒ 新增绑定后**必须显式**跑 `wails generate module -tags webkit2_41`，否则 `npm run build` 在 Rollup 阶段失败（`2026-09-18` spec §「工具链提醒」已记载踩坑） |

### 1.1 用户报告的缺陷（本次一并修）

```
2026-10-10 10:10:26 [ERROR] Error: sftp mkdir /home/lan/virtual/share/dll: client_loop: ssh_packet_write_poll: Connection to 10.99.152.18 port 22: Unknown error
2026-10-10 10:10:32 [ERROR] Error: sftp ls /home/lan/virtual/share: client_loop: send disconnect: Connection reset
```

现象：主机休眠 → 唤醒后 UI 仍显示"已连接"，但操作报错。根因由 §8.1 给出（不是猜测：见上表 `gosftp.go:1491`、`session.go:104`、`session.go:288` 三条事实的组合）。

---

## 2. 目标

1. **刷新**：工具栏刷新 = 双侧刷新；两侧右键菜单各加「刷新」（刷右键所在面板）；未连接时远程侧入口置灰、工具栏仍刷本地半边。
2. **右键菜单分级**：右键空白 = 面板级动作（刷新、新建文件夹、上传文件…）；右键文件行 = 选中项动作（下载/上传/重命名/删除）+ 刷新。
3. **最后路径持久化**：远程**每主机一条**最后路径 + 本地一条全局最后路径，重启后恢复。
4. **排序状态持久化**：本地面板一份 + 远程面板一份（排序键 + 升降序），重启后恢复。
5. **连接失效自愈**：休眠/网络中断后不再"假已连接"；切回标签主动探活，失效则自动重连一次并回到该主机最后路径。

## 3. 非目标

- **不做**定时心跳轮询、不做后台常驻健康检查（只在"切回标签"与"远程操作失败"两个时机探活）。
- **不做**"自动重试那次失败的操作"：链路恢复后由用户手动重做（避免自动重放删除/覆盖类动作）。
- **不保存**「显示隐藏文件」开关与过滤词（前者是全局开关、后者是会话内临时态）。
- **不做**每主机各自的排序（排序按面板分：本地一份 + 远程一份，所有主机共用）。
- **不改** `remote_recent`/`local_recent` 的 20 条上限与"最近位置"下拉语义。
- **不改** `Connected` 的粘性语义与 `SftpConnected` 绑定的对外行为（`syncConnection` 的未连接分支仍用它校正）。
- **不改**传输队列、冲突策略、深搜、拖拽、过滤/选择的既有语义。
- **不动** forward/sync 模块的重连逻辑（本轮只碰 SFTP 视图与 SFTP 后端探活）。

## 4. 已确认决策

| # | 决策点 | 结论 | 来源 |
|---|---|---|---|
| D1 | 工具栏刷新范围 | 双侧刷新（本地恒刷；远程已连接才刷） | 用户确认 |
| D2 | 右键「刷新」范围 | 只刷右键所在面板 | 用户确认 |
| D3 | 远程未连接时的刷新 | 远程侧刷新/新建文件夹/上传文件…**置灰**；工具栏刷双侧时本地照刷、远程跳过 | 用户确认 |
| D4 | 菜单分级依据 | **按右键落点**：空白 → 仅面板级动作；文件行 → 选中项动作 + 刷新；空白右键**不改动**当前选中（只有左键点空白才清空，沿用现状） | 用户确认（选项 A） |
| D5 | 「..」行的右键 | 视为**无选中**（面板级菜单）：对 `..` 做重命名/删除无意义 | 本轮设计 |
| D6 | 行菜单是否保留「新建文件夹」 | **不保留**（面板级动作归空白菜单）；如后续想要可一行加回 | 本轮推荐（用户未反对） |
| D7 | 最后路径范围 | 远程每主机一条 + 本地全局一条 | 用户确认 |
| D8 | 排序粒度 | 本地一份 + 远程一份 | 用户确认 |
| D9 | 状态存储位置 | 新增独立配置段 `[sftp_ui]` + `GetSftpUI`/`SaveSftpUI` 绑定；**不进** `AppSettings` | 用户确认（方案 1） |
| D10 | 最后路径的落盘时机 | 随既有 `AddRemoteRecent`/`AddLocalRecent` 在**同一次** `saveConfig` 里更新 ⇒ 每次导航仍然只写一次盘 | 本轮设计 |
| D11 | 排序落盘时机 | 用户点列头即写（值未变则短路不写）；**不节流**（避免"关窗丢最后一次"的不确定性） | 本轮推荐（用户未反对） |
| D12 | 记住的路径失效时 | 远程列不出目录 → 回落 `SftpHome`；本地打不开 → 回落 `Cwd()`；各记一条日志 | 本轮设计 |
| D13 | 唤醒后自愈强度 | 切回标签探活；失效 → 清连接意图 → **自动重连一次**（回到该主机最后路径）；再失败则停在"未连接" | 用户确认（选项 B） |
| D14 | 失效判定手段 | **不解析 stderr 字符串**：失败后用 `SftpProbe` 复核，探针说失效才算失效 | 本轮设计 |
| D15 | 「主目录」的落点优先级 | 「主目录」= **显式 `pendingHome` 标志**，优先于"记忆路径"；不得再用"空 `pendingPath` = home"的隐含约定 | 本轮自审修正（§6.4） |
| D16 | 手动「断开」→「连接」的落点 | 也回到该主机最后路径（**行为变更**：今天回 home） | 本轮设计（D7 的必然后果，§6.4 B1） |
| D17 | last path 的写入权 | **只有"前端面向的导航绑定"能写**；后端内部 recent 记录（传输完成 4 处 + `SftpHome`）一律 `rememberLast=false` | 二阶段审核 BF1（Blocking 回归） |
| D18 | `SaveSftpUI` 的调用形态 | 前端先 `GetSftpUI()` 读回、**只覆盖 sort 四字段**再整结构写回；payload 六个 snake_case 键逐字写出 | 二阶段审核 BF2/FF9 |
| D19 | 探活的两层上界 | 后端 `Pool.Probe` 修预算缺陷 + legacy batch 走 ctx runner；前端 `probeTimed` 客户端超时（超时=未知，`finally` 复位 `probing`） | 二阶段审核 BF5/BF6 |
| D20 | 落点决策单一实现 | `connect()` 与 `syncConnection()` 共用 `utils/remotepath.js` 的 `resolveRemoteTarget` | 二阶段审核 FF8 |
| D21 | 范围扩大 | `AddBookmark`/`RemoveBookmark` 的同类 slice 竞态**并入本轮**（用户 2026-10-10 在规格评审时选定"B. 通过，并一并修"）；对外行为与去重语义不变 | 用户决定 |

---

## 5. 交互设计

### 5.1 刷新

```
工具栏 [🔄 刷新]  ──► refreshAll()   = loadLocal() ⊕ (connected ? loadRemote() : skip)
右键菜单 [🔄 刷新] ──► refreshPane(pane) = 本地面板 loadLocal() / 远程面板 (connected ? loadRemote() : skip)
```

- 工具栏按钮去掉 `:disabled="!connected"`（本地半边总能刷）。**审核 FF4**：这条与 §11.2 的接线断言会互相打架（本地面板菜单项也要用同一个表达式）⇒ 测试断言必须**限定在刷新按钮标签内**（先 `match(/<button[^>]*refreshAll[^>]*>/)` 再断言其中无 disabled），不得用文件级 `not.toContain(':disabled="!connected"')`。
- `refreshAll` 用 `Promise.all` 并发两侧；两侧各自的 `localSeq`/`remoteSeq`（`SftpView.vue:205`、`:263`）继续负责丢弃过期响应，双击不会串。
- **刷新保留选中**：`loadRemote`/`loadLocal` 本来就不清选中（`SftpView.vue:218-228`、`:394-404`），本轮不改。
- **刷新必须绕过 `doAction` 的选择改写（审核 FF6）**：行菜单的「🔄 刷新」若走 `doAction('refresh')`，会先执行 `:684` 的 `if (it && … && !sel.isSelected(s, it.name)) sel.single(s, it.name)` —— 多选被悄悄收成一项，直接违反本条。实现要求：`doAction` **顶部**先处理
  ```js
  if (name === 'refresh') { closeMenu(); return refreshPane(pane) }
  ```
  即 refresh 分支必须位于 `sel.single` **之前**（接线测试用 `indexOf` 钉顺序，与 `sftpDropWiring.test.js` 的既有手法一致）。
- 刷新失败按 §8 走 `remoteOpFailed` 复核（若确为链路失效则自愈）。

### 5.2 右键触发面（`FilePane.vue`）

- `.pane` 根新增 `@contextmenu="onPaneContext"`，**刻意不带 `.prevent`**，在 handler 内决定是否 `preventDefault()`：

```js
function onPaneContext(e) {
  const t = e.target
  // 1) 可编辑控件放行原生菜单（保住路径栏/过滤框的复制粘贴）。
  //    contenteditable="false" 必须排除（审核 FF19：否则它也会被当成可编辑）
  if (t.closest && t.closest('input, textarea, [contenteditable]:not([contenteditable="false"])')) return
  // 2) 位置弹层（.pos-wrap）内右键：先收起弹层，且不弹我们的菜单
  //    （审核 FF7：否则 .posmenu 与 ContextMenu 两个 z-index:1000 浮层同屏压在一起）
  if (t.closest && t.closest('.pos-wrap')) { closePos(); e.preventDefault(); return }
  // 3) 任何 li（含 '..' 行）都由行自己的 @contextmenu.prevent 处理器负责 —— 必须无条件 yield，
  //    否则 '..' 行会「行处理器 + 面板处理器」各 emit 一次 = 双菜单（自审修正 1）
  if (t.closest && t.closest('li')) return
  e.preventDefault()
  emit('context', { item: null, event: e })
}
```

- 行右键沿用既有 `@contextmenu.prevent="emit('context', { item: it, event: $event })"`（`FilePane.vue:241`）不变；`..` 行（`FilePane.vue:231-233`）**新增** `@contextmenu.prevent="emit('context', { item: null, event: $event })"`（按无选中处理），并靠上面的"li 无条件 yield"避免双发射。
- `FilePane.vue:29` 的 `emits` 数组已含 `'context'`，无需新增事件名。
- **落点归类补充（审核 FF7）**：`.hdrow`（列头）与 `.fbar`（过滤栏）背景落在"面板级"⇒ 弹面板级菜单（见 §5.3 矩阵），这是有意的；`.posmenu` 内部按上面第 2 条处理。
- **已知边界（审核 FF19）**：loading 期间 `.list.busy { pointer-events: none }`（`FilePane.vue:310`）会让行上的右键落到 `.list-holder`/`.pane` ⇒ 命中"面板级"而不是行级。该语义写进实现注释（不额外修，因为 loading 期间行本来就不可交互）；若实机觉得别扭，再改成 loading 时整体不弹菜单。

### 5.3 菜单矩阵与置灰

| 右键位置 | 菜单项（按此顺序） |
|---|---|
| 本地面板 · 空白 | 🔄 刷新 · 📁 新建文件夹 · ⬆ 上传文件… |
| 本地面板 · 文件行 | ⬆ 上传到远程 (N) · ✏ 重命名 · 🗑 删除 (N) · ── · 🔄 刷新 |
| 远程面板 · 空白 | 🔄 刷新 · 📁 新建文件夹 · ⬆ 上传文件… |
| 远程面板 · 文件行 | ⬇ 下载…(N) · ⬆ 上传… · ✏ 重命名 · 🗑 删除 (N) · ── · 🔄 刷新 |
| 列头 / 过滤栏背景 | 同"空白"（面板级菜单） |

- **置灰的唯一判据是"该动作是否需要远程连接"**（不是"哪个面板"）：
  ```js
  const remoteDep = (menu.pane === 'remote') || name === 'upload' || name === 'upload-picked'
  :disabled="remoteDep && !connected"
  ```
  远程面板的 🔄 刷新 / 📁 新建文件夹 / ⬆ 上传文件…、本地面板的 ⬆ 上传文件… / ⬆ 上传到远程（`uploadPicked()` 与 `runBatch` 的目标都是 `remotePath.value`）全部适用。这是**有意的小行为变更**：与其点下去才报错，不如提前置灰。
- **第三个入口也必须一致（审核 FF5）**：本地面板底部过滤栏的批量下拉「已选 N 项 ▾」走的是 `actionsFor('local')`（`SftpView.vue:460-473`）＋ `FilePane.vue:270` 的 `:disabled="a.disabled"`，而 `actionsFor` 现在只给 `rename` 设置了 `disabled`。因此 `actionsFor` 的 `upload`/`upload-picked` 两项也要 `disabled: !connected`。否则同一个面板会出现"空白菜单灰、行菜单灰、批量下拉能点"的分裂 —— 正是自审修正 8 要消除的那类不一致。
- **审核 FF16 更正**：v1.1 写的"远程面板文件行菜单只可能在已连接时出现"**不成立**（`retryItem` 会让 `remoteItems` 在 `connected===false` 时也被填出来）。修法见 §8.5 的"远程操作成功后回同步 `connected=true`"；同时本表的行菜单仍复用同一 `remoteDep` 计算，不写特例。
- 「✏ 重命名」在行菜单里**保持现状语义**（重命名被右键的那一项；右键项不在集合内时先 `sel.single()`），**不**照搬 `actionsFor()` 里 `n !== 1` 的置灰 —— 这是刻意的范围控制，避免顺带改既有行为。
- **空选择的防御（审核 FF12）**：v1.1 写的"`names` 为空时已是 no-op"是错的 —— `runBatch` 在空 `names` 时仍可能因 `hiddenSelected > 0` 弹出**0 任务的冲突框**（`utils/batch.js:73-75`：`needsConfirm = conflictCount > 0 || hiddenSelected > 0`），并写一条"批量… 0 项"日志、触发一次 reload。新矩阵下空白菜单不再提供批量动作、理论上不可达，但 `doAction` 顶部仍加一条统一防御：
  ```js
  if (name !== 'refresh' && name !== 'mkdir' && !names.length) return
  ```
  （`refresh`/`mkdir` 是面板级动作，本来就不依赖选中。）

### 5.4 菜单浮层的禁用样式

`ContextMenu.vue:13-21` 目前只定义 hover，没有禁用态。新增：

```css
.menu :deep(button:disabled) { color: var(--text-faint); cursor: default; }
.menu :deep(button:disabled:hover) { background: none; }
```

---

## 6. 持久化设计

### 6.1 配置结构（`internal/config/store.go`）

```go
// SftpUIState 是 SFTP 视图的持久化状态：与 AppSettings 解耦（独立配置段），
// 因为它只服务于 SFTP 面板，且设置页的"保存/恢复默认"整结构覆盖不应牵连它。
type SftpUIState struct {
    LastLocalDir   string            `toml:"last_local_dir" json:"last_local_dir"`
    LastRemoteDirs map[string]string `toml:"last_remote_dirs" json:"last_remote_dirs"` // host → 最后路径
    SortLocalKey   string            `toml:"sort_local_key" json:"sort_local_key"`
    SortLocalDir   int               `toml:"sort_local_dir" json:"sort_local_dir"`
    SortRemoteKey  string            `toml:"sort_remote_key" json:"sort_remote_key"`
    SortRemoteDir  int               `toml:"sort_remote_dir" json:"sort_remote_dir"`
}
```

落盘形态（**已实测**，见 §16.2 的 toml 探针；键含点/空格时编码器自动加引号）：

```toml
[sftp_ui]
last_local_dir = "/home/lan/work"
sort_local_key = "modTime"
sort_local_dir = -1
sort_remote_key = "name"
sort_remote_dir = 1
[sftp_ui.last_remote_dirs]
"10.99.152.18" = "/var/log/nginx"
"prod db" = "/srv/app"
```

`AppConfig` 新增字段（放在 `RemoteRecent` 之后，保持"位置类"字段聚拢）：

```go
SftpUI SftpUIState `toml:"sftp_ui" json:"sftp_ui"`
```

**前提声明（审核 BF 提示，必须写进实现注释）**：`last_remote_dirs` 的键是 **SSH 主机名（不含 user）**，即本设计假定"host 唯一标识一个远端位置"。SFTP 视图的所有绑定都把 user 传 `''`（`SftpView.vue` 全量调用点），所以该假定在视图范围内成立；但 `internal/sync` 会用 `rule.User`（`internal/sync/ctrl.go:578/666/733`），若将来把"最后路径"能力扩展到 sync/watch，必须先把 user 纳入键。本轮不改键结构。

**兼容性**：老配置没有 `[sftp_ui]` 段 ⇒ 零值即默认（`LastLocalDir=""`、`LastRemoteDirs=nil`、sort 由 `Normalize` 补成 `name/1`），**不需要迁移**；首次因任何原因写盘后该段自然出现（`SftpUI` 不带 `omitempty`，且 `LoadConfig` 先 `DefaultAppConfig()` 再 decode）。

**归一挂点（自审修正 3，不能漏）**：必须把 `c.SftpUI.Normalize()` 加进现有的 `AppConfig.normalize()`（`internal/config/store.go:211`，现在只调 `c.App.Normalize()`）。`LoadConfig` 与 `DefaultAppConfig` 都走 `normalize()`，所以这是唯一的挂点；漏了它，手改配置写进去的非法 sort 值会一路飘到 UI。`GetSftpUI`/`SaveSftpUI` 仍各自再调用一次 `Normalize()`（双保险：绑定层可能被直接调用而不经过 `LoadConfig`）。

### 6.2 归一（`SftpUIState.Normalize()`）

- `SortLocalKey`/`SortRemoteKey` 白名单 `name|size|modTime`，非法（含空）→ `name`。
- `SortLocalDir`/`SortRemoteDir` 归一到 `±1`：`< 0` → `-1`，否则 `1`（零值 0 不合法 ⇒ 变 `1`）。
- `LastRemoteDirs` 为 nil → 空 map（避免前端拿到 null）。
- **不设条数上限**：host 集合来自 ssh config，规模有限；主机被删/改名后遗留条目无害（不清扫，避免误删用户数据）。

### 6.3 写入时机与并发规则（v1.2 重写 —— 修掉审核 BF1/BF2/BF4 三个真缺陷）

#### 6.3.1 谁写 `last_local_dir` / `last_remote_dirs`

**规则：只有"前端面向的导航绑定"能写 last path；后端内部的 recent 记录一律不写。**

- 可写：`AddLocalRecent(path)` / `AddRemoteRecent(host, path)` —— 它们的调用者只有"导航成功"（`SftpView.vue:252/276/297/400`）。
- **不可写（本轮新增的硬约束）**：内部函数 `recordRecentSFTP`（`app.go:1394-1409`）的全部调用点 —— 传输完成 4 处（`app.go:810/823/836/882`）与 **`SftpHome`（`app.go:1127`）**。实现方式：把记录函数拆成"是否同时记 last path"的显式参数（如 `recordRecentSFTP(host, remoteDir, localDir string, rememberLast bool)`），绑定层走 `true`、内部调用一律 `false`。

**为什么必须这么切（审核 BF1，Blocking）**：`connect()` 每次都会无条件调 `SftpHome(h)`（`SftpView.vue:244`），而 `SftpHome` 内部 `recordRecentSFTP(host, home, "")`。若它顺带写 last path，单主机用户的"最后路径"会被**改写成 home**；更糟的是前端 store 的队首短路（`stores/locations.js:52-53`）会让紧接着的 `addRemoteRecent(A, 记忆路径)` **不发 IPC**，于是这次污染不会被纠正 —— 而本次会话的落点仍然正确（镜像来自启动时 `GetSftpUI`）。结果：**验收 C2 通过、盘上已是 home、下次启动永久落 home**（静默数据损坏，最坏的一类缺陷）。

#### 6.3.2 last path 的写入不受队首短路约束

同一函数内先判 `lastChanged := cfg.SftpUI.LastRemoteDirs[host] != path`（本地同理），再走既有 recents 去重/短路：

- `lastChanged == false && recents 队首同值` ⇒ `return nil`（**不写盘**，保持今天的写盘次数）。
- 其余情况 ⇒ 更新该更新的部分，**一次** `saveConfig`。

理由：`recordRecentSFTP` 会（在不写 last path 的前提下）改写 recents 队首，此时"队首同值"不再等价于"last path 已正确"。**规格原文"短路语义天然正确"是错的，已删除**（审核 BF7 要求测试与意图对齐，见 §11.1）。

#### 6.3.3 排序状态与整结构覆盖

- 前端 `setSort(pane, key, dir)` 改内存后调 `saveSnapshot()`（值未变则短路）。
- **`saveSnapshot()` 必须先读回再合并（审核 BF2）**：`const cur = await GetSftpUI()` → 只覆盖 `sort_local_*` / `sort_remote_*` 四个字段 → 整结构写回。这样即使后端/镜像之间出现时序差，也**不会把较新的路径字段整结构回滚**。
- **payload 键名必须显式逐字写出**（Go 侧 `encoding/json` 按 json tag 精确匹配，`lastLocalDir` 这种 camelCase 会被静默忽略 ⇒ 字段被清零）：`{ last_local_dir, last_remote_dirs, sort_local_key, sort_local_dir, sort_remote_key, sort_remote_dir }`。禁止用 `{...this.$state}` 这类展开。
- `SaveSftpUI` 的调用点只允许出现在 `stores/sftpui.js` 内部。接线测试钉死：`grep -rn 'SaveSftpUI' frontend/src` 只允许命中 store、`*.test.js` 与 `wailsjs/**`（**审核 FF11**：store 单测必须 import 它才能断言，红线要给它留口）。

#### 6.3.4 并发红线（三条，全部写进实现注释）

1. **整结构赋值**：`cfg.SftpUI` 一律"构造新值后整结构赋值"，**绝不原地修改 `LastRemoteDirs` 这个 map**。原因：`snapshotConfig()`（`app.go:649-657`）是浅拷贝，map 头被共享；原地写会与锁外正在跑的 TOML 编码（`saveConfig`，`app.go:659-669`）并发读写同一个 map ⇒ `-race` 能抓到、生产会 panic。
2. **锁内绝不 Normalize、绝不碰返回值的 map/slice 头**：`GetSftpUI` 只在读锁内做 `s := a.cfg.SftpUI`（值拷贝），**解锁后**再 `s.Normalize()` 并返回（审核 BF3：`Normalize()` 会给 nil map 赋新 map 头，若在读锁内调用就是对共享结构的原地写）。`SaveSftpUI` 的 Normalize 在加写锁**之前**完成。
3. **slice 同类问题（审核 BF4；**范围已按用户 2026-10-10 决定扩大**）**：`AddLocalRecent`/`AddRemoteRecent`（`kept := a.cfg.LocalRecent[:0]`，`app.go:1353`、`:1380`）与 `AddBookmark`/`RemoveBookmark`（`app.go:1324`、`:1332`）**都**原地复用底层数组，而 `snapshotConfig` 的浅拷贝与该数组共享 ⇒ 锁外 TOML 编码期间并发写同一数组，是**真的 `-race` 缺陷**。本轮**四个函数统一改造**：

   - `kept := make([]T, 0, len(x))` 新数组（不复用 `[:0]`），或直接整结构赋值新 slice；
   - 去重/增删 + （仅 recents 两处）last path 更新整段进 `cfgMu` 写锁；
   - 锁内只赋值，解锁后再 `saveConfig()`。

   由此本规格可以声称"**这 4 个配置写入点并发安全**"；但**仍不声称全仓并发安全** —— 其它直接改 `a.cfg` 的路径（如 `updateTunnel` 等）未在本轮审计范围内。
4. 附：`app.go:37-42` 的 `cfgMu` 注释需同步扩写为"保护 `cfg.App` 与 `cfg.SftpUI`"。

#### 6.3.5 锁与 IO 规约

`cfgMu` 内**只构造新值 + 赋值**，绝不在锁内做 IO（与 `setAppSettings`：`app.go:404-411` 逐字一致）。三条写入路径（`SaveSftpUI`、`AddLocalRecent`、`AddRemoteRecent`）一律"写锁内赋值 → 解锁 → `saveConfig()`"；`SaveSftpUI` 的锁内还要有 `if a.cfg == nil { a.cfg = &config.AppConfig{} }` 守卫（照 `setAppSettings` 的写法；**审核 BF13**：否则 App 未 Init 时 `a.cfg.SftpUI = s` 空指针，而 Wails dispatcher 会静默 recover，前端只看到"点了没反应"）。

### 6.4 恢复时机、落点优先级与两处行为变更

| 状态 | 读取点 | 使用点 | 优先级 |
|---|---|---|---|
| 本地最后路径 | 启动 `sftpui.load()` | `onMounted` | `last_local_dir` > `await Cwd()` |
| 远程每主机最后路径 | 启动 `load()` + 会话内内存镜像（§7） | `connect()` | 见下方落点优先级 |
| 双侧排序 | 启动 `load()` | 两侧 `FilePane` 初始 props | 配置值（非法已被后端归一）> `name/1` |

**`connect()` 的落点优先级（自审修正 5 —— 这条不改就是回归）**：

| 优先级 | 来源 | 说明 |
|---|---|---|
| 1（最高） | `pendingPath`（非空） | "位置"下拉选中的目录、路径栏输入的目标、系统拖入前置目录等显式指定 |
| 2 | `pendingHome`（新增布尔标志） | **「主目录」按钮的显式意图** |
| 3 | `lastPathFor('remote', host)` | 该主机的记忆路径（本轮新增能力） |
| 4（兜底） | `SftpHome(host)` | 无记忆时 |

**为什么必须有第 2 级**：现有 `goRemoteHome()`（`SftpView.vue:306-314`）在未连接分支用 `pendingPath.value = ''` 表达"我要 home"，**依赖"空 pendingPath = home"这一隐含约定**。一旦插入第 3 级（记忆路径），这个约定就失效 —— 用户点「主目录」会被送回上次的目录。所以「主目录」必须改成显式标志（`pendingHome.value = true`），并在 `connect()` 里按上表分派；`pendingPath` 与 `pendingHome` 在消费后都要清空（含失败分支的清理，沿用 `SftpView.vue:335` 对残留 pendingPath 的既有处理原则）。

**两处必须写进规格的行为变更**（用户已确认方向，实施时不得"顺手改回去"）：

- **B1：手动「断开」→「连接」也会回到该主机最后路径**（今天是回 home）。这是 D7 的必然后果。
- **B2：`connect()` 里读记忆路径是有前置条件的**：只在"本次连接的目标来自记忆"时才允许 §6.5 的 home 回落。

**B3：启动顺序是硬约束（审核 BF8，漏了就静默失效）**：`onMounted`（`SftpView.vue:881-889`）必须写成

```
await loadHosts() → await locations.load() → await sftpui.load()
→ localPath = sftpui.lastPathFor('local') || await Cwd()
→ await loadLocal()          // 它内部会 addLocalRecent(localPath)
```

顺序错了会静默损坏数据：若 `sftpui.load()` 被追加在 `loadLocal()` 之后（最自然的改法），`loadLocal()` 已经先把 `last_local_dir` 写成 `Cwd()`（队首多半不同 ⇒ 不短路 ⇒ 真写盘），随后 `GetSftpUI` 读到的就是 cwd ⇒ **本地最后路径每次启动都被抹成 cwd**，D7 的本地一半失效而验收看不出来。

**B4：`connect()` 必须被 `sftpui.loaded` 兜住**：`load()` 未完成时点「连接」会拿不到记忆路径。`connect()` 首部 `if (!sftpui.loaded) await sftpui.load()`（幂等），或禁用连接按钮直到 loaded —— 取前者，避免 UI 出现新的置灰态。

**B5：落点决策必须只有一个实现（审核 FF8，防"两条路走两个落点"）**：抽一个纯函数（放 `frontend/src/utils/remotepath.js`，仓库 house style：纯逻辑进 utils 并单测）

```js
resolveRemoteTarget({ pendingPath, pendingHome, remembered, home })
  → { path, fromMemory }   // fromMemory 供 §6.5 的回落判定使用
```

`connect()` 与 `syncConnection()` 的"未连接分支"**共用**它。现状差异是真实存在的：`connect()` 走记忆路径，而 `syncConnection()` 的未连接分支（`SftpView.vue:906-909`）会把 `remotePath` 设成 `SftpHome(h)` —— 切主机再切回（后端粘性 `Connected` 仍为 true，`gosftp.go:1486-1495`）时经 `syncConnection` 恢复，就会落到 home，与 D7/D16/目标 5 直接矛盾。

其余（`SftpHome` 兜底、`connected=true`、导航成功后 `addRemoteRecent`）保持不动。

### 6.5 记住的路径失效时（D12）

**前提前置（自审修正 6）**：`loadRemote()`/`loadLocal()`（`SftpView.vue:218-228`、`:394-404`）**内部吞错**（`catch (e) { err(e) }`，不 rethrow），所以直接复用它们**永远无法**判定"这个目录列不出来"。实现时必须先从两者各抽出一个**会抛错**的取列表函数（如 `fetchRemote(target)` / `fetchLocal(target)`），由 `loadRemote`/`loadLocal` 包一层吞错保持既有语义，恢复路径与 §6.5 的回落判定则用裸函数 + `try/catch`。这是"回落分支能不能真的触发"的关键，不是实现细节。

- 本地：`last_local_dir` 打不开（目录被删/盘符已卸载）→ 回落 `await Cwd()`，日志一行「上次的本地目录已不可用，已回到 <cwd>」。
- 远程：记住的路径列不出目录 → 回落 `SftpHome`（**仅在"该路径来自记忆"时回落**；用户手输的路径失败仍按现有乐观提交回滚语义处理，不偷偷改道）→ 日志一行「上次的远程目录已不可用，已回到 <home>」。
- 若 `lastPathFor` 与 home 相同，不触发回落分支。
- 回落本身也失败（home 都列不出来）→ 沿用既有失败语义（日志 + 保持 `connected` 状态由 §8 的探针复核接管），不做二次回落。

---

## 7. 前端数据流

### 7.1 新 store：`frontend/src/stores/sftpui.js`

```js
state: {
  loaded: false,
  lastLocalDir: '',
  lastRemoteDirs: {},                                        // host → path
  sort: { local: { key: 'name', dir: 1 }, remote: { key: 'name', dir: 1 } },
}
actions:
  async load()                  // GetSftpUI() 一次；失败兜底默认并**抛出**（由 onMounted 的调用方 err()）
  lastPathFor(pane, host)       // remote → lastRemoteDirs[host] || ''；local → lastLocalDir || ''
  async setSort(pane, key, dir) // 值未变直接 return（不写盘）；否则改内存 + await saveSnapshot()
  noteLocalPath(path)           // 内存镜像（由 locations store 调用，不额外 IPC）
  noteRemotePath(host, path)    // 同上
  async saveSnapshot()          // 唯一的 SaveSftpUI 出口：先 GetSftpUI 读回、只覆盖 sort 四字段（§6.3.3）
```

- **`saveSnapshot()` 是本 store 的私有出口**：`SaveSftpUI` 只允许出现在这里，任何新功能要改 SFTP UI 状态都走 store action，禁止在组件里直接调绑定。
- `load()` 把后端返回当作**整体快照**写入（`lastLocalDir`/`lastRemoteDirs`/两侧 sort 一次成型），并对 sort 再做一次前端白名单兜底（后端已归一，前端只防"绑定返回畸形"）。
- **失败传播（审核 FF9）**：store 内**不吞错也不自己记日志** —— `err()` 是 `SftpView` 的组件内函数（`SftpView.vue:214-216`），store 调不到；让 `load()`/`setSort()` **抛出**，由调用方（`onMounted` / `onSort`）`try/catch` 后走既有 `err()`。内存状态**不回滚**（改内存在前、写盘在后），下次变更会再试。§10 的"走既有 `err()`"由此落地。
- **`setSort` 的调用点形状**：`SftpView` 的 `onSort(pane, {key, dir})` = `try { await sftpui.setSort(...) } catch (e) { err(e) }`。

单测仿 `frontend/src/stores/locations.test.js`（`vi.mock('../../wailsjs/go/main/App')` + `setActivePinia`）。

### 7.2 最后路径的内存镜像

`stores/locations.js` 的 `addLocalRecent`/`addRemoteRecent`（`:43-57`）成功后顺带调 `useSftpUIStore().noteLocalPath/noteRemotePath`：

- 后端已经把它落盘了；前端镜像只是为了让**同会话内**的"断开→重连"能立刻拿到正确路径（否则要等重启）。
- 纯内存、**零新增 IPC**；`locations.js` → `sftpui.js` 单向依赖，无环。
- 短路分支（队首同值 return）不更新镜像**在 `sftpui.loaded === true` 时**是安全的（队首同值 = 镜像值本就相同）。**审核 FF17 修正**：`load()` 失败时镜像是空串而 locations 队首非空，此时短路会跳过唯一能修正镜像的机会 —— 该情形的影响只是回落 home/cwd（与目标一致），所以**不做兜底**，但规格不得声称无条件成立。
- **审核 BF2 补充**：后端不再有"绕过导航写 last path"的路径（§6.3.1 的规则），所以镜像与后端不会长期分叉；`saveSnapshot()` 的"先读回再合并"（§6.3.3）是第二道保险。

### 7.3 排序状态上收到 SftpView（单一真源）

- `FilePane.vue` 删除内部 `sortKey`/`sortDir` ref（`:124-125`），改为 props `sortKey`（默认 `'name'`）、`sortDir`（默认 `1`）。
- **必须一起改的三处读点（审核 FF1，Blocking）**：规格 v1.1 只列了 `visible`，漏了同作用域的 `arrow()`（`FilePane.vue:180-182` `return sortKey.value === key ? (sortDir.value === 1 ? '▲' : '▼') : ''`）——它被模板 `:226-228` 调用三次，删 ref 而不改它 ⇒ **渲染期 `ReferenceError`，整块面板白屏**。`arrow()` 一律改为读 props。
- 抽一个纯函数进 `frontend/src/utils/sort.js`（仓库 house style：纯逻辑进 utils + 单测）：
  ```js
  export function nextSort(current, key) {          // current = {key, dir}
    return current.key === key ? { key, dir: -current.dir } : { key, dir: 1 }
  }
  ```
  `FilePane.sortBy(key)` = `emit('sort', nextSort({ key: props.sortKey, dir: props.sortDir }, key))`。语义与 `:129-136` 逐字一致：同列取反、换列归升序。
- `FilePane.vue:29` 的 `emits` 数组新增 `'sort'`。
- `visible`（`:138-154`）的排序实现不变，只把读源从 ref 换成 props；`shown`/`visibleKeys`/`@visible` 链路（决策 32 的"可见顺序单一真源"）**完全不动**。
- `SftpView.vue` 用 `sftpui.sort.local/remote` 作为两侧 props，`@sort="onSort('local', $event)"` → `try { await sftpui.setSort(pane, key, dir) } catch (e) { err(e) }`。

---

## 8. 连接失效自愈

### 8.1 根因（三条事实的组合，非推测）

1. `Connected(host)` 是**粘性连接意图**（`internal/sftp/gosftp.go:1491-1494`）：握手成功即 `true`，只有显式 `Disconnect`/`CloseAll` 才清 ⇒ 链路死掉后 `SftpConnected` 仍返回 `true`。
2. `syncConnection()` 首行 `if (!h || connected.value) return`（`SftpView.vue:900`）⇒ 已连接时**永不复核**，UI 永远停在"已连接"。
3. 池的 idle 上限是 **2 条/键**（`session.go:104`），唤醒后这两条死会话会被 `AcquireList` 优先复用 ⇒ 正好产生用户日志里相隔 6 秒的**两次**失败（`mkdir`、`ls`）。失败时 `reusable=false` 会丢会话，所以第三次操作通常会新建会话而成功 —— 但 UI 状态已经骗了用户，且期间的操作都失败。

### 8.2 后端：暴露真探活（**只加一个绑定**）

- `internal/sftp`：新增 `func (c *Ctrl) Probe(ctx context.Context, host, user string) bool`：
  - `*GoBackend` → `g.pool.Probe(ctx, host, user)`（真实往返 + `probeTimeout=5s` + 失败丢弃死会话，`session.go:288-305`），但**必须先修 `Pool.Probe` 的预算缺陷**（见下条）。
  - 其它后端（legacy batch）→ 一次真实列目录（`c.List(host, user, "/")` 成功即 true），但**必须给调用方一个上界**：`BatchBackend.runner` 是**无 ctx** 的 `osutil.Runner`（`batch.go:23`，`app.go:205` 传 `osutil.NewRunner()`），而 `CtxRunner` 是另一种类型并带进程组隔离 ⇒ 为探针改 batch 传输的 runner 装配**不成比例**（legacy 回退路径）。实现改为在 `Ctrl.Probe` 里**goroutine + `ctx` select**：
    ```go
    done := make(chan bool, 1)
    go func() { _, err := c.List(host, user, "/"); done <- err == nil }()
    select {
    case ok := <-done: return ok
    case <-ctx.Done(): return false
    }
    ```
    **再加每 host 单飞去重**（`map[string]bool` + mutex）：前一次 batch 探针还挂着时直接返回 `false`，不重复起 goroutine —— 否则反复切标签会按次泄漏 goroutine + 子进程。残留（子进程本身的回收）由 ssh 自身超时约束，记在 R8。
  - **不改** `Backend` 接口 —— 理由照 `RecoverSwaps`/`CleanupParts` 的既有先例（`ctrl.go:118-130`：只有 GoBackend 有的能力就在 `Ctrl` 层做类型断言，不进接口）。**审核 BF12 更正**：v1.1 写的"会波及全部测试替身"是夸大 —— 替身只有 2 个，`ctrl_test.go:658-712` 的 `fakeBackend`（逐方法实现，加接口方法会编译失败）与 `app_test.go:1472-1473` 的 `recordingBackend`（嵌入接口，不会编译失败但调用会 panic）。
- **`Pool.Probe` 的预算缺陷与修法（审核 BF5，Important）**：`AcquireList`（`session.go:182-210`）**完全不用 ctx**，池里没有 idle 会话时会真拨号，最长吃 `ConnectTimeout=10`（`gosftp.go:182`）；一旦拨号耗时超过 5s，随后的 `ReadDirContext(cctx, ".")` 面对**已过期**的 cctx 会立即返回 `ctx.Err()` ⇒ **一条健康连接被判失效**，还会把刚建好的健康会话 `Release(s, false)` 关掉，并触发 §8.3 的断开+重连（无辜 teardown）。修法：`AcquireList` 返回后先查 `cctx.Err()`，若已过期但**会话确实建出来了**（`markSessionConnected` 已跑），则用**新预算**复核一次（`context.WithTimeout(ctx, probeTimeout)`）再判，而不是直接 false。
- **探针的未声明副作用（必须写进实现注释）**：`Probe` 经 `AcquireList` → `markSessionConnected` 会把**粘性** `connected[host]` 置 `true`（`session.go:167-176`、`gosftp.go:157-167`）。因此"探针说失效（false）"与"粘性已置位（true）"会同时成立 —— 这正是 §8.3 降级时必须调用 `SftpDisconnect` 清意图的原因，后续实现者不得把 `Probe` 当纯只读。
- `app.go`：新增绑定 `func (a *App) SftpProbe(host string) bool` → `a.sftp.Probe(context.Background(), host, "")`（`a.sftp == nil` 时 false，与 `SftpConnected` 同构守卫，`app.go:983-988`）。
  - user 恒传 `""`：SFTP 视图的所有调用（`SftpList`/`SftpGet`/…）都用 `''`，`markConnected` 也已**不再记录**最近握手凭据（`gosftp.go:156-160`），没有别的来源可用；探针用 `""` 与视图自己的会话键 `host\x00""` 一致（后果是探针不会复用 sync/watch 用非空 user 建的会话，见 R7）。
- **不新增**"清理失效连接"的绑定：直接复用 `SftpDisconnect`（`Ctrl.Disconnect` = `clearConnected` + 丢该 host 空闲会话，`gosftp.go:1498-1501`）。这一步是**必须**的：否则 `syncConnection()` 的未连接分支会读到粘性 `true` 又翻回"已连接"，还会顺手把远程路径设成 `SftpHome`（`:900-911` 的既有行为）。
- 时延预算（**修正 R1 的定性**）：不再是单纯"最坏时延"，而是"**拨号 ≥ 5s 时会把健康连接误判为失效**"——已由上面的复核修掉；修后最坏预算 ≈ `ConnectTimeout=10` + 2×`probeTimeout`。**仍未实测**，R1 保留但改为实测该预算。

### 8.3 前端：切回标签探活 + 自动重连一次

`syncConnection()`（`SftpView.vue:900-911`）改为：

```js
async function syncConnection() {
  const h = host.value
  if (!h) return
  if (connected.value) {
    // 已连接：真探活复核（不重置路径、不重载列表——每次切标签跳目录是不可接受的）
    if (probing) return
    probing = true
    try { if (!(await probeTimed(h))) await recoverConnection(h) }
    catch { /* 探针本身失败不改变现状 */ }
    finally { probing = false }
    return
  }
  // 未连接分支：改用 §6.4 B5 的共享落点决策（不再无条件 SftpHome）
}
```

- **`probeTimed(h)` 是必须的客户端超时兜底（审核 BF6，Important）**：`Promise.race([SftpProbe(h), 计时器])`（建议 15s，覆盖后端最坏预算并留余量）。**超时视为"未知"，不是"失效"** —— 不触发重连；**但 `probing` 必须在 `finally` 里复位**，否则 legacy batch 后端一旦挂在 `cmd.Run()` 上，`await SftpProbe` 永不返回 ⇒ `probing` 永远为 true ⇒ 该视图此后**任何**切标签探活都被静默短路（自愈能力永久失效且无任何报错）。
- `recoverConnection(h)`：
  0. **入口幂等（自审修正 9）**：`if (recovering) return`，进入后置 `recovering = true`、`finally` 复位。`probing` 只防"探针并发"，管不住"探针 A 失败后 recoverConnection 还在跑、探针 B 又失败"的重入 —— 两个标志各管一段，缺一不可。
  1. 日志 warning：「远端连接已失效（主机休眠或网络中断），正在重连…」。
  2. `await SftpDisconnect(h)`（清粘性意图 + 丢死会话；失败静默，不能因此中断自愈）。
  3. `connected.value = false`；`remoteItems.value = []`；`sel.clear(remoteSelection)`（与 `disconnect()` 的降级形态一致 ⇒ §5.3 的置灰规则自动生效）。
  4. `pendingPath.value = sftpui.lastPathFor('remote', h)`（回到该主机最后路径，而不是 home —— 与 §6 协同）。
  5. `await connect()`（既有实现：握手 → 落点 → 列目录 → 记最近）。
  6. **成功/失败判据（审核 FF14）**：`connect()` 不返回值也不 rethrow，所以判据只能是 `connected.value`：`true` → 日志 info「已重新连接 <host>」；`false` → 日志 error「重连失败：…，请手动点「连接」」。**例外**：`connect()` 内部在 `await` 期间发现 `host.value !== h` 会静默早退（`SftpView.vue:245`），此时**不记"重连失败"**（用户已切主机，这条日志是噪音且误导）。
  7. **只重连一次**：不循环、不起定时器；`recovering` 标志去重，防止反复切标签叠加。

### 8.4 前端：远程操作失败后复核（D14）

新增 `remoteOpFailed(e)`：先 `err(e)` 记原文（保持现状），再 `verifyRemote()`：

```js
async function verifyRemote() {
  const h = host.value
  // connected === false 时直接返回：这是「连接本身刚失败」的场景（例如 recoverConnection
  // 自己的 connect() 失败），此时再探活/再重连只会打出第二轮无意义尝试（自审修正 7）。
  if (!h || !connected.value || probing) return
  probing = true
  try { if (!(await probeTimed(h))) await recoverConnection(h) } finally { probing = false }
}
```

**覆盖清单（v1.2 修正；含审核 FF2/FF3/FF15 的补漏与降级）**：

| # | 位置（`SftpView.vue`） | 处置 |
|---|---|---|
| 1 | `112` `dispatchTransfer` | 远程相关 ⇒ `remoteOpFailed`；**但 `rec.cancelRequested === true` 时跳过探活**（审核 FF15a：用户主动取消导致的失败不该被归因为"连接已失效"） |
| 2 | `226` `loadRemote` | 纯远程 ⇒ `remoteOpFailed` |
| 3 | `254` `connect` | **恒早退**（`connect()` 只在未连接时被调用，`verifyRemote` 的 `!connected.value` 守卫必然命中）⇒ 保留 `err(e)`，**不计入"覆盖点"**（审核 FF15b：这是死接线，不是覆盖） |
| 4 | `302` `goRemote` | 纯远程 ⇒ `remoteOpFailed` |
| 5 | `312` `goRemoteHome` 的 `SftpHome` | 纯远程 ⇒ `remoteOpFailed` |
| 6 | `606` `removeSelected` | 混合面板 ⇒ 远程分支 `remoteOpFailed` |
| 7 | `629` `renameItem` | 混合面板 ⇒ 远程分支 `remoteOpFailed` |
| 8 | `649` `mkdirIn` | 混合面板 ⇒ 远程分支 `remoteOpFailed` |
| 9 | `668` `uploadPicked` | 纯远程 ⇒ `remoteOpFailed` |
| 10 | `767` `onMoveDrop` 的 `targetItems` 加载 | **审核 FF2 新增**：它在 `try` 之外（唯一的 `try` 在 `:788` 循环体内）⇒ 死链时函数直接 reject、unhandled rejection、无日志无自愈。必须把"目标子目录列表加载 + `askConflict`"一起纳入 `try` |
| 11 | `796` `onMoveDrop` 移动循环 | 混合面板 ⇒ 远程分支 `remoteOpFailed` |
| 12 | `163` `cleanItem` 远程分支 | **审核 FF3 新增**：`SftpRemove(host, '', rec.partPath)` 的失败被包成字符串 `'清理临时文件失败：'` ⇒ 任何"无裸 `err(e)`"口径都看不见它。按 `rec.direction` 分派（download=本地、upload=远程） |

**明确排除**（不是链路健康信号）：`136` `SftpTransferCancel`（取消语义）、`389` `disconnect()` 的 `SftpDisconnect`（那正是要断开）、`402` `loadLocal`、`875` `CopyLocal`、`211` `ListHosts`，以及各混合点的本地分支。

**实现形状（自审修正 8 —— 与 §11.2 的断言口径必须一致）**：`:606`/`:629`/`:649`/`:796` 是**单 catch 覆盖两个面板**的混合点，改造后形如

```js
} catch (e) {
  if (pane === 'local') err(e)
  else await remoteOpFailed(e)
}
```

⇒ 这些函数体内**仍会保留 `err(e)`**（本地分支）。所以接线测试**不能**断言"这些函数里没有 `err(e)`"，正确口径见 §11.2。

### 8.5 降级形态与一处一致性回同步

- `connected=false` + `remoteItems=[]` + 清远程选中 ⇒ 远程面板回到"未连接"形态：刷新/新建文件夹/上传文件…置灰，工具栏刷新只刷本地。用户点「连接」即手动恢复；因为 §6 的最后路径已落盘，手动重连也会回到该主机最后路径。
- **审核 FF16 修正**：远程面板"行菜单只在已连接时出现"的原论据不成立 —— `loadRemote()`（`:218-219`）只判 `host` 不判 `connected`，而 `retryItem`（`:141-148`）会先派发传输（后端按需拨号 ⇒ 粘性 Connected 变 true）再 `loadRemote()` ⇒ 会出现"`remoteItems` 非空但 `connected === false`"，此时行菜单被置灰（链路其实可用）、工具栏刷新还跳过这个有内容的远程面板。修法：**远程操作成功后回同步** —— `dispatchTransfer` 的成功路径若 `connected.value === false` 则置 `true`（后端已建出会话 ⇒ 该值是真的），并在接线测试里钉住。

---

## 9. 接口契约

| 层 | 新增/改动 | 说明 |
|---|---|---|
| `internal/config` | `SftpUIState` + `AppConfig.SftpUI` + `(*SftpUIState).Normalize()` | §6.1/§6.2；老配置零值兼容，无迁移；`Normalize` 挂进 `AppConfig.normalize()` |
| `internal/sftp` | `Ctrl.Probe(ctx, host, user) bool` | §8.2；GoBackend 走 `pool.Probe`（含预算缺陷修复），其它后端走**有上界**的列目录；**不动 `Backend` 接口**（理由照 `RecoverSwaps`/`CleanupParts` 先例） |
| `internal/sftp` | `Pool.Probe` 就地修预算缺陷 | §8.2：`AcquireList` 返回后若 `cctx` 已过期但会话建出 ⇒ 用新预算复核，而不是判 false |
| `app.go` 绑定 | `GetSftpUI() config.SftpUIState` | **读锁内只做 `s := a.cfg.SftpUI` 值拷贝，解锁后再 `s.Normalize()`**（BF3：锁内 Normalize 会给 nil map 赋头 = 原地写共享结构）；`a.cfg == nil` → 返回 Normalize 过的零值 |
| `app.go` 绑定 | `SaveSftpUI(s config.SftpUIState) error` | `Normalize()` → `cfgMu` 写锁内 **`if a.cfg == nil { a.cfg = &config.AppConfig{} }`**（BF13，照 `setAppSettings`）+ 整结构赋值 → 解锁后 `saveConfig()` |
| `app.go` 绑定 | `SftpProbe(host string) bool` | §8.2；`a.sftp == nil` 时 false |
| `app.go` 改动 | `AddLocalRecent`/`AddRemoteRecent` | §6.3：recents 逻辑保持，新增"last path 不受短路约束"的写入；**slice 重建改新数组 + 整段进写锁**（BF4）；内部 `recordRecentSFTP` 加 `rememberLast` 参数并由内部调用一律传 `false`（BF1） |
| `app.go` 改动 | `AddBookmark`/`RemoveBookmark` | §6.3.4 第 3 条（用户 2026-10-10 决定纳入本轮）：同样改新数组 + 进写锁，消除既有 `-race`；**不改这两个函数的对外行为与去重语义** |
| `app.go` 注释 | `cfgMu` 注释（`app.go:37-42`） | 扩写为"保护 `cfg.App` 与 `cfg.SftpUI`" |
| `frontend/wailsjs/go/{main/App.js,main/App.d.ts,models.ts}` | **必须重生成并入库** | `wails generate module -tags webkit2_41`（仓库根执行；`wails v2.15.0 generate module` 支持 `-tags` 已实测）；`models.ts` 会多出 `SftpUIState` 类，`last_remote_dirs` 生成 `Record<string, string>`（同版本既有先例：`App.d.ts:149`） |
| 前端新增 | `stores/sftpui.js`、`utils/sort.js`、`utils/remotepath.js`（各 + 测试） | §7.1/§7.3/§6.4 B5 |
| 前端改动 | `FilePane.vue`（右键触发面 + 排序上收含 `arrow()`）、`ContextMenu.vue`（禁用样式）、`SftpView.vue`（刷新入口/菜单矩阵/恢复点/自愈/`actionsFor` 置灰）、`stores/locations.js`（内存镜像） | §5/§7 |

---

## 10. 错误处理与日志文案

| 场景 | 级别 | 文案（逐字实现，便于验收 grep） |
|---|---|---|
| 探测失败触发自愈 | warn | `远端连接已失效（主机休眠或网络中断），正在重连…` |
| 自愈成功 | info | `已重新连接 <host>` |
| 自愈失败 | error | `重连失败：<原因>，请手动点「连接」`（**例外**：`connect()` 因 `host.value !== h` 静默早退时不记，见 §8.3 步骤 6） |
| 记住的远程路径不可用 | warn | `上次的远程目录 <path> 已不可用，已回到 <home>` |
| 记住的本地路径不可用 | warn | `上次的本地目录 <path> 已不可用，已回到 <cwd>` |
| `GetSftpUI` 读取失败 | error | 由调用方（`onMounted`）`try/catch` 后走既有 `err()`（原文），前端回落默认值 |
| `SaveSftpUI` 写盘失败 | error | **审核 FF9 修正**：store **不吞错**（它调不到 `SftpView` 的 `err()`）⇒ `setSort()` 抛出，由 `SftpView.onSort` 的 `catch` 走既有 `err()`；内存状态**不回滚**（下次变更会再试） |

- 探针本身抛错（绑定异常）不改状态、只记 debug 级：不能因为"探活工具坏了"就把用户翻成未连接。
- **探针超时（`probeTimed` 超时）按"未知"处理**：不记 error、不改 `connected`、不触发重连；只保证 `probing` 复位（R8/R15）。
- 自愈过程中的 `SftpDisconnect` 失败静默（其失败不影响后续 `connect()` 重新握手）。

---

## 11. 测试策略与验收

### 11.1 Go 单测

| 用例 | 断言 |
|---|---|
| `SftpUIState.Normalize` 表驱动 | 非法/空 sort key → `name`；dir 0/2/-7 → `1/1/-1`；nil map → 非 nil 空 map |
| TOML 往返 | `SftpUI` 编码 → 解码逐字段相等；含**带空格/中文/引号/点**的 host 名（map 键转义）。**审核 BF10 修正措辞**："Windows 反斜杠原样"描述落盘形态是错的 —— 落盘是基本字符串转义形式（`"C:\\Users\\lan"`），断言只比**解码后**的值（该行为已由 §16.2 的探针实测：`D:\工作\虚拟 共享` 往返相等） |
| 老配置兼容 | 无 `[sftp_ui]` 段的 TOML 读入 → `Normalize` 后等于默认值；不产生迁移标记 |
| `SaveSftpUI` 持久化 | 调用后重新 `LoadConfig` 读回；且 `Tunnels`/`Syncs`/`Bookmarks`/`RemoteRecent` **逐项未受影响** |
| `SaveSftpUI` 整结构语义 | 传只含 sort 的 SftpUIState ⇒ 路径字段被清零（**记录成测试钉住这个危险语义**，与前端"必须带全字段"的红线互为对照）；传全字段 ⇒ 六字段逐一落盘 |
| `AppConfig.normalize` 挂点 | 写一份含 `sort_local_key = "bogus"` / `sort_local_dir = 7` 的 TOML → `LoadConfig` 读回后已归一到 `name/1`（防"Normalize 没接上"的静默漏改） |
| last path 写入（**审核 BF7 重写判据**） | `AddRemoteRecent(host, path)` / `AddLocalRecent(path)` 返回后，**无论 recents 队首是否同值**，`SftpUI.LastRemoteDirs[host]` / `LastLocalDir` 必须**等于 `path`**；构造升级场景 = 先手工把队首设成同值 + map 留空/旧值。recents 的短路断言（`TS` 不刷新 + 不产生重复项）只针对 `RemoteRecent` 自身（沿用 `app_test.go:1060-1093` 的判据，**不要用文件 mtime**） |
| `recordRecentSFTP` 不写 last path（**审核 BF1 回归**） | 直接调内部的 `recordRecentSFTP(host, dir, localDir, false)`（模拟 `SftpHome`/传输完成）后，`LastRemoteDirs`/`LastLocalDir` **保持不变**；这条用例专门防"每次 connect 被改写成 home"的静默损坏 |
| `GetSftpUI` 守卫 | `a.cfg == nil` 时返回归一化零值、不 panic；**且 `GetSftpUI` 不得原地改 `a.cfg.SftpUI`**（结构性断言：函数体里 Normalize 调用出现在解锁之后，或断言调用后 `a.cfg.SftpUI.LastRemoteDirs` 仍为 nil） |
| `SaveSftpUI` 未就绪守卫（**审核 BF13**） | 并入既有 `TestSftpBindingsFailFastBeforeInit`（`app_test.go:1282-1315`，用 `NewApp()` ⇒ `a.cfg == nil`）：`SaveSftpUI` 必须不 panic（要么建 cfg 正常返回，要么返回明确错误） |
| `Ctrl.Probe` 分派 | 注入强制后端（`NewCtrlForcedBackend`）→ 走"有上界的列目录"分支；`*GoBackend` + fake `DialFunc`（`cancel_test.go:491` 有注入先例）→ 走 `pool.Probe`（失败会话被丢弃） |
| `Pool.Probe` 预算缺陷回归（**审核 BF5**） | fake dial 人为慢于 `probeTimeout` 但**成功建出会话** ⇒ `Probe` 必须返回 `true`（不得因 cctx 过期把健康连接判死），且该会话不得被 `Release(s,false)` 关掉 |
| `SftpProbe` 绑定 | 未连接的 host → `false`；`a.sftp == nil` → `false`、不 panic |
| 竞态（`-race`，**审核 BF4/BF14**） | ① `SaveSftpUI` ‖ `saveConfig`；② **`AddRemoteRecent` ‖ `saveConfig`**；③ **`AddLocalRecent` ‖ `saveConfig`**；④ **`AddBookmark` ‖ `saveConfig`**、⑤ **`RemoveBookmark` ‖ `saveConfig`**（后两条来自用户 2026-10-10 决定扩大的范围，防 slice 原地复用）；⑥ **`GetSftpUI` ‖ `SaveSftpUI`**（防锁内 Normalize）。另补**结构性判据**（源码断言，与前端 wiring 同风格）：四个写入路径中不出现 `a.cfg.SftpUI.LastRemoteDirs[` 赋值、写锁内构造了新 map、不出现 `kept := a.cfg.X[:0]`——因为 `-race` 绿只说明"这次没重叠"，不能证明实现没原地写 |

### 11.2 前端 vitest

| 文件 | 断言 |
|---|---|
| `stores/sftpui.test.js`（新增） | `load()` 填充；`lastPathFor` 两侧分支；`setSort` 值未变**不调** `SaveSftpUI`；值变化时**先调 `GetSftpUI` 再调 `SaveSftpUI`**，且 payload **逐字**含六个 snake_case 键（`last_local_dir`/`last_remote_dirs`/`sort_local_key`/`sort_local_dir`/`sort_remote_key`/`sort_remote_dir`）；`SaveSftpUI` reject 时 `setSort` **抛出**且内存保留 |
| `utils/sort.test.js`（新增） | `nextSort({key,dir}, key)`：同列取反、换列归 `1`（把 §7.3 的纯逻辑从组件里拉出来测） |
| `utils/remotepath.test.js`（新增） | `resolveRemoteTarget`：`pendingPath` 优先；`pendingHome` 优先于记忆路径（**D15 回归守卫**）；记忆路径优先于 home；`fromMemory` 标记只在"采用记忆值"时为真 |
| `stores/locations.test.js`（扩展） | `addRemoteRecent`/`addLocalRecent` 成功后 `sftpui` 镜像同步更新，且**未**新增 IPC（mock 调用次数） |
| `components/filePaneWiring.test.js`（扩展） | 排序读 props：不再有 `const sortKey = ref(`，**且不得再出现 `/sortKey\.value/` 或 `/sortDir\.value/`**（审核 FF1：`arrow()` 漏改会渲染崩溃，这条断言专门抓它）；`emits` 含 `'sort'`；空白右键 handler 存在、对可编辑控件放行、**对任何 `li` 无条件 yield**、对 `.pos-wrap` 先 `closePos()` 且不弹菜单 |
| `components/filePane.render.test.js`（**新增，审核 FF10**） | 用仓库已有的**真实 SSR 渲染**通道（`createSSRApp` + `renderToString`，先例 `TransferQueue.render.test.js:1-11`，`@vue/server-renderer` 已在 node_modules）断言：给定 `items` + `sortKey`/`sortDir` 时**行序**正确、列头箭头 `▲/▼` 正确、`active` 类正确；并通过 `onVisible` prop 断言 `@visible` 载荷就是可见顺序（真渲染而非读源码）。**注**：需要鼠标事件的部分（点列头触发 `sortBy`、contextmenu 派发）写不出，仍用形状断言 |
| `views/sftpMenuWiring.test.js`（新增，读源码钉形状） | 刷新按钮标签内调 `refreshAll` **且该标签内无 `:disabled`**（断言先 `match(/<button[^>]*refreshAll[^>]*>/)` 再检查，**不得**文件级 `not.toContain`——审核 FF4）；`refreshPane` 存在且两面板共用；菜单含刷新项；`remoteDep && !connected` 置灰接线；`actionsFor('local')` 的 `upload`/`upload-picked` 带 `disabled`（审核 FF5）；两个面板的 `@context` 都接到 `showMenu`；空白分支 emit `item: null`；「主目录」走显式 `pendingHome`；`doAction` 里 `refresh` 分支出现在 `sel.single` **之前**（审核 FF6） |
| `views/sftpRecoverWiring.test.js`（新增） | `syncConnection` 已连接分支调探测（且经 `probeTimed`）；`recoverConnection` 内 `SftpDisconnect` → `pendingPath` → `connect` 顺序；**"只重连一次"的断言必须限定在 `recoverConnection` 函数体内**（审核 FF10：文件级 `not.toContain('setInterval')` 会误伤既有 `SftpView.vue:892` 的时钟）；覆盖口径（自审修正 8 + FF2/FF3）：逐个核对 §8.4 的 **12** 个落点所在函数——要求"远程分支可达 `remoteOpFailed(`"，混合面板函数额外要求存在 `pane === 'local'` 的 `err(e)` 分支，纯远程函数要求体内不再出现裸 `err(e)`；`onMoveDrop` 额外断言其 `SftpList` 调用处于 `try` 内；`cleanItem` 断言按 `direction` 分派 |
| 构建 | `cd frontend && npm run build` 必须过（绑定重生成的前置证明）；`npx vitest run` 全绿 |

### 11.3 真机验收（Linux 本机 + VirtualBox Windows 客户机）

| # | 场景 | 可观测判据 |
|---|---|---|
| A1 | 工具栏刷新 | 在两侧各制造一个变化（本地新建目录、远程新建目录）→ 点工具栏刷新 → 两侧同时出现；选中的行仍保持选中 |
| A2 | 右键刷新 | 右键本地面板 → 刷新 → 只有本地重载（远程日志/请求计数不变，可用远程侧故意造一个失败点观察） |
| B1 | 菜单分级 | 空白右键 = 刷新/新建文件夹/上传文件…；行右键 = 下载/上传/重命名/删除/刷新；空白右键**不清空**已有选中 |
| B2 | 置灰 | 断开后右键远程面板 → 三项均灰、点了无反应；工具栏刷新仍能刷出本地变化 |
| C1 | 排序持久化 | 远程面板点「修改时间」再点一次（降序）→ 重启应用 → 两侧排序各自恢复（本地与远程互不影响） |
| C2 | 最后路径持久化 | 主机 A 导航到 `/var/log`、切主机 B 导航到 `/srv` → 重启 → 分别连接：A 落 `/var/log`、B 落 `/srv`（连接日志 + 面板路径栏截图） |
| C3 | 配置段形态 | `cat ~/.config/sshore/sshore.toml`（**审核 BF9 更正**：路径是 `sshore/sshore.toml`，`config.toml` 不存在，见 `internal/config/store.go:351-357`）→ 有 `[sftp_ui]` 与 `[sftp_ui.last_remote_dirs]`，且 `[app]`/`[[tunnels]]` 等既有内容未丢 |
| C4 | **最后路径不被 `SftpHome` 污染（审核 BF1 的端到端回归）** | 单主机场景：① 连到 `/var/log`；② 点「断开」→「连接」（每次 connect 都会调 `SftpHome`）；③ `grep last_remote_dirs` 侧确认该主机的值仍是 `/var/log`（不是 home）；④ **重启应用再连接** → 仍落 `/var/log` |
| D1 | 休眠/断链自愈 | 连接后杀链路（Linux：`ss -K dst <host> dport = :22`；无 root 时用 `iptables -A OUTPUT -p tcp --dport 22 -j DROP` 再删规则）→ 点标签切走再切回 → 日志出现「远端连接已失效…正在重连…」与「已重新连接」→ 面板回到最后路径且可操作；失败时停在未连接并给出明确日志 |
| D2 | 操作失败复核 | 断链后直接点「新建文件夹」→ 报错原文仍在日志，且随后自动自愈（不需要手动断开重连） |
| D3 | 不误伤 | 权限错误（远端目录不可写）**不得**触发断开/重连：探针返回 true ⇒ 只有原始报错 |
| E1 | 「主目录」回归守卫（防 §6.4 修 5 的回归） | 已连到 `/var/log`（有记忆）时点「主目录」→ **必须到 home**，不得被记忆路径抢走 |
| E2 | 行为变更确认（B1） | 导航到 `/var/log` → 点「断开」→ 点「连接」→ 回到 `/var/log`（不是 home）；与 C2 一起构成 D7 的完整证据 |
| F1 | 探针预算实测（**审核 BF5/R1**） | 正常链路上连续触发 20 次探活，记录单次耗时分布（应远小于 5s）；再在**冷启动**（无 idle 会话 ⇒ 必须真拨号）下各测一次，确认不再出现"健康连接被判失效"（表现为误触发"远端连接已失效…正在重连"日志）。证据 = 日志时间戳 + 该日志不应出现 |
| F2 | legacy batch 后端探活活性（**审核 BF6**） | `SSHORE_SFTP_TRANSPORT=batch` 下断链：切标签探活不得让 UI 永久卡在"探活中"（`probing` 必须复位）；超时后应保持"未知/不改状态"而不是误判失效 |

**证据要求**：D1/D2 必须给出日志原文（含时间戳）+ 自愈前后截图；C2 必须给出两个主机各自路径栏的截图。`make ci` 全绿日志同样入库。

---

## 12. 风险与未证实项

| # | 风险 | 处置 |
|---|---|---|
| R1 | **探针误判为失效**（原 v1.1 只当"时延"）：`Pool.Probe` 的 `AcquireList` 不用 ctx，拨号 ≥ 5s 时 `ReadDirContext` 面对过期 cctx 立即失败 ⇒ 健康连接被判死 + 健康会话被关 + 误触发重连。修后最坏预算 ≈ `ConnectTimeout=10` + 2×`probeTimeout`（**未实测**） | 已按 §8.2 修 `Pool.Probe`（过期但会话建出 ⇒ 新预算复核）；§11.1 加 fake-dial 慢于 `probeTimeout` 的回归用例；§11.3 F1 实测耗时分布 |
| R2 | 自动重连在无网络时会失败 → 反复切标签会反复尝试 | 只重连一次 + `recovering`/`probing` 去重 + 失败即停在未连接；**不做**指数退避（没有后台循环，退避无意义） |
| R3 | 自愈重连会把远端路径设为该主机最后路径；该路径可能已不存在 | §6.5 的回落 `SftpHome` 生效并记日志；不会卡在空列表 |
| R4 | `last_remote_dirs` 的 map 键是主机名（含空格/引号/中文/点） | **已实测**（§16.2 探针）：BurntSushi/toml v1.6.0 自动给非 bare 键加引号并转义，6 个含点/空格/引号/中文的键往返全等；§11.1 保留 TOML 往返用例 |
| R5 | `snapshotConfig()` 浅拷贝 + **map/slice 原地写** ⇒ 与锁外 TOML 编码并发读写 | §6.3.4 三条红线（map 整结构替换、slice 改新数组、锁内不 Normalize）+ §11.1 三条 `-race` 用例 + 结构性源码断言（BF14：`-race` 绿不能证明无原地写） |
| R6 | 排序每次点列头写一次全量配置（含编码 + rename） | 已做"值未变短路"；`saveSnapshot()` 现在是"读回 + 合并 + 写"，多一次 IPC，但排序点击是低频操作；若实测不可接受，再引入节流（本轮按 D11 不节流） |
| R7 | `SftpProbe` 的 user 恒 `""`：若某次会话是用非空 user 建的（sync/watch 会用 `rule.User`），探针会另建会话 | 不影响正确性（探针是真实往返）；已在 §6.1 声明"host 唯一标识远端位置"这一前提 |
| R8 | **legacy batch 探针的活性上界缺失**（原 v1.1 只说"无 ctx 上界"）：`BatchBackend.List` 经 `osutil.Runner`=`cmd.Run()` 无超时，探针一旦挂住 ⇒ `await` 永不返回 ⇒ `probing` 恒 true ⇒ **此后所有切标签探活被静默短路（自愈永久失效且无报错）** | 三层：§8.2 的 `Ctrl.Probe` batch 分支用 goroutine + `ctx` select **+ 每 host 单飞**；§8.3 的 `probeTimed` 客户端超时（超时=未知，`finally` 复位 `probing`）；§11.3 F2 验收。**残留**：挂死的 batch 子进程本身仍靠 ssh 自身超时回收（不为 legacy 后端改 runner 装配，见 §8.2 理由） |
| R9 | 空白右键在 `.pane` 根上接管 `contextmenu`，可能误伤其它原生菜单需求（比如未来新增可编辑控件） | handler 里对 `input/textarea/[contenteditable]:not([contenteditable="false"])` 放行、对 `.pos-wrap` 收起弹层不弹菜单；新增可编辑控件时补进该选择器（注释写明） |
| R10 | 行菜单去掉「新建文件夹」是行为变更，老用户肌肉记忆会失效 | 已与用户确认（D6）；若实机反馈不便，一行加回 |
| R11 | `remoteOpFailed` 的替换点漏一个远程 catch，则那一类失败不会自愈 | §11.2 的**分类型覆盖口径**钉死（纯远程函数 / 混合面板函数 / 新增的 `:767` 与 `:163`）；实施时逐个核对 §8.4 的 12 项清单 |
| R12 | **`SftpHome` 改写 last path（审核 BF1，Blocking）** ⇒ 单主机用户的最后路径被静默改成 home，且验收 C2 看不出来 | 已按 §6.3.1 修（内部 `recordRecentSFTP` 一律不写 last path）；§11.1 加 `recordRecentSFTP(...,false)` 回归用例 + §11.3 C4 端到端回归 |
| R13 | **`AddBookmark`/`RemoveBookmark` 的 slice 原地复用是同类既有 `-race` 缺陷**（`app.go:1324`/`:1332`） | **已按用户决定纳入本轮**（§6.3.4 第 3 条，四个函数统一改造）；§11.1 加 `AddBookmark ‖ saveConfig`、`RemoveBookmark ‖ saveConfig` 两条 `-race` 用例；范围记录见 §15 D21 |
| R14 | `last_remote_dirs` 的键不含 user：同一 host 的不同 user 会共用一条记忆 | 视图范围内 user 恒 `''` ⇒ 不触发；已写进 §6.1 前提声明，扩展到 sync/watch 前必须先改键 |
| R15 | 前端探针超时把"未知"当"失效"会误触发重连 | `probeTimed` 超时**不改状态、不重连**，只复位 `probing`；需要 `finally` 保证（R8 的同一处代码） |

---

## 13. 后续子项（另行评审，不在本轮）

- **`AddBookmark`/`RemoveBookmark` 的同类 slice 竞态**（R13）：**已并入本轮**（用户 2026-10-10 决定），见 §6.3.4 第 3 条与 §15 D21 —— 本条不再作为后续子项。
- **`updateTunnel` 等其它直接写 `a.cfg` 的路径的并发审计**：本轮未覆盖（规格不声称全仓并发安全）。
- **`last_remote_dirs` 的键纳入 user**（R14）：扩展到 sync/watch 之前必须做。
- **探活耗时再优化**：若 §11.3 F1 实测偏慢，改为后台探测 + 结果异步落地。
- **失败的远程操作自动重放**（本轮明确不做）。
- **SFTP 连接健康的心跳 / 多主机并行探测**（本轮明确不做）。

## 14. 关键文件清单

**新增**
- `frontend/src/stores/sftpui.js`（+ `sftpui.test.js`）
- `frontend/src/utils/sort.js`（+ `sort.test.js`）、`frontend/src/utils/remotepath.js`（+ `remotepath.test.js`）
- `frontend/src/components/filePane.render.test.js`（SSR 真渲染）
- `frontend/src/views/sftpMenuWiring.test.js`、`frontend/src/views/sftpRecoverWiring.test.js`
- 本规格文档

**改动**
- `internal/config/store.go`（`SftpUIState` + `AppConfig.SftpUI` + `Normalize` 挂点）+ `internal/config/store_test.go`
- `internal/sftp/ctrl.go`（`Ctrl.Probe`）、`internal/sftp/session.go`（`Pool.Probe` 预算缺陷修复）+ 对应测试；**不改** `internal/sftp/api.go` 的接口（仅可加注释）
- `app.go`（`GetSftpUI`/`SaveSftpUI`/`SftpProbe`；`AddLocalRecent`/`AddRemoteRecent` 的新写法；`AddBookmark`/`RemoveBookmark` 的 slice/锁改造；`recordRecentSFTP` 加 `rememberLast`；`cfgMu` 注释）+ `app_test.go`
- `frontend/src/views/SftpView.vue`（刷新入口、菜单矩阵、`actionsFor` 置灰、恢复点与顺序、共享落点、自愈、排序接线、`cleanItem`/`onMoveDrop` 的 catch 改造）
- `frontend/src/components/FilePane.vue`（右键触发面、排序 props/emit 含 `arrow()`）
- `frontend/src/components/ContextMenu.vue`（禁用样式）
- `frontend/src/stores/locations.js`（最后路径内存镜像）
- `frontend/src/components/filePaneWiring.test.js`、`frontend/src/stores/locations.test.js`（扩展）
- `frontend/wailsjs/go/main/App.js`、`App.d.ts`、`frontend/wailsjs/go/models.ts`（`wails generate module -tags webkit2_41` 重生成）

## 15. 决策记录

| 时间 | 决策 | 选择 |
|---|---|---|
| 本轮 | 「区分有选中/无选中」的语义 | A：空白右键只给面板级动作；行右键给选中项动作 + 刷新 |
| 本轮 | 排序状态粒度 | A：本地一份 + 远程一份 |
| 本轮 | 最后路径范围 | A：远程每主机一条 + 本地全局一条 |
| 本轮 | 右键「刷新」范围 | B：右键刷本面板，工具栏刷双侧 |
| 本轮 | 远程未连接时的刷新 | A：远程侧入口置灰；工具栏刷双侧时本地照刷、远程跳过 |
| 本轮 | 状态存储位置 | 方案 1：新增 `[sftp_ui]` 段 + `GetSftpUI`/`SaveSftpUI` |
| 本轮 | 休眠唤醒后的自愈强度 | B：探活 + 自动重连一次 |
| 本轮 | 设计路径判定 | 架构级（配置结构 + 新绑定 + 交互模型 + 连接状态自愈）→ 先出本规格，评审通过后由 writing-plans 出实施计划 |

## 16. 审核记录

### 16.1 第一阶段：作者自审（9 项，已修正）

自审方法：逐条把规格里的断言回到源码复核（`file:line`），并检查各节之间是否自相矛盾、是否漏掉实现前提。发现并修正如下：

| # | 严重度 | 问题 | 修正位置 |
|---|---|---|---|
| 1 | **Blocking（设计引入的回归）** | 「主目录」依赖"空 `pendingPath` = home"（`SftpView.vue:310`），而新增的记忆路径优先级会把它抢走 ⇒ 点「主目录」回到上次目录 | §6.4 落点优先级 + 新增 `pendingHome` 显式标志；新增 D15 与验收 E1 |
| 2 | Important | `.pane` 的 `contextmenu` 处理器原稿对 `li.up` 不 yield ⇒ `..` 行被"行处理器 + 面板处理器"各发射一次 = 双菜单 | §5.2 改为"对任何 `li` 无条件 yield" |
| 3 | Important | 规格未把 `SftpUI.Normalize()` 挂进 `AppConfig.normalize()`（`store.go:211`）⇒ 手改配置的非法值永远不会被归一 | §6.1 新增"归一挂点"段 + §11.1 专项用例 |
| 4 | Important | `SaveSftpUI` 是整结构覆盖，原稿未写"前端必须带全字段"⇒ 只发 sort 会清空两个路径字段（`settings.js:115` 已为 `SetSettings` 记过同样的坑） | §6.3 新增"整结构覆盖红线" + §7.1 强制唯一出口 `saveSnapshot()` |
| 5 | Important | §6.5 的回落分支**永远无法触发**：`loadRemote`/`loadLocal` 内部吞错（`:226`/`:402`） | §6.5 新增"前提前置"：必须先抽出会抛错的 `fetchRemote`/`fetchLocal` |
| 6 | Important | §8.4 与 §11.2 自相矛盾：要求混合面板 catch 改走 `remoteOpFailed`，却又断言"这些函数体内不再出现裸 `err(e)`"——本地分支必须保留 `err(e)` | §8.4 新增"实现形状"；§11.2 改为分类型覆盖口径（纯远程函数 vs 混合面板函数） |
| 7 | Important | `verifyRemote` 未说明 `connected === false` 的早退理由 ⇒ 实现者可能去掉它，导致"连接本身失败"再触发一轮重连 | §8.4 注释写明 |
| 8 | Important | 本地面板的远程依赖动作置灰不一致：空白菜单「上传文件…」灰、行菜单「上传到远程」能点 | §5.3 统一规则（含"这是有意的小行为变更"说明） |
| 9 | Minor | 自愈重入只靠 `probing` 一个标志：探针 A 失败进入 `recoverConnection` 后，探针 B 仍可重入 | §8.3 新增 `recovering` 入口幂等 |

另有两处非缺陷的表述收紧：§6.3 补"锁内只赋值、锁外 `saveConfig`"规约；§11.1 把"短路不写盘"的判据从"文件 mtime"改成沿用既有 `app_test.go:1058-1093` 的 `TS` 判据（mtime 粒度会让断言不稳）。

### 16.2 第二阶段：干净 subagent 审核（2 个模块面 × 14 + 19 条，全部处置）

审核方式：两个**新上下文、不携带本轮对话背景**的只读 subagent，按模块拆包（后端/平台事实面、前端/交互面）；两者都被明确要求"**不要相信规格里的行号**，回当前工作区重新定位符号，行号漂移本身算 finding"。两份报告的关键事实由 Lead **独立复核**（读源码 + 跑 TOML 探针）后才采纳。

**两阶段共识的正确性证据**：两个审核者都确认 `git diff --stat 71e7498..HEAD -- frontend/ app.go internal/` 为空 ⇒ 规格引用的行号未漂移；唯一错的锚点是规格自己写错的 `settings.js:97`（实为 `:115`）。

#### 16.2.1 后端/平台事实面（14 条，全部接受）

| ID | 严重度 | 问题摘要 | 裁定与修法 |
|---|---|---|---|
| BF1 | **Blocking** | `SftpHome` 每次 connect 都调 `recordRecentSFTP` ⇒ 顺带写会把单主机用户的 last path 改写成 home；前端队首短路又让污染不被纠正 ⇒ **验收 C2 通过但盘上已错，下次启动永久 home** | 接受。§6.3.1 改为"只有前端面向的导航绑定写 last path"，内部 `recordRecentSFTP` 加 `rememberLast=false`；§11.1 加回归用例；§11.3 加 C4 端到端回归；R12 |
| BF2 | Important | "store 是 last_* 唯一持有者"不成立（后端 4 处传输 + SftpHome 会写），一次 `saveSnapshot()` 整结构回写就把较新值静默回滚 | 接受。§6.3.1 消掉越权写点；§6.3.3 要求 `saveSnapshot()` 先 `GetSftpUI()` 读回、只覆盖 sort 四字段；§7.2 补说明 |
| BF3 | Important | `GetSftpUI` 若在读锁内 `Normalize()`（会给 nil map 赋新头）⇒ 与并发写共享结构竞争，且 `-race` 用例抓不到 | 接受。§9 写死"锁内只值拷贝、解锁后 Normalize"；§11.1 加 `GetSftpUI ‖ SaveSftpUI` 竞态用例 |
| BF4 | Important | 规格说"slice 改动保持原样"，但 `kept := cfg.X[:0]` 与浅拷贝共享底层数组 ⇒ 真 `-race` | 接受。§6.3.4 第 3 条：改新数组 + 整段进写锁；**范围经用户 2026-10-10 决定扩大** —— `AddBookmark`/`RemoveBookmark` 一并修（R13/§15 D21），`-race` 加 4 条写入用例 |
| BF5 | Important | `AcquireList` 不用 ctx ⇒ 拨号 ≥5s 时健康连接被判失效、健康会话被关、误触发重连；且探针会置粘性 `Connected`（未声明副作用） | 接受。§8.2 修 `Pool.Probe`（过期但会话建出 ⇒ 新预算复核）+ 文档化副作用；§11.1 加 slow-dial 回归；R1 重写、F1 实测 |
| BF6 | Important | legacy batch 探针无活性上界 ⇒ `await` 永不返回 ⇒ `probing` 恒 true ⇒ 自愈永久静默失效 | 接受。§8.2 batch 分支用 goroutine + `ctx` select + 每 host 单飞（**v1.4 修订**：原先写的"改用 `osutil.NewCtxRunner()`"会牵动 batch 的 runner 装配，与 legacy 回退路径不成比例）+ §8.3 `probeTimed` 客户端超时（超时=未知、`finally` 复位）；R8 重写、F2 验收 |
| BF7 | Important | §11.1 的两条判据互相矛盾（既有用例构造下 map 为空，"短路⇒不写 map"又与意图冲突） | 接受。§11.1 判据重写为"无论队首是否同值，last path 必须 == path" |
| BF8 | Important | 启动顺序未写 ⇒ `sftpui.load()` 若排在 `loadLocal()` 之后，本地最后路径每次启动被抹成 cwd；且 `connect()` 未被 `loaded` 兜住 | 接受。§6.4 新增 B3（顺序硬约束）与 B4（`loaded` gate） |
| BF9 | Minor | 验收 C3 的配置路径写成 `config.toml`（不存在） | 接受。§11.3 C3 改为 `~/.config/sshore/sshore.toml` |
| BF10 | Minor | "反斜杠原样"其实是落盘转义形式 | 接受。§11.1 措辞改为"往返相等，只比解码后值"；并由探针实测佐证 |
| BF11 | Minor | TOML 示例把 bare key `prod-db` 写成带引号（与真实编码不符） | 接受。§6.1 示例键改为 `"10.99.152.18"` / `"prod db"`（实测编码即为带引号形态） |
| BF12 | Minor | "会波及全部测试替身"夸大；漏引更贴切的先例 | 接受。§8.2 改为引 `RecoverSwaps`/`CleanupParts`（`ctrl.go:118-130`）先例，并注明替身实际只有 2 个 |
| BF13 | Minor | `SaveSftpUI` 缺 `a.cfg == nil` 守卫 ⇒ 未 Init 时空指针、被 dispatcher 静默 recover | 接受。§9 补锁内 nil 守卫；§11.1 并入 `TestSftpBindingsFailFastBeforeInit` |
| BF14 | Minor | `-race` 是唯一判据但依赖时序，"绿"不能证明没有原地写 | 接受。§11.1 增加结构性源码断言（不出现 `a.cfg.SftpUI.LastRemoteDirs[` 赋值 / 写锁内构造新 map） |

#### 16.2.2 前端/交互面（19 条，全部接受）

| ID | 严重度 | 问题摘要 | 裁定与修法 |
|---|---|---|---|
| FF1 | **Blocking** | 规格只列了 `visible`，漏了同作用域 `arrow()`（`FilePane.vue:180-182`，模板调用三次）⇒ 删 ref 后渲染期 `ReferenceError`、整块面板白屏 | 接受。§7.3 明确必须一起改；§11.2 加"不得再出现 `sortKey.value`/`sortDir.value`"断言 |
| FF2 | Important | `onMoveDrop` 的 `targetItems` 加载在 `try` 之外 ⇒ 死链时 unhandled rejection、无日志无自愈，而覆盖口径仍会绿 | 接受。§8.4 清单新增 `:767`，并把"列表加载 + `askConflict`"纳入 `try`；§11.2 断言 `SftpList` 处于 `try` 内 |
| FF3 | Important | `cleanItem` 远程分支漏点，且错误被包成字符串 ⇒ 任何"无裸 `err(e)`"口径都看不见 | 接受。§8.4 清单新增 `:163`（按 `direction` 分派） |
| FF4 | Important | §5.3 与 §11.2 的断言互斥（本地面板也要用同一 disabled 表达式） | 接受。§5.1/§11.2 改为"限定在刷新按钮标签内" |
| FF5 | Important | 第三个入口（过滤栏批量下拉 `actionsFor`）未置灰 ⇒ 自审修正 8 的分裂仍在 | 接受。§5.3 统一 `remoteDep` 判据并覆盖 `actionsFor` 的 `upload`/`upload-picked` |
| FF6 | Important | 行菜单「刷新」走 `doAction` 会先 `sel.single()` ⇒ 违反"刷新保留选中" | 接受。§5.1 要求 `refresh` 分支在 `sel.single` 之前 return；§11.2 用 `indexOf` 钉顺序 |
| FF7 | Important | `.posmenu` 内右键 ⇒ 两个 z-index 1000 浮层同屏；`.hdrow`/`.fbar` 落点未定义 | 接受。§5.2 新增 `.pos-wrap` 分支（`closePos()` + 不弹菜单）+ 落点归类补充 |
| FF8 | Important | `syncConnection` 未连接分支与 `connect()` 落点不同 ⇒ 切主机再切回会落到 home，与 D7/D16 矛盾 | 接受。§6.4 B5：抽 `utils/remotepath.js` 的 `resolveRemoteTarget` 供两处共用 + `utils/remotepath.test.js` |
| FF9 | Important | §10 要求"走既有 `err()`"，但 store 调不到组件内 `err()`；payload 键名映射未定义（camelCase 会被静默忽略 ⇒ 字段清零） | 接受。§7.1/§10：store **抛出**、调用方 `catch` 后 `err()`；§11.2 断言 payload 逐字含六个 snake_case 键 |
| FF10 | Important | 对测试能力的判断不完整：仓库已有 **SSR 真渲染**通道（`TransferQueue.render.test.js`）可实证行序/箭头/`@visible`；且"无 `setInterval`"会误伤既有 `:892` 时钟 | 接受。§11.2 新增 `filePane.render.test.js`（SSR）+ `utils/sort.test.js` 纯函数；"只重连一次"的断言限定在 `recoverConnection` 体内 |
| FF11 | Minor | "grep `SaveSftpUI` 只命中 store 与 wailsjs"与"store 单测必须 import 它"冲突 | 接受。§6.3.3 红线改为"允许 store、`*.test.js`、`wailsjs/**`" |
| FF12 | Minor | "空 `names` 已是 no-op"不成立（`needsConfirm` 会因 `hiddenSelected>0` 弹 0 任务框） | 接受。§5.3 补 `doAction` 顶部的空选择防御，并改述理由 |
| FF13 | Minor | `settings.js:97` 锚错（注释在 `:115`） | 接受。已改（§6.3.3 与 §16.1 同步修正） |
| FF14 | Minor | §8.3 步骤 6 的"成功/失败"判据未定义；`host` 变更早退会误记"重连失败" | 接受。§8.3 步骤 6 改为"判据 = `connected.value`"，`host` 变更早退静默；§10 同步 |
| FF15 | Minor | 口径不一致：`:112` 会吞掉"用户取消"的失败并按链路信号处理；`:254` 因 `connected===false` 恒早退，是死接线 | 接受。§8.4：`:112` 在 `cancelRequested` 时跳过探活；`:254` 移出覆盖点（保留 `err(e)`） |
| FF16 | Minor | "行菜单只在已连接时出现"不成立（`retryItem` 会让 `remoteItems` 在断开时被填出） | 接受。§5.3 更正 + §8.5 新增"远程操作成功后回同步 `connected=true`" |
| FF17 | Minor | 镜像短路"安全"的等式隐含前提是 `loaded === true` | 接受。§7.2 改述（并说明 load 失败时不做兜底的理由） |
| FF18 | Minor | `sftpui.load()` 在 `onMounted` 的 await 链位置未规定 ⇒ 可能冲掉刚写的镜像 | 接受。与 BF8 合并为 §6.4 B3 |
| FF19 | Minor | loading 期间 `pointer-events:none` 让行右键落到面板级；`[contenteditable]` 会放行 `contenteditable="false"` | 接受。§5.2 选择器改 `:not([contenteditable="false"])` + 把 loading 边界写进实现注释 |

#### 16.2.3 Lead 的独立佐证与未验证边界

- **独立复核过的审核事实**（Lead 自己跑）：`SftpHome`→`recordRecentSFTP`（`app.go:1119-1130`）；`AcquireList` 函数体内无 `ctx` 使用；`osutil.NewCtxRunner()` 存在（`runner.go:36-42`）且 `app.go:212` 已在用；`DefaultConfigPath` = `<userconfig>/sshore/sshore.toml`（`store.go:351-357`）；`TestSftpBindingsFailFastBeforeInit` 用 `NewApp()`（`app_test.go:1282`）；`RecoverSwaps`/`CleanupParts` 是 Ctrl 层类型断言（`ctrl.go:118-130`）；`kept := a.cfg.{Local,Remote}Recent[:0]`（`app.go:1353/1380`）；`settings.js:115` 是那句注释。
- **TOML 探针（把 R4 从"未实测"变成"已实测"）**：用 `github.com/BurntSushi/toml v1.6.0` 跑最小程序（源在 `/tmp`、构建缓存用仓库自带 `.gocache`、**仓库零改动**），对含点/空格/引号/中文的 6 个键 + Windows 反斜杠路径做往返：编码结果如 §6.1 所示（非 bare 键自动加引号、`\` 与 `"` 转义），`map 长度 6→6 全等`、`LastLocalDir 相等`、`[app]` 段保留；同时实测**老配置（无 `[sftp_ui]` 段）读入后 `LastRemoteDirs == nil`、`SortLocalDir == 0`** ⇒ 直接证明 §6.2 的 `Normalize` 必要性。
- **两个审核者共同的未验证项**（本规格如实保留，不得当成已验证）：未跑 `go test`/`-race`/`npm run build`/`make ci`（沙箱 build cache 只读；审核为只读约束）；`wails generate module` 未实际执行；真机 A1–F2 全部未执行；Windows/WebView2 的 contextmenu/`e.target` 语义未实测；Wails v2.15.0 对 `map[string]string` **字段**的 models.ts 精确产物未生成核对（只有同版本既有 `Record<string, string>` 先例 + imported-map golden 旁证）。
