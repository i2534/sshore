# SFTP 右键「复制」子菜单与弹框按钮关闭 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 SFTP 本地/远程两个面板的文件行右键菜单加「复制 ▸」子菜单（文件名 / 全路径 / 文件），并让 5 个模态对话框只能通过显式按钮关闭。

**Architecture:** 后端新增纯 Go 的 `internal/clipboard` 包（Windows 侧**自己声明 Win32 proc** 写 `CF_HDROP`，非 Windows 降级为不支持——`x/sys/windows` 并不导出这些 API），由 `app.go` 暴露 3 个绑定并挂一个可注入的后台陈旧临时目录清理；前端新增 `utils/menu.js`（定位纯函数）、`utils/copy.js`（作用集合与文本拼接纯函数）、`MenuSubmenu.vue`（子菜单组件），`SftpView.vue` 的菜单矩阵接入 3 项动作——文本两项走 `navigator.clipboard.writeText`，把「文件」交给后端，远程「文件」复用既有传输队列下载到新临时目录后再入剪贴板。

**Tech Stack:** Go 1.26 + Wails v2.15.0（**纯 Go，不引入 cgo**；`golang.org/x/sys/windows` 仅用于 `NewLazySystemDLL`，其剪贴板 API 不存在，见 T2）；Vue 3.5 + Pinia + Vitest 4（**无 jsdom / 无 @vue/test-utils**，`@vue/server-renderer` 可做真实 SSR 断言）。

**Spec:** `docs/superpowers/specs/2026-10-12-sftp-copy-submenu-design.md`（v2；执行者必须两份一起读，本计划逐节实现它）

## Global Constraints

- **前端产物前置（Task 0a）**：`main.go` 用 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 `.gitignore` 忽略 ⇒ 干净克隆上**任何** Go 命令都会硬失败。干净克隆必须先 `cd frontend && npm ci && npm run build`（`package-lock.json` 已入库、`node_modules` 未入库）。本机 `frontend/dist` 已存在（244K）可跳过，但要先 `ls frontend/dist` 确认非空。
- **构建缓存**：所有 Go 命令必须带 `GOCACHE=$PWD/.gocache`（**实测默认 `~/.cache/go-build` 只读**：`touch` 报 `Read-only file system`；仓库根已有 gitignore 的 `.gocache/`）。这也是仓库惯例。
- **最终门禁**：`GOCACHE=$PWD/.gocache make ci`（= `cd frontend && npm run build` → `go vet ./...` → `go test ./... -race -count=1` → `cd frontend && npx vitest run`）。任何任务收尾前该命令必须全绿，或如实报告哪一步失败。
- **Windows 侧门禁**：`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...` 必须无输出（**CI 的 `go-windows` job 就是这么跑的**，`.github/workflows/ci.yml:73`；`make ci` 覆盖不到它，Linux 的 `go vet` 也看不到 windows-tagged 文件）。另有 `go test ./... -count=1`（不带 `-race`）。
- **新增 Go 绑定后必须重生成**：仓库根执行 `GOCACHE=$PWD/.gocache wails generate module -tags webkit2_41`（该子命令确实重生成绑定：`cmd/wails/generate.go` 调 `bindings.GenerateBindings`；CLI 帮助文案 "Generates a new Wails module" 容易误读成脚手架，不是）。提交 `frontend/wailsjs/go/main/App.{js,d.ts}`，并**一并检查 `frontend/wailsjs/go/models.ts`**（若生成器改了它也要提交）。**实测坑**：该命令把 `frontend/wailsjs/runtime/*` 的权限位从 `644` 改成 `755`（**内容 md5 完全相同**）⇒ 生成后 `git checkout -- frontend/wailsjs/runtime/` 还原，只提交真正的 API 变化。生成失败必须停下报告，不得手改生成文件。
- **只本地提交，不 push**（用户未授权推送）；每条提交用中文信息。
- **文案逐字**：菜单项 `文件名` / `全路径` / `文件`；日志见 spec §3.7（`<n>` = 实际写入项数）。
- **不许动的边界**：`internal/sftp` 的任何传输实现；`ContextMenu.vue` 的关闭逻辑；`FilePane.vue` 的 `.posmenu`；路径栏 `@keyup.esc="resetPath"`；`SftpView` 的 `Esc 清空选中`。
- **`queue.test.js` 的既有红线**：`frontend/src/utils/queue.test.js:505-511` 用**精确多重集**比对 `SftpView.vue` 里所有匹配 `status|partPath =` 的赋值行（白名单 9 条）。新代码**不得**再用 `t.` / `rec.` 这类变量名去赋 `status`/`partPath`（本计划规定的实现天然不新增：`r.status === '完成'` 是只读比较）。若这条测试红了，是写法偏离了本计划，**不得**改白名单。
- **前端测试手段**（没有 jsdom / @vue/test-utils）：纯函数单测（**首选**）、SSR 真渲染（`createSSRApp` + `renderToString`，先例 `TransferQueue.render.test.js`）、读源码钉形状（先例 `filePaneWiring.test.js`）。**读源码断言必须切到目标块**（不做文件级 `not.toContain`——`sftpMenuWiring.test.js:5` 已立此口径）。需要真实鼠标/定时器/hover 的断言写不出来，不要假装能写。
- **证据要求**：每条"已完成/已通过"必须附实际命令与原始输出；没实测的写"未验证"。

## Review Focus

以下 5 类输入/失效模式最可能咬到真实用户；每一类都在任务里配了**真断言**（纯函数单测优先，其次才是形状断言）：

1. **多选文本复制的顺序**：`selection.keys` 是 `Set`，直接展开是插入序（随机），粘贴出的多行顺序会乱 ⇒ 必须按**面板显示顺序**排序。测试落在 T6 的 `copy.test.js`（真单测：乱序 `keys` + 给定 `visibleNames` → 断言输出顺序）。
2. **选中集里含被过滤器隐藏的项**（dotfile 隐藏）：文本复制必须**一并包含**隐藏项（排在可见项之后），而不是只复制可见项。测试落在 T6。
3. **右键的行不在选中集内**：只作用于该行，且**不得改变选中集**（复制是只读动作，不能像 download/remove 那样顺手 `sel.single()`）。测试落在 T6（真单测断言入参 `keys` 调用前后不变）+ T6 的形状断言（复制分支必须位于 `doAction` 的 `sel.single` 之前）。
4. **本地路径分隔符**：远程路径必须走 `posixJoin`；本地走 `localpath.join`——注意它**沿用输入路径自身的分隔符**（`C:\foo`→`C:\foo\name`，`C:/foo`→`C:/foo\name` 不会强制改写），**不得声称"保证反斜杠"**。测试落在 T6 的 `copy.test.js`（两种输入各一条）。
5. **非 ASCII 文件名**（空格 / 中文 / emoji / 单引号）：`CF_HDROP` 载荷必须是 UTF-16LE、每条路径以 `\0` 结尾、整个列表再补一个 `\0`。测试落在 T1（真单测，断言前**先剥尾部 NUL** 再解码）。

> 三路独立审核认为更该进 Focus 的两类（会静默什么都不做、或崩溃级）已在任务里配断言：**冲突框误弹导致静默不复制**（T7 的 `existingNames: []` + 断言）与 **Windows 剪贴板线程/所有权误用**（T2 的硬约束 + 真机验收 T10）。

---

## Task 0a: 干净克隆的前端产物前置

**Files:**
- 无文件改动（生成 `frontend/dist`，已被 gitignore）

**Interfaces:**
- Consumes: 无
- Produces: `frontend/dist/index.html` + `frontend/dist/assets/*`（`//go:embed all:frontend/dist` 的编译前提，T1 之后所有 Go 命令依赖它）

- [ ] **Step 1: 判断是否需要重建**

Run: `ls frontend/dist && ls frontend/dist/assets | head`
Expected: 已有 `index.html` 与 assets 时可跳到 Step 3；目录不存在或为空则继续 Step 2

- [ ] **Step 2: 安装依赖并构建**

Run: `cd frontend && npm ci && npm run build`
Expected: `vite build` 成功、`frontend/dist/assets` 出现带 hash 的 js/css

- [ ] **Step 3: 确认 Go 侧能编译（前置生效）**

Run: `GOCACHE=$PWD/.gocache go build ./...`
Expected: EXIT=0（干净克隆上不做这一步，后面每个 `go test` 都会因 `pattern all:frontend/dist: no matching files found` 硬失败）

---

## Task 0b: 把设计文档与实施计划入库

**Files:**
- Commit: `docs/superpowers/specs/2026-10-12-sftp-copy-submenu-design.md`（新建，当前 untracked）
- Commit: `docs/superpowers/plans/2026-10-12-sftp-copy-submenu.md`（新建，当前 untracked）

**Interfaces:**
- Consumes: 无
- Produces: 分支上可追溯的设计与计划（T1–T10 的验收依据）

- [ ] **Step 1: 确认工作区只有这两个文件是新增**

Run: `git status --short`
Expected: 恰好两行 `?? docs/superpowers/{specs,plans}/2026-10-12-*.md`；若还有别的改动，先停下问用户

- [ ] **Step 2: 提交**

```bash
git add docs/superpowers/specs/2026-10-12-sftp-copy-submenu-design.md docs/superpowers/plans/2026-10-12-sftp-copy-submenu.md
git commit -m "docs: SFTP 右键「复制」子菜单与弹框关闭策略（设计 + 实施计划）"
```

---

## Task 1: `internal/clipboard` 纯逻辑（CF_HDROP 字节 / 临时目录 / 陈旧清理）

**Files:**
- Create: `internal/clipboard/hdrops.go`（无构建标签，全平台可编译以便单测）
- Create: `internal/clipboard/tempdir.go`
- Create: `internal/clipboard/hdrops_test.go`
- Create: `internal/clipboard/tempdir_test.go`

**Interfaces:**
- Produces（T2、T3、T7 依赖）：
  ```go
  // hdrops.go
  func buildHDropBytes(paths []string) []byte   // 私有：20 字节 DROPFILES 头 + UTF-16LE 双 \0 结尾路径列表

  // tempdir.go
  const TempDirPrefix = "sshore-clip-"
  func NewTempDir() (string, error)
  func CleanStaleTempDirs(root string, minAge time.Duration, now time.Time) (int, error)
  ```
- Consumes: 无

- [ ] **Step 1: 写失败测试 `internal/clipboard/hdrops_test.go`**

测试名与断言（`unicode/utf16` + `encoding/binary`）：

| 测试 | 断言 |
|---|---|
| `TestBuildHDropBytesHeader` | 长度 ≥ 20；`LE.Uint32(b[0:4]) == 20`；`b[4:8]`、`b[8:12]`、`b[12:16]` 全 0；`LE.Uint32(b[16:20]) == 1`（fWide=TRUE） |
| `TestBuildHDropBytesPayload` | 对 `[]string{"C:\\a.txt"}`：总长 `20 + 2*(len+1) + 2`；`b[20:]` 末尾是 `00 00 00 00` |
| `TestBuildHDropBytesMultiPath` | 两条路径的载荷逐段拼接；末尾 4 字节是 `00 00 00 00`（每条路径各一个 `\0` + 列表结束一个 `\0`） |
| `TestBuildHDropBytesNonASCII` | 输入含空格/中文/emoji/单引号，如 `[]string{"C:\\中文 目录\\it's 😀.txt"}`：**先剥尾部 NUL**（取 `b[20:]`，再从尾部连续裁掉 `0x0000` 码元）再 `utf16.Decode`，结果与输入的 rune 序列逐字相等；emoji 按代理对计（2 个 code unit） |
| `TestBuildHDropBytesEmpty` | `nil` 与 `[]string{}` 都只返回 20 字节头 + 一个 `\0`（总长 22），不 panic |

> ⚠️ `TestBuildHDropBytesNonASCII` 若不剥尾部 NUL 必然失败：`utf16.Decode(b[20:])` 得到的是带两个 `\x00` 的字符串。审核已实测确认（`trimmed==input? true`，`raw==input? false`）。

测试内自备 helper：`func utf16leBytes(s string) []byte`（`utf16.Encode([]rune(s))` + `binary.LittleEndian`）。

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./internal/clipboard/ -run TestBuildHDrop -v`
Expected: FAIL / 编译错误 `undefined: buildHDropBytes`

- [ ] **Step 3: 实现 `buildHDropBytes(paths []string) []byte`**

在 `hdrops.go`。算法（规格已固定，照写）：20 字节头（`pFiles=20`、`pt=(0,0)`、`fNC=0`、`fWide=1`），随后每条路径的 UTF-16LE 码元序列 + 单个 `\0` 码元，最后再补一个 `\0` 码元。用 `binary.LittleEndian` 写头，`utf16.Encode([]rune(p))` 写路径。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./internal/clipboard/ -run TestBuildHDrop -v`
Expected: PASS（5 个用例）

- [ ] **Step 5: 写失败测试 `internal/clipboard/tempdir_test.go`**

| 测试 | 断言 |
|---|---|
| `TestNewTempDirUniqueAndPrefixed` | 连续两次调用返回不同路径；两者 `os.Stat` 成功且 `IsDir()`；`strings.HasPrefix(filepath.Base(p), TempDirPrefix)`；路径位于 `os.TempDir()` 下；`t.Cleanup` 里 `os.RemoveAll` |
| `TestCleanStaleTempDirsRemovesOldDirsOnly` | `root := t.TempDir()`；建 `sshore-clip-old`（`os.Chtimes` 设为 `now-2h`）、`sshore-clip-new`（`now-10m`）、`other-old`（`now-2h`）、同前缀**文件** `sshore-clip-file`（`now-2h`）；调 `CleanStaleTempDirs(root, time.Hour, now)`；断言返回 `1`、`sshore-clip-old` 不存在、其余三者仍存在 |
| `TestCleanStaleTempDirsMissingRoot` | root 不存在时返回 `(0, nil)`（不报错） |

- [ ] **Step 6: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./internal/clipboard/ -run 'TestNewTempDir|TestCleanStale' -v`
Expected: FAIL `undefined: NewTempDir` / `undefined: CleanStaleTempDirs`

- [ ] **Step 7: 实现 `tempdir.go`（`TempDirPrefix` / `NewTempDir` / `CleanStaleTempDirs`）**

- `NewTempDir` = `os.MkdirTemp("", TempDirPrefix)`（不带 `*` 时 `MkdirTemp` 把 pattern 当前缀 + 随机串；审核实测前缀被保留）。
- `CleanStaleTempDirs`：`os.ReadDir(root)`（**非递归**，因此不会误删下载产生的子目录）；`errors.Is(err, os.ErrNotExist)` ⇒ `return 0, nil`；只处理 `entry.IsDir()` 且 `strings.HasPrefix(entry.Name(), TempDirPrefix)`；用 `entry.Info()` 的 `ModTime()` 与 `now.Add(-minAge)` 比较，早于才 `os.RemoveAll`；单个删除失败**继续**（返回已删数量与 `errors.Join` 的错误）。

- [ ] **Step 8: 跑本任务全部测试 + vet**

Run: `GOCACHE=$PWD/.gocache go test ./internal/clipboard/ -v && GOCACHE=$PWD/.gocache go vet ./internal/clipboard/`
Expected: 全部 PASS，vet 无输出

- [ ] **Step 9: 提交**

```bash
git add internal/clipboard/
git commit -m "feat(clipboard): CF_HDROP 字节构造与剪贴板临时目录（含陈旧清理）"
```

---

## Task 2: Windows 写入与非 Windows 降级（`Support` / `SetFiles`）

**Files:**
- Create: `internal/clipboard/clipboard_windows.go`（`//go:build windows`）
- Create: `internal/clipboard/clipboard_other.go`（`//go:build !windows`）
- Create: `internal/clipboard/clipboard_other_test.go`（`//go:build !windows`）
- Create: `internal/clipboard/clipboard_windows_test.go`（`//go:build windows`）

**Interfaces:**
- Consumes: T1 的 `buildHDropBytes`
- Produces（T3、T7 依赖）：
  ```go
  func Support() bool
  func SetFiles(paths []string) error   // 路径不存在 ⇒ 错误且不写剪贴板
  var ErrUnsupported error              // 非 Windows 平台返回它
  ```
- 平台契约：Windows `Support()==true`；其它平台 `Support()==false` 且 `SetFiles` 返回 `ErrUnsupported`

- [ ] **Step 1: 写失败测试 `clipboard_other_test.go`（`//go:build !windows`）**

| 测试 | 断言 |
|---|---|
| `TestSupportFalseOnNonWindows` | `Support() == false` |
| `TestSetFilesUnsupportedOnNonWindows` | `SetFiles([]string{"/tmp"})` 经 `errors.Is(err, ErrUnsupported)` 为真；且**不 panic** |

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test ./internal/clipboard/ -run TestSupport -v`
Expected: FAIL `undefined: Support`

- [ ] **Step 3: 实现 `clipboard_other.go`**

`var ErrUnsupported = errors.New("当前平台不支持把文件写入系统剪贴板")`；`func Support() bool { return false }`；`func SetFiles(paths []string) error { return ErrUnsupported }`。文件头注释写明"Linux 要让文件管理器粘贴文件需 `text/uri-list`，浏览器剪贴板 API 写不了，需 cgo+GTK3，本项目按用户决策不做（spec §4.4）"。

- [ ] **Step 4: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test ./internal/clipboard/ -v`
Expected: PASS（T1 + T2 全部用例）

- [ ] **Step 5: 写 `clipboard_windows.go`（`//go:build windows`）**

> ⚠️ **`golang.org/x/sys/windows` 没有剪贴板 API**：`GlobalAlloc`/`GlobalLock`/`GlobalUnlock`/`GlobalFree`/`OpenClipboard`/`EmptyClipboard`/`SetClipboardData`/`CloseClipboard`/`CF_HDROP`/`GMEM_MOVEABLE` **全部 undefined**（标准库 `syscall` 也没有；审核用 `GOOS=windows go build` 实测 10/10 报错）。唯一用得上的是 `windows.NewLazySystemDLL`。**不要**去 import wails 的 `internal/frontend/desktop/windows/winc/w32`——它在 `internal/` 树内，导入会被 Go 拒绝（只能当写法参照）。

自建过程表：

```go
var (
    user32   = windows.NewLazySystemDLL("user32.dll")
    kernel32 = windows.NewLazySystemDLL("kernel32.dll")
    // OpenClipboard / EmptyClipboard / SetClipboardData / CloseClipboard ← user32
    // GlobalAlloc / GlobalLock / GlobalUnlock / GlobalFree / RtlMoveMemory ← kernel32
)
const (cfHDrop = 15; gmemMoveable = 0x0002)
```

`SetFiles` 的实现顺序与**四条硬约束**（spec §4.1.2，每条都有真实的坑）：

1. **先校验路径**：逐个 `os.Stat`，不存在 ⇒ `fmt.Errorf("路径不存在: %s", p)`，**在 `OpenClipboard` 之前**完成（真机测试才能在不污染剪贴板的前提下跑）。
2. **`runtime.LockOSThread()` + `defer runtime.UnlockOSThread()`** 放函数首行：否则 goroutine 在 `OpenClipboard`/`CloseClipboard` 之间换线程 ⇒ 剪贴板死锁。
3. **填内存用不会触发 `go vet` `unsafeptr` 的写法**：`procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))`（方向为 Go 指针→uintptr，vet 不报）。**不要**写 `unsafe.Slice((*byte)(unsafe.Pointer(ptr)), n)` —— `GOOS=windows go vet` 会报 `possible misuse of unsafe.Pointer`，而 CI 的 `go-windows` job 跑的就是 `go vet ./...`。
4. **所有权**：`hMem` 只在**成功交接之前**任一环节失败时 `GlobalFree`；`SetClipboardData` 返回非 0 之后所有权归系统，**绝不能再释放或写它**。用一个 `owned := true` 标志 + `defer func(){ if owned { free(hMem) } }()`，成功设置 `owned = false`。

返回值判据（`Proc.Call` 的语义与直觉相反，全部三个坑都要处理）：

- `Proc.Call` 的 error **永远非 nil**（由 `GetLastError` 构造）⇒ **只看第一个返回值**判成败，绝不判 `err != nil`；
- `GlobalUnlock` 是反向约定：返回 `0` 才成功（要配合 `windows.GetLastError()`：成功时为 nil）；
- `LazyProc.Call` 在 proc 找不到时**直接 panic**（`mustFind`）⇒ 首次调用前先 `Find()`，把错误转成普通 error 返回，避免 panic 被 Wails dispatcher 静默吞掉。

`OpenClipboard` 建议加有限重试（最多约 1 秒，每次 `time.Sleep(time.Millisecond)`）——剪贴板可能被资源管理器/剪贴板管理器占着。

- [ ] **Step 6: 写 `clipboard_windows_test.go`（`//go:build windows`，由 CI 的 go-windows job 真跑）**

| 测试 | 断言 |
|---|---|
| `TestSupportTrueOnWindows` | `Support() == true` |
| `TestSetFilesMissingPathErrors` | `SetFiles([]string{`一个必定不存在的路径`})` 返回非 nil 错误，且**不 panic**（路径校验在 `OpenClipboard` 之前 ⇒ 这个用例不会污染 runner 的剪贴板） |
| `TestBuildHDropBytesWindowsBoundary` | `nil` / 单路径 / 多路径的总长与尾部 `\0` 计数 |

> 本仓库已有 windows-tagged 测试先例：`internal/update/lock_windows_test.go`。CI 的 `go-windows` job 是 Windows 侧**唯一**的自动前哨。

- [ ] **Step 7: 交叉编译 + Windows vet（本机无 Windows 运行环境）**

Run:
```bash
GOCACHE=$PWD/.gocache GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./internal/clipboard/ && \
GOCACHE=$PWD/.gocache GOOS=windows GOARCH=386 CGO_ENABLED=0 go build ./internal/clipboard/ && \
GOCACHE=$PWD/.gocache GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/clipboard/
```
Expected: 三条均 EXIT=0、无输出（`go vet` 若有 `unsafe.Pointer` 告警 ⇒ 回到 Step 5 改用 `RtlMoveMemory` 写法）

- [ ] **Step 8: 提交**

```bash
git add internal/clipboard/
git commit -m "feat(clipboard): Windows CF_HDROP 写入（自声明 Win32 proc、线程锁定、所有权安全）与非 Windows 降级"
```

> Windows 上 `SetFiles` 的真实粘贴行为由 T10 真机验收覆盖；本任务只保证编译、vet、windows-tagged 测试与非 Windows 降级正确。

---

## Task 3: `app.go` 三个绑定 + 后台陈旧清理（可注入）+ 重生成绑定

**Files:**
- Modify: `app.go`（`startup()` 里 `go cleanupStaleLocalPartsAt(...)` 同一区域；`CopyLocal`（`:1011`）一带新增绑定；import 区加 `sshore/internal/clipboard`）
- Create: `clipboard_bind_test.go`（`//go:build !windows`）
- Regenerate: `frontend/wailsjs/go/main/App.js`、`frontend/wailsjs/go/main/App.d.ts`（生成物；并检查 `models.ts`）

**Interfaces:**
- Consumes: T2 的 `clipboard.Support` / `clipboard.SetFiles`；T1 的 `clipboard.NewTempDir` / `clipboard.CleanStaleTempDirs`
- Produces（T6、T7 前端调用的绑定名，逐字）：
  ```go
  func (a *App) ClipboardFileSupport() bool
  func (a *App) ClipboardTempDir() (string, error)
  func (a *App) CopyFilesToClipboard(paths []string) error
  // 包级可注入清理钩子（沿用 cleanupStaleLocalPartsAt 的模式）
  var cleanupClipboardTempDirsAt = clipboard.CleanStaleTempDirs
  ```
- 生成物形态（审核已在 `/tmp` 副本实测）：`App.js` 出现 `export function ClipboardFileSupport()`、`ClipboardTempDir()`、`CopyFilesToClipboard(arg1)`；`App.d.ts` 出现 `Promise<boolean>` / `Promise<string>` / `(arg1:Array<string>):Promise<void>`

- [ ] **Step 1: 写失败测试 `clipboard_bind_test.go`（`//go:build !windows`）**

| 测试 | 断言 |
|---|---|
| `TestAppClipboardFileSupportFalseOnLinux` | `(&App{}).ClipboardFileSupport() == false` |
| `TestAppCopyFilesToClipboardDegradesOnLinux` | `(&App{}).CopyFilesToClipboard([]string{"/tmp"})` 返回非 nil 且 `errors.Is(err, clipboard.ErrUnsupported)` |
| `TestAppClipboardTempDirCreatesDir` | `p, err := (&App{}).ClipboardTempDir()`；`err == nil`、`os.Stat(p)` 是目录、`strings.HasPrefix(filepath.Base(p), clipboard.TempDirPrefix)`；`t.Cleanup(os.RemoveAll)` |
| `TestStartupCleanupIsInjectable` | 临时把 `cleanupClipboardTempDirsAt` 换成计数假函数；`(&App{}).startup(context.Background())` 后（等待/同步点见下）断言**假函数被调用**（证明清理走注入点，而不是直接调真函数去扫开发者机器的 `/tmp`） |

> `TestStartupCleanupIsInjectable` 的同步与隔离：真实 `startup` 会派生 goroutine，把假函数写成关闭一个 channel，测试里 `select` 等它（带超时），**不要** `time.Sleep`；替换包级变量后必须 `defer` 恢复原值，避免污染同包其它测试。**这条测试是防止"既有测试真删开发机 `/tmp`"的关键**——审核实测 `app_test.go:1553 TestStartupDoesNotBlockOnStalePartCleanup` 会直接调 `a.startup(...)`。

- [ ] **Step 2: 跑测试确认失败**

Run: `GOCACHE=$PWD/.gocache go test . -run 'TestApp(Clipboard|CopyFilesToClipboard)|TestStartupCleanupIsInjectable' -v`
Expected: FAIL `undefined: clipboard` / `(&App{}).ClipboardFileSupport undefined`

> 注意 `-run` 是**非锚定正则**：写成 `-run TestAppClipboard` 会漏掉 `TestAppCopyFilesToClipboardDegradesOnLinux`（它不含该子串），"PASS" 名不副实。

- [ ] **Step 3: 在 `app.go` 实现三个绑定**

放在 `CopyLocal`（`app.go:1011`）一带（**不是**文件末尾——`app.go` 共 1900+ 行），各带一行 doc 注释说明平台契约（Linux 返回 `false`/`ErrUnsupported`）。

- [ ] **Step 4: 在 `startup()` 挂后台陈旧清理（走注入点）**

在 `go cleanupStaleLocalPartsAt(a.localRecentPaths())` 同一区域追加：

```go
go func() {
    if _, err := cleanupClipboardTempDirsAt(os.TempDir(), time.Hour, time.Now()); err != nil {
        _ = err // 静默：清理只是兜底，且 startup 期 a.emit 尚未注入（spec §4.2）
    }
}()
```

必须 `go`（清理不得挡在控制器装配之前）；`os` 与 `time` 已在 `app.go` import 内。**必须走包级变量 `cleanupClipboardTempDirsAt`**，否则任何调 `a.startup(...)` 的测试都会真去扫删开发机的 `/tmp`。

- [ ] **Step 5: 跑测试确认通过**

Run: `GOCACHE=$PWD/.gocache go test . -run 'TestApp(Clipboard|CopyFilesToClipboard)|TestStartupCleanupIsInjectable' -v && GOCACHE=$PWD/.gocache go test ./ -count=1 && GOCACHE=$PWD/.gocache go vet ./`
Expected: PASS；vet 无输出

- [ ] **Step 6: 重生成前端绑定并还原权限位**

```bash
GOCACHE=$PWD/.gocache wails generate module -tags webkit2_41
git checkout -- frontend/wailsjs/runtime/   # 生成器把 runtime/* 的 mode 改成 755（内容 md5 相同）
git status --short                          # 只应看到 App.js / App.d.ts（以及可能的 models.ts）的改动
grep -n "ClipboardFileSupport\|ClipboardTempDir\|CopyFilesToClipboard" frontend/wailsjs/go/main/App.js frontend/wailsjs/go/main/App.d.ts
```
Expected: `App.js` 3 个新函数、`App.d.ts` 3 行签名；`git status` 无 runtime 相关条目

- [ ] **Step 7: 提交**

```bash
git add app.go clipboard_bind_test.go frontend/wailsjs/
git commit -m "feat(clipboard): app.go 暴露剪贴板绑定并在启动时后台清理陈旧临时目录"
```

---

## Task 4: 子菜单定位纯函数 `utils/menu.js`

**Files:**
- Create: `frontend/src/utils/menu.js`
- Create: `frontend/src/utils/menu.test.js`

**Interfaces:**
- Produces（T5 依赖）：
  ```js
  // anchor: { left, right, top, bottom }（父项 getBoundingClientRect 的四个值）
  // panel:  { width, height }（子面板测量尺寸）
  // viewport: { width, height }
  export function submenuPlacement(anchor, panel, viewport, gap = 2)
  // → { left, top, maxHeight }
  ```
- Consumes: 无

- [ ] **Step 1: 写失败测试 `menu.test.js`**

| 测试 | 输入 | 期望 |
|---|---|---|
| 右侧放得下 | anchor `{left:100,right:180,top:50,bottom:78}`、panel `{width:140,height:90}`、viewport `{width:1000,height:800}` | `{ left: 182, top: 50, maxHeight: 750 }` |
| 右缘左翻 | anchor `{left:900,right:980,top:50,bottom:78}`、panel `{width:140,height:90}`、viewport `{width:1000,height:800}` | `left === 900 - 2 - 140`（758） |
| 左翻仍越界贴左 | anchor `{left:30,right:110,top:50,bottom:78}`、panel `{width:900,height:90}`、viewport `{width:1000,height:800}`（右侧 `112+900=1012 > 1000` ⇒ 左翻；`30-2-900=-872 < 0` ⇒ 贴左） | `left === 0` |
| 下缘上移 | anchor `{left:100,right:180,top:700,bottom:728}`、panel `{width:140,height:300}`、viewport `{width:1000,height:800}` | `top === 500`、`maxHeight === 300` |
| 上移后仍越界贴顶 | 同上但 panel `{width:140,height:900}` | `top === 0`、`maxHeight === 800` |
| 默认 gap | 不传第 4 参 | `left === anchor.right + 2` |

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/menu.test.js`
Expected: FAIL（`submenuPlacement is not a function`）

- [ ] **Step 3: 实现 `submenuPlacement`**

规则（顺序固定）：① `left = anchor.right + gap`，若 `left + panel.width > viewport.width` 则 `left = anchor.left - gap - panel.width`，再若 `left < 0` 则 `left = 0`；② `top = anchor.top`，若 `top + panel.height > viewport.height` 则 `top = viewport.height - panel.height`，再若 `top < 0` 则 `top = 0`；③ `maxHeight = viewport.height - top`。纯函数、无 DOM 访问。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/menu.test.js`
Expected: PASS（6 个用例）

- [ ] **Step 5: 提交**

```bash
git add frontend/src/utils/menu.js frontend/src/utils/menu.test.js
git commit -m "feat(sftp): 子菜单定位纯函数（右侧优先、右/下缘翻转）"
```

---

## Task 5: `MenuSubmenu.vue` 子菜单组件

**Files:**
- Create: `frontend/src/components/MenuSubmenu.vue`
- Create: `frontend/src/components/menuSubmenuWiring.test.js`
- Create: `frontend/src/components/menuSubmenu.render.test.js`

**Interfaces:**
- Consumes: T4 的 `submenuPlacement`
- Produces（T6 依赖的组件契约）：
  ```js
  props:  { label: String(required), items: Array }  // items: [{ key, label, disabled? }]
  emits:  ['pick']                                    // pick(key)
  ```
- 行为契约：父项文案 = `label` + `▸`；**根元素**挂鼠标进出（覆盖父项 + 子面板）；悬停 120ms 展开、离开 160ms 收起；父项可点击切换；子项 `:disabled` 时不派发；点击子项 `emit('pick', key)` 后收起

- [ ] **Step 1: 写失败测试 `menuSubmenuWiring.test.js`（读源码钉形状，先例 `filePaneWiring.test.js`）**

| 测试 | 断言 |
|---|---|
| 定时器常量 | 源码含 `OPEN_DELAY` / `CLOSE_DELAY` 两个常量，值为 120 / 160 |
| **hover 挂在根元素** | 根 `div` 标签上含 `@mouseenter` 与 `@mouseleave`（**不是**只挂在父项 button 上——否则鼠标移向子面板途中会提前收起） |
| 父项点击是切换而非派发 | 父项 `@click` 的处理函数体内含 `!open.value`（或等价 toggle），且**不含** `emit('pick'` |
| 先渲染再测量 | 打开路径上调用了 `nextTick`（`await nextTick()`），且读取面板尺寸的代码在它之后：`body.indexOf('nextTick') < body.indexOf('getBoundingClientRect')` |
| ref 空兜底 | 读取 `panel.value` 处有判空（源码含 `if (!panel.value)` 或可选链 `panel.value?.`） |
| 定位调用 | 源码含 `submenuPlacement(`，且从 `'../utils/menu'` 引入；视口取 `window.innerWidth` / `window.innerHeight` |
| 子项派发 | 子面板内 `v-for` 出 button，含 `:disabled="it.disabled"`，点击处理函数内 `emit('pick'` |
| 收起 | 点击子项的处理函数内同时把展开态置 false |
| 关闭清理 | `onUnmounted` 里清掉两个定时器（`clearTimeout`） |

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/menuSubmenuWiring.test.js`
Expected: FAIL（读文件 ENOENT）

- [ ] **Step 3: 写失败测试 `menuSubmenu.render.test.js`（SSR 真渲染）**

用先例同款手法（`createSSRApp` + `renderToString`，见 `TransferQueue.render.test.js`）：挂载并传 `label: '复制'`、`items: [{key:'copy-name',label:'文件名'},{key:'copy-path',label:'全路径'},{key:'copy-file',label:'文件'}]`，断言渲染结果**含** `复制`，且**不含** `文件名`（子面板默认收起 ⇒ 首屏只渲染父项）。

- [ ] **Step 4: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/menuSubmenu.render.test.js`
Expected: FAIL（组件不存在）

- [ ] **Step 5: 实现 `MenuSubmenu.vue`**

结构：根 `div.menusub`（鼠标进出挂**这里**，覆盖父项 + 子面板）内放父项 `<button class="menusub-head">` 与 `v-if="open"` 的子面板 `div.menusub-panel`（`position: fixed`）。

打开流程：`open = true` → `await nextTick()` → `if (!panel.value) return` → 用 `panel.value.getBoundingClientRect()` 拿尺寸 → `submenuPlacement(headRect, panelSize, { width: window.innerWidth, height: window.innerHeight })` → 写 `left/top/maxHeight`。`v-if="open"` 使面板在置 true 的同一 tick 内还不存在，**必须先 `nextTick` 再测量**，否则 `panel.value.getBoundingClientRect()` 抛 TypeError —— 这是打开子菜单的唯一路径。

`openTimer` / `closeTimer` 两个 `let`；`onUnmounted` 两个都 `clearTimeout`。样式沿用 `ContextMenu.vue` 的按钮形态（`display:flex; justify-content:flex-start; width:100%; height:auto; gap:8px; text-align:left; background:none; border:none;`）与 `:disabled` 置灰规则（`color: var(--text-faint); cursor:default;` + `:hover { background: none }`）。

- [ ] **Step 6: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/components/menuSubmenu`
Expected: PASS（两个文件全部用例）

- [ ] **Step 7: 提交**

```bash
git add frontend/src/components/MenuSubmenu.vue frontend/src/components/menuSubmenuWiring.test.js frontend/src/components/menuSubmenu.render.test.js
git commit -m "feat(sftp): 右键菜单子菜单组件（悬停/点击展开、边缘翻转、禁用态）"
```

---

## Task 6: 复制纯函数 + `SftpView` 接入「复制 ▸」与文本两项

**Files:**
- Create: `frontend/src/utils/copy.js`
- Create: `frontend/src/utils/copy.test.js`
- Modify: `frontend/src/views/SftpView.vue`（import 区加 `MenuSubmenu` 与 `utils/copy`；`doAction` 一带新增动作函数；菜单矩阵 `:1377-1411`）
- Create: `frontend/src/views/sftpCopyMenuWiring.test.js`

**Interfaces:**
- Consumes: T3 的 `ClipboardFileSupport` 绑定；T5 的 `MenuSubmenu`
- Produces（T7 依赖）：
  ```js
  // utils/copy.js —— 纯函数，可被真单测
  export function copyTargetNames(selection, item, visibleNames)  // → string[]
  export function copyPayload(kind, names, ctx)                   // → string；ctx: { pane, localPath, remotePath }

  // SftpView.vue
  const clipboardFileSupport = ref(false)
  const COPY_ITEMS = computed(() => [...])   // [{ key, label, disabled }]，Linux 缺 'copy-file'
  async function copyText(pane, names, kind)
  ```
- 组件契约：`<MenuSubmenu label="复制" :items="COPY_ITEMS" @pick="doAction($event)" />`（`label` 用静态属性）

- [ ] **Step 1: 写失败测试 `frontend/src/utils/copy.test.js`（真单测，覆盖 Review Focus #1/#2/#3/#4）**

| 测试 | 输入 | 期望 |
|---|---|---|
| 单选取该行 | `selection.keys = new Set(['a.txt'])`、`item = {name:'a.txt'}`、`visibleNames = ['a.txt','b.txt']` | `['a.txt']` |
| **多选按显示顺序** | `keys = new Set(['c.txt','a.txt','b.txt'])`（插入序乱）、`item={name:'a.txt'}`、`visibleNames=['a.txt','b.txt','c.txt']` | `['a.txt','b.txt','c.txt']`（不是插入序） |
| **右键行不在选中集 ⇒ 只取该行** | `keys = new Set(['x.txt','y.txt'])`、`item={name:'z.txt'}`、`visibleNames=['x.txt','y.txt','z.txt']` | `['z.txt']` |
| **不改选中集** | 同上输入，调用后断言 `[...selection.keys]` 与调用前逐字相同 | 不变 |
| 空白/`..` 行 | `item = null`；以及 `item = {name:'..'}` | `[]` |
| **隐藏项排末尾** | `keys = new Set(['hidden.txt','a.txt'])`、`item={name:'a.txt'}`、`visibleNames=['a.txt']` | `['a.txt','hidden.txt']` |
| 文本：文件名多行 | `copyPayload('copy-name', ['a.txt','b.txt'], {})` | `'a.txt\nb.txt'` |
| 文本：空集合 | `copyPayload('copy-name', [], {})` | `''` |
| **路径：远程用 POSIX** | `copyPayload('copy-path', ['a.txt'], { pane:'remote', remotePath:'/home/u' })` | `'/home/u/a.txt'` |
| **路径：本地沿用已有分隔符（反斜杠）** | `{ pane:'local', localPath:'C:\\Users\\me' }` | `'C:\\Users\\me\\a.txt'` |
| **路径：本地沿用已有分隔符（正斜杠）** | `{ pane:'local', localPath:'C:/Users/me' }` | `'C:/Users/me/a.txt'`（**不强制改写**，见 spec §3.6 注） |

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/copy.test.js`
Expected: FAIL（`copyTargetNames is not a function`）

- [ ] **Step 3: 实现 `frontend/src/utils/copy.js`**

- `copyTargetNames(selection, item, visibleNames)`：`item` 为空或 `item.name === '..'` ⇒ `[]`；否则 `names = selection.keys.has(item.name) ? [...selection.keys] : [item.name]`；用 `visibleNames` 建索引 `Map`，`sort` 时取不到索引的（隐藏项）用 `Number.MAX_SAFE_INTEGER`；**只读 `selection`，绝不调用任何 `sel.single` 类写操作**。
- `copyPayload(kind, names, { pane, localPath, remotePath })`：`kind === 'copy-name'` ⇒ `names.join('\n')`；`kind === 'copy-path'` ⇒ `names.map(n => pane === 'local' ? localJoin(localPath || '/', n) : posixJoin(remotePath, n)).join('\n')`（`localJoin` 从 `./localpath` 引入；`posixJoin` 的等价物见 T6 Step 5 的说明——`utils/copy.js` 里自带一份最小 POSIX join）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/copy.test.js`
Expected: PASS（11 个用例）

- [ ] **Step 5: 在 `SftpView.vue` 接入菜单与文本两项**

1. import 区加：`import MenuSubmenu from '../components/MenuSubmenu.vue'`、`import { copyTargetNames, copyPayload } from '../utils/copy'`；**并把三个剪贴板绑定加进已有的 wailsjs import 行**（`ClipboardFileSupport, ClipboardTempDir, CopyFilesToClipboard`）——⚠️ 漏了这一步运行期就是 `ReferenceError`，而 Vite 构建**不会**报错（未声明标识符被当作全局原样留在产物里）。最终评审的 Critical 正是这条；T6 的测试已补"绑定必须出现在 import 列表里"的断言防重犯。
2. 菜单矩阵两支各插入 `<MenuSubmenu label="复制" :items="COPY_ITEMS" @pick="doAction($event)" />`：远程行放在 `上传…` 之后、`重命名` 之前；本地行放在 `上传到远程` 之后、`重命名` 之前。
3. `COPY_ITEMS` 用**固定形状**（测试按这个形状断言）：
   ```js
   const COPY_ITEMS = computed(() => {
     const items = [
       { key: 'copy-name', label: '文件名' },
       { key: 'copy-path', label: '全路径' },
     ]
     if (clipboardFileSupport.value) items.push({ key: 'copy-file', label: '文件', disabled: menu.value.pane === 'remote' && !connected.value })
     return items
   })
   ```
   置灰判据写在 **script**（不写进模板）：既有 `sftpMenuWiring.test.js:72` 明令 ContextMenu 模板切片内不得出现 `connected.value`。
4. `clipboardFileSupport`：**放进既有的 `onMounted`（`SftpView.vue:1158`）内部末尾**，不要新增第二个 `onMounted`——`sftpRestoreWiring.test.js:12` 用 `/onMounted\(async \(\) => \{([\s\S]*?)\n\}\)/` 取**第一个** `onMounted`，在它之前插一个新的会让两条既有断言（恢复顺序、回落文案）全红。写法用不阻塞启动路径的：
   ```js
   ClipboardFileSupport().then((v) => { clipboardFileSupport.value = !!v }).catch(() => { clipboardFileSupport.value = false })
   ```
5. `async function copyText(pane, names, kind)`：`if (!names.length) return`（**不得**把剪贴板清成空串）；`const text = copyPayload(kind, names, { pane, localPath: localPath.value, remotePath: remotePath.value })`；`await navigator.clipboard.writeText(text)`；成功 `info('已复制 ' + names.length + ' 个' + (kind === 'copy-name' ? '文件名' : '全路径') + '到剪贴板')`；失败 `err(e)`。
6. `doAction` 加分支，**位置必须在既有 `sel.single`（`SftpView.vue:940`）之前**——否则多选会被悄悄收成一项：
   ```js
   if (name === 'copy-name' || name === 'copy-path') { const names = copyTargetNames(selectionFor(pane), menu.value.item, visibleFor(pane)); closeMenu(); return copyText(pane, names, name) }
   ```

- [ ] **Step 6: 写 `sftpCopyMenuWiring.test.js`（形状断言，注意切块）**

| 测试 | 断言 |
|---|---|
| 菜单矩阵含复制子菜单 | ContextMenu 切片内含 `<MenuSubmenu`、`label="复制"`、`@pick="doAction(` |
| 位置在重命名之前 | 复用 `sftpMenuWiring.test.js` 的锚点切法：`iRemoteRow = tpl.indexOf("menu.pane === 'remote'")`、`iLocalRow = tpl.indexOf(":key=\"'row-local-remote'\"")`，切出远程行段与本地行段，**各自**断言锚点 `<MenuSubmenu` 的位置 > -1 且 < 该段内 `doAction('rename')` 的位置（两段都判，防只改一支；用 `<MenuSubmenu` 而非 `'复制'` 作锚，避免被含"复制"的注释干扰） |
| 三项文案逐字 | 用**花括号配对切片**取 `COPY_ITEMS` 块（先例 `sftpRecoverWiring.test.js:23-30` 的 sliceBlock），只在该块内断言 `'文件名'`、`'全路径'`、`'文件'` 与三个 key（全文 `toContain` 会被 `上传文件…` 等既有文案误满足） |
| Linux 分支不渲染「文件」 | 同一 `COPY_ITEMS` 块内：`block.indexOf('clipboardFileSupport.value') < block.indexOf("'copy-file'")`，且块内含 `if (clipboardFileSupport.value)`（'copy-file' 只在能力为真时才 push） |
| 「文件」置灰判据在 script | 同一 `COPY_ITEMS` 块内含 `menu.value.pane === 'remote' && !connected.value`；且**模板切片内不含**该表达式（既有 `sftpMenuWiring.test.js:72` 的红线） |
| 空白菜单不出现复制 | `v-if="!menuHasItem"` 分支切片内**不含** `<MenuSubmenu` |
| 能力查询在既有 onMounted 内 | 存在一个 `onMounted(async () => {` 块同时含 `ClipboardFileSupport()` 与 `clipboardFileSupport.value = false`；且全文件 `onMounted(async () => {` 的出现次数**仍为 1**（防新增第二个打红 `sftpRestoreWiring`） |
| 复制分支在 sel.single 之前 | `doAction` 函数体内：`indexOf("'copy-name'")` > -1 且 < `indexOf('sel.single(')`（与既有 refresh 分支同款位置断言） |
| doAction 有分派 | `doAction` 函数体含 `'copy-name'` 与 `'copy-path'` 分支 |
| 用纯函数而非内联实现 | `SftpView.vue` 从 `'../utils/copy'` 引入 `copyTargetNames`；`doAction` 复制分支内调用它（**不得**在 SftpView 里内联排序逻辑——排序正确性由 `copy.test.js` 真单测守） |

- [ ] **Step 7: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/copy.test.js src/views/sftpCopyMenuWiring.test.js && npx vitest run`
Expected: 新测试 PASS；既有前端测试全绿。

**已核实的兼容性（读 `sftpMenuWiring.test.js` 全文确认）**：该文件用字面量正则 `/doAction\('([^']+)'\)/g` 收集模板动作名并要求每个都有分派；本任务的复制项写成 `@pick="doAction($event)"`（**无字面量参数**），不会被收进集合，`toEqual(['download','remove','rename','upload'])`、"动作名都有分派"、`not.toMatch(/connected\.value/)`、remoteRow/localRow 各 1 个 refresh 四条断言均不受影响。若真的红了，说明改动偏离计划，先查偏差，**不得**为变绿改既有断言。

- [ ] **Step 8: 提交**

```bash
git add frontend/src/utils/copy.js frontend/src/utils/copy.test.js frontend/src/views/SftpView.vue frontend/src/views/sftpCopyMenuWiring.test.js
git commit -m "feat(sftp): 右键菜单加「复制」子菜单与文件名/全路径复制（纯函数 + 真单测）"
```

---

## Task 7: 「文件」——本地直写、远程先下载后入剪贴板

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（`runBatch` 签名与返回值、`doAction` 加 `copy-file` 分支、新增 `copyFilesAsObjects`）
- Create: `frontend/src/views/sftpCopyFileWiring.test.js`

**Interfaces:**
- Consumes: T6 的 `COPY_ITEMS` / `copyTargetNames` / `doAction` 分派位；T3 的 `ClipboardTempDir`、`CopyFilesToClipboard`
- Produces:
  ```js
  async function copyFilesAsObjects(pane, names)   // 本地：直写；远程：下载到临时目录后写
  // runBatch 新增三个可选参数（都有默认值 ⇒ 既有调用点无需改）：
  //   hiddenSelected  默认 undefined = 内部计算
  //   keepSelection   默认 false；true 时不执行 sel.remove
  //   existingNames   默认 undefined = 用 targetItems 派生；复制场景传 [] 强制"无冲突"
  // runBatch 返回值改为 { ...summarize(results), results }
  ```
- **既有调用点（7 个，全部要人工确认兼容）**：
  - `runBatchFor`：`SftpView.vue:725`（download）、`:728`（upload）
  - `doAction`：`:953`（download）、`:960`（upload）
  - `onPaneDrop`（`:994` 起）：`:1007`（download）、`:1009`（upload）
  - `handleSystemDrop`（`:1118` 起）：`:1126`（upload，带 `systemPaths`）
  - （`onMoveDrop`（`:1014` 起）**不**调用 `runBatch`，它走自己的 `classify`/`askConflict`）

- [ ] **Step 1: 写失败测试 `sftpCopyFileWiring.test.js`（形状断言，注意切块）**

| 测试 | 断言 |
|---|---|
| 本地直写 | `copyFilesAsObjects` 函数体内含 `localJoin(` 与 `CopyFilesToClipboard(` |
| 远程先建临时目录 | 函数体内 `ClipboardTempDir(` 出现在 `runBatch(` 之前 |
| 远程走队列 | 函数体内含 `runBatch({` 且 `direction: 'download'`、`targetDir: tmp`（同一变量名） |
| **不弹冲突框（关键）** | `runBatch(` 调用内含 `existingNames: []`（**不是**只写 `hiddenSelected: 0`——审核已实测：`existing` 取自用户当前本地目录，本地有同名文件就会误弹冲突框并按默认策略「跳过」⇒ 静默什么都不复制） |
| 同时传 hiddenSelected: 0 | 调用内含 `hiddenSelected: 0` |
| 不清空用户选中集 | 调用内含 `keepSelection: true` |
| 临时目录失败有兜底 | 函数体内含 `try {` 与 `catch`，且 catch 内调 `err(` |
| 队列返回 null 有兜底 | 函数体内对 `runBatch(` 的返回值有 `if (!s)` 判空（并写 error 日志后 return；spec §3.6 要求取消/失败要留痕） |
| 只写成功项 | 切出**远程分支**（从 `ClipboardTempDir(` 到函数体末尾）后断言：`status === '完成'` 的过滤出现在 `CopyFilesToClipboard(` 之前（全局比 indexOf 会被本地分支的调用提前满足） |
| 远程路径用本地拼接 | 远程分支内含 `localJoin(tmp`（临时目录是本地路径，**不能**用 `posixJoin`） |
| 取消 ⇒ 整批不写 | 函数体内含对 `'取消'` 的判定（`s.results.some(` 或等价），且该分支在写剪贴板之前 return |
| 部分失败仍写 | 函数体内对 `failed > 0` 有 `err(` 调用 |
| 全部失败不写 | 成功项数为 0 时 return 且不调 `CopyFilesToClipboard(` |
| runBatch 返回值 | `runBatch` 函数体末 `return { ...s, results }`（既有 `s.ok/s.failed/s.skipped` 字段仍在） |

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/sftpCopyFileWiring.test.js`
Expected: FAIL

- [ ] **Step 3: 改 `runBatch` 三处**

① 形参加 `hiddenSelected`、`keepSelection = false`、`existingNames`；
② `const existing = (targetItems || []).map((it) => it.name)`（在 `SftpView.vue:762`，**紧跟在** `:761` 的 `const hidden = …` 之后）改为 `const existing = existingNames ?? (targetItems || []).map((it) => it.name)`；
③ `const hidden = hiddenSelectedCount(...)` 改为 `const hidden = hiddenSelected ?? hiddenSelectedCount(...)`；末尾 `sel.remove(sourceSelection, doneNames)` 改为 `if (!keepSelection) sel.remove(sourceSelection, doneNames)`（注释：复制到剪贴板是只读动作）；末尾 `return s` 改为 `return { ...s, results }`。

新增参数都有默认值 ⇒ 7 个既有调用点无需修改；`{ ...s }` 保留 `ok/skipped/failed/failures` 字段 ⇒ 读这些字段的调用点不受影响。逐个人工确认一遍。

> 保留 `runBatch` 那条 `批量download N 项：成功 …` 日志：远程复制**确实**是一次下载，这条是进度线索；随后 `copyFilesAsObjects` 再补一条「已复制 … 到剪贴板」。

- [ ] **Step 4: 实现 `copyFilesAsObjects(pane, names)`**

整体套 `try { … } catch (e) { err(e) }`（跨进程绑定可能抛错；`doAction` 的调用方是模板事件处理器，抛出会变成无提示的 unhandled rejection）。

- 本地面板：`await CopyFilesToClipboard(names.map(n => localJoin(localPath.value || '/', n)))`；成功 `info('已复制 ' + names.length + ' 个文件到剪贴板（可直接粘贴到文件管理器）')`。
- 远程面板：
  1. `const tmp = await ClipboardTempDir()`
  2. `const s = await runBatch({ direction: 'download', names, sourceDir: remotePath.value, targetDir: tmp, sourceItems: visibleRemoteItems.value, gesture: 'batch', hiddenSelected: 0, keepSelection: true, existingNames: [] })`
  3. `if (!s) { await err('复制已取消，未写入剪贴板'); return }`（runBatch 在冲突框被取消时返回 `null`）
  4. `if (s.results.some((r) => r.status === '取消')) { await err('复制已取消，未写入剪贴板'); return }`（**半批数据不得进剪贴板**）
  5. `const paths = s.results.filter((r) => r.status === '完成').map((r) => localJoin(tmp, r.name))`（临时目录是本地路径 ⇒ 必须 `localJoin`）
  6. `if (!paths.length) { await err('复制失败：没有成功的项'); return }`
  7. `await CopyFilesToClipboard(paths)`；`info('已复制 ' + paths.length + ' 个文件到剪贴板（可直接粘贴到文件管理器）')`（`<n>` = **实际写入项数**）
  8. `if (s.failed > 0) await err('有 ' + s.failed + ' 项复制失败（见传输队列）')`

> ⚠️ **不得**写 `t.status = …` / `rec.status = …` 这类赋值：`queue.test.js:505-511` 用精确多重集白名单钉死 SftpView 里所有 `status|partPath` 赋值行（详见 Global Constraints）。上面的实现只做 `r.status === '完成'` 只读比较。

- [ ] **Step 5: `doAction` 加 `copy-file` 分支**

与文本两项同址（仍在 `sel.single` 之前）：`if (name === 'copy-file') { const names = copyTargetNames(selectionFor(pane), menu.value.item, visibleFor(pane)); closeMenu(); return copyFilesAsObjects(pane, names) }`。

- [ ] **Step 6: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/views/sftpCopyFileWiring.test.js && npx vitest run`
Expected: 新测试 PASS；全量前端测试绿（含 `queue.test.js` 的白名单）

- [ ] **Step 7: Go 侧联动检查**

Run: `GOCACHE=$PWD/.gocache go test ./... -count=1`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add frontend/src/views/SftpView.vue frontend/src/views/sftpCopyFileWiring.test.js
git commit -m "feat(sftp): 复制「文件」到系统剪贴板（远程先入队下载到临时目录，显式空目标免冲突框）"
```

---

## Task 8: 5 个模态框改为只能按钮关闭

**Files:**
- Modify: `frontend/src/components/AppDialog.vue:24,34`、`ConflictDialog.vue:32`（并改 `:20-22` 注释）、`SearchOverlay.vue:112`（并改 `:84` 注释）、`SettingsDialog.vue:37`、`SyncConflictsDialog.vue:33`
- Create: `frontend/src/components/dialogCloseWiring.test.js`

**Interfaces:**
- Consumes: 无
- Produces: 5 个组件的关闭路径只剩显式按钮；`emit('cancel')` / `emit('close')` 的事件名与参数**不变**

- [ ] **Step 1: 写失败测试 `dialogCloseWiring.test.js`（断言收进 `<template>` 切片）**

对 5 个文件逐个 `readFileSync`，并切出模板段：`const tpl = src.slice(src.indexOf('<template>'), src.indexOf('</template>'))`。

| 测试 | 断言 |
|---|---|
| 无遮罩关闭 | 5 个文件的 **`tpl`** 均不含 `@click.self` |
| 无 Esc 关闭 | 5 个文件的 **`tpl`** 均不含 `keyup.esc` |
| 注释也不残留（防过时注释） | `ConflictDialog.vue` 与 `SearchOverlay.vue` 的**全文**均不含 `keyup.esc`（它们的注释里有该字面量，Step 3 必须改掉；这两条是本任务唯一允许全文判定的地方） |
| 显式按钮仍在 | AppDialog 模板含 `emit('ok'` 与 `emit('cancel'`；ConflictDialog 含 `emit('cancel'` 与 `emit('confirm'`；SearchOverlay 含 `emit('close'`；SettingsDialog 含 `emit('close'`；SyncConflictsDialog 含 `emit('close'` |
| 保留 Enter 提交 | AppDialog 模板仍含 `@keyup.enter="confirm"` |
| 不误伤 | `FilePane.vue` 仍含 `@click.self="onListBlankClick"` 与 `@keyup.esc="resetPath"`（证明本轮没扩大范围） |

> 说明：不能对 5 个文件做**全文** `not.toContain('keyup.esc')`——审核实测 `ConflictDialog.vue:20`、`SearchOverlay.vue:84`、`ConflictDialog.vue:60` 的注释里都有该字面量，全文断言会假阳性变红。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/dialogCloseWiring.test.js`
Expected: FAIL（前两组与"注释不残留"不满足）

- [ ] **Step 3: 逐个删除遮罩与 Esc 关闭，并改写过时注释**

- `AppDialog.vue`：删遮罩 `@click.self="emit('cancel')"`；删输入框 `@keyup.esc="emit('cancel')"`（保留 `@keyup.enter="confirm"`）。
- `ConflictDialog.vue`：删 `@click.self` 与 `@keyup.esc`；**改写 `:20-22` 与 `:60` 的注释**——原文写"`@keyup.esc` 形同虚设"/"Esc 即可关闭"/"遮罩靠 tabindex 接收 Esc"，删属性后这些说法既错又会让断言红。`ref="overlay" tabindex="-1"` + `nextTick` 聚焦**保留**，注释改为说明它只用于焦点管理（不再有 Esc 语义）。
- `SearchOverlay.vue`：删 `@click.self` 与 `@keyup.esc`；**改写 `:84` 注释**（原文"`@keyup.esc` 挂在遮罩上…（spec §8.2 要求 Esc 可关）"），保留 `watch` + `nextTick` 聚焦输入框的逻辑（它本身与 Esc 无关）。
- `SettingsDialog.vue`：删 `@click.self="emit('close')"`。
- `SyncConflictsDialog.vue`：删 `@click.self="emit('close')"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/components/dialogCloseWiring.test.js && npx vitest run`
Expected: PASS；既有 `searchOverlay.test.js` 等仍全绿（它们都针对 FilePane，不受影响）

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/AppDialog.vue frontend/src/components/ConflictDialog.vue frontend/src/components/SearchOverlay.vue frontend/src/components/SettingsDialog.vue frontend/src/components/SyncConflictsDialog.vue frontend/src/components/dialogCloseWiring.test.js
git commit -m "fix(ui): 模态框只能通过显式按钮关闭（去掉遮罩点击与 Esc 关闭）"
```

---

## Task 9: README 同步 + 全量门禁（含 Windows job 等价）

**Files:**
- Modify: `README.md:169` 一带（"右键菜单 / 拖动投递…"那一行）
- Modify: `README.en.md:184-185` 附近（对应句）

**Interfaces:**
- Consumes: 前 8 个任务的全部产出
- Produces: 文档与代码一致；门禁全绿

- [ ] **Step 1: 更新 README 的 SFTP 功能行**

在 `右键菜单` 后补一句，例如：`（含「复制 ▸」：文件名 / 全路径 / 文件——「文件」在 Windows 上可直接粘贴到资源管理器，Linux 仅提供前两项）`。英文 README 同步对应句子。**只改这一处**，不得顺手重排其它内容。

- [ ] **Step 2: 跑全量门禁**

Run: `GOCACHE=$PWD/.gocache make ci`
Expected: 四步全绿（`vite build` 成功、`go vet` 无输出、`go test ./... -race -count=1` 全 PASS、`vitest run` 全 PASS）。任一步失败 ⇒ 停下修到绿，或如实报告失败项与原始输出。

- [ ] **Step 3: 交叉编译 + Windows vet 复核（CI `go-windows` job 的等价，`make ci` 不覆盖）**

Run:
```bash
GOCACHE=$PWD/.gocache GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./... && \
GOCACHE=$PWD/.gocache GOOS=windows GOARCH=386 CGO_ENABLED=0 go build ./... && \
GOCACHE=$PWD/.gocache GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...
```
Expected: 三条均 EXIT=0；`go vet` **无输出**（有 `unsafe.Pointer` 告警说明 T2 的填内存写法回退了）

- [ ] **Step 4: 提交**

```bash
git add README.md README.en.md
git commit -m "docs: README 补充 SFTP 右键「复制」子菜单说明"
```

---

## Task 10: Windows 真机验收（不可推断，必须实测）

**Files:**
- Create: `docs/superpowers/reports/2026-10-12-sftp-copy-submenu-acceptance.md`
- 产出：Windows 侧二进制 + 粘贴结果取证（截图/哈希/命令原始输出）

**Interfaces:**
- Consumes: 前 9 个任务的全部产出
- Produces: 验收报告；未能实测的项逐条写明"未验证"

**验收清单（逐条附证据）**

- [ ] **Step 1: 构建并投递**

`make windows`（产出 `build/bin/sshore.exe`），按 skill `sshore-real-machine-testing` / `vbox-win` 投递到客户机并启动。启动后确认应用没崩（剪贴板线程/所有权写错会在**点复制那一刻**才暴露）。

- [ ] **Step 2: 本地「文件」→ 资源管理器粘贴**

在客户机本地面板右键一个文件（至少覆盖：普通名、含中文/空格名、含单引号名）→ 复制 ▸ 文件 → 在资源管理器目标目录 Ctrl+V → `certutil -hashfile` 或 `Get-FileHash` 对比源/目标哈希。再对一个**目录**重复一次（CF_HDROP 支持目录）。

- [ ] **Step 3: 远程「文件」→ 资源管理器粘贴**

连上真实 SSH 主机，右键远程文件 → 复制 ▸ 文件；确认：传输队列出现下载任务、完成后日志出现 `已复制 1 个文件到剪贴板（可直接粘贴到文件管理器）`、粘贴后哈希一致。

**并专门验"本地有同名文件"的场景**（这是本轮审核抓到的 Blocker）：先在本地目标目录放一个同名文件，再对远程同名文件执行复制 ▸ 文件 —— 预期**不弹冲突框**、仍然正常写入剪贴板（`existingNames: []` 生效）。若弹了框或日志说"没有成功的项"，本任务失败。

- [ ] **Step 4: 远程多选与取消场景**

- 多选 3 个远程文件 → 复制 ▸ 文件：3 项都进队列、全部完成后日志 `<n>` 为 3；粘贴出的 3 个文件哈希逐个核对。
- 多选后立刻在队列里点「取消」：**不得**写入剪贴板（日志出现取消提示，粘贴仍是旧内容）。
- 含被过滤隐藏的 dotfile 选中项：文本两项要含它（排在末尾）。

- [ ] **Step 5: 文本两项与选中集不变**

复制 ▸ 文件名 / 全路径，粘到记事本逐字核对（多选多行顺序 = 面板显示顺序）；核对复制前后面板头「已选 N 项」不变、行高亮不变（远程「文件」下载完成后也再核一次）。

- [ ] **Step 6: 子菜单交互（spec §3.2 全项）**

- 悬停父项展开；**鼠标从父项平移到右侧子面板，面板不得中途消失**（这是 hover 挂根元素的验收点）；
- 父项点击可开合；
- 贴右缘右键：子面板左翻；贴下缘：上移；
- 点击子项执行动作且整条菜单关闭；
- 未连接远程主机时右键远程文件：「文件」项置灰，另两项可用。

- [ ] **Step 7: 弹框关闭策略**

分别打开 5 个模态框，逐一点遮罩、按 Esc，断言**都不关**；再用各自按钮关闭，断言能关。

- [ ] **Step 8: Linux 复核**

本机确认 `ClipboardFileSupport()` 返回 false（`go test . -run TestAppClipboardFileSupportFalseOnLinux` 已覆盖）；在 Linux 桌面上确认右键菜单**没有**「文件」项、且「文件名/全路径」粘贴有效（若失效，按 spec §8.3 回头找用户重新决策，不得自行改方案）。

- [ ] **Step 9: 写验收报告并提交**

报告含：每条验收项的**实际命令 + 原始输出/哈希**；未实测项写"未验证"。

```bash
git add docs/superpowers/reports/2026-10-12-sftp-copy-submenu-acceptance.md
git commit -m "test(acceptance): Windows 真机验收「复制」子菜单与弹框关闭策略"
```

---

## 自检记录

### 结构性自检（写完计划后回看 spec）

- **spec 覆盖**：§1 → T5–T8；§3.1 → T6 Step 5/6；§3.2（含 nextTick 与 hover 根元素）→ T4/T5 + T10 Step 6；§3.3 → T6 Step 3/5 + T7 Step 3/4；§3.4（含"置灰在 script"）→ T6 Step 5/6 + T7 Step 1；§3.5（含隐藏项排末尾、空集不写）→ T6 Step 1/5；§3.6（含空目标免冲突框、取消整批不写、`<n>` 口径）→ T6 Step 5 + T7 Step 3/4；§3.7 → T6 Step 5 + T7 Step 4；§4.1/§4.1.1/§4.1.2 → T1/T2；§4.2（含可注入）→ T3 Step 1/4；§4.3/§4.4 → T1/T2 Step 3/T3 Step 4/T7 + T10 Step 8；§5（含注释改写）→ T8；§7 → T9/T10；§8 → T10 + 报告。文档入库 → T0b；前端产物前置 → T0a。无 uncovered 条目。
- **占位符扫描**：无 TBD/TODO；每个断言都有具体值；无 `t.Skip` 占位测试；无"处理边界情况"这类空话。
- **类型一致性**：`buildHDropBytes`（T1）→ T2；`Support`/`SetFiles`/`ErrUnsupported`（T2）→ T3；`ClipboardFileSupport`/`ClipboardTempDir`/`CopyFilesToClipboard` + `cleanupClipboardTempDirsAt`（T3）→ T6/T7；`submenuPlacement`（T4）→ T5；`MenuSubmenu` 的 `label`/`items`/`pick`（T5）→ T6；`copyTargetNames`/`copyPayload`（T6，纯函数）→ T6/T7；`runBatch` 的 `hiddenSelected`/`keepSelection`/`existingNames` + `{...s, results}`（T7）→ 只在 T7 消费，7 个既有调用点靠默认值兼容。命名前后一致。
- **Review Focus**：5 条分别在 T6（1/2/3/4，真单测）、T1（5，真单测）落地；另两条（冲突框误弹、Windows 剪贴板线程/所有权）分别在 T7 与 T2/T10 落地。
- **比例**：计划约为 spec 的 2.4 倍；代码块只出现在"必须逐字写对"的地方（DROPFILES 布局、proc 表、`COPY_ITEMS` 形状、`runBatch` 改动点），其余是签名/断言/命令。

### 修复记录

**第一阶段（作者自审）**：

1. T4「左翻仍越界贴左」用例参数自相矛盾 ⇒ 改为 `width:900` 并写明算式。
2. T7 漏掉 `runBatch` 收尾 `sel.remove` 清空选中集 ⇒ 新增 `keepSelection`。
3. T7 缺 `ClipboardTempDir()` 抛错与 `runBatch` 返回 `null` 的兜底 ⇒ 补 `try/catch` 与判空。
4. T6 缺"空白菜单不出现复制"断言 ⇒ 补上。
5. T6 文案断言用全文 `toContain` 会被既有文案误满足 ⇒ 收紧到块内。
6. 删除一个永远 `t.Skip` 的占位测试。
7. 增加 Task 0（文档入库）。
8. 核实 `sftpMenuWiring.test.js` 全部断言与本计划兼容，并把依据写进 T6。

**第二阶段（3 个独立 subagent 审核 + 作者逐条复核）**：

采纳并已修：

1. **[Blocker] Windows 剪贴板 API 不存在**：`x/sys/windows` 不导出 `GlobalAlloc/OpenClipboard/SetClipboardData/CF_HDROP` 等 10 个符号（作者独立复核：真实模块目录 grep 零命中）⇒ T2 Step 5 与 spec §4.1.1 改为自建 Win32 proc。
2. **[Blocker] 冲突框误弹导致静默不复制**：`runBatch` 的 `existing` 取自**用户当前本地目录**，`hiddenSelected: 0` 挡不住 ⇒ 改为 `existingNames: []`，断言升级；spec §3.6 同步。
3. **[Major] `go vet` unsafeptr 会让 CI windows job 变红** ⇒ T2 明确用 `RtlMoveMemory`/`WriteProcessMemory` 写法，并把 Windows `go vet` 列为门禁（T9 Step 3）。
4. **[Major] 所有权语义**：`SetClipboardData` 成功后不得 `GlobalFree` ⇒ spec §4.1.2 第 3 条 + T2 的 `owned` 标志。
5. **[Major] 线程锁定**：缺 `runtime.LockOSThread()` 会剪贴板死锁 ⇒ spec §4.1.2 第 1 条 + T2。
6. **[Major] `Proc.Call` 语义三坑**（error 恒非 nil / `GlobalUnlock` 反向 / `LazyProc` panic）⇒ spec §4.1.2 第 2 条 + T2。
7. **[Major] 取消后已完成的项仍会入剪贴板**（`summarize` 不统计"取消"）⇒ T7 Step 4 加"出现取消项则整批不写"，spec §3.6 同步。
8. **[Major] `nextTick` 时序**：`v-if="open"` 后同 tick 测量会 TypeError，且这是打开子菜单的唯一路径 ⇒ T5 明确先 `nextTick` 再测量 + ref 空兜底；spec §3.2 同步。
9. **[Major] hover 必须挂根元素**：只挂父项会让鼠标移向子面板时提前收起 ⇒ T5 断言与实现均改；spec §3.2 同步。
10. **[Major] 新增第二个 `onMounted` 会打红既有 `sftpRestoreWiring` 断言**（它取第一个 `onMounted`），且原计划的"放在 window.addEventListener 之前"理由是错的（监听在 `onActivated`）⇒ T6 Step 5 改为放进既有 `onMounted` 内部，并加"`onMounted(async () => {` 出现次数仍为 1"的断言。
11. **[Major] T8 断言会命中注释字面量假阳性** ⇒ 断言收进 `<template>` 切片 + 要求改写 `ConflictDialog`/`SearchOverlay` 的过时注释；spec §5 同步。
12. **[Major] Review Focus 1/2/3/4 只有形状断言、测不出真行为** ⇒ 把作用集合与文本拼接抽成 `utils/copy.js` 纯函数 + 11 条真单测（T6）。
13. **[Major] 置灰断言写错位置**（写在"模板切片内"，实际判据在 script，且会撞既有红线）⇒ 移到 `COPY_ITEMS` 块（T6 Step 6）。
14. **[Major] 复制分支必须位于 `sel.single` 之前**只有散文约束 ⇒ 加同款位置断言（T6 Step 6）。
15. **[Major] Task 0 撞名且前端产物前置无人承接** ⇒ 拆成 Task 0a（前置）/ Task 0b（文档入库）。
16. **[Major] 未利用 CI 的 `go-windows` job** ⇒ 新增 `clipboard_windows_test.go` + T9 Step 3 的 Windows vet/build 门禁。
17. **[Major] startup 清理不可注入 ⇒ 既有测试会真删开发机 `/tmp`**（`app_test.go:1553` 直接调 `a.startup(...)`）⇒ 走包级变量 `cleanupClipboardTempDirsAt` + `TestStartupCleanupIsInjectable`；spec §4.2 同步。
18. **Minor 全采纳**：`-run` 正则漏测（改为 `-run 'TestApp(Clipboard|CopyFilesToClipboard)|TestStartupCleanupIsInjectable'`）、NonASCII 断言需先剥尾部 NUL、`NewTempDir` 注释与签名不符、清理并发缺口与 `OpenClipboard` 竞争写进 spec §8、startup 失败处理措辞与实际一致（改为静默）、wails 子命令出处、`CopyLocal` 位置（`:1011`，非文件末尾）、`clipboard` import、7 个调用点与宿主函数名逐个纠正、`ConflictDialog`/`SearchOverlay` 的 `watch+nextTick`（不是 `onMounted`）、T7 断言的切块位置、`copyText` 空集守卫、`localpath.join` 分隔符口径如实描述、日志文案 `<n>` 口径、`models.ts` 一并提交、`COPY_ITEMS` 切片改花括号配对、`queue.test.js` 白名单红线写进 Global Constraints、单引号纳入非 ASCII 用例、`RtlMoveMemory` 未真机核实写进 spec §8。

**驳回（附理由）**：

1. **"隐藏目录会被当普通文件下载"（前端审核 M7）** —— 不成立。`FilePane.vue:168` 的隐藏过滤在 FilePane 内部（`props.items.filter(it => !it.name.startsWith('.'))`），SftpView 的 `remoteItems` 始终是后端全量（含 dotfile），而 `visibleRemoteItems` 只剔除内部 part 文件（`isInternalTempName`）⇒ `sourceItems` 对隐藏目录的 `isDir` 信息完整。已在 Review Focus 的引注里保留该论证，防止后人重复误判。
2. **"用 `gesture: 'single'` 代替显式空目标"（两份审核的建议之一）** —— 采纳了更清晰的方案 `existingNames: []`：`gesture: 'single'` 能绕过确认框，但它的语义是"单文件手势免确认"，用它来表达"目标目录是全新的"属于借语义（后续维护者会误读），且它同时关掉了隐藏项提示，掩盖真实意图。
3. **"`make ci` 之外还要跑 windows 全量 `go test`"** —— 部分采纳：本地加了 `GOOS=windows go vet`（能抓 `unsafeptr`，且审核实测它 EXIT=0 可行），windows 全量 `go test` 与真机粘贴仍交给 CI 的 `go-windows` job 与 T10，不在本机伪跑。
