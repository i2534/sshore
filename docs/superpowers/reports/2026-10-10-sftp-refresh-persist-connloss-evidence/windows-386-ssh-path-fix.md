# windows/386 上 `exec: "ssh": executable file not found in %PATH%` —— 根因与修复

**范围**：只修「OpenSSH 客户端（`ssh`/`sftp`）在哪里」这一件事。不碰刷新 / 持久化 / 自愈，不碰前端，不改任何既有参数列表，不引入新依赖。

**提交**：

| SHA | 内容 |
|---|---|
| `23df979` | `feat(osutil): 新增 OpenSSH 客户端路径解析器（Sysnative 优先于 System32）` |
| `cf9eb92` | `fix(exec): 真实 exec 层改用 OpenSSH 解析器，参数列表不动` ← 本修复 |
| `4914c6f` | `test(osutil): 真实 exec 层接线冒烟（ssh/sftp 生效、其它名字逐字透传）` |

---

## 1. 现象与根因

真机（同一台 `DESKTOP-ACCEPT`，AMD64）用 `windows/386` 构建点「连接」：

```
[ERROR] Error: exec: "ssh": executable file not found in %PATH%
```

客户机侧实测（证据见 `real/WINDOWS-C.md` §2 与 `real/c-win-ssh-not-found.jpg`）：

```
System32\OpenSSH\ssh.exe exists: True
SysWOW64\OpenSSH exists:         False
SysWOW64\OpenSSH\ssh.exe exists: False
PATH 含 OpenSSH:                 C:\Windows\System32\OpenSSH\
```

根因是 **WOW64 文件系统重定向**：386（32 位）进程做 PATH 解析时，`C:\Windows\System32`
被重定向成 `C:\Windows\SysWOW64`，而后者没有 `OpenSSH` 目录；真实的
`ssh.exe` / `sftp.exe` 只存在于 64 位的 `System32\OpenSSH` 下。
从 32 位进程触达 64 位 `System32` 的唯一入口是 **`Sysnative`** 别名。
`os/exec` 只按裸名 `ssh` 查 PATH，因此必然失败；`sftp`（batch 后端的批量通道）同理。

## 2. 解析顺序（`internal/osutil/execpath.go`）

`SSHExecutable()` / `SFTPExecutable()` 走同一实现 `resolveOpenSSH(name)`，顺序固定：

| # | 来源 | 说明 |
|---|---|---|
| 1 | `SSHORE_SSH_PATH` / `SSHORE_SFTP_PATH` 环境变量 | 覆盖一切；给人手动指定与测试注入用（不做存在性校验，是"我就要这个"） |
| 2 | `exec.LookPath(<name>)` | amd64 与 Linux 都在这里命中，行为与修复前逐字节相同 |
| 3 | **仅 Windows**：`%SystemRoot%\Sysnative\OpenSSH\<name>.exe` → `%SystemRoot%\System32\OpenSSH\<name>.exe` → `%ProgramFiles%\OpenSSH\<name>.exe` | 每个候选都要 `stat` 到普通文件（是目录则跳过）；`%SystemRoot%` 缺失时按系统默认 `C:\Windows` 兜底 |
| 4 | 兜底裸名 `<name>` | 等价于修复前的行为（`exec.Command` 自己再查一次 PATH），不更差、不 panic、不返回空串 |

**`Sysnative` 必须排在 `System32` 之前**：32 位进程里 `System32` 会被 WOW64 重定向到
没有 OpenSSH 的 `SysWOW64`，先试 `System32` 在 386 上等于白试；而 64 位（amd64）进程里
`Sysnative` 根本不存在，`stat` 失败后自然落到 `System32`——两层都不会被这条顺序伤到。
这条顺序就是本 bug 的回归判据。

非 Windows **只走 1→2→4**，第 3 步整段被 `goos == "windows"` 挡住；单测里用注入的
`stat` 直接 `t.Errorf` 断言"非 Windows 分支一次文件系统探测都不许发生"。

### 2.1 可测性 seam

`execpath.go` 顶部四个包级变量，注释标明**仅供测试注入**，生产代码从不修改：

```go
var (
	goos     = runtime.GOOS  // 仅供测试注入
	getenv   = os.Getenv     // 仅供测试注入
	lookPath = exec.LookPath // 仅供测试注入
	stat     = os.Stat       // 仅供测试注入
)
```

因此 `GOOS=windows` 的分支能在 Linux 上确定性覆盖，不依赖宿主是否装了 ssh、
也不要求宿主是 Windows（CI 的 linux job 与 go-windows job 都能跑）。

## 3. 逐文件接线（只改"真实 exec 的那一层"）

原则：**调用方继续传字面 `"ssh"` / `"sftp"`**，只让真正落 `exec.Command*` 的那一层
把名字换掉。这样注入假 runner / spawner / pipes 的既有测试看到的仍是裸名，
`internal/sftp` 那一批 `startSFTPPipes` 注入用例一个字都不用改。

| 生产 spawn 点（grep 全量确认） | 真实 exec 层 | 落点 |
|---|---|---|
| `internal/sftp/gosftp.go:199` `startSFTPPipes("ssh", …)`（SFTP 主通道，`= osutil.StartPipes`） | `osutil.StartPipes` | `internal/osutil/runner.go:241` |
| `internal/sftp/batch.go:90` `c.runner("sftp", …)` | `osutil.NewRunner`（`app.go:221`） | `internal/osutil/runner.go:30` |
| `internal/sftp/batch.go:152` / `:184` `c.runner("ssh", …)` | 同上 | 同上 |
| `internal/forward/ctrl.go:52` `BuildArgs` 的 `args[0]="ssh"`；`:241` `c.spawner.Start("ssh", …)` | `osutil.NewSpawner`（`app.go:218`）→ `realSpawner.Start` → `StartStream` | `internal/osutil/runner.go:117` |
| `internal/sshconn/sshconn.go:63` / `:81` / `:98` `r(ctx, "ssh", …)` | `osutil.NewCtxRunner`（`app.go:228`） | `internal/osutil/runner.go:46` |
| `internal/watch/inotify.go:92` `s.sp.StartStream("ssh", …)` | `osutil.NewStreamer`（`app.go:227`）→ `StartStream` | `internal/osutil/runner.go:117` |
| `internal/config/parser.go:84` `exec.CommandContext(ctx, "ssh", "-G", alias)` | 本包 `defaultSSHGRun`（"声明为变量便于测试注入"的那一层） | `internal/config/parser.go:90`，改用 `osutil.SSHExecutable()` |

改动共 **2 个既有文件 + 2 个新文件**：

```
internal/osutil/execpath.go        (新) 解析器
internal/osutil/execpath_test.go   (新) 单测 + 接线冒烟
internal/osutil/runner.go          (改) 4 处 exec 调用点换成 resolveExecName(name)
internal/config/parser.go          (改) 1 处 exec 调用点换成 osutil.SSHExecutable()
```

`resolveExecName` 只重写 `"ssh"` / `"sftp"` 两个名字，其余（`inotifywait`、
测试 helper 进程、更新脚本……）**原样返回**，所以 osutil 里那些用测试二进制当
替身进程的既有用例（`StartPipes(self, …)`、`NewCtxRunner()(ctx, self, …)`）行为不变。

**参数列表逐字未改**：`git show cf9eb92` 的 diff 里只出现命令名位置的替换，
`args...` / `args` 一律原样透传。例如

```diff
-		cmd := exec.Command(name, args...)
+		cmd := exec.Command(resolveExecName(name), args...)
-	out, err := exec.CommandContext(ctx, "ssh", "-G", alias).Output()
+	out, err := exec.CommandContext(ctx, osutil.SSHExecutable(), "-G", alias).Output()
```

## 4. RED → GREEN 原文

先写测试、再用"只有 1→2→4 步"的实现跑一遍（等价修复前的行为），拿到 RED；
然后补上第 3 步 Windows 回退，拿到 GREEN。命令：

```
GOCACHE=$PWD/.gocache go test ./internal/osutil/ -run 'TestResolve' -count=1
```

### RED（6 条红，全是 Windows 回退用例；EXIT=1）

```
--- FAIL: TestResolveSSHWindowsOnlySystem32 (0.00s)
    execpath_test.go:144: SSHExecutable() = "ssh", want "C:\\Windows/System32/OpenSSH/ssh.exe"
--- FAIL: TestResolveSSHWindowsSysnativeBeatsSystem32 (0.00s)
    execpath_test.go:155: SSHExecutable() = "ssh", want Sysnative "C:\\Windows/Sysnative/OpenSSH/ssh.exe"（Sysnative 必须排在 System32 之前）
--- FAIL: TestResolveSFTPWindowsSysnativeBeatsSystem32 (0.00s)
    execpath_test.go:165: SFTPExecutable() = "sftp", want Sysnative "C:\\Windows/Sysnative/OpenSSH/sftp.exe"
--- FAIL: TestResolveSSHWindowsSkipsDirectory (0.00s)
    execpath_test.go:184: SSHExecutable() = "ssh", want "C:\\Program Files/OpenSSH/ssh.exe"
--- FAIL: TestResolveSSHWindowsProgramFilesFallback (0.00s)
    execpath_test.go:193: SSHExecutable() = "ssh", want "C:\\Program Files/OpenSSH/ssh.exe"
--- FAIL: TestResolveSSHWindowsEmptySystemRoot (0.00s)
    execpath_test.go:213: SSHExecutable() = "ssh", want "C:\\Windows/Sysnative/OpenSSH/ssh.exe"
FAIL
FAIL	sshore/internal/osutil	0.005s
FAIL
```

（期望值里的 `/` 是 `filepath.Join` 在 **Linux 编译目标**下的分隔符：实现与测试用同一
拼法，故用例在 Linux 与 Windows 上都成立；Windows 同时接受 `/` 与 `\`。）

### GREEN（全部 16 条；EXIT=0）

```
--- PASS: TestResolveSSHEnvOverrideWins (0.00s)
--- PASS: TestResolveSFTPEnvOverrideWins (0.00s)
--- PASS: TestResolveSFTPEnvOverrideWithoutLookPath (0.00s)
--- PASS: TestResolveSSHUnixLookPathHit (0.00s)
--- PASS: TestResolveSFTPUnixLookPathHit (0.00s)
--- PASS: TestResolveSSHUnixLookPathMissFallsBackToBareName (0.00s)
--- PASS: TestResolveSSHWindowsLookPathHitWins (0.00s)
--- PASS: TestResolveSSHWindowsOnlySystem32 (0.00s)
--- PASS: TestResolveSSHWindowsSysnativeBeatsSystem32 (0.00s)
--- PASS: TestResolveSFTPWindowsSysnativeBeatsSystem32 (0.00s)
--- PASS: TestResolveSSHWindowsSkipsDirectory (0.00s)
--- PASS: TestResolveSSHWindowsProgramFilesFallback (0.00s)
--- PASS: TestResolveSSHWindowsNothingFoundFallsBackToBareName (0.00s)
--- PASS: TestResolveSSHWindowsEmptySystemRoot (0.00s)
--- PASS: TestResolveExecNameLeavesOthersUntouched (0.00s)
--- PASS: TestResolveExecNameRewritesOpenSSH (0.00s)
PASS
ok  	sshore/internal/osutil	0.005s
```

用例与题目要求的对应关系：

| 题目要求 | 用例 |
|---|---|
| 环境变量覆盖优先 | `TestResolveSSHEnvOverrideWins` / `TestResolveSFTPEnvOverrideWins` / `TestResolveSFTPEnvOverrideWithoutLookPath`（LookPath 失败也照样覆盖） |
| 非 Windows：LookPath 命中就用它 / 未命中回退裸名（seam 注入，不依赖本机 ssh） | `TestResolveSSHUnixLookPathHit` / `TestResolveSFTPUnixLookPathHit` / `TestResolveSSHUnixLookPathMissFallsBackToBareName` |
| `GOOS=windows`：只有 `System32\OpenSSH\<name>.exe` 存在 ⇒ 选中它 | `TestResolveSSHWindowsOnlySystem32` |
| `GOOS=windows`：两者都存在 ⇒ **必须选 Sysnative**（本 bug 回归判据） | `TestResolveSSHWindowsSysnativeBeatsSystem32` / `TestResolveSFTPWindowsSysnativeBeatsSystem32` |
| `GOOS=windows`：都不存在 ⇒ 回退裸名，不 panic、不返回空串 | `TestResolveSSHWindowsNothingFoundFallsBackToBareName` |
| 额外：目录不当可执行文件 / ProgramFiles 兜底 / SystemRoot 缺失 / 非 ssh 名字透传 | `TestResolveSSHWindowsSkipsDirectory` / `TestResolveSSHWindowsProgramFilesFallback` / `TestResolveSSHWindowsEmptySystemRoot` / `TestResolveExecNameLeavesOthersUntouched` |

## 5. 为什么 amd64 / Linux 行为没变

1. **顺序保证**：非 Windows 在第 2 步之后直接返回（第 3 步被 `goos == "windows"` 挡住），
   只有 1→2→4 与修复前一致；单测用会 `t.Errorf` 的 `stat` seam 断言非 Windows
   不发生任何文件系统探测（`TestResolveSSHUnixLookPathHit` / `…MissFallsBackToBareName`）。
2. **amd64**：`LookPath("ssh")` 在 64 位 Windows 上成功返回 `System32\OpenSSH\ssh.exe`，
   第 3 步根本不进入 —— `TestResolveSSHWindowsLookPathHitWins` 钉住"LookPath 命中即用、
   即使回退候选存在也不改选"。真机上 386 之所以走第 3 步，正是因为 LookPath 被
   WOW64 重定向后查不到。
3. **名字透传**：`resolveExecName` 对非 `ssh`/`sftp` 的名字原样返回，
   `TestResolveExecNameLeavesOthersUntouched` +
   `TestRealExecLayerPassesThroughOtherNames`（名字与 argv 逐字到达子进程，退出码 7）钉住。
4. **LookPath 命中时的等价性**：`exec.Command` 对无分隔符的名字本来就内部
   `LookPath`；把结果显式传进去与其内部行为等价，找不到时依旧回退裸名、错误信息不变。
5. **既有测试**：`make ci`（见 §6）全绿，其中 `internal/osutil`、`internal/config`、
   `internal/sftp`、`internal/forward`、`internal/sshconn`、`internal/watch`、
   `internal/sync` 全部通过 —— 这些包正是本次接线涉及的包。

## 6. 验证汇总（实际命令与结果）

```
$ GOOS=windows GOARCH=386  go build ./internal/...   # EXIT=0
$ GOOS=windows GOARCH=amd64 go build ./internal/...  # EXIT=0
$ GOOS=linux   GOARCH=amd64 go build ./internal/...  # EXIT=0
$ GOOS=darwin  GOARCH=arm64 go build ./internal/...  # EXIT=0

$ GOCACHE=$PWD/.gocache go test ./internal/osutil/ ./internal/config/ ./internal/sftp/ \
    ./internal/forward/ ./internal/sshconn/ ./internal/watch/ ./internal/sync/ -count=1
ok  	sshore/internal/osutil	0.343s
ok  	sshore/internal/config	0.194s
ok  	sshore/internal/sftp	18.198s
ok  	sshore/internal/forward	0.866s
ok  	sshore/internal/sshconn	0.005s
ok  	sshore/internal/watch	5.802s
ok  	sshore/internal/sync	15.233s
EXIT=0

$ GOCACHE=$PWD/.gocache go test ./internal/osutil/ ./internal/config/ -race -count=1
ok  	sshore/internal/osutil	6.445s
ok  	sshore/internal/config	1.360s
EXIT=0
```

接线冒烟（真实 exec 层，不用 seam）：把 `SSHORE_SSH_PATH` / `SSHORE_SFTP_PATH` 指向
测试二进制（helper 模式），确认 `NewRunner` / `NewCtxRunner` / `NewSpawner` /
`StartPipes` 四个真实 exec 层都会把它当成 `ssh`/`sftp` 起起来 ⇒ 名字替换确实发生在
spawn 之前：

```
--- PASS: TestRealExecLayerUsesResolvedSSHPath (4.06s)
--- PASS: TestRealExecLayerUsesResolvedSFTPPath (1.01s)
--- PASS: TestRealExecLayerPassesThroughOtherNames (0.01s)
```

收尾门禁（`GOCACHE=$PWD/.gocache make ci`，**EXIT=0**）：

```
cd frontend && npm run build     # vite build ✓ built in 1.20s
go vet ./...                     # 无输出（干净）
go test ./... -race -count=1
ok  	sshore	1.861s
ok  	sshore/internal/config	1.350s
ok  	sshore/internal/forward	1.883s
ok  	sshore/internal/importer	1.017s
ok  	sshore/internal/localfs	1.024s
ok  	sshore/internal/namepattern	1.016s
ok  	sshore/internal/osutil	11.527s
ok  	sshore/internal/preset	1.024s
ok  	sshore/internal/sftp	19.603s
ok  	sshore/internal/sshconn	1.018s
ok  	sshore/internal/sync	16.276s
ok  	sshore/internal/update	14.817s
ok  	sshore/internal/watch	6.825s
cd frontend && npx vitest run    # Test Files 38 passed (38) / Tests 489 passed (489)
EXIT=0
```

完整日志：`make-ci-windows-386-ssh-path-fix.log`（本目录，本轮新增；早前两轮的
`make-ci.log` / `make-ci-final.log` 未改动）。这是"既有测试仍全绿"的直接依据。

## 7. 未做 / 已知边界

- **没有在真 Windows 上复跑 386 二进制**：本机只有 i686 mingw，能出 `windows/386`，
  但要跑起来仍需把 exe 投放进 Windows 客户机、装好 WebView2 并点「连接」。
  本次给出的证据是"解析顺序正确 + `GOOS=windows/386` 交叉构建通过 + 二进制里
  确实走到 `resolveExecName`"，**真机复验仍未做**。等价的静态判断：
  386 进程 `stat(%SystemRoot%\Sysnative\OpenSSH\ssh.exe)` 必然成功（客户机实测
  `System32\OpenSSH\ssh.exe` 存在，且 `Sysnative` 是 WOW64 对 32 位进程的别名）。
- **`e2e/winprobe/main.go` 未改**：它是**独立 Go module**（`e2e/winprobe/go.mod`，
  `module winprobe`，无 `replace`），按设计"不依赖 sshore 本身"，因此**无法** import
  `sshore/internal/osutil`。要一并收益，只能给它加 `replace` 把 sshore 拉进来、或者
  在那边复制一份解析器 —— 两者都超出本次范围，故有意不动。它本身是 386 目标时同样会
  踩 WOW64，但它是诊断工具、不是发布产物。
- `%ProgramFiles%` 在 32 位进程下会被 WOW64 重定向到 `Program Files (x86)`；本实现按
  题目要求的字面语义用 `getenv("ProgramFiles")`，没有额外引入 `ProgramW6432`。
  真正的修法是第 3 步的 `Sysnative`，`ProgramFiles` 只是多一层兜底。

## 复核轮补记（已知可见差异）

- **`argv[0]` 变为绝对路径**（非 Windows 亦然）：`exec.Command` 对无分隔符名字本就会内部 LookPath 并更新 `Path`，因此**可执行文件路径等价**；但 `Args[0]` 从 `"ssh"` 变成 `/usr/bin/ssh`（复核者用 `-overlay` 探针实测：子进程 `$0` 由 `sh` 变 `/bin/sh`）。对 ssh/sftp 无功能影响（basename 不变），但 `ps` 的 argv 显示、以及按 `$0=="ssh"` 判断的 wrapper 会看到差异 —— 原先"行为等价"的表述应限定为 **Path 等价**。
- **386 下 `%ProgramFiles%` 兜底近似无效**：32 位进程读 `%ProgramFiles%` 会被重定向到 `Program Files (x86)`，而 OpenSSH 装在 64 位目录；本 bug 的修法是 **Sysnative 优先**，故不影响；该兜底仅为兼容非常规安装位置。


## 真机复验（已完成，2026-10-10）

用修复后的 386 二进制（`Version=v0.7.2-77-gc38f5c2`）在 VBox `win10`（`DESKTOP-ACCEPT`，AMD64）上经 RDP 真实输入通道复跑：

| 步骤 | 结果 |
|---|---|
| 点「连接」（主机 `winlocal`） | **不再出现** `exec: "ssh": executable file not found in %PATH%` |
| 远程面板 | 列出 `/C:/Users/lan`：`「开始」菜单`、`3D Objects` 等，共 **85 项** ⇒ SFTP 通道真的建立了 |
| 连带验证 | 已连接态下空白菜单三项全部深色启用；点 `上传文件…` 弹出**原生 Windows 文件选择器**（`选择要上传的文件`） |

证据：`real/c-win-386-ssh-fixed-connected.jpg`、`real/c-win-native-filedialog.jpg`（结论同时回写到 `real/WINDOWS-C.md` 的 C-6 与 §2）。

收尾：客户机 app 已 kill、RDP 会话已断开并把会话 1 接回控制台、我临时挂的 `rdpalt` 转发已删除、VM 关机、锁释放、宿主 `xfreerdp`/`Xvfb` 归零。


## 事实更正（2026-10-10 晚）

当初「本机只有 i686 mingw ⇒ 只能构建 386」的判断**不成立**：实测
`GOCACHE=$PWD/.gocache wails build -platform windows/amd64 -skipbindings -nopackage -trimpath`
在本机 **9.2 秒成功**，产物经 `file` 判定为 `PE32+ executable (GUI) x86-64, for MS Windows` —— **Wails v2 的 Windows 目标不需要 C 工具链**（新版 Go WebView2Loader 为纯 Go）。

因此：
- 本问题**只影响 `windows/386` 目标**；仓库发布目标 `windows/amd64` 从一开始就没这个毛病。
- 本机无 x86_64 mingw、也无 clang/zig 都无所谓；若将来确实需要 mingw（例如别的 cgo 依赖），`sudo apt install gcc-mingw-w64-x86-64` 即可（当前 apt 候选 10.3.0-14ubuntu1+24.3；sudo 需要密码）。
- 修复仍然保留：它让 386 目标可用，且解析顺序对 amd64 无副作用（amd64 走 `LookPath` 命中 System32 下的 ssh）。
