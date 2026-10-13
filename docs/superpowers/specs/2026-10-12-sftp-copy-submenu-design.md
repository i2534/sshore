# SFTP 右键「复制」子菜单与弹框关闭策略 设计

**日期**：2026-10-12
**状态**：设计定稿（经与用户 5 轮问答确认 + 3 个独立 subagent 审核后修订），待实现
**实现计划**：`docs/superpowers/plans/2026-10-12-sftp-copy-submenu.md`
**修订记录**：v1 初稿（问答定稿）→ v2（三路独立审核 + 作者复核后的修订，见 §6 表 13–20）

---

## 1. 目标

1. SFTP 面板的**本地与远程**两个文件行右键菜单各新增一个「复制 ▸」项，子菜单三项：**文件名**、**全路径**、**文件**。
2. 5 个模态对话框改为**只有显式点击按钮才能关闭**：不得通过点击遮罩关闭，也不得通过 `Esc` 关闭。

### 非目标（YAGNI）

- **不做**右键菜单自身的关闭按钮（按平台惯例保持"点外部关闭"）。
- **不做**「位置」下拉 `.posmenu` 的关闭按钮（同上）。
- **不做**应用内剪贴板 / 跨面板「粘贴」功能（用户明确只要求"复制"）。
- **不做** Linux 的"文件对象"剪贴板（见 §4.4）。
- **不做**复制动作的专用工具条/快捷键入口（仅右键子菜单）。
- **不碰** `internal/sftp` 的任何传输实现（复用既有下载管线）。

## 2. 术语：三项分别是什么

| 子菜单项 | 剪贴板里装的东西 | 粘到终端/编辑器 | 粘到文件管理器 |
|---|---|---|---|
| 文件名 | 纯文本 `report.pdf` | 出现 `report.pdf` | 不会得到该文件 |
| 全路径 | 纯文本 `/home/u/proj/report.pdf` | 出现该路径 | 不会得到该文件 |
| 文件 | **文件对象**（该文件/目录本身） | 通常无可粘内容 | 得到真实文件副本 |

**关键语义**：「文件」在 Windows 上写入的是**路径引用**（CF_HDROP 路径列表），粘贴那一刻由资源管理器去读该路径。因此：

- 源文件在粘贴前被删除/移动 ⇒ 粘贴失败；
- 远程文件必须先落到本地磁盘才能进剪贴板（见 §4.3）；
- 临时目录**不能**在写入剪贴板后立即删除。

## 3. 交互规格

### 3.1 菜单矩阵位置

行菜单（远程行 / 本地行）里，`复制 ▸` 插在**「重命名」之前**：

- 远程行：`下载… / 上传… / 复制 ▸ / 重命名 / 删除 / ── / 刷新`
- 本地行：`上传到远程 / 复制 ▸ / 重命名 / 删除 / ── / 刷新`

空白/面板级菜单（列头、过滤栏背景、`..` 行）**不出现**复制——它只针对文件行。

### 3.2 子菜单交互

- 鼠标**悬停父项约 120ms** 展开子面板；鼠标离开**父项与子面板这一整块**约 160ms 后收起。hover 监听必须挂在**组件根元素**上（覆盖父项 + 子面板），否则鼠标从父项移向右侧子面板的途中就会触发父项的 `mouseleave` 而提前收起，子项永远点不到。
- 父项**可点击**切换展开/收起（键盘与触屏可预期）。
- 子面板定位于父项右侧；贴近视口右缘时左翻，贴近下缘时上移，仍放不下则贴顶并限制高度。视口尺寸取 `window.innerWidth / window.innerHeight`（面板是 `position: fixed`）。
- **先渲染再测量**：`open = true` 之后必须 `await nextTick()` 再读 `panel.getBoundingClientRect()`；同一 tick 内读会拿到 null。ref 为空时跳过定位（用兜底坐标）而不是抛错——这是打开子菜单的唯一路径，抛错等于功能不可用。
- 点击任一子项后，整条右键菜单关闭（沿用既有 `closeMenu()`）。
- 子面板是右键菜单 DOM 的子树 ⇒ 点击它不会触发 `window` 上的 `outsideClick`（`ContextMenu` 的 `@click.stop` 已覆盖）。

### 3.3 动作作用集合（多选全支持）

沿用既有菜单语义，但**不改变选中集**（复制是只读动作）：

- 右键那一行**在**选中集内 ⇒ 作用于**整个选中集**；
- 右键那一行**不在**选中集内 ⇒ 只作用于**该行**（等义于既有菜单项的 `sel.single()`，但不真的改选中集）。

三项均**不置灰**（多选可用）。唯一例外见 §3.4。

**这条约束覆盖复制的全流程，包括远程「文件」的下载阶段**：既有的批量下载在收尾时会 `sel.remove(…, 成功项)` 把成功项从选中集里摘掉（对"下载"是正确的）。复制到剪贴板是只读动作，不得沿用该副作用 ⇒ 复用批量下载时必须显式跳过选中集清理（实现计划 T7 的 `keepSelection: true`）。

### 3.4 平台与连接态

| 子项 | 本地面板 | 远程面板（已连接） | 远程面板（未连接） |
|---|---|---|---|
| 文件名 | 可用 | 可用 | 可用 |
| 全路径 | 可用 | 可用 | 可用 |
| 文件 | 可用 | 可用 | **置灰** |

**Linux 全程例外**：`文件` 项不渲染（不是置灰）——Linux 上没有该能力，见 §4.4。

- 「文件名/全路径」是纯字符串拼接，不依赖后端、不需要连接。
- 「文件」在远程面板需要先下载 ⇒ 未连接时置灰。该置灰状态**写在 script 的 `COPY_ITEMS` 计算属性里**（现有菜单项的 `remoteDep && !connected` 是模板内表达式；既有测试明令 ContextMenu 模板切片内不得出现 `connected.value`，所以复制项的判据必须在 script，不进模板）。
- 「文件」是否渲染由**运行时能力查询** `ClipboardFileSupport()` 决定：Windows `true`、Linux `false`。

### 3.5 文本复制的顺序与内容

- 多选复制「文件名」/「全路径」时，**每项一行、以 `\n` 拼接**。
- 行序 = **当前面板的显示顺序**（`visibleFor(pane)` 的顺序），不是 `Set` 的插入序。
- **被过滤器隐藏的选中项也一并复制**，但排在可见项之后（它们不在显示顺序里；顺序只在可见项之间有意义）。
- 空集合不写剪贴板（不得把剪贴板清空成空串）。

### 3.6 单项行为细节

| 子项 | 本地 | 远程 |
|---|---|---|
| 文件名 | `item.name` | `item.name` |
| 全路径 | `localJoin(localPath, name)`——**沿用本地路径自身的分隔符**（`localpath.join` 的 `sepOf` 语义） | `posixJoin(remotePath, name)`（POSIX） |
| 文件 | `CopyFilesToClipboard([绝对路径])` | `ClipboardTempDir()` → 入队下载到该目录 → **全部完成后**对成功项调 `CopyFilesToClipboard` |

> 注：`localpath.join` 刻意**沿用输入路径已有的分隔符**（`C:\foo` → `C:\foo\name`，`C:/foo` → `C:/foo/name`），不会强制改写。这不是缺陷——Windows API 两种都接受。因此不能声称"本任务保证产出反斜杠路径"。

远程「文件」的补充规则：

- 下载走**既有传输队列**（进度、可取消、失败可重试），落点是新建的临时目录；
- **不弹冲突询问框**：既有批量下载是拿**用户当前本地目录的可见项**去判冲突的（不是临时目录），只要本地有同名文件就会误判冲突并按默认策略「跳过」——**静默什么都不复制**。因此复制路径必须让冲突判定看到一个**空目标**（实现计划 T7 的 `existingNames: []`），而不是只靠"隐藏选中项数为 0"；
- 下载**全部批次结束后**才写剪贴板；只把 `status === '完成'` 的项写进去；
- 下载期间**不改动用户的选中集**（见 §3.3）；
- **一旦本批出现「取消」项，整批不写剪贴板**（下载到一半被取消时，已完成的项也不写——半批数据进剪贴板会让用户以为复制了全部）；
- 部分失败：成功项仍写入剪贴板，失败项写 `error` 日志；
- 全部失败：不写剪贴板，写 `error` 日志；
- 临时目录创建失败：写 `error` 日志，流程终止（不得抛出未捕获异常）。

### 3.7 日志文案

- 成功：`已复制 <n> 个文件名到剪贴板` / `已复制 <n> 个全路径到剪贴板` / `已复制 <n> 个文件到剪贴板（可直接粘贴到文件管理器）`（`level: 'info'`）。
- **`<n>` 的口径**：一律 = **本次实际写入剪贴板的项数**（文本两项 = 参与拼接的名字数；远程「文件」= `status === '完成'` 的项数，不是选中项数）。部分失败时日志不得报虚数。
- 失败：`level: 'error'`，内容为原始错误字符串（沿用 `err(e)`）。

## 4. 后端规格

### 4.1 新包 `internal/clipboard`

```go
// Support 报告当前平台是否支持把文件/目录作为文件对象写入系统剪贴板。
func Support() bool

// SetFiles 把 paths 指向的文件/目录作为文件对象写入系统剪贴板。
// 任一路径不存在时返回错误，且不写入剪贴板。
func SetFiles(paths []string) error

// TempDirPrefix 是剪贴板临时目录的名字前缀。
const TempDirPrefix = "sshore-clip-"

// NewTempDir 在 os.TempDir() 下新建唯一目录（名字带 TempDirPrefix）并返回其路径。
// 与 CleanStaleTempDirs 的 root 口径一致。
func NewTempDir() (string, error)

// CleanStaleTempDirs 删除 root 下名字以 TempDirPrefix 开头、
// 且 ModTime 早于 now-minAge 的目录；返回删除数量。now 由调用方注入以便测试。
func CleanStaleTempDirs(root string, minAge time.Duration, now time.Time) (int, error)
```

- `buildHDropBytes(paths []string) []byte`：构造 CF_HDROP 数据块（20 字节 `DROPFILES` 头 + UTF-16LE 双 `\0` 结尾路径列表）。放**无构建标签**的文件里，Linux 上也能单测。
- `clipboard_windows.go`（`//go:build windows`）：**纯 Go + 自己声明 Win32 proc**，不引入 cgo。
- `clipboard_other.go`（`//go:build !windows`）：`Support() == false`、`SetFiles` 返回 `ErrUnsupported`。

#### 4.1.1 Windows 实现必须自己声明 proc（不可用 x/sys 现成 API）

`golang.org/x/sys/windows`（v0.47.0，本仓直接依赖）**不导出**任何剪贴板/全局内存 API：`GlobalAlloc`、`GlobalLock`、`GlobalUnlock`、`GlobalFree`、`OpenClipboard`、`EmptyClipboard`、`SetClipboardData`、`CloseClipboard`、`CF_HDROP`、`GMEM_MOVEABLE` **一个都没有**（标准库 `syscall` 也没有）。必须自建：

```go
user32   = windows.NewLazySystemDLL("user32.dll")
kernel32 = windows.NewLazySystemDLL("kernel32.dll")
// OpenClipboard / EmptyClipboard / SetClipboardData / CloseClipboard  ← user32
// GlobalAlloc / GlobalLock / GlobalUnlock / GlobalFree / RtlMoveMemory ← kernel32
const (cfHDrop = 15; gmemMoveable = 0x0002)
```

wails 自带的 `internal/frontend/desktop/windows/winc/w32` 有全套符号，但位于 `internal/` 树内，**只能当写法参照，不能 import**。

#### 4.1.2 Windows 实现的四条硬约束（每条都有真实的坑）

1. **线程锁定**：`SetFiles` 首行 `runtime.LockOSThread()` + `defer runtime.UnlockOSThread()`。否则 goroutine 在 `OpenClipboard` 与 `CloseClipboard` 之间换线程 ⇒ **剪贴板死锁**（wails `win32/clipboard.go` 有同款注释与写法）。
2. **`Proc.Call` 的返回值语义**：`x/sys/windows` 的 `Proc.Call` 返回的 error **永远非 nil**（由 `GetLastError` 构造），必须看**第一个返回值**判成败；`GlobalUnlock` 是**反向**约定（返回 0 才是成功）；`LazyProc.Call` 在 proc 找不到时**直接 panic**（`mustFind`）——首次调用前先 `Find()` 并把错误转成普通 error，避免 panic 被 Wails dispatcher 静默吞掉。
3. **内存所有权**：`GlobalAlloc` 得到的 `hMem` **只在成功交接之前**任一环节失败时 `GlobalFree`；`SetClipboardData` 返回非 0 之后所有权归系统，**绝不能再 `GlobalFree` 或写它**（否则资源管理器粘贴时读已释放内存）。
4. **填内存不得撞 `go vet` 的 `unsafeptr`**：`GlobalLock` 回 `uintptr`，最自然的 `unsafe.Slice((*byte)(unsafe.Pointer(ptr)), n)` 会被 `go vet`（默认含 `unsafeptr`）判为 `possible misuse of unsafe.Pointer`，而 **CI 的 `go-windows` job 会跑 `go vet ./...`** ⇒ 必然变红（Linux job 因 build tag 排除看不到）。用不会触发该分析器的写法：自建 `RtlMoveMemory` proc 做 memcpy（`procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))`），或用 `windows.WriteProcessMemory(windows.CurrentProcess(), …)`。

### 4.2 `app.go` 绑定

```go
func (a *App) ClipboardFileSupport() bool
func (a *App) ClipboardTempDir() (string, error)
func (a *App) CopyFilesToClipboard(paths []string) error
```

`startup()` 里**后台**执行一次陈旧清理。两条约束：

- 必须 `go`（沿用既有教训：清理不得挡在控制器装配之前）；
- 清理动作必须走**包级函数变量**（形如 `var cleanupClipboardTempDirsAt = clipboard.CleanStaleTempDirs`），以便测试注入临时 root。**否则任何调用 `a.startup(...)` 的既有测试都会真去扫删开发机的 `/tmp`**（既有 `cleanupStaleLocalPartsAt` 就是这个模式）。
- 失败处理：**静默忽略**（与既有 `cleanupStaleLocalPartsAt` 一致；startup 期间 `a.emit` 尚未注入，无法安全记日志）。spec 不再声称"失败只记不报"。

### 4.3 临时目录生命周期

1. 每次「文件」（远程）复制调 `ClipboardTempDir()` **新建**一个目录；
2. 下载写入该目录；
3. 写入剪贴板后**本次不删除**（CF_HDROP 是路径引用）；
4. **下次启动**清理 `os.TempDir()` 下 `ModTime` 早于"1 小时前"的 `sshore-clip-*` 目录。

1 小时门槛**只**覆盖下列场景：不误删"刚复制、粘贴窗口期还没过"的目录，也不误删同一台机器上另一个正在运行的实例**刚建**的目录。它**不**覆盖：实例 A 复制后超过 1 小时，实例 B 才启动（B 会把 A 剪贴板仍指向的目录删掉 ⇒ A 的粘贴失败）。这是**已接受的取舍**（§8）。

### 4.4 为什么 Linux 没有「文件」

Linux 要让文件管理器粘贴出文件，剪贴板必须由进程提供 `text/uri-list` / `x-special/gnome-copied-files` 这类"文件对象"格式；WebKit 的剪贴板 API 写不了该格式，需 cgo + GTK3。用户已明确决定**不做**，因此 Linux 上该项不渲染。

## 5. 弹框关闭策略

下列 5 个模态框**删除**遮罩点击关闭与 `Esc` 关闭，只保留显式按钮：

| 组件 | 删除 | 保留的显式关闭路径 |
|---|---|---|
| `AppDialog.vue` | `@click.self="emit('cancel')"`；prompt 输入框的 `@keyup.esc` | 「确认/确定」、「取消」（输入框 `Enter` 提交保留） |
| `ConflictDialog.vue` | `@click.self="emit('cancel')"`、`@keyup.esc="emit('cancel')"` | 「取消本次批量」、「开始」（遮罩 `tabindex=-1` + 聚焦保留，作为焦点管理手段） |
| `SearchOverlay.vue` | `@click.self="emit('close')"`、`@keyup.esc="emit('close')"` | 「关闭」、「取消」、「搜索」 |
| `SettingsDialog.vue` | `@click.self="emit('close')"` | 「×」、「完成」 |
| `SyncConflictsDialog.vue` | `@click.self="emit('close')"` | 「关闭」、各动作按钮 |

**注释也要改**：`ConflictDialog.vue` 与 `SearchOverlay.vue` 的源码注释里写有 `@keyup.esc` 字面量（"…形同虚设"/"…挂在大遮罩上"），删属性后这些注释既过时又会让"源码不含 keyup.esc"的断言失败。两处注释必须同步改写。

**不在此列**：`ContextMenu.vue`、`FilePane.vue` 的 `.posmenu`、`FilePane.vue` 的路径栏 `@keyup.esc="resetPath"`、`SftpView` 的 `Esc 清空选中`（都不是模态框）。

## 6. 决策记录（问答回溯 + 审核修订）

| # | 问题 | 结论 |
|---|---|---|
| 1 | 「文件」的语义 | 复制为**系统剪贴板里的文件对象**（非文本、非内容） |
| 2 | 多选行为 | （初答"仅单选置灰"，后**改判**）三项都支持多选 |
| 3 | 关闭策略范围 | 只管 5 个模态对话框；右键菜单与位置下拉保持点外部关闭 |
| 4 | Linux 实现 | 不引入 cgo；Linux 上「文件」不出现 |
| 5 | Linux 范围再确认 | Linux 保留「文件名/全路径」，仅「文件」不出现 |
| 6 | 远程「文件」的下载呈现 | 进**既有传输队列**（进度/可取消/可重试） |
| 7 | 位置 | 插在「重命名」之前 |
| 8 | 日志 | 成功写 `info`、失败写 `error`（都写） |
| 9 | 子菜单交互 | 悬停展开 + 父项可点击展开 |
| 10 | 临时目录残留 | 下次启动清理（1 小时门槛） |
| 11 | 标签命名 | 「文件本身」→「文件对象」→ 最终定为**「文件」** |
| 12 | 作者自审：复用批量下载会 `sel.remove` 清空选中集、并可能弹确认框 | 下载阶段传 `keepSelection: true`；确认框问题见 #15 |
| 13 | 审核发现：Windows 侧 `x/sys/windows` 无剪贴板 API | 自建 Win32 proc（§4.1.1） |
| 14 | 审核发现：`unsafe.Pointer(uintptr)` 会撞 `go vet` ⇒ CI windows job 红 | 用 `RtlMoveMemory` / `WriteProcessMemory` 填内存（§4.1.2 第 4 条） |
| 15 | 审核发现：`hiddenSelected: 0` **挡不住**冲突框（`existing` 取自用户本地目录） | 改为显式空目标 `existingNames: []`（§3.6） |
| 16 | 审核发现：`setClipboardData` 成功后 `GlobalFree` = 悬垂句柄 | 所有权规则写进 §4.1.2 第 3 条 |
| 17 | 审核发现：复制中途取消时已完成的项仍会入剪贴板 | 出现「取消」项则整批不写（§3.6） |
| 18 | 审核发现：`onMounted` 新增第二个会打红既有 `sftpRestoreWiring` 断言 | 能力查询放进既有 `onMounted` 内部（计划 T6） |
| 19 | 审核发现：`localpath.join` 沿用输入分隔符，不能声称"保证反斜杠" | §3.6 改为如实描述 |
| 20 | 审核发现：新增 startup 清理若不可注入会真删开发机 `/tmp` | 走包级函数变量（§4.2） |

## 7. 验收标准

1. 本地/远程行右键菜单均有「复制 ▸」，子菜单三项文案逐字为 `文件名`、`全路径`、`文件`（Linux 上无 `文件`）。
2. 单选与多选都能复制；多行文本按面板显示顺序、换行拼接；**复制前后选中集保持不变**（含远程「文件」的下载阶段）。
3. Windows 上「文件」可让资源管理器粘贴出真实副本（本地与远程各验一次，内容哈希一致）。
4. 远程「文件」在未连接时置灰；下载进队列可见；完成后日志提示且 `<n>` 等于实际写入项数。
5. 子菜单可用：悬停展开、父项点击可开合、**鼠标从父项移到子面板不会中途收起**、右缘左翻/下缘上移、点击子项执行动作并关闭菜单。
6. 5 个模态框：点遮罩不关、按 Esc 不关、点各自按钮能关。
7. `make ci` 全绿（`npm run build` → `go vet ./...` → `go test ./... -race -count=1` → `npx vitest run`），且 CI 的 `go-windows` job 也绿（它跑 `go vet ./...` + `go test ./... -count=1`）。

## 8. 风险与未验证项

1. **远程「文件」分两步**：下载完成前粘贴会拿到旧内容，只能靠日志提示。
2. **CF_HDROP 的粘贴端行为未经真机实测**（"资源管理器粘贴出副本"）。三路审核都确认本机无 Windows 运行环境，只能靠真机验收，不得推断。
3. **Linux 上 `navigator.clipboard.writeText` 未实测**：WebKitGTK + Wails 的 secure context 下按推断可用（现有「复制失败清单」用同一 API，但无 Linux 真机记录）。若实测失效，Linux 的文本两项也是"点了没反应"，需回头找用户重新决策。
4. **临时目录清理的已知缺口**：1 小时门槛不覆盖"实例 A 复制后超过 1 小时、实例 B 才启动"与"复制后超过 1 小时才粘贴"两种情况（§4.3）。
5. **`OpenClipboard` 竞争**：资源管理器/剪贴板管理器占着剪贴板时 `SetFiles` 会直接失败（本设计不做重试，靠 error 日志暴露）。
6. **内存所有权与线程锁定误用**：写错即剪贴板损坏/死锁，且 Linux 上无法被任何测试发现——只能靠 Windows 真机验收（§4.1.2）。
7. **`RtlMoveMemory` 的 kernel32 导出与 `WriteProcessMemory` 自写**：审核者只验证了"编译 + vet"通过，真机行为未核实。
