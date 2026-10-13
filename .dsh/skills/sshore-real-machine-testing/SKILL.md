---
name: sshore-real-machine-testing
description: "Use when 要在这个仓库（sshore / sshkit）上做真机验收，而不是只跑单测与构建：验证 SFTP GUI 行为（两侧刷新、右键菜单分级与置灰、排序与最后路径持久化、休眠/断链自愈、传输落点与内容哈希），或在 Linux 桌面（Xvfb + XTEST 工作台）与真 Windows（VBox 客户机 + RDP 真实输入通道）上跑一遍；也用于复现『主机休眠/网络中断后仍显示已连接但操作报错』这类链路故障、验跨平台构建差异（linux/amd64、windows/amd64、windows/386），以及把结论沉淀成可复核的证据。"
---

# SSHorE 真机验收（sshore-real-machine-testing）

本 skill 解决一件事：**把"单测/构建都绿了"推进到"在真桌面、真 SFTP 协议、真断链下确实按设计工作"，并留下别人能复核的证据。**
适用范围仅限本仓库（`.dsh/skills/` 为项目级 skill，随仓库版本管理；不要复制到公共 skills）。

## 0 什么时候用它

- 改了 SFTP 视图/刷新/右键菜单/持久化/自愈/传输相关代码，准备合并前要"真机跑一遍"。
- 要复现或验证链路故障：主机休眠、网络中断、SSH 会话被杀、代理黑洞。
- 要在 Windows 上确认 WebView2 特有的菜单/原生对话框/窗口行为。
- 要回答"能不能在这台机器上交叉构建某个平台"这类问题（先构建，别猜工具链）。

**不适用**：纯逻辑改动（用 `make ci`/单测即可）、纯前端样式、以及需要 root 才能造的网络故障（见 §4 的替代做法）。

## 1 一分钟上手

```bash
cd <repo>                                   # /home/lan/workspace/scripts/sshkit
export GOCACHE=$PWD/.gocache                # 必须：沙箱里 ~/.cache/go-build 只读
make ci                                     # 门禁基线：前端构建 + vet + go test -race + vitest

# 构建三选一（GUI 验收要真二进制）
make build                                  # linux/amd64（依赖 webkit2gtk-4.1，标签 webkit2_41）
wails build -platform windows/amd64 -skipbindings -nopackage -trimpath   # 真·发布目标，无需 mingw
wails build -platform windows/386  -skipbindings -nopackage -trimpath   # 能出，但见 §6 的 WOW64 坑

# 通道 A：Linux 桌面工作台（默认，最省事）
export ACC=/tmp/sshkit-acc && mkdir -p $ACC/{home,log,run}
gcc -O2 -o $ACC/xdo .dsh/skills/sshore-real-machine-testing/tools/xdo.c -lX11 -lXtst
Xvfb :99 -screen 0 1600x1000x24 -nolisten tcp &
setsid env HOME=$ACC/home DISPLAY=:99 XDG_RUNTIME_DIR=$ACC/run \
  LIBGL_ALWAYS_SOFTWARE=1 WEBKIT_DISABLE_COMPOSITING_MODE=1 GDK_BACKEND=x11 \
  build/bin/sshore >>$ACC/log/app.log 2>&1 &

# 通道 B：真 Windows（见 §4）
export PATH=~/.dsh/skills/vbox-win/bin:$PATH && vboxctl status && vboxctl who
```

证据一律落在 `docs/superpowers/reports/<日期>-<特性>-evidence/real/`（实例见 §9）。

## 2 硬规则（都是踩出来的）

1. **隔离用户环境**：应用必须用 `HOME=<临时目录>` 启动 ⇒ 用户真实的 `~/.config/sshore/sshore.toml` 不会被读写；验收结束后核对它没被动过。
2. **临时 ssh 别名要可还原**：若为了让被测应用能解析主机而在真实 `~/.ssh/config` 追加别名块，先备份（`cp` 到临时目录 + 记 md5），收尾**整块删除**并 `md5sum` 与备份逐字节比对。
3. **所有 Go/wails 命令带 `GOCACHE=$PWD/.gocache`**；`wails build -clean` 会**清空 `build/bin/`**（把别的平台产物一起删掉），需要时再重建。
4. **不要用 `pkill -f`/`pgrep -f` 匹配可能出现在自己命令行里的模式**：命令后文一旦出现同样的字面量，`pkill` 会先杀掉自己（本 skill 的教训：`build/bin/sshore`、`proxy.py`、`Xvfb :99` 都中过）。用 `pgrep -x <精确进程名>` 取 pid 再按 **数字** kill。
5. **每次点击前先截图定位**：面板布局会变（例如**传输队列面板一出现，两侧面板整体上移**，过滤框 y 从 510 → 460）。不要复用"上一次的坐标"。
6. **结论必须有原始证据**：日志原文（含时间戳）、截图、以及**传输类必须双侧哈希核对**（宿主 md5/sha256 ↔ 客户机 Get-FileHash），不许用"看起来对"下结论。
7. **共享资源礼仪**：VBox 客户机可能同时有别人在用 ⇒ 用 `vboxctl who` 看占用、只用 `C:\vbox-agent\` 与 `Z:\vbox-agent\`、**不要**动别人的进程/隧道/端口（见 §6 的 13389）。

## 3 Linux 通道（Xvfb + XTEST）

- 输入驱动 `tools/xdo.c`（自包含，编译见 §1）。命令集：`list / focus / click / dclick / rclick / slowclick / clickmod <shift|ctrl> / move / key / type / hotkey / drag / scroll / movewin`。
- 截图：`DISPLAY=:99 import -window root /tmp/x.png`（`import`-based，之后再转 JPEG 入库）。
- **模拟链路故障（无需 root）**：跑 `tools/proxy.py` 做可控 TCP 转发，把应用要连的主机别名指向代理端口，然后用控制文件切 `normal / blackhole / blackhole-all / reset`：
  - `blackhole` ⇒ 已验证的连接停摆、新连接仍通 ⇒ 测"探针超时=未知、不改状态"；
  - `blackhole-all` ⇒ 全黑洞 ⇒ 配合 ssh 自身 `ConnectTimeout` 测"确凿失败=失效+重连"；
  - `reset` ⇒ 直接拆连接 ⇒ 测 `Connection reset` 类断链。
- 另一种更贴近"主机休眠"的做法：**直接 kill 应用的 `ssh … -s <host> sftp` 子进程**（`pgrep -f 'acc-targe[t] sftp'` 这类**括号技巧**避免自匹配），然后切换侧栏页签触发探活。
- 观测点：应用内日志面板（`[INFO] 探活耗时 Nms`、`[WARN] 远端连接已失效（主机休眠或网络中断），正在重连…`、`[INFO] 已重新连接 <host>`、`[ERROR] Error: sftp …: <原文>`、`批量upload/download N 项：成功/跳过/失败`）。

## 4 Windows 通道（VBox 客户机 + RDP 真实输入）

优先用 **RDP 真实鼠标键盘**，不要依赖会话内 agent 的 SendInput（见 §6）。

```bash
export PATH=~/.dsh/skills/vbox-win/bin:$PATH
vboxctl who && vboxctl start                 # 侵入类操作会自动认领锁
# 关键：宿主 13389 可能被别人的 ssh 隧道占用 ⇒ 先核验证书 CN 是不是目标机器
ss -ltnp | grep -E ':13389|:2222'
VBoxManage controlvm win10 natpf1 "rdpalt,tcp,127.0.0.1,13390,,3389"     # 备端口
echo | openssl s_client -connect 127.0.0.1:13390 2>/dev/null | openssl x509 -noout -subject
# 凭据只从 ~/.dsh/.credentials.yaml 的 vbox_win10_password 读（vboxctl 同款读法）
setsid env DISPLAY=:99 ~/.dsh/skills/vbox-win/bin/xfreerdp \
  /v:127.0.0.1:13390 /u:lan /p:"$PW" /cert:ignore /size:1600x900 +clipboard &
DISPLAY=:99 import -window root /tmp/win1.png        # 之后用 xdo 在 RDP 窗口上点击
```

- 部署：宿主 `/home/lan/virtual/share/vbox-agent/<子目录>/` ↔ 客户机 `Z:\vbox-agent\<子目录>\`；应用由会话内 agent 启动（`vboxctl agent '{"op":"run",…}'`）。
- 客户机侧读数/造夹具：`vboxctl ps <脚本.ps1>`（SSH/PowerShell；**脚本不要带 BOM**，`vboxctl gui` 暂存链路会把 BOM 变成字面字符）；哈希用 `Get-FileHash -Algorithm MD5|SHA256`。
- 收尾：kill 客户机内应用 → 删我加转发 → `vboxctl console-restore` → `vboxctl stop` → `vboxctl release`；宿主侧 kill `xfreerdp`/`Xvfb`。

## 5 验收矩阵（可直接复用）

| 组 | 项 |
|---|---|
| 刷新 | 工具栏刷新两侧同刷且保持选中；右键/面板刷新只刷本面板 |
| 菜单 | 空白右键=面板级（刷新/新建文件夹/上传文件…）；行右键=行级（下载…/上传…/重命名/删除/刷新）；有选中时空白右键**不清空**选中；未连接时依赖连接的项**置灰且点击惰性**（可量文字灰度对比） |
| 持久化 | 排序（本地/远程各一份）落盘并在重启后恢复；最后路径**按主机**分别记忆；`SftpHome` 不污染最后路径；记忆目录失效时优雅回落到 CWD 并 WARN |
| 自愈 | 杀 ssh 子进程/断链后：报错原文 + `[WARN] 远端连接已失效…` + `[INFO] 已重新连接`；探针预算内确凿失败才判失效；超时=未知**不改状态**；权限等非链路错误**不**触发重连 |
| 探针预算 | 稳定链路上探活耗时分布（本仓库实测 warm 10–24ms、冷拨号 208–224ms，预算 5s）；无「失效」误判 |
| 传输 | 单文件/目录上传下载、批量；**双侧哈希一致**；fallback（batch）后端也要各跑一遍 |

## 6 坑位表（症状 → 根因 → 对策）

| 症状 | 根因 | 对策 |
|---|---|---|
| 命令"没有任何输出"就结束 | `pkill -f <pattern>` 匹配到了自己命令行里的同名字面量 | 用 `pgrep -x` + 数字 kill（§2.4） |
| 自动化点击"点哪都没反应" | 面板布局随传输队列/日志面板变化而位移 | 每步先截图定位（§2.5） |
| 应用内页签点不动 | 无窗口管理器（Xvfb）时焦点未落在窗口 | `xdo focus <win>` 后再点 |
| 应用到 Windows 后连接报 `exec: "ssh": executable file not found in %PATH%` | `windows/386` 的 32 位进程被 WOW64 重定向（`System32\OpenSSH` → 不存在的 `SysWOW64\OpenSSH`） | 优先构建 **amd64**（无需 mingw）；386 已由 `internal/osutil/execpath.go` 用 `Sysnative` 解析修掉 |
| RDP 报 `STATUS_LOGON_FAILURE (0xC000006D)` 但 GA 通道认证成功 | 宿主 `13389` 被**别人的** ssh 隧道占用 ⇒ 连到的是另一台机器（用证书 CN 可判别）；VBox 的 `rdpfwd` 绑定失败是**静默**的 | 先 `ss -ltnp` 看占用 + 核验 RDP 证书 CN；改挂 `13390` |
| `vboxctl ping` 报"客户机 SSH 不通" | 宿主 `2222` 被自己的代理占用 ⇒ VBox `sshfwd` 静默绑定失败 | 杀掉代理后重挂 `natpf1 sshfwd,tcp,127.0.0.1,2222,,22` |
| RDP 裸连被"REMOTE HOST IDENTIFICATION HAS CHANGED"拒 | 宿主 `known_hosts` 里 `[127.0.0.1]:2222` 是旧客户机密钥 | 用独立 `UserKnownHostsFile`/`StrictHostKeyChecking no` 的临时块，用完删除 |
| 会话内 agent `click{x,y}` 不生效（`cursor` 恒为屏幕中心） | 该 op 不吃 x/y | 先 `{"op":"setpos","x":..,"y":..}` 再 `click`；但仍可能整条 SendInput 不落地（本次实测鼠标键盘都不生效）⇒ 换 RDP 通道 |
| 客户机 `.ps1` 某行"莫名失效" | 中文版 Windows 的 PS 5.1 按 GBK 读无 BOM 脚本 | 走 `vboxctl ps`（自动处理）；**`vboxctl gui` 反而不要自己加 BOM** |
| `xdo type` 输入含 `\` 的路径时报 `bad keysym \` | `xdo.c` 的 `type` 把每个字符当 **keysym 名**解析，而 `\` / `:` / `-` 的 keysym 名分别是 `backslash` / `colon` / `minus`，不是字符本身 | 改用 `xdo key <keysym 名>` 逐键发；`:` 需要上档键 ⇒ `xdo hotkey shift semicolon`；**每键之间留 ~0.15s**（连发会丢键，表现为路径只输入了一部分） |
| 读客户机剪贴板验证 `CF_HDROP` 时拿到空 / `count=0` | **剪贴板是 per-session 的**：`vboxctl ps`（SSH）落在 Session 0，读不到 Session 1（桌面）的剪贴板；另外 `GetFileDropList()` 需要 STA 线程 | 用 `vboxctl gui readclip.ps1` 送进 Session 1 再读：`Add-Type -AssemblyName System.Windows.Forms` → `[Windows.Forms.Clipboard]::GetFileDropList()` / `GetText()`，结果写 `Z:\` 让宿主能读（本次验 CF_HDROP 即此法） |
| `VBoxManage` 报 `character device /dev/vboxdrv does not exist`，但 `lsmod \| grep vboxdrv` 明明有模块 | 当前会话的**文件沙箱遮蔽了 `/dev`**（既不是容器隔离，也不是驱动没装）：`workspace-write` 下 `/dev/vbox*` 不可见，放宽权限后立刻可见 | 不要在受限沙箱里下"本机跑不了 VBox"的结论——这是最容易误判的一条；换有完整 `/dev` 可见性的权限后复验 `ls -la /dev/vbox*` + `VBoxManage --version` |
| 只跑 `make ci` 就宣称"真机通过" | 门禁不含 GUI 与真协议 | 本 skill 的存在理由：`make ci` 之后必须真机跑 §5 矩阵 |

## 7 证据规范

- 目录：`docs/superpowers/reports/<日期>-<特性>-evidence/real/`；命名 `<矩阵项>-<序号>-<动作>.jpg|png`。
- 每项必须写进结果表：**判据 / 结果 / 关键观察（日志原文、哈希、像素量化）/ 证据文件名**；未验项单独列，禁止把"没验"写成"通过"。
- 截图入库前压到 1280 宽 JPEG（`convert -resize 1280x -quality 82`）以免仓库膨胀；全量原图留在临时目录。
- 复现所需的驱动源码入库到本 skill 的 `tools/`（`xdo.c`、`proxy.py`），别人不必再手写一遍。

## 8 收尾清单（逐条打勾）

- [ ] 客户机/共享目录里我造的夹具与投放件已删；临时 ssh 别名块已删并 `md5sum` 与备份一致
- [ ] 应用进程、`Xvfb`、`xfreerdp`、代理进程全部归零；VM 已 `stop`、锁已 `release`、我加的 `natpf*` 已删
- [ ] 用户真实配置未被动过（`~/.config/sshore/sshore.toml`、`~/.ssh/config` md5）
- [ ] 结论/证据已提交；`git status` 干净；最后一次代码改动后 `make ci` EXIT=0
- [ ] 报告里明确列出"未验证项"

## 9 实例（本仓库已有的真机验收产出）

- `docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-evidence/real/RESULTS.md` —— 15 项矩阵（A1–F2）+ A 轮复验
- 同目录 `REGRESSION-B.md`（旧功能回归：上/下载哈希、搜索、批量）、`WINDOWS-C.md`（Windows/WebView2 通道与坑）、`harness.md`（工作台与坐标）
- 同目录 `c-win-*.jpg` / `amd64-*.jpg` / `win-*.jpg` —— 决定性截图（含 Windows 原生对话框、菜单分级、置灰量化）
- 更细的命令手册、坐标标定与排障树：见同目录 `REFERENCE.md`
