# 检测更新与自升级（GitHub Release 源 + SHA256 校验 + 脚本替换）设计

> 状态：v3 · 用户已逐节确认（§16 决策记录）· 两阶段审核已完成：第一阶段自审（§16.1，16 项）+ 第二阶段干净 subagent 双路评审（§16.2，8 Blocking / 16 Important / 9 Minor 全部处置）· **待用户评审** · 尚未实施
> 关联：`docs/superpowers/specs/2026-08-25-sshkit-design.md`（应用定位与设置项基线）
> 关联：`README.md` §下载安装 / §尺寸优化（UPX 误报的既有记载）
> 本期范围：**阶段 A** = 检查更新与提示；**阶段 B** = 应用内下载 + SHA256 校验 + 用户显式触发的一次性升级脚本
> 证据来源：§1.1 的代码事实（均带 `file:line`，两轮评审逐条核对过）、§4 的待复核事实清单（F1–F8，其中 F1/F5/F6/F7/F8 已实测）

---

## 1. 背景

### 1.1 现状（代码事实）

| 位置 | 事实 |
|---|---|
| `app.go:50-51` | `Version = "dev"`、`Repo = "https://github.com/i2534/sshore"`，由 `-X main.Version=...` 注入 |
| `Makefile:22-23` | `VERSION := $(shell git describe --tags 2>/dev/null || echo dev)`、`LDFLAGS := -s -w -X main.Version=$(VERSION)`。注意**无 `--always`**：实测当前 HEAD 产出 `v0.6.0-80-gc2d2a36`（不是 tag，也不是 `dev`） |
| `app.go:231` `GetAppInfo()` | 唯一对外暴露版本的地方：`AppInfo{Name, Version, Repo}` |
| `frontend/src/components/SettingsDialog.vue:97-105` | 「帮助」段只展示 应用 · 版本 · 仓库 与在线文档链接，**没有任何动作按钮** |
| `frontend/src/App.vue` | 侧栏四个按钮（:60-63）；全局错误条 `.fatal`（:66）；`onMounted` 里 `GetAppInfo()` 设置 HTML 标题（:37） |
| `internal/config/store.go:17-26` | `AppSettings` 现有 7 字段：auto_reconnect_default / sftp_transport / theme / font_scale / latin_font / cjk_font / auto_start_on_launch |
| `internal/config/store.go:156-166` | `AppConfig{App, Tunnels, RecentSFTP, Syncs, Bookmarks, LocalRecent, RemoteRecent, LegacyMigrated}`，TOML |
| `app.go:251` / `app.go:260` | `GetSettings()` / `SetSettings(AppSettings)` 已存在 —— 新设置项复用这两个绑定即可 |
| `app.go:90` / `app.go:143` | `startup(ctx)`（有 ctx、赋值 a.ctx）/ `Init(emit)`（**无 ctx**，且被 **24 处**测试直接调用） |
| `app.go:1044` / `main.go:35,62-63` | `App.OnShutdown()` 由 Wails 的 OnShutdown 回调；OnStartup 先调 app.startup(ctx) 再调 app.Init(emit) |
| `app.go:484` | 进度事件先例：`runtime.EventsEmit(a.ctx, progressEventName, ...)` |
| 一方源码 | **零 `net/http` 引用**（依赖图里已有 net/http：Wails 资产服务与 x/net）→ 更新检查是应用第一次主动对外发起 HTTP |
| `frontend/wailsjs/go/main/App.{js,d.ts}`、`models.ts` | **被 git 跟踪的生成物**（文件头写着 DO NOT EDIT），而所有构建路径都带 `-skipbindings`（Makefile:49/57/63、.github/workflows/ci.yml:126）→ 新增绑定必须重新生成并提交 |
| `.github/workflows/ci.yml` | build-linux / build-windows 各自打包 sshore-<v>-<os>-amd64.{tar.gz,zip}；另有 **go-windows** job 在 Windows runner 上跑 go vet/go test（windows-only 文件只在这里被编译）；release job 下载全部 artifacts 后 gh release create/upload，**只上传 *.tar.gz/*.zip，无校验文件** |
| `Makefile:25-28` | `COMPRESS ?= 1`，默认 UPX 压缩；README 已记载「杀毒软件误报 UPX 打包的 Go 二进制」并给 COMPRESS=0 退路 |
| `internal/sync/transfer.go:20-26` / `:49` | 工具函数 PartSuffix（20-26）与真正的原子替换 `os.Rename(tmp, target)`（49）—— 仓库内既有的「先写临时名再 rename」先例 |
| 仓库根 | **没有 `.gitattributes`**；`git ls-files --eol` 全部 i/lf w/lf、core.autocrlf=false |

### 1.2 缺口

1. 用户不知道有新版本：Release 更新了，应用内无任何提示（GetAppInfo 只读本地注入值）。
2. 拿到新版本全靠用户自己去 GitHub Releases 找包、解压、替换二进制 —— 对 Windows 用户尤其别扭（要退出、改名、再改名）。
3. 仓库有完整的产物命名与发布流水线，但**没有校验文件**；补上它只能覆盖「传输损坏 / 资产错发」，**不等于安全闭环**（信任边界见 §10.1）。

### 1.3 先考虑过、但未采纳的路

- **静默自动安装（不点确认）**：会被隧道/同步被切断的意外打断，且放大 AV 特征；用户明确选择「显式触发」。
- **helper 模式（sshore --apply-update 自替换，不开脚本）**：纯 Go、无解释器链；但用户选择要一份可审阅、可手动重跑的脚本。它**只能规避「脚本被 AV 拦截」**，不能规避 F3（§10.4 已修正）。
- **纯静态规则源（302 取 tag + 命名推导，不解析 JSON）**：零 API 配额，但拿不到更新说明/发布时间，且把产物命名变成对外契约；用户选择统一走 API JSON。
- **不校验完整性**：与「自动下载二进制并替换自身」组合风险过高，用户选择 SHA256 校验 + 发布流程补 checksums.txt。
- **前端 JS 侧实现下载**：webview 内无法可靠落盘到程序目录、无法做进程替换，全部逻辑必须在 Go 侧。

---

## 2. 决策摘要

**两阶段（一份 spec 覆盖，实施分两步交付）**

- **阶段 A（可独立交付）**：检查更新 + 提示（自动检查、轮询、角标、设置页更新区、发布页兜底）。
- **阶段 B（在 A 之上）**：应用内下载 → SHA256 校验 → 解包待安装文件 → 用户点「重启并升级」→ 分离启动升级脚本 → 应用退出 → 脚本替换并启动新版。

**澄清结论（逐条经用户确认，详见 §16）**

1. 更新源：**GitHub API JSON**，默认 https://api.github.com/repos/i2534/sshore，可在设置里覆盖。
2. 完整性：发布流程补 checksums.txt（SHA256），应用下载后比对，**不符即拒绝安装**。
3. 时机：启动后延迟 5s 自动检查一次 + 手动按钮 + 后台按间隔轮询（默认 12h，0 = 关闭轮询）。
4. 提示：设置页「更新」区呈现全部细节 + 侧栏「⚙ 设置」按钮角标；**不做全局常驻横幅**。
5. 触发：**只有用户显式点击「重启并升级」才替换二进制**；不自动安装、不自动重启。
6. 脚本：`go:embed` 静态脚本 + **参数经不可被 shell 解释的通道传递**；Windows 用**纯 cmd**（不调 PowerShell）。*实现细节调整*：Linux 用 argv（execve，天然无 shell），Windows 用**环境变量**——理由与事实见 §8.5；「零 shell 插值」这一语义未变。
7. 命名：待安装文件 sshore.<目标版本号>[.exe]；旧二进制备份 sshore.<当前版本号>[.exe]，**只保留最近一份备份**。
8. 边界：非 release 构建（dev 与 git describe 串）不自动检查，手动可查、可升级；无本平台资产时明确告知并给发布页。
9. UPX 默认值本次不改（独立决策，见 R9）。
10. **Windows「可重命名运行中 exe」（F3）是实施前置门禁**：spike 不通过则本期只交付阶段 A + 人工替换指引（§4 F3、§10.4）。

---

## 3. 范围

### 3.1 本期做

- `internal/update` 新包：版本类判定、源访问、资产挑选、下载、校验、解包、替换计划、脚本生成、ExeDir 排他锁、状态机。
- `app.go` 新增 8 个绑定 + 1 个发布页打开（见 §5.3），并在 startup(ctx) / App.OnShutdown / App.SetSettings 三处接线；进度与状态经 Wails 事件推送。
- **重新生成并提交 Wails 绑定**（frontend/wailsjs/go/main/App.{js,d.ts}）：所有构建路径都带 -skipbindings，不重新生成前端会直接构建失败。
- AppSettings 新增 4 个字段（复用现有 GetSettings/SetSettings）。
- 前端：设置对话框「更新」区 + 侧栏角标 + stores/update.js + utils/update.js。
- CI：release job 生成并上传 checksums.txt；Linux job 跑 shellcheck；Windows job 用真实 cmd.exe 跑一次脚本（见 §11）。
- 新增 .gitattributes 钉死脚本换行符（*.sh → LF、*.cmd → CRLF），否则 go:embed 会把 CRLF 嵌进 Linux 脚本。
- 文档：README 增「更新与升级」一节。

### 3.2 本期不做（明确排除）

无人工点击的自动安装；升级后会话恢复；断点续传；增量/差分更新；**全局单实例应用守卫**（只做 apply 临界区的 ExeDir 排他锁，见 §7.6）；ETag/条件请求；macOS 支持；UPX 默认值调整；对 360/火绒等第三方 AV 的兼容保证；回滚界面（备份文件仅作为手工回滚手段）。

---

## 4. 事实前提（实施期必须复核的硬事实）

| # | 前提 | 状态 / 复核方式 | 不成立时的应对 |
|---|---|---|---|
| F1 | GET <api>/releases/latest 返回 tag_name / body / published_at / assets[].name / assets[].browser_download_url | **已核**（gh api repos/i2534/sshore/releases/latest：tag=v0.6.0、body 非空、assets 两个） | 调整结构体字段；必要时补 Accept: application/vnd.github+json |
| F2 | 未认证配额约 60 次/小时/IP（文档值） | 观察 X-RateLimit-* 响应头并写日志 | 拉长默认间隔或降级为「打开发布页」 |
| F3 | **Windows 允许重命名（同卷 move）正在运行的 exe，但不允许删除/覆盖它** | **实施前置门禁 GO**（Task 0，2026-09-19）：Windows 10 22H2 VM 上 Session 0（sshd 子进程）与交互式 Session 1（schtasks /it）各验证一次，均为 `RENAME_OK` + `WRITE_ORIGINAL_OK`；改名副本与原 exe 的 SHA256 逐字节一致 | **只退化为「人工替换 + 明确文案」**；helper 模式救不了 F3（§10.4）。不通过则不进入阶段 B（本次已通过） |
| F4 | Go 的 net/http 不写 Zone.Identifier（下载产物无 MOTW） | VM 上 Get-Item -Stream Zone.Identifier | 无应对，仅影响 SmartScreen 预期 |
| F5 | 产物名形如 sshore-v0.6.0-linux-amd64.tar.gz / sshore-v0.6.0-windows-amd64.zip | **已核**（gh release view v0.6.0 --json assets） | 资产挑选按「含 tag + 含 os + 含 arch + 后缀」宽松匹配，失败即 no-asset |
| F6 | 归档内二进制：tar.gz 为 **./sshore**（./ 前缀、mode 0755、4,104,452 B）+ ./README.md + ./LICENSE；zip 为根下 **sshore.exe**（4,600,320 B）。压缩包 3,693,254 B / 4,429,317 B | **已核**（gh release download + tar -tvzf / unzip -l） | ExtractBinary 用 filepath.Base+Clean 归一后匹配（裸名精确匹配会取不到） |
| F7 | syscall（Windows）**没有** CREATE_NO_WINDOW 符号；常量在 golang.org/x/sys/windows（go.mod:10，v0.47.0 直接依赖）与 internal/osutil/procattr_windows.go:13（**未导出**，跨包不可用） | **已核**（GOOS=windows go doc syscall.CREATE_NO_WINDOW → no symbol） | 用 windows.CREATE_NO_WINDOW 或包内自定义常量；不要照字面写 syscall.CREATE_NO_WINDOW |
| F8 | Makefile:22 的 git describe --tags 产出**非 tag 串**（实测 v0.6.0-80-gc2d2a36） | **已核**（git describe --tags） | 版本类判定必须覆盖 describe 类（§5.1 version.go） |

---

## 5. 架构与组件

### 5.1 新增 internal/update（依赖全部注入，逻辑可纯单测）

| 文件 | 职责 | 关键接口 |
|---|---|---|
| version.go | 版本**分类**与比较 | type Kind int（Clean/Describe/Dev/Invalid）、Class(v string) Kind、IsRelease(v) bool、Compare(a, b string) int、Base(v string) string、ParsePendingName(name string) (ver string, ok bool) |
| source.go | 读 <source>/releases/latest | type Release struct{ Tag, Notes, PublishedAt string; Assets []Asset }、Asset{Name, URL string; Size int64}（Size = 上游 assets[].size，缺失为 0）、(c *Client) Latest(ctx context.Context, source string) (Release, error)、错误哨兵 ErrRateLimited/ErrCheckFailed |
| asset.go | 挑本平台包与校验文件；**校验下载 URL 在受信主机集合内** | PickArchive(rel, goos, goarch) (Asset, error)、PickChecksums(rel) (Asset, error)、SameOrigin(source, rawURL string) bool（同 host；源为 api.github.com 时另允许 github.com / codeload.github.com / *.githubusercontent.com，理由见 §10.1） |
| checksum.go | 解析 sha256sum 格式并比对 | ParseChecksums(io.Reader) (map[string]string, error)、VerifyFile(path, want string) error |
| download.go | 流式下载（进度节流 200ms / 空闲超时 / 半截清理）；空间检查按平台拆文件 | (c *Client) Download(ctx context.Context, url, dest string, opt DownloadOpt) error（边下边算的哈希由调用方用 FileSHA256 复核）、DownloadOpt{IdleTimeout, Throttle time.Duration; Progress func(done, total int64)}、FreeSpace(dir string) (int64, error)（freespace_unix.go / freespace_windows.go） |
| extract.go | 从 tar.gz/zip 只取出二进制 | ExtractBinary(archive, goos, dest string) error：**先用 unsafeEntry 显式拒绝含 .. 或绝对路径的条目**（path.Base 会把 ../sshore 洗白）、再按 filepath.Base+Clean 匹配白名单（sshore / sshore.exe）、**仅目标二进制同名条目**的 symlink/hardlink 被拒绝（无关链接条目跳过）、多匹配报错、单条目 ≤ 64 MiB |
| plan.go | 计算替换计划与 pending 名义判定 | type Plan struct{ Target, Pending, Backup, Sidecar, LogPath, ExeDir string; Size int64; Wait time.Duration }、PlanFor(goos, exePath, fromVer, toVer string, size int64, wait time.Duration) Plan（备份名：Clean/Describe 用 Base(fromVer)，仅 Dev/Invalid 用时间戳）、ResumePending(exeDir, current string) (Plan, bool)、IsPendingName(name, current string) bool |
| script.go + start_unix.go / start_windows.go | go:embed 脚本 + 参数/环境构造 + 分离启动（SysProcAttr 按平台拆文件：Linux 的 syscall.SysProcAttr 没有 HideWindow 字段） | ScriptName(goos) string、ScriptBytes(goos) ([]byte, error)、ScriptArgs(Plan, pid int) []string（Linux argv）、ScriptEnv(Plan, pid int) []string（Windows 环境变量）、StartDetached(goos, scriptPath string, args, env []string) error、平台函数 setDetached(*exec.Cmd) |
| lock.go | ExeDir 级排他锁，防两个实例同时 apply | Acquire(exeDir string) (release func(), err error)（Windows：命名互斥体；Linux：.sshore-update.lock 上的 flock） |
| scripts/update.sh | Linux 升级脚本（embed、可 shellcheck） | — |
| scripts/update.cmd | Windows 升级脚本（embed、纯 cmd、不含 powershell） | — |
| service.go | 状态机 + 轮询 + 残留自检 + 事件发射 | 见 §5.2 |

### 5.2 Service 契约

```go
type Options struct {
    Goos, Goarch, Version, ExePath string
    Doer    Doer                                              // *http.Client（含超时）；测试注入 httptest
    Launch  func(goos, path string, args, env []string) error  // 分离启动；测试注入假实现
    Acquire func(exeDir string) (func(), error)                // 排他锁；测试注入假实现
    Emit    func(event string, payload any)                   // update:state / update:progress
    Config  func() Settings                                   // 读配置，每次调用都重新读
    Save    func(patch Settings) error                        // 写配置（跳过版本），由 app.go 落到 TOML
    Now     func() time.Time
}

type Settings struct {
    Auto     bool
    Interval time.Duration
    Source   string
    Skipped  string
}

func New(opt Options) *Service
func (s *Service) Init(ctx context.Context)              // 残留自检 + 自动检查/轮询调度（ctx 来自 startup）
func (s *Service) Shutdown()                             // 停轮询、取消在飞请求（App.OnShutdown）
func (s *Service) Reconfigure()                          // 重读配置并按新间隔重建定时器（SetSettings 后）
func (s *Service) Info() UpdateInfo                      // 当前快照（零值安全）
func (s *Service) Check(ctx context.Context, manual bool) (UpdateInfo, error)
func (s *Service) StartDownload(ctx context.Context) error
func (s *Service) CancelDownload() bool
func (s *Service) ApplyAndRestart(ctx context.Context) error
func (s *Service) DiscardPending() error                 // 「删除已下载的更新」（连 sidecar 一起删）
func (s *Service) SkipVersion(v string) error            // 「跳过此版本」
func (s *Service) ClearSkipped() error                   // 「取消跳过」→ 立刻重查
```

UpdateInfo 是前后端唯一契约（JSON）：

```go
type UpdateInfo struct {
    Seq         int64  // "seq"          单调递增；前端丢弃迟到的旧载荷（§12.2）
    State       string // "state"        §7.1 状态枚举
    Current     string // "current"
    Latest      string // "latest"
    Notes       string // "notes"
    PublishedAt string // "published_at"
    Source      string // "source"
    Progress    int    // "progress"     0-100；-1 = 总大小未知
    ReadyPath   string // "ready_path"
    Skipped     bool   // "skipped"      仅表示「用户跳过了 latest 版本」
    Manual      bool   // "manual"       最近一次检查是否手动触发（决定文案颗粒度）
    Hint        string // "hint"         "" 或 "manual-upgrade"（不可写/需人工替换）
    Error       string // "error"
    PendingLog  string // "pending_log"  上次升级失败日志（存在且 RESULT=fail 时）
}
```

### 5.3 app.go 绑定与接线

```go
func (a *App) GetUpdateInfo() update.UpdateInfo
func (a *App) CheckUpdate(manual bool) (update.UpdateInfo, error)
func (a *App) StartUpdateDownload() error
func (a *App) CancelUpdateDownload() bool
func (a *App) ApplyUpdateAndRestart() error
func (a *App) DiscardUpdateDownload() error   // 删除待安装文件 + sidecar
func (a *App) SkipUpdateVersion(version string) error
func (a *App) ClearSkippedUpdate() error
func (a *App) OpenReleasePage() error         // runtime.BrowserOpenURL(Repo + "/releases")
```

**接线**：

- **构造与调度放在 startup(ctx)（app.go:90）**：那里才有 ctx，且每个进程只走一次；App.Init（app.go:143）**不启动任何调度**（它无 ctx，且被 24 处测试调用，在那里起 goroutine 会污染每个 main 包测试且无人 Shutdown）。
- App.OnShutdown（app.go:1044，已由 main.go:62-63 回调）→ Service.Shutdown()。
- App.SetSettings（app.go:260）保存成功后 → Service.Reconfigure()（更新源/间隔/开关立即生效）。
- **零值安全**：测试直接构造的 App（未走 startup）调用任意更新绑定必须返回 State: disabled 快照或明确错误，不得 panic（加单测）。
- 事件沿用 app.go:484 的既有模式：

| 事件 | 载荷 | 说明 |
|---|---|---|
| update:state | 完整 UpdateInfo | 状态变化时发一次 |
| update:progress | {done, total, percent} | 下载中按 **200ms 节流** |

设置项不新增绑定：4 个字段进 AppSettings，前端走既有 GetSettings/SetSettings。

### 5.4 前端文件

| 文件 | 变化 |
|---|---|
| frontend/wailsjs/go/main/App.{js,d.ts}、models.ts | **必须重新生成并提交**（wails generate module，v2.15.0 实测存在该子命令）。否则 UpdateSection.vue 的 import 会让 npx vitest 与 npm run build 直接失败 |
| frontend/src/components/SettingsDialog.vue | 「帮助」段下方挂载新的 UpdateSection 组件 |
| frontend/src/components/UpdateSection.vue（新） | 更新区全部 UI（§12） |
| frontend/src/stores/update.js（新） | 事件归约（按 seq）、角标派生、动作封装 |
| frontend/src/utils/update.js（新） | 纯函数：状态→文案、按钮可用性、字节/百分比格式化 |
| frontend/src/stores/settings.js | 4 个新字段读写（load/save 的显式字段清单，:83-106） |
| frontend/src/App.vue | 侧栏「⚙ 设置」角标；onMounted 取一次 GetUpdateInfo 快照（**前端不调度检查**） |

---

## 6. 配置

internal/config/store.go 的 AppSettings 新增（TOML / JSON 键同名）：

| 字段 | 类型 | 默认 | Normalize 规则（纯函数，**不写日志**） |
|---|---|---|---|
| update_check_auto | bool | true | 无 |
| update_check_interval_hours | int | 12 | < 0 → 12；0 → 0（显式关闭轮询）；> 168 → 168 |
| update_skipped_version | string | 空 | 去空白与 v 前缀；含 [0-9A-Za-z.+-] 之外的字符则清空 |
| update_source | string | 空（= 内置默认 API base） | 去首尾空白；非 http(s):// 前缀则清空 |

**回退与告警移出 Normalize**：internal/config 不依赖日志（Normalize 是纯方法，被 LoadConfig 与 SetSettings 调用）。「非 loopback 必须 https、否则视为非法 → 回退默认源 + 写一行日志 + 设置页提示」由 Service 读配置时执行一次（§10.2）。

前端「设置」页新增控件：自动检查开关、间隔下拉（12 / 24 / 48 / 168 小时 / 关闭轮询）、更新源文本框（含「恢复默认」）。保存（SetSettings）后由 app.go 调 Service.Reconfigure()。

**跳过版本的双写问题**：update_skipped_version 有两个潜在写者 —— Service.SkipVersion（经 Options.Save）与前端 stores/settings.js 的全量 save()（字段清单来自挂载时的 load() 快照）。规定：

1. SkipVersion/ClearSkipped **只经 Options.Save「读改写」a.cfg.App.UpdateSkippedVersion**，不依赖前端回传；
2. 前端在「跳过 / 取消跳过」成功后立刻 settingsStore.load() 重读，避免用过期的字段快照整体回写；
3. 加测试：跳过 → 改主题 → 保存 → 跳过仍在。

---

## 7. 状态机与数据流

### 7.1 状态枚举与迁移

**状态**（not-writable 并入 io-failed 后用 Hint 区分）：

idle · checking · up-to-date · available · skipped · rate-limited · check-failed · no-asset · no-checksum · downloading · verify-failed · io-failed · ready · applying · disabled

语义澄清：

- skipped **只**表示「命中 update_skipped_version（用户跳过了这个版本）」，绝不用来表示门卫拦截。
- disabled：非 release 构建（dev/describe）或 auto=false，且本次不是手动检查 → 不发请求、不亮角标、界面说明原因（无「取消跳过」按钮）。
- io-failed：IO/磁盘/权限/下载超时/锁冲突的合集；Hint="manual-upgrade" 表示目录不可写等需人工介入的情况。

**合法迁移**：

```
idle/up-to-date/available/skipped/rate-limited/check-failed/no-asset/no-checksum/disabled
   --Check(manual)--> checking --(结果)--> up-to-date|available|skipped|rate-limited|check-failed|no-asset|no-checksum
available --SkipVersion()--> skipped        skipped --ClearSkipped()--> checking（立刻重查）
available|verify-failed|io-failed --StartDownload()--> downloading   // 重试路径（原设计缺这两条边）
downloading --(成功)--> ready --ApplyAndRestart()--> applying
downloading --(失败)--> verify-failed|io-failed|available(取消)
ready --DiscardPending()--> available
Init 残留自检：命中 pending（§7.5 唯一规则）--> ready
applying 为终态（进程即将退出）
```

**并发守卫（一把锁，全在 Service 内）**：

- Check 在 downloading/ready/applying 期间**一律拒绝**（不得覆盖下载进度或抹掉 ready 的 ReadyPath，更不得破坏终态）；在 checking 期间忽略重复调用。
- StartDownload 仅在 available/verify-failed/io-failed 且无在飞下载时接受。
- SkipVersion/ClearSkipped/DiscardPending 在 applying 时拒绝。
- ApplyAndRestart 只允许从 ready 进入且不可重入。

### 7.2 阶段 A：检查

1. 版本分类：Class(Version)。门卫：!IsRelease(Version) && !manual → disabled；!auto && !manual → disabled（两者都**不发请求**、写一行日志说明原因）。
2. GET <source>/releases/latest：总超时 10s；User-Agent: sshore/<version>；Accept: application/vnd.github+json。
3. HTTP 映射：200 → 解析；429 → rate-limited；403 **仅当** X-RateLimit-Remaining: 0（或响应体含 rate limit）才判 rate-limited，否则 check-failed；其它状态码、JSON 解析失败、网络错误 → check-failed。X-RateLimit-* 一律写日志。
4. 比较：IsRelease(Version) 时 Compare(tag, Version) <= 0 → up-to-date；**非 release 构建不做比较短路**（dev/describe 与 release tag 不可比），直接进入资产挑选判 available，界面文案注明「本地构建，无法判定是否最新」。
5. 跳过：Base(tag) == Base(Skipped) → skipped（Skipped=true；手动检查仍可见 + 「取消跳过」）。
6. 资产：PickArchive / PickChecksums 失败 → no-asset / no-checksum；SameOrigin 校验不通过 → check-failed（§10.2）。
7. 通过 → available（带 Notes/PublishedAt）。
8. 调度：Init 后延迟 5s 首查（auto=true 且 IsRelease），此后每 interval；Shutdown 停止；Reconfigure 重建。

### 7.3 阶段 B-1：下载与就绪

1. 可写性探测：在 ExeDir 写 .sshore-write-test-<rand> 并删除；失败 → io-failed + Hint="manual-upgrade"。
2. 磁盘空间：FreeSpace(ExeDir) 需 ≥ 资产大小 + 64 MiB，否则 → io-failed。
3. 下载到 os.TempDir()/sshore-update-<pid>.part，边下边算 SHA256，进度 200ms 节流；**超时**：ResponseHeaderTimeout 15s，且**连续 30s 无进度**即取消 → io-failed。
4. 取 checksums.txt → ParseChecksums → 比对压缩包：不符 → 删临时文件、verify-failed（日志记期望/实际前 12 位），**绝不向程序目录写任何东西**。
5. 通过 → ExtractBinary 到 <ExeDir>/sshore.<目标版本号>[.exe]，并写 **sidecar** <pending>.sha256（内容单行 <hash> 两个空格 <pending 文件名>）；状态 ready。
6. 删除临时压缩包；CancelDownload（ctx 取消）→ 删 .part → 回 available。**不做断点续传**。

### 7.4 阶段 B-2：应用（ApplyAndRestart）

1. 前置：State==ready；pending 存在；Size>0 时校验大小，Size==0（例如重启后由残留自检进入 ready）时**以磁盘 stat 为准**，不做进程内比较。
2. **sidecar 重算校验**：读 <pending>.sha256 并对 pending 重算 SHA256，不符或 sidecar 缺失 → 拒绝（→ verify-failed），不生成脚本。
3. **取 ExeDir 排他锁**（Acquire(exeDir)）：失败 → io-failed「另一个实例正在升级」。
4. 生成脚本：按 Goos 写 embed 资源为 <ExeDir>/sshore-update.sh|.cmd（Linux 0755）；写失败 → io-failed，不启动、不退出。
5. 组装参数：Linux ScriptArgs(Plan, os.Getpid())；Windows ScriptEnv(Plan, os.Getpid())（§8.2/§8.5）。
6. 分离启动（两侧都不依赖父进程存活）：Linux SysProcAttr{Setsid: true}；Windows cmd /d /c "<脚本>" + SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}（F7）。
7. 置 applying 并发一次 update:state（「正在重启…」）。
8. runtime.Quit(ctx)。

时序（Windows 为例）：

```
用户点「重启并升级」
  → Go: 状态/大小/sidecar 校验 → 取锁 → 写脚本 → cmd /d /c 分离启动（环境变量传参）→ runtime.Quit
  → 脚本: cd /d "%~dp0" → 等旧 PID 退出（tasklist|find，上限 SSHORE_WAIT 默认 60s）
        → 自检 pending 存在 + 大小一致 → 清理更早备份（保留 pending）
        → sshore.exe → sshore.v0.6.0.exe
        → sshore.v0.7.0.exe → sshore.exe
        → start "" "sshore.exe" → 存活探测 3s
             ↘ 失败: 回滚（新文件退回 pending、备份改回正式名、启动旧版）→ RESULT=fail:<step>
        → RESULT=ok → 自删脚本 → 删日志
  → 新版窗口标题显示 "SSHore v0.7.0"
```

### 7.5 残留自检（Init）

**唯一判别规则**（原规则三条件互斥、会把备份误判成 pending）：设 v 为从文件名解析出的版本段（sshore.<v>[.exe]，且 v 必须以数字或 dev- 开头 —— 否则不是候选，例如正式二进制 sshore.exe）：

| 判定 | 条件 | 处理 |
|---|---|---|
| **pending** | Class(v)==Clean 且 Compare(v, Version) > 0 | 状态 ready（可「重试升级」，无需重新下载）；Size 置 0（以磁盘为准） |
| 备份 | 其余候选（dev-<ts>、describe 串、≤ 当前版本的 clean tag、当前版本自身） | 仅记一行日志「上一版本备份：<路径>」 |
| 陈旧日志 | sshore-update.log 末行是 RESULT=ok | 删除（**仅此处允许删除**），不打扰用户 |
| 失败日志 | sshore-update.log 末行是 RESULT=fail:<step> | PendingLog = 该路径；界面提示「上次升级未完成」+「重试升级」（若无 pending 则提示人工替换步骤，§9） |

前言约束：自检**只读扫描**，唯一允许的写操作是删除 RESULT=ok 的陈旧日志（消除原稿「前言说不删、正文说要删」的矛盾）。pending 的存在以本表判别为准，**绝不**在自检里做任何替换。

### 7.6 并发与锁

- 应用本身**不做**全局单实例（§3.2）；但 apply 是危险临界区：两个同目录实例同时进入会互相 rename 导致二进制丢失。
- 因此 ApplyAndRestart 必须先 Acquire(ExeDir)：Windows 用命名互斥体 Global\\sshore-update-<sha1(ExeDir)>（windows.CreateMutex），Linux 用 <ExeDir>/.sshore-update.lock 上的 flock(LOCK_EX|LOCK_NB)。
- 取锁失败 → io-failed：「另一个实例正在升级，请稍后重试」。
- 锁在本进程退出时释放（Windows 句柄随进程关闭；Linux fd 随进程关闭），脚本不需要参与加锁。

---

## 8. 脚本契约

### 8.1 生成与生命周期

- 脚本是 **go:embed 静态文件**（internal/update/scripts/update.sh、update.cmd），按 GOOS 选一份写出，**不运行时拼接脚本文本**。
- **换行符钉死**：新增 .gitattributes（*.sh text eol=lf、*.cmd text eol=crlf）。go:embed 原样嵌入工作区字节；仓库当前没有 .gitattributes，Windows runner 若检出 CRLF，嵌进二进制的 update.sh 会带 \\r，Linux 用户执行时报行首 shebang 解析失败。
- 写在 ExeDir，名 sshore-update.sh|.cmd（同盘、用户可读、可手动重跑）。
- 成功：自删脚本 + 删除日志（日志末行已写 RESULT=ok）。
- 失败：**保留**脚本、pending、sidecar 与日志（RESULT=fail:<step>），供重试与排障。

### 8.2 参数与传递通道

| 参数 | Linux（argv） | Windows（环境变量） |
|---|---|---|
| 旧进程 PID | --pid | SSHORE_PID |
| 正式二进制绝对路径 | --target | SSHORE_TARGET |
| 待安装文件绝对路径 | --pending | SSHORE_PENDING |
| 备份绝对路径 | --backup | SSHORE_BACKUP |
| 待安装文件字节数 | --size | SSHORE_SIZE |
| 日志绝对路径 | --log | SSHORE_LOG |
| 等待旧进程退出秒数（默认 60） | --wait | SSHORE_WAIT |

**校验规则**（原规则「路径不存在即失败」会拒绝合法调用）：

- **必须存在**：TARGET（当前二进制）与 PENDING（待安装文件）——不存在即 RESULT=fail:args 退出 2。
- **只要求父目录存在且可写**：BACKUP（本次运行才创建）与 LOG（首次出错才写）；脚本第 1 步创建/清空日志。
- PID / SIZE / WAIT 必须是正整数，否则 RESULT=fail:args。
- 不做「路径存在性」之外的猜测，不兜底、不静默。

### 8.3 步骤（两平台同构）

| # | 步骤 | Linux | Windows |
|---|---|---|---|
| 0 | 切到目标目录（原 glob 作用于继承的 CWD） | cd "$(dirname "$TARGET")" | cd /d "%~dp0" |
| 1 | 解析参数、校验、建日志 | case 循环 + : > "$LOG" | %~ 解析 / %SSHORE_*% + type nul > "%SSHORE_LOG%" |
| 2 | 等旧 PID 退出，上限 WAIT | kill -0 轮询 1s（分数 sleep 在 busybox/sysvinit 变体上可能不支持）；超时 → RESULT=fail:wait 退出 3 | tasklist /FI "PID eq %SSHORE_PID%" | find "%SSHORE_PID%" >nul 轮询 1s；超时同上 |
| 3 | 自检 pending 存在 + 大小一致 | [ -f "$PENDING" ] + wc -c < "$PENDING" | if exist "%SSHORE_PENDING%" + for %%A in ("%SSHORE_PENDING%") do set PEND_SIZE=%%~zA，**比较必须 setlocal enabledelayedexpansion + !PEND_SIZE!**（同一 if 块内 %PEND_SIZE% 会在解析期展开） |
| 4 | 清理更早备份（只留最近一份），**绝不删 pending** | 遍历 "$DIR"/sshore.v* 与 "$DIR"/sshore.dev-*，取 basename 后与 pending 的 basename 比较，不等才删 | for %%f in ("%DIR%\\sshore.v*") ...，用 %%~nxf 取 basename 比较 |
| 5 | 旧二进制改名备份 | mv -f "$TARGET" "$BACKUP" | move /y "%SSHORE_TARGET%" "%SSHORE_BACKUP%" |
| 6 | 待安装文件改名正式名 | mv -f "$PENDING" "$TARGET" | move /y "%SSHORE_PENDING%" "%SSHORE_TARGET%" |
| 7 | 可执行位 | chmod +x "$TARGET" | 不适用 |
| 8 | 启动新版 + **存活探测**，失败回滚 | setsid 或 nohup 启动，3s 后 kill -0 探测 | start "" "%SSHORE_TARGET%"，3s 后 tasklist 探测 |
| 9 | 收尾 | 成功写 RESULT=ok + 删日志 + 自删脚本 | 成功写 RESULT=ok + del 日志 + del "%~f0" |

**明确禁令**（同时是测试断言）：脚本内不得出现 powershell、curl、wget、certutil、网络动作或 eval。

**失败回滚**（原设计在「新版起不来」时无回滚，用户会只看到应用消失）：

- 第 5/6 步失败 → 若正式名已空，把备份改回正式名后 RESULT=fail:<step> 退出。
- 第 8 步存活探测失败 → 把新文件改回 pending 名、备份改回正式名、启动旧版，写 RESULT=fail:launch 并**保留 pending**（用户下次启动看到「上次升级未完成」+ 可重试）。

### 8.4 命名与备份契约

| 名字 | 规则 |
|---|---|
| 正式二进制 | sshore（Windows sshore.exe）—— 判别时**必须显式排除**（否则 sshore.exe 会被当成 sshore.<ver> 候选） |
| 待安装文件 | sshore.<目标版本号>[.exe]，如 sshore.v0.7.0 |
| 待安装哈希 sidecar | sshore.<目标版本号>[.exe].sha256（单行 <hash> 两个空格 <文件名>） |
| 备份 | sshore.<当前版本号>[.exe]；dev 构建用 sshore.dev-<YYYYMMDD-HHMMSS>[.exe]；describe 构建用 sshore.<describe 串>[.exe]（如 sshore.v0.6.0-80-gc2d2a36） |
| 备份保留策略 | **只保留最近一份**：第 4 步删除除「本次 pending」「本次 backup」外的全部候选（含 dev- 与 describe 类） |
| 升级脚本 | sshore-update.sh / sshore-update.cmd |
| 升级日志 | sshore-update.log（末行协议见 §8.6） |
| 下载临时文件 | os.TempDir()/sshore-update-<pid>.part，校验完成即删 |
| 锁文件 | <ExeDir>/.sshore-update.lock（Linux；Windows 用命名互斥体，无文件） |

### 8.5 为什么 Windows 不走 argv（决策 6 的实现细节调整）

- Go 的 syscall.EscapeArg（$GOROOT/src/syscall/exec_windows.go）只处理双引号、反斜杠、空格与制表符；经 cmd.exe 转发时参数里的 % 会被展开成变量、无空格时的 & 与 ^ 会被当作命令分隔符/转义符 → **exec.Command("cmd","/c",script,args...) 传参不满足「零 shell 插值」**。
- 因此 Windows 侧改用**环境变量**：cmd.Env 由 Go 直接设置，不经过任何 shell 解析；脚本用 "%SSHORE_TARGET%" 读取。cmd 对变量内容**不做二次展开**（单遍展开），带引号的值里 & ^ 与空格都安全。
- Linux 侧继续用 argv（execve 不经 shell，天然零插值），便于用真实脚本测试。
- 两平台都传**绝对路径**，脚本第 0 步 cd 到目标目录后只做 basename 比较。

### 8.6 脚本日志协议

- 第 1 步建立/清空日志（首次可创建）。
- 每步失败写一行 STEP=<n> ERR=<原因>，最后写 RESULT=fail:<step> 并以非 0 退出。
- 成功写 RESULT=ok，随后删日志（删除失败只留无害残留）。
- Service 的残留自检只读**末行**判断（§7.5），不做全文解析。

---

## 9. 错误处理与边界

| 情形 | 行为 |
|---|---|
| 自动检查失败（无网/超时/5xx/坏 JSON） | 静默，仅日志面板 1 行；角标不亮 |
| 手动检查失败 | 文案含原因 + 「打开发布页」；不自动重试、不退避堆叠 |
| 429，或 403 且 X-RateLimit-Remaining: 0 | rate-limited：「更新源限流，请稍后再试或手动下载」；**403 但无该头 → check-failed**（私有/不存在仓库、被代理拦截）——与 §7.2 对齐 |
| 该 Release 无 checksums.txt | no-checksum：**拒绝自动升级**（安全优先）+ 发布页兜底 |
| 无本平台资产（arm64/386/改名） | no-asset：「本平台暂无可用包」+ 发布页 |
| 非 release 构建（dev/describe） | 不自动检查（disabled）；手动检查显示最新版并允许升级；界面注明「本地构建，无法判定是否最新」；备份名见 §8.4 |
| ExeDir 不可写（/usr/bin、Program Files） | io-failed + Hint="manual-upgrade"：给人工升级步骤；仍允许「重试」（重试会重新探测可写性） |
| 下载中取消/中断 | 删 .part、回 available；断点续传不做 |
| 下载 30s 无进度 / 响应头 15s 超时 | io-failed：文案含原因（源半死不活不会永久卡在 downloading） |
| 磁盘空间不足（< 资产 + 64 MiB） | io-failed：不做任何落盘 |
| SHA256 不符（压缩包） | verify-failed：删临时文件、日志记期望/实际前 12 位、程序目录零写入 |
| sidecar 缺失或 pending 重算不符 | 拒绝 apply（→ verify-failed），提示重新下载（防「落盘后被替换」） |
| 另一个实例持锁 | io-failed：「另一个实例正在升级，请稍后重试」 |
| 解包/落盘 IO 失败 | io-failed：带原始错误文本 |
| 脚本失败（占用/权限/AV 拦截/新版起不来） | 保留脚本+pending+日志；下次启动读到 RESULT=fail:* → 「上次升级未完成」+「重试升级」；若 pending 已被消耗（如 launch 失败后回滚成功）则给人工步骤 |
| 跳过版本 | 自动检查静默；手动可见 + 「取消跳过」（→ 立刻重查） |
| 重复点击 / 并发 | §7.1 守卫：Check 在 downloading/ready/applying 被拒；applying 只允许一次；下载中按钮禁用 |
| 更新源配置非法 | 服务侧回退默认源 + 一行日志 + 设置页提示（§6） |
| 代理 | Go 默认传输自动读 HTTP_PROXY/HTTPS_PROXY，不新增设置项 |
| 隐私 | 检查请求带版本号 UA；自动检查默认开，README 与设置页各写一句、且可关 |

---

## 10. 安全、隐私与 AV

### 10.1 信任边界（原稿把「补校验文件」说成了安全闭环）

- **信任根 = HTTPS + GitHub（i2534/sshore）账户与 GitHub 自身**。checksums.txt 与压缩包同源同主机，它能发现的是：**传输损坏、半包、资产错发/错配**；它**不能**防：更新源被攻破、GitHub 账户被盗后发布的被篡改 Release、自定义源被伪造。
- 因此额外加两道：
  1. **下载来源白名单**：默认源是 `api.github.com`，但资产的 `browser_download_url` 在 `github.com`/`*.githubusercontent.com` 上——所以「同源」必须按**受信下载主机集合**实现，而不是字面 host 相等：同 host 通过；源为 `api.github.com` 时额外允许 `github.com`、`codeload.github.com`、`*.githubusercontent.com`；其它 host 一律 `check-failed`（`SameOrigin`，见 §5.1）；
  2. **pending 完整性持久化**：解包后写 sidecar <pending>.sha256，**apply 前（含重启后由残留自检进入的路径）重算比对**——补上「落盘后到 apply 之间被替换」的窗口。
- 校验失败一律拒绝安装，程序目录不留残留（verify-failed 时连 pending 一起删）。

### 10.2 源与传输

- 默认源为 https://；自定义源**非 loopback 必须 https://**，否则视为非法 → 回退默认源 + 日志 + 设置页提示。http:// 仅允许 127.0.0.1 / localhost（内网离线镜像与端到端测试用）。
- 使用**非默认源**时，「重启并升级」前弹**二次确认**，明确告知「更新源为自定义地址 <source>」。
- 下载与校验都在临时目录完成，校验通过才落程序目录。

### 10.3 AV 与签名（已知风险，如实记录）

- 自升级天然带三条行为特征：联网下载可执行文件并运行、重命名运行中的 exe、脚本操弄 PE 文件；我们是**未签名、零声誉**的 Go 二进制，这是最不利组合。
- UPX 打包本身已有误报记载（README）；本次不改默认值。
- 缓解：脚本最小化 + 不调 PowerShell + 走环境变量传参（少一层命令行解析）；Defender 真机证据进验收；README 说明；**根因解法是代码签名**（SignPath Foundation 对 OSS 免费 / Azure Trusted Signing 等，资格与价格以官方为准，本次不实施）。
- 对 360、火绒等第三方 AV **不做保证**。

### 10.4 预案（helper 救不了 F3）

| 风险 | 预案 | 说明 |
|---|---|---|
| 脚本被 Defender/AV 拦截 | 切 **helper 模式**：sshore --apply-update（由已校验哈希的待安装二进制分离启动，自己完成等待/备份/替换/启动） | 彻底不过脚本与解释器 |
| **F3 不成立**（Windows 不允许重命名运行中的 exe） | **只能退化为「人工替换 + 明确文案」**（设置页给出复制/改名步骤） | helper 同样要把运行中的旧 exe 改名，**依赖同一个 F3**，救不了 |

**helper 模式的真实影响面**（原稿「只影响三处」是低估）：作废 §8 整节、§13.2（脚本真跑）、§11.1（shellcheck）与 §14 对应条目、§12 的脚本文案；新增 main.go 早期分支的 wait/backup/rename/start 实现与测试；§5.1 文件表的 script.go 与 scripts/* 要替换为 helper 实现。是否采用应在 F3/Defender 门禁结果出来后一次性决定。

---

## 11. CI 变更

**11.1 脚本静态检查**：既有 Linux 测试步骤加 shellcheck internal/update/scripts/update.sh（runner 未预装则先 sudo apt-get install -y shellcheck）。本机实测无 shellcheck，无 CI 兜底时 DoD 只能靠人工。

**11.2 换行符**：新增 .gitattributes（*.sh text eol=lf、*.cmd text eol=crlf，见 §8.1），保证 Windows runner 上 update.sh 仍以 LF 检出/嵌入。

**11.3 Windows 侧脚本真跑**：在既有 go-windows job（.github/workflows/ci.yml:41-59）里加一步：用真实 cmd.exe 跑一次 update.cmd（假「旧二进制」+ 假 pending + 假 PID，断言替换结果与 RESULT=ok）。这比只在 Windows VM 上验便宜得多，且能覆盖 windows-only 代码。

**11.4 发布校验文件**：build-linux / build-windows 不改；release job 新增一步，在 **Linux runner** 上对 artifacts 里的 *.tar.gz / *.zip 统一算哈希：

```bash
cd artifacts
find . -type f \( -name '*.tar.gz' -o -name '*.zip' \) -print0 \
  | sort -z \
  | xargs -0 sha256sum \
  | awk '{ n=$0; sub(/^[^ ]+  /, "", n); sub(/^.*\//, "", n); print $1"  "n }' > checksums.txt
cat checksums.txt
```

（原稿 n=$2 在文件名含空格时会被截断；sub(/^[^ ]+  /,…) 更稳；当前产物名无空格，属预防。）随后把既有 mapfile -t FILES 的 find 匹配条件补上 -o -name 'checksums.txt'，让校验文件与压缩包一起走 gh release create 与 gh release upload --clobber。**实施期验证**：生成的 checksums.txt 与本地对同一压缩包 sha256sum 的结果，经 ParseChecksums 解析后**逐项相等**（不要求字节级一致）。

---

## 12. UI 设计

### 12.1 设置 → 更新区（UpdateSection.vue）

| 元素 | 显示条件 | 内容 |
|---|---|---|
| 当前版本 | 常显 | 当前版本 v0.6.0；非 release 构建显示本地构建 v0.6.0-80-gc2d2a36（dev） |
| 状态行 | 常显 | utils/update.js 的状态→文案表（§9 文案） |
| 更新说明 | available/ready 且有 notes | 折叠展示 published_at + body（截断 2000 字符） |
| 进度条 | downloading | percent（总大小未知 → indeterminate + 已下载字节） |
| 上次升级未完成 | pending_log 非空 | 日志路径 + 「重试升级」或人工步骤（§7.5/§9） |
| 设置控件 | 常显 | 自动检查开关、间隔下拉（12/24/48/168 小时 / 关闭轮询）、更新源输入框 + 「恢复默认」；非 https 的自定义源给一行风险提示 |

按钮矩阵（补齐重试缺边、去掉 skipped 的错位语义）：

| 状态 | 可用动作 |
|---|---|
| idle / up-to-date / check-failed / rate-limited / no-asset / no-checksum | 检查更新 · 打开发布页 |
| disabled | 检查更新（手动仍可用）· 打开发布页（并说明为何未自动检查） |
| available | 下载更新 · 跳过此版本 · 检查更新 · 打开发布页 |
| skipped | 取消跳过（清字段并立即重查）· 检查更新 · 打开发布页 |
| downloading | 取消下载 |
| verify-failed / io-failed | 检查更新 · 重试（重新下载；Hint="manual-upgrade" 时同时给人工步骤）· 打开发布页 |
| ready | **重启并升级**（自定义源时二次确认）· 删除已下载的更新 · 打开发布页 |
| applying | 全部禁用，显示「正在重启…」 |

### 12.2 侧栏角标

- App.vue 的「⚙ 设置」按钮右上角小圆点：条件只有 state 属于 {available, ready}（skipped/disabled 天然不亮）。
- store.acknowledge() 记录**已确认的版本号**（非布尔）：latest 变化或状态由 available 走到 ready 时重新亮；仅内存态，不落配置。
- 竞态：store **先注册事件监听再取 GetUpdateInfo() 快照**，按 UpdateInfo.seq 丢弃迟到的旧载荷。

### 12.3 文案

全中文，与现有界面一致；错误文案遵循「说清发生了什么 + 下一步动作」；非 release 构建、自定义源、AV 风险三类情况各有一句明确提示。

---

## 13. 测试策略

### 13.1 Go 单测（internal/update，零真实网络、零真实进程）

| 目标 | 用例要点 |
|---|---|
| version | Class 表驱动：v0.7.0(Clean)、v0.6.0-80-gc2d2a36(Describe)、dev / 空串(Dev)、1.2 / abc(Invalid)；Compare 十进制语义（0.10.0 > 0.9.0）；ParsePendingName：**sshore.exe 必须不是候选**（评审发现的误判） |
| pending 判别 | 表驱动：当前 v0.6.0 时 sshore.v0.7.0→pending、sshore.v0.6.0→备份、sshore.v0.6.0-80-gc2d2a36→备份、sshore.dev-<ts>→备份；当前为 describe 串时 sshore.v0.7.0→pending |
| source | httptest：200 / 403（带与不带 X-RateLimit-Remaining: 0）/ 429 / 500 / 坏 JSON / 空 assets → 状态映射 |
| asset | linux/amd64、windows/amd64 命中；386/arm64 缺失 → no-asset；checksums.txt 缺失 → no-checksum；**跨 host 的 asset URL → 拒绝** |
| checksum | sha256sum 格式（* 前缀、CRLF、空行、缺项）、比对失败 |
| download | 进度单调 + 200ms 节流；ctx 取消删 .part；**响应头超时与 30s 无进度 → io-failed**；哈希正确；空间不足 → io-failed |
| extract | 内存 fixture：tar 条目带 ./ 前缀 / zip 根名；**目标二进制的** symlink/hardlink 条目 → 拒绝；无关链接条目跳过；含 `..` 或绝对路径的条目 → 整档拒绝；多匹配 → 报错；>64 MiB 条目 → 拒绝 |
| plan / script | ResumePending 命中与不命中；备份清理**不包含 pending**；ScriptArgs / ScriptEnv 逐项断言；脚本字节**不含** powershell/curl/wget/certutil；update.sh 字节里无 \\r |
| lock | 同进程第二次 Acquire 失败（Linux flock 语义用双进程/双 fd 验证） |
| service | 状态机守卫（重复检查、Check 在 downloading/ready/applying 被拒、重复下载、未就绪 apply、applying 终态、applying 期间拒绝 skip/discard）；disabled 门卫零请求（假 doer 断言）；非 release 不做比较短路；ClearSkipped → checking；sidecar 缺失/不符 → 拒绝 apply；自检读日志末行（ok → 删、fail → 提示）；Seq 单调；Reconfigure 重建定时器（假时钟） |
| app.go | 门卫表测；**零值 App（未 startup）调用任意更新绑定 → 返回 disabled/错误且不 panic** |

### 13.2 脚本真跑

**Linux（CI 可做）**：临时目录造假「旧二进制」（输出 OLD）、pending（输出 NEW，大小已知）、更早的备份若干；分别跑：

1. 成功路径（--pid 为**已 reap 的**立即退出子进程）：断言备份名正确、正式名内容为 NEW、**只留一份备份**、**pending 已被消费**、脚本自删、日志已删；
2. 清理断言：**第 4 步之后 pending 仍存在**（挡住「恒不相等 → 删掉 pending」这个 Blocking 缺陷）；
3. 超时路径（长存活子进程 + --wait 1）：断言**保留现场**（pending/备份/日志都在）、RESULT=fail:wait、非 0 退出；
4. 参数非法（--target 不存在、--backup 父目录不存在、PID 非数字）：RESULT=fail:args；
5. 存活探测失败路径：pending 换成立刻退出的假「新版」→ 回滚、旧版仍在位、pending 保留。

（夹具里的假 PID 必须是被 reap 的子进程；父 shell 未 reap 的僵尸进程会让 kill -0 一直成功 —— 这正是超时用例要区分的情形。）

**Windows（CI 的 go-windows job，真实 cmd.exe）**：同样的 1/2/3/4 四条，用 SSHORE_* 环境变量传参；断言 RESULT=ok 与替换结果。

### 13.3 前端 vitest（沿用无 jsdom / 纯函数 + SSR 风格）

| 目标 | 用例要点 |
|---|---|
| utils/update.js | 状态→文案全枚举；按钮矩阵（每状态可用动作集合）；disabled / 非 release 文案；字节/百分比格式化；notes 截断 |
| stores/update.js | 事件归约；角标派生；acknowledge 后消失、版本变化后重新亮；seq 更小的迟到事件被丢弃；先订阅后快照 |
| updateWiring.test.js（结构性不变量） | 自动检查默认值来自后端（前端无硬编码）；角标单一来源；applying 全禁用；「重启并升级」仅在 ready；设置页保存包含 4 个新字段；跳过成功后调用 settingsStore.load() |
| stores/settings.test.js（扩展） | 新字段读写；**跳过 → 改主题 → 保存 → 跳过仍在** |

### 13.4 可复现的端到端自升级

**夹具现状**：仓库内**没有** XTEST/Xvfb 夹具（只有 e2e/test_local.sh 与 e2e/winprobe）；上一轮真机用的 /tmp/sshore-linux/xdrive.py **未入库**，且既有记录明确写过「用 setsid 脱离会话启动后 XTEST 合成事件不再触发 WebKit」，而升级后的新实例正是 setsid 启动的。因此：

- **自动化到 ready**：应用把 update_source 指向本地假源（http://127.0.0.1:<port>，仅 loopback 允许 http）+ 用当前树现场构建的更高版本产物 + checksums.txt + auto=true → 启动后自动检查 → 断言 available；这一段的下载点击用合成输入完成。
- **脚本层真跑**（§13.2）覆盖「替换 + 启动新版」这个危险核心，**不需要 GUI**：假二进制 + 标记文件即可在 CI 与真机验证。
- **GUI 全链路（点「重启并升级」→ 新版窗口标题变化）**：允许人工点一次，自动化只断言前后状态、磁盘与进程结果，并在报告里如实记录哪一步是人工的；夹具是否入库与「setsid 后 XTEST 失效」作为**文档化的已知限制**。

### 13.5 变体验证

| 变体 | 期望 |
|---|---|
| update_skipped_version = 最新版 | 自动检查静默、角标不亮；手动 → skipped + 「取消跳过」 |
| Release 缺 checksums.txt | no-checksum，无下载、程序目录零写入 |
| assets 无本平台包 | no-asset + 发布页 |
| checksums.txt 哈希改一位 | verify-failed，程序目录零残留 |
| ExeDir 只读 | io-failed + Hint=manual-upgrade，不下载 |
| pending/备份判别 | 升级成功后遗留备份**不会**提示「重试升级」；真 pending 能进入 ready |
| 脚本人为失败（pending 大小不符 / --wait 超时） | 保留现场 + RESULT=fail:* → 下次启动提示 + 可重试 |
| sidecar 被篡改 | apply 被拒（verify-failed），不生成脚本 |
| 两个实例同时 apply | 第二个 io-failed（锁冲突），不产生 rename 竞争 |
| 成功升级后再次启动 | 无残留脚本/日志；备份只剩一份 |

---

## 14. 验收清单（DoD）

1. make ci 全绿（frontend build + go vet + go test -race + npx vitest run；前端测试数以**运行时实际输出**为准 —— 本轮实测 187，评审用 grep 计数得 189，口径不同，故不写死数字）。
2. **go-windows job 全绿**：windows-only 的 update 代码（update.cmd 生成、备份模式、CreateMutex、CREATE_NO_WINDOW）只在这里被编译。
3. Wails 绑定已重新生成并提交（frontend/wailsjs/go/main/App.{js,d.ts} 含 9 个新函数），npm run build 与 vitest 通过。
4. §13.2 的脚本真跑在 **Linux（CI）** 与 **Windows（真实 cmd.exe）** 都通过：1/2/3/4 + 存活探测失败回滚。
5. §13.4 的端到端在 Linux 与 Windows VM 各跑一次：自动到 available（假源）→ 下载 → ready → 触发升级 → 新版本号显示；人工步骤如实记录。
6. Windows Defender 全程无拦截/隔离（有告警则按 §10.4 切 helper 或转人工替换）。
7. §13.5 的 10 个变体全部验证。
8. shellcheck 通过（§11.1；CI 的 `go` job 用 `shellcheck -s sh internal/update/scripts/update.sh` 强制门禁，安装失败即报错、不许静默跳过）；两个脚本文本不含 powershell/curl/wget/certutil；update.sh 无 \\r。
   （另：`go test` 里依赖「目录只读」语义的两个用例在 root 下会因 CAP_DAC_OVERRIDE 静默 skip，CI 必须以非 root 运行；前提注释见 `.github/workflows/ci.yml`，与 §13.2 的 util-linux setsid 前提并列。）
9. README 增「更新与升级」一节：更新源与可配置性、SHA256 能防与不能防（§10.1）、隐私说明、人工升级步骤（Windows/Linux）、AV 注意事项与 COMPRESS=0 退路。
10. **变异校验（具体化）**：手工制造以下 6 个变异，**每个都必须让至少一条测试失败** —— ① 判 pending 时不比较版本序；② 清理时按模式删除（连 pending 一起删）；③ 去掉 sidecar 重算；④ 去掉 Check 在 applying 的守卫；⑤ 脚本第 8 步去掉存活探测；⑥ 去掉角标的 ready 条件。结果记入实施报告。

---

## 15. 风险与缓解

| # | 风险 | 缓解 |
|---|---|---|
| R1 | AV/Defender 拦截自升级 | 脚本最小化、不调 PowerShell、环境变量传参、Defender 真机证据进验收、README 说明、必要时切 helper；根因解法是代码签名 |
| R2 | API 限流 | 默认 12h + rate-limited 文案 + 发布页兜底；日志记录 X-RateLimit-* |
| R3 | 自定义镜像不反代 API 路径 | 设置页与 README 明确要求 GitHub 兼容 API；失败即 check-failed（不猜）；同源约束见 §10.2 |
| R4 | F3 不成立 | **前置门禁 spike**；不成立则不进入阶段 B，退化为人工替换（§10.4） |
| R5 | 安装目录只读 | io-failed + Hint 提前拦截，不产生半状态 |
| R6 | 脚本中途失败留下半状态 | 分步回滚（§8.3）+ RESULT=fail:<step> 协议 + 下次启动提示与重试 |
| R7 | 新旧实例短暂并存 | 脚本先等旧 PID 退出（上限 --wait，默认 60s） |
| R8 | 非 release 构建被误升级/误判备份 | §7.2 与 §8.4 显式定义类别；备份名带 describe 串或时间戳 |
| R9 | UPX 误报 | 本次不改默认值；README 保留 COMPRESS=0 退路 |
| R10 | 自定义源被 MITM（http） | 非 loopback 强制 https + 同源约束 + 自定义源二次确认（§10.2） |
| R11 | 两实例并发 apply 互相 rename | ExeDir 排他锁（§7.6） |
| R12 | 新版本启动失败后「应用消失」 | 脚本存活探测 + 回滚 + 保留 pending（§8.3） |
| R13 | 真机夹具缺失导致 DoD 无法自动化 | §13.4 拆成「自动化到 ready + 脚本层真跑 + 人工点击一步」，并如实记录 |

---

## 16. 决策记录

| # | 问题 | 结论 |
|---|---|---|
| 1 | 做到哪一步 | **C**：一份 spec 覆盖 A（检测提示）+ B（下载自升级），实施分两步 |
| 2 | 更新源 | **B**：默认 GitHub，更新源可覆盖（形态：API JSON，方案 1） |
| 3 | 完整性校验 | **B**：发布流程补 checksums.txt（SHA256），比对失败拒绝安装 |
| 4 | 检查时机 | **C**：启动自动 + 手动 + 按间隔轮询（默认 12h） |
| 5 | 提示形态 | **B**：设置页「更新」区 + 侧栏角标，不做全局横幅 |
| 6 | 安装触发 | **B**：只下载不自动重启；补充为「显式点击后立即执行脚本」 |
| 7 | 脚本机制 | **②**：go:embed 静态脚本；Linux argv / Windows 环境变量（§8.5，语义仍是「零 shell 插值」）；Windows 纯 cmd |
| 8 | 命名与备份 | 读法 1：待安装文件带目标版本号；备份带当前版本号；只保留最近一份 |
| 9 | 边界 | 非 release 构建不自动检查（手动可查可升级）；无平台资产时明确告知 + 发布页 |
| 10 | UPX | 本次不改默认值（R9） |
| 11 | F3 | 实施前置门禁；不通过则只交阶段 A + 人工替换（§4/§10.4） |
| — | 修正 A | 脚本不做 SHA256 二次校验（避免 certutil），改由 Go 侧 sidecar 重算（§10.1） |
| — | 修正 B | CI 只改 release job 生成 checksums.txt（§11.4） |

### 16.1 第一阶段自审（本地）修正清单（16 项）

| # | 问题 | 修正 |
|---|---|---|
| 1 | 非 release 版本与语义化版本不可比，原设计让 dev 构建永远「已是最新」 | 引入版本**类别**（Clean/Describe/Dev），非 release 不做比较短路（§7.2） |
| 2 | 缺「跳过/取消跳过/删除已下载」的绑定与写配置通路 | Options.Save + 3 个绑定（§5.3） |
| 3 | 设置改了更新源/间隔后不生效 | Service.Reconfigure() + 三处接线（位置在 §16.2 又修正一次） |
| 4 | 前端「先快照后订阅」竞态 | UpdateInfo.Seq + 先订阅再快照（§12.2） |
| 5 | 角标 acknowledge() 语义含糊 | 改为记录「已确认版本号」（§12.2） |
| 6 | 残留自检把陈旧日志误判为「上次升级未完成」 | 改为日志末行协议 RESULT=ok/fail（§8.6/§7.5） |
| 7 | 成功升级后日志不清理 | 第 9 步成功时删日志（§8.3） |
| 8 | §9「仅 https」与「自定义源允许 http」+ 假源矛盾 | 改为默认 https、非 loopback 强制 https（§16.2 再收紧 + 同源约束） |
| 9 | Windows %~zVAR% 取大小是错的 | 改 for %%A in (...) do set PEND_SIZE=%%~zA（§16.2 补延迟展开） |
| 10 | Linux 依赖系统 setsid 二进制 | Go 侧 SysProcAttr{Setsid:true}；脚本内缺 setsid 退化 nohup（§8.3） |
| 11 | Windows 分离启动未说明子进程独立 | 补 CREATE_NO_WINDOW（§16.2 修正符号）与「不依赖父进程存活」 |
| 12 | 403 一律判限流 | 仅 X-RateLimit-Remaining: 0（或响应体含 rate limit）才判（§7.2；§16.2 同步 §9） |
| 13 | --size 在「重启后重试」路径不自洽 | --size 取磁盘 stat 值；Size==0 时不做进程内比较（§16.2 再修正守卫） |
| 14 | 生成脚本写盘失败无状态 | 归 io-failed（§7.4） |
| 15 | Plan.Launch 与 Target 重复 | 删除，计划字段重排（§16.2 修 Size 类型） |
| 16 | 测试与 DoD 未覆盖新守卫，§14 编号重复 | 补测试与编号（§16.2 继续补） |

### 16.2 第二阶段（干净 subagent 双路评审）修正清单

评审方式：两个全新上下文、只读的 subagent，分别做**事实核查**与**设计/可执行性**评审；结论均在本地逐条复核后才采纳。

| 来源 | 问题 | 处置 |
|---|---|---|
| 事实 B1 / 设计 B1 | pending 与 backup 命名不可区分、判据自相矛盾（会误报「重试升级」，甚至可被用来**降级安装**） | **采纳**：改为版本序唯一判别（§7.5）+ ApplyAndRestart 硬守卫 + 表驱动测试（§13.1/§13.5）。我另发现该规则会误把 sshore.exe 当候选，已加「候选必须以数字或 dev- 开头」 |
| 设计 B2 | §8.3 第 4 步用绝对路径比 glob 裸名，恒不相等 → **会删掉 pending**，升级 100% 失败；且 glob 作用于继承的 CWD | **采纳**：新增第 0 步 cd 到目标目录、统一 basename 比较、加「清理后 pending 仍存在」断言（§8.3/§13.2） |
| 设计 B3 | 参数校验会拒绝合法调用（--backup/--log 尚不存在） | **采纳**：只校验 TARGET/PENDING 存在；BACKUP/LOG 只校验父目录可写并在第 1 步创建日志（§8.2） |
| 设计 B4 | Windows cmd /c 既丢 argv，且 Go EscapeArg 不处理 % & ^ → 不满足自诩的「零 shell 插值」 | **采纳**：Windows 改走环境变量通道（§8.2/§8.5）；Linux 继续 argv。这是决策 6 的**实现细节调整**，语义不变 |
| 设计 B5 | 迁移表缺 verify-failed/io-failed 的重试边；not-writable 是死状态 | **采纳**：补重试边；not-writable 并入 io-failed + Hint（重试时重新探测可写性）（§7.1/§7.3/§12.1） |
| 事实 I5 / 设计 B6 | 门卫与「用户跳过」共用 skipped → 界面出现无对象的「取消跳过」 | **采纳**：门卫改 disabled；skipped 只保留一种语义（§7.1/§7.2/§12.1） |
| 设计 B7 | F3 不成立时「切 helper」是错的（helper 同样要把运行中的旧 exe 改名） | **采纳**：F3 退化为人工替换，helper 只对应脚本被 AV 拦截；F3 升为实施前置门禁（§4/§10.4） |
| 设计 B8 | checksums.txt 与资产同源，原文暗示安全闭环；http 自定义源可被 MITM 整体绕过 | **采纳**：写明能防/不能防；非 loopback 强制 https；**下载 URL 必须与源同源**；自定义源 apply 前二次确认；新增 sidecar 重算（§10.1/§10.2/§7.3/§7.4） |
| 设计 I1 | §9 与 §7.2 的 403 判据不一致 | **采纳**：§9 对齐 §7.2 |
| 设计 I2 | 重启后 ready 的 Size 为 0，被自己的守卫拒绝 | **采纳**：Size==0 时以磁盘为准（§7.4） |
| 设计 I3 | ClearSkipped 目标态 idle vs §12.1「立刻重查」矛盾 | **采纳**：统一为 checking |
| 设计 I4 | 校验只在下载时做一次，落盘后到 apply 之间可被替换 | **采纳**：sidecar 持久化 + apply 前重算（§7.3/§7.4/§10.1） |
| 设计 I5/I6 | 新版启动失败无回滚（用户只看到应用消失）；§7.5 前言与正文自相矛盾 | **采纳**：脚本存活探测 + 回滚 + RESULT=fail:launch；自检改为「唯一允许删除 RESULT=ok 的陈旧日志」（§8.3/§7.5/§9） |
| 设计 I7 | 无并发守卫，两实例可互相 rename | **采纳**：新增 lock.go + §7.6（Windows 命名互斥体 / Linux flock） |
| 设计 I8 | 下载无超时，源半死不活会永久停在 downloading | **采纳**：响应头 15s + 30s 无进度取消（§7.3/§9） |
| 设计 I9 | Check 可在 downloading/ready/applying 期间运行并覆盖状态 | **采纳**：§7.1 守卫 + §13.1 用例 |
| 设计 I10/I11 | DoD 只写 make ci，覆盖不到 windows-only 代码；update.cmd 行尾无保障 | **采纳**：DoD 加 go-windows 全绿；§11.2 .gitattributes；§11.3 在 Windows job 用真实 cmd.exe 跑脚本 |
| 事实 I4 / 设计 I12 | 新绑定未重新生成提交（所有构建都带 -skipbindings） | **采纳**：§3.1/§5.4 写进实施步骤 + DoD 第 3 条 |
| 设计 I13 | 脚本「等 PID 退出」超时语义未定义；僵尸 PID 会让 kill -0 恒成功 | **采纳**：超时=保留现场并非 0 退出（RESULT=fail:wait）；§13.2 区分「已 reap 的子进程」与「超时」两路 |
| 设计 I14 + 归档实测 | 归档条目带 ./ 前缀，裸名精确匹配取不到；未定义链接类型 | **采纳**：按 filepath.Base+Clean 匹配、拒绝 symlink/hardlink、多匹配报错、大小上限（§5.1/§13.1，F6 已实测） |
| 设计 I15 | §10.4「只影响三处」低估 helper 的影响面 | **采纳**：列出真实影响清单（§10.4） |
| 事实 M1 / 设计 M1 | §1.1 行号不准（transfer.go:20-26 是 PartSuffix、真正 rename 在 :49；Makefile 22/23；main.go 62/63） | **采纳**：§1.1 全部修正 |
| 设计 M2 | DoD 的前端测试数（187）与 grep 计数（189）不符 | **采纳**：不写死数字，以运行时输出为准（§14.1）。本地实测 npx vitest run 为 187 |
| 事实 M3 / 设计 M3 | Plan.Size 写成 string；§14 编号重复 | **采纳**：改 int64；编号修正 |
| 设计 M4 | Windows %%~zA 在 if 块内需延迟展开 | **采纳**：§8.3 写明 setlocal enabledelayedexpansion + !PEND_SIZE! |
| 设计 M5 | §9「下载新版本时清空跳过字段」是死规则 | **采纳**：删除该条 |
| 设计 M6 | --wait 未出现在 ScriptArgs 签名 | **采纳**：Plan.Wait + 各平台参数表（§5.1/§8.2） |
| 设计 M7 | 非 release 构建每次检查都提示有新版本 | **采纳**：文案注明「无法判定是否最新」（§7.2/§12.1） |
| 事实 M9 / 设计 M8 | awk 取文件名遇空格截断；「逐字节一致」过度要求 | **采纳**：sub(/^[^ ]+  /,…) + 改为「解析后逐项相等」（§11.4） |
| 事实 M8 | Normalize 要求写日志但 config 包无日志依赖 | **采纳**：回退/告警移到 Service 读配置处（§6） |
| 事实 M6 | update_skipped_version 双写者：前端全量 save 会静默清掉跳过 | **采纳**：写入只经 Options.Save；前端动作后 load() 重读；加测试（§6/§13.3） |
| 事实 M12 / 设计 §13.4 | 「沿用现有 XTEST 夹具」不成立（夹具未入库），且 setsid 启动的新实例会让 XTEST 失效 | **采纳**：§13.4 改为「自动化到 ready + 脚本层真跑 + 允许人工一步」，并把夹具与 XTEST 限制文档化 |
| YAGNI 建议 | rate-limited/not-writable 可合并为标志；Manual/Source/PendingLog 可省；BackupPatterns 不必导出；DoD 变异测试没预算 | **部分采纳**：not-writable 合并进 io-failed；BackupPatterns 不再导出；变异测试具体化为 6 个（§14.10）。**不采纳**：rate-limited 保留独立状态（它改的是「不要马上重试」的策略而不只是文案）；Manual/Source/PendingLog 保留（分别驱动文案颗粒度、自定义源二次确认与「上次升级未完成」提示） |
| 设计「缺失」项 | 磁盘空间检查、pending 哈希持久化、多 pending、解压白名单/链接类型、启动失败兜底、ETag | **采纳**前五项（§7.3/§7.4/§5.1/§8.3/§7.5）；**不做** ETag（12h × 60/h 够用，YAGNI） |

---

## 17. 后续（本期之外）

- 代码签名（SignPath Foundation / Azure Trusted Signing 等，资格与价格以官方为准）。
- 升级后会话恢复（隧道/SFTP/同步重放）。
- 断点续传与增量更新；ETag 条件请求。
- 备份的回滚界面（把「最近一份备份」变成一键回滚）。
- UPX 默认值调整讨论（体积 vs 误报）。
- 真机 GUI 夹具入库（xdrive.py 等），消除 §13.4 的「人工一步」。




