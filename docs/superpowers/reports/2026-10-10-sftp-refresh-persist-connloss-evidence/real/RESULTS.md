# 真机验收执行结果（15/15 项已执行）

- 被测二进制：`build/bin/sshore`，`wails build -tags webkit2_41` 产，`LDFlags Version=v0.7.2-48-ge05d0bb`（= 分支 HEAD `e05d0bb` 的工作树）。
- 工作台与原始过程记录见同目录 [`harness.md`](harness.md)；决定性截图见本目录（25 张，JPEG q82/1280 宽；全量原图存 `/tmp/sshkit-acc/png-originals/`，未入库）。

| # | 判据 | 结果 | 关键观察（日志/界面原文） | 证据 |
|---|---|---|---|---|
| A1 | 工具栏刷新两侧 | **PASS** | 本地新增 `acc-local-141612`、远程新增 `acc-remote-141612`（23→**24 项**）；刷新前后同一行 `build` 都保持选中（已选 1/17 → 1/18） | `a1-1-selected.jpg`、`a1-2-after-refresh.jpg` |
| A2 | 右键刷新只刷本面板 | **PASS** | 本地出现 `acc-local2-141636`（17→19 项）；远程仍 **24 项**且**没有** `acc-remote2-141636` | `a2-1-row-menu.jpg`、`a2-2-after-pane-refresh.jpg` |
| B1 | 空白/行菜单分级 | **PASS** | 空白右键 = `刷新 / 新建文件夹 / 上传文件…`；行右键 = `下载… / 上传… / 重命名 / 删除 / ── / 刷新`（**无「新建文件夹」**） | `b1-4-blank-menu.jpg`、`b1-1-row-menu-remote.jpg` |
| B1' | 空白右键不清空选中 | **PASS** | 过滤后选中 `Android`（已选 1/2）→ 下方空白右键 → 菜单仍为空白菜单，底部仍「已选 1/2 项」 | `b1-8-blank-with-selection.jpg` |
| B1'' | 「上传文件…」不再是死按钮（本轮修复的端到端复核） | **PASS** | 点击后弹出 GTK 文件选择器（新窗口 `0x200ee5` 1096x822，含 Recent/Home/Desktop 侧栏与 Cancel/Open） | `b1-5-upload-picker.jpg` |
| B2 | 断开后置灰 + 工具栏仍刷本地 | **PASS** | 断开后按钮回到「连接」、远程 0 项；菜单三项文字变灰（菜单区平均亮度 **0.930 → 0.960**，同底色），点灰项无对话框、无新窗口、无日志新增；工具栏刷新让本地 19→**20 项**而远程保持 0 项 | `b2-2-greyed-menu.jpg`、`b2-3-toolbar-refresh-while-disconnected.jpg` |
| C1 | 排序持久化 | **PASS** | 点击后落盘 `sort_local_key="size"/dir=1`、`sort_remote_key="modTime"/dir=-1`；**重启后**本地仍按「大小 ▲」升序（340B→4.6KB）、远程（未连接）「修改时间 ▼」仍高亮 | `c1-1-sorted.jpg`、`c1-2-after-restart.jpg` |
| C2 | 每主机最后路径（双击手势） | **PASS** | 双击进 `Documents` ⇒ `acc-target = "/home/lan/Documents"`；换 `acc-proxy` 双击进 `Downloads` ⇒ `acc-proxy = "/home/lan/Downloads"` 且 `acc-target` 不变；**重启后**连 acc-target 落 `/home/lan/Documents`(11 项)、连 acc-proxy 落 `/home/lan/Downloads`(21 项) | `c2-2-dblclick-documents.jpg`、`c2-4-proxy-downloads.jpg`、`c2-5-restart-target-documents.jpg` |
| C3 | 配置段形态 | **PASS** | `[sftp_ui]` 含 `last_local_dir` + 四个 `sort_*`，`[sftp_ui.last_remote_dirs]` 含两台主机；既有 `[app]`、`[[local_recent]]`、`[[remote_recent]]` 未丢（原文见下 §C3） | 文本 §C3 |
| C4 | `SftpHome` 不污染最后路径 | **PASS** | 断开 → 连接（每次 connect 都会调 `SftpHome`）后远程仍是 `/home/lan/Documents`，配置值不变 | `c4-1-after-reconnect.jpg` |
| D1 | 休眠/断链自愈 | **PASS** | 杀 `ssh … -s acc-target sftp` 子进程 + 切标签回来 ⇒ 新 ssh 子进程自动出现；日志逐字：`[WARN] 远端连接已失效（主机休眠或网络中断），正在重连…` + `[INFO] 已重新连接 acc-target`（前置 `[INFO] 探活耗时 25ms`）；面板恢复 **25 项**可操作 | `d1-1-selfheal.jpg` |
| D2 | 断链后直接操作 ⇒ 报错原文 + 自动恢复 | **PASS**（含语义说明） | 死链上**不手动断开**直接点工具栏刷新 ⇒ `[ERROR] Error: sftp ls /home/lan: connection lost` + `[INFO] 探活耗时 210ms`，随后 ssh 子进程 0→**1**（链路被自动恢复）；因探针实测（新会话 210ms）判定链路健康，**未**打印「失效/重连」——这是正确语义（探针说没事就不许声称失效） | `d2-4-toolbar-refresh-on-dead-link.jpg` |
| D3 | 权限错误不得触发断开/重连 | **PASS** | `ls /etc` 下执行新建文件夹 ⇒ `[ERROR] Error: sftp mkdir /etc/acc-nope: permission denied` + `[INFO] 探活耗时 220ms`；导航 `/root` ⇒ `[ERROR] Error: sftp ls /root: permission denied` + `[INFO] 探活耗时 208ms`；两次都**无**「失效/重连」，ssh 子进程 pid 前后一致（278653→278653、283235 段亦无重连日志） | `d3-14-permission-denied.jpg` |
| E1 | 「主目录」回归守卫 | **PASS** | 记忆为 `/home/lan/Documents` 时点「位置 → 主目录」⇒ 落到 `/home/lan`（25 项），未被记忆路径抢走 | `e1-1-locations-menu.jpg`、`e1-2-after-home.jpg` |
| E2 | 断开 → 连接回到最后路径 | **PASS** | 见 C4（回落 `/home/lan/Documents`，非 home） | `c4-1-after-reconnect.jpg` |
| F1 | 探活预算实测 | **PASS** | 清空日志后**正好 6 次**切标签 ⇒ **正好 6 条** `探活耗时`（23/24/24/24/24/24ms，1:1 对应）；会话内 warm 样本 **10–24ms**（18 条）、冷拨号样本 **208–224ms**（5 条）；最大 224ms ≈ 5s 预算的 **4.5%**；全程**无**「远端连接已失效」误判（会话内仅 D1 的两次为真实断链） | `f1-9-batch6.jpg` |
| F2 | legacy batch 后端探活活性 | **PASS** | ① 连接期黑洞：`[INFO] 探活耗时 10027ms` → `[WARN] 远端连接已失效…` → 重连亦超时（10s）→ `[ERROR] 重连失败：连接未建立，请手动点「连接」`，**未**永久卡在探活中、**无**无限重试（ssh 自身 `ConnectTimeout=10` 给出活性上界）；② 已建链后数据面黑洞：`[INFO] 探活耗时 15003ms`（前端 15s 兜底），**无**「失效」、按钮仍「断开」、面板仍 21 项 ⇒ 超时=未知、**不改状态** | `f2-8-blackhole-all-probe.jpg`、`f2-10-unknown-timeout.jpg` |

## §C3 配置原文（隔离 HOME）

```toml
[sftp_ui]
  last_local_dir = "/home/lan/workspace/scripts/sshkit"
  sort_local_key = "size"
  sort_local_dir = 1
  sort_remote_key = "modTime"
  sort_remote_dir = -1
  [sftp_ui.last_remote_dirs]
    acc-proxy = "/home/lan/Downloads"
    acc-target = "/home/lan/Documents"
```

## 顺带观察到的事实（非判据）

- batch 后端的连接形态：`ssh -o BatchMode=yes -o IdentitiesOnly=yes -o ConnectTimeout=10 -o ControlMaster=yes -o ControlPersist=30 -o ControlPath=/tmp/sshore-sftp-ctrl/cm-<host>.sock -N -f <host>`，每条命令都有 `sftp <子命令>` 日志（如 `sftp ls done (21 items)`）——可观测性很好。
- gosftp（默认）后端：`ssh … -s <host> sftp` 子进程 + `sftp.NewClientPipe`，与设计一致。

## 未覆盖 / 边界（不得读成"全绿"）

1. **显示为 `Xvfb :99`（无窗口管理器）**，不是物理桌面；对本轮判据（页面内菜单/自愈/持久化/排序）无影响，但 Windows/WebView2 的 `contextmenu`/`e.target` 语义、以及真实合成器下的右键行为仍未覆盖。
2. **"远端"就是本机**（`10.99.152.18` = `lan-ai-ubuntu`）：走真实 sshd 与真实 SFTP 协议，但不是跨物理网络 ⇒ 时延/丢包/中间设备导致的断链形态未覆盖。
3. **无 root**：`ss -K`/iptables 未使用；断链用"杀 ssh 子进程"与"可控 TCP 代理（黑洞/重置）"两种方式模拟，日志留证（见 D1/D2/F2）。
4. **上传/下载/拖拽移动**类手势不在本轮 15 项之内（本轮覆盖了双击进目录、右键菜单、点列头、切标签、连接/断开、新建文件夹）。
5. 验收期间为让 `ssh` 子进程解析别名，临时在真实 `~/.ssh/config` 追加过两个别名块；**已删除并用 md5 校验与备份逐字节一致**（`8b71afb349b86c58b35a73e95a97dc4b`）。应用配置全程走隔离 HOME（`/tmp/sshkit-acc/home`），**未触碰**用户真实 `~/.config/sshore/sshore.toml`；测试用的 `acc-local*`/`acc-remote*`/`/tmp/acc-empty` 已删除。
6. `wails build` / `wails generate module` 会把 `frontend/wailsjs/runtime/*` 的 mode 在 755/644 间抖动（内容零改动）；已还原为已提交状态并记入 deferred minor。
