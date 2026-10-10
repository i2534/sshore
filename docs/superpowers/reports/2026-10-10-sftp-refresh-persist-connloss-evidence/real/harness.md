# 真机验收 · 工作台与过程记录

## 环境（实测）
- 应用二进制：`build/bin/sshore`，`LDFlags Version=v0.7.2-48-ge05d0bb`（= 分支 HEAD e05d0bb），`wails build -tags webkit2_41`。
- 显示：`Xvfb :99`（1600x1000，无 WM）；输入：自写 XTEST 驱动 `xdo`（`/tmp/sshkit-acc/xdo.c`，等效 xdotool）。
- 隔离 HOME：`/tmp/sshkit-acc/home`（应用配置落在 `.../home/.config/sshore/sshore.toml`，**不动用户真实配置**）。
- 远端：`acc-target`（HostName 192.0.2.10, User lan, Port 22）。**实测本机自身就是 192.0.2.10**（`hostname -I`），即本机 sshd 的真实 SFTP 会话。
- 传输后端：应用日志 `[INFO] sftp transport=gosftp`；子进程 `ssh -o BatchMode=yes -o IdentitiesOnly=yes -o ConnectTimeout=10 -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -s acc-target sftp`（pid 258887）⇒ 断链可**直接杀该子进程**，无需 root。
- 真实 `~/.ssh/config` 临时追加了 `acc-target`/`acc-proxy` 两个别名（**已备份**到本目录 `user-ssh-config.backup-*` 与 `/tmp/sshkit-acc/ssh-config.bak`，验收后删除并校验 md5）。
- 代理（用于"网络黑洞/重置"）：`/tmp/sshkit-acc/proxy.py`（venv），`127.0.0.1:2222 → 192.0.2.10:22`，控制文件 `/tmp/sshkit-acc/proxy.ctl`（normal|blackhole|reset），`acc-proxy` 走它。

## 结果
| # | 判据 | 结果 | 证据 |
|---|---|---|---|
| C3 | 配置段形态 | **PASS** | 隔离 HOME 的 `sshore.toml`：`[sftp_ui]` 五键 + `[sftp_ui.last_remote_dirs] acc-target = "/home/lan"`，既有 `[app]`/`[[local_recent]]`/`[[remote_recent]]` 未丢 |
| A1 | 工具栏刷新两侧 | **PASS** | `a1-1-selected.png`（刷新前选中 `build`，已选 1/17）、`a1-2-after-refresh.png`（本地出现 `acc-local-141612`、远程出现 `acc-remote-141612`，23→24 项；同一行 `build` 仍选中，已选 1/18） |
| A2 | 右键刷新只刷本面板 | **PASS** | `a2-1-row-menu.png`（本地行菜单：上传到远程/重命名/删除/──/刷新）、`a2-2-after-pane-refresh.png`（本地出现 `acc-local2-141636` 17→19 项；远程仍 24 项、**没有** `acc-remote2-141636`） |
| B1-a | 行右键菜单（远程） | **PASS**（副产物） | `b1-1-row-menu-remote.png`：`下载…/上传…/重命名/删除/──/刷新`，**无「新建文件夹」** |
| 附带 | 非链路错误不触发重连 + F1 抓手存在 | **PASS** | 日志：`[ERROR] Error: sftp ls tmp/acc-empty: file does not exist` + `[INFO] 探活耗时 213ms`，**无**「远端连接已失效」（探针答 ok ⇒ 不重连） |
