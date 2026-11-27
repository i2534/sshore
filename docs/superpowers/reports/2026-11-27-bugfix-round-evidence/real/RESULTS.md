# 2026-11-27 bugfix 轮真机验收结果

被测提交：`d9f0006`（fix(frontend)）及其前 5 个修复提交（f269bcf..d9f0006，共 30 条 bug 修复）。
二进制：`make build`（linux/amd64，标题 `SSHore v0.8.0-8-gd9f0006`）与 `wails build -platform windows/amd64`（标题 `SSHore dev`，未注入版本）。
通道：Linux = Xvfb :99 + XTEST（xdo）；Windows = VBox win10 + RDP 真实输入（:10）。
协议层：`bash e2e/test_local.sh` 全过（真 sshd，batch/gosftp 双后端，8 用例，见 `.audit/e2e.log`，未入库）。

## 结果表

| 项 | 判据 | 结果 | 关键观察 | 证据 |
|---|---|---|---|---|
| A2 单引号导入 | `ssh -N -L '2299:127.0.0.1:2299' acc-target` 导入后落盘端口正确 | **通过** | `sshore.toml`：`listen_port = 2299 / target_port = 2299 / host = "acc-target"`（旧代码会静默产出端口 0） | linux-04-import-fixed.jpg |
| A2 非法 spec 报错 | 含分号的坏 spec 必须明确报错而非静默 | **通过** | 红条 `操作失败: Error: malformed local spec "2299;127.0.0.1;2299"`（输入脚本误打分号，恰好验证报错路径） | .audit/acc/log/03-error-zoom.png（未入库） |
| F1/F2 隧道启停 | 启动→connected→真实流量转发；停止→端口释放 | **通过** | 2298 监听；`ssh -p 2298` 经转发得到 `TUNNEL-FORWARD-OK`；停止后 `ss` 无 2298、日志 `[INFO] stopped` | linux-09-tunnel-connected.jpg、linux-11-tunnel-stopped.jpg |
| F2 端口占用预检 | 本地端口被占时启动必须报占用 | **通过** | `[ERROR] local port 127.0.0.1:2299 already in use`（2299 被 sshd 占用） | linux-06-log-filtered.jpg |
| 自愈重连 | 链路失败后退避重连，恢复后自动连上 | **通过** | 指数退避 1s→2s→4s→8s→16s→30s；别名生效后 `reconnected（共尝试 8 次）` | linux-11-tunnel-stopped.jpg（日志面板） |
| V3 规则日志过滤 | 按规则过滤时启停错误必须可见 | **通过** | 过滤 chip=acc-target 下可见 `[ERROR] acc-target: Error: : local port … already in use`（旧代码 source_id='rule' 会被过滤掉） | linux-06-log-filtered.jpg |
| S1 上传 commit 路径 | 上传走带 Begin 前守卫的 commitRemote，双侧哈希一致 | **通过** | 双侧 md5 `244b7bc3d94e8ba33b45ba77802b7c3b` | .audit/acc/log/18-upload-done.png（未入库） |
| 下载哈希 | 单文件下载双侧哈希一致 | **通过** | 双侧 md5 `282711733a61808aaddfcf35f55738f7`；队列「完成」 | linux-16-download-dialog.jpg |
| V4 删除选中集 | 成功删除后选中集清空 | **通过** | delete-me.txt 远端消失；「已选」徽标消失（2 项无选中）；队列删除项「完成」 | linux-25-after-delete.jpg |
| V8 底部菜单 | 底部行右键菜单完整落在窗口内 | **通过（部分）** | 最底行（clientY≈391）右键菜单完整可见；**夹取计算本身由单测字面量断言覆盖**（sftpMenuClamp.test.js）；溢出场景未真机复现——720px 固定窗高下文件行到不了夹取阈值（clientY>480） | linux-20-bottom-menu.jpg |
| S2 Windows 穿越守卫 | 含 `..\..\evil.txt` 的远端目录下载必须整项拒绝、零落盘 | **通过** | 日志原文：`拒绝下载危险远端路径 "..\\..\\evil.txt": 路径分量 … 含 Windows 非法字符 \ 或 :（已传输 0 个文件/0 字节，剩余文件数未知）`；`批量download 1 项: 成功 0 跳过 0 失败 1`；客户机 `Get-ChildItem C:\vbox-agent -Recurse -Filter '*evil*'` 为空 | win-rdp-06.jpg、win-rdp-08.jpg |
| Windows 正常传输 | 守卫不误伤正常文件 | **通过** | normal.txt 双侧 MD5 `2f3c3dd012fff07506bc8e641ab7ba13`（客户机 Get-FileHash ↔ 宿主 md5sum） | win-rdp-09.jpg |

## 未验证项（如实列出）

- **V1 system 主题监听**：Xvfb 无操作系统深浅色切换，真机无法触发；由单测覆盖（真实形状 MediaQueryList + change 触发断言）。
- **V5 冲突对话框竞态 / V6 pending 泄漏 / V7 清理错主机**：需要多规则冲突/快速切机等编排，真机未构造；由单测覆盖（乱序 promise、参数断言）。
- **Y1/Y2/Y3 同步对账**：真机未建同步规则跑对账；由单测覆盖（含第二轮零传输、本地修改保留断言）。
- **F1 post-spawn 闸门本身**（spawn 期间 Stop 的毫秒级竞态）：真机无法稳定命中窗口；由专门单测覆盖（TestStartNoResurrectionAfterStopDuringSpawn/Shutdown）。
- **F3 WOW64 / windows/386**：本轮真机为 amd64；386 的 ProgramW6432 分支由注入单测覆盖，交叉编译通过。
- **更新状态机 F4-F8**：真机未走更新流程（需 GitHub release 夹具）；由单测覆盖。
- **V8 溢出夹取**：见上，固定窗高限制。

## 偏差与事故记录

1. **xdo 冒号丢档**：`xdo key colon` 不带 shift 打出 `;`，首轮导入报 malformed——是输入脚本问题，非应用 bug；改 `hotkey shift semicolon` 后通过。skill 坑位表早有记载。
2. **OpenSSH 按 passwd home 读配置**：隔离 HOME 下应用能枚举别名（os.UserHomeDir 尊重 $HOME）但 spawn 的 ssh 读真实 ~/.ssh/config → 按 skill 硬规则 2 备份后临时追加别名块，收尾整块删除并 md5 核对一致（`fdd861f442e080739731f70953190760`）。
3. **客户机 ssh 配置恢复 md5 不一致**：恢复脚本用 `Set-Content -Encoding ASCII` 规范化了编码/换行，且备份删除过早无法逐字节复核。已验证功能完整：acc-temp-block/acc-target 均不存在、原配置末行 intact。**宿主侧配置已用备份 cp 恢复，md5 逐字节一致**；客户机侧留此偏差记录。
4. **沙箱进程回收**：常规 bash 调用结束即回收子进程（bwrap --unshare-pid），setsid 也不够；常驻服务（Xvfb/sshd/应用）改走 harness 后台任务存活。RDP 会话由 vboxctl rdp-session 管理。
5. **SendInput 不落地**：vboxctl click 后 cursor 恒为屏幕中心、截图哈希不变——与 skill 记载一致，改 RDP 通道后输入全部生效。
6. **RDP 丢键**：0.12s 键间隔在 RDP 延迟下丢字符（路径变 `/home/lanspace`），放慢到 0.25s 后正常。

## 收尾清单

- [x] 客户机应用进程已杀（PID 8988）；部署目录 C:\vbox-agent\sshore-acc 已删；宿主共享目录 sshore-acc 已删
- [x] 客户机 ssh 配置临时块已删（功能验证；md5 偏差见上）；宿主 ~/.ssh/config 备份恢复 md5 一致
- [x] VM 已 stop、锁已 release、console-restore 已执行、RDP 进程（1565620/1565510）已 kill
- [x] Linux 应用/sshd/Xvfb 后台任务已取消
- [x] 用户真实 ~/.config/sshore/sshore.toml 未被动过（mtime 仍为 Sep 12）
- [x] 最后一次代码改动后全量 `go test ./... -race -count=1` exit=0、vitest 560/560（HEAD 确认性运行，见 .audit/head_*.log）
- [x] 未验证项已单独列出