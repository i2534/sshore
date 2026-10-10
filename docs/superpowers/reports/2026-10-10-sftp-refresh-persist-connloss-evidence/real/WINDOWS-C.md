# Windows / WebView2 验收（C 轮）——已试，被环境阻塞，附证据与可执行清单

## 结论一句话

**应用能在真 Windows（VBox `win10`，19045 x64）上构建、启动并正确渲染**；但**这台客户机当前整条合成输入链路不通**（连 Windows 开始菜单都点不动），且备用的真实输入通道（RDP）缺客户机口令 ⇒ **Windows 特有的菜单/窗口行为本轮未能验证**。这是环境阻塞，不是应用缺陷的证据。

## 已完成（含原始证据）

| 步骤 | 命令 / 证据 |
|---|---|
| 交叉构建 | 本机只有 **i686** mingw（`dpkg -l` 无 x86_64 包）⇒ 目标 `windows/386`：`wails build -platform windows/386 -skipbindings -clean -nopackage -ldflags "-s -w -X main.Version=v0.7.2-52-g0aba549" -trimpath` ⇒ `build/bin/sshore.exe` 12,561,920 字节 |
| 部署到客户机 | 写共享目录 `/home/lan/virtual/share/vbox-agent/sshore-b/` ⇒ 客户机 `Z:\vbox-agent\sshore-b\`（只用自己目录，遵守 skill §4.0 共享 VM 礼仪） |
| 启动（两条路径都试了） | ① `vboxctl gui run2.ps1`（计划任务 + Interactive）；② 会话内 agent `{"op":"run",...}` ⇒ `{"ok":true,"pid":8476}` |
| 渲染确认 | 窗口标题 `SSHore v0.7.2-52-g0aba549`，hwnd 655922，矩形 `150,145,1250,865`，**foreground**；控件树 `Chrome_WidgetWin_0` / `Chrome_WidgetWin_1` / `Chrome_RenderWidgetHostHWND("Chrome Legacy Window")` / `Intermediate D3D Window` 全在（`ctls` 输出）；`Get-Process`：`sshore` + **8 个 msedgewebview2**，`Responding=True`，全部 `SessionId=1` |

## 阻塞点（有对照实验）

> **2026-10-10 第 2 次尝试（同一 VM，重启后干净桌面）——诊断进一步收紧：**
> - 先排除了"控制台会话被 RDP 顶掉"：`query session` = `console lan 1 Active`、`services 0 Disc`、`rdp-tcp 65536 Listen`，agent `sessionId=1` ⇒ 会话状态正常。
> - 排除了 DPI 缩放：`AppliedDPI=96`（100%）；且 agent `wins` 给出的窗口矩形（`150,145,1250,865`）与帧缓冲截图逐像素一致 ⇒ 坐标空间就是 1400x1050。
> - 发现 **agent 的 `click` 不吃 x/y**（每次返回 `cursor` 恒为屏幕中心 `700,525`；必须先 `setpos` 再 `click`）——这是本轮之前"点哪都没反应"的一半原因。
> - **改成 `setpos` 精确定位后再 `click`，仍然完全无 UI 效果**：开始菜单 `(64,1037)`、任务栏搜索框 `(75,1037)`、桌面「此电脑」双击 `(30,45)` 全部无反应；键盘侧用扫描码发左 Win（`{"op":"key","sc":91,"hold":true/false}`）也无效（`LWIN` 不在 agent 键名表里，会报 `unknown key: LWIN`）。
> - 而桌面本身持续刷新（相邻截图时间 16:18→16:21、托盘网络图标形变）⇒ **不是"截图是陈旧帧"，是注入的鼠标/键盘事件到不了交互输入桌面**；agent 自身 `diag` 又报告 `sendInputLastRet: 1`（SendInput 调用本身成功）⇒ 属于"调用成功但不落地"的输入桌面/会话隔离问题。
> - 结论：**该 VM 当前状态下，会话内 agent 这条通道无法驱动 GUI**（不论鼠标或键盘）。要继续必须换通道：RDP（需口令）、修 agent 的输入桌面归属、或改用 aigc 上的 KVM win10。


1. **合成输入进不到桌面**（不是只进不到应用）：
   - 点应用内 SFTP 页签 `(192,283)`、点输入框 `(500,294)` 后 `type "ABC123"`、点窗口最大化 `(1165,158)` —— 全部无任何变化；
   - **对照**：点 Windows 开始按钮 `(64,1025)` 同样无反应；
   - 而桌面是活的：相邻两次截图之间 KHelpHub 的 toast 文案变了（`推荐给别人使用` → `通过配置卡片中的"推荐给"…`）⇒ 截图不是陈旧帧。
   - `diag` 显示 `foreground=hwnd 655922`、agent 在 `sessionId=1`、`elevated=false`，光标按请求精确落位（`setpos "at":"192,283"`）。任务 `VBoxAgent/VBoxAgentKeep` 的 `RunLevel` 均为 `Limited`（排除 UIPI 提权假设）；换成 agent 自己 `run` 启动的实例表现相同（排除"计划任务 token"假设）。
2. **备用真实输入通道不可用**：`vboxctl rdp-session --seconds 25 --shot /tmp/c-rdp.png` ⇒ `vboxctl: 读不到客户机口令（~/.dsh/.credentials.yaml 的 vbox_win10_password 或 $VBOX_WIN_PASSWORD）`（exit 2）。加了口令即可继续。
3. **顺带修掉的两个环境坑**（都已处理，供后续复用）：
   - **端口冲突**：我上一轮 SFTP 验收的 TCP 代理占着 `127.0.0.1:2222`（正是 VBox `sshfwd` 的宿主端口），VM 启动时 NAT 转发**静默失败** ⇒ `vboxctl ping` 报"客户机 SSH 不通"。处置：杀掉遗留代理（pid 311062）+ `VBoxManage controlvm win10 natpf1 delete/… sshfwd,tcp,127.0.0.1,2222,,22` ⇒ 立即恢复（`ping` 返回 `pong:true, host:DESKTOP-LLF2GB6`）。
   - **known_hosts 陈旧**：宿主 `known_hosts` 里 `[127.0.0.1]:2222` 存的是**旧 win10 客户机**的密钥，裸 `ssh -p 2222` 会被"REMOTE HOST IDENTIFICATION HAS CHANGED"拒绝（vboxctl 自带选项不受影响）。这也是本轮之前 `acc-proxy`（同样借道 2222 做 SFTP 代理）首次连接失败的同一根因。

## 未验证（照实记录，不得读成"通过"）

- Windows/WebView2 下的右键菜单语义（行菜单 vs 空白菜单、`e.target`/`contextmenu` 行为、新窗口/原生文件对话框）；
- DPI 缩放/窗口行为、原生「上传文件…」文件选择器；
- Windows 侧的 SFTP 会话（默认 gosftp 走 `ssh -s … sftp`，Windows OpenSSH 客户端路径语义）。

## 可执行清单（下一位接手照做即可）

```bash
# 0) 先确认没有别的会话占用（skill §4.0）
export PATH=~/.dsh/skills/vbox-win/bin:$PATH && vboxctl who && vboxctl status
# 1) 确保宿主 2222 空闲（勿把验收代理留在 2222）
ss -ltnp | grep :2222 || true
# 2) 起 VM；若 ping 不通，重挂 NAT 转发
vboxctl start; vboxctl ping || { VBoxManage controlvm win10 natpf1 delete sshfwd; \
  VBoxManage controlvm win10 natpf1 "sshfwd,tcp,127.0.0.1,2222,,22"; vboxctl ping; }
# 3) 把口令写进 ~/.dsh/.credentials.yaml（键：vbox_win10_password）后走真实输入通道
vboxctl rdp-session --seconds 90 --shot /tmp/desk.png     # 宿主 Xvfb 上的真 RDP 会话
#    然后在宿主用 xdo 对 xfreerdp 窗口做点击/截图（真实输入，不依赖 agent SendInput）
# 4) 应用投放（已构建好）：build/bin/sshore.exe → /home/lan/virtual/share/vbox-agent/… → Z:\…
```
备选：先排查 agent 注入失效本身（比如 `{"op":"mclick"}` 消息注入、或换 `win10-aigc` 上那台正在运行的 KVM win10）。

## 收尾（已执行）

- 客户机内应用已 kill（`{"op":"kill","name":"sshore"}` → `killed:1`）；VM 已 ACPI 关机（"已关机"）；占用锁已释放（"已释放"）。
- 共享目录投放件已删除；宿主我的验收实例已停（`HOME=/tmp/sshkit-acc/home`、`DISPLAY=:99`，确认是我的实例后才 kill）。
- `~/.ssh/config` 已还原并 **md5 校验一致**：`8b71afb349b86c58b35a73e95a97dc4b`（与本轮备份 `/tmp/sshkit-acc/ssh-config.bak2` 相同，即用户原文件）。
