# SFTP 右键「复制」子菜单与弹框关闭策略 验收记录

**日期**：2026-10-12（真机执行于 2026-10-13）
**计划**：`docs/superpowers/plans/2026-10-12-sftp-copy-submenu.md`
**设计**：`docs/superpowers/specs/2026-10-12-sftp-copy-submenu-design.md`
**证据目录**：`docs/superpowers/reports/2026-10-12-sftp-copy-submenu-evidence/real/`

## 结论摘要

**真机验收通过（Windows 10 客户机 + RDP 真实输入）。** 核心项「复制 → 文件」在真实桌面被验证到**资源管理器粘贴出副本且 MD5 逐字节一致**（含中文/空格文件名、多选）；弹框"只能点按钮关闭"策略在真机验证通过；文本两项与多行顺序、未连接远程面板的面板级菜单也都符合设计。

**仍未验证的项**见第 5 节（主要是需要可 SSH 主机的远程面板路径）——不得据本报告认为整份 T10 清单已全部覆盖。

## 1. 环境

| 项 | 值 |
|---|---|
| 客户机 | VirtualBox `win10`（`DESKTOP-LLF2GB6`，Windows 10 19045），本次由 `vboxctl start` 启动、收尾 `vboxctl stop` 关机 |
| 应用产物 | `build/bin/sshore.exe`（PE32+ GUI x86-64，4,941,824 B，标题栏 `SSHore v0.7.4-13-gebbaa7` = 含 C1/C2 修复的提交） |
| 输入通道 | **RDP 真实鼠标键盘**（宿主 Xvfb `:98` + `xfreerdp` → `127.0.0.1:13389`，证书 CN 核验为 `DESKTOP-LLF2GB6`）+ 本 skill 的 `xdo` 驱动 |
| 配置隔离 | 启动脚本覆盖 `APPDATA=C:\vbox-agent\sshkit-copy\appdata`（`os.UserConfigDir()` 的来源），保留 `USERPROFILE` 以便 ssh 可用；行为上印证隔离生效：应用启动后本地面板回落 `Cwd` 而非用户记忆路径 |
| 夹具目录 | `C:\vbox-agent\sshkit-copy\{src,dst}`（自有目录，未触碰他人产物） |

> 会话内 agent 的 `SendInput`（`vboxctl agent click`）在本机**不落地**（与 skill §6 记录一致），因此全程改用 RDP 通道。

## 2. 验收矩阵（判据 / 结果 / 关键观察 / 证据）

| # | 判据 | 结果 | 关键观察（原始证据） | 证据文件 |
|---|---|---|---|---|
| A1 | 本地面板行右键菜单含「复制 ▸」，且位于「重命名」之前 | **通过** | 菜单依次为：上传到远程 / **复制 ▸** / 重命名 / 删除 / ── / 刷新 | `menu2.jpg` |
| A2 | 子菜单三项文案逐字为 文件名 / 全路径 / 文件（Windows） | **通过** | 子面板贴父项右侧展开，三项齐全；「文件」存在即证明 `ClipboardFileSupport()` 返回 true（绑定 import 修复生效，界面无红色错误横幅） | `submenu2.jpg` |
| A3 | 「复制 → 文件」（本地）写入 CF_HDROP 文件对象 | **通过** | Session 1 读剪贴板：`count=1` / `file=c:\vbox-agent\sshkit-copy\src\hello.txt` / `text=`（空 ⇒ 不是文本） | `clipboard-single.out`、`copy1.jpg` |
| A4 | 资源管理器真实 Ctrl+V 粘贴出副本且内容一致 | **通过** | `hello.txt`：src `len=19|md5=19CD442CC00294FABA2DD9BD22E117F7`，dst **同值** | 见 §3 哈希原文 |
| A5 | 多选 + 非 ASCII 文件名同样成立 | **通过** | `count=2`，两条路径含 `中文 文件.txt`（UTF-16 编码正确）；粘贴后两个文件 MD5 双侧一致 | `clipboard-multiselect-chinese.out`、`multi1.jpg` |
| A6 | 复制不改变选中集（只读动作） | **通过** | 多选复制前后面板头均显示「已选 2 / 2 项」，两行保持高亮 | `multi1.jpg`、日志 `10:09:26` |
| A7 | 文本两项：纯文本、多行顺序＝面板显示顺序 | **通过** | 文件名：`files=0`、`text=中文 文件.txt⏎hello.txt`；全路径：完整本地路径同样两行同序 | `clipboard-text-name.out`、`clipboard-text-path.out` |
| A8 | 日志文案逐字、`<n>`＝实际写入项数 | **通过** | `已复制 1 个文件到剪贴板（可直接粘贴到文件管理器）` / `已复制 2 个文件…` / `已复制 2 个文件名到剪贴板` / `已复制 2 个全路径到剪贴板` | `copy1.jpg`、`remote_blank.jpg` |
| A9 | 空白/面板级菜单不出现「复制」 | **通过** | 未连接的远程面板空白右键只有：刷新 / 新建文件夹 / 上传文件… | `remote_blank.jpg` |
| B1 | 模态框：点遮罩**不**关、按 Esc **不**关 | **通过** | 设置对话框在**连续三次遮罩点击 + 一次 Esc** 后仍打开（且遮罩吞掉了对其余界面的点击） | `dlg_montage.jpg` |
| B2 | 模态框：点显式按钮**能**关 | **通过** | 点「完成」后对话框关闭，界面恢复 SFTP 视图 | `dlg3_montage.jpg` |

## 3. 哈希核对原文（双侧，逐字节）

```
--- src ---                                          --- dst ---
hello.txt|len=19|19CD442CC00294FABA2DD9BD22E117F7     hello.txt|len=19|19CD442CC00294FABA2DD9BD22E117F7
中文 文件.txt|len=17|57A7904DACD7F469816C9E5CA172B00B  中文 文件.txt|len=17|57A7904DACD7F469816C9E5CA172B00B
```

## 4. 本机侧门禁（改动的机械验证，非真机）

| 项 | 命令 | 结果 |
|---|---|---|
| 全量门禁 | `GOCACHE=$PWD/.gocache make ci` | **EXIT=0**：vite build ✓ / `go vet ./...` 无输出 ✓ / `go test ./... -race -count=1` 全 ok ✓ / vitest **45 文件 547 测试全 PASS** |
| Windows 交叉编译 | `GOOS=windows GOARCH=amd64\|386 CGO_ENABLED=0 go build ./...` | 均 EXIT=0 |
| Windows vet（CI `go-windows` 等价） | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...` | **EXIT=0 无输出**（无 `unsafeptr` 告警） |
| 剪贴板包单测 | `go test ./internal/clipboard/ -v` | 10/10 PASS |

## 5. 未验证项（**不得**当作已通过）

| 项 | 为什么没验 |
|---|---|
| 子菜单**悬停**展开、以及"鼠标从父项移到子面板不消失" | 本次用**点击**展开验证（点父项开合可用 ✓）；悬停 120ms 与 2px 缝隙穿越需要精细的指针轨迹，RDP 通道下未做 |
| 未连接时「文件」**置灰** | 远程面板未连接 ⇒ 列表为空、无法产生**文件行**右键（只验证了面板级菜单不含复制）；置灰态需要连接后的远程文件行 |
| 远程「文件」全链路（`ClipboardTempDir` → 入队下载 → 完成后入剪贴板 → 日志） | 需要一台可 SSH 的真实主机；本次未配置 |
| 远程多选 + 中途取消 ⇒ 整批不写剪贴板（C2 修复的真机确认） | 同上（依赖远程下载队列） |
| 其余 4 个模态框（AppDialog / ConflictDialog / SearchOverlay / SyncConflictsDialog） | 只抽验了 SettingsDialog；四处是**同一处改动**（去遮罩/去 Esc），但未逐个真机点击 |
| 临时目录下次启动清理（`sshore-clip-*`，>1 小时） | 需要跨启动 + 1 小时时间窗 |

## 6. 复现方式（关键命令）

```bash
export PATH=~/.dsh/skills/vbox-win/bin:$PATH
vboxctl who && vboxctl start                       # VM 启动 + SSH 就绪
GOCACHE=$PWD/.gocache make windows                 # 产出 build/bin/sshore.exe
cp build/bin/sshore.exe /home/lan/virtual/share/vbox-agent/sshkit-copy/   # 宿主 ↔ Z:\ 共享
# 启动（Session 1，APPDATA 隔离）：见计划 T10 与本次用的 launch.ps1
# 输入通道：Xvfb :98 + xfreerdp 连 127.0.0.1:13389（先核验证书 CN=DESKTOP-LLF2GB6）
# 剪贴板读取必须走 Session 1（Session 0 与 Session 1 剪贴板隔离）：
#   vboxctl gui readclip.ps1   # 内部用 [System.Windows.Forms.Clipboard]::GetFileDropList()
vboxctl stop && vboxctl release                    # 收尾
```

## 7. 收尾清单

- [x] 客户机夹具与脚本已删（`C:\vbox-agent\sshkit-copy\{src,dst,*.ps1,*.out}`），应用进程已退出
- [x] 宿主 `xfreerdp`、`Xvfb :98` 已归零；共享目录 `sshkit-copy/` 已删
- [x] VM 已 `vboxctl stop`（已关机）、锁已 `release`；本次未新增任何 `natpf*`
- [x] 用户真实配置未被改写（`APPDATA` 覆盖 + 启动后回落 `Cwd` 的行为证据）
- [x] 证据与结论已提交；`git status` 干净
- [x] 未验证项已在第 5 节逐条列出
