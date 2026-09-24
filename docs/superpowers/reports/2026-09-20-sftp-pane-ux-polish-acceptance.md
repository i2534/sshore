# SFTP 面板交互打磨 —— 真机验收报告

- 仓库：/home/lan/workspace/scripts/sshkit
- 分支：feat/sftp-pane-ux-polish ｜ 验收时 HEAD：c9225cb（`v0.7.1-6-gc9225cb`）
- 验收时间：2026-09-24 19:05 ~ 19:20 CST（本机 `date` 输出）
- 性质：**验证与取证**。本任务刻意不改任何源码；首次提交只含本报告。
- 截图目录：/tmp/sshkit-t5/shots/（**在 /tmp，未提交进仓库**）
- 驱动脚本：/tmp/sshkit-t5/xdrive.py（XTEST）、/tmp/sshkit-t5/gui.sh（窗口操作封装）、/tmp/sshkit-t5/xsendevent.py（对照实验）

## 0. 结论摘要

| # | 判据 | 结论 | 关键证据（/tmp/sshkit-t5/shots/） |
|---|---|---|---|
| 1 | 单击「..」不跳转；双击「..」跳上级 | **通过** | 08、09（单击）；10（双击） |
| 2 | 本地路径栏输入不存在路径：错误入日志 + 路径与列表整体回滚 | **通过** | 13、14 |
| 3 | 远端路径栏输入 `~`；位置弹层点「主目录」——两条入口都跳远端 home | **通过** | 15、16、17、21、48 |
| 4 | >8 条最近：每区 ≤8 行 +「展开全部 N」；展开后能滚到最后一行且整行可见 | **通过** | 26、27、28 |
| 5 | 打开 🔍 不自动深搜（3 秒内无「搜索中… 已扫描 N 个目录」） | **通过** | 29、30、31（三帧像素差 AE=0） |
| 6 | 输入 `*` 回车：正常深搜、结果区内部滚动、最多 500 条 | **通过** | 32、33、34 |
| 7 | 弹层打开时点文件行 / 列表空白 / ☆ / 🔍 → 立即收起 | **通过** | 36（文件行）、49（列表空白）、38（☆）、39（🔍） |
| 8 | 中文输入法回车确认候选不得触发跳转 | **未验证** | 环境无可用的活中文输入法，见 §3.8 |

**分布：8 条中 7 条通过、1 条未验证。未发现产品缺陷。**

## 1. 门禁（make ci）

命令：

    make ci 2>&1 | tee /tmp/task5-make-ci.log; echo "MAKE_CI_RC=${PIPESTATUS[0]}"

Makefile:94-98 展开为：`cd frontend && npm run build` + `go vet ./...` + `go test ./... -race -count=1` + `cd frontend && npx vitest run`。

**退出码：MAKE_CI_RC=0**（原始输出见 /tmp/task5-make-ci.log）。

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
- ssh config 别名 `t5host` → 127.0.0.1:22911。远端 home 由 passwd 决定 = `/home/lan`（只做只读列目录；未做任何写/删操作）。
- 预置 `sshore.toml`：20 条 local_recent + 12 条 remote_recent（用于判据 4 的「>8 条」场景）。
- 应用日志：/tmp/sshkit-t5/app99.log；应用内日志面板显示 `[INFO] sftp transport=gosftp`（走 Go 直连后端）。

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

- 远端先跳根目录 `/`：截图 **15**（路径 `/`，25 项：bin/boot/cdrom/dev/etc/home/lib…）。
- 远端路径栏输入 `~`：截图 **16**（输入框显示 `~`）。
- 回车：截图 **17** —— 路径 `/home/lan`，列表为远端 home 内容（20 项：Android/Desktop/Documents/Downloads/Music/Pictures/Public…）。✔
- 再跳回 `/`（截图 **19**），打开远端「📍 位置」弹层：截图 **48**（预设区可见「主目录」「根目录」），点「主目录」后：截图 **21** —— 路径 `/home/lan`，列表回到 home 内容。✔
- 两条入口都落在远端 home；未连接时的静默无反应问题不在本次覆盖范围（验收时会话已连接）。

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

- 在浮层输入框输入 `*`：截图 **32**。
- 回车后 3s：截图 **33**（运行态）；12s：截图 **34** ——
  - 总数行显示 `命中 500 项 · 触发上限（最多 500 条）`（后端 limit=500 生效）；
  - 结果列表右侧有内部滚动条、`.hits` 为 `max-height:46vh; overflow:auto`，浮层整体仍完整落在窗口内（未撑满整屏）。✔

### 判据 7：弹层打开时点面板其他位置 → 立即收起 —— 通过

每种情况都是「先打开本地/远端「📍 位置」弹层 → 再点目标 → 截图确认弹层消失」：

| 目标 | 截图 | 观察 |
|---|---|---|
| 本面板文件行 | **36** | 弹层消失；app.go 行被选中（已选 1 项） |
| 列表空白 | **49** | 弹层消失（远端列表过滤到 2 行后点其下方空白）；未出现搜索浮层等副作用 |
| ☆ | **38** | 弹层消失；☆ 变为收藏态（黄色 ★） |
| 🔍 | **39** | 弹层消失；随即弹出深搜浮层（`在 /home/lan/workspace/scripts/sshkit 下递归搜索` + 空态提示） |

判定：四种点击都立即收起，弹层不会长期盖在列表上。✔

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

验收过程中曾观察到一次「本地路径栏显示值疑似与列表不一致」：截图 **42/45** 里路径栏文本看起来是 `/home/lan/`，而列表仍是仓库内容。受控复测（截图 **54**，含「点过滤框使其失焦」「点路径栏后按 Esc」两步）显示路径栏三次都正确回显真实目录（`/home/lan/workspace/scripts…`）。

当前证据无法排除是脚本注入点击造成的输入框聚焦/滚动窗口假象（`.curpath` 为 `flex:1`，宽度随右侧「已选 N 项」chip 变化，文本会被裁剪），**故不作为缺陷上报**，仅记录证据路径供控制者复核。若控制者要追，建议在真实鼠标操作下按「输入 → 失焦 → Esc」顺序复核。

## 5. 截图清单（全部在 /tmp/sshkit-t5/shots/，未提交进仓库）

| 文件 | 内容 |
|---|---|
| 05-xvfb-initial.png | :99 上应用启动后的初始界面（端口转发页） |
| 06-sftp-tab.png | 切到 SFTP 页（t5host / 本地 / 远程双栏） |
| 07-connected.png | 点「连接」后：远端 /home/lan，日志 sftp transport=gosftp |
| 08-item1a-selected.png | 判据1：选中 app.go（已选 1 项） |
| 09-item1b-up-single.png | 判据1：单击「..」→ 该行高亮、已选 0 项、未跳转 |
| 10-item2-up-dbl.png | 判据1：双击「..」→ 跳到 /home/lan/workspace/scripts（27 项） |
| 13-item3c-typed-clean.png | 判据2：路径栏输入 /no/such/dir-task5 |
| 14-item3d-rollback-clean.png | 判据2：回滚 + 日志面板红色 ERROR |
| 15/16/17-item4*.png | 判据3：远端 / → 输入 ~ → 跳 /home/lan |
| 19/20/21-item4*.png | 判据3：再回 / → 打开位置弹层 → 点主目录 → /home/lan |
| 26/27/28-item5*.png | 判据4：≤8+展开全部20 → 展开 → 滚到最后一行整行可见 |
| 29/30/31-item6*.png | 判据5：0.3s/1.5s/3.5s 三帧（AE=0，无自动搜索） |
| 32/33/34-item7*.png | 判据6：输入 * → 深搜 → 命中 500 项·触发上限 |
| 36/38/39/49-item8*.png | 判据7：点文件行 / ☆ / 🔍 / 列表空白 → 弹层收起 |
| 42/45/51/52/53/54 | §4 过程观察与受控复测 |
| 00/01/04/11-root 等 | 早期渲染/输入探测失败证据（说明为何需要 :99 + 关合成） |

## 6. 清理

验收用进程与临时数据在报告落盘后清理：sshd（pid 见 /tmp/sshkit-t5/sshd.pid）、应用（/tmp/sshkit-t5/app99.pid）、Xvfb :99（/tmp/sshkit-t5/xvfb.pid）、临时 HOME 与垫片。**未触碰**用户真实 ~/.ssh/config、用户数据、既有 exe/脚本/配置。日志证据保留在 /tmp/task5-make-ci.log、/tmp/task5-make-build.log、/tmp/sshkit-t5/app99.log、/tmp/sshkit-t5/sshd.log。
