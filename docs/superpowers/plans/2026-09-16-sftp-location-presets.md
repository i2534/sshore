# SFTP 位置预设与 Windows 盘符实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

并支持用独立配置文件 `presets.toml` 自定义全部预设项

`presets.toml` 是预设组的唯一来源（首次启动文件不存在时由 `preset.Defaults()` 生成一次**带注释的模板**，之后应用永不改写）

**Tech Stack:** Go 1.26 + Wails v2.15.0 + Vue 3 + Pinia + vitest。盘符与已知文件夹用 **已在依赖图中**的 `golang.org/x/sys v0.46.0`（`go mod tidy` 只会把它从 indirect 提到 direct，版本与下载都不变）。

**Spec:** `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`（本计划是 §9「位置模型」的**补充**：原文只定义了书签 + 最近，本计划为其加"预设"与"磁盘"两组，并在 Task 7 回写 spec 决策 34/35/36）

**前置:** P1（`2026-09-16-sftp-p1-selection-batch-drag.md`）与 P2（`2026-09-16-sftp-p2-filter-search-locations.md`）均已完成，`HEAD = 2c2543b`（tag `v0.5.0`），工作区干净。

---

## 0. 背景（为什么会有这个计划）

1. **位置下拉目前基本是空壳。** `FilePane.vue:99-107` 只渲染 `书签` / `最近` 两组，二者都为空时（新装、未收藏、无最近）下拉里除占位项外**什么都没有**。spec §9（`…design.md:375`）与 P2 计划都从未定义"固定预设"。
2. **Windows 上无法切换盘符。** 没有任何盘符入口；`curpath` 只是不可编辑的 `<span>`（`FilePane.vue:98`），`..` 是唯一向上途径。
3. **本地路径助手是 POSIX-only，`..` 在 Windows 上会把用户甩回 `/`。** `SftpView.vue:227-233` 的 `parentOf` 用 `lastIndexOf('/')` 切分，对 `C:\Users\lan` 得 -1 → 返回 `'/'`；Go 在 Windows 把 `/` 解析为**当前盘**的根（通常 C:），于是显示变成 `/` 且被锁死在该盘。`parentOf` 来自 `34c20a9`（0.4.x 就有的老问题，非 P1/P2 引入）。盘符预设一加进来，这个缺陷会立刻变成"选了 D: 就再也回不去"的体感故障，**所以必须同批修**。

**不在范围**（明确 YAGNI）：

- 不加"打开目录…"按钮（与决策 2 的紧凑单行布局、决策 3 的"工具栏零新增"冲突）。
- 不做"记住上次位置"（决策 19 的"最近"已覆盖）。
- 不改 `utils/batch.js:8-9` 的 `joinDir`（它同时服务下载/上传两个方向，改造要按方向分派、会牵动 `batch.test.js`；已核实：它产出的 `C:\dir/name` 混用分隔符 **Go 侧完全可用**，只是传输记录里的展示不够漂亮）。
- Linux 的本地化目录名（`~/桌面` 之类）不专门解析 `user-dirs.dirs`：`Local` 用"候选名 + **存在性检查**"，不存在的目录**静默隐藏**——宁可不显示，也不能给一个点进去就报错的预设。本机实测 `~/Desktop|~/Documents|~/Downloads` 均存在且 `user-dirs.dirs` 指向它们，覆盖了常见情形。

**已确认（用户回复）**

1. 预设清单确认：本地 `主目录 / 桌面 / 下载 / 文档 / 根目录` + Windows 全盘符；远程 `主目录 / 根目录`。
2. **预设全部由配置文件配置，默认值就是上述几项**（第二轮）；**第三轮评审后定案：预设放独立文件 `presets.toml`**（见下）。
3. 本地路径语义一并修（Task 3 + Task 6）；Windows 真机复测要做（Task 8，必做）。

### 预设的最终形态：独立文件 `presets.toml`（第三轮定案）

- **位置**：与主配置同目录 —— unix 是 `~/.config/sshore/presets.toml`，Windows 是 `%AppData%\sshore\presets.toml`（`config.DefaultPresetsPath()`）。
- **内容**：`[[presets]]` 条目（`name` / `scope`（缺省 local）/ `host`（仅 remote）/ `path`）。**预设组 = 这个文件的全部内容**。
- **首启播种**：只有文件**不存在**时才生成一次，内容是**带说明注释的模板** + 默认项（本地：主目录、桌面、下载、文档、非 Windows 的根目录；远程：主目录 `~`、根目录 `/`）。
- **之后永不改写**：应用唯一的写入口就是这次"首次生成"，所以用户加的注释、顺序、格式会永久保留。这正是**不放进主配置**的关键原因——主配置每次保存书签/最近都会整份全量重编码，把用户的注释反复抹掉。
- **判据是"文件是否存在"**，不是某个标记字段：删掉条目 → 下拉里就没有了；保留文件但删光条目 → 空预设组（**不会重建**，空组不渲染）。要恢复默认就把文件删掉（会被视为"从未播种"而重新生成），这条必须写进 README —— **别让用户以为"删文件能清空预设"**。
- **降级安全**：旧版本二进制不认识这个文件，不会碰它。（实测反例：把 `[[presets]]` 放进主配置，旧版解码时未知键被静默忽略，**一旦保存就把整段抹掉且无备份**；sidecar 没有这个问题。）
- **坏文件不覆盖**：解析失败只记录错误并在 UI 提示（`a.presetsErr`，与 H2 的 `cfgLoadErr` 同一模式），预设组退化为空，**绝不覆盖用户文件**。
- **生效时机**：`ListPresets` 每次调用都重新读文件，但前端只在进入 SFTP 页时加载一次 → **改完配置重启应用生效**（README 写明）。
- **磁盘组不是预设、不进文件**：`Drives(GetLogicalDrives())` 永远实时枚举 —— 这是"能切盘"的前提（插 U 盘重开面板即可见）。

## Global Constraints

- **预设路径必须"可直接交给列表接口"**：Windows 盘符一律带尾反斜杠（`D:\`）。裸写 `D:` 会被 `os.ReadDir` 当成"D 盘的当前目录"而不是盘根。
- **三组预设都不得为 nil**：nil 切片经 JSON 变成 `null`，前端就得处处判空。Go 侧一律用 `[]Preset{}` 起手；前端仍用 `|| []` 兜底。
- **预设的唯一来源是 `presets.toml`**（sidecar）：渲染路径不得混入任何硬编码条目（`Local` / `Remote` 只作为**播种源** `Defaults()`）；展示顺序 = 文件顺序；完全相同的 `(name, path, host)` 去重保留先出现的，同一路径的不同名字都保留；`name` 留空用 `path` 显示。
- **只在"文件不存在"时写一次**：唯一写入口是 `startup` 的首次生成；此后任何代码路径都**不得**改写 `presets.toml`（用户注释/顺序必须永久保留）。删条目 ≠ 删文件：删掉文件会被视为"从未播种"而重新生成默认模板（README 必须写明）。
- **坏文件只降级、不覆盖**：解析失败 → 记 `presetsErr` + 预设组为空，**绝不**写回或覆盖用户文件（与 `loadOrBackupConfig` 的"先备份再降级"同一立场）。
- **主配置 `AppConfig` 不新增预设字段**（不加 `presets` / `presets_seeded`）：这是降级安全的根本 —— 旧版二进制根本不会碰 `presets.toml`。
- **磁盘组不进配置、不进文件**：`Drives(logicalDriveMask())` 每次调用实时枚举，无法（也不应）静态化。
- **本地条目必须是绝对路径**；为容忍手写习惯，`~` / `~/xxx`（**仅 local**）在渲染前展开为主目录；远端的 `~` 是 sentinel，绝不展开（`TestExpandLocalTilde` 锁住这条）。
- **远程条目的 host 过滤在前端**（与 `bookmarksForPane` 同规则）：`host` 留空 = 所有主机，非空 = 只在该主机显示。
- **远程 home sentinel 是 `~`**：Go 侧常量 `preset.RemoteHomeToken`、JS 侧常量 `REMOTE_HOME`，改一处必须改两处。Go 侧由 `TestRemote` 断言字面值；**JS 侧无法单测**（`REMOTE_HOME` 定义在 `SftpView.vue` 的 `<script setup>` 内，仓库没有组件测试），改由 Task 8 清单第 7 条真机断言（未连接选「主目录」→ 连接后落 `SftpHome`）。
- **远程路径语义保持 POSIX**：`localpath` 只用于**本地面板**；远程继续走 `posixJoin` / `posixParentOf`。
- **不新增第三方依赖**；`go mod tidy` 后 `golang.org/x/sys` 版本必须仍是 `v0.46.0`（只从 indirect 变 direct）。
- 新结构体**带 json tag**；绑定类型**带包名限定**；绑定变更后执行 `wails generate module -tags webkit2_41`（决策 26）。
- **Go 改动提交前跑 `gofmt -w`**（改动的文件即可）：`make ci` 只查 `go vet`，不查格式，但仓库既有代码是 gofmt 干净的，别让格式漂移进来（`make fmt` 是仓库既有入口）。
- 提交信息用简体中文；每个 Task 结束即提交，不留跨任务的半成品。

## File Structure（先锁分解，再写任务）

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/preset/preset.go` | 创建 | 预设数据结构（含 `Host`）+ 渲染路径 `User(scope, entries)` / `All(user)` + **播种源** `Defaults()`（= `Local` + `Remote`，不含盘符）+ 盘符 `Drives(mask)`（纯逻辑，`exists` 注入，可跨平台单测；**不** import `internal/config`） |
| `internal/preset/platform_windows.go` | 创建 | Windows：`KnownFolderPath` 取真实桌面/下载/文档、`GetLogicalDrives` 取盘符、`根目录` 交给磁盘组 |
| `internal/preset/platform_other.go` | 创建 | 非 Windows：无已知文件夹、无盘符、`根目录 = /` |
| `internal/preset/preset_test.go` | 创建 | 上述纯逻辑的单测（在 Linux 上全绿，含配置条目过滤/去重/排序） |
| `internal/config/store.go` | 修改 | 预设 sidecar：`DefaultPresetsPath()` / `Preset` / `NormalizeScope()` / `LoadPresets()`（只读；坏文件只报错不改写）/ `SavePresets()`（复用原子写 + 0600）/ `PresetsTemplate()`（带注释的首次生成模板，`%q` 转义 Windows 反斜杠）。**`AppConfig` 不新增任何字段** |
| `internal/config/store_test.go` | 修改 | 模板往返（含 Windows 反斜杠路径）、缺文件 → `os.ErrNotExist`、scope 归一、坏 TOML 只报错且不改动文件 |
| `app.go` | 修改 | `startup` 首次生成 `presets.toml`（文件不存在时）+ `presetsPath`/`presetsErr`（Init 里补发事件，同 H2）；新增 `type Presets` 与 `func (a *App) ListPresets() Presets`（每次读文件 + 实时枚举盘符） |
| `app_test.go` | 修改 | `ListPresets` 三组非 nil、远程 sentinel、Windows 至少一个盘 |
| `frontend/wailsjs/go/main/App.{js,d.ts}`、`models.ts` | 重生成 | 新增绑定的 JS/TS 声明 |
| `frontend/src/utils/localpath.js` | 创建 | 跨平台本地路径助手（纯字符串；盘根不外溢、分隔符沿用路径自身风格） |
| `frontend/src/utils/localpath.test.js` | 创建 | POSIX 回归 + Windows 盘符/UNC/裸盘符用例 |
| `frontend/src/stores/locations.js` | 修改 | `load()` 并行拉 `ListPresets`，新增 `presetsForPane(pane, host)`（远程按 host 过滤）/ `disksForPane(pane)` |
| `frontend/src/stores/locations.test.js` | 修改 | mock 加 `ListPresets`；断言三组过滤与 null 兜底 |
| `frontend/src/components/FilePane.vue` | 修改 | 位置下拉新增 `预设` / `磁盘` 两个 optgroup（空组不渲染） |
| `frontend/src/views/SftpView.vue` | 修改 | 传入预设/磁盘；远程 `~` sentinel 分支；本地面板改用 `localpath` 助手（`openLocal` / `openHit` / `removeSelected` / `renameItem` / `mkdirIn` / `handleSystemDrop` / `runBatch` 的 systemPaths 分支） |
配置示例留指针 + 新增 `presets.toml` 说明小节
| `docs/superpowers/specs/2026-09-16-…-design.md` | 修改 | 决策表追加 34/35，§9 追加两条 |

---

### Task 1: 预设数据模型 —— `internal/preset` + 预设文件 `presets.toml`（sidecar）

**Files:**
- Create: `internal/preset/preset.go`
- Create: `internal/preset/platform_windows.go`
- Create: `internal/preset/platform_other.go`
- Test: `internal/preset/preset_test.go`
- Modify: `internal/config/store.go`（`type Preset` + `AppConfig.Presets` + `normalize()` 缺省 scope）
- Test: `internal/config/store_test.go`

**Interfaces:**
- Produces:
  - `const preset.RemoteHomeToken = "~"`
  - `type Preset struct { Name string \`json:"name"\`; Path string \`json:"path"\`; Host string \`json:"host,omitempty"\` }`
  - `type Entry struct { Name, Scope, Host, Path string }`（配置文件条目，app 层转换；`preset` **不** import `internal/config`）
  - `func User(scope string, entries []Entry) []Preset`（配置条目过滤/去重）
  - `func Local(home string, exists func(string) bool) []Preset`（内置）
  - `func Drives(mask uint32) []Preset`
  - `func Remote() []Preset`（内置）
  - `func All(user []Entry) (local, disks, remote []Preset)`
  - `type config.Preset struct { Name, Scope, Host, Path string }` + `AppConfig.Presets []Preset`
- 供 Task 2（`ListPresets`）使用。

- [ ] **Step 1: 写失败测试**

```go
// internal/preset/preset_test.go
package preset

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func names(ps []Preset) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name+"="+p.Path)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("长度不符：\n got %v\nwant %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项不符：\n got %v\nwant %v", i, got, want)
		}
	}
}

// 测试替身：Windows 上 KnownFolderPath 会返回真实路径，必须屏蔽掉，否则用例不可复现。
func stubKnownDirs(t *testing.T, m map[string]string) {
	t.Helper()
	old := knownDirsFn
	knownDirsFn = func() map[string]string { return m }
	t.Cleanup(func() { knownDirsFn = old })
}

func TestLocalSkipsMissingDirs(t *testing.T) {
	stubKnownDirs(t, nil)
	home := filepath.Join("home", "u")
	desktop := filepath.Join(home, "Desktop")
	docs := filepath.Join(home, "Documents")
	exists := map[string]bool{home: true, desktop: true, docs: true} // Downloads 不存在
	got := Local(home, func(p string) bool { return exists[p] })

	want := []string{"主目录=" + home, "桌面=" + desktop, "文档=" + docs}
	if runtime.GOOS != "windows" {
		want = append(want, "根目录=/")
	}
	eq(t, names(got), want)
}

func TestLocalPrefersKnownDirs(t *testing.T) {
	// 桌面被 OneDrive 重定向时，必须用 KnownFolderPath 给的真实路径，
	// 而不是拼 home\Desktop 给出一个不存在的预设。
	real := filepath.Join("Z:", "OneDrive", "桌面")
	stubKnownDirs(t, map[string]string{"Desktop": real})
	home := filepath.Join("home", "u")
	got := Local(home, func(p string) bool { return p == home || p == real })

	want := []string{"主目录=" + home, "桌面=" + real}
	if runtime.GOOS != "windows" {
		want = append(want, "根目录=/") // 非 Windows 由 rootPresets() 追加
	}
	eq(t, names(got), want)
}

func TestDrives(t *testing.T) {
	// bit0=A: bit2=C: bit3=D:
	eq(t, names(Drives(1|4|8)), []string{`A:=A:\`, `C:=C:\`, `D:=D:\`})
	if got := Drives(0); got == nil || len(got) != 0 {
		t.Fatalf("空磁盘组必须是非 nil 空切片（nil 经 JSON 变 null）：%#v", got)
	}
	if got := Drives(1 << 25); len(got) != 1 || got[0].Path != `Z:\` {
		t.Fatalf("bit25 必须是 Z:\\，实得 %v", names(got))
	}
}

func TestUserFilterDedupeAndOrder(t *testing.T) {
	user := []Entry{
		// 完全重复（name+path+host 全同）→ 只保留先出现的
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		// name 留空 → 用 path 显示
		{Name: "", Scope: "local", Path: "/work/noname"},
		// path 为空 → 忽略
		{Name: "坏条目", Scope: "local", Path: ""},
		// scope 非法 → 忽略
		{Name: "错 scope", Scope: "somewhere", Path: "/x"},
		// 同一路径、不同名字 → 都保留（用户显式意图）
		{Name: "重复", Scope: "local", Path: "/work/proj"},
		{Name: "远端日志", Scope: "remote", Host: "prod", Path: "/var/log"},
	}
	eq(t, names(User("local", user)), []string{
		"项目=/work/proj",
		"/work/noname=/work/noname",
		"重复=/work/proj",
	})
	got := User("remote", user)
	if len(got) != 1 || got[0].Path != "/var/log" || got[0].Host != "prod" {
		t.Fatalf("远端自定义预设必须带 host 透传：%+v", got)
	}
}

func TestExpandLocalTilde(t *testing.T) {
	home := filepath.Join("home", "u")
	got := expandLocalTilde([]Entry{
		{Name: "a", Scope: "local", Path: "~/work"},
		{Name: "b", Scope: "local", Path: "~"},
		{Name: "c", Scope: "remote", Path: "~"}, // 远端 sentinel 不得被展开
		{Name: "d", Scope: "local", Path: "/abs"},
	}, home)
	want := []string{filepath.Join(home, "work"), home, "~", "/abs"}
	for i, w := range want {
		if got[i].Path != w {
			t.Fatalf("第 %d 条展开错：got %q want %q", i, got[i].Path, w)
		}
	}
	if kept := expandLocalTilde([]Entry{{Name: "a", Scope: "local", Path: "~/x"}}, ""); len(kept) != 1 || kept[0].Path != "~/x" {
		t.Fatalf("home 为空时必须原样返回：%+v", kept)
	}
}

func TestAllUsesConfigEntriesOnly(t *testing.T) {
	stubKnownDirs(t, nil)
	local, _, remote := All([]Entry{
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		{Name: "生产日志", Scope: "remote", Host: "prod", Path: "/var/log"},
	})
	// 渲染路径不得混入任何硬编码条目：预设组 = 配置文件条目
	eq(t, names(local), []string{"项目=/work/proj"})
	eq(t, names(remote), []string{"生产日志=/var/log"})
}

func TestDefaultsSeedsExistentDirsOnly(t *testing.T) {
	stubKnownDirs(t, nil) // 平台实现置空：桌面/下载/文档 由 home + 英文名拼出并做存在性检查
	got := Defaults()
	if len(got) == 0 {
		t.Fatal("默认预设不能为空")
	}
	scopes := map[string]int{}
	for _, e := range got {
		if e.Scope != "local" && e.Scope != "remote" {
			t.Fatalf("scope 只能是 local/remote：%+v", e)
		}
		if e.Path == "" {
			t.Fatalf("默认预设不得有空 path：%+v", e)
		}
		scopes[e.Scope]++
	}
	if scopes["local"] == 0 || scopes["remote"] == 0 {
		t.Fatalf("默认值必须同时覆盖两个面板：%+v", got)
	}
	// 本地面板的默认项必须真实存在：播种进配置的预设不能一点就报错
	for _, e := range got {
		if e.Scope != "local" {
			continue
		}
		if fi, err := os.Stat(e.Path); err != nil || !fi.IsDir() {
			t.Fatalf("本地默认预设必须是存在的目录：%+v", e)
		}
	}
	found := false
	for _, e := range got {
		if e.Scope == "local" && e.Name == "主目录" {
			found = true
		}
	}
	if !found {
		t.Fatalf("默认值必须包含本地面板的 主目录：%+v", got)
	}
}

func TestRemote(t *testing.T) {
	eq(t, names(Remote()), []string{"主目录=" + RemoteHomeToken, "根目录=/"})
	if RemoteHomeToken != "~" {
		t.Fatalf("sentinel 必须字面量是 ~（前端 REMOTE_HOME 与它配对）：%q", RemoteHomeToken)
	}
}

func TestAllNonNil(t *testing.T) {
	local, disks, remote := All(nil)
	if local == nil || disks == nil || remote == nil {
		t.Fatalf("三组都不得为 nil（配置为空 = 预设组为空，但不能是 null）：%v %v %v", local, disks, remote)
	}
	if len(local) != 0 || len(remote) != 0 {
		t.Fatalf("配置为空时预设组就该是空的：%v %v", names(local), names(remote))
	}
	if runtime.GOOS == "windows" {
		if len(disks) == 0 {
			t.Fatal("Windows 上至少要有一个逻辑盘（C:）")
		}
	} else if len(disks) != 0 {
		t.Fatalf("非 Windows 不应有磁盘组：%v", names(disks))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/preset/ -count=1`
Expected: FAIL —— `no Go files in …/internal/preset`

- [ ] **Step 3: 写实现**

```go
// internal/preset/preset.go
// Package preset 提供「📍 位置」下拉里的三组内容：
//   - 预设组的渲染（User / All）：内容完全来自配置文件，首次启动由 Defaults() 播种；
//   - 默认播种源（Defaults = Local + Remote；**不含盘符**）；
//   - Windows 盘符（Drives，每次实时枚举，不进配置）。
// 平台差异收在 platform_windows.go / platform_other.go；不 import internal/config（叶子包只依赖标准库）。
//
// 计算逻辑与"目录是否存在"的判定分离（exists 注入），平台差异收在
// platform_windows.go / platform_other.go 里，因此三组预设都能在任何平台上单测。
package preset

import (
	"os"
	"path/filepath"
	"strings"
)

// RemoteHomeToken 是远程「主目录」预设的占位路径：远端 home 必须先连上主机、
// 执行 pwd 才能得到，所以下拉里放一个 sentinel，由前端在选中时解析。
// 前端对应常量：SftpView.vue 的 REMOTE_HOME。
const RemoteHomeToken = "~"

// Preset 是位置下拉里的一项。
// Path 必须是"可直接交给列表接口"的路径：Windows 盘符一律带尾反斜杠，
// 否则 "D:" 会被解析为"D 盘的当前目录"而不是盘根。
// Host 只有远程预设会填：留空 = 所有主机（前端按当前主机过滤，规则同书签）。
type Preset struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}

// Entry 是配置文件里的一条预设（app 层从 config.Preset 转换而来）。
// 配置文件是预设组的**唯一数据源**：首次启动由 Defaults() 播种，之后完全由用户维护。
// preset 包刻意不 import internal/config：保持只依赖标准库、便于单测，
// 也避免"数据模型包"反过来依赖"整个应用配置"。
type Entry struct {
	Name  string
	Scope string // local | remote
	Host  string // 仅 scope=remote 有意义
	Path  string
}

// dirSpecs：显示名 → 已知文件夹 key（Windows 的 KnownFolderPath）／英文目录名（其他平台）。
var dirSpecs = []struct{ Name, Key string }{
	{"桌面", "Desktop"},
	{"下载", "Downloads"},
	{"文档", "Documents"},
}

// knownDirsFn 是平台实现的测试接缝：Windows 上 knownDirs() 会返回真实路径，
// 单测必须能把它换成固定值。
var knownDirsFn = knownDirs

// User 把配置文件条目按面板过滤成**该面板的全部预设**（渲染路径上唯一的数据源）：
//   - scope 不匹配或 path 为空的条目**静默忽略**——手改配置写错一条，
//     不能让整个位置下拉消失，也不能让应用报错；
//   - name 留空时用 path 兜底显示；
//   - **完全相同**的 (name, path, host) 只保留先出现的那条（重复粘贴通常是无意的）；
//     但同一路径配不同名字都保留——v3 下配置是预设的唯一来源，不能替用户丢掉一个。
func User(scope string, entries []Entry) []Preset {
	out := []Preset{}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Scope != scope || e.Path == "" {
			continue
		}
		name := e.Name
		if name == "" {
			name = e.Path
		}
		k := name + "\x00" + e.Path + "\x00" + e.Host
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Preset{Name: name, Path: e.Path, Host: e.Host})
	}
	return out
}

// Defaults 返回"首次生成 presets.toml 时写进模板"的默认预设（见 config.PresetsTemplate）：
// 本地 主目录/桌面/下载/文档（+ 非 Windows 的 根目录）、远程 主目录(~)/根目录。
// 只播种**实际存在**的本地目录，且**不含盘符**——磁盘组每次实时枚举，不进配置。
func Defaults() []Entry {
	home, _ := os.UserHomeDir()
	out := []Entry{}
	for _, p := range Local(home, dirExists) {
		out = append(out, Entry{Name: p.Name, Scope: "local", Path: p.Path})
	}
	for _, p := range Remote() {
		out = append(out, Entry{Name: p.Name, Scope: "remote", Path: p.Path})
	}
	return out
}

// Local 返回本地面板的默认预设项（只被 Defaults 用作播种源；渲染路径不调用它）。
// exists 为目录存在性判定（注入以便单测）；不存在的目录被静默跳过——
// 宁可不显示，也不能给一个点进去就报错的预设。
func Local(home string, exists func(string) bool) []Preset {
	out := []Preset{}
	if home != "" && exists(home) {
		out = append(out, Preset{Name: "主目录", Path: home})
	}
	known := knownDirsFn()
	for _, d := range dirSpecs {
		p := known[d.Key]
		if p == "" && home != "" {
			p = filepath.Join(home, d.Key)
		}
		if p != "" && exists(p) {
			out = append(out, Preset{Name: d.Name, Path: p})
		}
	}
	return append(out, rootPresets()...)
}

// Drives 把 GetLogicalDrives 的位掩码翻译成盘符预设（bit0=A: … bit25=Z:）。
func Drives(mask uint32) []Preset {
	out := []Preset{}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		out = append(out, Preset{Name: letter + ":", Path: letter + `:\`})
	}
	return out
}

// Remote 返回远程面板的固定预设：主目录（sentinel，选中后由前端解析）与根目录。
func Remote() []Preset {
	return []Preset{
		{Name: "主目录", Path: RemoteHomeToken},
		{Name: "根目录", Path: "/"},
	}
}

// All 组装位置下拉需要的三组预设。预设组（local/remote）**完全来自配置文件条目**；
// 磁盘组每次实时枚举（盘符不进配置：插拔 U 盘后重开面板就该看到新盘）。
// 三组都保证非 nil：nil 切片经 JSON 会变成 null，前端就得处处判空。
func All(user []Entry) (local, disks, remote []Preset) {
	home, _ := os.UserHomeDir()
	// 本地面板的 "~" / "~/xxx" 展开为主目录；远端条目的 "~" 是 sentinel，必须原样保留。
	return User("local", expandLocalTilde(user, home)), Drives(logicalDriveMask()), User("remote", user)
}

// expandLocalTilde 把 local 条目里 "~" / "~/xxx" 的 path 展开成 home：
// 手写配置时 "~/work" 是最自然的写法，不展开就等于"点了就报错"。
// 远端条目的 "~" 是"连上主机后才解析"的 sentinel，**绝不能**在这里展开。
func expandLocalTilde(entries []Entry, home string) []Entry {
	if home == "" {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Scope == "local" && (e.Path == "~" || strings.HasPrefix(e.Path, "~/")) {
			e.Path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(e.Path, "~"), "/"))
		}
		out = append(out, e)
	}
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
```

```go
// internal/preset/platform_windows.go
//go:build windows

package preset

import "golang.org/x/sys/windows"

// knownDirs 用 KnownFolderPath 取真实路径：这几个目录可能被 OneDrive 重定向
// 或用户手工移动，直接拼 %USERPROFILE%\Desktop 会给出不存在的预设。
func knownDirs() map[string]string {
	out := map[string]string{}
	for key, id := range map[string]*windows.KNOWNFOLDERID{
		"Desktop":   windows.FOLDERID_Desktop,
		"Downloads": windows.FOLDERID_Downloads,
		"Documents": windows.FOLDERID_Documents,
	} {
		if p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil && p != "" {
			out[key] = p
		}
	}
	return out
}

// rootPresets：Windows 的"根"按盘符给（见 Drives），不再重复一个 "/"。
func rootPresets() []Preset { return nil }

func logicalDriveMask() uint32 {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return 0
	}
	return mask
}
```

```go
// internal/preset/platform_other.go
//go:build !windows

package preset

// knownDirs：非 Windows 没有"已知文件夹"API，交给 Local 用 home + 英文目录名兜底。
func knownDirs() map[string]string { return nil }

// rootPresets：非 Windows 的根就是 "/"。
func rootPresets() []Preset { return []Preset{{Name: "根目录", Path: "/"}} }

// logicalDriveMask：盘符是 Windows 概念，其他平台恒为 0（Drives 因此返回空组）。
func logicalDriveMask() uint32 { return 0 }
```

- [ ] **Step 4: 跑测试确认通过 + 本地 Windows 交叉检查**

Run: `go test ./internal/preset/ -count=1 -v`
Expected: PASS（9 个用例全绿：LocalSkipsMissingDirs / LocalPrefersKnownDirs / Drives / UserFilterDedupeAndOrder / ExpandLocalTilde / AllUsesConfigEntriesOnly / DefaultsSeedsExistentDirsOnly / Remote / AllNonNil）

平台文件（`platform_windows.go`）在 Linux 上**不参与编译**，而 CI 的 `go-windows` job 要 push 之后才跑。所以本地必须补一次交叉检查：

Run: `GOOS=windows GOARCH=amd64 go vet ./internal/preset/ && GOOS=windows GOARCH=386 go build -o /dev/null ./internal/preset/`
Expected: 均无输出（成功）

- [ ] **Step 5: 预设文件（sidecar）：`internal/config` 的读写 + 带注释模板**

**为什么单独一个文件**（第三轮评审后定案）：预设是**用户手改**的，而主配置 `sshore.toml` 会被应用频繁**整份重编码**（任何书签/最近的保存都走 `SaveConfig`），放一起会反复抹掉用户的注释；更致命的是**降级** —— 旧版二进制用旧结构体解码新配置时未知键被静默忽略，**一旦保存就把 `presets` 整段抹掉且无备份**。sidecar 文件旧版根本不认识，天然降级安全，而且应用只在"文件不存在"时写一次，之后**永不改写**。

先写失败测试：

```go
// internal/config/store_test.go 追加（需要 import "errors"、"strings"、"path/filepath"）
func TestPresetsTemplateRoundTrip(t *testing.T) {
	seed := []Preset{
		{Name: "主目录", Scope: "local", Path: "/home/u"},
		{Name: "桌面", Scope: "local", Path: `C:\Users\u\Desktop`}, // Windows 反斜杠必须被正确转义
		{Name: "主目录", Scope: "remote", Path: "~"},
		{Name: "生产日志", Scope: "remote", Host: "prod", Path: "/var/log"},
	}
	data := PresetsTemplate(seed)
	// 模板必须带说明注释（这是用户唯一的使用入口）
	if !strings.Contains(string(data), "# ") || !strings.Contains(string(data), "[[presets]]") {
		t.Fatalf("模板应带注释与 [[presets]] 段：\n%s", data)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "presets.toml")
	if err := SavePresets(p, data); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPresets(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(seed) {
		t.Fatalf("往返丢条目：got %d want %d", len(got), len(seed))
	}
	for i := range seed {
		if got[i] != seed[i] {
			t.Fatalf("第 %d 条往返不一致：got %+v want %+v", i, got[i], seed[i])
		}
	}
}

func TestLoadPresetsNormalizesScopeAndKeepsFileOnError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "presets.toml")

	// 缺文件 → os.ErrNotExist（调用方据此决定是否播种）
	if _, err := LoadPresets(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺文件必须返回 os.ErrNotExist，实得 %v", err)
	}

	// 手写的大小写/空白笔误必须归一（预设层是精确比较）
	raw := "[[presets]]\nname = \"a\"\nscope = \"Remote\"\npath = \"/x\"\n\n[[presets]]\nname = \"b\"\nscope = \" remote \"\npath = \"/y\"\n\n[[presets]]\nname = \"c\"\npath = \"/z\"\n"
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPresets(p)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range []string{"remote", "remote", "local"} {
		if got[i].Scope != w {
			t.Fatalf("第 %d 条 scope 归一失败：got %q want %q", i, got[i].Scope, w)
		}
	}

	// 解析失败：返回错误，且 LoadPresets **绝不修改**用户文件
	bad := "[presets]\nbroken = \n"
	if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPresets(p); err == nil {
		t.Fatal("坏 TOML 必须返回错误")
	}
	after, _ := os.ReadFile(p)
	if string(after) != bad {
		t.Fatalf("LoadPresets 不得修改用户文件：\n%s", after)
	}
}
```

Run: `go test ./internal/config/ -run 'TestPresetsTemplate|TestLoadPresets' -count=1`
Expected: FAIL —— `PresetsTemplate undefined`

实现（`store.go`；`SavePresets` 复用既有原子写原语与 `renameWithRetry`；`AppConfig` **不动**）：

```go
// DefaultPresetsPath 返回预设文件路径：与主配置同目录（unix 是 ~/.config/sshore/presets.toml，
// Windows 是 %AppData%/sshore/presets.toml）。
//
// 单独一个文件是有意为之：预设是**用户手改**的，而主配置会被应用频繁整份重编码
// （任何书签/最近保存都走 SaveConfig），放一起会反复抹掉用户写的注释；且旧版本
// 二进制不认识这个文件，**降级不会丢预设**（放主配置里会被旧版保存时整段抹掉）。
func DefaultPresetsPath() (string, error) {
	cfg, err := DefaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfg), "presets.toml"), nil
}

// Preset 是预设文件里的一条预设；scope 缺省 local。
type Preset struct {
	Name  string `toml:"name" json:"name"`
	Scope string `toml:"scope,omitempty" json:"scope,omitempty"` // local | remote
	Host  string `toml:"host,omitempty" json:"host,omitempty"`   // 仅 scope=remote：留空 = 所有主机
	Path  string `toml:"path" json:"path"`
}

// NormalizeScope 归一 scope：大小写/首尾空白是手写配置的常见笔误，而预设层是精确比较，
// 不归一就等于"配了却什么都没有"。（store.go 需新增 import "strings"）
func NormalizeScope(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "local"
	}
	return s
}

// LoadPresets 读取预设文件。文件不存在返回 (nil, os.ErrNotExist)，调用方据此决定是否播种；
// 解析失败返回错误，并且**绝不改写用户文件**（调用方只记录 + 降级，不覆盖）。
func LoadPresets(path string) ([]Preset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Presets []Preset `toml:"presets"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, fmt.Errorf("解析预设文件 %s: %w", path, err)
	}
	for i := range doc.Presets {
		doc.Presets[i].Scope = NormalizeScope(doc.Presets[i].Scope)
	}
	return doc.Presets, nil
}

// SavePresets 以 0600 原子写入预设文件（先写 <path>.tmp-<pid>-<seq> 再 rename，与 SaveConfig
// 同一套原语）。**只在"首次生成"时被调用**，之后应用不再改写这个文件。
func SavePresets(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d-%d", path, os.Getpid(), atomic.AddInt64(&tmpSeq, 1))
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := renameWithRetry(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

const presetsHeader = "# SSHore 位置预设（「📍位置」下拉里的「预设」组 = 本文件的全部内容）\n" +
	"#\n" +
	"# 这个文件只在你首次启动时由应用生成一次，之后应用**不会**再改写它 ——\n" +
	"# 你可以放心加注释、调顺序、改名、删条目。\n" +
	"#   - 删掉哪条，下拉里就没有哪条；保留文件但删光条目 = 预设组消失（不会重建）\n" +
	"#   - 本地面板的 path 用绝对路径（~/xxx 会展开为主目录）；远端的 \"~\" 表示远端 home\n" +
	"#   - scope 缺省 local；remote 条目可用 host 限定主机（留空 = 所有主机）\n" +
	"#   - Windows 路径推荐用单引号字面量（双引号里反斜杠是转义符，写错会让文件解析失败）\n" +
	"#   - 改完重启应用生效\n\n"

// PresetsTemplate 渲染"首次生成"用的文件内容：说明注释 + 默认条目。
// 字段一律用 %q 写出，Windows 反斜杠会被正确转义，保证生成的文件一定能被解析。
func PresetsTemplate(seed []Preset) []byte {
	var b strings.Builder
	b.WriteString(presetsHeader)
	for _, p := range seed {
		b.WriteString("[[presets]]\n")
		fmt.Fprintf(&b, "name = %q\n", p.Name)
		fmt.Fprintf(&b, "scope = %q\n", NormalizeScope(p.Scope))
		if p.Host != "" {
			fmt.Fprintf(&b, "host = %q\n", p.Host)
		}
		fmt.Fprintf(&b, "path = %q\n\n", p.Path)
	}
	return []byte(b.String())
}
```

Run: `go test ./internal/config/ -count=1`
Expected: PASS（含既有用例；`AppConfig` 结构与 `SaveConfig` 路径**完全没动**）

**注意**：`AppConfig` 里**不再**新增 `Presets` / `PresetsSeeded` 字段 —— 主配置保持原样，这是本方案降级安全的根本。

- [ ] **Step 6: 提升依赖并确认版本没变**

Run: `go mod tidy && git diff --stat go.mod go.sum && grep -n 'golang.org/x/sys' go.mod`
Expected: `golang.org/x/sys v0.46.0` 从 `require ( … // indirect )` 块移到直接 require 块；**版本不变**、`go.sum` 无增删。若版本被改动，停下来说明（不许顺带升级依赖）。

- [ ] **Step 7: Commit**

```bash
git add internal/preset internal/config go.mod go.sum
git commit -m "feat(preset): 预设数据模型 + presets.toml 读写（sidecar 预设文件 + Windows 盘符枚举）"
```

---

### Task 2: `app.go` —— 首次生成 `presets.toml` + `ListPresets` 绑定 + 重生成 wails 模块

**Files:**
- Modify: `app.go`（`startup` `:84-99` 首次生成预设文件；`App` 增 `presetsPath`/`presetsErr`；`Init` `:103-131` 补发错误事件；`:754-780` 附近新增 `Presets`/`ListPresets` 与两个转换助手）
- Test: `app_test.go`
- Regenerate: `frontend/wailsjs/go/main/App.js`、`App.d.ts`、`frontend/wailsjs/go/models.ts`

**Interfaces:**
- Consumes: Task 1 的 `preset.Preset` / `preset.Entry` / `preset.All(user)` / `preset.Defaults()`；`config.DefaultPresetsPath` / `config.LoadPresets` / `config.SavePresets` / `config.PresetsTemplate` / `config.NormalizeScope`
- Produces:
  - `type Presets struct{ Local []preset.Preset; LocalDisks []preset.Preset; Remote []preset.Preset }`（json tag `local` / `localDisks` / `remote`）
  - `func (a *App) ListPresets() Presets`
- 供 Task 4（store）使用。

- [ ] **Step 1: 写失败测试**

```go
// app_test.go 追加（import 只需加 "sshore/internal/preset"：runtime/config/os/strings/filepath 都已在）
func TestListPresetsReadsPresetsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.toml")
	if err := config.SavePresets(path, config.PresetsTemplate([]config.Preset{
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		{Name: "生产日志", Scope: "remote", Host: "prod", Path: "/var/log"},
	})); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.presetsPath = path
	p := a.ListPresets()
	// 预设组必须完全等于文件内容：不得混入任何硬编码条目
	if len(p.Local) != 1 || p.Local[0].Name != "项目" {
		t.Fatalf("本地面板预设必须完全等于文件内容：%+v", p.Local)
	}
	if len(p.Remote) != 1 || p.Remote[0].Path != "/var/log" || p.Remote[0].Host != "prod" {
		t.Fatalf("远端条目必须透传 host：%+v", p.Remote)
	}
	if runtime.GOOS == "windows" && len(p.LocalDisks) == 0 {
		t.Fatal("Windows 上磁盘组不得为空")
	}
	if runtime.GOOS != "windows" && len(p.LocalDisks) != 0 {
		t.Fatalf("非 Windows 不应有磁盘组：%+v", p.LocalDisks)
	}
}

func TestListPresetsDegradesOnBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.toml")
	bad := "[presets]\nbroken = \n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.presetsPath = path
	p := a.ListPresets() // 坏文件不能让整个面板失败
	if p.Local == nil || p.LocalDisks == nil || p.Remote == nil {
		t.Fatalf("坏文件时三组也必须非 nil：%+v", p)
	}
	if len(p.Local) != 0 || len(p.Remote) != 0 {
		t.Fatalf("坏文件时预设组应退化为空：%+v %+v", p.Local, p.Remote)
	}
	if after, _ := os.ReadFile(path); string(after) != bad {
		t.Fatal("坏文件绝不能被改写或覆盖")
	}
	// presetsPath 为空（路径解析失败）时也不能炸、也不能是 nil
	if got := NewApp().ListPresets(); got.Local == nil || got.LocalDisks == nil || got.Remote == nil {
		t.Fatalf("presetsPath 为空时三组应是非 nil 空切片：%+v", got)
	}
}

func TestStartupSeedsPresetsFileOnce(t *testing.T) {
	pp, err := config.DefaultPresetsPath()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(pp) // 模拟"从 v0.5.0 升级上来"：还没有预设文件
	a := NewApp()
	a.startup(context.Background())
	if a.presetsErr != nil {
		t.Fatalf("首次生成不应报错：%v", a.presetsErr)
	}
	data, err := os.ReadFile(pp)
	if err != nil {
		t.Fatalf("首次启动必须生成 presets.toml：%v", err)
	}
	if !strings.Contains(string(data), "# ") || !strings.Contains(string(data), "[[presets]]") {
		t.Fatalf("生成的模板必须带注释与条目：\n%s", data)
	}
	if ps, err := config.LoadPresets(pp); err != nil || len(ps) == 0 {
		t.Fatalf("生成的模板必须能被自己解析出条目：%v %+v", err, ps)
	}

	// 用户改动之后重启：绝不改写（注释/顺序/新增内容必须永久保留）
	custom := string(data) + "\n# 我加的注释\n"
	if err := os.WriteFile(pp, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	a2 := NewApp()
	a2.startup(context.Background())
	if after, _ := os.ReadFile(pp); string(after) != custom {
		t.Fatal("已存在的预设文件绝不能被启动流程改写")
	}

	// 保留文件但删光条目：重启不得复活默认项
	empty := "# 我只要磁盘组\n"
	if err := os.WriteFile(pp, []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	a3 := NewApp()
	a3.startup(context.Background())
	if got := a3.ListPresets(); len(got.Local) != 0 {
		t.Fatalf("用户清空条目后不得复活默认预设：%+v", got.Local)
	}

	// 坏文件：不覆盖，并把错误如实记到 presetsErr（Init 时补发事件，用户才看得到）
	bad := "[presets]\nbroken = \n"
	if err := os.WriteFile(pp, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	a4 := NewApp()
	a4.startup(context.Background())
	if a4.presetsErr == nil {
		t.Fatal("坏文件必须在 startup 阶段被记录（否则用户看不到任何提示）")
	}
	if after, _ := os.ReadFile(pp); string(after) != bad {
		t.Fatal("坏文件不得被覆盖")
	}
}

// 评审 M8：config.Preset 与 preset.Entry 是两个结构体，字段漂移没有编译期保护，
// 用对称性（转过去再转回来逐字段相等）把它钉住。
func TestPresetConvertersAreSymmetric(t *testing.T) {
	src := []preset.Entry{{Name: "a", Scope: "remote", Host: "prod", Path: "/x"}}
	back := presetEntries(toConfigPresets(src))
	if len(back) != 1 || back[0] != src[0] {
		t.Fatalf("转换必须保字段：%+v -> %+v", src, back)
	}
	got := presetEntries([]config.Preset{{Name: "b", Path: "/y"}})
	if len(got) != 1 || got[0].Scope != "local" || got[0].Name != "b" || got[0].Path != "/y" {
		t.Fatalf("scope 缺省应为 local 且字段不得丢：%+v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./ -run 'TestListPresets|TestStartupSeedsPresets|TestPresetConverters' -count=1`
Expected: FAIL —— `a.presetsPath undefined` / `a.ListPresets undefined`

- [ ] **Step 3: 写实现**

```go
// app.go：import 块按字母序插入（app.go 已有 errors/fmt/os，无 "strings" 也不需要）
	"sshore/internal/preset"

// app.go：App 结构体（:25-40）追加两个字段
	// H2 同构：startup 阶段发现的预设文件问题（首次生成失败 / 坏文件），
	// Init 时 emit 补发，前端日志面板可见。
	presetsPath string
	presetsErr  error

// app.go：startup（:84-99）里追加（紧跟 MigrateLegacyRecents 之后）
	// 预设文件只在"不存在"时生成一次；之后永不改写（用户的注释/顺序必须永久保留）。
	// 判据是**文件存在性**，不是某个标记字段 —— "删条目"与"删文件"语义不同，见 README。
	pp, perr := config.DefaultPresetsPath()
	a.presetsPath = pp
	if perr != nil {
		a.presetsErr = perr
	} else if _, statErr := os.Stat(pp); errors.Is(statErr, os.ErrNotExist) {
		if werr := config.SavePresets(pp, config.PresetsTemplate(toConfigPresets(preset.Defaults()))); werr != nil {
			a.presetsErr = fmt.Errorf("生成默认预设文件 %s 失败: %w", pp, werr)
		}
	} else if _, lerr := config.LoadPresets(pp); lerr != nil {
		// 坏文件：只记录、只降级，绝不覆盖用户文件
		a.presetsErr = lerr
	}

// app.go：Init（:103-131）里，紧邻 cfgLoadErr 的事件补发（:122-130）之后追加
	if a.presetsErr != nil && a.emit != nil {
		a.emit(forward.Event{
			SourceType: "system",
			SourceID:   "app",
			TS:         time.Now().Format(time.RFC3339),
			Level:      "error",
			Message:    a.presetsErr.Error(),
		})
	}

// app.go：紧邻 ListLocations（原 :754-780）新增

// toConfigPresets / presetEntries 是 app 层**唯一**的类型转换点：
// internal/preset 刻意不 import internal/config（叶子包只依赖标准库）。
func toConfigPresets(es []preset.Entry) []config.Preset {
	out := make([]config.Preset, 0, len(es))
	for _, e := range es {
		out = append(out, config.Preset{Name: e.Name, Scope: e.Scope, Host: e.Host, Path: e.Path})
	}
	return out
}

func presetEntries(ps []config.Preset) []preset.Entry {
	out := make([]preset.Entry, 0, len(ps))
	for _, p := range ps {
		out = append(out, preset.Entry{
			Name:  p.Name,
			Scope: config.NormalizeScope(p.Scope), // 与 LoadPresets 同一条归一规则，双保险
			Host:  p.Host,
			Path:  p.Path,
		})
	}
	return out
}

// Presets 是「📍 位置」下拉里的固定预设：本地面板（local）、Windows 的逻辑盘
// （localDisks）、远程面板（remote）。local/remote 完全来自 presets.toml，
// localDisks 每次实时枚举；remote 条目的 Host 由前端按当前主机过滤。
type Presets struct {
	Local      []preset.Preset `json:"local"`
	LocalDisks []preset.Preset `json:"localDisks"`
	Remote     []preset.Preset `json:"remote"`
}

// ListPresets 返回位置下拉的预设：每次调用都重新读 presets.toml 并实时枚举盘符。
// 坏文件不让面板整体失败：预设组退化为空（错误已在 startup 记录、Init 时上报）。
// 注意前端只在进入 SFTP 页时拉一次 → 手改文件后需要重启应用生效。
func (a *App) ListPresets() Presets {
	var entries []preset.Entry
	if a.presetsPath != "" {
		ps, err := config.LoadPresets(a.presetsPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			ps = nil // 坏文件：只降级，不覆盖用户文件
		}
		entries = presetEntries(ps)
	}
	local, disks, remote := preset.All(entries)
	return Presets{Local: local, LocalDisks: disks, Remote: remote}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./ -run 'TestListPresets|TestStartupSeedsPresets|TestPresetConverters' -count=1 -v && go vet ./...`
Expected: PASS（4 个用例）；vet 无输出

- [ ] **Step 5: 重生成绑定并核对**

Run:
```bash
$(go env GOPATH)/bin/wails generate module -tags webkit2_41
grep -n 'ListPresets' frontend/wailsjs/go/main/App.d.ts frontend/wailsjs/go/main/App.js
grep -n 'class Presets' frontend/wailsjs/go/models.ts
```
Expected: `App.d.ts` 出现 `export function ListPresets():Promise<main.Presets>;`；`models.ts` 出现 `main.Presets`（含 `local`/`localDisks`/`remote`）。**漏了这一步的失败形态是构建期 Rollup 硬错误**（`"ListPresets" is not exported by wailsjs/go/main/App.js`，已实测复现），dev 模式可能只是警告、跑到面板加载时才炸 —— 所以 Step 5 是 Task 4/5/6 的硬前置。

- [ ] **Step 6: Commit**

```bash
git add app.go app_test.go frontend/wailsjs
git commit -m "feat(app): presets.toml 首次生成 + ListPresets 绑定（本地预设/Windows 盘符/远程预设）"
```

---

### Task 3: `utils/localpath.js` 跨平台本地路径助手

**Files:**
- Create: `frontend/src/utils/localpath.js`
- Test: `frontend/src/utils/localpath.test.js`

**Interfaces:**
- Produces:
  - `isWindowsPath(p) → boolean`
  - `normalize(p) → string`（裸盘符 `D:` → `D:\`）
  - `sepOf(p) → '\\' | '/'`（沿用路径里已有的分隔符）
  - `isRoot(p) → boolean`（`C:\` / `/` / `\\\\srv\\share`）
  - `join(base, name) → string`
  - `parentOf(p) → string`
  - `splitPath(p) → { dir, name }`
  - `joinRel(base, rel) → string`
- 供 Task 6 使用（**只用于本地面板**，远程继续用 POSIX 助手）。

- [ ] **Step 1: 写失败测试**

```js
// frontend/src/utils/localpath.test.js
import { describe, it, expect } from 'vitest'
import { isWindowsPath, normalize, sepOf, isRoot, join, parentOf, splitPath, joinRel } from './localpath'

describe('localpath · POSIX 回归（本地面板在 Linux/macOS 上的既有行为不能变）', () => {
  it('join / parentOf 维持原语义', () => {
    expect(join('/', 'home')).toBe('/home')
    expect(join('/home/u', 'x')).toBe('/home/u/x')
    expect(join('/home/u/', 'x')).toBe('/home/u/x')
    expect(parentOf('/home/u')).toBe('/home')
    expect(parentOf('/home')).toBe('/')
    expect(parentOf('/')).toBe('/')
    expect(parentOf('')).toBe('/')
  })

  it('根相对路径（深搜命中项恒用 / 分隔）拼接与切分', () => {
    expect(joinRel('/home/u', 'sub/dir')).toBe('/home/u/sub/dir')
    expect(splitPath('sub/a.txt')).toEqual({ dir: 'sub', name: 'a.txt' })
    expect(splitPath('a.txt')).toEqual({ dir: '', name: 'a.txt' })
  })
})

describe('localpath · Windows', () => {
  it('盘根不外溢：上级停在盘根，绝不返回 /', () => {
    expect(parentOf('C:\\')).toBe('C:\\')
    expect(parentOf('C:\\Users')).toBe('C:\\')
    expect(parentOf('C:\\Users\\lan')).toBe('C:\\Users')
    expect(parentOf('D:\\x\\y')).toBe('D:\\x')
    expect(parentOf('D:\\x')).toBe('D:\\')
  })

  it('裸盘符归一为盘根（os.ReadDir("D:") 读的是当前目录，不是根）', () => {
    expect(normalize('D:')).toBe('D:\\')
    expect(parentOf('D:')).toBe('D:\\')
    expect(isRoot('D:')).toBe(true)
  })

  it('分隔符沿用路径自身的风格，不擅自改写', () => {
    expect(join('C:\\', 'Users')).toBe('C:\\Users')
    expect(join('C:/', 'Users')).toBe('C:/Users')
    expect(join('C:\\Users', 'lan')).toBe('C:\\Users\\lan')
    expect(sepOf('C:\\Users')).toBe('\\')
    expect(sepOf('C:/Users')).toBe('/')
    expect(sepOf('/home/u')).toBe('/')
  })

  it('盘根识别与深搜跳转', () => {
    expect(isRoot('C:\\')).toBe(true)
    expect(isRoot('C:\\Users')).toBe(false)
    expect(joinRel('C:\\Users\\lan', 'sub/dir')).toBe('C:\\Users\\lan\\sub\\dir')
    expect(splitPath('C:\\Users\\lan\\a.txt')).toEqual({ dir: 'C:\\Users\\lan', name: 'a.txt' })
    expect(isWindowsPath('C:\\Users')).toBe(true)
    expect(isWindowsPath('C:/Users')).toBe(true)
    expect(isWindowsPath('/home/u')).toBe(false)
  })

  it('UNC 共享根不外溢', () => {
    expect(isRoot('\\\\srv\\share')).toBe(true)
    expect(parentOf('\\\\srv\\share')).toBe('\\\\srv\\share')
    expect(parentOf('\\\\srv\\share\\dir')).toBe('\\\\srv\\share')
  })
})
```

> 反斜杠说明：本代码块里写的就是**源码原文**（`'C:\\'` 是合法的 JS 源码，表示字符串 `C:\`）。这 7 个用例已用 vitest 实跑全绿，**照抄即可，不要再手工"修正"转义**（把 `'C:\\'` 改成 `'C:'` 会让"盘根"用例失去意义）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/localpath.test.js`
Expected: FAIL —— 无法解析 `./localpath`

- [ ] **Step 3: 写实现**

```js
// frontend/src/utils/localpath.js
// 本地路径助手：纯字符串处理，不访问文件系统。
// 语义：按"路径自身"的风格工作——路径里出现 '\' 或形如 C:\ / C:/ / \\server\share 时
// 按 Windows 规则，否则按 POSIX 规则；分隔符优先沿用路径里已有的那个，不擅自改写。
// 远程（SFTP）路径一律用 POSIX 语义，见 SftpView.vue 的 posixJoin / posixParentOf。

const WIN_DRIVE = /^[A-Za-z]:[\\/]/
const WIN_DRIVE_BARE = /^[A-Za-z]:$/
const WIN_DRIVE_ROOT = /^[A-Za-z]:[\\/]$/
const UNC_SHARE = /^\\\\[^\\/]+[\\/][^\\/]+$/
const TRAILING_SEP = /[\\/]+$/

export function isWindowsPath(p) {
  const s = p || ''
  return WIN_DRIVE.test(s) || s.startsWith('\\\\')
}

// 盘符裸写 "D:" 归一为 "D:\"：Windows 上 "D:" 表示"D 盘的当前目录"，
// 只有带分隔符的 "D:\" 才是盘根。
export function normalize(p) {
  const s = p || ''
  return WIN_DRIVE_BARE.test(s) ? s + '\\' : s
}

export function sepOf(p) {
  const s = normalize(p)
  if (s.includes('\\')) return '\\'
  if (s.includes('/')) return '/'
  return isWindowsPath(s) ? '\\' : '/'
}

// 盘根（C:\）、POSIX 根（/）、UNC 共享根（\\srv\share）——它们的上级是它们自己。
export function isRoot(p) {
  const s = normalize(p)
  if (!s) return false
  if (s === '/') return true
  if (WIN_DRIVE_ROOT.test(s)) return true
  return UNC_SHARE.test(s.replace(TRAILING_SEP, ''))
}

export function join(base, name) {
  const b = normalize(base)
  if (!b) return name || ''
  const sep = sepOf(b)
  const body = b.replace(TRAILING_SEP, '')
  return body === '' ? sep + name : body + sep + name
}

export function parentOf(p) {
  const s = normalize(p)
  if (!s) return '/'
  if (isRoot(s)) return s
  const trimmed = s.replace(TRAILING_SEP, '')
  const idx = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  if (idx < 0) return '.'
  const head = trimmed.slice(0, idx)
  if (WIN_DRIVE_BARE.test(head)) return head + sepOf(s)
  if (head === '') return sepOf(s)
  return head
}

// 切出 { dir, name }；dir 为空字符串表示"没有目录部分"（相对路径的首层项）。
export function splitPath(p) {
  const s = normalize(p)
  const idx = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  if (idx < 0) return { dir: '', name: s }
  const dir = s.slice(0, idx)
  const name = s.slice(idx + 1)
  if (dir === '') return { dir: sepOf(s), name }
  if (WIN_DRIVE_BARE.test(dir)) return { dir: dir + sepOf(s), name }
  return { dir, name }
}

// 把"根相对路径"（深搜命中项，恒用 '/' 分隔）拼到基目录后，按基目录的风格输出分隔符。
export function joinRel(base, rel) {
  const parts = String(rel || '').split(/[\\/]+/).filter(Boolean)
  if (!parts.length) return normalize(base)
  return join(base, parts.join(sepOf(base)))
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/localpath.test.js`
Expected: PASS（2 个 describe、7 个用例全绿）

- [ ] **Step 5: Commit**

```bash
git add frontend/src/utils/localpath.js frontend/src/utils/localpath.test.js
git commit -m "feat(frontend): 跨平台本地路径助手（盘根不外溢、UNC、裸盘符归一）"
```

---

### Task 4: `stores/locations.js` 加载预设

**Files:**
- Modify: `frontend/src/stores/locations.js`
- Modify: `frontend/src/stores/locations.test.js`

**Interfaces:**
- Consumes: Task 2 的 `ListPresets()`（`{ local, localDisks, remote }`）
- Produces:
  - state 新增 `presets: { local: [], localDisks: [], remote: [] }`
  - `presetsForPane(pane, host) → Preset[]`（远程按 host 过滤：`host` 留空 = 不筛）、`disksForPane(pane) → Preset[]`（远程面板恒为空）
- 供 Task 5/6 使用。

- [ ] **Step 1: 写失败测试**

```js
// frontend/src/stores/locations.test.js：mock 工厂里追加 ListPresets
vi.mock('../../wailsjs/go/main/App', () => ({
  ListLocations: vi.fn(async () => ({ /* …既有内容不变… */ })),
  ListPresets: vi.fn(async () => ({
    local: [{ name: '项目', path: '/work/proj' }, { name: '主目录', path: '/home/u' }, { name: '根目录', path: '/' }],
    localDisks: [],
    remote: [
      { name: '主目录', path: '~' },
      { name: '根目录', path: '/' },
      { name: '生产日志', path: '/var/log', host: 'prod' },
    ],
  })),
  AddBookmark: vi.fn(async () => {}),
  RemoveBookmark: vi.fn(async () => {}),
  AddLocalRecent: vi.fn(async () => {}),
  AddRemoteRecent: vi.fn(async () => {}),
}))

// 追加用例
it('load 同时拉取三组固定预设', async () => {
  const s = useLocationsStore()
  await s.load()
  expect(s.presetsForPane('local', '').map((p) => p.path)).toEqual(['/work/proj', '/home/u', '/'])
  expect(s.presetsForPane('remote', 'prod').map((p) => p.path)).toEqual(['~', '/', '/var/log'])
  expect(s.disksForPane('local')).toEqual([])
})

it('远程预设按当前主机过滤（host 留空 = 所有主机）', async () => {
  const s = useLocationsStore()
  await s.load()
  expect(s.presetsForPane('remote', 'db').map((p) => p.path)).toEqual(['~', '/'])
  // 还没选主机时不筛，退化为"全部显示"
  expect(s.presetsForPane('remote', '').map((p) => p.path)).toEqual(['~', '/', '/var/log'])
  expect(s.presetsForPane('remote').length).toBe(3)
})

it('磁盘组只属于本地面板', async () => {
  const s = useLocationsStore()
  await s.load()
  s.presets.localDisks = [{ name: 'C:', path: 'C:\\' }]
  expect(s.disksForPane('local').map((p) => p.name)).toEqual(['C:'])
  expect(s.disksForPane('remote')).toEqual([])
})

it('后端返回 null 时兜底为空数组', async () => {
  ListPresets.mockResolvedValueOnce(null)
  const s = useLocationsStore()
  await s.load()
  expect(s.presetsForPane('local')).toEqual([])
  expect(s.disksForPane('local')).toEqual([])
  expect(s.presetsForPane('remote')).toEqual([])
})
```

同时把 `import { AddLocalRecent, AddRemoteRecent } from '../../wailsjs/go/main/App'` 改成
`import { AddLocalRecent, AddRemoteRecent, ListPresets } from '../../wailsjs/go/main/App'`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/stores/locations.test.js`
Expected: FAIL —— `s.presetsForPane is not a function`

- [ ] **Step 3: 写实现**

```js
// frontend/src/stores/locations.js：import 追加 ListPresets
import {
  ListLocations, ListPresets, AddBookmark, RemoveBookmark, AddLocalRecent, AddRemoteRecent,
} from '../../wailsjs/go/main/App'

export const useLocationsStore = defineStore('locations', {
  state: () => ({
    bookmarks: [], localRecents: [], remoteRecents: [],
    // 固定预设（local/remote 来自配置文件，localDisks 由后端实时枚举），与用户位置数据分开存。
    presets: { local: [], localDisks: [], remote: [] },
    loaded: false,
  }),
  actions: {
    async load() {
      // 两次 IPC 并行；预设失败不该连累书签/最近（绑定异常时各自兜底）。
      const [data, presetData] = await Promise.all([
        ListLocations(),
        ListPresets().catch(() => null),
      ])
      this.bookmarks = (data && data.bookmarks) || []
      this.localRecents = (data && data.localRecents) || []
      this.remoteRecents = (data && data.remoteRecents) || []
      // 后端保证三组非 nil；这里仍兜底 null，避免绑定异常时整块 UI 白屏。
      this.presets = {
        local: (presetData && presetData.local) || [],
        localDisks: (presetData && presetData.localDisks) || [],
        remote: (presetData && presetData.remote) || [],
      }
      this.loaded = true
    },
    // …既有 addBookmark / removeBookmark / addLocalRecent / addRemoteRecent / recentsForPane / bookmarksForPane 保持不变…
    presetsForPane(pane, host) {
      if (pane === 'local') return this.presets.local
      // 远程自定义预设可带 host：留空 = 所有主机（与 bookmarksForPane 同规则）。
      // host 为空（还没选主机）时不筛，避免面板空白。
      return this.presets.remote.filter((p) => !p.host || !host || p.host === host)
    },
    // 磁盘是本地概念：远程面板恒为空组。
    disksForPane(pane) {
      return pane === 'local' ? this.presets.localDisks : []
    },
  },
})
```

- [ ] **Step 4: 跑测试 + 构建确认通过**

Run: `cd frontend && npx vitest run src/stores/locations.test.js && npm run build`
Expected: PASS（含既有 6 个用例）**且构建成功**。

> ⚠️ 只跑 vitest 是**假绿**：`locations.test.js` 用 `vi.mock` 整个替换了 `wailsjs/go/main/App`，绑定缺失照样全绿。真正的失败形态是**构建期 Rollup 硬错误**：`"ListPresets" is not exported by wailsjs/go/main/App.js`（已实测复现）。所以本任务的完成判据必须包含 `npm run build`，其前置是 Task 2 Step 5 已重生成绑定。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/stores/locations.js frontend/src/stores/locations.test.js
git commit -m "feat(store): locations 并行加载固定预设与磁盘组"
```

---

### Task 5: `FilePane.vue` 位置下拉新增「预设」「磁盘」两组

**Files:**
- Modify: `frontend/src/components/FilePane.vue`（props `:16-22`、模板 `:99-107`）

**Interfaces:**
- Consumes: 父组件给的 `presets` / `disks`（来自 Task 4）
- Produces: 无新增事件（仍复用 `@pick-position`，值即 `path`）

**说明**：仓库没有组件单测（`frontend/src/components/` 下无 `.test.js`，与 Plan B Task 8 的做法一致），因此本任务的验证 = `npm run build` + 末尾人工清单；渲染逻辑刻意保持"零判断"（空组由 `v-if` 关掉），不引入新分支。

- [ ] **Step 1: 加两个 prop**

```js
  // 位置下拉里的固定预设与 Windows 盘符，由父组件按面板给出（内容来自 ListPresets）。
  presets: { type: Array, default: () => [] },
  disks: { type: Array, default: () => [] },
```

- [ ] **Step 2: 改模板（`:99-107` 的 `<select>` 整块替换）**

```html
      <select class="pos" :value="''" @change="onPick($event)">
        <option value="" disabled selected>📍 位置</option>
        <optgroup v-if="presets.length" label="预设">
          <option v-for="p in presets" :key="'p' + p.path" :value="p.path" :title="p.path">{{ p.name }}</option>
        </optgroup>
        <optgroup v-if="disks.length" label="磁盘">
          <option v-for="d in disks" :key="'d' + d.path" :value="d.path" :title="d.path">{{ d.name }}</option>
        </optgroup>
        <optgroup v-if="bookmarks.length" label="书签">
          <option v-for="b in bookmarks" :key="'b' + b.path" :value="b.path">{{ b.name || b.path }}</option>
        </optgroup>
        <optgroup v-if="recents.length" label="最近">
          <option v-for="r in recents" :key="'r' + r.path" :value="r.path">{{ r.path }}</option>
        </optgroup>
      </select>
```

顺序固定为 **预设 → 磁盘 → 书签 → 最近**（越上位越"固定"）；`onPick` 的"选中即清零"逻辑不动——它保证同一个位置连选两次都能触发 `change`。

- [ ] **Step 3: 构建验证**

Run: `cd frontend && npm run build`
Expected: 构建成功（无 `presets is not defined` 之类的模板错误）

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/FilePane.vue
git commit -m "feat(FilePane): 位置下拉新增预设与磁盘两组"
```

---

### Task 6: `SftpView.vue` 接线（预设/磁盘 + 远程 sentinel + 本地路径助手）

**Files:**
- Modify: `frontend/src/views/SftpView.vue`
  - import 区（`:3-18`）
  - 本地/远程路径助手（`:222-233`）
  - `openRemote`/`openLocal`（`:207-220`）
  - `openHit`（`:172-188`）
  - `pickPosition`（`:147-154`）与 `connect()`（`:127-145`，**`pendingPath === '/' ` 的过期哨兵必须同批修**）
  - `removeSelected`（`:392-407`）、`renameItem`（`:415-429`）、`mkdirIn`（`:436-448`）
  - `handleSystemDrop`（`:606-644`）、`runBatch` 的 systemPaths 分支（`:325-327`）
  - 模板里两个 `<FilePane>`（`:718-733`）

**Interfaces:**
- Consumes: Task 3 的 `localpath` 助手、Task 2/4 的预设数据、Task 5 的新 prop、`preset.RemoteHomeToken = "~"`
- 行号均以 `HEAD = 2c2543b` 为准；实施时按内容定位，不要盲信行号。

- [ ] **Step 1: import 与常量**

```js
import * as sel from '../utils/selection'
import { join as localJoin, parentOf as localParent, joinRel, splitPath } from '../utils/localpath'
```

并在 `const search = reactive(...)`（`:25`）附近加：

```js
// 远程「主目录」预设的 sentinel：必须与 Go 侧 preset.RemoteHomeToken 字面量一致。
// 远端 home 要先连上主机 pwd 才知道，所以下拉里传的是占位符而不是路径。
const REMOTE_HOME = '~'
```

- [ ] **Step 2: 路径助手改名 + 本地面板改用 localpath**

```js
// 远程路径语义：POSIX，'/' 是根，向上到 '/' 为止（原样保留）。
function posixJoin(base, name) {
  if (base === '/' || base === '') return '/' + name
  return base.replace(/\/+$/, '') + '/' + name
}
function posixParentOf(p) {
  if (!p || p === '/') return '/'
  const trimmed = p.replace(/\/+$/, '')
  const idx = trimmed.lastIndexOf('/')
  if (idx <= 0) return '/'
  return trimmed.slice(0, idx)
}
```

调用点改写（**全部**调用点，漏一个就会出现"某条路径还是 POSIX 语义"的隐性不一致）：

| 原调用 | 位置 | 改成 |
|---|---|---|
| `parentOf(remotePath.value)` | `openRemote` `:208` | `posixParentOf(remotePath.value)` |
| `join(remotePath.value, it.name)` | `openRemote` `:210` | `posixJoin(remotePath.value, it.name)` |
| `parentOf(localPath.value)` | `openLocal` `:215` | `localParent(localPath.value)` |
| `join(localPath.value, it.name)` | `openLocal` `:217` | `localJoin(localPath.value, it.name)` |
| `join(localPath.value, it.name)` / `join(localPath.value, newName)` | `renameItem` `:421` | `localJoin(...)` 两处 |
| `join(remotePath.value, …)` 两处 | `renameItem` `:424` | `posixJoin(...)` 两处 |
| `join(localPath.value, name)` | `mkdirIn` `:441` | `localJoin(...)` |
| `join(remotePath.value, name)` | `mkdirIn` `:444` | `posixJoin(...)` |
| `join(remotePath.value, name)` | `uploadPicked` `:458`（传输记录 dst） | `posixJoin(...)` |
| `join(remotePath.value, name)` | `uploadPicked` `:461`（**SftpPut 的实参**，与上一行是同名不同处，别漏） | `posixJoin(...)` |
| `base.replace(/\/+$/, '') + '/' + n` | `removeSelected` `:394` | `pane === 'local' ? localJoin(base, n) : posixJoin(base, n)` |

**本表的范围与已知 out-of-scope（诚实标注，别当成遗漏）**：本表覆盖 `grep -rn 'join(' frontend/src/views/SftpView.vue` 与 `grep -rn 'parentOf(' …` 的**全部命中**（共 11 处：8 处 `join(` + 2 处 `parentOf(` + 定义 1 处）。以下三处**有意不改**，理由各自写明：

| 位置 | 现状 | 为什么不改 |
|---|---|---|
| `SftpView.vue:542-556` `onMoveDrop` | 本地侧用 `base.replace(/\/+$/,'') + '/' + name` 拼 3 处 | 混用分隔符 **Go 侧 `os.Rename` 完全接受**，且这些串只在本函数内部互相比较（判重/守卫/登记），自洽；改它要顺带把 `canDropInto` 的前缀守卫改成分隔符感知，超出本次"能切盘 + 位置预设"的范围 |
| `frontend/src/utils/dnd.js:26-40` `isSubPath`/`canDropInto` | 纯 POSIX 前缀判断 | 同上：其输入（`sourceDir + '/' + item.name`、`targetDir`）都由本函数用同一规则构造，比较自洽；**自嵌套守卫当前不可被绕过**（`targetIsTheItem` 的字符串相等仍能拦下） |
| `frontend/src/utils/batch.js:8-9` `joinDir` | 同时服务下载/上传两个方向 | 见 §0「不在范围」 |

- [ ] **Step 3: `openHit` 本地分支用 `splitPath`/`joinRel`**

```js
async function openHit(hit) {
  // hit.path 是**根相对路径**且后端已 filepath.ToSlash（恒用 '/' 分隔）；
  // 本地拼回绝对路径时要按基目录的风格输出分隔符（Windows 上是 '\'）。
  const { dir, name } = splitPath(hit.path)
  if (search.pane === 'local') {
    if (dir) localPath.value = joinRel(localPath.value, dir)
    await loadLocal()
    if (name) sel.single(localSelection, name)
  } else {
    // 远程必须继续用 POSIX 助手：sepOf() 只要发现路径里有 '\' 就返回 '\'，
    // 而远端路径里出现反斜杠是合法的（文件名），joinRel 会产出混合分隔符。
    // hit.path 后端已 filepath.ToSlash，dir 恒用 '/' 分隔。
    if (dir) remotePath.value = posixJoin(remotePath.value, dir)
    await loadRemote()
    if (name) sel.single(remoteSelection, name)
  }
  search.visible = false
}
```

- [ ] **Step 4: `pickPosition` 支持预设（含远程 `~`）**

```js
// 面板头「📍 位置」选中一项。本地面板直接跳；远程同机已连接直接跳，
// 未连接则记入 pendingPath 交给 connect()（它内部消费 pendingPath）。
// 远程「主目录」是 sentinel：已连接时问后端要 home，未连接时把 pendingPath 置空
// 让 connect() 走它的 else 分支（dest = SftpHome）。
async function pickPosition(pane, path) {
  if (pane === 'local') { localPath.value = path; await loadLocal(); await locations.addLocalRecent(path); return }
  if (path === REMOTE_HOME) {
    if (!host.value) { err('请先选择主机'); return }
    if (!connected.value) { pendingPath.value = ''; await connect(); return }
    try { remotePath.value = await SftpHome(host.value) } catch (e) { err(e); return }
    await loadRemote()
    return
  }
  if (host.value && connected.value) { remotePath.value = path; await loadRemote(); await locations.addRemoteRecent(host.value, path); return }
  pendingPath.value = path
  await connect()
}
```

**同一步必须修 `connect()` 里的一个过期启发式**（`SftpView.vue:136`）：

```js
// 旧：把 '/' 当成"未指定"。本轮把「远程 根目录 /」加进了默认预设，
//     于是未连接时选它 → pendingPath='/' → 被误判为未指定 → 落到 home。
// remotePath.value = pendingPath.value && pendingPath.value !== '/' ? pendingPath.value : dest
// 新：只有空串才是"未指定"（REMOTE_HOME 分支正是置空后交给 connect() 去落 home）
remotePath.value = pendingPath.value ? pendingPath.value : dest
```

依据（实施时自查）：`grep -n pendingPath frontend/src/views/SftpView.vue` 在 HEAD 上只有 5 处 —— `:54` 注释、`:56` 定义、`:136` 消费、`:137` 清空、`:152` 写入；**唯一写入点是 pickPosition**，所以 `'/'` 这个哨兵值已无来源，留着只会把「根目录」预设误判掉。（P2 把入口从 header 移到面板后，这个启发式就失效了。）

- [ ] **Step 5: 系统拖入与批量任务的本地目标路径**

```js
// handleSystemDrop（原 :617）：base 不再手工去尾分隔符，dst 一律经过助手
  const base = localPath.value || '/'
  // …
    const dst = localJoin(base, name)          // 原 base + '/' + name
  // …同一处 transfers.push 里的 dst 也用 localJoin(base, i.name)

// runBatch 的 systemPaths 分支（原 :326）：
  const joinDst = direction === 'download' ? localJoin : posixJoin
  const tasks = systemPaths
    ? systemPaths.map((i) => ({ name: i.name, src: i.path, dst: joinDst(targetDir, i.name), isDir: i.isDir }))
    : planTasks({ /* 保持不变 */ })
```

- [ ] **Step 6: 模板传入预设与磁盘**

```html
        <FilePane title="本地" pane="local" host="" :path="localPath || '/'" :items="localItems" :sel-keys="[...localSelection.keys]" :anchor="localSelection.anchor"
          :presets="locations.presetsForPane('local', '')" :disks="locations.disksForPane('local')"
          :bookmarks="locations.bookmarksForPane('local', '')" :recents="locations.recentsForPane('local', '')" :bookmarked="bookmarkedFor('local')"
          …
        <FilePane title="远程" pane="remote" :host="host" :path="remotePath" :items="remoteItems" :sel-keys="[...remoteSelection.keys]" :anchor="remoteSelection.anchor"
          :presets="locations.presetsForPane('remote', host)"
          :bookmarks="locations.bookmarksForPane('remote', host)" :recents="locations.recentsForPane('remote', host)" :bookmarked="bookmarkedFor('remote')"
          …
```

（远程面板**不传** `disks`：`FilePane` 的默认值就是空数组，磁盘组不会出现。）

- [ ] **Step 7: 构建 + 单测 + "无残留"硬检查**

Run:
```bash
cd frontend
npm run build && npx vitest run
# 关键完整性检查：改名后旧助手名不得再被调用（除 localpath.js 自身的定义/测试）
grep -rn 'join(remotePath\|join(localPath\|parentOf(' src || echo "无残留（预期）"
```

Expected: 构建成功；vitest 全绿；grep **不得命中 `src/views/SftpView.vue`**。

> ⚠️ 这一条不是形式主义：本计划的调用点表格**最初就漏掉了一处** —— `uploadPicked` 里 `SftpPut(host.value, '', local, join(remotePath.value, name))`（仓库现状 `:461`，与 `:458` 的传输记录 `dst` 是同名不同处）。`join` 改名 `posixJoin` 后，漏改的那行会在运行时抛 `join is not defined`，被 `try/catch` 吞成"上传失败"；而 `npm run build` 与 `npx vitest run` **都查不出来**（`SftpView.vue` 没有单测覆盖）。所以这条 grep 是必做的，不是可选的。

- [ ] **Step 8: Commit**

```bash
git add frontend/src/views/SftpView.vue
git commit -m "feat(sftp): 位置预设/磁盘接线，本地面板改用跨平台路径助手"
```

---

### Task 7: 文档与全量回归

**Files:**
- Modify: `README.md`（`:78` 的 SFTP 功能行）
- Modify: `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`（决策表 + §9）

- [ ] **Step 1: README：功能行 + 配置示例**

(a) 把 `:78` 的 `📍 位置下拉（书签 + 最近位置，各上限 20，远程按主机隔离）与 ☆ 收藏 / 双侧递归深搜`
改成
`📍 位置下拉（预设：主目录/桌面/下载/文档/根目录 + 配置文件自定义；Windows 全部盘符 / 书签 / 最近位置，各上限 20，远程按主机隔离）与 ☆ 收藏 / 双侧递归深搜`。

(b) 主配置示例里**不再出现 `[[presets]]`**（预设已移出主配置），改成一行指针 + 一节专门说明：

在配置示例的 `[[bookmarks]]`（`:121`）之后插入一行注释：

```toml
# 位置预设不在本文件里：见同目录的 presets.toml（首次启动自动生成，带注释说明）
```

在配置章节末尾（`:125` 的代码块之后、`## 架构` 之前）新增一节：

```markdown
### 位置预设（presets.toml）

「SFTP 页 → 📍位置」里的「预设」组**完全等于** `presets.toml`（与 `sshore.toml` 同目录：
unix 是 `~/.config/sshore/presets.toml`，Windows 是 `%AppData%\sshore\presets.toml`）。
这个文件**只在你首次启动时生成一次，之后应用不会再改写**——加注释、调顺序、改名都可以放心改。

```toml
[[presets]]
name  = "主目录"
scope = "local"        # local | remote（缺省 local）
path  = "/home/lan"    # 本地面板用绝对路径（~/xxx 会展开为主目录）

[[presets]]
name  = "主目录"
scope = "remote"
path  = "~"            # 远端 home：连上主机后解析为 SftpHome

# 只对某台主机显示的远端预设（host 留空 = 所有主机）
# [[presets]]
# name  = "生产日志"
# scope = "remote"
# host  = "prod"
# path  = "/var/log"
```

- **删掉哪条，下拉里就没有哪条**；保留文件、删光条目 = 「预设」组消失（空组不渲染）。
- **想恢复默认**：删掉整个 `presets.toml`（会被视为"从未播种"，下次启动重新生成默认模板）。
  注意"删条目"与"删文件"语义相反，别用删文件来清空。
- **Windows 路径写法**：双引号里反斜杠是转义符，`path = "C:\Users\you\Desktop"` 会**解析失败**；
  用单引号字面量 `path = 'C:\Users\you\Desktop'` 或双写 `"C:\\Users\\you\\Desktop"`。
- **改完重启应用生效**（面板只在进入 SFTP 页时读一次）。
- Windows 的「磁盘」组不在任何配置文件里：盘符每次实时枚举，插拔 U 盘后重开面板即可见。
- 文件写坏时应用只提示、**不会覆盖你的文件**（日志面板会看到解析错误），预设组暂时为空。
```

(c) （可选，但建议做）`docs/images/sftp.png` 里还是旧的面板头（README `:16` 引用它做"双栏浏览、最近位置"示意），本轮 UI 变了：用带 预设/磁盘 组、且位置下拉展开的界面重截一张，避免文档与产品不符。

- [ ] **Step 2: 回写 spec（决策表 `:76` 之后追加三行：决策 34/35/36；§9 `:379` 之后追加两条）**

```markdown
| 34 | 位置预设 | 「📍位置 ▾」在书签/最近之上固定加两组：**预设**（首启播种时只写实际存在的本地目录；播种后完全以配置为准）与 **磁盘**（Windows：全部逻辑盘符）；预设组的**内容来源**见决策 36（配置文件；首次启动播种），本决策只管 UI 结构：两组、固定顺序、空组不渲染。远程「主目录」用 sentinel `~`（`preset.RemoteHomeToken`）：已连接时解析为 `SftpHome`，未连接时置空 `pendingPath` 走 `connect()` 的 home 分支。盘符路径一律带尾反斜杠（裸写 `D:` 在 Windows 上表示"D 盘的当前目录"） |
| 35 | 本地路径语义 | 本地面板的路径拼接/上溯改用 `frontend/src/utils/localpath.js`（按路径自身风格处理 `\`/`/`、盘根与 UNC 共享根不外溢、裸盘符 `D:` 归一为 `D:\`）；**远程面板继续用 POSIX 助手**。修此缺陷是决策 34 可用的前提：原 `parentOf` 对 `C:\\Users\\lan` 返回 `/`（Go 在 Windows 把 `/` 解析为当前盘根），从盘符进目录后按 `..` 会被甩到 C 盘 |
| 36 | 预设放独立文件 `presets.toml` | 「📍位置」的**预设组完全等于** `presets.toml` 的 `[[presets]]` 条目（按 `scope` 分面板，远端再按 `host` 过滤，留空=所有主机）。**只在文件不存在时生成一次**（带说明注释的模板 + 默认项：本地 主目录/桌面/下载/文档 + 非 Windows 的根目录；远程 主目录 `~` / 根目录 `/`），此后**永不改写** —— 用户注释/顺序永久保留；**判据是文件存在性**，不是标记字段。删条目即消失、不重建；删文件会在下次启动重新生成默认模板（README 必须写明两者语义相反）。**单独文件换来的两件事**：① 降级安全（旧版二进制不认识该文件；放主配置里时旧版解码忽略未知键、一旦保存就整段抹掉且无备份 —— 已实测）；② 不被主配置的高频全量重编码反复抹掉注释。坏文件只降级不覆盖（`presetsErr` → Init 补发事件）；`AppConfig` **不新增任何字段**。**磁盘组不进文件**（盘符必须实时枚举）。不改预设存储位置、不引入 UI 增删 |
```

```markdown
- **预设与磁盘**（决策 34）：下拉顺序固定 `预设 → 磁盘 → 书签 → 最近`，空组不渲染；预设组的内容来源见决策 36。
- **预设的唯一来源**（决策 36）：`presets.toml` 单独一个文件，只在首次启动生成一次（带注释模板），此后永不改写；**判据是文件存在性**（删条目 ≠ 删文件）；坏文件只降级不覆盖；远端条目的 `host` 由前端按当前主机过滤；**磁盘组实时枚举、不进文件**。
```

- [ ] **Step 3: 全量回归**

Run: `make ci`
Expected: `npm run build` 成功；`go vet` 无输出；`go test ./... -race -count=1` 全绿；`npx vitest run` 全绿

- [ ] **Step 4: e2e 脚本（若本机有 /usr/sbin/sshd）**

Run: `make e2e`
Expected: `bash e2e/test_local.sh` 正常退出。本计划未触碰 `internal/sftp`，此步只是防回归；若环境缺 sshd 则跳过并在报告里注明。

- [ ] **Step 5: Commit**

```bash
git add README.md docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md
git commit -m "docs: 位置预设（presets.toml sidecar）与本地路径语义写入 README 与 spec（决策 34/35/36）"
```

---

### Task 8: Windows 真机验证（**必做**：本批改动的主体就是 Windows 特有行为，Linux 上只能覆盖到"编译 + 纯函数单测"）

**为什么必须做**（用户已确认）：本批改动的主体（盘符枚举、KnownFolderPath、盘根上溯）在 Linux 上**只覆盖到"编译通过 + 纯函数单测"**，真实行为只能在 Windows 上判定。执行方式按 `real-machine-testing` 技能的 Windows 章节（VirtualBox `win10` 客户机 + NAT `sshfwd 127.0.0.1:2222→22`、Guest Additions 7.2.16、SSH 通道 `-p 2222`、控制台截图取证 `VBoxManage controlvm … screenshotpng`）。

**已知坑（技能 §2.9 有完整清单，这里列出与本任务直接相关的三条）**：
- Wails 的 386 构建受 **WOW64 文件系统重定向**影响，`C:\Windows\System32\OpenSSH` 看不到 → 用 `GOARCH=amd64` 构建，或先把 `sftp.exe` 复制到用户目录并把该目录前置进 `PATH`（上一轮用的是后者）。
- 经 SSH 启动的 GUI 属于 **Session 0**，WebView2 会报 `error creating controller with 800700aa` → 必须在交互式会话里启动（`run_sshore.cmd` 用 `start "" …`）。
- `VBoxManage keyboardputstring` 不能输入非 ASCII → 中文目录路径用"预设下拉点选"而不是手打。

- [ ] **Step 1: 构建 Windows 产物并投递到客户机**
- [ ] **Step 2: 首启并观察播种**（**先不要动任何配置**）：启动应用 → 打开 SFTP 页 → 确认「预设」组出现默认项 → 退出应用，把 `%AppData%\sshore\presets.toml` 复制一份作对照（第 10 条前半段的证据：模板 + 默认项确实被生成了，且带说明注释）
  > 顺序不能反：只有**文件不存在**时才会生成，所以必须先首启再改文件；反过来先手写 `presets.toml`，第 10 条前半段就没法观察了。
- [ ] **Step 3: 加工预设文件并重启**：在 `%AppData%\sshore\presets.toml` 里加一条 `scope=local` 指向真实目录、一条 `scope=remote, host=winlocal, path=/tmp`、一条 `path` 为空的坏条目（**坏条目要单独验一次并恢复**，见第 12 条）；另外顺手加一行注释，用来验"应用不会改写这个文件"（第 11 条）。**重启应用**后看两个面板
- [ ] **Step 4: 逐条核对下面的清单，截图留证**（证据文件放 `/tmp`，报告中引用绝对路径）

| # | 断言 | 判定依据 |
|---|---|---|
| 1 | 位置上拉出现 **预设** 组，含默认播种的项 | 截图里 optgroup 展开可见（**主目录必定在**；桌面/下载/文档只播种实际存在的目录，可能不足 4 项，别按"必须 4 项"判失败） |
| 2 | 位置上拉出现 **磁盘** 组，列出全部盘符（至少 C:、D:） | 截图；与 `wmic logicaldisk get deviceid` 或资源管理器对比 |
| 3 | 选 `D:\` → 本地面板列出 D 盘根内容，路径显示 `D:\` | 列表非空 + `curpath` 文本 |
| 4 | 在 `D:\<子目录>` 按 `..` → 回到 `D:\`（**不跳到 C:，也不显示 `/`**） | 连按两次 `..` 后路径仍是 `D:\`（这是决策 35 的核心回归） |
| 5 | 在 `D:\` 继续按 `..` → 停在 `D:\` | 路径不变、列表不变 |
| 6 | 桌面/下载/文档预设的路径与资源管理器一致（含 OneDrive 重定向的机器） | 路径文本对比 |
| 7 | 远程面板「主目录」/「根目录」：未连接时选中 → 连接后分别落在 `SftpHome` / `/`；已连接时选中 → 直接跳转 | 面板路径文本。**「根目录」这条专门回归 `connect()` 的 `pendingPath === '/'` 过期哨兵**——不测它，本轮评审发现的 Critical 就是漏网的 |
| 8 | 深搜（🔍）本地命中项双击 → 跳到所在目录并选中该项（Windows 反斜杠路径） | 跳转后选中行高亮 |
| 9 | 中文/空格目录名下的选择、批量下载、拖拽落点仍正常 | 无回归 |
| 10 | 首启后 `presets.toml` **被自动生成**且带说明注释、含默认项；接着手加的两条也生效（本地那条出现在本地面板，远端那条只在 `winlocal` 主机下显示） | 文件内容截图 + 两个面板截图；切到别的主机后远端那条消失 |
| 11 | 删掉文件里的某条（再试一次全删）→ 重启后下拉里就没有了，**不会自动重建**；用户加的注释**原样还在**（应用从未改写该文件） | 截图；确认文件字节与手改后一致 |
| 12 | 坏条目/坏 TOML（先删掉一个引号）→ 应用启动时日志面板报解析错误、预设组为空、**主配置的隧道/书签不受影响**、**文件未被覆盖**（改回后恢复正常） | 日志面板 + 文件内容对比截图 |
- [ ] **Step 5: 清理客户机**（删除投递的产物与测试配置条目、恢复 `~/.ssh/config`、关机），把结论写入 `.superpowers/sdd/<plan-basename>/progress.md`

---

## 自检（Self-Review）

**1. Spec 覆盖**：本计划是 spec \9 的补充，四点分别由 Task 1（预设数据模型 + `presets.toml` 读写与带注释模板）、Task 2（startup 首次生成 + `ListPresets` 读文件）、Task 3/6（本地路径语义）、Task 4/5/6（store 过滤 + 渲染 + 接线）实现，并在 Task 7 回写 spec 决策 34/35/36。spec 其余章节（P1/P2）已完成，不受影响。

**2. Placeholder 扫描**：无 TBD/TODO；每个 code step 都给了可直接粘贴的实现与精确的运行命令。Task 8 的流程引用 `real-machine-testing` 技能（等价于 Plan B 引用 spec 的做法），但验收断言与已知坑都已写死。Task 6 的表格给出**全部**调用点（含行号），避免"只改一半"。

**3. 类型一致性**：`config.Preset{Name,Scope,Host,Path}` ↔ `preset.Entry`（由 `toConfigPresets`/`presetEntries` 双向转换，`TestPresetConvertersAreSymmetric` 钉住）→ `preset.All(entries)`（= `User("local",…)` + `Drives` + `User("remote",…)`）→ `Presets{Local,LocalDisks,Remote}` → JS `presets:{local,localDisks,remote}` → `presetsForPane(pane, host)` → `FilePane` 的 `presets/disks`。生成链路：`preset.Defaults()` → `toConfigPresets` → `config.PresetsTemplate` → `config.SavePresets`（只此一次）。`scope` 归一只有一处实现（`config.NormalizeScope`），`LoadPresets` 与 `presetEntries` 都调它，且都有断言。

## 验收清单（可脚本化 / 人工）

**可脚本化**（Task 1/2/3/4/7 已覆盖）：

- `go test ./internal/preset/ -count=1` → 全绿（预设组 = 配置条目、`Defaults` 只播种存在的目录、盘符位掩码、sentinel 字面量、过滤/去重、三组非 nil）
- `go test ./internal/config/ -run 'TestPresetsTemplate|TestLoadPresets' -count=1` → 全绿（带注释模板往返含 Windows 反斜杠、缺文件 → `os.ErrNotExist`、scope 归一、坏 TOML 只报错且不改动文件）
- `go test ./ -run 'TestListPresets|TestStartupSeedsPresets|TestPresetConverters' -count=1` → 全绿（预设组完全等于文件内容、host 透传、坏文件只降级不覆盖、首次生成带注释模板且之后永不改写、清空条目不复活、转换器对称）
- `cd frontend && npx vitest run src/utils/localpath.test.js src/stores/locations.test.js` → 全绿（含远程预设按 host 过滤）
- `make ci` → 前端构建 + vet + `-race` 全量 Go 测试 + vitest 全绿
- `wails generate module` 后 `App.d.ts` 含 `ListPresets`

**人工**（Linux 桌面）：首启后 `~/.config/sshore/presets.toml` 被生成（带注释、含默认项）、下拉的 预设 组与文件一一对应、**没有** 磁盘 组；删掉一条重启后消失（不复活）、用户加的注释仍在；选中预设可跳转；`..` 行为与改动前一致；书签/最近/☆/深搜无回归。

**人工**（Windows 真机，Task 8 的 12 条清单）：磁盘组列出全部盘符、`D:\` 可进、`..` 在盘根停住、KnownFolder 路径正确、`presets.toml` 首次生成且此后不被改写、手改生效/删除不复活、坏文件只降级不覆盖（主配置的隧道/书签不受影响）、远程主目录与根目录 sentinel 正确、深搜跳转正确。

## 执行交接

计划已保存到 `docs/superpowers/plans/2026-09-16-sftp-location-presets.md`。执行方式二选一：

1. **Subagent-Driven（推荐）** —— 每个 Task 派一个全新 subagent，任务间做两阶段评审（自审 + 干净 subagent 评审），失败修复最多 5 轮；ledger 写 `.superpowers/sdd/2026-09-16-sftp-location-presets/progress.md`。
2. **Inline Execution** —— 在本会话按 `executing-plans` 分批执行，带检查点。

无论哪种方式，**Task 8 必做**（用户已确认要做 Windows 真机验证）。
