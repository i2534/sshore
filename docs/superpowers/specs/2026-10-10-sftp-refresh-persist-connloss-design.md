# SFTP：双侧刷新 + 右键菜单分级 + 最后路径/排序持久化 + 连接失效自愈 设计

> 状态：v1.1 · 用户已逐节确认（§15 决策记录）· **第一阶段作者自审已完成（9 项修正，见 §16）** · 第二阶段干净 subagent 审核进行中 · **待用户评审** · 尚未实施
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

---

## 5. 交互设计

### 5.1 刷新

```
工具栏 [🔄 刷新]  ──► refreshAll()   = loadLocal() ⊕ (connected ? loadRemote() : skip)
右键菜单 [🔄 刷新] ──► refreshPane(pane) = 本地面板 loadLocal() / 远程面板 (connected ? loadRemote() : skip)
```

- 工具栏按钮去掉 `:disabled="!connected"`（本地半边总能刷）。
- `refreshAll` 用 `Promise.all` 并发两侧；两侧各自的 `localSeq`/`remoteSeq`（`SftpView.vue:205`、`:263`）继续负责丢弃过期响应，双击不会串。
- **刷新保留选中**：`loadRemote`/`loadLocal` 本来就不清选中（`SftpView.vue:218-228`、`:394-404`），本轮不改。
- 刷新失败按 §8 走 `remoteOpFailed` 复核（若确为链路失效则自愈）。

### 5.2 右键触发面（`FilePane.vue`）

- `.pane` 根新增 `@contextmenu="onPaneContext"`，**刻意不带 `.prevent`**，在 handler 内决定是否 `preventDefault()`：

```js
function onPaneContext(e) {
  // 1) 可编辑控件放行原生菜单（保住路径栏/过滤框的复制粘贴）
  if (e.target.closest && e.target.closest('input, textarea, [contenteditable]')) return
  // 2) 任何 li（含 '..' 行）都由行自己的 @contextmenu.prevent 处理器负责 —— 必须无条件 yield，
  //    否则 '..' 行会「行处理器 + 面板处理器」各 emit 一次 = 双菜单（自审修正 1）
  if (e.target.closest && e.target.closest('li')) return
  e.preventDefault()
  emit('context', { item: null, event: e })
}
```

- 行右键沿用既有 `@contextmenu.prevent="emit('context', { item: it, event: $event })"`（`FilePane.vue:241`）不变；`..` 行（`FilePane.vue:231-233`）**新增** `@contextmenu.prevent="emit('context', { item: null, event: $event })"`（按无选中处理），并靠上面的"li 无条件 yield"避免双发射。
- `FilePane.vue:29` 的 `emits` 数组已含 `'context'`，无需新增事件名。

### 5.3 菜单矩阵与置灰

| 右键位置 | 菜单项（按此顺序） |
|---|---|
| 本地面板 · 空白 | 🔄 刷新 · 📁 新建文件夹 · ⬆ 上传文件… |
| 本地面板 · 文件行 | ⬆ 上传到远程 (N) · ✏ 重命名 · 🗑 删除 (N) · ── · 🔄 刷新 |
| 远程面板 · 空白 | 🔄 刷新 · 📁 新建文件夹 · ⬆ 上传文件… |
| 远程面板 · 文件行 | ⬇ 下载…(N) · ⬆ 上传… · ✏ 重命名 · 🗑 删除 (N) · ── · 🔄 刷新 |

- 置灰规则统一为 `remoteGate = menu.pane === 'remote' && !connected`：远程面板的 🔄 刷新 / 📁 新建文件夹 / ⬆ 上传文件… 均 `:disabled="remoteGate"`。
- **依赖远程连接的本地侧动作统一置灰（自审修正 2）**：本地面板的「⬆ 上传文件…」（`uploadPicked()` 的目标是 `posixJoin(remotePath.value, name)`）与本地面板行菜单的「⬆ 上传到远程」（`runBatch` 的 targetDir 是 `remotePath.value`）都要 `:disabled="!connected"`。这是**有意的小行为变更**：与其点下去才报错，不如提前置灰；两处的规则必须一致，否则同一面板会出现"空白菜单灰、行菜单能点"的分裂。
- 远程面板的文件行菜单只可能在**已连接**时出现（`disconnect()`/`onHostChange()` 会清空 `remoteItems`，`:387-393`、`:231-236`），其 🔄 刷新仍复用同一 `remoteGate` 计算（结果恒为 false，不写特例）。
- 「✏ 重命名」在行菜单里**保持现状语义**（重命名被右键的那一项；右键项不在集合内时先 `sel.single()`），**不**照搬 `actionsFor()` 里 `n !== 1` 的置灰 —— 这是刻意的范围控制，避免顺带改既有行为。
- 无选中的批量动作（下载/上传到远程/重命名/删除）**不进**空白菜单；`doAction()` 里对应分支在 `names` 为空时已是 no-op（`SftpView.vue:680-702`），不需要新增防御。

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

落盘形态：

```toml
[sftp_ui]
last_local_dir = "/home/lan/work"
sort_local_key = "modTime"
sort_local_dir = -1
sort_remote_key = "name"
sort_remote_dir = 1
[sftp_ui.last_remote_dirs]
"prod-db" = "/var/log/nginx"
```

`AppConfig` 新增字段（放在 `RemoteRecent` 之后，保持"位置类"字段聚拢）：

```go
SftpUI SftpUIState `toml:"sftp_ui" json:"sftp_ui"`
```

**兼容性**：老配置没有 `[sftp_ui]` 段 ⇒ 零值即默认（`LastLocalDir=""`、`LastRemoteDirs=nil`、sort 由 `Normalize` 补成 `name/1`），**不需要迁移**；首次因任何原因写盘后该段自然出现。

**归一挂点（自审修正 3，不能漏）**：必须把 `c.SftpUI.Normalize()` 加进现有的 `AppConfig.normalize()`（`internal/config/store.go:211`，现在只调 `c.App.Normalize()`）。`LoadConfig` 与 `DefaultAppConfig` 都走 `normalize()`，所以这是唯一的挂点；漏了它，手改配置写进去的非法 sort 值会一路飘到 UI。`GetSftpUI`/`SaveSftpUI` 仍各自再调用一次 `Normalize()`（双保险：绑定层可能被直接调用而不经过 `LoadConfig`）。

### 6.2 归一（`SftpUIState.Normalize()`）

- `SortLocalKey`/`SortRemoteKey` 白名单 `name|size|modTime`，非法（含空）→ `name`。
- `SortLocalDir`/`SortRemoteDir` 归一到 `±1`：`< 0` → `-1`，否则 `1`（零值 0 不合法 ⇒ 变 `1`）。
- `LastRemoteDirs` 为 nil → 空 map（避免前端拿到 null）。
- **不设条数上限**：host 集合来自 ssh config，规模有限；主机被删/改名后遗留条目无害（不清扫，避免误删用户数据）。

### 6.3 写入时机与并发红线

**最后路径（零新增前端调用）**：`AddLocalRecent`（`app.go:1343`）与 `AddRemoteRecent`（`app.go:1367`）在**队首同值短路之后**、`saveConfig()` 之前，顺带写入 `a.cfg.SftpUI.LastLocalDir` / `LastRemoteDirs[host]`，随后同一次 `saveConfig` 落盘。

- 结果：一次成功导航仍然是**一次**全量编码 + rename（与今天一致），不引入写放大。这是选它而不选"前端另调 `SaveSftpUI`"的唯一理由（后者会让每次导航变成两次写盘，而 `AddRemoteRecent` 的注释已经点过批量场景的写放大问题）。
- 短路语义天然正确：队首同值 = 该路径已经是最后路径，无需重写。

**排序状态**：前端 `setSort(pane, key, dir)` 改内存后调 `SaveSftpUI`（值未变则短路，见 §7）。

**整结构覆盖红线（自审修正 4）**：`SaveSftpUI(s)` 是**整结构替换**（与 `SetSettings` 同构），前端每次调用**必须带全字段**（`last_local_dir` + `last_remote_dirs` + 两侧 sort）。只发 sort 字段会把两个路径字段一起清零 —— 这正是 `frontend/src/stores/settings.js:97` 注释里为 `SetSettings` 记下的坑。因此：**`stores/sftpui.js` 必须是这些字段在前端的唯一持有者**，`SaveSftpUI` 的调用点只允许出现在该 store 内部（其他组件一律通过 store action 改状态）。实施时用一条接线测试钉死：`grep 'SaveSftpUI'` 只命中 store 与 wailsjs 绑定文件。

**锁规约**：`cfgMu` 内**只构造新值 + 赋值**，绝不在锁内做 IO。三条写入路径（`SaveSftpUI`、`AddLocalRecent` 顺带写、`AddRemoteRecent` 顺带写）都必须"写锁内赋值 → 解锁 → `saveConfig()`"，与 `SetSettings`（`app.go:483-500`）的既有规约逐字一致；`AddLocalRecent`/`AddRemoteRecent` 现有的无锁 slice 改动保持原样，只把新增的 `SftpUI` 写入放进一个独立的加锁小段。

**并发红线（必须写进实现注释）**：`a.cfg.SftpUI` 一律**整结构赋值**（在 `cfgMu` 写锁内构造新值后赋给字段），**绝不原地修改 `LastRemoteDirs` 这个 map**。原因：`snapshotConfig()`（`app.go:649`）是浅拷贝，map 头会被共享；若原地写该 map，就会与锁外正在跑的 TOML 编码（`saveConfig`，`app.go:659`）并发读写同一个 map —— 那是 `-race` 能抓到、生产上会 panic 的真缺陷。构造"旧 map + 新键"的新 map 再整体赋值即可规避。

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

其余（`SftpHome` 兜底、`connected=true`、`addRemoteRecent`）保持不动。

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
  async load()                  // GetSftpUI() 一次；失败兜底默认 + 返回错误由调用方记日志
  lastPathFor(pane, host)       // remote → lastRemoteDirs[host] || ''；local → lastLocalDir || ''
  async setSort(pane, key, dir) // 值未变直接 return（不写盘）；否则改内存 + saveSnapshot()
  noteLocalPath(path)           // 内存镜像（由 locations store 调用，不额外 IPC）
  noteRemotePath(host, path)    // 同上
  async saveSnapshot()          // 唯一的 SaveSftpUI 出口：**整结构**发全字段（§6.3 红线）
```

- **`saveSnapshot()` 是本 store 的私有出口**：`SaveSftpUI` 只允许出现在这里（§6.3 的整结构覆盖红线），任何新功能要改 SFTP UI 状态都走 store action，禁止在组件里直接调绑定。
- `load()` 把后端返回当作**整体快照**写入（`lastLocalDir`/`lastRemoteDirs`/两侧 sort 一次成型），并对 sort 再做一次前端白名单兜底（后端已归一，前端只防"绑定返回畸形"）。

单测仿 `frontend/src/stores/locations.test.js`（`vi.mock('../../wailsjs/go/main/App')` + `setActivePinia`）。

### 7.2 最后路径的内存镜像

`stores/locations.js` 的 `addLocalRecent`/`addRemoteRecent`（`:43-57`）成功后顺带调 `useSftpUIStore().noteLocalPath/noteRemotePath`：

- 后端已经把它落盘了；前端镜像只是为了让**同会话内**的"断开→重连"能立刻拿到正确路径（否则要等重启）。
- 纯内存、**零新增 IPC**；`locations.js` → `sftpui.js` 单向依赖，无环。
- 短路分支（队首同值 return）不更新镜像是安全的：队首同值 = 镜像值本就相同。

### 7.3 排序状态上收到 SftpView（单一真源）

- `FilePane.vue` 删除内部 `sortKey`/`sortDir` ref（`:124-125`），改为 props `sortKey`（默认 `'name'`）、`sortDir`（默认 `1`）；`sortBy(key)` 变为 `emit('sort', { key, dir: props.sortKey === key ? -props.sortDir : 1 })`（`:129-136` 的语义逐字保留：同列取反、换列归升序）。
- `FilePane.vue:29` 的 `emits` 数组新增 `'sort'`。
- `visible`（`:138-154`）的排序实现不变，只把读源从 ref 换成 props；`shown`/`visibleKeys`/`@visible` 链路（决策 32 的"可见顺序单一真源"）**完全不动**。
- `SftpView.vue` 用 `sftpui.sort.local/remote` 作为两侧 props，`@sort="onSort('local', $event)"` → `sftpui.setSort(pane, key, dir)`。

---

## 8. 连接失效自愈

### 8.1 根因（三条事实的组合，非推测）

1. `Connected(host)` 是**粘性连接意图**（`internal/sftp/gosftp.go:1491-1494`）：握手成功即 `true`，只有显式 `Disconnect`/`CloseAll` 才清 ⇒ 链路死掉后 `SftpConnected` 仍返回 `true`。
2. `syncConnection()` 首行 `if (!h || connected.value) return`（`SftpView.vue:900`）⇒ 已连接时**永不复核**，UI 永远停在"已连接"。
3. 池的 idle 上限是 **2 条/键**（`session.go:104`），唤醒后这两条死会话会被 `AcquireList` 优先复用 ⇒ 正好产生用户日志里相隔 6 秒的**两次**失败（`mkdir`、`ls`）。失败时 `reusable=false` 会丢会话，所以第三次操作通常会新建会话而成功 —— 但 UI 状态已经骗了用户，且期间的操作都失败。

### 8.2 后端：暴露真探活（**只加一个绑定**）

- `internal/sftp`：新增 `func (c *Ctrl) Probe(ctx context.Context, host, user string) bool`：
  - `*GoBackend` → `g.pool.Probe(ctx, host, user)`（现成能力：真实往返 + `probeTimeout=5s` 上界 + 失败丢弃死会话，`session.go:288-305`）。
  - 其它后端（legacy batch）→ 退化为一次真实列目录（`c.List(host, user, "/")` 成功即 true）。**不做** ControlMaster 专用探测（legacy 后端不做过度投资，见 R8）。
  - **不改** `Backend` 接口：接口新增方法会波及全部测试替身（`NewCtrlForcedBackend` 的注入型替身），而 `Probe` 只有 Go 后端有原生实现 ⇒ 类型分派放在 `Ctrl` 层。
- `app.go`：新增绑定 `func (a *App) SftpProbe(host string) bool` → `a.sftp.Probe(context.Background(), host, "")`（`a.sftp == nil` 时 false，与 `SftpConnected` 同构守卫，`app.go:983-988`）。
  - user 恒传 `""`：SFTP 视图的所有调用（`SftpList`/`SftpGet`/…）都用 `''`，`markConnected` 也已**不再记录**最近握手凭据（`gosftp.go:156-160`），没有别的来源可用；探针用 `""` 与视图自己的会话键 `host\x00""` 一致。
- **不新增**"清理失效连接"的绑定：直接复用 `SftpDisconnect`（`Ctrl.Disconnect` = `clearConnected` + 丢该 host 空闲会话，`gosftp.go:1498-1501`）。这一步是**必须**的：否则 `syncConnection()` 的未连接分支会读到粘性 `true` 又翻回"已连接"，还会顺手把远程路径设成 `SftpHome`（`:900-911` 的既有行为）。
- 最坏时延：ssh `ConnectTimeout=10` + `probeTimeout=5s`（异步 await，不阻塞渲染）。**未实测**具体上界，列入 R1。

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
    try { if (!(await SftpProbe(h))) await recoverConnection(h) }
    catch { /* 探针本身失败不改变现状 */ }
    finally { probing = false }
    return
  }
  // 未连接分支保持现状（SftpConnected 校正 + 恢复路径 + loadRemote）
}
```

`recoverConnection(h)`：

0. **入口幂等（自审修正 9）**：`if (recovering) return`，进入后置 `recovering = true`、`finally` 复位。`probing` 只防"探针并发"，管不住"探针 A 失败后 recoverConnection 还在跑、探针 B 又失败"的重入 —— 两个标志各管一段，缺一不可。
1. 日志 warning：「远端连接已失效（主机休眠或网络中断），正在重连…」。
2. `await SftpDisconnect(h)`（清粘性意图 + 丢死会话；失败静默，不能因此中断自愈）。
3. `connected.value = false`；`remoteItems.value = []`；`sel.clear(remoteSelection)`（与 `disconnect()` 的降级形态一致 ⇒ §5.3 的置灰规则自动生效）。
4. `pendingPath.value = sftpui.lastPathFor('remote', h)`（回到该主机最后路径，而不是 home —— 与 §6 协同）。
5. `await connect()`（既有实现：握手 → 落点 → 列目录 → 记最近）。
6. 成功 → 日志 info「已重新连接 <host>」；失败 → `connected.value = false` + 日志 error「重连失败：…，请手动点「连接」」。
7. **只重连一次**：不循环、不起定时器；同一时刻只有一个 `recovering` 流程（`recovering` 标志去重，防止反复切标签叠加）。

### 8.4 前端：远程操作失败后复核（D14）

新增 `remoteOpFailed(e)`：先 `err(e)` 记原文（保持现状），再 `verifyRemote()`：

```js
async function verifyRemote() {
  const h = host.value
  // connected === false 时直接返回：这是「连接本身刚失败」的场景（例如 recoverConnection
  // 自己的 connect() 失败），此时再探活/再重连只会打出第二轮无意义尝试（自审修正 7）。
  if (!h || !connected.value || probing) return
  probing = true
  try { if (!(await SftpProbe(h))) await recoverConnection(h) } finally { probing = false }
}
```

替换**远程相关**的 `err(e)` catch 点（`SftpView.vue` 当前行号）：`112`（`dispatchTransfer`，两端方向都涉及远端）、`226`（`loadRemote`）、`254`（`connect`）、`302`（`goRemote`）、`312`（`goRemoteHome` 的 `SftpHome`）、`606`（`removeSelected` 远程分支）、`629`（远程 `renameItem`）、`649`（远程 `mkdirIn`）、`668`（`uploadPicked`）、`796`（`onMoveDrop` 远程分支）。

**明确排除**（它们不是链路健康信号）：

- `136` `SftpTransferCancel` 失败（取消语义，与链路无关）。
- `389` `disconnect()` 的 `SftpDisconnect` 失败（那正是要断开）。
- `402`/`875`/`211` 等本地/配置路径失败。
- pane 分支为 local 的 catch（`:606`/`:629`/`:649`/`:796` 的本地侧、`:875` `CopyLocal`）。

**实现形状（自审修正 8 —— 与 §11.2 的断言口径必须一致）**：`:606`/`:629`/`:649`/`:796` 是**单 catch 覆盖两个面板**的混合点，改造后形如

```js
} catch (e) {
  if (pane === 'local') err(e)
  else await remoteOpFailed(e)
}
```

⇒ 这些函数体内**仍会保留 `err(e)`**（本地分支），也会保留 `removeSelected`/`renameItem` 等调用链上的直接调用（例如 `renameItem` 自身按 pane 分支返回）。所以接线测试**不能**断言"这些函数里没有 `err(e)`"，正确口径见 §11.2。

### 8.5 降级形态（与既有"未连接"一致）

`connected=false` + `remoteItems=[]` + 清远程选中 ⇒ 远程面板回到"未连接"形态：刷新/新建文件夹/上传文件…置灰，工具栏刷新只刷本地。用户点「连接」即手动恢复；因为 §6 的最后路径已落盘，手动重连也会回到该主机最后路径。

---

## 9. 接口契约

| 层 | 新增/改动 | 说明 |
|---|---|---|
| `internal/config` | `SftpUIState` + `AppConfig.SftpUI` + `(*SftpUIState).Normalize()` | §6.1/§6.2；老配置零值兼容，无迁移 |
| `internal/sftp` | `Ctrl.Probe(ctx, host, user) bool` | §8.2；GoBackend 走 `pool.Probe`，其它后端退化列目录；**不动 `Backend` 接口** |
| `app.go` 绑定 | `GetSftpUI() config.SftpUIState` | 读锁下取副本；`a.cfg == nil` 时返回 `Normalize()` 过的零值 |
| `app.go` 绑定 | `SaveSftpUI(s config.SftpUIState) error` | `Normalize()` → `cfgMu` 写锁内整结构赋值 → 解锁后 `saveConfig()`（照 `SetSettings` 的"锁内不做 IO"规约，`app.go:483-500`） |
| `app.go` 绑定 | `SftpProbe(host string) bool` | §8.2 |
| `app.go` 改动 | `AddLocalRecent`/`AddRemoteRecent` 顺带写 `SftpUI` | §6.3；整结构替换新 map |
| `frontend/wailsjs/go/{main/App.js,main/App.d.ts,models.ts}` | **必须重生成并入库** | `wails generate module -tags webkit2_41`（仓库根执行）；`models.ts` 会多出 `SftpUIState` 类 |
| 前端新增 | `stores/sftpui.js`（+ 测试） | §7.1 |
| 前端改动 | `FilePane.vue`（右键触发面 + 排序上收）、`ContextMenu.vue`（禁用样式）、`SftpView.vue`（刷新入口/菜单矩阵/恢复点/自愈）、`stores/locations.js`（内存镜像） | §5/§7 |

---

## 10. 错误处理与日志文案

| 场景 | 级别 | 文案（逐字实现，便于验收 grep） |
|---|---|---|
| 探测失败触发自愈 | warn | `远端连接已失效（主机休眠或网络中断），正在重连…` |
| 自愈成功 | info | `已重新连接 <host>` |
| 自愈失败 | error | `重连失败：<原因>，请手动点「连接」` |
| 记住的远程路径不可用 | warn | `上次的远程目录 <path> 已不可用，已回到 <home>` |
| 记住的本地路径不可用 | warn | `上次的本地目录 <path> 已不可用，已回到 <cwd>` |
| `GetSftpUI` 读取失败 | error | 走既有 `err()`（原文），前端回落默认值 |
| `SaveSftpUI` 写盘失败 | error | 走既有 `err()`（原文）；内存状态**不回滚**（下次变更会再试） |

- 探针本身抛错（绑定异常）不改状态、只记 debug 级：不能因为"探活工具坏了"就把用户翻成未连接。
- 自愈过程中的 `SftpDisconnect` 失败静默（其失败不影响后续 `connect()` 重新握手）。

---

## 11. 测试策略与验收

### 11.1 Go 单测

| 用例 | 断言 |
|---|---|
| `SftpUIState.Normalize` 表驱动 | 非法/空 sort key → `name`；dir 0/2/-7 → `1/1/-1`；nil map → 非 nil 空 map |
| TOML 往返 | `SftpUI` 编码 → 解码逐字段相等；含**带空格/中文/引号的 host 名**（map 键转义）与 Windows 风格本地路径（反斜杠原样） |
| 老配置兼容 | 无 `[sftp_ui]` 段的 TOML 读入 → `Normalize` 后等于默认值；不产生迁移标记 |
| `SaveSftpUI` 持久化 | 调用后重新 `LoadConfig` 读回；且 `Tunnels`/`Syncs`/`Bookmarks`/`RemoteRecent` **逐项未受影响** |
| `SaveSftpUI` 整结构语义 | 传只含 sort 的 SftpUIState ⇒ 路径字段被清零（**记录成测试钉住这个危险语义**，与前端"必须带全字段"的红线互为对照）；传全字段 ⇒ 六字段逐一落盘 |
| `AppConfig.normalize` 挂点 | 写一份含 `sort_local_key = "bogus"` / `sort_local_dir = 7` 的 TOML → `LoadConfig` 读回后已归一到 `name/1`（防"Normalize 没接上"的静默漏改） |
| `AddRemoteRecent` 顺带写 | 调一次后 `cfg.SftpUI.LastRemoteDirs[host] == path`；**队首同值时短路**（沿用既有 `app_test.go:1058-1093` 的判据：`TS` 不刷新 + 不产生重复项，**不用文件 mtime**——mtime 粒度会让该断言不稳定） |
| `AddLocalRecent` 顺带写 | 同上（`LastLocalDir`） |
| `GetSftpUI` 守卫 | `a.cfg == nil` 时返回归一化零值、不 panic |
| `Ctrl.Probe` | 注入强制后端（`NewCtrlForcedBackend`）→ 走退化列目录分支；`*GoBackend` + fake `DialFunc` → 走 `pool.Probe`（失败会话被丢弃） |
| `SftpProbe` 绑定 | 未连接的 host → `false`；`a.sftp == nil` → `false`、不 panic |
| 竞态 | 上述用例纳入 `go test -race`（`make ci` 已带 `-race`），专门覆盖"`SaveSftpUI` 与 `saveConfig` 并发"（防 R5 的 map 竞争） |

### 11.2 前端 vitest

| 文件 | 断言 |
|---|---|
| `stores/sftpui.test.js`（新增） | `load()` 填充；`lastPathFor` 两侧分支；`setSort` 值未变**不调** `SaveSftpUI`、值变化时调用并带全字段；`SaveSftpUI` reject 时不崩且内存保留 |
| `stores/locations.test.js`（扩展） | `addRemoteRecent`/`addLocalRecent` 成功后 `sftpui` 镜像同步更新，且**未**新增 IPC（mock 调用次数） |
| `components/filePaneWiring.test.js`（扩展） | 排序读 props：`FilePane.vue` 不再有 `const sortKey = ref(`；`emits` 含 `'sort'`；`sortBy` emit 的 dir 语义（同列取反/换列归 1）；空白右键 handler 存在、对可编辑控件放行，且**对任何 `li` 无条件 yield**（防双菜单；断言不得再要求 `classList.contains('up')` 之类的例外分支） |
| `views/sftpMenuWiring.test.js`（新增，读源码钉形状） | 工具栏按钮调 `refreshAll` 且无 `:disabled="!connected"`；`refreshPane` 存在且两面板共用；菜单含 `doAction('refresh')`；`remoteGate` 置灰接线；两个面板的 `@context` 都接到 `showMenu`；空白分支 emit `item: null`；「主目录」走显式 `pendingHome`（防 §6.4 回归） |
| `views/sftpRecoverWiring.test.js`（新增） | `syncConnection` 已连接分支调 `SftpProbe`；`recoverConnection` 内 `SftpDisconnect` → `pendingPath` → `connect` 的顺序；只重连一次（无 `while`/`setInterval`）；**正确的覆盖口径（自审修正 8）**：逐个核对 §8.4 的 10 个远程落点所在函数——要求"远程分支可达 `remoteOpFailed(`"，混合面板函数（`removeSelected`/`renameItem`/`mkdirIn`/`onMoveDrop`）额外要求存在 `pane === 'local'` 的 `err(e)` 分支；纯远程函数（`dispatchTransfer`/`loadRemote`/`goRemote`/`goRemoteHome`/`uploadPicked`）则要求体内不再出现裸 `err(e)`（`connect` 按 B2 单独核对） |
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
| C3 | 配置段形态 | `cat ~/.config/sshore/config.toml` 有 `[sftp_ui]` 与 `[sftp_ui.last_remote_dirs]`，且 `[app]`/`[[tunnels]]` 等既有内容未丢 |
| D1 | 休眠/断链自愈 | 连接后杀链路（Linux：`ss -K dst <host> dport = :22`；无 root 时用 `iptables -A OUTPUT -p tcp --dport 22 -j DROP` 再删规则）→ 点标签切走再切回 → 日志出现「远端连接已失效…正在重连…」与「已重新连接」→ 面板回到最后路径且可操作；失败时停在未连接并给出明确日志 |
| D2 | 操作失败复核 | 断链后直接点「新建文件夹」→ 报错原文仍在日志，且随后自动自愈（不需要手动断开重连） |
| D3 | 不误伤 | 权限错误（远端目录不可写）**不得**触发断开/重连：探针返回 true ⇒ 只有原始报错 |
| E1 | 「主目录」回归守卫（防 §6.4 修 5 的回归） | 已连到 `/var/log`（有记忆）时点「主目录」→ **必须到 home**，不得被记忆路径抢走 |
| E2 | 行为变更确认（B1） | 导航到 `/var/log` → 点「断开」→ 点「连接」→ 回到 `/var/log`（不是 home）；与 C2 一起构成 D7 的完整证据 |

**证据要求**：D1/D2 必须给出日志原文（含时间戳）+ 自愈前后截图；C2 必须给出两个主机各自路径栏的截图。`make ci` 全绿日志同样入库。

---

## 12. 风险与未证实项

| # | 风险 | 处置 |
|---|---|---|
| R1 | `SftpProbe` 最坏时延未实测（半死连接上 `ReadDirContext` 可能吃满 `probeTimeout=5s`，加上 `dial` 的 `ConnectTimeout=10`） | 真机（D1）实测并记录实际耗时；若 > 10s 影响体感，则把探活从"切标签同步等待"改为"先乐观保持 + 后台探测"（本轮不做，先量） |
| R2 | 自动重连在无网络时会失败 → 反复切标签会反复尝试 | 只重连一次 + `recovering`/`probing` 去重 + 失败即停在未连接；**不做**指数退避（没有后台循环，退避无意义） |
| R3 | 自愈重连会把远端路径设为该主机最后路径；该路径可能已不存在 | §6.5 的回落 `SftpHome` 生效并记日志；不会卡在空列表 |
| R4 | `last_remote_dirs` 的 map 键是主机名（可能含空格/引号/中文/点） | BurntSushi/toml 会自动引号化键；§11.1 的 TOML 往返用例逐字覆盖 |
| R5 | `snapshotConfig()` 浅拷贝 + map 原地写 ⇒ 与锁外 TOML 编码的并发读写 | §6.3 红线（整结构替换）+ `-race` 专项用例 + 实现注释 |
| R6 | 排序每次点列头写一次全量配置（含编码 + rename） | 已做"值未变短路"；若实测写入耗时不可接受，再引入节流（本轮按 D11 不节流） |
| R7 | `SftpProbe` 的 user 恒 `""`：若某次会话是用非空 user 建的（当前 UI 不会，sync/watch 可能），探针会另建会话 | 不影响正确性（探针是真实往返）；文档记录该前提 |
| R8 | legacy batch 后端的 Probe 无 ctx 上界（`List` 会起子进程） | 仅在用户显式回退 batch 时出现；本轮接受（默认后端是 gosftp），列入后续子项 |
| R9 | 空白右键在 `.pane` 根上接管 `contextmenu`，可能误伤其它原生菜单需求（比如未来新增可编辑控件） | handler 里对 `input/textarea/[contenteditable]` 一并放行；新增可编辑控件时补进该选择器（注释写明） |
| R10 | 行菜单去掉「新建文件夹」是行为变更，老用户肌肉记忆会失效 | 已与用户确认（D6）；若实机反馈不便，一行加回 |
| R11 | `remoteOpFailed` 的替换点漏一个远程 catch，则那一类失败不会自愈 | §11.2 的**分类型覆盖口径**（纯远程函数要求无裸 `err(e)`；混合面板函数要求远程分支可达 `remoteOpFailed(`）钉死；实施时逐个核对 §8.4 的行号清单 |

---

## 13. 后续子项（另行评审，不在本轮）

- **batch 后端探活上界**（R8）。
- **探活耗时优化**：若 R1 实测偏慢，改为后台探测 + 结果异步落地。
- **失败的远程操作自动重放**（本轮明确不做）。
- **SFTP 连接健康的心跳/多主机并行探测**（本轮明确不做）。

## 14. 关键文件清单

**新增**
- `frontend/src/stores/sftpui.js`（+ `sftpui.test.js`）
- `frontend/src/views/sftpMenuWiring.test.js`、`frontend/src/views/sftpRecoverWiring.test.js`
- 本规格文档

**改动**
- `internal/config/store.go`（`SftpUIState` + `AppConfig.SftpUI` + `Normalize`）+ `internal/config/store_test.go`
- `internal/sftp/ctrl.go`（`Ctrl.Probe`）+ 对应测试；如需要，`internal/sftp/api.go` 仅加注释（**不加接口方法**）
- `app.go`（`GetSftpUI`/`SaveSftpUI`/`SftpProbe`，`AddLocalRecent`/`AddRemoteRecent` 顺带写）+ `app_test.go`
- `frontend/src/views/SftpView.vue`（刷新入口、菜单矩阵、恢复点、自愈、排序接线）
- `frontend/src/components/FilePane.vue`（空白右键触发面、排序 props/emit）
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
| 4 | Important | `SaveSftpUI` 是整结构覆盖，原稿未写"前端必须带全字段"⇒ 只发 sort 会清空两个路径字段（`settings.js:97` 已为 `SetSettings` 记过同样的坑） | §6.3 新增"整结构覆盖红线" + §7.1 强制唯一出口 `saveSnapshot()` |
| 5 | Important | §6.5 的回落分支**永远无法触发**：`loadRemote`/`loadLocal` 内部吞错（`:226`/`:402`） | §6.5 新增"前提前置"：必须先抽出会抛错的 `fetchRemote`/`fetchLocal` |
| 6 | Important | §8.4 与 §11.2 自相矛盾：要求混合面板 catch 改走 `remoteOpFailed`，却又断言"这些函数体内不再出现裸 `err(e)`"——本地分支必须保留 `err(e)` | §8.4 新增"实现形状"；§11.2 改为分类型覆盖口径（纯远程函数 vs 混合面板函数） |
| 7 | Important | `verifyRemote` 未说明 `connected === false` 的早退理由 ⇒ 实现者可能去掉它，导致"连接本身失败"再触发一轮重连 | §8.4 注释写明 |
| 8 | Important | 本地面板的远程依赖动作置灰不一致：空白菜单「上传文件…」灰、行菜单「上传到远程」能点 | §5.3 统一规则（含"这是有意的小行为变更"说明） |
| 9 | Minor | 自愈重入只靠 `probing` 一个标志：探针 A 失败进入 `recoverConnection` 后，探针 B 仍可重入 | §8.3 新增 `recovering` 入口幂等 |

另有两处非缺陷的表述收紧：§6.3 补"锁内只赋值、锁外 `saveConfig`"规约；§11.1 把"短路不写盘"的判据从"文件 mtime"改成沿用既有 `app_test.go:1058-1093` 的 `TS` 判据（mtime 粒度会让断言不稳）。

### 16.2 第二阶段：干净 subagent 审核（待填）

审核面按模块拆包（后端/平台事实面 + 前端/交互面），每个 subagent 只拿"规格文件路径 + 仓库路径 + 复核要求"，**不携带本轮对话背景**；结论回来后在下一修订版填此节，并逐条处置（接受/驳回 + 理由）。
