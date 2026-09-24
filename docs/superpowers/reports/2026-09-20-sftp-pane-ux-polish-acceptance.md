# SFTP 面板交互打磨 —— 真机验收报告

- 仓库：/home/lan/workspace/scripts/sshkit
- 分支：feat/sftp-pane-ux-polish ｜ 验收时 HEAD：c9225cb（`v0.7.1-6-gc9225cb`）
- 验收时间：2026-09-24 19:05 ~ 19:20 CST（本机 `date` 输出）；fix round 1 重拍：2026-09-24 19:29 ~ 19:37 CST（同一夹具）
- fix round 1 时 HEAD：823656d（只改本报告，未改源码）
- 性质：**验证与取证**。本任务刻意不改任何源码；首次提交只含本报告，fix round 1 也只改本报告（未改任何源码）。
- 截图目录：/tmp/sshkit-t5/shots/（**在 /tmp，未提交进仓库**）
- 驱动脚本：/tmp/sshkit-t5/xdrive.py（XTEST）、/tmp/sshkit-t5/gui.sh（窗口操作封装）、/tmp/sshkit-t5/xsendevent.py（对照实验）

## 0. 结论摘要

| # | 判据 | 结论 | 关键证据（/tmp/sshkit-t5/shots/） |
|---|---|---|---|
| 1 | 单击「..」不跳转；双击「..」跳上级 | **通过** | 08、09（单击）；10（双击） |
| 2 | 本地路径栏输入不存在路径：错误入日志 + 路径与列表整体回滚 | **通过** | 13、14 |
| 3 | 远端路径栏输入 `~`；位置弹层点「主目录」——两条入口都跳远端 home；**未连接时也不得静默无反应** | **通过** | 已连接：15、16、17、21；未连接输入 `~` 回车：70、71、72；未连接点「主目录」：84、85 |
| 4 | >8 条最近：每区 ≤8 行 +「展开全部 N」；展开后能滚到最后一行且整行可见 | **通过** | 26、27、28 |
| 5 | 打开 🔍 不自动深搜（3 秒内无「搜索中… 已扫描 N 个目录」） | **通过** | 29、30、31（三帧像素差 AE=0） |
| 6 | 输入 `*` 回车：正常深搜、结果区内部滚动、最多 500 条 | **通过** | 32、33、34 |
| 7 | 弹层打开时点文件行 / 列表空白 / ☆ / 🔍 → 立即收起 | **通过**（fix round 1 重拍） | 文件行 73→74；列表空白 82→83；☆ 75b→76b；🔍 77→78（每对都是「弹层可见」前置帧 + 收起后帧） |
| 8 | 中文输入法回车确认候选不得触发跳转 | **未验证** | 环境无可用的活中文输入法，见「判据 8」 |

**分布：8 条中 7 条通过 / 0 条部分通过 / 1 条未验证（判据 8：环境无可用的活中文输入法）。未发现产品缺陷。**

> **fix round 1 订正**：首次提交的判据 7 证据（35/38/39/43 等）前置帧里弹层并未打开、不具判别力，已全部重拍为「弹层可见 → 点击后消失」；判据 3 补上了 brief 明列的「未连接时输入 `~` 不得静默无反应」分支；「未触碰用户数据」的表述按 §6 据实改为「只读枚举了真实 home 的目录名」。重拍过程与命令见 `.superpowers/sdd/2026-09-20-sftp-pane-ux-polish/task-5-report.md` 的 fix round 1 附录。

## 1. 门禁（make ci）

命令：

    make ci 2>&1 | tee /tmp/task5-make-ci.log; echo "MAKE_CI_RC=${PIPESTATUS[0]}"

Makefile:94-98 展开为：`cd frontend && npm run build` + `go vet ./...` + `go test ./... -race -count=1` + `cd frontend && npx vitest run`。

**退出码：MAKE_CI_RC=0。出处订正**：该值由终端命令里的 `echo "MAKE_CI_RC=${PIPESTATUS[0]}"` 打印，**不在** `/tmp/task5-make-ci.log` 内——日志共 67 行，末行是 `make[1]: Leaving directory '/home/lan/workspace/scripts/sshkit'`；`grep -nE 'RC|Leaving' /tmp/task5-make-ci.log` 只返回 Entering/Leaving 两条，没有任何 `RC=` 行。日志本身能佐证的「全绿」是：`✓ built in 1.21s`、`go vet ./...` 后无输出、13 个 Go 包全部 `ok`、vitest `25 passed / 325 passed`，且 make 正常打印 `Leaving directory`（目标走完）。

### 1.1 前端构建

    vite v7.3.6 building client environment for production...
    ✓ 70 modules transformed.
    dist/index.html                                        0.37 kB │ gzip:  0.27 kB
    dist/assets/nunito-v16-latin-regular-CSmEcOMa.woff2   18.97 kB
    dist/assets/index-CUPKXEp7.css                        25.27 kB │ gzip:  5.00 kB
    dist/assets/index-DRqU0BeY.js                        176.76 kB │ gzip: 63.43 kB
    ✓ built in 1.21s
    go vet ./...

`go vet ./...` 无任何输出（干净通过）。

### 1.2 Go 测试（-race -count=1）

    ok  	sshore	1.678s
    ok  	sshore/internal/config	1.344s
    ok  	sshore/internal/forward	1.887s
    ok  	sshore/internal/importer	1.017s
    ok  	sshore/internal/localfs	1.030s
    ok  	sshore/internal/namepattern	1.018s
    ok  	sshore/internal/osutil	6.448s
    ok  	sshore/internal/preset	1.018s
    ok  	sshore/internal/sftp	3.279s
    ok  	sshore/internal/sshconn	1.020s
    ok  	sshore/internal/sync	16.296s
    ok  	sshore/internal/update	14.832s
    ok  	sshore/internal/watch	6.830s

13 个包全部 `ok`，无 FAIL/SKIP。

### 1.3 前端测试（vitest）

    Test Files  25 passed (25)
         Tests  325 passed (325)
      Duration  598ms

与任务描述的基线（25 文件 / 325 用例）一致。

### 1.4 门禁副作用

`make ci` 重写了 `frontend/dist`（.gitignore 已忽略）。验收结束时 `git status --porcelain` 为空 —— 工作区仍干净。


## 2. 真机验收环境与方法

### 2.1 为什么没有用本机物理桌面 :1

计划允许「Windows 客户机或本机 GUI」，但本机 Windows VM 未运行（2222 端口拒绝连接），故走本机 GUI。**物理桌面 :1 上 XTEST 注入被一个活动指针 grab 吞掉**，客观证据：

    $ DISPLAY=:1 python3 -c 'XGrabPointer(...)'      # 自己的探测 grab
    XGrabPointer rc=1  (0=GrabSuccess,1=AlreadyGrabbed,...)   # 已有客户端持有 grab
    $ DISPLAY=:1 xev -geometry 240x140+60+60 &        # 指针确实移到 xev 内 (180,167)
    $ DISPLAY=:1 python3 xdrive.py click 180 167      # XTEST 按钮事件
    XTEST on :1 -> ButtonPress=0 KeyPress=0           # 目标窗口收不到
    # 但按钮状态确实被服务器处理了（按下期间 XQueryPointer mask=0x110 = Button1Mask|Mod2）
    $ xlsclients -display :1 | grep -E 'yunshu|nxrunner'
    lan-ai-ubuntu  yunshu-cross
    lan-ai-ubuntu  /usr/NX/bin/nxrunner.bin --monitor --pid 670762   # NoMachine

XTEST 事件被 grab 持有者消费，`xev` 收不到任何 ButtonPress/KeyPress。XSendEvent 合成事件能送达纯 X 应用（xev 收到 `synthetic YES` 的 ButtonPress/KeyPress），但 GTK/WebKit 忽略核心合成事件（实测对 sshore 无效）。
未采用的做法：杀掉用户的 yunshu-cross / NoMachine。那属于用户会话里的既有进程，本任务无授权，且可能中断其远程会话。

### 2.2 采用的回路：独立 Xvfb 显示 + 真实 GTK/WebKit 应用 + XTEST

    Xvfb :99 -screen 0 1600x1000x24 -nolisten tcp      # PID 1979698
    setsid env HOME=/tmp/sshkit-t5/home PATH=/tmp/sshkit-t5/shim:$PATH \
      DISPLAY=:99 WEBKIT_DISABLE_COMPOSITING_MODE=1 \
      /home/lan/workspace/scripts/sshkit/build/bin/sshore

- `Xvfb :99` 无第三方 grab，XTEST 可用（先用 `xev` 在 :99 上验证 ButtonPress=1 / KeyPress=1 才继续）。
- 这是同一台机器上的**真实 GTK3 + WebKitGTK 应用进程**（不是 mock、不是单测环境），窗口与标题可被 X 工具取证。
- `WEBKIT_DISABLE_COMPOSITING_MODE=1` 是必需的：否则 WebKit 走 GL 离屏渲染，X 截屏（import/xwd）只能拿到纯背景色（见早期失败截图 00/01/11-root）。该环境变量只改渲染路径，不改任何业务行为。

### 2.3 数据隔离夹具（复用 e2e/test_local.sh 的范式）

- 临时 HOME：/tmp/sshkit-t5/home（`os.UserHomeDir`/`os.UserConfigDir` 在 Linux 取 `$HOME`，故 .ssh/config 与 sshore.toml 都被隔离）。
- PATH shim：/tmp/sshkit-t5/shim/{ssh,sftp} —— OpenSSH 的用户配置取自 passwd 家目录而非 `$HOME`，所以垫片强制 `-F $HOME/.ssh/config`；**绝不读写用户真实 ~/.ssh/config**。
- 一次性 sshd（internal-sftp）：127.0.0.1:22911，临时 hostkey/authorized_keys，`PidFile /tmp/sshkit-t5/sshd.pid`，`-E /tmp/sshkit-t5/sshd.log`。
- ssh config 别名 `t5host` → 127.0.0.1:22911。**远端 home 是当前用户 `lan` 的真实家目录 `/home/lan`**（由 passwd 决定；本轮**没有**用专用用户或 chroot 隔离远端，数据边界见 §6）。
- 预置 `sshore.toml`：20 条 local_recent + 12 条 remote_recent（用于判据 4 的「>8 条」场景）。
- **应用日志文件**：/tmp/sshkit-t5/app99.log（首次运行 145 B，内容只有 WebKit 的 `Overriding existing handler for signal 10. …` 与退出时的 `Ctrl+C detected. Shutting down...`；fix round 1 重跑后为 110 B，只剩前者）。该文件里**没有任何 sftp 业务行**；`[INFO] sftp transport=gosftp` 是应用**内日志面板**渲染出来的，只在截图 07 / 72 可见，不能拿 app99.log 当它的证据。

### 2.4 窗口与截图取证

    $ DISPLAY=:99 xwininfo -root -tree | grep 'SSHore v'
     0x200003 "SSHore v0.7.1-6-gc9225cb": ("sshore" "Sshore")  1100x720+250+140  +250+140
    $ DISPLAY=:99 xprop -id 0x200003 WM_NAME
    WM_NAME(STRING) = "SSHore v0.7.1-6-gc9225cb"

截图命令：`xwd -id 0x200003 -silent | convert xwd:- <png>`（在 :1 上 `import -window` 只能拿到纯色；:99 + 关合成模式下 xwd 正常）。
点击/输入：`xdrive.py`（ctypes 调 libX11/libXtst，**所有指针参数显式声明 argtypes/restype**；未声明时 64 位指针被截断 → SIGSEGV）。窗口内坐标 = 绝对坐标 - 窗口原点 (250,140)。


## 3. 逐条验收

### 判据 1：单击「..」不跳转、双击「..」跳上级 —— 通过

- 单击前先在本地列表选中 `app.go`：截图 **08**（该行高亮、右上角「已选 1 项」）。
- 单击「..」行（窗口坐标 250,152）：截图 **09** —— 「..」行高亮（蓝色 + 左侧 accent 条）、「已选 0 项」（原选中被取消）、路径栏与列表**完全未变**（仍是 sshkit 仓库列表 17 项）。判定：未跳转 ✔。
- 双击「..」：截图 **10** —— 列表由仓库内容（17 项）变为 `/home/lan/workspace/scripts` 的内容（27 项：12306/comet/crack/crons/dsh/edu/golden/jsapi/knowledge/lz/mumu/my-agent/opencode/prompt/quark…），路径栏前缀同步为 `/home/lan/workspace/script…`。判定：跳上级 ✔。

### 判据 2：本地路径栏输入不存在路径 → 错误入日志 + 整体回滚 —— 通过

- 清空路径栏后输入 `/no/such/dir-task5`：截图 **13**（输入框完整显示该字符串，列表保持不变）。
- 回车后：截图 **14** ——
  - 路径栏回到真实目录（`/home/lan/workspace/script…`，与回车前一致）；
  - 列表 27 项逐行不变（未清空、未停在错误路径）；
  - 日志面板新增红色 ERROR：

        2026-09-24 19:12:29 [ERROR] 无法打开 /no/such/dir-task5: open /no/such/dir-task5: no such file or directory

- 判定：三项都满足 ✔。（另有一次输入被 `Ctrl+A` 未生效影响、拼成了另一个不存在路径，日志同样报 `无法打开 /home/lan/wor/no/such/dir-task5kspace/scripts` 并回滚 —— 截图 11/12，行为一致。）

### 判据 3：远端路径栏 `~` 与位置弹层「主目录」 —— 通过

**已连接态（首次验收）**

- 远端先跳根目录 `/`：截图 **15**（路径 `/`，25 项：bin/boot/cdrom/dev/etc/home/lib…）。
- 远端路径栏输入 `~`：截图 **16**（输入框显示 `~`）。
- 回车：截图 **17** —— 路径 `/home/lan`，列表为远端 home 内容（20 项：Android/Desktop/Documents/Downloads/Music/Pictures/Public…）。✔
- 再跳回 `/`（截图 **19**），打开远端「📍 位置」弹层：截图 **48**（预设区可见「主目录」「根目录」），点「主目录」后：截图 **21** —— 路径 `/home/lan`，列表回到 home 内容。✔

**未连接态（fix round 1 补测 brief 明列的禁用行为：「未连接时不得静默无反应（应连上后落 home）」）**

- 新起应用、主机 `t5host` 已选但**不点「连接」**：截图 **70** —— 按钮仍是「连接」；远端路径 `/`、列表只有 `..`、日志面板无任何 sftp 行。
- 在远端路径栏输入 `~`、回车前：截图 **71**（放大帧 `zoom-71-char.png` 核对输入确为单个 U+007E，避免把 `~~` 或 `˜` 误当 `~`）。
- 回车后（等 5s）：截图 **72** —— 按钮变「断开」（已连上）、路径 **`/home/lan`**、列表为该 home 的 20 项、日志面板出现 `[INFO] sftp transport=gosftp`。**不是静默无反应**。✔
- 同一未连接态下从「📍 位置」选「主目录」：打开弹层 **84**（未连接），点击后 **85** —— 自动连接并落 `/home/lan`，与路径栏入口一致。✔

- 结论：两条入口 × 已连接/未连接共四种组合都落在远端 home；未连接时都表现为「先连上、再落 home」。✔

### 判据 4：>8 条最近的分区截断与展开后可达最后一行 —— 通过

夹具：20 条 local_recent（rec01..rec20）+ 12 条 remote_recent。

- 未展开：截图 **26** —— 「最近」标题行右侧有「展开全部 20」，其下恰好 8 行。✔（每区 ≤8）
- 点「展开全部 20」：截图 **27** —— 全部 20 行渲染，弹层右侧出现**内部滚动条**（面板不随内容撑高）。✔
- 在弹层内滚轮向下：截图 **28** —— 最后一行（第 20 行 `/tmp/sshkit-t5/data/localdir/rec19`，因启动时又记了一次仓库路径，rec20 被 20 条上限挤掉）**整行完整可见**（基线 y≈517），弹层底边（含横向滚动条，y≈525）完整落在面板列表之上，没有被 `.pane{overflow:hidden}` 裁掉。✔
- 远端「最近」同样满足 ≤8 + 展开：截图 **48**（「展开全部 13」+ 8 行）。

### 判据 5：打开 🔍 不自动深搜 —— 通过

- 远端 🔍（窗口坐标 952,78）打开浮层，在 0.3s / 1.5s / 3.5s 各截一帧（截图 **29 / 30 / 31**）：
  - 头部标题：`🔍 在 t5host : /home/lan 下递归搜索`；
  - 提示行：`输入名称或通配符（支持 * ?）后回车搜索`；
  - 命中区**无任何行**；
  - **三帧像素完全一致**：`compare -metric AE` 依次为 `0`（差异 bounding box 为 `1x1`，即无差异）→ 3 秒窗口内既无「搜索中… 已扫描 N 个目录」文案、也无任何进度刷新。✔
- 说明：本次未做「临时给 BatchBackend.ListMany 加日志」的插桩；不自动搜索的正面证据是 UI 三帧恒等 + 空命中区 + 长驻提示文案。

### 判据 6：输入 `*` 回车正常深搜、结果区内部滚动、最多 500 条 —— 通过

- 在浮层输入框输入 `*`：截图 **32**。（本次深搜范围是**真实** home `t5host : /home/lan`，只读递归，详见 §6「数据边界」。）
- 回车后 3s：截图 **33**（运行态）；12s：截图 **34** ——
  - 总数行显示 `命中 500 项 · 触发上限（最多 500 条）`（后端 limit=500 生效）；
  - 结果列表右侧有内部滚动条、`.hits` 为 `max-height:46vh; overflow:auto`，浮层整体仍完整落在窗口内（未撑满整屏）。✔

### 判据 7：弹层打开时点面板其他位置 → 立即收起 —— 通过（fix round 1 重拍）

> **订正**：首次提交引用的 36/38/39 及前置帧 `35-item8a-open.png`、`43-item8b4-pos-open.png` 里，文件列表完全可见、**弹层并未打开**，因此不具判别力（唯一有效的旧证据是远端空白 48→49）。本轮对四个子项都重拍为「前置帧弹层可见 → 点击后弹层消失」。

| 目标（本面板） | 前置帧：弹层可见 | 后置帧：弹层消失 | 观察 |
|---|---|---|---|
| 文件行 | **73**（本地面板，弹层展开） | **74** | 弹层消失；`Makefile` 行被选中（右上「已选 1 项」） |
| 列表空白 | **82**（本地切到空目录 `/tmp/sshkit-t5/blankdir` 后打开弹层） | **83** | 弹层消失；无搜索浮层等副作用 |
| ☆ | **75b** | **76b** | 弹层消失；☆ 进入收藏态（**橙色描边空心星**——`.star.on{color:var(--seed)}`、`--seed:#f59e0b`，字形仍是 ☆ 不填充，见放大帧 `zoom-76b-head.png`） |
| 🔍 | **77** | **78** | 弹层消失；随即弹出深搜浮层（`在 /home/lan/workspace/scripts/sshkit 下递归搜索` + 空态提示） |

- 每对前后帧都做了像素级核对：弹层内取样点 (200,150) 在前置帧为 `#FFFFFF`（弹层底色），后置帧回到列表底色（74/76b 为 `#F6F8FB`，78 为深搜遮罩 `#7B7C7D`）；`compare -metric AE` 依次为 73→74 = `116312`、82→83 = `117563`、75b→76b = `103900`、77→78 = `765469`。
- 判定：四种点击都立即收起，弹层不会长期盖在列表上。✔

### 判据 8：中文输入法回车确认候选不得触发跳转 —— **未验证**

原因（客观环境不具备，非未执行）：

    $ dpkg -l | grep fcitx5 | awk '{print $2}'
    fcitx5 / fcitx5-chinese-addons / fcitx5-chinese-addons-bin / fcitx5-frontend-gtk3 ...
    $ ls /usr/share/fcitx5/inputmethod/ | head
    cangjie.conf db.conf erbi.conf pinyin.conf shuangpin.conf ...
    $ pgrep -a fcitx5 || echo 'fcitx5 NOT running'
    fcitx5 NOT running
    $ pgrep -a fcitx || echo 'fcitx NOT running'
    fcitx NOT running

- fcitx5 与中文拼音引擎**已安装但未运行**；桌面上的 `ibus-daemon` 属于 :1 会话，与验收所用的 :99 隔离显示无关。
- 在 :99 上启用 fcitx5 需要额外的 DBus 会话 + 输入法激活，并且要真的构造出「preedit 中按 Enter 确认候选」这一状态才能判定 `e.isComposing`；本次未建立该回路，因此**不给出任何结论**。
- 代码层事实（只读核对，非实测）：`frontend/src/components/FilePane.vue:35` 有显式守卫 `if (e && e.isComposing) return`；前端仓库里 `grep -rn isComposing frontend/src` 只有这一处，**没有对应单测**。
- 建议：由控制者决定是补一条针对 `commitPath` 的 `isComposing` 单测，还是在装有活中文输入法的桌面上做一次人工验证。


## 4. 过程观察（非缺陷结论）

验收过程中观察到一次「本地路径栏显示值疑似与列表不一致」：截图 **42/45** 里路径栏文本显示为 `/home/lan/`，而列表仍是仓库内容。本轮又稳定复现了一次：截图 **79a**（本地列表被过滤成 `go.mod` 一行、路径栏显示 `/home/lan/`）→ 截图 **79d**（点本地面板空白使其失焦后，路径栏**自愈**为 `/home/lan/workspace/scripts…`）。

**定性（据代码与本轮复现）**：路径栏的唯一真源是输入框自己的 `pathDraft` 草稿（`frontend/src/components/FilePane.vue:31-33`），`props.path` 变化时才回写，`@blur="resetPath()"` 会把草稿回读成 `props.path`。所以截图里的 `/home/lan/` 是**「疑似草稿 ≠ 已提交路径」**：它只是输入框的临时草稿态，失焦即自愈；同一时间列表与内部 `localPath` 仍是仓库目录（48/49 等其他帧里同一栏也显示正确路径，见放大帧 `zoom-79d-localpath.png`）。这不是「面板显示/停在错误目录」的产品缺陷，也**不需要改源码**。

触发该草稿态的具体事件序列尚未定位（脚本注入的点击/焦点切换是嫌疑），故仅按「疑似草稿≠已提交路径（可自愈）」记录，不作为缺陷上报。若控制者要追，建议在真实鼠标操作下按「点路径栏 → 失焦」顺序复核，并优先用 DOM 层断言而不是截图裁剪来判定。

## 5. 截图清单（全部在 /tmp/sshkit-t5/shots/，未提交进仓库）

### 5.1 fix round 1 重拍（本轮新增，判据 3/7 与 §4 的正证据）

| 文件 | 内容 |
|---|---|
| 70-t5-i2a-unconnected.png | 判据3：新起应用、主机已选但**未连接**（按钮「连接」，远端路径 `/`，列表只有 `..`，日志面板无 sftp 行） |
| 71-t5-i2b-tilde-typed-unconnected.png | 判据3：未连接时远端路径栏输入 `~`（放大帧 zoom-71-char.png 核对为单个 U+007E） |
| 72-t5-i2c-enter-connected-home.png | 判据3：未连接回车 → 自动连接并落 `/home/lan`（按钮「断开」+ 日志面板 `sftp transport=gosftp`） |
| 84-t5-i2d-remote-pos-open-disconnected.png | 判据3：未连接时打开远端「📍 位置」弹层 |
| 85-t5-i2e-remote-zhumulu-connected-home.png | 判据3：未连接点「主目录」→ 自动连接并落 `/home/lan` |
| 73-t5-i7a-local-pos-open.png / 74-t5-i7a-local-row-after.png | 判据7 文件行：前置帧弹层可见 → 点击后弹层消失、Makefile 被选中 |
| 82-t5-i7b-local-blank-before-open.png / 83-t5-i7b-local-blank-after.png | 判据7 列表空白：前置帧弹层可见 → 点击空白后弹层消失 |
| 75b-t5-i7c-local-star-before-open.png / 76b-t5-i7c-local-star-after.png | 判据7 ☆：前置帧弹层可见 → 点击后弹层消失、☆ 变收藏态（橙色描边空心星） |
| 77-t5-i7d-local-find-before-open.png / 78-t5-i7d-local-find-after.png | 判据7 🔍：前置帧弹层可见 → 点击后弹层消失、深搜浮层出现 |
| zoom-75b-head.png / zoom-76b-head.png | 星标区域放大（收藏态是橙色 `#f59e0b` 描边空心星，不是「黄色实心 ★」） |
| zoom-71-char.png / zoom-61b-char2.png | 输入框字符放大：证明正确证据用的是单个 `~`，早期失败帧里是两个 `~~` |
| 78b-after-esc.png | 关闭深搜浮层后的状态 |
| 79a-local-filtered.png / 79d-after-blur.png / zoom-79d-localpath.png | §4：路径栏草稿态（`/home/lan/`）→ 失焦自愈（`/home/lan/workspace/scripts…`） |
| 80-t5-local-blankdir-typed.png / 81-t5-local-blankdir.png | 判据7 空白点击的准备：本地切到空目录 /tmp/sshkit-t5/blankdir |
| 61-t5-i2-tilde-typed-unconnected.png / 61b-t5-tilde-retry.png / 62-t5-i2-tilde-enter-home.png | **作废**：夹具误敲出两个 `~`，落到不存在的 `~~` 目录并报错（因此不能证明产品行为） |
| 79c-retry-open.png / 79e-target-test.png | 过程定位（确认 (378,79) 是「位置」按钮、(540,118) 是过滤框），非判据证据 |

### 5.2 首次验收证据（判据 1–6，仍然有效）

| 文件 | 内容 |
|---|---|
| 05-xvfb-initial.png | :99 上应用启动后的初始界面（端口转发页） |
| 06-sftp-tab.png | 切到 SFTP 页（t5host / 本地 / 远程双栏） |
| 07-connected.png | 点「连接」后：远端 /home/lan，**应用内日志面板**显示 `sftp transport=gosftp` |
| 08-item1a-selected.png | 判据1：选中 app.go（已选 1 项） |
| 09-item1b-up-single.png | 判据1：单击「..」→ 该行高亮、已选 0 项、未跳转 |
| 10-item2-up-dbl.png | 判据1：双击「..」→ 跳到 /home/lan/workspace/scripts（27 项） |
| 11-item3a-typed.png / 12-item3b-after-enter.png | 判据2 的一次额外输入（Ctrl+A 未生效），日志同样报错并回滚 |
| 13-item3c-typed-clean.png | 判据2：路径栏输入 /no/such/dir-task5 |
| 14-item3d-rollback-clean.png | 判据2：回滚 + 日志面板红色 ERROR |
| 15-item4a-remote-root.png / 16-item4b-tilde-typed.png / 17-item4c-tilde-home.png | 判据3（已连接）：远端 / → 输入 ~ → 跳 /home/lan |
| 18-item4d-pos-overlay.png / 19-item4e-root-again.png / 20-item4f-overlay-open.png / 21-item4g-after-zhumulu.png | 判据3（已连接）：再回 / → 打开位置弹层 → 点主目录 → /home/lan |
| 22-item5a-local-overlay.png / 23-item5b-expanded.png / 24-item5c-scrolled-bottom.png / 25-item5d-reconnected.png | 判据4 过程帧（25 = 重连后的状态） |
| 26-item5e-overlay20.png / 27-item5f-expanded20-top.png / 28-item5g-expanded20-bottom.png | 判据4：≤8+展开全部20 → 展开 → 滚到最后一行整行可见 |
| 29-item6a-search-0.3s.png / 30-item6b-search-1.5s.png / 31-item6c-search-3.5s.png | 判据5：0.3s/1.5s/3.5s 三帧（AE=0，无自动搜索） |
| 32-item7a-star-typed.png / 33-item7b-searching-3s.png / 34-item7c-done-12s.png | 判据6：输入 * → 深搜 → 命中 500 项·触发上限（34 里可见真实 home 的 `.ssh`/`.pki`/`.keys`/`workspace` 等条目） |

### 5.3 判据 7 的首次尝试（前置帧无弹层，**已作废**，仅留档）

| 文件 | 说明 |
|---|---|
| 35-item8a-open.png / 43-item8b4-pos-open.png | **作废**：文件名叫 open，但文件列表完全可见、弹层并未打开 → 不具判别力 |
| 36-item8a-row-click.png / 38-item8c-star-click.png / 39-item8d-find-click.png | 对应的「点击后」帧（没有有效前置帧；结论已由 73→74 / 75b→76b / 77→78 取代） |
| 37-item8b-blank-click.png / 40-item8b-filtered.png / 41-item8b2-blank-click.png / 42-item8b3-clean.png / 44-item8b5-blank-after.png / 46-item8b6-remote-filtered.png | 本地/远端空白点击尝试的中间帧 |
| 45-header-zoom.png / 47-remote-header-zoom.png | 面板头放大（首次尝试） |
| 48-item8b7-remote-pos-open.png | 唯一有效的旧前置帧：远端「📍 位置」弹层确实打开 |
| 49-item8b8-remote-blank-after.png | 48 的「点击后」帧：弹层消失（该对仍有效，但结论已由 82→83 覆盖） |

### 5.4 环境 / 过程 / 失败取证（辅助）

| 文件 | 内容 |
|---|---|
| 00-initial-root.png / 00-initial-win.png / 01-win-nocomp.png / 04-sftp-sendevent.png / 11-root.png / 11-root.xwd | 早期渲染/注入失败证据（说明为何需要 :99 + `WEBKIT_DISABLE_COMPOSITING_MODE=1`） |
| 02-sftp-tab.png / 02-sidebar-zoom.png / 03-sftp-tab-b.png / 10-root-screen.png / 12-ffmpeg.png | 早期切页/整屏探测帧 |
| 50-path-history.png / 51-state-before.png / 52-state-blurred.png / 53-state-esc.png / 54-path-reset-test.png / z51.png / z52.png / z53.png | §4 路径栏现象的首次受控复测 |
| diff6.png / p_36-item8a-row-click.png / p_37-item8b-blank-click.png / p_38-item8c-star-click.png / p_39-item8d-find-click.png / p_40-item8b-filtered.png | 像素 diff 中间产物 |

> 全部 100+ 个文件都在 /tmp/sshkit-t5/shots/（含上述各文件与 zoom-* 放大派生帧）；未提交进仓库。正文引用的文件均已在上表中列出。

## 6. 清理与数据边界

- **进程/临时数据清理**：首次验收结束时清理了 sshd（pid 见 /tmp/sshkit-t5/sshd.pid）、应用（/tmp/sshkit-t5/app99.pid）、Xvfb :99（/tmp/sshkit-t5/xvfb.pid）、临时 HOME 与垫片；fix round 1 复用了同一夹具，结束后同样清理。
- **数据边界（据实订正）**：本地侧是隔离的（临时 `$HOME` + PATH 垫片，**未读写**用户真实 `~/.ssh/config`，也未覆盖任何既有 exe/脚本/配置）。但**远端不是隔离环境**：sshd 以当前用户身份运行，`~` 解析为**真实家目录** `/home/lan`；判据 6 的 `*` 深搜就是在 `t5host : /home/lan` 下**只读递归枚举真实目录名**（截图 34 命中了 `.ssh`、`.pki`、`.keys`、`workspace`、`.copilot`、`.gitconfig` 等真实条目）。整个过程只走 `ReadDir/ListMany`（枚举 + 读元数据），**没有任何写入、改名或删除**；但「未触碰用户数据」的说法不成立——准确表述是「**只读枚举了真实 home 的目录名**」。
- **已知限制 / 后续改进**：真机验收的远端应换成**专用用户或 chroot/容器**（把家目录指到临时目录），避免递归遍历真实 home。本轮未做，因此凡依赖「远端 home 内容」的判据（判据 3/6）其证据都带有「真实 home、只读」的限定。
- 日志证据保留在 /tmp/task5-make-ci.log、/tmp/task5-make-build.log、/tmp/sshkit-t5/app99.log、/tmp/sshkit-t5/sshd.log。
