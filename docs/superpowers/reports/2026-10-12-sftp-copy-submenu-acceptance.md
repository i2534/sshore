# SFTP 右键「复制」子菜单与弹框关闭策略 验收记录

**日期**：2026-10-12（执行于 2026-10-13）
**计划**：`docs/superpowers/plans/2026-10-12-sftp-copy-submenu.md`
**设计**：`docs/superpowers/specs/2026-10-12-sftp-copy-submenu-design.md`

## 结论摘要

**代码与本机可执行的全部验证通过**：构建、单元测试、交叉编译、Windows `go vet`、`make ci` 全绿。

**真机交互验收未执行** —— 本机没有可用的 Windows 客户机（VirtualBox 内核模块 `/dev/vboxdrv` 不存在），
也没有 Linux 桌面 GUI 工作台。因此「资源管理器粘贴出真实副本」「点遮罩/按 Esc 不关弹框」
「子菜单悬停与边缘翻转」这类**必须看真实界面**的条目一律为 **未验证**，不得据此认为功能已在真机可用。

## 1. 环境限制（原始输出）

```
$ VBoxManage list vms
WARNING: The character device /dev/vboxdrv does not exist. Try
           sudo /sbin/vboxconfig
         and if that is not successful, try to re-install the package.

$ which virsh
（无输出）
```

`VBoxManage` 因内核驱动缺失而挂起，已终止该探测。⇒ 无法投递或驱动任何 Windows 客户机。

## 2. 已执行并通过的验证（附命令与结果）

| 验收项 | 命令 | 结果 |
|---|---|---|
| Windows 二进制构建 | `GOCACHE=$PWD/.gocache make windows` | **EXIT=0**，产出 `build/bin/sshore.exe` |
| 产物形态 | `file build/bin/sshore.exe` | `PE32+ executable (GUI) x86-64`，4,941,312 字节 |
| Linux 能力查询 | `go test . -run TestAppClipboardFileSupportFalseOnLinux` | PASS（Linux 上 `ClipboardFileSupport()` 返回 false ⇒ 前端不渲染「文件」项） |
| 剪贴板包单测 | `go test ./internal/clipboard/ -v` | **8/8 PASS**（DROPFILES 布局、双 `\0` 终止、非 ASCII/emoji/单引号、临时目录唯一性与陈旧清理） |
| 全量门禁 | `GOCACHE=$PWD/.gocache make ci` | **EXIT=0**：`vite build` ✓ / `go vet ./...` 无输出 ✓ / `go test ./... -race -count=1` 全 ok ✓ / `npx vitest run` **45 文件 543 测试全 PASS** |
| Windows 交叉编译 | `GOOS=windows GOARCH=amd64` 与 `386`，`CGO_ENABLED=0 go build ./...` | 两条均 **EXIT=0** |
| Windows vet（CI `go-windows` job 的等价门禁） | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...` | **EXIT=0，无输出**（证明 `RtlMoveMemory` 填内存写法没有触发 `unsafeptr` 告警） |

另有前端定向证据（`npx vitest run`）：
`utils/copy.test.js` 12 项（顺序/隐藏项/非选中行/不改选中集/路径口径）、
`sftpCopyMenuWiring.test.js` 9 项、`sftpCopyFileWiring.test.js` 12 项、
`menuSubmenu*` 9 项、`dialogCloseWiring.test.js` 6 项，全部 PASS。

## 3. 未验证项（需要真机／桌面环境）

| 计划的验收项 | 未验证原因 | 需要的条件 |
|---|---|---|
| T10 Step 2 本地「文件」→ 资源管理器粘贴出副本（哈希核对，含中文/空格/单引号名与目录） | 无 Windows 客户机 | VBox 内核驱动可用 + win10 客户机 |
| T10 Step 3 远程「文件」→ 粘贴（含"本地有同名文件时不弹冲突框"） | 同上，且需真实 SSH 主机 | 同上 |
| T10 Step 4 远程多选 + 中途取消不写剪贴板 + 隐藏 dotfile 选中项 | 同上 | 同上 |
| T10 Step 5 文本两项与「复制前后选中集不变」 | 同上 | 同上 |
| T10 Step 6 子菜单悬停展开 / 父项点击开合 / 鼠标移向子面板不中途收起 / 右缘左翻 / 下缘上移 / 未连接置灰 | 无 Windows 客户机；Linux 桌面 GUI 未搭建 | 任一可交互桌面环境 |
| T10 Step 7 5 个模态框：点遮罩不关、按 Esc 不关、点按钮能关 | 同上 | 同上 |
| T10 Step 8 Linux 桌面：右键菜单无「文件」项、文本两项粘贴有效 | 未搭 Xvfb 工作台 | Linux 桌面（X11/Xvfb） |

## 4. 未验证项对应的风险（设计 §8）

1. **CF_HDROP 真机粘贴未实测** —— 这是本次改动风险最高的一环：`GMEM_MOVEABLE` 块能否被资源管理器接受、
   `SetClipboardData` 成功后所有权移交是否做对，只有真机才能确认。
2. **Windows 剪贴板线程锁定（`LockOSThread`）是否真避免死锁**未实测。
3. **Linux `navigator.clipboard.writeText` 是否可用**未实测；若不可用，Linux 的文本两项会表现为"点了没反应"，
   需回头找用户重新决策（设计 §8.3）。

## 5. 建议的下一步

在具备 Windows 客户机的机器上执行计划的 T10 Step 2–7，把第 3 节表格逐条替换为实测结果
（每条附命令与原始输出/哈希）。**在此之前，本功能不应视为已交付。**
