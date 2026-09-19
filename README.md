# sshore

**简体中文** | [English](README.en.md)

跨平台 SSH 端口转发 / 远程文件同步 / SFTP 管理工具。基于 Wails v2（Go）+ Vue 3 构建，
所有连接都复用系统自带的 OpenSSH。

## 界面预览

> 截图取自一次性本地 `sshd` + 演示数据，未使用真实主机。

| 端口转发 | 文件同步 |
| :---: | :---: |
| ![端口转发：隧道规则、实时状态与日志面板](docs/images/forward.png) | ![文件同步：同步计数与事件日志](docs/images/sync.png) |
| **SFTP** | **设置** |
| ![SFTP：双栏浏览、最近位置与传输队列](docs/images/sftp.png) | ![设置：主题、字号、字体与启动项](docs/images/settings.png) |

## 环境要求

- Go 1.26
- Node.js 22+（LTS）
- 系统 `PATH` 中需有 OpenSSH（`ssh`；默认的 `gosftp` 传输底座不再需要 `sftp` 二进制，
  用 `sftp_transport = "batch"` 回退旧后端时才需要）
- wails CLI（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）

## 下载安装

在 [GitHub Releases](https://github.com/i2534/sshore/releases) 下载对应平台的压缩包
（`sshore-<version>-linux-amd64.tar.gz` / `sshore-<version>-windows-amd64.zip`），
解压后直接运行 `sshore` 即可。

- 认证完全交给系统 OpenSSH（密钥 / ssh-agent / `~/.ssh/config`），应用本身不存储任何凭据。
- Linux 需要 webkit2gtk-4.1（Ubuntu/Debian：`libwebkit2gtk-4.1-0`）。
- Release 产物已用 UPX 压缩，运行时不依赖 UPX。

## 更新与升级

sshore 会按设置里的间隔向**更新源**查询最新 Release，有新版时在侧栏「⚙ 设置」上亮角标；
检查、下载与校验的进度都在「设置 → 更新」里可见。

### 更新源与校验

- 默认更新源是 GitHub API：`https://api.github.com/repos/i2534/sshore/releases/latest`
  （可配置的源是 `https://api.github.com/repos/i2534/sshore`，程序请求 `<源>/releases/latest`）。
- 可在「设置 → 更新」里把更新源换成镜像/内网地址（配置项 `update_source`），
  但必须提供 **GitHub 兼容的 API**（同样返回 `<源>/releases/latest` 的 JSON）。
- **非本机地址必须用 `https://`**；只有 loopback（`127.0.0.1` / `localhost` / `::1`）
  允许 `http://`（离线镜像与端到端自测用）。非 loopback 的 `http://` 源会被回退到默认源
  （服务层记一行日志）；设置页对仍为非 `https://`（且非 loopback `http://`）的输入框内容
  持续显示「非本机地址必须使用 https。」。不带 `http(s)://` 前缀的源在配置加载时就被规范化
  清空，同样回落到默认源。
- 检查时机：**启动后自动检查一次** + 设置页「立即检查更新」+ 每
  `update_check_interval_hours` 小时轮询（默认 12，可选 12/24/48/168；`0` = 关闭轮询，
  手动检查仍然可用）。取消勾选「自动检查更新」后不会再自动发起请求。
- **开发/非 release 构建不参与自动检查**：`Version` 是 `dev` 或 `git describe` 串
  （如 `v0.6.0-80-gc2d2a36`）时与 Release tag 不可比，自动检查会被直接跳过（状态停在
  「尚未检查更新」），设置页会标注「本地构建 …（dev）」；手动「立即检查更新」仍然可用。
- 校验强度：发布产物配套 `checksums.txt`（SHA256），压缩包下载后比对；**不匹配就拒绝安装**，
  临时文件与半成品都不会落进程序目录。校验通过的二进制会连同哈希 sidecar
  （`sshore.<目标版本>[.exe].sha256`）一起落盘，点「重启并升级」前再重算一次。
- 能防什么：传输损坏、半包、资产错发/错配。**不能防**更新源被攻破，或 GitHub 账户被盗后
  发布的被篡改版本 —— checksums.txt 与压缩包同源，挡不住源本身被攻陷；这类风险只能靠
  HTTPS 与上游账户安全。
- 该 Release 没有本平台包（例如平台尚未提供产物）时会明确提示「本平台暂无可用包」，
  并给出「打开发布页」让你手动下载。

### 升级流程（必须由你点一下）

1. 有新版时点「下载更新」：下载压缩包 → 比对 `checksums.txt` 的 SHA256 → 解包出待安装文件
   （与当前二进制同目录，文件名带**目标版本**，如 `sshore.v0.7.0` / `sshore.v0.7.0.exe`）
   → 写入同名 `.sha256`。解包只认白名单里的 `sshore` / `sshore.exe`，拒绝符号链接与路径遍历条目。
2. 点「重启并升级」：重算哈希、写出一次性升级脚本、分离启动它，然后程序自己退出。
3. 脚本等旧进程退出 → 把当前二进制备份为 `sshore.<当前版本>`（**只保留最近一份备份**）
   → 把待安装文件换成正式名 → 启动新版；新版起不来会**自动回滚**并保留待安装文件。
4. 成功会删除脚本与日志；失败会保留 `sshore-update.log`，下次启动时「设置 → 更新」会展示该
   日志（截断 2000 字符）并提示「上次升级未完成」；日志末行是 `RESULT=fail:<步骤>`，同时给出
   「打开发布页」以便手动下载替换。

升级涉及的文件都在 `<ExeDir>`（二进制所在目录）：

| 文件 | 命名 |
| --- | --- |
| 待安装文件（带**目标版本**） | `sshore.<目标版本>`（Windows：`sshore.<目标版本>.exe`） |
| 待安装校验 sidecar | `sshore.<目标版本>[.exe].sha256` |
| 备份（带**当前版本**，只留最近一份） | `sshore.<当前版本>`（dev 构建为 `sshore.dev-<时间戳>`） |
| 升级日志 | `sshore-update.log` |
| 升级锁 | `.sshore-update.lock`（Linux；Windows 用命名互斥体，无文件） |

锁文件只是内核锁的载体，进程退出时自动释放，**不需要也不应手动删除**（删掉会引入两个实例
同时升级、互相覆盖二进制的风险）。

### 隐私

检查更新只向更新源发出一个带版本号 User-Agent 的 HTTPS 请求，不上报任何本机信息。
取消勾选「自动检查更新」后，只有你手动点「立即检查更新」时才会请求。
使用非默认更新源时，点「重启并升级」前会再弹一次确认。

### 手动升级（不依赖脚本）

```bash
# Linux：解压后覆盖到 sshore 所在目录（先建好解压与安装目录，首次粘贴即可跑通）
mkdir -p /tmp/upd ~/bin
tar -xzf sshore-v0.7.0-linux-amd64.tar.gz -C /tmp/upd
install -m 0755 /tmp/upd/sshore ~/bin/sshore
```

```powershell
# Windows：退出 sshore 后，把解压出来的 sshore.exe 覆盖到原位置即可
```

如果安装目录不可写（如 `/usr/bin`、`Program Files`），设置页会提示「安装目录不可写，请按
手动步骤升级（可打开发布页手动下载）」——具体步骤即本节上面的手动升级命令。

### 杀毒软件误报

自升级会「联网下载可执行文件并替换自身」，这和部分恶意软件的行为特征重合；Release 产物又经过
UPX 压缩（「尺寸优化」一节已说明 UPX 本身易被误报），且当前二进制**未做代码签名**。如果杀软
（Windows Defender、360、火绒等）拦截了下载、替换或启动新版：

1. 按杀软能力尽量只放行 `sshore` / `sshore.exe` 与升级脚本（不必整个 `<ExeDir>` 目录，
   以免放行此后落在该目录的任何文件），然后重试升级；
2. 若仍被拦截，用上面的**手动升级**步骤手动替换二进制；
3. 或自行 `make both COMPRESS=0` 构建不带 UPX 的版本。

## 构建

```bash
make both     # 清理后同时构建 Linux 与 Windows 二进制（默认，尺寸优化）
make linux    # 仅 Linux amd64
make windows  # 仅 Windows amd64（打包：图标经 .syso 嵌入）
make build    # 当前平台
```

或直接使用 Wails CLI：

```bash
wails build
```

### 尺寸优化

`make` 目标会剥离符号/DWARF（`-ldflags "-s -w"`）、传递 `-trimpath`，
并默认用 UPX 压缩最终二进制（Linux ≈3.7 MB，Windows ≈4.5 MB）；
同时把 `git describe --tags` 得到的版本经 `-X main.Version=...` 注入，
供应用内「设置 → 帮助」显示。

- 设置 `COMPRESS=0` 可跳过 UPX（例如杀毒软件误报 UPX 打包的 Go 二进制，
  或你更看重启动速度）：`make both COMPRESS=0`
- 需要 `PATH` 中存在 UPX；若缺失，`wails build -upx` 会告警并跳过压缩
  （Wails 仅在安装 UPX 时才压缩）。
- 构建 Windows 时**不要**传 `-nopackage`：Wails 仅在启用打包（`Pack=true`）
  时生成带图标的 `.syso` 资源，`-nopackage` 会得到无图标的 exe。
  `make windows` 目标特意保留打包。

## 开发运行

```bash
wails dev
```

## 功能特性

- **SSH 端口转发**：本地 `-L`、远程 `-R`、动态 SOCKS `-D`、跳板机 `-J`
- **自动重连**：按规则开关。异常断开后按退避重试（日志显示
  `Ns 后第 N 次重连`），卡片状态如实进入「重连中 / 错误」，不会静默假装已连接
- **SFTP 文件管理**：双栏浏览 / 多选（Ctrl 点选、Shift 区间、Ctrl+A）/ 批量下载·上传·删除 /
  右键菜单 / 拖动投递（双栏互拖、从系统拖入、拖到子目录移动）/ 双侧即时过滤；每面板的
  📍 位置下拉（预设：主目录/桌面/下载/文档/根目录 + 配置文件自定义；Windows 全部盘符 / 书签 / 最近位置，各上限 20，远程按主机隔离）与 ☆ 收藏 / 双侧递归深搜
  （可随时取消、默认 5 层、最多 500 条命中，不可读目录如实标注）/「显示隐藏文件」开关；
  传输队列表含方向、源到目标、跳过与失败原因
- **远程文件夹同步**：监控远端目录**或单个文件**，变化同步到本地；优先用
  `inotifywait`，不可用时自动降级为轮询（也可用 `force_poll` 强制轮询）；
  支持 `max_depth` 递归深度（`-1` 为不限）与排除项；默认不删除本地文件
  （镜像删除需显式开启，且仅对目录规则生效）；本地被改动时不覆盖，改为进入
  冲突队列由用户裁决（保留本地 / 用远端覆盖 / 另存远端版本）；批量删除会挂起
  等待二次确认；失败的传输可一键重试
- **设置**：深色 / 浅色 / 跟随系统主题，字号（小 / 标准 / 大），英文与中文字体
  栈，「启动后自动连接转发通道」，以及新建同步规则的默认自动重连值——全部
  持久化在 TOML 配置里
- **配置**：只读解析 `~/.ssh/config`（用于枚举主机）；隧道与同步规则保存在
  `~/.config/sshore/sshore.toml`（Windows 为 `%APPDATA%\sshore\sshore.toml`），
  以 0600 权限原子写入
- **命令导入**：将粘贴的 `ssh -L/-R/-D ...` 命令解析为规则
- **实时日志面板**：隧道 / SFTP / 同步事件的内存环形缓冲（1000 条）；
  可按规则单独切换过滤（规则卡片「日志」按钮 / 日志面板 chips），ssh 子进程的
  stderr 逐行采集进面板——隧道内部错误不再被吞掉
- **规则校验**：创建/编辑/导入时拒绝空目标主机（`-L 23080::3080` 这类
  坏规则会让 ssh 解析空主机名失败：连接即 RST 而进程不退出，界面误报
  connected）；本地/远程转发绑定失败时 `ExitOnForwardFailure` 使隧道
  直接进入 error 状态
- 认证使用系统 OpenSSH（密钥/agent/config），因此 GUI 不支持密码与
  keyboard-interactive 认证——请为你的主机配置密钥或 agent 认证

## 配置

`sshore.toml` 可以直接手改：缺失的键回落到默认值，显式的空/非法值
（主题、`font_scale`、`max_depth`、`poll_interval_s`、空排除列表）会在加载时
被规范化，不会静默改变行为。

```toml
legacy_migrated = true           # 旧 recent_sftp 已迁移（不再写回；只保留解析一版以便降级）

[app]
  theme = "dark"                 # dark | light | system
  font_scale = 1.0               # 0.9 | 1.0 | 1.15
  auto_start_on_launch = false   # 启动后自动连接转发通道
  auto_reconnect_default = true  # 新建同步规则的默认自动重连值
  sftp_transport = ""            # ""/auto（= gosftp 内置默认）| "gosftp" | "batch"（旧后端回退）

[[tunnels]]                      # 端口转发规则（-L / -R / -D）
[[syncs]]                        # 远端 → 本地 的同步规则
[[bookmarks]]                    # 手动固定的位置（scope = local | remote）
# 位置预设不在本文件里：见同目录的 presets.toml（首次启动自动生成，带注释说明）
[[local_recent]]                 # 本地最近位置（自动记录，上限 20）
[[remote_recent]]                # 远程最近位置（按 host 隔离，上限 20）
[[recent_sftp]]                  # 旧字段：首次启动已迁移成上面两项，只保留解析能力以便降级
```

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

## SFTP 传输底座（`sftp_transport`）

SFTP 有两条传输实现，可在设置页或配置里切换；连接与认证始终交给系统 `ssh`
（密钥 / ssh-agent / `~/.ssh/config` / 跳板机），只是传输与列目录由谁驱动不同：

- `gosftp`（**默认**）— 长连 `ssh -s <host> sftp` 起 SFTP 子系统，由 `pkg/sftp` 直接驱动
  协议；**传输独占一个新建会话（并发上限 1）**，**列表/变更类复用空闲会话（每主机最多 2 个）**，
  带真实字节级进度、整批取消、失败重试与 `.part` 续传。
- `batch`（兼容，保留一个发布周期）— 每次操作一个 `sftp -b` 进程，解析 `ls -la` 文本输出；
  无进度、无续传，是升级前的旧行为。

**切换优先级：环境变量 `SSHORE_SFTP_TRANSPORT` > 配置项 `[app] sftp_transport` > 内置默认
（`gosftp`）。** 非法值不阻断启动：非法**环境变量**值会被忽略、继续按配置项取值；非法
**配置项**值（含空串 / `auto`）在加载时被规范化（`internal/config/store.go` 的 `Normalize`），
最终回落到内置默认（`internal/sftp/backend.go` 的 `resolveTransport`）。

```bash
SSHORE_SFTP_TRANSPORT=batch sshore    # 临时回退旧后端（无需改配置）
SSHORE_SFTP_TRANSPORT=gosftp sshore   # 显式选定新后端
```

- 设置页「传输 → SFTP 传输后端」的下拉等价于写配置项 `sftp_transport`
  （`""`/`auto` = 内置默认，即 `gosftp`）。
- 想**回退整轮默认**（例如真机验收在本机失败）：把 `internal/sftp/backend.go` 的
  `const defaultTransport = KindGo` 改回 `KindBatch` 重新构建即可；这不影响上表两个显式覆盖。
- `batch` 后端与开关计划在 **v0.8 删除**，届时只保留 `gosftp`。

### 进度、取消与续传（仅 `gosftp`）

- **进度**来自后端真实的已传字节数（节流上报 + 结尾强制补一帧末值）：单文件显示百分比、
  速度与 ETA；目录传输先枚举再逐文件计数，枚举超预算时退化为「已完成 N 个文件」。
- **取消**是**整批**语义：取消当前在传项（关会话让在飞 IO 立刻失败，目标名不会出现半截
  文件），同批尚未派发的项直接标记为「已取消，停止后续项」。取消有三种如实状态，绝不假装
  取消成功：① `Cancel` 返回 `true` 且该项以失败收尾 ⇒ 终态「取消」；② `Cancel` 返回
  `false`（已提交 / 不在传输中 / 从未开始）且该项仍在跑 ⇒ 进行态显示「未能取消」，此后若
  失败则终态「失败」并给出原始错误；③ 请求过取消但该项最终**提交成功** ⇒ 终态「完成」，
  备注「取消过晚（已完成）」，目标文件保留。
- **重试**重新执行失败项；**续传**复用上次失败保留下来的 `.part`，从已落盘字节继续。
  续传前会核对源文件的大小/修改时间指纹：源在两次传输之间被改写时不会拼接出坏文件，
  而是整份重传（`.part` 大小等于源时直接提交，不重传）。
- 每次传输在同一目标上互斥（host + 用户 + 方向 + 目标）：重复触发会立刻报错，不会静默排队
  后互相覆盖。

### 内部临时文件命名（面板隐藏 / sync 忽略）

原子提交的临时名统一带中缀 `.sshore-sftppart-`（唯一来源 `internal/sftp.PartMarker`）：

- 常规：`<原名>.sshore-sftppart-<id8>-<rand>`（与目标同目录）
- 备份（无 `posix-rename` 时的 backup-swap）：`<原名>.sshore-sftppart-bak-<rand>`
- 超长名退化（原名超过 200 字节时避免 `ENAMETOOLONG`）：
  `.sshore-sftppart-<id8>-<rand>` / `.sshore-sftppart-bak-<rand>`

这些名字**不会出现在文件面板里**（前端按中缀过滤），也**被同步 / 变更监控忽略**
（`watch.IsInternalTemp` + `sync.inScope`），不会被当成新文件同步出去或下载下来。

### 已知限制（不掩饰）

- **必须在交互桌面会话里运行**：Session 0（Windows 服务 / 非交互会话）中由本应用 spawn 的
  `ssh.exe` 会在 SFTP INIT 之后无响应（观测 ≥15–25s；**没有**做「更长等待是否自行恢复」的
  实验）。这是**本机这次混装组合**（客户端 9.5p1 + 唯一服务端 sshd 9.2p1；本机没有第二套
  sshd 可作对照）下的观测：被测的 4 个 console 创建标志均无效，window station / desktop
  未试，也不对其它 Windows / OpenSSH 组合作一般性归因。结论：**不要以服务方式启动**；
  用计划任务也要用 `/it` 投到交互会话。
- **超预算的目录扫描只传已枚举前缀**：单次树扫描超过 **20000 个文件** 或 **5s** 预算时，
  放弃继续枚举，只传输已经枚举到的那部分；调用返回**成功**，进度用 `-1` 分母
  （`total`/`filesTotal` 为负）表示分母未知 —— 也就是「传输成功」不等于「整棵树都传完了」。
- **`Atomic=false` 路径直写、无 `.part`、无 journal**：旧的四参 `Get/Put` 与 legacy 树面
  `GetRecursive/PutRecursive`（含 `internal/sync` 的用法）都直接写目标文件、不产生可续传的
  `.part`、也不写 backup-swap 的 journal；原子性由 sync 自己（它的 `.part` + rename）负责。
- **临时文件路径被信任为私有**：`.part` / `.sshore-sftppart-bak-` 的路径（本地或远端）假定只有
  本应用会写；**不防御同一台机器上其它进程**在传输期间改写/截断它。提交前置只有基于字节计数
  的 `done == total`（`internal/sftp/copy.go` 的 `decideCommit`），它看不出「稀疏空洞」——
  例如越过 EOF 的续写在服务端会被零填充，计数依然相等，于是可能把一个带洞的文件提交成最终
  文件。因此不要把多用户可写 / 共享目录当成可信传输目标。
- **目录传输先建会话再枚举**：`GetTree` / `PutTree` 在枚举之前就取走唯一的传输会话
  （`AcquireTransfer`，即一次新建握手），整段扫描窗口都占着传输并发额度。扫描相并非都不发
  会话 IO：`GetTree` 的远端枚举走 `s.Conn.ReadDirContext`（`internal/sftp/copy.go` 的
  `scanTree`），取消靠 ctx；只有 `PutTree` 的本地 `WalkDir` 不碰会话。**即使扫描刚开始
  就被取消，也已经付过一次握手**；此外扫描期间后续传输无法开始。
- **陈旧 `.part` 清理只扫配置里的 LocalRecent 目录**：启动时只清理最近使用过的本地目录
  （超过 7 天的临时文件），不做全盘索引；其它目录里的孤儿 `.part` 需要手动清理。
- **`RemoveRecursive` 有硬上限**：树深度 > 64 层或条目数 > 100000 时整体拒绝删除
  （收集阶段就失败，不会删一半），**没有覆盖开关**。
- **`Connected` 是粘性连接意图，不是探活**：一次成功握手后一直为真，直到显式
  `Disconnect`/`CloseAll`；池内会话被逐出或关闭都不会翻转它。不要把它当存活探针用。
- **多主机 journal 恢复只处理可达主机的条目**：按 `(host, user)` 分组，只探条目自己的主机；
  主机不可达的那组原样保留到下次；**没有 host 的旧条目永远不动作、也不删除**。
- **启动时不恢复**：journal（backup-swap）恢复发生在**优雅退出**路径；进程崩溃 / 断电后的
  中断提交留到下一次优雅退出再收敛。
- **退出宽限期 5s**：退出时最多等在飞传输 5s，超时即强制取消并关会话。卡在不可中断本地写
  的传输仍可能在 `CloseAll` 返回后才收尾（它不会写 journal 意图，恢复时是无歧义状态）；
  极端时序下还有一条逃过两次 `CloseAll` 池快照的窄窗口（低概率）。

## 架构

- `internal/config` — 解析 `~/.ssh/config`（枚举用 kevinburke/ssh_config，
  权威字段用 `ssh -G`）并读写原子化的 TOML 配置存储
- `internal/forward` — 生成/管理长驻 `ssh -N` 子进程、生命周期状态机、
  端口预检、错误分类、自动重连退避
- `internal/sftp` — 门面 + 双后端（默认 `gosftp` = `pkg/sftp` 长驻会话池，
  `batch` = 旧的 `sftp -b` 进程），`pkg/sftp` 驱动协议
- `internal/sync` — 同步引擎：状态机、冲突队列、删除闸门、传输与路径映射
- `internal/watch` — 远端变更探测：`inotifywait` 探测 + 轮询降级
- `internal/sshconn` — ControlMaster socket 路径与 ssh/sftp 连接参数的唯一来源
- `internal/importer` — 将 `ssh -L/-R/-D` 命令行分词为规则（注入安全）
- `internal/osutil` — 跨平台进程执行、取消与信号处理
- `frontend/src` — Vue 3 UI（左侧导航模块切换：转发 / 文件同步 / SFTP）+ Pinia 存储

## 测试

```bash
go test ./...          # Go 子系统测试（mock ssh/sftp）
cd frontend && npx vitest run   # 前端单测（store、同步工具函数）
```

`make ci` 一条命令执行 CI 的全部内容（vet + `-race` Go 测试 + 前端测试）。

## E2E

```bash
make e2e    # 等价于：bash e2e/test_local.sh
```

`e2e/test_local.sh` 会启动一个临时本地 sshd，验证 sshore 依赖的 OpenSSH 行为：
`ssh -G` 别名解析、`-N -L` 本地转发绑定、`sftp ls -l` 输出解析。
需要 `/usr/sbin/sshd`、`ssh-keygen` 和 `python3`。

**直接在 Windows（或任意机器）上跑 e2e 用例时注意**：`internal/sftp` 与 `internal/sync` 的
e2e 用例要求 `SSHORE_E2E_HOST` / `SSHORE_E2E_REMOTE` / `SSHORE_SFTP_TRANSPORT` **三项齐全**。
其中：

- 缺 `SSHORE_E2E_HOST` / `SSHORE_E2E_REMOTE` ⇒ **Skip**（`go test ./...` 在无网络/无 sshd 时无声通过）；
- 已设 `SSHORE_E2E_HOST`、却缺 `SSHORE_SFTP_TRANSPORT` ⇒ **故意 `t.Fatalf`**
  （`SSHORE_SFTP_TRANSPORT 未送达：后端身份无法证明`）—— 这是防假绿设计，不是缺陷：
  两轮迭代都悄悄跑同一个后端会让"后端矩阵"变成假矩阵。

## CI

`.github/workflows/ci.yml` 在 push/PR 时运行以下作业：

- `go` — Ubuntu 上 `go vet` + `-race` Go 测试
- `go-windows` — 在真实 Windows runner 上跑同一套 vet/测试（覆盖 `osutil`
  的 Windows 分支、`%APPDATA%` 配置路径、Windows OpenSSH 的 `ls` 输出形状）
- `frontend` — `vitest run` + Vite 构建
- `build-linux` / `build-windows` — 分别构建 Linux（webkit2gtk-4.1）与
  Windows（图标经 `.syso` 嵌入）的 `wails build`，均含 UPX 压缩，并打包成
  带版本与平台名的压缩包
- `release` — 打 `v*` tag（或手动 workflow_dispatch 指定 `release_tag`）时
  把上述压缩包发布到 GitHub Release
