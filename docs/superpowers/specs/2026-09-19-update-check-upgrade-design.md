# 检测更新与自升级（GitHub Release 源 + SHA256 校验 + 脚本替换）设计

> 状态：v2 · 用户已逐节确认（§16 决策记录）· 已完成两阶段审核的**第一阶段**（自审，清单见 §16 末段）· 待**第二阶段**（干净 subagent）复核 · 尚未实施
> 关联：`docs/superpowers/specs/2026-08-25-sshkit-design.md`（应用定位与设置项基线）
> 关联：`README.md` §下载安装 / §尺寸优化（UPX 误报的既有记载）
> 本期范围：**阶段 A** = 检查更新与提示；**阶段 B** = 应用内下载 + SHA256 校验 + 用户显式触发的一次性升级脚本
> 证据来源：§1.1 的代码事实（均带 `file:line`）与 §4 的待复核事实清单

---

## 1. 背景

### 1.1 现状（代码事实）

| 位置 | 事实 |
|---|---|
| `app.go:49-51` | `Version = "dev"`、`Repo = "https://github.com/i2534/sshore"`，由 `-X main.Version=...` 注入 |
| `Makefile:23` | `LDFLAGS := -s -w -X main.Version=$(VERSION)`，`VERSION := $(shell git describe --tags ...)` |
| `app.go:231` `GetAppInfo()` | 唯一对外暴露版本的地方：`AppInfo{Name, Version, Repo}` |
| `frontend/src/components/SettingsDialog.vue:97-105` | 「帮助」段只展示 `应用 · 版本 · 仓库` 与一个在线文档链接，**没有任何动作按钮** |
| `frontend/src/App.vue` | 侧栏四个按钮（端口转发 / 文件同步 / SFTP / ⚙ 设置）；全局错误条 `.fatal`；`onMounted` 里 `GetAppInfo()` 设置 HTML 标题 |
| `internal/config/store.go:17-26` | `AppSettings`：`auto_reconnect_default` / `sftp_transport` / `theme` / `font_scale` / `latin_font` / `cjk_font` / `auto_start_on_launch` |
| `internal/config/store.go:156-166` | `AppConfig{App, Tunnels, RecentSFTP, Syncs, Bookmarks, LocalRecent, RemoteRecent, LegacyMigrated}`，TOML |
| `app.go:251` / `app.go:260` | `GetSettings()` / `SetSettings(AppSettings)` 已存在 —— 新设置项复用这两个绑定即可 |
| 全仓库 | **零 `net/http` 引用**：更新检查是本应用第一次对外发 HTTP 请求 |
| `.github/workflows/ci.yml` | `build-linux` / `build-windows` 各自打包 `sshore-<v>-<os>-amd64.{tar.gz,zip}`；`release` job 下载全部 artifacts 后 `gh release create/upload`，**只上传 `*.tar.gz`/`*.zip`，无校验文件** |
| `Makefile:25-28` | `COMPRESS ?= 1`，默认 UPX 压缩；`README.md` 已记载「杀毒软件误报 UPX 打包的 Go 二进制」并给 `COMPRESS=0` 退路 |
| `internal/sync/transfer.go:20-26` | 仓库内既有的「先写临时名再 `rename`」原子落盘先例 |

### 1.2 缺口

1. 用户不知道有新版本：Release 更新了，应用内无任何提示（`GetAppInfo` 只读本地注入值）。
2. 拿到新版本全靠用户自己去 GitHub Releases 找包、解压、替换二进制 —— 对 Windows 用户尤其别扭（要退出、改名、再改名）。
3. 仓库已经有完整的产物命名规则与 Release 流水线，但**没有校验文件**，任何「自动下载并安装」都必须先把这个洞补上。

### 1.3 先考虑过、但未采纳的路

- **静默自动安装（不点确认）**：会被隧道/同步被切断的意外打断，且放大 AV 特征；用户明确选择「显式触发」。
- **helper 模式（`sshore --apply-update` 自替换，不开脚本）**：纯 Go、无解释器链，Windows 上最稳；但用户选择要一份可审阅、可手动重跑的脚本。列为 AV 告警时的预案（§10.4）。
- **纯静态规则源（302 取 tag + 命名推导，不解析 JSON）**：零 API 配额、对简陋反代友好，但拿不到更新说明/发布时间，且把产物命名变成对外契约；用户选择统一走 API JSON。
- **不校验完整性**：与「自动下载二进制并替换自身」组合风险过高，用户选择 SHA256 校验 + 发布流程补 `checksums.txt`。
- **前端 JS 侧实现下载**：Wails 的 webview 内无法可靠落盘到程序目录、无法做进程替换，全部逻辑必须在 Go 侧。

---

## 2. 决策摘要

**两阶段（一份 spec 覆盖，实施分两步交付）**

- **阶段 A（可独立交付）**：检查更新 + 提示（含自动检查、轮询、角标、设置页更新区、发布页兜底）。
- **阶段 B（在 A 之上）**：应用内下载 → SHA256 校验 → 解包待安装文件 → 用户点「重启并升级」→ 分离启动升级脚本 → 应用退出 → 脚本替换并启动新版。

**澄清结论（逐条经用户确认，详见 §16）**

1. 更新源：**GitHub API JSON**，默认 `https://api.github.com/repos/i2534/sshore`，可在设置里覆盖。
2. 完整性：发布流程补 `checksums.txt`（SHA256），应用下载后比对，**不符即拒绝安装**。
3. 时机：启动后延迟 5s 自动检查一次 + 手动按钮 + 后台按间隔轮询（默认 12h，0 = 关闭）。
4. 提示：设置页「更新」区呈现全部细节 + 侧栏「⚙ 设置」按钮角标；**不做全局常驻横幅**。
5. 触发：**只有用户显式点击「重启并升级」才替换二进制**；不自动安装、不自动重启。
6. 脚本：`go:embed` 静态脚本 + **argv 传参**（零 shell 插值）；Windows 用**纯 cmd**（不调 PowerShell）。
7. 命名：待安装文件 `sshore.<新版本号>[.exe]`；替换时旧二进制备份为 `sshore.<旧版本号>[.exe]`，**只保留最近一份备份**。
8. 边界：`dev` 构建不自动检查（手动可查、可升级）；无本平台资产时明确告知并给发布页。
9. UPX 默认值本次不改（独立决策，见 §15）。

---

## 3. 范围

### 3.1 本期做

- `internal/update` 新包：源访问、版本比较、资产挑选、下载、校验、解包、替换计划、脚本生成、状态机。
- `app.go` 新增 8 个绑定（查询 / 检查 / 下载 / 取消 / 应用 / 删除已下载 / 跳过版本 / 取消跳过）+ 1 个发布页打开，并在 `App.Init`/`App.OnShutdown`/`App.SetSettings` 三处接线；进度与状态经 Wails 事件推送。
- `AppSettings` 新增 4 个字段（复用现有 `GetSettings`/`SetSettings`）。
- 前端：设置对话框「更新」区 + 侧栏角标 + 启动首查 + `stores/update.js` + `utils/update.js`。
- CI：`release` job 生成并上传 `checksums.txt`。
- 文档：README 增「更新与升级」一节。

### 3.2 本期不做（明确排除）

无人工点击的自动安装；升级后会话恢复；断点续传；增量/差分更新；macOS 支持；UPX 默认值调整；对 360/火绒等第三方 AV 的兼容保证；回滚界面（备份文件仅作为手工回滚手段）。

---

## 4. 事实前提（实施期必须复核的硬事实）

| # | 前提 | 复核方式 | 不成立时的应对 |
|---|---|---|---|
| F1 | `GET <api>/releases/latest` 返回 `tag_name` / `body` / `published_at` / `assets[].name` / `assets[].browser_download_url` | 实施第一步用 `gh api repos/i2534/sshore/releases/latest` 与 `curl` 各核一次 | 调整结构体字段；必要时补 `Accept: application/vnd.github+json` |
| F2 | 未认证配额约 60 次/小时/IP（文档值） | 观察响应头 `X-RateLimit-Remaining` / `X-RateLimit-Reset`，并在日志里记录 403/429 | 拉长默认间隔或降级为「打开发布页」 |
| F3 | **Windows 允许重命名（同卷 move）正在运行的 exe，但不允许删除/覆盖它** | 在 Windows VM 上先做一次性 spike（复制一个 exe 到临时目录、运行并尝试 `move`） | 改用 helper 模式（§10.4）或让用户手动替换 |
| F4 | Go 的 `net/http` 不写 Zone.Identifier（下载产物无 MOTW） | VM 上对下载产物 `Get-Item -Stream Zone.Identifier` 确认不存在 | 无需应对，仅影响 SmartScreen 预期 |
| F5 | 产物文件名形如 `sshore-v0.6.0-linux-amd64.tar.gz` / `sshore-v0.6.0-windows-amd64.zip` | `gh release view v0.6.0 --json assets`（已核） | 资产挑选按「含 tag + 含 os + 含 arch + 后缀」宽松匹配，失败即 `no-asset` |
| F6 | 归档内的二进制路径：tar.gz 为 **`./sshore`**（带 `./` 前缀、mode 0755、4,104,452 B）+ `./README.md` + `./LICENSE`；zip 为根目录下的 **`sshore.exe`**（4,600,320 B）+ README + LICENSE。压缩包本身 3,693,254 B / 4,429,317 B | `gh release download v0.6.0 --repo i2534/sshore --pattern 'sshore-v0.6.0-*'` 后 `tar -tvzf` / `unzip -l`（已核） | `ExtractBinary` 的条目匹配必须同时接受 `sshore` 与 `./sshore`；匹配不到即 `io-failed` |

---

## 5. 架构与组件

### 5.1 新增 `internal/update`（依赖全部注入，逻辑可纯单测）

| 文件 | 职责 | 关键接口 |
|---|---|---|
| `version.go` | 版本解析与比较，容忍 `v` 前缀、`v0.6.0-3-gabcd`、`dev`、空串 | `Compare(a, b string) int`、`IsDev(v string) bool`、`Base(v string) string` |
| `source.go` | 读 `<source>/releases/latest` | `type Release struct{ Tag, Notes, PublishedAt string; Assets []Asset }`、`Asset{Name, URL string}`、`Latest(ctx, source string) (Release, error)` |
| `asset.go` | 从 assets 里挑本平台包与校验文件 | `PickArchive(rel Release, goos, goarch string) (Asset, error)`、`PickChecksums(rel Release) (Asset, error)` |
| `checksum.go` | 解析 `sha256sum` 格式并比对 | `ParseChecksums(io.Reader) (map[string]string, error)`、`Verify(path string, want string) error` |
| `download.go` | 流式下载 + 边下边算 SHA256 + 进度回调 + ctx 取消 | `Download(ctx, url, dest string, progress func(done, total int64)) (string, error)` |
| `extract.go` | 从 `tar.gz`/`zip` 中只取出二进制（匹配 `sshore` / `./sshore` / `sshore.exe`，见 F6）；**条目白名单 + 拒绝路径遍历 + 单条目大小上限（≤ 64 MiB）** | `ExtractBinary(archive, goos, dest string) error` |
| `plan.go` | 计算替换计划（目标、待安装名、备份名、待清理的备份模式） | `type Plan struct{ Target, Pending, Backup, Size, ExeDir string }`、`PlanFor(goos, exePath, fromVer, toVer string, size int64) Plan`、`BackupPatterns(goos string) []string` |
| `script.go` | `go:embed` 脚本 + 参数拼装（**纯函数**） | `ScriptArgs(Plan, pid int, logPath string) []string`、`ScriptName(goos string) string`、`ScriptBytes(goos string) []byte` |
| `scripts/update.sh` | Linux 升级脚本（embed 资源，独立文件、可 shellcheck） | — |
| `scripts/update.cmd` | Windows 升级脚本（embed 资源，纯 cmd，不含 `powershell`） | — |
| `service.go` | 状态机 + 轮询 + 残留自检 + 事件发射 | 见 §5.2 |

### 5.2 Service 契约

```go
type Options struct {
    Goos, Goarch, Version, ExePath string
    Doer   Doer                                        // *http.Client；测试注入 httptest
    Launch func(script string, args []string) error     // 分离启动；测试注入假实现
    Emit   func(event string, payload any)              // update:state / update:progress
    Config func() Settings                              // 读配置（源、间隔、跳过版本），每次调用都重新读
    Save   func(patch Settings) error                   // 写配置（跳过/取消跳过版本时用），由 app.go 落到 TOML
    Now    func() time.Time
}

type Settings struct {
    Auto     bool
    Interval time.Duration
    Source   string
    Skipped  string
}

func New(opt Options) *Service
func (s *Service) Init(ctx context.Context)             // 启动自检（残留检测）+ 自动检查/轮询调度
func (s *Service) Shutdown()                            // 停止轮询与在飞请求（app.OnShutdown 调用）
func (s *Service) Reconfigure()                         // 重读配置并重建轮询定时器（SetSettings 后调用）
func (s *Service) Info() UpdateInfo                     // 当前快照，供 GetUpdateInfo 直接返回
func (s *Service) Check(ctx context.Context, manual bool) (UpdateInfo, error)
func (s *Service) StartDownload(ctx context.Context) error
func (s *Service) CancelDownload() bool
func (s *Service) ApplyAndRestart(ctx context.Context) error
func (s *Service) DiscardPending() error                // 「删除已下载的更新」
func (s *Service) SkipVersion(v string) error            // 「跳过此版本」：写 update_skipped_version
func (s *Service) ClearSkipped() error                  // 「取消跳过」
```

`UpdateInfo` 是前后端的唯一契约（JSON）：

```go
type UpdateInfo struct {
    Seq         int64  `json:"seq"`          // 单调递增；前端用它丢弃迟到的旧载荷（§12.2）
    State       string `json:"state"`        // §7 状态枚举
    Current     string `json:"current"`
    Latest      string `json:"latest"`
    Notes       string `json:"notes"`
    PublishedAt string `json:"published_at"`
    Source      string `json:"source"`
    Progress    int    `json:"progress"`     // 0-100；-1 表示未知总大小
    ReadyPath   string `json:"ready_path"`
    Skipped     bool   `json:"skipped"`
    Manual      bool   `json:"manual"`       // 最近一次检查是否手动触发（决定文案颗粒度）
    Error       string `json:"error"`
    PendingLog  string `json:"pending_log"`  // 存在 sshore-update.log 时的路径
}
```

### 5.3 `app.go` 绑定与事件

```go
func (a *App) GetUpdateInfo() update.UpdateInfo
func (a *App) CheckUpdate(manual bool) (update.UpdateInfo, error)
func (a *App) StartUpdateDownload() error
func (a *App) CancelUpdateDownload() bool
func (a *App) ApplyUpdateAndRestart() error
func (a *App) DiscardUpdateDownload() error  // 删除已下载的待安装文件
func (a *App) SkipUpdateVersion(version string) error
func (a *App) ClearSkippedUpdate() error
func (a *App) OpenReleasePage() error        // runtime.BrowserOpenURL(Repo + "/releases")
```

**接线（`app.go` / `main.go`）**：`App.Init`（`app.go:143`，与 `forward`/`sftp`/`sync` 同处）里 `update.New` 并 `Service.Init(ctx)`；`App.OnShutdown`（已被 `main.go:62` 的 `OnShutdown` 回调）里 `Service.Shutdown()`；`App.SetSettings`（`app.go:260`）保存成功后调 `Service.Reconfigure()`，让更新源/间隔/开关立即生效。

事件（沿用 `app.go:484` `runtime.EventsEmit` 的既有模式）：

| 事件 | 载荷 | 说明 |
|---|---|---|
| `update:state` | 完整 `UpdateInfo` | 状态发生变化时发一次 |
| `update:progress` | `{done, total, percent}` | 下载中按 **200ms 节流**（防事件风暴） |

设置项不新增绑定：4 个字段进 `AppSettings`，前端走既有 `GetSettings`/`SetSettings`。

### 5.4 前端文件

| 文件 | 变化 |
|---|---|
| `frontend/src/components/SettingsDialog.vue` | 「帮助」段下方挂载新的 `<UpdateSection>` |
| `frontend/src/components/UpdateSection.vue`（新） | 更新区全部 UI（§12） |
| `frontend/src/stores/update.js`（新） | `update:state`/`update:progress` 归约、角标计算、动作封装 |
| `frontend/src/utils/update.js`（新） | 纯函数：状态→文案、按钮可见性、字节/百分比格式化 |
| `frontend/src/stores/settings.js` | `AppSettings` 的 4 个新字段 + 保存时的字段列表 |
| `frontend/src/App.vue` | 侧栏「⚙ 设置」角标；`onMounted` 后取一次 `GetUpdateInfo` 快照填充 store（**前端不自己调度检查**，首查与轮询一律由后端 `Service.Init` 负责） |

---

## 6. 配置

`internal/config/store.go` 的 `AppSettings` 新增（TOML 键 / JSON 键同名）：

| 字段 | 类型 | 默认 | Normalize 规则 |
|---|---|---|---|
| `update_check_auto` | bool | `true` | 无 |
| `update_check_interval_hours` | int | `12` | `< 0` → 12；`0` → 0（显式关闭轮询）；`> 168` → 168 |
| `update_skipped_version` | string | `""` | 去首尾空白与 `v` 前缀后保留；含 `[0-9A-Za-z.\-+]` 之外的字符则清空 |
| `update_source` | string | `""` | `""` = 内置默认源；非 `http(s)://` 前缀 → 清空回退默认（并写日志 1 行） |

前端「设置」页新增控件：自动检查开关、间隔下拉（6h / 12h / 24h / 关闭）、更新源文本框（含「恢复默认」）。

设置保存（`SetSettings`）后由 `app.go` 调 `Service.Reconfigure()`：重读配置、按新间隔重建定时器；`update_skipped_version` 与 `update_source` 立即对下一次检查生效。

---

## 7. 状态机与数据流

### 7.1 状态枚举

`idle` · `checking` · `up-to-date` · `available` · `skipped` · `rate-limited` · `check-failed` · `no-asset` · `no-checksum` · `downloading` · `verify-failed` · `io-failed` · `not-writable` · `ready` · `applying`

合法迁移（守卫在 `Service` 内，全部持锁）：

```
idle/up-to-date/available/skipped/rate-limited/check-failed/no-asset/no-checksum
        --Check()--> checking --(结果)--> up-to-date | available | skipped | rate-limited | check-failed | no-asset | no-checksum
available --SkipVersion()--> skipped          skipped --ClearSkipped()--> idle（下一次 Check 重判）
available --StartDownload()--> [not-writable | downloading] --(成功)--> ready --ApplyAndRestart()--> applying
                                    |--(失败)--> verify-failed | io-failed | available(取消)
ready     --DiscardPending()--> available     applying 为终态（进程即将退出）
Init 残留自检：pending 存在 → ready（可直接「重试升级」，无需重新下载）
```

并发守卫（同一把锁，全部在 `Service` 内）：`checking`/`downloading` 时忽略同类新请求；`SkipVersion`/`ClearSkipped`/`DiscardPending` 在 `applying` 时一律拒绝；`ApplyAndRestart` 只允许从 `ready` 进入且不可重入。

### 7.2 阶段 A：检查

1. 门卫：`IsDev(Version) && !manual` → `skipped`（**不发请求**）；`!auto && !manual` → 同样不发。
2. `GET <source>/releases/latest`：超时 10s；`User-Agent: sshore/<version>`；`Accept: application/vnd.github+json`。
3. HTTP 映射：`200` → 解析；`429` → `rate-limited`；`403` **仅当** `X-RateLimit-Remaining: 0`（或响应体含 `rate limit`）才判 `rate-limited`，否则 `check-failed`（例如私有/不存在的仓库、被代理拦截）；其它状态码与 JSON 解析失败 → `check-failed`。`X-RateLimit-Remaining`/`Reset` 一律写进日志。
4. 比较：**`IsDev(Version)` 时不做比较短路**（`dev` 与语义化版本不可比），直接进入资产挑选并判 `available`；否则 `Compare(tag, Version) <= 0` → `up-to-date`。
5. 跳过：`Base(tag) == Base(update_skipped_version)` → `skipped`（`Skipped = true`；手动检查仍可见并可「取消跳过」）。
6. 资产：`PickArchive` + `PickChecksums` 任一失败 → `no-asset` / `no-checksum`。
7. 全部通过 → `available`（携带 `Notes`/`PublishedAt`）。
8. 调度：`Init` 后延迟 5s 首查（`auto=true`），此后每 `interval` 一次；`Shutdown` 停止。`checking`/`downloading` 期间不叠加触发。

### 7.3 阶段 B-1：下载与就绪

1. 可写性探测：在 `ExeDir` 写一个 `.sshore-write-test-<rand>` 并删除；失败 → `not-writable`（文案给人工升级步骤）。
2. 下载 `PickArchive` 的 `browser_download_url` 到 `os.TempDir()/sshore-update-<pid>.part`，边下边算 SHA256，进度 200ms 节流。
3. 取 `checksums.txt`（`PickChecksums` 的 URL）→ `ParseChecksums` → 比对压缩包哈希：不符 → 删临时文件、`verify-failed`（日志记期望/实际前 12 位），**绝不向程序目录写任何东西**。
4. 通过 → `ExtractBinary` 出二进制到 `<ExeDir>/sshore.<新版本号>[.exe]`（同盘，保证 rename 不跨文件系统）；记录 `Size`。
5. 删除临时压缩包；状态 `ready`（`ReadyPath` 指向待安装文件）。
6. `CancelDownload`：ctx 取消 → 删除 `.part` → 回 `available`。**不做断点续传**。

### 7.4 阶段 B-2：应用（`ApplyAndRestart`）

1. 前置：状态必须为 `ready`，待安装文件存在且大小等于记录值；否则返回错误（不改变状态）。
2. `--size` 取待安装文件的**当前 stat 值**（进程内下载时记录的值只用于同一进程内的完整性判断；重启后重试时以磁盘为准）。
3. 生成脚本：`Goos` 选 embed 资源写出为 `<ExeDir>/sshore-update.sh|.cmd`（Linux 0755）；写失败 → 状态 `io-failed`，不启动、不退出。
4. 拼 argv（见 §8.2）。
5. 分离启动：Linux `exec.Cmd` + `SysProcAttr{Setsid: true}`（不依赖系统 `setsid` 二进制）；Windows `exec.Command("cmd", "/c", scriptPath)` + `SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}` —— 两侧都不依赖父进程存活。
6. 状态置 `applying`，发一次 `update:state`（前端显示「正在重启…」）。
7. `runtime.Quit(ctx)`。

时序（Windows 为例）：

```
用户在设置页点「重启并升级」
  → Go: 校验 → 写脚本 → cmd /c 分离启动 → runtime.Quit
  → 脚本: 等旧 PID 退出（tasklist|find 轮询，上限 --wait 默认 60s）
        → 自检 pending 存在且大小匹配
        → 清理更早备份（只留最近一份）
        → sshore.exe → sshore.v0.6.0.exe
        → sshore.v0.7.0.exe → sshore.exe
        → start "" "sshore.exe"
        → del "%~f0"（成功自删；失败写 sshore-update.log）
  → 新版窗口标题显示 "SSHore v0.7.0"
```

### 7.5 残留自检（`Init`）

`Init` 扫描程序目录（只读一次 stat，不做删除），按以下三条判定：

| 磁盘事实 | 处理 |
|---|---|
| 存在 pending 文件（匹配 `sshore.<版本号>[.exe]` 且版本 ≠ 当前版本、名字不在备份模式内） | 状态置 **`ready`**：可直接「重试升级」（脚本重新生成、`--size` 取磁盘实际值），并把 `PendingLog` 填成日志路径（若存在） |
| 存在 `sshore-update.log` 但**无** pending 文件 | 判为**陈旧日志**：写一行日志面板、删除该文件，**不打扰用户**（避免「上次升级未完成」误报） |
| 存在备份文件（`sshore.v*`/`sshore.dev-*`，且非 pending） | 仅记录到日志面板（「上一版本备份：<路径>」），不影响状态 |

脚本成功路径会自删脚本与日志（§8.3 第 9 步），所以「日志 + pending 同时存在」才是真正的中断升级。

---

## 8. 脚本契约

### 8.1 生成与生命周期

- 脚本由 **`go:embed` 静态文件**提供（`internal/update/scripts/update.sh`、`update.cmd`），按 `GOOS` 选一份写出；**不运行时拼接脚本文本**。
- **换行符必须钉死**：`go:embed` 原样嵌入工作区文件的字节，但本仓库当前**没有 `.gitattributes`**（实测 `git ls-files --eol` 全部为 `i/lf w/lf`、`core.autocrlf=false`）。新增 `.gitattributes`：`*.sh text eol=lf`、`*.cmd text eol=crlf` —— 否则 Windows runner 上若检出 CRLF，嵌进去的 `update.sh` 会带 `\r`，Linux 用户执行时报 `#!/bin/sh\r: not found`。
- 写在程序目录，名 `sshore-update.sh|.cmd`：与待安装文件同盘、用户可读可手动重跑。
- 成功 → 自删（Linux `rm -f "$0"`；Windows `del "%~f0"`，若因自身占用失败则留下无害残留并记日志）。
- 失败 → **保留**脚本与待安装文件，把步骤号与原因追加到 `<ExeDir>/sshore-update.log`。

### 8.2 参数（argv，零 shell 插值）

```
--pid <旧进程 PID> --target <正式二进制绝对路径> --pending <待安装文件绝对路径>
--backup <备份绝对路径> --size <待安装文件字节数> --log <日志绝对路径>
--wait <等待旧进程退出的秒数，默认 60>
```

缺失、非数字、路径不存在 → 立即写日志并以非 0 退出（不猜、不兜底）。

### 8.3 步骤（两平台同构）

| # | 步骤 | Linux | Windows |
|---|---|---|---|
| 1 | 解析 argv 并校验 | `case` 循环 | `%~1` 解析 |
| 2 | 等旧 PID 退出（上限 `--wait`，默认 60s） | `kill -0` 轮询（0.2s） | `tasklist /FI "PID eq %PID%" | find "%PID%"` 轮询（1s） |
| 3 | 自检待安装文件存在 + 大小一致 | `[ -f "$PENDING" ]` + `wc -c < "$PENDING"` | `if exist "%PENDING%"` + `for %%A in ("%PENDING%") do set PEND_SIZE=%%~zA`（`%~z` 只对批处理参数/for 变量生效，不能直接写 `%~zVAR%`） |
| 4 | 清理更早备份（只留最近一份） | 遍历 `sshore.v*` / `sshore.dev-*`，排除 pending 与本次 backup | `for %%f in (...) do if /i not "%%f"=="..."` 同规则 |
| 5 | 旧二进制改名备份 | `mv -f` | `move /y` |
| 6 | 待安装文件改名正式名 | `mv -f` | `move /y` |
| 7 | 可执行位 | `chmod +x` | 不适用 |
| 8 | 启动新版 | 优先 `setsid "$TARGET" >/dev/null 2>&1 &`，无 `setsid` 时退化为 `nohup "$TARGET" >/dev/null 2>&1 &` | `start "" "%TARGET%"` |
| 9 | 收尾（成功） | `rm -f "$0"`（脚本自删）+ 删除 `sshore-update.log`（若存在） | `del "%~f0"` + `del "%LOG%"`（失败则留日志） |

**明确的禁令**（同时是测试断言）：脚本内**不得出现** `powershell`、`curl`、`wget`、`certutil`、网络动作或 SH`eval`。第 5 步「改名运行中的 exe」在 Windows 上是被允许的（F3），等待退出只是为了避免新旧实例短暂并存。

**失败回滚**：第 6 步失败时，若目标名已空，把备份改回正式名后再退出（避免「应用消失」）。

### 8.4 文件命名与备份契约

| 名字 | 规则 |
|---|---|
| 正式二进制 | `sshore`（Windows `sshore.exe`） |
| 待安装文件 | `sshore.<目标版本号>[.exe]`，如 `sshore.v0.7.0` |
| 备份 | `sshore.<当前版本号>[.exe]`，如 `sshore.v0.6.0` |
| `dev` 构建的备份名 | `sshore.dev-<YYYYMMDD-HHMMSS>[.exe]` |
| 备份保留策略 | **只保留最近一份**；脚本第 4 步清理 `sshore.v*` 与 `sshore.dev-*` |
| 升级脚本 | `sshore-update.sh` / `sshore-update.cmd` |
| 升级日志 | `sshore-update.log` |
| 下载临时文件 | `os.TempDir()/sshore-update-<pid>.part`，校验完成即删 |

---

## 9. 错误处理与边界

| 情形 | 行为 |
|---|---|
| 自动检查失败（无网/超时/5xx/坏 JSON） | 静默，仅日志面板 1 行；角标不亮 |
| 手动检查失败 | 文案含原因 + 「打开发布页」；不自动重试、不重试退避堆叠 |
| 403 / 429 限流 | `rate-limited`：「更新源限流，请稍后再试或手动下载」 |
| 该 Release 无 `checksums.txt` | `no-checksum`：**拒绝自动升级**（安全优先）+ 发布页兜底 |
| 无本平台资产（arm64/386/改名） | `no-asset`：「本平台暂无可用包」+ 发布页 |
| `dev` 构建 | 不自动检查；手动检查照常显示最新版并允许升级，备份名 `sshore.dev-<ts>` |
| `ExeDir` 不可写（`/usr/bin`、Program Files） | `not-writable`：给人工升级步骤，不尝试替换 |
| 下载中取消/中断 | 删 `.part`、回 `available`；断点续传不做 |
| SHA256 不符 | `verify-failed`：删临时文件、日志记期望/实际前 12 位、程序目录零写入 |
| 解包/落盘 IO 失败 | `io-failed`：带原始错误文本 |
| 脚本失败（占用/权限/AV 拦截） | `sshore-update.log` 与待安装文件都保留；下次启动由残留自检进入 `ready`，界面给「重试升级」+ 日志路径 |
| 跳过版本 | 自动检查静默；手动可见 + 「取消跳过」；**下载新版本时清空该字段** |
| 重启后仍存在待安装文件 | 残留自检置 `ready`，界面给「重试升级」（不必重新下载） |
| 生成脚本时目录变只读 | `io-failed`：不启动脚本、不退出应用，文案含原因 |
| 重复点击 / 并发 | 状态机守卫；`downloading` 期间按钮禁用；`applying` 只允许一次 |
| 更新源配置非法 | 回退内置默认源并写日志 1 行 |
| 代理 | 用 Go 默认传输（自动读 `HTTP_PROXY`/`HTTPS_PROXY`），不新增设置项 |
| 隐私 | 检查请求会向更新源发出带版本号 UA 的请求；自动检查默认开，README 与设置页各写一句、且可关 |

---

## 10. 安全、隐私与 AV

### 10.1 完整性

- 权威校验发生在 **Go 侧对下载的压缩包**做 SHA256 比对（`checksums.txt` 来自同一 Release 的 assets）。
- 脚本**不做**哈希二次校验（避免 Windows 依赖 `certutil`）：只做「存在 + 大小一致」的廉价自检。
- 校验失败一律拒绝安装，且程序目录不留任何残留。

### 10.2 传输

- 默认源为 `https://`；自定义源允许 `http://`（内网镜像、离线环境、端到端测试的本地假源都需要它），**不强制**，但设置页在非 https 时给一行风险提示。
- 下载与校验都在临时目录完成，校验通过才落程序目录。

### 10.3 AV 与签名（已知风险，如实记录）

- 自升级天然带上三条行为特征：联网下载可执行文件并运行、重命名运行中的 exe、脚本操弄 PE 文件。我们是**未签名、零声誉**的 Go 二进制，这是最不利组合。
- UPX 打包本身已有误报记载（README）；本次不改默认值。
- 缓解：脚本最小化 + 不调 PowerShell；Defender 真机验证进验收；README 说明；**根因解法是代码签名**（SignPath Foundation 对 OSS 免费 / Azure Trusted Signing 等，资格与价格以官方为准，本次不实施）。
- 对 360、火绒等第三方 AV **不做保证**。

### 10.4 预案：切 helper 模式

若 Windows Defender 真机验证出现拦截/隔离，或 F3 不成立，则把「应用」阶段改为 `sshore --apply-update --pid/…`（由已校验哈希的待安装二进制分离启动，自己完成等待/备份/占位/启动），删除脚本路径。该变动只影响 §5.2 的 `ApplyAndRestart`、§5.1 的 `script.go`/`scripts/*` 与 `main.go` 的一个早期分支，其余设计不变。

---

## 11. CI 变更

**11.1 脚本静态检查**：在既有的 Linux 测试步骤里加一行 `shellcheck internal/update/scripts/update.sh`（runner 未预装时先 `sudo apt-get install -y shellcheck`）。本地实测 `shellcheck` 未安装，所以这条必须有 CI 兜底，否则 §14 的 DoD 只能靠人工。

**11.2 换行符**：新增的 `.gitattributes`（见 §8.1）同时覆盖 CI：Windows runner 上 `update.sh` 仍以 LF 检出、`update.cmd` 以 CRLF 检出。

**11.3 发布校验文件**：`build-linux` / `build-windows` **不改**。`release` job 已经下载全部 artifacts，新增一步：对 artifacts 里的 `*.tar.gz`/`*.zip` 在 **Linux runner** 上统一算哈希，生成只含文件名的 `checksums.txt`：

```bash
cd artifacts
find . -type f \( -name '*.tar.gz' -o -name '*.zip' \) -print0 \
  | sort -z \
  | xargs -0 sha256sum \
  | awk '{ n=$2; sub(/^.*\//, "", n); print $1"  "n }' > checksums.txt
cat checksums.txt
```

格式即标准 `sha256sum` 输出的 `<hash>  <文件名>`（只留文件名，供应用按 assets 名比对）。随后：

- 把既有 `mapfile -t FILES < <(find artifacts …)` 的匹配条件补上 `-o -name 'checksums.txt'`，让校验文件与压缩包一起走 `gh release create` 与 `gh release upload --clobber` 两条路径；
- `if [ "${#FILES[@]}" -eq 0 ]` 的兜底判断不变。

这样避免 Windows `Get-FileHash` 的格式转换与两份 checksums 的合并逻辑。**实施期必须验证**：生成的 `checksums.txt` 与本地对同一压缩包 `sha256sum` 的结果逐字节一致（含换行风格）。

---

## 12. UI 设计

### 12.1 设置 → 更新区（`UpdateSection.vue`）

| 元素 | 显示条件 | 内容 |
|---|---|---|
| 当前版本 | 常显 | `当前版本 v0.6.0`（`dev` 时显示 `开发构建（dev）`） |
| 状态行 | 常显 | 由 `utils/update.js` 的状态→文案表生成（§9 的文案） |
| 更新说明 | `available`/`ready` 且有 `notes` | 折叠展示 `published_at` + `body`（截断到 2000 字符） |
| 进度条 | `downloading` | `percent`（总大小未知时显示 indeterminate + 已下载字节） |
| 按钮 | 见下 | 每个状态只给必要动作 |
| 设置控件 | 常显 | 自动检查开关、间隔下拉、更新源输入框 + 恢复默认 |

按钮矩阵：

| 状态 | 可用动作 |
|---|---|
| `idle`/`up-to-date`/`check-failed`/`rate-limited`/`no-asset`/`no-checksum` | 检查更新 · 打开发布页 |
| `available` | 下载更新 · 跳过此版本（写 `update_skipped_version`）· 检查更新 · 打开发布页 |
| `skipped` | 取消跳过（清 `update_skipped_version` 并立刻重查）· 检查更新 · 打开发布页 |
| `downloading` | 取消下载 |
| `ready` | **重启并升级** · 删除已下载的更新 · 打开发布页 |
| `verify-failed`/`io-failed` | 重试（重新下载）· 打开发布页 |
| `not-writable` | **不给重试**：只给人工升级步骤 + 打开发布页 |
| `applying` | 全部禁用，显示「正在重启…」 |

### 12.2 侧栏角标

- `App.vue` 的「⚙ 设置」按钮右上角一个小圆点，条件只有一条：`state ∈ {available, ready}`。被用户跳过的版本状态是 `skipped`（不是 `available`），因此天然不亮角标。
- `store.acknowledge()`（打开设置对话框时调用）记录**已确认的版本号**，不是布尔开关：只要 `latest` 变了（换了新版本）或状态从 `available` 走到 `ready`，角标重新亮。仅内存态，不落配置。
- **竞态**：store 先注册事件监听再取 `GetUpdateInfo()` 快照，并按 `UpdateInfo.seq` 单调递增丢弃迟到的旧载荷（`seq` 小于已应用值的事件直接忽略），避免「快照覆盖新事件」或反之。
- 单一来源：角标只读 `stores/update.js` 的派生值，不额外拉取。

### 12.3 文案与语言

全部中文，与现有界面一致；错误文案遵循「说清发生了什么 + 给下一步动作」，不暴露堆栈。

---

## 13. 测试策略

### 13.1 Go 单测（`internal/update`，零真实网络、零真实进程）

| 目标 | 用例要点 |
|---|---|
| `version` | `v0.7.0 > v0.6.0`、相等、`v0.7.0-3-gabcd`、`0.10.0 > 0.9.0`（十进制而非字典序）、`dev`、`""`、非 semver 串 |
| `source` | `httptest` 返回 200/403/429/500/坏 JSON/空 assets → 状态映射正确；不触网 |
| `asset` | linux/amd64、windows/amd64 命中；386/arm64 缺失 → 错误；`checksums.txt` 缺失 → 错误 |
| `checksum` | `sha256sum` 格式（含 `*` 二进制前缀、CRLF、空行、缺项）、哈希不符 |
| `download` | 分块响应下的进度回调单调、200ms 节流、ctx 取消删 `.part`、哈希边下边算正确 |
| `extract` | 内存构造 `tar.gz`/`zip` fixture：只取目标文件；**含 `../` 条目的包必须被拒绝** |
| `plan`/`script` | 计划字段正确；脚本参数拼装逐项断言；**脚本文本不含 `powershell`/`curl`/`wget`/`certutil`** |
| `service` | 状态机守卫（重复检查、重复下载、未就绪 apply、`applying` 终态、`applying` 期间拒绝 discard/skip）；跳过版本比对；`dev` 门卫不发请求（用假 doer 断言零调用）；`dev` 手动检查**不做版本比较短路**直接 `available`；`403 + X-RateLimit-Remaining: 0` → `rate-limited`、`403` 无该头 → `check-failed`；`Seq` 单调递增；`Reconfigure` 换间隔后定时器重建（注入假时钟） |
| `app.go` | `CheckUpdate(manual)` 的门卫表测（dev / auto=false / skipped）；`GetUpdateInfo` 快照一致性 |

### 13.2 脚本真跑（Linux CI 可做）

临时目录构造：假「已安装」二进制（内容 `#!/bin/sh echo OLD`）、待安装文件（`#!/bin/sh echo NEW`，大小已知）、假日志路径；用**一个立即退出的子进程 PID**（成功路径）与**一个长时间存活的子进程 PID + `--wait 1`**（超时路径）各跑一次生成的 `update.sh`，断言：

1. 备份文件名 = 传入的 `--backup`；
2. 正式名内容是 `NEW`；
3. 更早的备份被清理，只留一份；
4. 自检失败（大小不符）时**不替换**且退出码非 0；
5. 成功路径下脚本自删；失败路径下脚本与日志保留。

Windows `.cmd` 的行为只能在 Windows VM 上验（§13.4）。

### 13.3 前端 vitest（沿用现有风格：无 jsdom、纯函数 + SSR）

| 目标 | 用例要点 |
|---|---|
| `utils/update.js` | 状态→文案表全枚举覆盖；按钮矩阵（每个状态可用动作集合）；字节/百分比格式化；`notes` 截断 |
| `stores/update.js` | `update:state`/`update:progress` 归约；角标派生；`acknowledge()` 后角标消失、**版本变化后重新亮**；`skipped` 不亮角标；**`seq` 更小的迟到事件被丢弃**；快照与事件竞态（先订阅后快照） |
| `updateWiring.test.js`（结构性不变量，仿 `sftpDropWiring.test.js`） | 自动检查默认值来自后端（前端无硬编码 `true`）；角标单一来源；`applying` 时所有按钮禁用；`ready` 才出现「重启并升级」；设置页保存包含 4 个新字段 |
| `stores/settings.test.js`（扩展） | 新字段读写与保存 |

### 13.4 可复现的端到端自升级（关键设计点）

让应用把 `update_source` 指向**本地假源**（`http://127.0.0.1:<port>`，用 `httptest` 或一次性静态服务器），源里放：

- `releases/latest` 的 JSON（`tag_name` = 现场构建的更高版本号，`assets[].browser_download_url` 指向本机 HTTP 上的产物与 `checksums.txt`）；注意源 base 就填 `http://127.0.0.1:<port>`，代码按 `<source>/releases/latest` 拼路径，因此假源只需提供这一个路径与资产文件；
- 用**当前树现场构建**的目标平台产物 + **对应的 `checksums.txt`**。

自动化链路：写配置 → 启动应用 → 等 `available` → 触发下载 → 等 `ready` → 触发「重启并升级」→ 等新进程起来 → 断言窗口标题/版本显示为新版本号、备份文件命名正确且只有一份、无残留脚本、日志无错误。

**触发方式**：一律通过界面合成点击（Linux 用 XTEST 夹具、Windows 用 `SendInput` 绝对坐标），**不为测试在生产代码里加开关**；若某平台合成点击在模态对话框里不可靠，则退化为「人工点一次 + 自动化只验证前后的状态、磁盘与进程结果」，并在报告中如实记录哪一步是人工的。

- **Linux**：Xvfb 桌面（沿用现有 XTEST 夹具与一次性 sshd 环境）。
- **Windows**：VirtualBox 客户机（Session 1 交互式）；**同时采集 Defender 证据**：`Get-MpPreference`（实时/云保护状态）、`Get-MpThreatDetection`、`Microsoft-Windows-Windows Defender/Operational` 事件日志、`MpCmdRun.exe -Scan -ScanType 3 -File <待安装文件>` 与最终二进制的扫描结果。
- 在报告里明确记录「第三方 AV（360/火绒）无法验证」这一限制。

### 13.5 变体验证

| 变体 | 期望 |
|---|---|
| `update_skipped_version` = 最新版 | 自动检查静默、角标不亮；手动检查显示 `skipped` + 「取消跳过」 |
| Release 缺 `checksums.txt` | `no-checksum`，无任何下载/写入程序目录 |
| assets 里没有本平台包 | `no-asset` + 发布页兜底 |
| `checksums.txt` 里的哈希改一位 | `verify-failed`，程序目录零残留 |
| `ExeDir` 置只读 | `not-writable`，不尝试下载/替换 |
| 脚本人为失败（pending 大小不符） | 保留脚本 + 日志，下次启动进入 `ready` 并给「重试升级」 |
| 成功升级后再次启动 | 无残留脚本、无日志；旧备份只剩一份 |

---

## 14. 验收清单（DoD）

1. `make ci` 全绿（`go vet` + `go test -race` + `npx vitest run`；现有 187 个前端测试不回归）。
2. §13.4 的端到端自升级在 **Linux** 与 **Windows VM** 各成功一次，证据含：新版本号显示、备份命名、只留一份备份、无残留脚本、日志无错误；**并额外验证「中断后重试」路径**（人为让脚本失败一次 → 下次启动处于 `ready` → 重试成功）。
3. Windows Defender 全程无拦截/隔离（有告警则按 §10.4 切 helper 模式并重跑）。
4. §13.5 的 7 个变体全部验证。
5. `internal/update/scripts/update.sh` 通过 CI 里的 `shellcheck`（§11.1），且单元测试断言两个脚本都不含 `powershell`/`curl`/`wget`/`certutil`；新增 `.gitattributes` 后 `git ls-files --eol` 显示 `update.sh` 为 `i/lf w/lf`。
6. README 增「更新与升级」一节：更新源与可配置性、SHA256 校验、隐私说明（检查请求）、人工升级步骤（Windows/Linux）、AV 注意事项与 `COMPRESS=0` 的既有退路。
7. 所有新增测试做过变异校验（至少覆盖：状态门卫、校验失败拒装、备份只留一份、脚本禁令断言、角标条件），变异必须被杀死。

---

## 15. 风险与缓解

| # | 风险 | 缓解 |
|---|---|---|
| R1 | AV/Defender 拦截自升级（行为启发式 + 未签名零声誉） | 脚本最小化、不调 PowerShell、Defender 真机证据进验收、README 说明、必要时切 helper 模式；根因解法是代码签名（另议） |
| R2 | API 限流导致检查失败 | 默认 12h 间隔 + `rate-limited` 明确文案 + 发布页兜底；日志记录 `X-RateLimit-*` |
| R3 | 自定义镜像不反代 API 路径 | 设置页与 README 明确「更新源需提供 GitHub 兼容 API」；失败即 `check-failed`（不猜） |
| R4 | F3（Windows 重命名运行中 exe）不成立 | 前置 spike 验证；不成立则切 helper 模式 |
| R5 | 安装目录只读 | `not-writable` 提前拦截，不产生半状态 |
| R6 | 脚本中途失败留下半状态 | 步骤 6 回滚 + 日志 + 下次启动「上次升级未完成」提示 |
| R7 | 新旧实例短暂并存 | 脚本先等旧 PID 退出（上限 `--wait`，默认 60s） |
| R8 | `dev` 构建被误升级为 Release | 允许但备份名带时间戳；设置页明确显示「开发构建」 |
| R9 | 误报 UPX（既有问题） | 本次不改默认值；README 保留 `COMPRESS=0` 退路；记录为独立决策 |

---

## 16. 决策记录

| # | 问题 | 结论 |
|---|---|---|
| 1 | 做到哪一步 | **C**：一份 spec 覆盖 A（检测提示）+ B（下载自升级），实施分两步 |
| 2 | 更新源 | **B**：默认 GitHub，更新源 base URL 可覆盖（最终形态：API JSON，方案 1） |
| 3 | 完整性校验 | **B**：发布流程补 `checksums.txt`（SHA256），比对失败拒绝安装 |
| 4 | 检查时机 | **C**：启动后自动 + 手动 + 按间隔轮询（默认 12h） |
| 5 | 提示形态 | **B**：设置页「更新」区 + 侧栏角标，不做全局横幅 |
| 6 | 安装触发 | **B**：只下载不自动重启；后补充为「显式点击后立即执行脚本」 |
| 7 | 脚本机制 | **②**：`go:embed` 静态脚本 + argv 参数；Windows 纯 cmd，不调 PowerShell（AV 考量） |
| 8 | 命名与备份 | 读法 1：待安装文件带**目标版本号**；旧二进制备份带**当前版本号**；**只保留最近一份**备份 |
| 9 | 边界 | `dev` 不自动检查（手动可查可升级）；无平台资产时明确告知 + 发布页兜底 |
| 10 | UPX | 本次不改默认值（独立决策，见 R9） |
| — | 修正 A | 脚本内不做 SHA256 二次校验，只做「存在 + 大小」自检（避免 `certutil` 与额外 AV 特征） |
| — | 修正 B | CI 只改 `release` job：在 Linux runner 上对 artifacts 统一生成 `checksums.txt` |

### 16.1 第一阶段自审（本地）修正清单

| # | 问题 | 修正 |
|---|---|---|
| 1 | `dev` 版本与语义化版本不可比，但 §7.2 直接用 `Compare(tag, Version)` 短路 `up-to-date`，会让 dev 构建永远是「已是最新」 | 明确：`IsDev(Version)` 时不做比较短路，直接进资产挑选判 `available` |
| 2 | 缺「跳过此版本 / 取消跳过 / 删除已下载」的绑定与写配置通路 | `Options.Save`、`Service.SkipVersion/ClearSkipped`、`App.SkipUpdateVersion/ClearSkippedUpdate/DiscardUpdateDownload` 三个绑定，§3.1 计数同步为 8+1 |
| 3 | 设置改了更新源/间隔后不会生效（定时器不重建） | 新增 `Service.Reconfigure()`，由 `App.SetSettings` 保存后调用；并写明 `App.Init`/`App.OnShutdown` 的接线位置 |
| 4 | 前端「先取快照后订阅事件」存在覆盖竞态 | `UpdateInfo` 增加单调递增 `Seq`，store 丢弃迟到载荷；先订阅再取快照 |
| 5 | 角标 `acknowledge()` 语义含糊（布尔清除会让新版本也不再提示） | 改为记录「已确认的版本号」；新版本或状态变化重新亮 |
| 6 | 残留自检把「陈旧日志」也判成「上次升级未完成」，会误报 | 三分支表：有 pending → `ready` 可重试；只有日志无 pending → 判陈旧、清日志、仅记日志面板；有备份 → 只记录 |
| 7 | 成功升级后日志文件不会被清理，导致下次启动误判 | §8.3 第 9 步：成功时同时删除日志 |
| 8 | §9 声称「仅 https」，与「自定义源允许 http」以及端到端假源（`http://127.0.0.1`）自相矛盾 | 改为默认 https、自定义允许 http 并给风险提示 |
| 9 | Windows 用 `%~zVAR%` 取文件大小是错的（`%~z` 只对批处理参数/for 变量生效） | 改为 `for %%A in ("%PENDING%") do set PEND_SIZE=%%~zA` |
| 10 | Linux 侧依赖系统 `setsid` 二进制，缺失则静默失败 | Go 侧改用 `SysProcAttr{Setsid: true}`；脚本内启动新版时 `setsid` 缺失则退化 `nohup` |
| 11 | Windows 分离启动只写了 `HideWindow`，未说明子进程独立于父进程 | 补 `CreationFlags: CREATE_NO_WINDOW` 与「两侧都不依赖父进程存活」 |
| 12 | `403` 一律判 `rate-limited`，但私有/不存在/被代理拦截也是 403 | 仅当 `X-RateLimit-Remaining: 0`（或响应体含 rate limit）才判限流，否则 `check-failed` |
| 13 | `ApplyAndRestart` 的 `--size` 来源在「重启后重试」路径上不自洽 | 明确 `--size` 取磁盘 stat 值；进程内记录值仅用于同进程完整性判断 |
| 14 | 生成脚本写盘失败没有对应状态 | 归入 `io-failed`：不启动脚本、不退出应用 |
| 15 | `Plan` 里的 `Launch` 字段与 `Target` 重复 | 删除该字段，补 `BackupPatterns` |
| 16 | 单测/前端测试/DoD 未覆盖新增的守卫与竞态，且 §14 编号重复 | 补 dev 短路、403 判据、`Seq`、`Reconfigure`、重试路径与 shellcheck 条目；修正编号与变体计数 |

---

## 17. 后续（本期之外）

- 代码签名（SignPath Foundation / Azure Trusted Signing 等，资格与价格以官方为准）。
- 升级后会话恢复（隧道/SFTP/同步重放）。
- 断点续传与增量更新。
- 备份的回滚界面（把「最近一份备份」变成一键回滚）。
- UPX 默认值调整讨论（体积 vs 误报）。
