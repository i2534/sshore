# 验收报告 — SFTP 刷新/持久化/连接失效自愈

- 计划：`docs/superpowers/plans/2026-10-10-sftp-refresh-persist-connloss.md`
- 规格：`docs/superpowers/specs/2026-10-10-sftp-refresh-persist-connloss-design.md`（v1.7）
- 分支：`feat/sftp-refresh-persist-connloss`（起点 `master` = 6169102，当前 HEAD = `538110b`）
- **只本地提交，未 push。**

## 1 结论摘要

| 面 | 状态 | 证据 |
|---|---|---|
| 自动化门禁（build / vet / `go test -race` / vitest） | **全绿** | `docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-evidence/make-ci.log` |
| 真机验收 A1–F2（§11.3 矩阵） | **未执行**（环境阻塞，见 §3） | 阻塞证据见 §3；可照跑的清单见 §4 |
| 单测级判据（探活预算、batch 活性、并发写、13 落点、排序/落点/恢复顺序） | 已覆盖 | 见 §2 与各任务报告 |

**不得把本报告读成"真机验收已通过"** —— §4 的 15 项仍需在有图形桌面与可断链网络的环境里逐项执行。

## 2 自动化判据（实测输出节选）

命令：`GOCACHE=$PWD/.gocache make ci`（= `cd frontend && npm run build` → `go vet ./...` → `go test ./... -race -count=1` → `cd frontend && npx vitest run`），退出码 **0**。

```
✓ built in 1.73s
ok  	sshore	1.358s
ok  	sshore/internal/config	1.333s
ok  	sshore/internal/forward	1.908s
ok  	sshore/internal/importer	1.026s
ok  	sshore/internal/localfs	1.034s
ok  	sshore/internal/namepattern	1.032s
ok  	sshore/internal/osutil	6.499s
ok  	sshore/internal/preset	1.039s
ok  	sshore/internal/sftp	19.552s
ok  	sshore/internal/sshconn	1.039s
ok  	sshore/internal/sync	16.228s
ok  	sshore/internal/update	14.907s
ok  	sshore/internal/watch	6.840s
 Test Files  38 passed (38)
      Tests  483 passed (483)
```

- 最终一次门禁（提交 `5bc3004` 之后、最后一个写入者之后）：`docs/superpowers/reports/2026-10-10-sftp-refresh-persist-connloss-evidence/make-ci-final.log`，退出码 **0**，vitest **38 files / 488 tests**、13 个 Go 包 `-race` 全 `ok`。上表为任务收尾时的那次运行（484 tests）。
- 新增用例总量：Go（`internal/config` 4 + `internal/sftp` 2 + 根包 6 组）+ 前端（T6 14、T7 13、T8 4、T9 10、T10 9、T11 13、T12 12、T13 20）。
- 关键判别力实验（由实现者/复核者在各自任务内实跑）：`Pool.Probe` 的「过期即 true」变异被新用例判红；四条并发写路径在改前报真 `DATA RACE`；`commit 82fd960` 上 `upload-picked` 无分派被新断言判红；13 个落点逐个抹掉 `remoteOpFailed(` 全部转红。

## 3 真机验收的阻塞证据（本机实测）

| 依赖 | 实测 | 结论 |
|---|---|---|
| 图形桌面 | `DISPLAY` 未设置，`XDG_SESSION_TYPE=tty`，`/tmp/.X11-unix` 不存在 | 无法运行 Wails GUI |
| GUI 驱动 | `xdotool`/`wmctrl` 不存在；`python3 -c "import Xlib"` → `ModuleNotFoundError` | 双击/右键/点列头/切标签无法自动化（`Xvfb` 只提供显示，不提供输入驱动） |
| Windows 客户机 | `VBoxManage list runningvms` → `/dev/vboxdrv` 不存在 | Windows 侧 A/B/C1/D1 全部不可执行 |
| 断链手段 | `sudo` 自身损坏（`/etc/sudo.conf` 属主 uid 65534） | `ss -K` / `iptables … DROP` 不可用（无提权） |
| 远端目标 | `10.99.152.18:22` TCP 实测可达 | 唯一具备的前提 |

## 4 待人工执行的最小验收清单

前置：在**有图形桌面**的机器上 `GOCACHE=$PWD/.gocache wails build -tags webkit2_41`（或 `wails dev`），启动后按 §11.3 执行。断链用 `sudo ss -K dst <host> dport = :22` 或 `sudo iptables -A OUTPUT -p tcp --dport 22 -j DROP`（用完删规则）；Windows 侧用 VirtualBox 停用网卡或 `netsh interface set interface <名称> disable`。

| # | 步骤 | 判据 | 证据 |
|---|---|---|---|
| A1 | 两侧各造一个变化（本地新建目录、远程新建目录）→ 点工具栏刷新 | 两侧同时出现；选中行仍保持选中 | 截图 |
| A2 | 右键本地面板 → 刷新 | 只有本地重载（远程请求计数/日志不变） | 截图 + 日志 |
| B1 | 空白右键 / 行右键各一次 | 空白 = 刷新/新建文件夹/上传文件…；行 = 下载/上传/重命名/删除/刷新；空白右键**不清空**已有选中 | 两张截图 |
| B2 | 断开后右键远程面板 | 三项置灰、点了无反应；工具栏刷新仍能刷出本地变化 | 截图 |
| C1 | 远程点「修改时间」两次 → 重启应用 | 两侧排序各自恢复、互不影响 | 截图 + `grep sort_` |
| C2 | 主机 A **双击进** `/var/log`、主机 B **双击进** `/srv` → 重启 → 分别连接 | A 落 `/var/log`、B 落 `/srv` | 两主机路径栏截图 |
| C3 | `cat ~/.config/sshore/sshore.toml` | 有 `[sftp_ui]` 与 `[sftp_ui.last_remote_dirs]`，既有 `[app]`/`[[tunnels]]` 未丢 | 配置原文 |
| C4 | 单主机：连到 `/var/log` → 断开 → 连接 → 重启再连 | `last_remote_dirs` 始终是 `/var/log`（不被 `SftpHome` 污染） | `grep last_remote_dirs` + 截图 |
| D1 | 连接后杀链路 → 点标签切走再切回 | 日志出现「远端连接已失效（主机休眠或网络中断），正在重连…」与「已重新连接」；面板回到最后路径且可操作 | 日志原文（含时间戳）+ 前后截图 |
| D2 | 断链后直接点「新建文件夹」 | 报错原文仍在日志，随后自动自愈（无需手动断开） | 日志原文 |
| D3 | 远端目录不可写时点上传 | 只有原始报错，**不得**触发断开/重连 | 日志原文 |
| E1 | 已连到 `/var/log` 时点「主目录」 | **必须到 home**（不被记忆路径抢走） | 截图 |
| E2 | 导航到 `/var/log` → 断开 → 连接 | 回到 `/var/log`（不是 home） | 截图 |
| F1 | 正常链路连续触发 20 次探活；冷启动再测 ≥3 次 | 汇总日志里的 `探活耗时 <N>ms`；**没有**「远端连接已失效」误判 | 日志汇总 |
| F2 | `SSHORE_SFTP_TRANSPORT=batch` 下断链 → 切标签 | UI 不永久停在「探活中」；超时保持"未知/不改状态" | 日志 + 截图 |

## 5 未验证项与残留风险

1. 上表 15 项**全部未在真机执行**（§3 的四项环境阻塞）。
2. `internal/sftp` 的 E2E 用例（`SSHORE_E2E_*`）本轮未启用 ⇒ 真实 SSH/SFTP 协议往返只在单测的 `net.Pipe + sftp.NewServer` 与 Go 侧探针单测里覆盖。
3. Windows/WebView2 的 `contextmenu` / `e.target` 语义未实测（右键分流只有源码形状断言）。
4. 单飞/重连的**并发时序**只在单测层覆盖（Go：`-race` + 挂住放行替身；前端：无 jsdom ⇒ 形状断言）。
5. 已 triage 后**保留**的 deferred minor：`SearchOverlay` 的 `emit('failed')` 不分 scope（本地深搜失败也会白跑一次探活，规格 §8.4 第 13 项本就要求无条件 emit）；`recoverConnection` 的失败原因恒为「连接未建立」（真因由 `connect()` 内既有 `err(e)` 先落一条）；`runtime/**` 三个生成文件被 `wails generate` 改成 755（内容零改动）；T9/T12 的若干测试粒度偏粗项。
