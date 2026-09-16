# SFTP 增强：选择/批量/拖拽 + 导航（过滤·深搜·位置）设计

- **日期**: 2026-09-16
- **状态**: 待评审（第 2 稿，已按「作者自审 + 两轮独立只读复审」修订）
- **关联**: `docs/superpowers/specs/2026-08-25-sshkit-design.md`（SFTP 子系统基线）、`docs/superpowers/specs/2026-09-10-remote-folder-sync-design.md`（`ListMany` 与「缺失 key = 未知」不变量、冲突队列先例）
- **路径判定**: 架构级（前端交互模型重构 + 后端新增能力 + 配置结构变更）
- **设计过程**: 经可视化草图逐屏确认（草稿在 `.superpowers/brainstorm/`，已在 `.gitignore`，不入库）

> **修订说明（第 2 稿）**：第 1 稿经① 作者自审、② 两个独立只读审查者（后端/平台事实面、前端/交互面）复审后修订。修订要点：
> - **B1 取消契约**：Wails 绑定在 `err != nil` 时**丢弃第一个返回值**（已核实 `internal/frontend/dispatcher/calls.go:51-60`：`if err != nil {...} else { Result = result }`），故「取消返回部分结果 + ctx.Err()」前端拿不到结果 → 增加 `Cancelled` 字段 + app.go 吞掉 `context.Canceled`（§3 决策 18、§5.1、§8.2）。
> - **删除批的失败隔离**：`rm`/`rmdir` 失败会中止整批（已核实 `man sftp`：中止列表含 `rm`；源码 `sftp.c` 中 `rmdir` 同样 `return err`）→ 每条命令加 `-` 前缀（仓库先例 `internal/sftp/ctrl.go:338-340`），并按 stderr 反查失败路径（§3 决策 27、§5.1）。
> - **glob 元字符**：sftp 的 `rm` 会对路径做远端 glob 展开（`remote_glob`），而 `quoteArg` 不转义 `* ? [` → 不可撤销的删除必须先保守拒绝（§3 决策 28、§13 R9）。
> - **迁移写回**：原「新字段为空即迁移」会与 `SaveConfig` 全量编码、`recordRecentSFTP` 写旧字段互相打架，且用户清空后旧数据会复活 → 改为显式迁移标记 + 启动落盘（§3 决策 21、§10.2）。
> - **真实 API 名**：`DisableWebViewDrop`（不是 `DisableWebViewDragAndDrop`），且它会全局关掉 webview 拖放接收、把本源拖入一起废掉，**不得**当隔离手段（§13 R4）。绑定重生成命令补 `-tags`（§3 决策 26）。
> - **前端补空洞**：键盘/焦点基础设施与守卫（§3 决策 31）、可见顺序单一真源（§3 决策 32）、书签/最近位置绑定（§3 决策 30）、系统拖入的路径语义（§3 决策 29）、验收拆成「可脚本化 / 人工」（§12.3）。
> - **驳回一条复审意见**：复审 m1 认为 `OnFileDrop` 的签名是 JS 版 `(callback, useDropTarget)`。核实：那是**前端 JS 版** API（`frontend/wailsjs/runtime/runtime.d.ts:239`）；本设计在 `OnStartup` 里用的是 **Go 版** `runtime.OnFileDrop(ctx, func(x, y int, paths []string))`（`pkg/runtime/draganddrop.go:9-32`），第 1 稿写法正确，不改。

---

## 1. 目标

把 SFTP 模块从「单文件、右键逐个操作」升级为「可多选、可批量、可拖拽」的文件管理体验，并补齐「在本地与远端目录树中定位文件」的导航能力。

本轮交付两个工作流：

- **P1 选择与投递模型**：多选（Ctrl / Shift / Ctrl+A）+ 批量操作（下载·上传·删除）+ 四类拖拽投递 + 批量冲突策略 + 增强的传输队列。
- **P2 导航能力**：双侧当前目录即时过滤、双侧递归深搜、每面板「书签 + 快速位置」。

## 2. 非目标

- **不做**传输进度百分比 / 传输取消 / 失败重试 / 断点续传 / 校验和 —— 共用同一个「流式执行底座」，列为后续子项 P3（§14）。
- **不做**远端文本编辑、chmod/权限修改、软链接可视化。
- **不做**从窗口拖出到系统文件管理器 —— Wails 只提供拖入 API，无导出 API。**不伪造**该能力。
- **不做**并发批量传输：批量一律**顺序**执行。
- **不做**传输队列持久化（重启即清空，沿用现状）。
- **不做**目录「整树替换 / 镜像删除」语义（那是「文件同步」模块的领域）：目录冲突一律是**跳过 / 并入 / 另存副本**（§6.3）。
- **不改** `internal/sftp` 现有方法的语义，本轮只**新增**方法。
- **不改** `internal/sync` 的边界。

## 3. 已确认决策

| # | 决策点 | 结论 |
|---|---|---|
| 1 | 本轮范围 | P1 + P2 合并为一个交付单元；P3（流式传输底座）另行讨论 |
| 2 | 面板头布局 | 方案 A：**紧凑单行**（`标题 · 路径 · 📍位置▾ · ☆ · 🔍深搜 · 已选 N 项▾`） |
| 3 | 批量操作的落点 | **下沉到各自面板**的「已选 N 项 ▾」下拉；**工具栏零新增** |
| 4 | 批量操作的对象 | 就是**该面板**的选择集合；目标目录 = **对侧面板当前目录** |
| 5 | 右键菜单 | 保留同一套批量动作：右键项在选中集合内 → 整批；不在 → 先 `single()` 该项（设锚点）再操作 |
| 6 | 多选交互 | 单击=单选（设锚点）；Ctrl/Cmd+单击=切换；Shift+单击=锚点到该项区间；Ctrl+A=**聚焦面板**全选；空白处单击=清空；切目录=清空该面板；Delete=批量删除 |
| 7 | 冲突策略 | 批量投递前**统一询问一次**（另存副本 / 跳过已存在项 / 覆盖全部），**默认选中「跳过已存在项」**；剩余项复用该选择；**单文件手势不弹窗** |
| 8 | 批量失败语义 | 单项失败**不中断**，记录原因继续；结束汇总「成功 / 跳过 / 失败」并提供可复制的失败清单 |
| 9 | 拖拽路径（四条全要） | ① 双栏互拖（远程↔本地，含多选与目录）② 系统 → 远程面板 = 上传 ③ 系统 → 本地面板 = 复制 ④ 面板内拖到子目录 = 移动 |
| 10 | 拖拽非法落点 | 同侧非目录行、把目录拖进自身或其子树 → 前端直接拒绝，不发请求 |
| 11 | 递归上传 | 新增后端 `PutRecursive`（`sftp put -r`）；目录**下载**复用现有 `GetRecursive` |
| 12 | 移动 | 复用现有 `Rename`（本地 `RenameLocal`、远端 `SftpMove`）。**本地移动前先判目标是否存在**：Windows 上 `os.Rename` 目标存在会直接失败（`app.go:463-465`），不会静默覆盖 |
| 13 | 系统拖入实现 | Go 版 `runtime.OnFileDrop(ctx, cb)`（在 `OnStartup` 注册）→ `EventsEmit("files:dropped", {x, y, paths})`；**不用** JS 版同名 API（签名不同：`(callback, useDropTarget)`） |
| 14 | 过滤 | 双侧面板内**即时过滤**（纯前端、不区分大小写子串 + 命中高亮），只影响显示 |
| 15 | 过滤与选择的关系 | 被过滤隐藏的项**仍保留在选中集合**，但面板头如实显示「已选 N 项（含 M 项被过滤）」，投递确认框同样提示 |
| 16 | 深搜入口 | **双侧面板头各一个 `🔍 深搜`**；共用同一浮层形态，标题写明 `主机 : 路径` |
| 17 | 深搜实现与深度语义 | 远程：`ListMany` 上的 BFS（批次 64、批间检查 `ctx`）；本地：Go `WalkDir`。**深度语义与仓库一致**：`0`=仅本层、`N`=N 层、`-1`=无限（`store.go:73`、`watch/scan.go:135`）；**绑定层**把 `0` 当「未设置」归一为 5，UI 不提供无限入口。匹配器在 `internal/sftp` 内自实现，**不复用** `watch.MatchExclude`（依赖方向禁止 + 它区分大小写） |
| 18 | 深搜取消契约 | 绑定方法返回非 nil error 时 Wails 会丢结果，故**取消不算错误**：`SearchOutcome.Cancelled=true` + 部分结果，app.go 把 `context.Canceled` 转成 `(outcome, nil)`；`Truncated` 仅表示命中上限 |
| 19 | 位置模型 | 每个面板自己的「📍位置 ▾」= 上半区**书签** + 下半区**最近**（自动、去重、最近在前、上限 20）；路径旁 `☆` 收藏当前目录 |
| 20 | 移除旧交互 | 删除 header 全局「最近位置」下拉及其 UI；**同一并移除** `ListRecentSFTP` 的调用与 `recordRecentSFTP` 写旧字段的逻辑 |
| 21 | 配置结构与迁移 | 新增 `bookmarks` / `local_recent` / `remote_recent`；迁移用**显式标记** `legacy_migrated`（不靠「新字段为空」判断，避免用户清空后旧数据复活），startup 后落盘一次 |
| 22 | 传输队列 | 展示 `状态 / 方向 源→目标 / 大小 / 耗时`；`statusClass` 显式映射：完成→done、失败→err、**跳过→skip（新增灰色样式）**、其余→doing；底部汇总 + 可复制失败清单；**不放**取消/重试假按钮 |
| 23 | 远端目录删除 | 新增 `RemoveRecursive`。现有 `Remove` 走裸 `rm`，对目录**无效**（`ctrl.go:256-261`）——批量删除含目录必须走新方法，并顺带修掉单项目录删除的既有缺口 |
| 24 | 键盘守卫 | 焦点在输入框 / 浮层 / 对话框内时，`Delete`、`Ctrl+A`、`Esc` **不**触发文件操作 |
| 25 | 目标判重 | 目标目录内容未加载时**先按需加载一次**；判重用**原始 items（未经 showAll 过滤）**，隐藏同名文件同样参与判重；`src` 与 `dst` 互为自身或子树 → 拒绝 |
| 26 | 绑定重生成 | 新增绑定方法后必须执行 `wails generate module -tags webkit2_41`（Makefile 全构建都带该 tag，见 `Makefile:12,49,57,63`；`-skipbindings` 不会自动更新） |
| 27 | 删除批的失败隔离 | 每条删除命令加 **`-` 前缀**（`-rm` / `-rmdir`），否则第一条失败即中止整批、留下半棵树；**不用 `@`**（会抑制回显，失去失败归因与"发到哪些路径"的证据）；失败按 stderr 逐行反查路径，沿用 `stderrListFailure` 的思路 |
| 28 | glob 元字符路径 | `rm`/`get`/`put` 的路径会被 sftp 做**远端 glob 展开**，而 `quoteArg` 不转义 `* ? [`：**删除**遇到含元字符的路径**直接拒绝**并在队列报「文件名含通配符，暂不支持删除」；`get`/`put` 允许但记 warn。转义方案待 §13 R9 的 e2e 探针确认后再放开 |
| 29 | 系统拖入路径语义 | 新增绑定 `StatPaths(paths) → [{path,name,isDir,size}]`（`os.Lstat`）；前端用 `name` 与目标目录拼接；**目录递归展开由后端负责**（`PutRecursive`/`CopyLocal` 直接接受目录），前端不展开 |
| 30 | 书签/最近位置绑定 | 新增 `ListLocations` / `AddBookmark` / `RemoveBookmark` / `AddLocalRecent` / `AddRemoteRecent`；**单写者约定**：位置数据只经这些方法写入（后端内部 `saveConfig`），前端**不**通过 `GetSettings/SetSettings` 写位置（那是整对象覆盖，会互踩） |
| 31 | 键盘/焦点基础设施 | `FilePane` 根节点 `tabindex="0"` + 聚焦态样式 + `@focus` 上报；`SftpView` 维护 `focusedPane`；keydown 分派与守卫抽成纯函数 `utils/keys.js`（`actionFor(ev)` → `'delete'|'select-all'|'escape'|null`，`isEditableTarget(el)` 为守卫判定）以便单测 |
| 32 | 可见顺序单一真源 | `可见顺序 = showAll ∩ filter` 排序后的 keys，由 `FilePane` 通过 `@visible` 上抛；选择区间、批量顺序、拖拽多选集合**共用**这一个来源，禁止各处再算一份 |
| 33 | 破坏性确认的强度 | 本轮删除确认 = 数量 + 「含 M 项被过滤」+「目录将递归删除」提示，**不引入 fingerprint**（远端取树快照成本高）；TOCTOU 风险记入 §13 R7。交互样式可沿用同步模块确认框的视觉，但不改其逻辑 |

## 4. 架构与模块边界

```
SftpView.vue（编排：键盘分派、批量投递、冲突询问、拖拽落点、双侧深搜浮层、事件订阅）
   │  纯函数层（可单测）
   ├── utils/selection.js   选择模型（Ctrl/Shift/锚点/全选/清空）
   ├── utils/batch.js       任务规划 + 冲突策略 + 结果汇总
   ├── utils/dnd.js         拖拽载荷 + 落点判定 + 合法性
   ├── utils/keys.js        键盘分派与守卫（新）
   └── stores/locations.js  书签与每面板快速位置
   │  Wails 绑定
   ▼
app.go（接线：绑定方法 + 事件转发；不写业务算法）
   ├── internal/sftp    +PutRecursive +RemoveRecursive +Search
   ├── internal/localfs +Search（WalkDir）、+Copy（新包）
   └── internal/config  书签/最近位置 + 显式迁移标记
```

### 4.1 后端新增

- `internal/sftp`：`PutRecursive`、`RemoveRecursive`、`Search`（含本地匹配器），复用既有 `run`/`buildBatch`/`ListMany`/`quoteArg`，不新增命令通道。
- `internal/localfs`（新包）：`Search`（`filepath.WalkDir`，限深/限条/可取消）、`Copy`（文件与目录递归）。**只依赖标准库**，不依赖 `sftp`/`watch`。
- `internal/config`：`Bookmark`/`RecentLocal`/`RecentRemote` + `MigrateLegacyRecents`（显式标记）。

### 4.2 前端新增

- `src/utils/{selection,batch,dnd,keys}.js`：**纯函数**，不碰 DOM/Wails，全部单测覆盖。
- `src/stores/locations.js`：书签与两侧快速位置。
- `src/components/SearchOverlay.vue`、`ConflictDialog.vue`：`AppDialog` 只有 `confirm|prompt` 两态（`AppDialog.vue:5-11`），承载不了三选项，故新建。

### 4.3 改动现有代码

| 文件 | 改动 |
|---|---|
| `frontend/src/views/SftpView.vue` | 删除 header 全局「最近位置」下拉与 `recents/applyRecent`（`pendingPath` **保留**为内部状态）；接入编排/冲突框/拖拽/深搜浮层；**键盘分派层**与 `focusedPane`；`onActivated/onDeactivated` 成对订阅/退订 `files:dropped` 与搜索进度事件（`App.vue:22-24` 已有"不退订会叠加注册"的教训） |
| `frontend/src/components/FilePane.vue` | 多选渲染（选中集合、锚点、聚焦态、`tabindex`）、面板头四个控件、过滤框、拖拽源/目标高亮；`selected` prop 改为「选择集合 + 锚点」；新增 `@visible` 上抛可见顺序 |
| `frontend/src/components/TransferQueue.vue` | 方向 / 源→目标 / **跳过态** / 失败原因 / 汇总行；`statusClass` 补 `skip` 分支与样式 |
| `frontend/src/stores/settings.js` | 确认**不**承载书签/最近位置（避免与整对象写入互踩） |
| `main.go` | `options.App.DragAndDrop = &options.DragAndDrop{EnableFileDrop: true}`；`OnStartup` 注册 Go 版 `runtime.OnFileDrop` |
| `app.go` | 新增绑定（§5.3）；`recordRecentSFTP`/`ListRecentSFTP` 改造为 `remote_recent`/`local_recent`；新增事件用 `runtime.EventsEmit(a.ctx, ...)` **直接发**（现有 `Init(emit)` 只映射 `forward.Event` → `"log"`，见 `main.go:32-34`） |
| `internal/sftp/ctrl.go` | `PutRecursive`、`RemoveRecursive`、`quoteArg` 的 glob 处理口径 |
| `internal/config/store.go` | 新字段 + `legacy_migrated` + 迁移 |

### 4.4 依赖方向

`app.go → {sftp, localfs, config}`；`localfs` 不依赖任何内部包；前端纯函数层不依赖 `wailsjs`。**禁止** `internal/sftp → internal/watch`（既有方向是 `watch → sftp`）。

## 5. 接口契约

### 5.1 `internal/sftp` 新增

```go
// PutRecursive 递归上传本地目录到远端目录（sftp put -r）。
// 预期语义：把 local 目录及其内容放到 remoteDir 之下，即 remoteDir/<base(local)>/...；
// remoteDir 必须已存在（不存在则报错，不隐式建树）。
// ⚠️ 该语义待 §13 R1 实测确认后写入实现与注释（put -r 对已存在同名目录的行为未证实）。
func (c *Ctrl) PutRecursive(host, user, local, remoteDir string) error

// RemoveRecursive 递归删除远端路径（文件或目录）。
// 实现：BFS 收集「文件」与「空目录」，在**一个 sftp 批处理**里先删文件、再按深度倒序删空目录。
// 硬约束：
//   1) 每条命令加 '-' 前缀（-rm / -rmdir）：否则任一条失败即中止整批（man sftp），
//      留下半棵树——这与"批量删除必须成功"直接冲突（仓库先例 ctrl.go:338-340）。
//   2) 不用 '@' 前缀：保留回显，便于按 stderr 反查是哪些路径失败。
//   3) 路径含 glob 元字符（* ? [）→ 返回明确错误，不做删除（sftp 会做远端 glob 展开）。
// 失败语义：部分删除不回滚；返回错误里带上失败路径与远端原文，由调用方汇总。
func (c *Ctrl) RemoveRecursive(host, user, path string) error

// SearchHit 是一次深搜的命中项。
type SearchHit struct {
    Path    string `json:"path"`    // 相对 root；root 自身不出现
    IsDir   bool   `json:"isDir"`
    Size    int64  `json:"size"`
    ModTime string `json:"modTime"`
}

// SearchOutcome 是一次深搜的结果快照。
// 绑定约束：调用方（app.go）**不得**在返回非 nil error 时期待前端拿到结果
// ——Wails 的 dispatcher 在 err != nil 时不写 Result（calls.go:51-60）。
type SearchOutcome struct {
    Hits       []SearchHit `json:"hits"`
    Scanned    int         `json:"scanned"`    // 已成功列出的目录数（仅进度，不受 limit 约束）
    Unreadable int         `json:"unreadable"` // 缺失 key 的目录数（"缺失 key = 未知"不变量）
    Truncated  bool        `json:"truncated"`  // 命中数达到 limit 而提前停止
    Cancelled  bool        `json:"cancelled"`  // ctx 被取消：Hits/Scanned 为**已扫描部分**
}

// Search 在 root 下 BFS 搜索；pattern 为空则返回全部条目。
// 深度语义与仓库一致：0=仅本层，N=N 层，-1=无限（调用方决定；UI 只传 5）。
// ctx 在批次之间检查：取消时返回已扫描的部分结果 + ctx.Err()。
// limit 为**命中条数**上限（<=0 视为 500）。
func (c *Ctrl) Search(ctx context.Context, host, user, root, pattern string,
    maxDepth, limit int, onProgress func(scanned int)) (SearchOutcome, error)
```

**匹配规则**（`internal/sftp` 内的 `matchName(pattern, rel, base string) bool`，不复用 `watch.MatchExclude`：依赖方向禁止，且后者区分大小写）：
- `pattern` 不含元字符 → **不区分大小写**的子串匹配（`base` 与 `rel` 都试）。
- 含 `*`/`?`/`[` → 双方先 `ToLower` 再 `path.Match`，保证两种模式大小写表现一致；`path.Match` 返回 `ErrBadPattern`（如 `[abc`）→ **回退为子串匹配**并记一条 warn 事件（不静默）。

**glob 元字符口径**（`rm`/`get`/`put` 共用）：sftp 会对这些参数做远端 glob（`GLOB_NOCHECK`）。因此删除路径含 `* ? [` 一律**拒绝并报错**（决策 28）；转义方案待 §13 R9 实测，未证实前不写进实现。

### 5.2 `internal/localfs` 新增

```go
type Hit struct {
    Path    string `json:"path"` // 相对 root
    IsDir   bool   `json:"isDir"`
    Size    int64  `json:"size"`
    ModTime string `json:"modTime"`
}
type SearchOpts struct { MaxDepth, Limit int } // 深度语义同 §5.1

// Search 用 filepath.WalkDir 搜索 root（不跟随符号链接；不可读目录跳过并计数）。
func Search(ctx context.Context, root, pattern string, o SearchOpts) (hits []Hit, skipped int, truncated bool, err error)

// Copy 复制文件或目录树（递归）；dst 为最终目标路径。
// 调用方负责按冲突策略决定覆盖/改名/跳过；本函数不做判重。
func Copy(src, dst string) error
```

### 5.3 Wails 绑定（`app.go`）

类型必须**带包名限定**（`App.d.ts` 用命名空间类型，如 `config.SyncRule`、`sftp.Item`）且新结构体**都带 json tag**（否则前端收到 PascalCase 字段，与 `parser.go:11-17` 的既有惯例不符）。

```go
// —— 深搜（两个方法，各自的请求类型不含 scope，避免"传了却没人校验"）——
type RemoteSearchRequest struct {
    ID       string `json:"id"`        // 前端序号：用于取消与丢弃过期结果
    Host     string `json:"host"`
    Root     string `json:"root"`
    Pattern  string `json:"pattern"`
    MaxDepth int    `json:"maxDepth"`  // 0 → 归一为 5；-1 → 无限（UI 不提供）；>0 原样
    Limit    int    `json:"limit"`     // <=0 → 500
}
type LocalSearchRequest struct { ID, Root, Pattern string; MaxDepth, Limit int } // json tag 同上

func (a *App) SftpSearch(req RemoteSearchRequest) (sftp.SearchOutcome, error)
func (a *App) LocalSearch(req LocalSearchRequest) (LocalSearchOutcome, error)
func (a *App) SearchCancel(id string)

// LocalSearchOutcome 与远程同形，命中项用 localfs.Hit。
type LocalSearchOutcome struct {
    Hits       []localfs.Hit `json:"hits"`
    Scanned    int           `json:"scanned"`
    Unreadable int           `json:"unreadable"`
    Truncated  bool          `json:"truncated"`
    Cancelled  bool          `json:"cancelled"`
}

// —— 传输与移动 ——
func (a *App) SftpPutRecursive(host, user, local, remoteDir string) error
func (a *App) SftpRemoveRecursive(host, user, path string) error // 新增：含目录的删除
func (a *App) SftpMove(host, user, oldPath, newPath string) error // 语义 = Rename
func (a *App) CopyLocal(src, dst string) error

// —— 系统拖入的路径信息 ——
type PathInfo struct {
    Path  string `json:"path"`
    Name  string `json:"name"`
    IsDir bool   `json:"isDir"`
    Size  int64  `json:"size"`
    Err   string `json:"err,omitempty"`
}
func (a *App) StatPaths(paths []string) []PathInfo // os.Lstat；单项失败不整体失败

// —— 位置（书签 / 最近）——
type Locations struct {
    Bookmarks    []config.Bookmark     `json:"bookmarks"`
    LocalRecents []config.RecentLocal  `json:"localRecents"`
    RemoteRecents []config.RecentRemote `json:"remoteRecents"`
}
func (a *App) ListLocations() Locations
func (a *App) AddBookmark(b config.Bookmark) error
func (a *App) RemoveBookmark(scope, host, path string) error
func (a *App) AddLocalRecent(path string) error
func (a *App) AddRemoteRecent(host, path string) error
```

**`user` 参数为何保留**：与既有 `SftpList/SftpPut/SftpGet...` 签名一致（当前前端恒传 `""`，实际登录用户由 ssh 配置决定）；本轮不改变这一约定。

### 5.4 事件与订阅

- 后端批量/深搜动作继续走 `forward.Event{SourceType:"sftp"}` → 日志面板（`logEvent`，含 stderr 原文）。
- **新增事件不经 `Init(emit)` 通道**（它只映射 `forward.Event` → `"log"`）：由 `app.go` 用 `runtime.EventsEmit(a.ctx, name, payload)` 直接发：
  - `sftp:search-progress` → `{id, scanned}`
  - `files:dropped` → `{x, y, paths}`
- **订阅生命周期**：`SftpView` 在 `onActivated` 订阅、`onDeactivated` 退订，且必须保存 `EventsOn` 返回的退订函数（`App.vue:22-24` 的既有教训：不保存会在 HMR/KeepAlive 重挂载时叠加注册）；搜索进度只在浮层打开期间订阅。

## 6. 选择与批量投递模型

### 6.1 选择模型（`utils/selection.js`）

```js
createSelection()            // { keys: Set, anchor: string|null }
single(sel, key)             // 单击/手势对象不在集合内：替换为单项并设锚点
toggle(sel, key)             // Ctrl+单击：切换并移动锚点
rangeTo(sel, keys, key)      // Shift+单击：锚点到 key 的区间（keys = 唯一的可见顺序，§3 决策 32）
all(sel, keys) / clear(sel) / isSelected(sel, key)
remove(sel, keys)            // 删除/移动完成后，把已消失的 key 从集合移除
```

- 两个面板各自持有独立选择对象；面板头下拉**只列可见项**，选中集合可含被过滤隐藏项。
- **可见顺序单一真源**：`FilePane` 用 `showAll ∩ filter` 排序后 `@visible` 上抛 keys；`rangeTo/all` 与批量顺序、拖拽集合都用它。排序变化后区间**不重算**（区间是"点选那一刻"的结果）。
- 切目录 / 切主机：`clear()`；**删除/移动完成后：`remove(sel, 已完成项)`**（含被过滤隐藏项），避免下一次批量携带已删除项。
- **焦点与键盘基础设施**：`FilePane` 根节点 `tabindex="0"`、`:class="{focus}"`、`@focus` 上报；`SftpView` 维护 `focusedPane` 并挂 `keydown`；`Ctrl+A`/`Delete`/`Esc` 先过守卫 `utils/keys.js#actionFor(ev)`（`ev.target` 命中 `input/textarea/select/[contenteditable]` 或位于浮层/对话框内 → 交还输入控件）。
- 手势统一：右键、拖拽的"对象不在集合内"一律先 `single()`（设锚点），再按集合语义操作（§3 决策 5/10）。

### 6.2 批量动作

| 触发 | 动作 |
|---|---|
| 本地面板「已选 N 项 ▾」 | 上传到远程、删除、重命名（多选置灰）、全选、清空 |
| 远程面板「已选 N 项 ▾」 | 下载到本地、删除、重命名（多选置灰）、全选、清空 |
| 右键菜单 | 同上；右键项不在集合内 → 先 `single()` |
| 键盘 | `Delete` 删除（带数量确认）、`Ctrl+A` 全选（聚焦面板）、`Esc` 清空/关浮层；输入框/浮层聚焦时由守卫让行 |

### 6.3 冲突策略（`utils/batch.js`）

```js
planTasks({ pane, items, sourceDir, targetDir, direction }) // 'download'|'upload'|'copy'|'move'
classify(tasks, existingNames)                              // → { clean, conflicts }
applyPolicy(conflicts, policy)                              // 'skip'(默认) | 'overwrite' | 'rename'
summarize(results)                                          // → { ok, skipped, failed, failures[] }
```

- **判重来源**：**原始 items（未经 `showAll` 过滤）**，隐藏同名文件同样参与判重（否则会静默覆盖隐藏文件）。跨面板投递用对侧当前目录；面板内移动 / 拖到子目录用**目标子目录**，其内容未加载 → **先按需加载一次**再判重（加载失败则不执行并提示）。当前目录正在刷新时批量入口置灰。
- **弹框条件**：`存在冲突 ∨ 选中集合含被过滤隐藏项` → 弹一次 `ConflictDialog`；两者都不满足 → 直接执行。**无冲突但含被过滤隐藏项**时，对话框只显示摘要 + 「其中 M 项被过滤隐藏」，无三选项。默认选中「跳过已存在项」。
- **单文件手势**（右键单项 / 拖拽单项）：不弹窗，**有意直接覆盖**——注意这是**新行为**：当前代码 path 上完全没有判重（`SftpView.vue:207-232,290-321`），不是"沿用现状"。
- **目录 vs 目录**：语义固定为 `跳过 / 并入 / 另存副本`（`name (2)`），**不承诺整树替换**；"并入"对上传即 `put -r` 的行为，**以 §13 R1 实测结论为准**，实测前 `PutRecursive` 注释与单测都要标注。
- 另存副本命名：`name (2).ext`、`name (3).ext`…（保留扩展名；目录无扩展名）。

### 6.4 执行与失败语义

- **顺序**执行；每项开始/结束各写一条队列记录（含方向与源→目标）。
- 单项失败：记录原因（含远端 stderr 原文）并继续下一项。
- 结束后：刷新目标面板；`remove(sel, 成功项)`；队列底部汇总 `成功 N · 跳过 N · 失败 N`；`复制失败清单` 输出 `源 → 目标：原因` 多行。
- **删除**：确认框显示**实际将删项数** + 「含 M 项被过滤隐藏」+「目录将递归删除」。本地目录走 `DeleteLocal`（`os.RemoveAll`），远端目录走 `SftpRemoveRecursive`——否则远端目录删除必然失败。
- **移动**：本地移动前先判目标是否存在（Windows `os.Rename` 目标存在即失败，`app.go:463-465`）；存在则走冲突对话框（跳过/覆盖/另存副本），"覆盖"在 Windows 上等价于先删目标再改名——**该分支待实测**（§13 R2）。
- 取消：本轮**不支持中断进行中的批量**（P3 才有底座）；对话框的 `取消本次批量` 只在开始前生效。

## 7. 拖拽模型

### 7.1 四条路径

| # | 手势 | 语义 | 后端 |
|---|---|---|---|
| 1 | 面板互拖（远程 → 本地） | 下载（目录递归） | `SftpGet` / `SftpGetDir` |
| 1' | 面板互拖（本地 → 远程） | 上传（目录递归） | `SftpPut` / `SftpPutRecursive` |
| 2 | 系统 → 远程面板 | 上传到该远程面板当前目录 | 同上 |
| 3 | 系统 → 本地面板 | **复制**到该本地目录 | `CopyLocal` |
| 4 | 面板内拖到子目录行 | **移动**（`old → targetDir/basename`） | `RenameLocal` / `SftpMove` |

- 拖起项若在选中集合内 → 投递**整批**；否则只投递该项（先 `single()`）。
- **系统拖入的路径语义**：`paths` 只有绝对路径字符串，无类型信息 → 先调 `StatPaths` 拿到 `name/isDir/size`；目标文件名用后端给的 `name`（不要前端自己切分隔符）；本地路径分隔符 → 远端 POSIX 路径的转换只在"本地面板 → 远程面板"方向上做，且**由后端负责**（前端只传转义后的绝对路径）。目录**不展开**：直接把目录交给 `PutRecursive`/`CopyLocal`。

### 7.2 落点判定（`utils/dnd.js`）

```js
payloadFor(pane, items)                    // HTML5 dragstart 载荷（pane + names）
hitPane({ x, y }, paneRects)               // 系统拖入坐标 → 'local' | 'remote' | null
canDropInto(sourcePane, targetPane, item)  // 面板内移动合法性
```

- 面板互拖：整块对侧面板为放置区（虚线高亮 + 中央文案 `⬇ 下载 3 项 → /path`）。
- 面板内移动：仅**目录行**可作落点；`item` 为目录且 `target` 位于其子树内 → 拒绝。
- 系统拖入：命中本地面板 = 复制、命中远程面板 = 上传；落在工具栏/日志区 → 忽略并提示。
- 非法落点（同侧非目录行、自嵌套、未连接时的远程面板）→ 红框 + 文案，`dragover` 阶段就 `preventDefault` 掉。
- 自嵌套判定在**发请求前**完成（含"把面板当前目录本身拖进自己的子目录"）；远端侧同理。

### 7.3 平台事实与待证实项

- **已核实**：`EnableFileDrop` 在 Wails v2.15.0 的 Linux/Windows/Darwin 三端均有实现，最终汇入 Go 版 `runtime.OnFileDrop(ctx, cb)`（`pkg/runtime/draganddrop.go:9-32`）。
- **不要混淆**：前端 JS 侧也有 `runtime.OnFileDrop(callback, useDropTarget)`（`frontend/wailsjs/runtime/runtime.d.ts:239`），本设计**不用**它。
- **待实测**（§13 R3）：`OnFileDrop` 坐标的单位与 DPI 缩放；不可靠时退化为「以最后一次悬停的面板为目标，并在拖入期间高亮两侧让用户确认」。
- **已核实**（§13 R4 修正）：隔离字段是 `DragAndDrop.DisableWebViewDrop`，它在本机是全局 `gtk_drag_dest_unset`，**一旦打开会把 §7.1 的 ②③ 两条本源拖入一起废掉**——不得当隔离手段。

## 8. 过滤与深搜

### 8.1 即时过滤

- 位置：**列头行右侧**（方案 A 紧凑单行布局，与「名称 / 大小 / 修改时间」同排；`FilePane` 现状的 `head` 与 `columns` 两行结构见 `FilePane.vue:61-71`，实施时按此落位）。
- 规则：不区分大小写子串匹配，命中片段高亮；只影响显示。
- **单一约定**：`显示顺序 = showAll ∩ filter`（再排序），该结果即 §3 决策 32 的"可见顺序"，选择/判重/拖拽不得各自再算一份；**判重另用原始 items**（§6.3）。
- 与选择的关系见 §3 决策 15；面板头与投递确认框都必须显示「含 M 项被过滤」。

### 8.2 深搜浮层

- 双侧面板头各一个 `🔍 深搜`；浮层标题固定写明范围：`在 <host> : <path> 下递归搜索` / `在 <path> 下递归搜索`。
- 输入回车或点击触发；**新搜索自动取消并丢弃上一次**（id + seq，沿用 `remoteSeq` 模式）。
- 远程：显示 `搜索中… 已扫描 N 个目录`（`sftp:search-progress`）与 `取消`；本地 `WalkDir` 通常毫秒级返回，不显示进度。
- **取消语义**：取消后必须 resolve 一个 `Cancelled=true` + 部分结果的 outcome（**不能**用 error 返回，否则 Wails 丢结果），UI 显示「已取消 · 已扫描 N 个目录」并列出部分命中。
- 结果列：相对路径 / 类型 / 大小 / 修改时间；**双击 → 跳到所在目录并选中该项**（远程需先确保连接）。
- 结束态如实标注：`命中 N 项 · 深度 5 层 · 触发上限` / `M 个目录不可读`。
- `Esc` 或点击遮罩关闭浮层；关闭时退订进度事件、调用 `SearchCancel`。

## 9. 位置模型（书签 + 快速位置）

- 每面板一个「📍 位置 ▾」：上半区**书签**（`name` + `path`，远程含 `host`；可增删），下半区**最近**（自动、去重、最近在前、上限 20）。
- 路径旁 `☆`：收藏/取消收藏当前目录。
- 远程位置按**当前主机**过滤；切换主机时列表随之切换。
- 点选位置：同机已连接 → 直接跳转；未连接 → 记录 `pendingPath` 后触发连接，连接成功落到该目录（保留现有 `connect()` 的 `pendingPath` 语义，入口从 header 移到面板）。
- **读写只走 §5.3 的位置绑定**（单写者）；header 全局「最近位置」下拉及其 UI 删除。

## 10. 配置模型与迁移

### 10.1 新结构

```toml
[[bookmarks]]
name  = "生产日志"
scope = "remote"        # remote | local
host  = "prod-db"       # scope=local 时为空字符串
path  = "/var/log/app"

[[local_recent]]
path = "/home/lan/work"
ts   = "2026-09-16T10:00:00Z"

[[remote_recent]]
host = "prod-db"
path = "/var/log/app"
ts   = "2026-09-16T10:00:00Z"

# 迁移标记：显式、持久，避免"新字段为空即重新迁移"复活旧数据
legacy_migrated = true
```

### 10.2 迁移（`MigrateLegacyRecents`）

- 触发条件：`legacy_migrated != true` 且 `recent_sftp` 非空。**不再用「新字段是否为空」判断**（用户清空书签/最近后旧数据会被复活）。
- 拆分：`remote_recent` 按 `(host, remote_dir)` 去重（两者非空，保留最新 ts）；`local_recent` 按 `local_dir` 去重；两侧 ts 倒序、截断 20。
- 写回：迁移后置 `legacy_migrated = true`，由 startup 之后的一次 `a.saveConfig()` 落盘（`internal/config` 里**没有**现成的 dirty 机制，必须显式落盘）。
- 旧字段 `recent_sftp` 保留**解析**（兼容降级），但 `recordRecentSFTP` 改造后**不再写**它；`ListRecentSFTP` 与 SftpView 的调用一并移除（§4.3）。

## 11. 错误处理与日志

- 后端错误统一走 `forward.Event{SourceType:"sftp"}`（`logEvent`），含 stderr 原文；**新增事件**（进度、拖入）直发 `runtime.EventsEmit`（§5.4）。
- 批量：单项失败不中断；队列红标 + 结束汇总 + 可复制失败清单。
- 删除批：`-` 前缀保证一项失败不拖垮整批；失败按 stderr 反查路径（§3 决策 27）。
- 拖拽：非法落点/自嵌套在**前端**拒绝，不发请求。
- 深搜：根不可读 → 明确报错；部分不可读 → 标注数量；取消 → `Cancelled` + 部分结果（不是 error）。
- 并发保护：批量顺序执行；深搜与目录刷新用 id/seq 丢弃过期结果。

## 12. 测试策略与验收

### 12.1 Go 单测（沿用 mock 基建）

| 目标 | 用例 |
|---|---|
| `sftp.PutRecursive` | 单文件、目录树、远端目标已存在（断言以 §13 R1 实测结论为准）、路径含空格与引号 |
| `sftp.RemoveRecursive` | 单文件、空目录、多层目录树（自底向上顺序）、**批内一项失败其余仍执行**（`-` 前缀）、**含 `* ? [` 的路径被拒绝**、路径含空格 |
| `sftp.Search` | 深度限制（0/N/-1）、limit 截断置 `Truncated`、ctx 取消置 `Cancelled` 且返回部分结果、`Unreadable` 计数、根不可读、大小写不敏感、`*` 通配、非法 glob（`[abc`）回退子串 |
| `localfs.Search` | 深度/limit、ctx 取消、不可读目录计数、不跟随符号链接 |
| `localfs.Copy` | 文件、目录递归、同名覆盖（调用方决定）、目标为自身子目录 → 拒绝 |
| `config` 迁移 | 拆分、去重、ts 倒序、上限 20、**`legacy_migrated` 幂等**（清空后不复活）、旧字段只读不写回 |

### 12.2 前端 vitest

| 模块 | 用例 |
|---|---|
| `selection.js` | 单击设锚点、Ctrl 切换、Shift 区间、全选/清空、切目录清空、`remove()` 清理、被过滤隐藏项不影响集合、**排序/过滤变化后既存区间不被重算** |
| `batch.js` | 三分支冲突策略（含默认 skip）、`另存副本` 命名（扩展名/目录）、失败汇总、目录项语义 |
| `dnd.js` | 命中面板、同侧子目录、自嵌套拒绝、无效落点、拖起项在集合内 → 整批 |
| `keys.js` | 守卫判定：焦点在 input/textarea/select/contenteditable/浮层/对话框 → 放行给输入控件 |
| `locations.js` | 去重、上限 20、按主机过滤、书签增删 |
| `TransferQueue` 的 `statusClass` | 完成→done、失败→err、**跳过→skip**、其余→doing（纯函数导出后单测） |

组件级行为（聚焦态样式、拖拽高亮、对话框交互）不做组件单测——`vite.config.js` 无 test 环境、`package.json` 无 jsdom/@vue/test-utils，引入组件测试栈不在本轮范围——由 §12.3 人工验收覆盖。

### 12.3 验收

**可脚本化（能进 CI）**

1. `make ci` 全绿：vet + Go `-race` 单测（含 §12.1 全部用例）+ vitest（含 §12.2 全部用例）。
2. `make e2e`（`e2e/test_local.sh` 一次性 sshd）探针脚本，覆盖 §13 的三条未证实项：
   - `put -r` 目标同名目录已存在时的行为（并入 vs 嵌套）与 `PutRecursive` 断言一致；
   - 远端 `rename` 覆盖已存在目标的成败（`posix-rename@openssh.com`）；
   - 远端文件名含 `*` 时 `rm` 的 glob 行为（决定 §5.1 的拒绝策略是否可放开）。
3. 迁移等价性可断言：给定含两条 `recent_sftp` 的旧 TOML → `remote_recent` 含去重后的 `(host, remote_dir)`、`local_recent` 含去重后的 `local_dir`、均按 ts 倒序且 ≤20、`legacy_migrated=true`。

**人工验收（交互，无法脚本化）**

4. 四条拖拽路径各做一次（含多选与目录）；非法落点/自嵌套被拒绝且有文案。
5. 批量下载/上传/删除各一次；冲突三分支各验一次；**含远端非空目录**的批量删除必须成功（本轮修掉的既有缺口）；单文件手势直接覆盖。
6. 深搜：远程取消后浮层显示「已取消 · 已扫描 N 个目录」并列出部分命中；本地毫秒返回；双击结果能跳转并选中。
7. 过滤后选中隐藏项 → 面板头与确认框都出现「含 M 项被过滤」。
8. 书签增删、按主机过滤、迁移后「最近位置」可用（内容与第 3 条断言一致）。
9. 键盘：`Delete`/`Ctrl+A` 作用于聚焦面板；焦点在过滤框内按 `Delete`/`Ctrl+A` **只编辑文本**。
10. 系统拖入四个方向（文件/目录 × 本地/远程面板）各一次，落在工具栏区被忽略并提示。
11. 过滤命中片段高亮可见；批量结束后 `复制失败清单` 粘出的文本可逐条对应到队列里的失败项。
12. 传输队列的 `跳过(已存在)` 显示为灰色 skip 态（不是「处理中」色），汇总行数字与队列条数一致。

## 13. 风险与未证实项

| # | 风险 | 处置 |
|---|---|---|
| R1 | `sftp put -r` 在「远端同名目录已存在」时是**并入**还是**嵌套**，未实测 | §12.3 第 2 条探针实测；`PutRecursive` 行为与注释以实测结论为准；实测前该分支的 UI 文案不得写死 |
| R2 | 远端 `rename` 覆盖已存在目标的成败（取决于 `posix-rename@openssh.com`）；本地在 Windows 上 `os.Rename` 目标存在**必然失败** | 探针实测远端；本地移动前先判存在，Windows 分支按「先删后改名」实现并单测 |
| R3 | `OnFileDrop` 坐标单位 / DPI 缩放不准 → 命中面板出错 | 实测；退化方案：以最后悬停面板为目标并在拖入期间高亮两侧确认 |
| R4 | ~~用 `DisableWebViewDragAndDrop` 隔离~~ —— 字段名不存在；真实字段 `DisableWebViewDrop` 会全局关掉 webview 拖放接收，**同时废掉本源拖入** | **不得使用该开关**；真正的回退是"拖入期间忽略面板内 DnD 高亮"，JS 侧本来就按 `e.dataTransfer.types.includes("Files")` 区分外部文件拖入与内部 HTML5 拖拽（旁证缓解本风险） |
| R5 | 深搜在大目录树上耗时（如 `/`） | 默认 `maxDepth=5`、`limit=500`、可取消；不提供无限深度入口 |
| R6 | 本地面板被过滤隐藏的选中项被误删 | 面板头 + 确认框双重提示数量；删除确认始终显示实际将删的项数；判重用原始 items |
| R7 | 远端递归删除**不可中断、部分失败不回滚**（删到一半失败会留下半棵子树）；确认与执行之间存在 TOCTOU | `-` 前缀保证一项失败不拖垮整批；失败按项进失败清单并明确「已删除 N 项」；**不承诺原子性**；R7 的"压成单批"只对删除阶段成立——BFS 收集阶段每层一个 `ListMany` 批次，收集与删除之间新增的文件会让对应 `rmdir` 失败，此时如实报错 |
| R8 | 键盘快捷键误触（过滤框内 `Delete` 删文件） | §6.1 键盘守卫 + §12.2 `keys.js` 单测 + §12.3 第 9 条人工验收 |
| R9 | glob 元字符路径的**正确**处理方式未证实（转义能不能被远端 sftp 接受取决于 sftp 端 glob 解析与 quote 顺序） | 本轮对删除采取**保守拒绝**（决策 28）；§12.3 第 2 条探针给出结论后再决定是否放开转义 |
| R10 | Wails 绑定「err != nil 丢结果」这一约束容易被后续实现者忘记 | 写进 §5.1 注释与 §12.1 用例（取消必须 resolve 而非 reject）；实现时 `SftpSearch` 显式吞掉 `context.Canceled`/`DeadlineExceeded` |

## 14. 后续子项（另行评审，不在本轮）

**P3 传输底座**：把「一次性 `sftp -b` 进程」升级为可流式上报的执行器，从而支持 ① 传输进度百分比 ② 进行中取消 ③ 失败重试 ④ 断点续传 ⑤ 校验和比对。选择模型与批量投递层（本轮产物）无需返工，只需在传输调用点接入新回调。

## 15. 关键文件清单

**新增**
- `internal/localfs/search.go`、`internal/localfs/copy.go`（+ 测试）
- `internal/sftp/search.go`（含 `matchName`，+ 测试）
- `frontend/src/utils/{selection,batch,dnd,keys}.js`（+ 测试）
- `frontend/src/stores/locations.js`
- `frontend/src/components/SearchOverlay.vue`、`ConflictDialog.vue`
- `e2e/` 下的未证实项探针脚本（R1/R2/R9）

**改动**
- `internal/sftp/ctrl.go`（`PutRecursive`、`RemoveRecursive`、`quoteArg` 的 glob 口径）
- `internal/config/store.go`（`Bookmark`/`RecentLocal`/`RecentRemote` + `legacy_migrated` + 迁移）
- `app.go`（新绑定、`StatPaths`、位置绑定、`recordRecentSFTP` 改造、`runtime.EventsEmit` 直发、深搜取消转换）
- `main.go`（`DragAndDrop.EnableFileDrop` + Go 版 `OnFileDrop` 注册）
- `frontend/src/views/SftpView.vue`、`components/FilePane.vue`、`components/TransferQueue.vue`、`stores/settings.js`
- `frontend/wailsjs/go/main/App.{js,d.ts}` 与 `models.ts`（执行 `wails generate module -tags webkit2_41` 重生成）
