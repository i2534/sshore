# SFTP 面板排版修复 —— 真机验收报告（Linux + Windows）

- 分支：`feat/sftp-pane-layout-fixes`
- 被验对象：Task 1–6 的全部产出，HEAD = `876d369`
- 本任务无生产代码改动（只验证与记录）
- 验收日期：2026-09-24 23:47 – 2026-09-25 00:30（+08:00）
- 证据根目录：`/tmp/sshkit-t7/shots`（Linux，14 张）、`/tmp/sshkit-t7/shots-win`（Windows，72 张）、`/tmp/sshkit-t7/logs/make-ci.log`

## 0.1 判据表（编号 / 操作 / 期望 / 实测数值）

| 编号 | 操作 | 期望 | 实测数值 | 结论 |
|---|---|---|---|---|
| S1 | `make ci` | RC=0、vet/test/vitest/build 全绿 | `MAKE_CI_RC=0`；vitest `Test Files 30 passed (30)` / `Tests 386 passed (386)`；`go test -race` 13 包全 ok | 通过 |
| A | 无滚动条面板：列头「修改时间」右墨缘 vs 首个文件行时间值右墨缘 | diff ≤ 2 | header=456 row=455 **diff=1** | 通过 |
| A2 | 带纵向滚动条（classic，占 21px）重测 A | diff ≤ 2，且列头行与列表行同止于内容盒右边界 | 两行底边框同止于 **x=594**（442px）；header=435 row=434 **diff=1** | 通过 |
| B | 列头行内不得出现输入框色带 | 只有三段列头文字 | 8 段字形 run，最长 **27px**（名称▲ / 大小 / 修改时间▲） | 通过 |
| C | 同排 4 控件（路径框/位置/☆/🔍）上下边框 y | 完全一致、高 = 28px | 四者均 上=**55** 下=**82** → **28px** | 通过 |
| D | 点「名称」「大小」「修改时间」各一次，前后像素 diff | 每次 AE > 0 | AE = **3487 / 1903 / 672** | 通过 |
| D2 | 再点一次「修改时间」，箭头反向 | 变化区 = 箭头字形且行剖面反转 | 变化区 **8x6+577+99**；profile 反转（29px↔29px，峰值行 6→1） | 通过 |
| W1 | 点「修改时间」列头排序（用户缺陷①）；再点一次 | AE>0 且箭头反向 | AE **9217**；再点 AE **5215**；箭头字形区 10x8、profile 反转（46px↔46px） | 通过 |
| W2 | 四屏（SFTP/设置/端口转发/文件同步）量控件高 | 每个控件高 = 28px | 端口转发 28px（两排）、文件同步 28px、设置 28px、SFTP 导航行 28px | 通过 |
| W2b | 同上；侧栏导航按钮 | 同排控件 y 相同；侧栏 ≥40px | 上下边框行 x 区间**逐段相同**；侧栏项背景带 **40px** | 通过 |
| W3 | 点「📍位置」 | 弹层宽 220；底部 8px 无 >100px 的 `#8B8B8B`；分段默认 4 行、`展开全部 N` 可见 | 宽 **220px**（546→765）；底带最长 `#8B8B8B` = **0px**；默认 **4 行**；8 条预设时 OCR = `展开全部 8` | 通过 |
| W4 | Ctrl/Shift 点选、Ctrl+A、Shift+↑↓、全选/反选/清空 | 计数依次 已选 1/N → 区间 → 已选 N/N | OCR：**1/27 → 2/27 → 4 → 6 →（Ctrl+A）**；反选 **26/27**、全选 **27/27**、清空 **0** | 通过 |
| W4b | 点「已选 n ▾」 | 菜单顶边 < 底栏顶边；整块在面板矩形内；能点到「删除」 | 菜单 y **463→549**、x 696→857；底栏顶边 **543** → 向上；面板 149–578 内未裁切；**「能点到删除」未完全验证** | 部分通过 |
| W5 | 空态队列：面板底边框→分隔条上边框 | ≤ 12px（有任务时 ≥30px） | 空态 **8px** ≤ 12；**有任务态未验证** | 部分通过 |
| W6 | 按住分隔条拖 40px；再拖到顶/底 | 高度变化 ∈[30,50]；夹取后仍在 80–420 | 166→206（**Δ+40**）；拖到顶 **446**、拖到底 **106**（= 420+26 / 80+26） | 通过 |
| W7 | 过滤框用中文输入法打「测试」回车 | 过滤生效且不触发路径跳转 | **未验证**（应用窗口失去输入响应；见 §6.2） | 未验证 |
| W8 | 面板头路径框宽度 | ≥ 180px | 本地 **212px**（469→680）、远程 **211px**（933→1143） | 通过 |

## 0.2 结论摘要（读数速览）

| 判据 | 平台 | 结论 | 关键实测值 |
|---|---|---|---|
| Step 1 `make ci` | Linux | 通过 | RC=0；vitest `30 passed (30)` / `Tests 386 passed (386)` |
| A 列头/行时间列右对齐 | Linux | 通过 | header=456 row=455 **diff=1**（阈值 ≤2） |
| A2 带纵向滚动条重测 A | Linux | 通过 | 滚动条独占 21px：列头行底边框与 `..` 行底边框**同止于 x=594**；header=435 row=434 **diff=1** |
| B 列头行内无输入框色带 | Linux | 通过 | 列头行 8 段文字 run，最长 **27px**（无整段填充） |
| C 同排 4 控件等高 | Linux | 通过 | 路径框/位置/☆/🔍 上边框 y=**55**、下边框 y=**82** → 高 **28px**，四者完全一致 |
| D 点三个列头各一次 | Linux | 通过 | AE = **3487 / 1903 / 672**（均 >0） |
| D2 再点「修改时间」箭头反向 | Linux | 通过 | 变化区仅 **8x6+577+99**（箭头字形）；行内 ink profile 反转（29px↔29px） |
| W1 点「修改时间」能排序（用户缺陷①） | Windows | 通过 | AE=**9217**；再点 AE=**5215**；箭头字形区 10x8、ink profile 反转（46px↔46px） |
| W2 四屏控件等高 28px | Windows | 通过 | 端口转发 2 排 **28px**、文件同步 **28px**、设置 **28px**、SFTP 导航行 **28px** |
| W2b 同排控件 y 相同 / 侧栏未压扁 | Windows | 通过 | 上下边框行的 x 区间**逐段相同**；侧栏导航按钮高 **40px** |
| W3 位置弹层 | Windows | 通过 | 宽 **220px**（x 546→765）；底部 8px 带内 `#8B8B8B` 最长 **0px**；预设默认 **4 行**、有 `展开全部 8` |
| W4 Ctrl/Shift 点选、Ctrl+A、Shift+↑↓、三按钮 | Windows | 通过 | OCR：`1/27` → `2/27` → 区间 `4` → `6` → 全选；反选 `26/27`、全选 `27/27`、清空 `0` |
| W4b 批量菜单向上展开不被裁 | Windows | 部分通过 | 菜单矩形 y 463→549、x 696→857；底栏顶边框 y=**543** → 463 < 543（向上）；整块在面板矩形 149–578 内。**「能点到删除」未完全验证**（见 §6） |
| W5 空态队列贴分隔条 | Windows | 部分通过 | 空态：面板底边框(508) → 分隔条上边框(517) = **8px** ≤ 12；**有任务态未验证**（见 §6） |
| W6 拖分隔条 40px | Windows | 通过 | 日志面框高 166→206（**Δ+40** ∈[30,50]）；拖到顶 **446**、拖到底 **106**（= LOG_MAX 420 / LOG_MIN 80 + 26 内边距边框） |
| W7 中文输入法打「测试」回车 | Windows | **未验证** | 客户机有 zh-Hans-CN 微软拼音，但应用窗口在注入前失去输入响应（见 §6），不得默认通过 |
| W8 路径框宽 ≥180px | Windows | 通过 | 本地 **212px**（x 469→680）、远程 **211px**（x 933→1143） |

**一句话结论**：Linux 几何判据 A/A2/B/C/D/D2 全部通过；Windows W1/W2/W2b/W3/W4/W6/W8 通过，W4b 的「能点到删除」与 W5 的有任务态、W7（IME）三项因注入通道在后半程失效而**未验证**，已在 §6 附原始证据与卡点。

## 1. Step 1 门禁（make ci）

命令与原始输出（完整日志 `/tmp/sshkit-t7/logs/make-ci.log`）：

```console
$ cd /home/lan/workspace/scripts/sshkit && make ci
dist/assets/index-4EDa91-i.js                        179.62 kB │ gzip: 64.42 kB
✓ built in 1.22s
go vet ./...
go test ./... -race -count=1
ok  	sshore	1.688s
ok  	sshore/internal/config	1.329s
ok  	sshore/internal/forward	1.900s
ok  	sshore/internal/importer	1.034s
ok  	sshore/internal/localfs	1.038s
ok  	sshore/internal/namepattern	1.016s
ok  	sshore/internal/osutil	6.451s
ok  	sshore/internal/preset	1.017s
ok  	sshore/internal/sftp	3.261s
ok  	sshore/internal/sshconn	1.018s
ok  	sshore/internal/sync	16.286s
ok  	sshore/internal/update	14.821s
ok  	sshore/internal/watch	6.821s
cd frontend && npx vitest run
 Test Files  30 passed (30)
      Tests  386 passed (386)
MAKE_CI_RC=0
```

对照：本计划记录的开工前基线是 `MAKE_CI_RC=0`、`Test Files 25 passed / Tests 335 passed`；Task 1–6 新增了 5 个测试文件 / 51 条用例，与本轮 30/386 一致。

构建产物：`build/bin/sshore`（v0.7.2-10-g876d369，UPX 后 12,048,848 B）。

## 2. Linux 真机验收

### 2.1 环境与通道

```console
$ Xvfb :99 -screen 0 1600x1000x24 &            # 独立显示，绝不碰用户真实桌面 :1
$ xdpyinfo | head -3
name of display:    :99
vendor string:     The X.Org Foundation
```

- 隔离 HOME：`HOME=/tmp/sshkit-t7/home`（XDG_* 全部指向其下），app 配置写在 `$HOME/.config/sshore/`，`font_scale = 1.0`。
- 一次性 sshd：`/usr/sbin/sshd -f /dev/null -p 22911 -h <tmp>/hostkey -o AuthorizedKeysFile=<tmp>/authorized_keys -o StrictModes=no -o "Subsystem sftp internal-sftp"`，远端树 `/tmp/sshkit-t7/remote`。
- **PATH 垫片**（skill §1.4 的关键坑：OpenSSH 从 passwd 家目录而非 `$HOME` 读 `~/.ssh/config`）：
  ```bash
  cat > /tmp/sshkit-t7/shim/ssh <<'EOF'
  #!/usr/bin/env bash
  exec /usr/bin/ssh -F /tmp/sshkit-t7/home/.ssh/config "$@"
  EOF
  # sftp 同理
  ```
- 启动：`env HOME=... DISPLAY=:99 WEBKIT_DISABLE_COMPOSITING_MODE=1 PATH=/tmp/sshkit-t7/shim:$PATH build/bin/sshore`
- 窗口与取证：
  ```console
  $ DISPLAY=:99 xwininfo -root -tree | grep -i sshore
     0x200003 "SSHore v0.7.2-10-g876d369": ("sshore" "Sshore")  1100x720+250+140
  $ xprop -id 0x200003 WM_NAME
  WM_NAME(STRING) = "SSHore v0.7.2-10-g876d369"
  $ import -window 0x200003 /tmp/sshkit-t7/shots/10-local-small.png   # PNG 1100x720
  ```
- 主题：Linux 跟随系统，本机为**浅色** → `SURF=F6F8FB ELEV=FFFFFF BORD=CBD5E1 BG=EEF2F7`（色值必须参数化，否则在深色系统上判据恒不匹配）。

### 2.2 探针本身的两处必要修正（否则会假通过/假失败）

1. **必须带 `-depth 8`**（reviewer B 的假通过根因）：Q16 ImageMagick 对 16 位图输出 12 位 hex（`#222230304A4A`），老式截 6 位得到 `222230`，与背景色永不相等 ⇒ 每个像素都被当成前景、判据恒 PASS。本报告所有探针均带 `-depth 8`，窗口宽用 `identify -format '%w'` 现取。
2. **单一 `hprobe` 必须限制在目标面板内**：brief 给的 `hprobe` 裁的是整幅（`${W}x1+0+$2`），双面板窗口下它的「最后一段 run」永远属于**右面板**。改为先 `convert -crop` 出单面板，再对裁剪图跑**原样的** `hprobe/vprobe`。
3. **A2 必须排除滚动条自身的 x 带**：本机 WebKitGTK 默认是 overlay 滚动条（不吃布局宽度），但 `GTK_OVERLAY_SCROLLING=0` 下滚动条**独占 21px**；此时滚动条本身会成为行内「最后一段 run」，与列头比就是「滚动条 vs 滚动条」的假通过。故 A2 把探针裁到内容盒右边界（x1=594）。

另注：brief 的 `HD_ROW=$(vprobe "$SHOT" 250 | awk '{print $2+1}')` 在 x=250 会取到第 2 个边框（y=39，工具栏底）而非列头行底边框。实际 `HD_ROW` 由**列色彩 run 扫描**确定：列头行底边框 = 「长度 ≥20px 的 ELEV run 之后、其后紧跟 ≥20px SURF run」的那条边框 = **y=112**（见下方原始列扫描）。

### 2.3 判据 A / A2

原始列扫描（`python3 probe.py col <crop> 100 0 175`，crop 起点 x=150）确认 `HD_ROW=112`：

```
  y   79-111  ( 29px) #FFFFFF      <- 列头行背景
  y  112-112  (  1px) #CBD5E1      <- 列头行底边框 = HD_ROW
  y  113-135  ( 23px) #F6F8FB      <- '..' 行
  y  136-136  (  1px) #CBD5E1      <- '..' 行底边框
```

**判据 A（无纵向滚动条；目录 = /tmp/sshkit-t7/rt/local-small，3 个文件）**

```console
$ hprobe crop.png 102
14-15 20-33 40-44 293-296 300-311 414-421 425-426 430-456
$ hprobe crop.png 152
13-20 25-26 37-58 63-66 71-74 85-91 95-96 103-115 119-122 273-294 304-310 330-359 371-375 379-383 394-400 406-407 418-431 436-438 442-447 451-455
RESULT A: header=456 row=455 diff=1
A: PASS (diff=1 <= 2)
```

> 上述 456/455 是**裁剪坐标**（crop 起点 x0=150）；换算到窗口内绝对 x 为 **606 / 605**（两者仍差 1）。

截图：`/tmp/sshkit-t7/shots/10-local-small.png`

**判据 A2（带纵向滚动条；目录 = /tmp/sshkit-t7/rt/local-many，60 个文件）**

为让「表头在滚动容器外」这个原始缺陷真正显形，用 `GTK_OVERLAY_SCROLLING=0` 启动，使滚动条**独占布局宽度**：

```console
$ python3 bin/sbscan.py shots/12-many-classic-sb.png | head -3
  y=150: SURF from 562 to 594  colours 605..618 = 605:#CCCCCC ... 609:#CCCCCC 610:#FFFFFF ...
# 内容盒右边界 594；595..615 为滚动条槽（约 21px）
$ python3 probe.py border shots/12-many-classic-sb.png 112 150 640 "#CBD5E1" | tail -1
  left=153 right=594  right-left=441  right-left+1=442      <- 列头行底边框止于 594
$ python3 probe.py border shots/12-many-classic-sb.png 136 150 640 "#CBD5E1" | tail -1
  left=153 right=594  right-left=441  right-left+1=442      <- '..' 行底边框同样止于 594
```

```console
$ hprobe(a2 crop x=150..594) 102
14-15 20-33 40-44 272-275 279-290 393-400 404-405 409-435
$ hprobe(a2 crop x=150..594) 152
13-20 25-26 37-43 47-67 78-91 95-96 101-114 119-122 267-273 283-289 309-338 350-354 358-362 373-379 385-386 397-410 415-417 421-426 430-434
RESULT A: header=435 row=434 diff=1
A: PASS (diff=1 <= 2)
```

对照（overlay 滚动条，同一 60 文件目录）：`border row 112 → left=153 right=616`、`border row 136 → left=153 right=616`，两者仍一致。
**A2 的判别力**：内容盒宽度在两种滚动条模式下由 464px 变为 442px（Δ22px），而列头行的底边框与列表行的底边框**始终同止于同一条 x**（594/594 或 616/616）——这正是「`.hdrow` 已移入 `.list-holder` 且 sticky」的结构性质；若列头仍在滚动容器外，它会停在 616 而列表行退到 594。

截图：`/tmp/sshkit-t7/shots/11-local-many.png`（overlay）、`/tmp/sshkit-t7/shots/12-many-classic-sb.png`（classic，A2 主证据）

### 2.4 判据 B

```console
$ hprobe crop.png 102
14-15 20-33 40-44 293-296 300-311 414-421 425-426 430-456
B: header row distinct non-bg runs = 8
   run 1: 14-15 width=2 / run 2: 20-33 width=14 / run 3: 40-44 width=5     <- 名称 ▲
   run 4: 293-296 width=4 / run 5: 300-311 width=12                        <- 大小
   run 6: 414-421 width=8 / run 7: 425-426 width=2 / run 8: 430-456 width=27  <- 修改时间 ▲
```

列头行只有三段列头文字（8 段字形 run），**最长 run = 27px**；过滤框已移出列头行，没有任何输入框边框/填充形成的长色带。通过。

### 2.5 判据 C

```console
$ for x in 250 480 540 585; do vprobe crop.png $x; done
C x=250   borders: 12 39 48 55 82 112 136 481 485 512 516 525 533 542 555 582 707
C x=480   borders: 48 55 82 112 136 481 492 506 516 525 533 542 707
C x=540   borders: 48 55 82 112 136 481 516 525 533 542 707
C x=585   borders: 48 55 82 112 136 481 516 525 533 542 707
```

四列（路径框 x=250、📍位置 x=480、☆ x=540、🔍 x=585）的**上边框 y=55、下边框 y=82** 完全一致 → 高度 = 82−55+1 = **28px** = `--ctl-h: calc(28px * 1.0)`。通过。

（控件 x 区间由 row 86 的边框色 run 得到：路径框 219-430、位置 454-509、☆ 528-551、🔍 570-601。）

### 2.6 判据 D / D2

```console
$ bash bin/run-d2.sh
D[名称] click=(429,242) AE=3487  -> PASS
D[大小] click=(681,242) AE=1903  -> PASS
D[修改时间] click=(814,242) AE=672  -> PASS
D[修改时间-again] click=(814,242) AE=36  -> PASS
```

D2 箭头反向（数值证据，非肉眼）：

```console
$ convert shots/d-4-before.png shots/d-4-after.png -compose difference -composite -threshold 0 -trim -format '%wx%h+%X+%Y' info:
8x6+577+99                       <- 两次点击之间唯一变化的就是箭头字形（8x6）
$ python3 bin/arrow.py shots/d-4-before.png shots/d-4-after.png 576 586 98 106
  before rows: [0, 2, 3, 4, 6, 6, 8, 0, 0]  total=29   <- 最宽的行在**下**边 = ▲（升序）
  after  rows: [0, 8, 6, 6, 4, 3, 2, 0, 0]  total=29   <- 最宽的行在**上**边 = ▼（降序）
  verdict: profile INVERTED (arrow reversed)
```

截图：`/tmp/sshkit-t7/shots/d-{1..4}-{before,after}.png`、`20-d-before.png`

## 3. Windows 真机验收

### 3.1 环境与通道（与 brief 预设通道的差异，必须先说明）

侦察结果：`VBoxManage list runningvms` 显示 `win10` 在运行，但**宿主机任何显示（:1 与 :99）上都没有 VirtualBox 控制台窗口**（`xwininfo -root -tree` 无相关窗口、`ps` 无 GUI 前端进程）——客户机是 **headless** 方式运行的。因此 brief 预设的「宿主侧 XTEST 点 VirtualBox 控制台窗口 + 坐标映射（title+menu ≈52px）」在本环境**不可执行**（没有可点的控制台窗口，也就没有坐标映射对象）。

实际采用的等价通道（仍是**真实 Windows、真实内核输入**，且不需要坐标映射）：

| 环节 | 做法 |
|---|---|
| 客户机访问 | `ssh -p 2222 -i ~/.ssh/id_ed25519 lan@127.0.0.1`（项目既有约定，NAT `sshfwd 2222→22`） |
| GUI 起到交互会话 | `schtasks /create /tn t7app /tr "C:\Users\lan\rt\run-t7.cmd" /sc once /st 00:00 /it /f` + `schtasks /run`（SSH 起的会落 Session 0 → WebView2 `800700aa`，故必须 `/it`）；`query session` 确认 `console lan 1 Active` |
| 输入注入 | **会话 1 内的 guest 侧注入代理**：`t7agent.ps1`（PowerShell）用 `Add-Type` 内联 C# 调 `user32!SendInput / SetCursorPos / GetWindowRect / EnumWindows`，轮询 `C:\Users\lan\rt\t7cmd.txt` 执行 `click/move/dblclick/drag/ctrlclick/shiftclick/shiftdown/vk/combo/uni/windows/fg/activate/cursor/screen`。agent 本身由 `schtasks /it` 起到会话 1（`screen=1600x900` 与真机一致）。 |
| 取证 | `VBoxManage controlvm win10 screenshotpng /tmp/sshkit-t7/shots-win/NN.png`（headless 可用），PNG 1600x900 |
| 几何/颜色 | 复用 Linux 的 `hprobe/vprobe`（同样 `-depth 8`）与 `-crop` 到窗口矩形 |

窗口与主题：

```console
$ # 由注入代理枚举（GetWindowRect）
40312|SSHore t7-win|250,70,1350,790|normal
$ # 客户机 1600x900；浅色主题同色板 SURF=F6F8FB ELEV=FFFFFF BORD=CBD5E1 BG=EEF2F7
```

被验二进制：`wails build -platform windows/386 -nopackage -skipbindings -clean -ldflags "-s -w -X main.Version=t7-win" -trimpath -upx` → `build/bin/sshore.exe` 4,292,096 B，拷到 `C:\Users\lan\rt\sshore-t7.exe`（**新文件名，不覆盖用户既有 exe**）。
**386 前提**（skill §2.9 #2）：32 位进程看不到 `C:\Windows\System32\OpenSSH\sftp.exe`（WOW64 重定向），启动器把 `C:\Users\lan\bin` 前置 PATH（该目录已有 sftp.exe/ssh.exe 副本）。
隔离：`APPDATA=C:\Users\lan\rt\t7appdata`、`LOCALAPPDATA=C:\Users\lan\rt\t7localappdata`，应用配置全部落在沙箱，不写用户 `%APPDATA%\sshore`（见 §7 证明）。

顶部布局标定（`hlines.py` 找整宽边框行，窗口内坐标 = 客人坐标−（250,70））：

```console
$ python3 bin/hlines.py rt/win-conn.png 0 720 800
  y=  79 guest= 149 n=880     <- 面板顶
  y=  86 guest= 156 n=648     <- 导航行控件上边框
  y= 113 guest= 183 n=652     <- 导航行控件下边框（高 28px）
  y= 143 guest= 213 n=882     <- 列头行底边框 (HD_ROW)
  y= 169 guest= 239 n=882     <- '..' 行底边框
  y= 473 guest= 543 n=912     <- 过滤栏上边框
  y= 508 guest= 578 n=880     <- 面板底边框
  y= 517 guest= 587 n=920     <- 分隔条上边框
  y= 525 guest= 595 n=920     <- 分隔条下边框
  y= 534 guest= 604 n=904     <- 日志面板上边框
  y= 699 guest= 769 n=904     <- 日志面板下边框
```

### 3.2 W1 —— 点「修改时间」能排序（用户缺陷①）通过

```console
$ compare -metric AE shots-win/40-sftp-base.png shots-win/41-sort-modtime.png null:
9217
$ compare -metric AE shots-win/41-sort-modtime.png shots-win/42-sort-modtime2.png null:
5215
$ # 把差异限制在列头行（guest y=193..214）后再看：一次点击把活动列从「名称」切到「修改时间」
$ ... header-band AE(base->click1) = 722, bbox = 456x22+161+0
$ ... header-band bbox(click1->click2) = 10x8+583+5        <- 只是「修改时间」的箭头字形
$ python3 bin/arrow.py shots-win/41-sort-modtime.png shots-win/42-sort-modtime2.png 831 845 195 209
  before rows: [0,0,0,2,3,4,5,6,7,9,10,0,0,0,0]  total=46   <- 最宽在**下** = ▲
  after  rows: [0,0,0,10,9,7,6,5,4,3,2,0,0,0,0]  total=46   <- 最宽在**上** = ▼
  verdict: profile INVERTED (arrow reversed)
```

截图：`40-sftp-base.png` → `41-sort-modtime.png` → `42-sort-modtime2.png`（列表中 `t7out.txt/t7appdata/sshore-t7.exe` 等按修改时间重排，箭头 ▼）。两次点击 AE 均 > 0，且箭头字形反转。通过。

### 3.3 W2 / W2b —— 四屏控件等高 28px、同排 y 相同、侧栏未压扁 通过

用统一的「找一对相距 20–40px、且两行的边框色 x 区间**逐段相同**的边框行」检查：

```console
$ python3 bin/w2check.py rt/90-forward.png:120:1080 rt/91-sync.png:120:1080 rt/92-settings.png:0:1100 rt/sftp-base.png:120:1080
== 90-forward.png
  control row top=56 bottom=83 height=28 px ; 10 controls, x-extents match: [(148,148),(161,161),(180,268),(289,330),(614,614),(627,627),(646,796),(817,875),(896,925),(1079,1079)]
  control row top=137 bottom=164 height=28 px ; 7 controls, x-extents match: [... (180,535),(554,595) ...]
== 91-sync.png
  control row top=56 bottom=83 height=28 px ; 10 controls, x-extents match: [...]
== 92-settings.png
  control row top=56 bottom=83 height=28 px ; 10 controls, x-extents match: [...]
```

SFTP 页导航行（直接量 4 个控件，W2b 的「窗口内任何一排控件」样本）：

```console
$ python3 probe.py border rt/win-conn.png 86 0 1100 "#CBD5E1"   # 上边框行
  window-local: 148-148 219-430 454-509 528-551 570-601 683-893 917-973 992-1014 1033-1064
$ python3 probe.py border rt/win-conn.png 113 0 1100 "#CBD5E1"  # 下边框行
  window-local: 148-148 219-430 454-509 528-551 570-601 683-893 917-973 992-1014 1033-1064
# guest: 本地路径框 469-680(212px) 位置 704-759 ☆ 778-801 🔍 820-851
#        远程路径框 933-1143(211px) 位置 1167-1223 ☆ 1242-1264 🔍 1283-1314
```

上下边框行的 x 区间**完全相同** ⇒ 同一排控件的上下边框 y 相同且高度 = 113−86+1 = **28px**。
侧栏导航按钮（W2b 的「没被压成 28px」）：

```console
$ python3 probe.py col rt/win-conn.png 100 100 180      # 侧栏 x=100
  y  119-158  ( 40px) #E8EDF4    <- 当前项背景带高 40px
```

截图：`shots-win/90-forward.png`、`91-sync.png`、`92-settings.png`、`94-sftp.png`、`32-connected.png`

### 3.4 W3 —— 位置弹层 通过

```console
$ python3 bin/popgeo.py shots-win/43-posmenu.png 500 820 185 450
# popover left=546 right=765 width=220
# popover top=186 bottom=430 height=245
# last-8px band above popover bottom=430: longest #8B8B8B run = 0px
# verdict: SCROLLBAR-ABSENT (threshold >100px)
```

- 宽 **220px**（= `POS_MENU_WIDTH`），左右边框 x=546/765。
- 底部 8px 带内 `#8B8B8B` 最长连续 **0px** ⇒ 无横向滚动条长带。
- 预设分段默认 **4 行**：截图 `43-posmenu.png` 中「预设」组恰为 主目录/桌面/下载/文档 四行。
- 「展开全部 N」可见性：默认预设只有 4 条（不溢出，故无该链接）；把沙箱 `presets.toml` 扩到 8 条本地预设后重开：

```console
$ python3 bin/w4an3.py ocr shots-win/51-posmenu8.png 540,180,775,440 6
  51-posmenu8.png = 预设 展开全部 8 主目录 卓面 下载 | ... ; C: Zz: 有最近 C:\Users\lan\rt
```

即溢出时仍只显示 4 行 + `展开全部 8`。截图：`43-posmenu.png`、`51-posmenu8.png`

### 3.5 W4 —— Ctrl/Shift 点选、Ctrl+A、Shift+↑↓、三按钮 通过

底栏计数文本用 tesseract（`chi_sim+eng`）读，截图见 `shots-win/5*.png`、`6*.png`、`7*.png`：

| 步骤 | 命令（注入动作） | OCR / 实测 | 期望 |
|---|---|---|---|
| 普通点击第 1 行 | `click 520 275` | `已选 1/27 项`（`53-w4-plain.png`） | 已选 1 |
| Ctrl+点击第 3 行 | `ctrlclick 520 323` | `已选 2/27 项`（`54-w4-ctrl.png`） | +1 |
| Shift+点击第 6 行 | `shiftclick 520 395` | `已选 4`（`55-w4-range.png`，区间=第 3–6 行） | 区间 |
| Shift+↓ ×2 | `shiftdown 2` | `已选 6`（`56-w4-shiftdown.png`） | 区间扩展 |
| Ctrl+A | `combo 11 41` | 全选（`70/81/83`；选中行高亮带检测 26–27 段） | 已选 N/N |
| 反选 | `click 652 563`（该态下按钮右移，见注） | `已选 26/27 项`（`63-btn-invert.png`） | 1 → 26 |
| 全选 | `click 598 563` | `已选 27/27 项`（`64-btn-selectall.png`） | 27 |
| 清空 | `click 682 563` | 计数区 OCR 为空（0） | 0 |

> 注：过滤栏是 `[过滤框 flex:1][全选][反选][清空][selinfo][已选 n ▾]`。`已选 n ▾` 出现后 `flex:1` 的过滤框收窄，其后按钮整体左移（实测：0 选时全选 712-735／反选 754-777；1 选时 599-622／641-664／683-706；27 选时 587-610／629-652／671-694）。第一次按固定坐标点三按钮因此空点，改为**按态取坐标**后全部命中——这也是判据数值可信的必要条件。

### 3.6 W4b —— 批量菜单向上展开 部分通过

菜单矩形（从菜单已打开的 `70-w4b-before.png` 直接量边框）：

```console
$ python3 probe.py col shots-win/70-w4b-before.png 750 440 570 | head -6
  y  463-463  (  1px) #CBD5E1     <- 菜单上边框
  y  464-471  (  8px) #FFFFFF
$ python3 probe.py row shots-win/70-w4b-before.png 520 660 880 | head -4
  x  696-696  (  1px) #CBD5E1     <- 菜单左边框
$ python3 probe.py row shots-win/70-w4b-before.png 500 660 880 | tail -8
  x  857-857  (  1px) #CBD5E1     <- 菜单右边框
# 菜单矩形：x 696..857（宽 162 ≈ min-width 160 + 2 border），y 463..549
```

- **向上展开**：菜单顶边 y=463 **<** 过滤栏顶边框 y=543（`hlines` 的 `y=473 guest=543`）。
- **未被 `overflow:hidden` 裁掉**：面板矩形 guest y=149..578，菜单 463..549 完整落在其内；截图里 4 项（上传到远程(27) / 上传文件… / 重命名 / 删除(27)）全部完整渲染。
- **「能点到删除」：未完全验证**。我没有对「删除」发出真实点击（破坏性操作）；改为把指针移到该项上做单像素高亮检测（`.bmenu button:hover` → `background: var(--surface-hover)`），但两次取证之间菜单整体位移了约 9px（`70`：x=702 处白色带 y=464..549；`86`：y=455..554），单像素对比因此不成立，不能算证据。故本项只认「向上 + 不被裁」两半。

截图：`70-w4b-before.png`（菜单打开、四项可见）、`86-w4b-hover-delete.png`

### 3.7 W5 —— 空态队列贴分隔条 部分通过

空态（无任何传输任务）：

```console
$ python3 bin/splity.py rt/71-split-base.png 70 150
  full-width rows (window-local): [169, 508, 517, 525, 534, 699]  counts: [882, 880, 920, 920, 904, 904]
  panes_bottom=508 splitter_top=517 splitter_bottom=525 logpane_top=534
  gap panes_bottom -> splitter_top = 8 px
  logpane height = 175 px
```

**面板底边框 → 日志分隔条上边框 = 8px ≤ 12px** 通过（正好等于 `.sftp` 的 `gap: 8px`，即空态队列完全不占高度，Task 5 生效）。
**有任务态（应被队列撑到 ≥30px）：未验证** —— 见 §6。

截图：`71-split-base.png`（空态）

### 3.8 W6 —— 拖分隔条 40px 通过

判据口径：`.logpane` 的渲染高度（border-box）= `logHeight` + 26px（上下 padding 12px×2 + 边框 2px）；常量取自源码 `frontend/src/utils/logpane.js`：`LOG_MIN_H=80 / LOG_MAX_H=420 / LOG_DEFAULT_H=140`。

```console
$ python3 bin/splity.py rt/72-split-up40.png 70 150      # 从 (800,591) 拖到 (800,551)，即上移 40px
  panes_bottom=468 splitter_top=477 splitter_bottom=485 logpane_top=494
  gap panes_bottom -> splitter_top = 8 px
  splitter centre (guest y) = 551
  logpane height = 206 px
$ python3 bin/splity.py rt/79-split-top.png 70 150       # 继续拖到顶部
  logpane height = 446 px
$ python3 bin/splity.py rt/80b.png 70 150                # 再拖到底部
  logpane height = 106 px
```

| 状态 | logpane 渲染高 | 反推 logHeight | 判定 |
|---|---|---|---|
| 初始 | 166 | 140 = `LOG_DEFAULT_H` | — |
| 拖 40px | 206 | 180 | **Δ=+40 ∈ [30,50]** 通过 |
| 拖到顶（越过上限） | 446 | 420 = `LOG_MAX_H` | 夹取到 420 ∈[80,420] 通过 |
| 拖到底（越过下限） | 106 | 80 = `LOG_MIN_H` | 夹取到 80 ∈[80,420] 通过 |

截图：`71-split-base.png`、`72-split-up40.png`、`79-split-top.png`、`80-split-bottom.png`

### 3.9 W8 —— 路径框宽 ≥180px 通过

来源同 §3.3 的导航行边框行（row 86 与 row 113 的 x 区间逐段相同）：

| 面板 | 路径框 x 区间（guest） | 宽度 |
|---|---|---|
| 本地 | 469 → 680 | **212px** |
| 远程 | 933 → 1143 | **211px** |

两者均 ≥ 180px（修复前约 108px）。截图：`32-connected.png`、`94-sftp.png`

## 4. 文件清单（截图证据）

| 文件 | 内容 |
|---|---|
| `/tmp/sshkit-t7/logs/make-ci.log` | `make ci` 全量输出（MAKE_CI_RC=0） |
| `/tmp/sshkit-t7/shots/00-boot.png` | Linux 应用冷启动 |
| `/tmp/sshkit-t7/shots/01-sftp-boot.png` | Linux SFTP 页（本地=仓库目录，已出现纵向滚动条） |
| `/tmp/sshkit-t7/shots/10-local-small.png` | 判据 A/B/C 取证（无滚动条，3 个文件） |
| `/tmp/sshkit-t7/shots/11-local-many.png` | 60 文件目录（overlay 滚动条） |
| `/tmp/sshkit-t7/shots/12-many-classic-sb.png` | **A2 主证据**（`GTK_OVERLAY_SCROLLING=0`，滚动条独占 21px） |
| `/tmp/sshkit-t7/shots/d-1..4-{before,after}.png` | 判据 D/D2 像素 diff 对 |
| `/tmp/sshkit-t7/shots-win/30-boot.png`、`31-sftp.png`、`32-connected.png` | Windows 应用启动 / SFTP 页 / 连接 winlocal（84 项，`sftp transport=gosftp`） |
| `/tmp/sshkit-t7/shots-win/40..42-*.png` | W1 排序前后与箭头反转 |
| `/tmp/sshkit-t7/shots-win/43-posmenu.png`、`51-posmenu8.png` | W3 弹层（4 行 / 展开全部 8） |
| `/tmp/sshkit-t7/shots-win/52..65-*.png`、`70-w4b-before.png`、`86-w4b-hover-delete.png` | W4 各选择态与 W4b 菜单 |
| `/tmp/sshkit-t7/shots-win/71..80b-*.png` | W5 空态 / W6 分隔条四种状态 |
| `/tmp/sshkit-t7/shots-win/90-forward.png`、`91-sync.png`、`92-settings.png`、`94-sftp.png` | W2 四屏 |

## 5. 本次未触碰用户数据

**Linux**：应用以 `HOME=/tmp/sshkit-t7/home`、`XDG_*` 全部指向该临时 HOME 运行；夹具 sshd 使用临时 hostkey 与临时 `authorized_keys`；`ssh/sftp` 经 PATH 垫片强制 `-F /tmp/sshkit-t7/home/.ssh/config`，因此既没有读也没有写用户的 `~/.ssh/config`（这正是 skill §1.4 记录的 passwd-家目录泄漏坑）。用户真实桌面 `:1` 未被使用（判据全程在独立 `Xvfb :99`）。

**Windows**：
- 应用以 `APPDATA=C:\Users\lan\rt\t7appdata`、`LOCALAPPDATA=...\t7localappdata` 启动，配置全部落在沙箱；**没有写过**用户 `%APPDATA%\sshore`。
- 既有 exe 哈希前后一致：
  ```console
  $ certutil -hashfile C:\Users\lan\sshore.exe SHA256
  f7ba4792dc107126208ebfff20573d8b27b490b475bbd8d3403dcca6901b3c0f   # 运行前
  f7ba4792dc107126208ebfff20573d8b27b490b475bbd8d3403dcca6901b3c0f   # 运行后（一致）
  $ dir C:\Users\lan\sshore.exe
  2026/09/16  18:54         4,424,192 sshore.exe                      # 大小/mtime 未变
  ```
- 用户配置目录文件清单与 mtime 前后一致（全部早于本次会话 23:47–00:30）：
  ```console
  $ dir C:\Users\lan\AppData\Roaming\sshore
  2026/09/16  19:10             1,163 presets.toml
  2026/09/16  19:10             1,163 presets.toml.t8b-bak
  2026/09/20  09:58             2,037 sshore.toml
  2026/09/24  20:07    <DIR>          state          # state/*.json 亦为 20:07
  ```
- 客户机临时目录归还原状：清理后 `C:\Users\lan\rt` = `18 File(s) 51,259,826 bytes`，与运行前清单逐字节一致。
- 未删除/重命名任何用户文件；被误触发的「上传文件」原生对话框用 Escape 取消、误开的记事本已关闭。

## 6. 未通过 / 未验证项与原因

### 6.1 未验证：W5「有任务时队列把面板撑到 ≥30px」

- **尝试了什么**：① `schtasks /it` 起 GUI 后，把远程面板导航到自建临时目录 `C:\Users\lan\rt\t7remote`（内含 12B 的 `dl-src.txt`），随后双击该文件触发下载（`dblclick 1120 251`）；② 也尝试过走右键上下文菜单的「上传到远程」。
- **卡在哪一步**：双击确实启动了下载（系统的记事本随后打开了该文件——说明下载+打开链路走通），但在那之后**应用窗口开始完全不接受注入输入**，无法再驱动界面、也就无法在传输进行时取到队列态的几何。
- **输入通道本身没坏（这是关键区分证据）**：
  ```console
  $ # 同一条注入通道、同一时刻：点开始按钮 ⇒ 帧缓冲大幅变化
  $ send "click 28 876"   ; shotwin shots-win/G2.png
  $ compare -metric AE shots-win/G1.png shots-win/G2.png null:
  433675                      <- 注入有效
  $ send "move 400 300"; send "cursor"
  cursor=400,300              <- SetCursorPos/GetCursorPos 精确往返
  $ send "appactivate SSHore"
  appactivate=True fg=SSHore t7-win   <- 应用确为前台
  $ # 但应用窗口连自身标题栏都不响应
  $ send "click 1250 80"    # 最小化按钮
  $ ... GetWindowRect 仍为 250,70,1350,790|normal   AE(d1,d2)=0
  ```
- **结论**：证据指向「应用窗口（Wails/WebView2）本身在这段时间内不再处理输入」，而不是注入坐标或焦点错误；我**没有**把任何此后的截图当作判据证据。按任务裁定记「未验证」，不默认通过。
- 空态（判据真正的回归点）已通过：`8px ≤ 12px`。

### 6.2 未验证：W7 中文输入法在过滤框打「测试」并回车

- **前置条件已具备**：客户机确实装有中文输入法与拼音方案：
  ```console
  $ powershell -NoProfile -Command "Get-WinUserLanguageList | ConvertTo-Json -Compress"
  [{"LanguageTag":"zh-Hans-CN","Autonym":"中文(中华人民共和国)", ...,
    "InputMethodTips":["0804:{81D4E9C9-1D3B-41BC-9E6C-4B40BF79E35E}{FA550B04-5AD7-411F-A5AC-CA038EC515D7}"]}]
  ```
  任务栏可见「拼／英」指示（截图 `shots-win/30-boot.png` 右下角）。
- **卡在哪一步**：计划用 `vk 43/45/53/48/49` 打 `ceshi` + `vk 20`(Space) 上屏，或退化用 `uni 测试`（SendInput `KEYEVENTF_UNICODE`）；但触发点是**同一个**应用窗口失去输入响应问题（§6.1），注入前就无法把字符送进过滤框，因此**两条路径都没能实测**。
- **结论**：「未验证」，未默认通过，也未用 unicode 注入冒充 IME 通过。
- 附带说明：`uni`（unicode 注入）与中文输入法共用同一 DOM 事件路径（`input` + `keydown.enter`），但这只是推断，不作为证据。

### 6.3 未完全验证：W4b「能点到删除」

见 §3.6 —— 菜单「向上 + 不被面板裁切」有数值证据；「能点到删除」因不发出破坏性点击、且单像素 hover 对比不成立（菜单两次取证间位移 9px），记「未完全验证」。

## 7. 收尾清理

| 对象 | 动作 | 结果 |
|---|---|---|
| 本地应用 | `pkill -x sshore` | 已停 |
| 夹具 sshd | `kill <pid 2510424>` | 已停 |
| `Xvfb :99` | `kill <pid 2510466>` | 已停 |
| 隧道 | — | 本次未开隧道（`ssh` 全部短连接） |
| Linux 夹具 | 删除 `/tmp/sshkit-t7/{rt,home,shim,remote,win,bin}` | 已删；**保留 `shots/`、`shots-win/`、`logs/` 作为证据** |
| 客户机临时文件 | `del sshore-t7.exe t7agent.ps1 t7cmd.txt t7out.txt run-t7.cmd dl-src.txt`；`rmdir t7appdata t7localappdata t7remote` | 已删；`rt` 恢复为 18 文件 51,259,826 B |
| 计划任务 | `schtasks /delete /tn t7app /f`、`/tn t7agent /f` | 已删（`schtasks /query | findstr t7` 无输出） |
| 误开程序 | `taskkill /F /IM notepad.exe`、取消原生文件对话框 | 已处理 |
| **VM 状态** | 发现时 `win10` 为 **running（headless）**；我在 23:56 重启为 headless，收尾执行 `VBoxManage controlvm win10 savestate` → **State: saved**（可 resume，无硬断电） | 见下 |

```console
$ VBoxManage showvminfo win10 | grep '^State'
State:                       running (since 2026-09-24T15:56:11.743000000)   # 收尾前
$ VBoxManage controlvm win10 savestate
0%...10%...20%...30%...40%...50%...60%...70%...80%...90%...100%
$ VBoxManage showvminfo win10 | grep '^State'
State:                       saved (since 2026-09-24T16:28:53.396000000)     # 收尾后
```

> 说明：本机 VM 在我接手前（15:49Z）曾被外部关机，我在 15:56Z 以 headless 重启；按任务收尾口径「VM 若启动过要恢复原状态（savestate，别硬断电）」执行 savestate。如需恢复，`VBoxManage startvm win10` 即可从保存态继续。

## 8. 方法学备注（供复核）

1. 本报告所有像素判据均使用 `convert -depth 8`，窗口宽 `identify -format '%w'` 现取；色值按主题参数化（本机 Linux/Windows 均为浅色板）。
2. brief 的 `hprobe` 裁整幅，双面板下「最后一段 run」属于右面板 —— 先按面板 `-crop` 再跑原样探针。
3. `HD_ROW` 由列色 run 扫描确定（=112），brief 里 `awk '{print $2+1}'` 的启发式在本布局会取到工具栏底边框（y=39→40），故按 brief 括注「按上一步输出填实际值」显式取 112。
4. A2 的判别力依赖**会占布局宽度的滚动条**；本机 WebKitGTK 默认 overlay（不占宽），故额外用 `GTK_OVERLAY_SCROLLING=0` 造出 classic 滚动条，使「表头若在滚动容器外就会错位 22px」这一条件真正成立。
5. Windows 侧控件高度判据用「一对边框行 + 两行边框色 x 区间逐段相同」的通用检查，避免依赖单点猜测坐标；过滤栏按钮位置随 `已选 n ▾` 出现而左移，所有点击坐标按态重取（§3.5 注）。
6. Windows 通道与 brief 预设不同（headless 无控制台窗口），已改用会话 1 内的 guest 侧 `SendInput` 代理；输入等价且无需坐标映射，取证仍用 `controlvm screenshotpng`。
