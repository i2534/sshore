# Windows / WebView2 验收（C 轮）——已执行，结论与证据

**一句话结论**：应用在真 Windows（VBox `win10` = `DESKTOP-LLF2GB6`，19045 x64）上能构建、启动、渲染；**SFTP 两个面板的右键菜单分级、禁用态在 Windows/WebView2 上逐项符合设计**（本次用 RDP 真实输入通道驱动，非注入）；同时发现**一个只属于 `windows/386` 构建的问题**：SFTP 连接时报 `exec: "ssh": executable file not found in %PATH%`（WOW64 重定向，见 §2）。

- 被测二进制：`build/bin/sshore.exe`（12.5 MB，`windows/386`，`Version=v0.7.2-71-gc5a59ba`）
- 构建命令（本机只有 **i686** mingw，没有 x86_64 ⇒ 只能出 386；仓库 `make windows` 目标写的是 `windows/amd64`）：
  `wails build -platform windows/386 -skipbindings -clean -nopackage -ldflags "-s -w -X main.Version=v0.7.2-71-gc5a59ba" -trimpath`
- 通道：应用由会话内 agent 在 **Session 1** 启动；GUI 由 **RDP 真实会话**驱动（`Xvfb :99` + skill 自带 `xfreerdp` → `127.0.0.1:13390`），宿主用自写 `xdo` 在 RDP 窗口上点击、用 `import` 截屏。

## 1. 通过项

| # | 项 | 结果 | 关键观察 | 证据 |
|---|---|---|---|---|
| C-1 | 构建/投放/启动/渲染 | **PASS** | 窗口 `SSHore v0.7.2-71-gc5a59ba` 正常渲染；WebView2 控件树 `Chrome_WidgetWin_0/1` + `Chrome_RenderWidgetHostHWND` + `Intermediate D3D Window` 齐备；`sshore` + 8×`msedgewebview2` 全 `Responding=True`、全部 `SessionId=1` | `c-win-launched.jpg` |
| C-2 | **行右键菜单**（本地行 `sshore.exe`） | **PASS** | 菜单 = `上传到远程 / 重命名 / 删除 / ── / 刷新`（**无「新建文件夹」**），是行级菜单 ⇒ WebView2 的 `contextmenu`/`e.target` 路由正确 | `c-win-rowmenu.jpg` |
| C-3 | **空白右键菜单**（面板空白处） | **PASS** | 菜单 = `刷新 / 新建文件夹 / 上传文件…`（面板级），与 Linux 侧形态一致 | `c-win-blankmenu.jpg` |
| C-4 | 菜单项**禁用态**渲染与惰性（未连接时） | **PASS** | 依赖连接的 `上传到远程`/`上传文件…` 被置灰：文字平均灰度 **173.2**，启用项 `刷新`/`新建文件夹` 为 **97.4 / 90.6**；点击置灰项**无任何反应**（菜单也不关）⇒ 与 spec §5.3「本地面板的 upload/upload-picked 落点依赖 remotePath，未连接时置灰」一致 | `c-win-blankmenu.jpg` + 像素量化 |
| C-5 | 启用项**可用** | **PASS** | 点 `刷新` 菜单项 ⇒ 菜单关闭；点 `新建文件夹` ⇒ 弹出模态「新建文件夹 / 新建文件夹名称 / 确定 取消」 | `c-win-newfolder-modal.jpg` |
| C-6 | **原生文件选择器**（已连接态） | **PASS** | 连上 `winlocal` 后（远程面板列出 `/C:/Users/lan`，85 项），空白菜单三项全部**深色启用**；点 `上传文件…` ⇒ 弹出原生「选择要上传的文件」对话框：`\\VBOXSVR\share\vbox-agent\sshore-b`、左侧导航（此电脑/图片/文档/下载/本地磁盘 (C:)/share/网络）、`sshore.exe 12,280 KB 应用程序`、`文件名(N):`、`打开(O)/取消`、过滤器 `All Files (*.*)` | `c-win-native-filedialog.jpg`、`c-win-menu-enabled-crop.jpg` |

## 2. 发现：`windows/386` 构建下 SFTP 连接不可用（candidate，待你决定是否处理）

真机点击「连接」（主机 `winlocal`）后日志：
```
2026-10-10 17:55:18 [ERROR] Error: exec: "ssh": executable file not found in %PATH%
```
客户机侧核实：
```
System32\OpenSSH\ssh.exe exists: True
SysWOW64\OpenSSH exists:         False
SysWOW64\OpenSSH\ssh.exe exists: False
PATH 含 OpenSSH:                 C:\Windows\System32\OpenSSH\
OS 架构: AMD64
```
⇒ 32 位进程经 PATH 解析 `ssh` 时被 **WOW64 文件系统重定向**：`C:\Windows\System32` → `C:\Windows\SysWOW64`，而后者**没有** `OpenSSH` 目录 ⇒ `os/exec` 找不到 `ssh.exe`。

**范围界定（重要）**：这是 **386 目标**的架构性后果。仓库 `Makefile` 的 windows 目标是 `windows/amd64`，本次因本机只有 i686 mingw 无法构建 amd64 验证。若 386 不是发布目标，这条只作为记录；若要支持 386，修法是显式解析 `%SystemRoot%\Sysnative\OpenSSH\ssh.exe`（或 `ProgramFiles` 下的 OpenSSH）而不是裸 `ssh`。

证据：`c-win-ssh-not-found.jpg`

**已修复并真机复验（`23df979` + `cf9eb92`）**：新增解析器按 `SSHORE_SSH_PATH` → `exec.LookPath` → 仅 Windows `%SystemRoot%\Sysnative\OpenSSH` → `System32\OpenSSH` → `%ProgramFiles%\OpenSSH` → 裸名 的顺序解析，接线在**真实 exec 层**（调用方与参数列表逐字未动）。用修复后的 386 二进制复跑：点「连接」**不再报 exec 错误**，远程面板列出 `/C:/Users/lan`（`「开始」菜单`、`3D Objects` …，85 项）⇒ 真 Windows/386 上 SFTP 可用。证据 `c-win-386-ssh-fixed-connected.jpg`；详见 `../windows-386-ssh-path-fix.md`。

已在 commit `cf9eb92` 修复（见 `windows-386-ssh-path-fix.md`）：按 `SSHORE_SSH_PATH` / `SSHORE_SFTP_PATH` → `exec.LookPath` → 仅 Windows 的 `%SystemRoot%\Sysnative\OpenSSH` → `System32\OpenSSH` → `%ProgramFiles%\OpenSSH` → 裸名 的顺序解析 OpenSSH 客户端，`Sysnative` 排在 `System32` 之前；接线只改真实 exec 的那一层（`osutil` 的 Runner / CtxRunner / Spawner / StartPipes 与 `config.defaultSSHGRun`），参数列表逐字未改。

## 3. 未验证（照实记录）

- 已连接状态下的**远端行菜单**（下载…/上传…）与真实传输/进度 UI 未逐项点击验证（远端**列目录**已验证：`/C:/Users/lan` 85 项）。
- 窗口行为细节（最大化/最小化/DPI 缩放/多显示器）未逐项验。
- 跨物理网络未覆盖。

## 4. 本轮排障中最值钱的一条：宿主端口被别人的隧道占用

前几轮 C 一直失败在 `SPNEGO … STATUS_LOGON_FAILURE [0xC000006D]`，而同一口令经 **GA 通道认证成功**（`VBoxManage guestcontrol … whoami` → `desktop-llf2gb6\lan`）。根因：

```
ss -ltnp | grep 13389
LISTEN 127.0.0.1:13389  users:(("ssh",pid=52430,fd=5))     ← 别人的 ssh 本地转发
```
VBox 配置的 `rdpfwd,tcp,127.0.0.1,13389,,3389` **绑定失败**（端口被占，VBox 静默不报错）⇒ 我一直在向 `13389` 后面的**另一台** Windows 认证（其 RDP 证书 CN = `DESKTOP-MTJ8KQ3`，本机应为 `DESKTOP-LLF2GB6`）⇒ 必然口令错误。

处置：另挂 `rdpalt,tcp,127.0.0.1,13390,,3389`（不影响别人的隧道），证书 CN 立刻变成 `DESKTOP-LLF2GB6`，登录成功 ⇒ 所有 C 项才得以执行。**同类坑**：早前宿主 `127.0.0.1:2222` 被我的验收代理占用，导致 VBox 的 `sshfwd` 静默绑定失败（`vboxctl ping` 报"客户机 SSH 不通"）。

**建议**（给 skill/后续）：跑 `vboxctl rdp-session` / `start` 前先 `ss -ltnp | grep -E ':13389|:2222'`，并核对 RDP 证书 CN 是否等于目标机器名；`start` 后也应校验两个转发真的绑上。

## 5. 未采用的其它通道（备查）

- 会话内 agent 的 `SendInput`：`setpos` 定位后 `click`/键盘扫描码在该 VM 上**无 UI 效果**（连开始菜单都点不开，而桌面持续刷新；`diag` 自报 `sendInputLastRet: 1`）⇒ 本轮改用 RDP 通道绕开。**另一个坑**：agent 的 `click` 不吃 x/y，返回 `cursor` 恒为屏幕中心，必须先 `setpos`。
- aigc 主机上那台 KVM win10（当时 running）：未使用。

## 6. 收尾（已执行）

- 客户机内应用已 kill；我加的 `rdpalt` 转发已删除；`vboxctl console-restore` 把会话 1 接回控制台；VM 已 ACPI 关机；占用锁已释放；共享目录投放件已删除。
- 宿主侧：我的 `xfreerdp`/`Xvfb :99` 已退出；**未触碰**别人占用 `13389` 的 ssh 隧道。
- 口令已按你的指示写入 `~/.dsh/.credentials.yaml`（`vboxctl` 自己读的那个文件），**没有**写进仓库/文档，避免进 git 历史。
