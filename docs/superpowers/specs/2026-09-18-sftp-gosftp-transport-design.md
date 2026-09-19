# SFTP 传输底座换代（Go SFTP over OpenSSH 子系统）设计

> 状态：v6 · 用户已确认 §12.2 的 a–g（2026-09-18）· 已合入四份独立评审（架构正确性 / 事实核查 + 一致性覆盖度 / 可执行性）+ 一轮自审；两阶段审核已完成
> 关联：`docs/superpowers/specs/2026-08-25-sshkit-design.md`（传输定位：v1 只做文件级状态、字节级进度被列为 v1.1 增强）
> 关联：`docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md` §14（P3「流式传输底座」）
> 本期范围：**能力②** = 底座换代 + 字节级进度 + 中途取消（整批）+ 手动重试 + **下载与上传双向续传** + 原子落盘
> 证据来源：§2.2 的 spike 实测（复现步骤见 §14）与 §2.3 的 API 事实（两轮评审各自独立核对过源码）

---

## 1. 背景

### 1.1 现状（代码事实）

SFTP 的每一次操作都是**一个一次性 `sftp -b` 进程**：

| 位置 | 事实 |
|---|---|
| `internal/sftp/ctrl.go:56-87` | `c.runner("sftp", args...)`，参数带 `-b <batchfile>`；**`ctrl.go:69-71` 现用 `BatchMode=yes / IdentitiesOnly=yes / ConnectTimeout=10`**；Linux/macOS 追加 ControlMaster 复用（`ctrl.go:74-81`），**Windows 不加**（`isWindows`，无 `-f`/ControlMaster） |
| `internal/osutil/runner.go:24-34` | `exec.Command` + `bytes.Buffer` 收 stdout/stderr + `cmd.Run()`，**阻塞到底、非流式** |
| `internal/sftp/ctrl.go:216-292` | 手工拼批处理命令（`ls -la` / `get` / `get -r` / `put` / `put -r` / `rm` / `mkdir` / `rename`；**`rmdir` 不在 `buildBatch`**，在 `removeBatch`（`ctrl.go:665`）—— 三审 F4），逐参数引号转义 + 控制字符拒绝 |
| `internal/sftp/parser.go:23-56` | 解析 `ls -la` 文本（月份映射、MMM DD HH:MM 或 YYYY 的时间字段） |
| `internal/sftp/listmany_parse.go` | 按 `sftp> ` 回显行切块归属结果（parseListMany:22-50）；空块是「失败」还是「空目录」要靠 stderr 原文区分（Windows OpenSSH 成功列表不含 `.`/`..`） |
| `internal/sftp/ctrl.go:647-688` | 删除批量用 `-` 前缀抑制中止（注释 647，函数体 649-688），退出码无法区分「全成」与「部分失败」，只能**按 stderr 原文反查失败路径** |
| `internal/sshconn/sshconn.go:38-99` | ControlMaster socket 判活/建立（38-76）、`ssh` 一次性远端命令（85-99） |
| `internal/sync/transfer.go:20-26,28-54,56-76` | 本地同步引擎的原子落盘先例：写 `.sshore-part-<ruleid8>-<rand>` 再 rename；`CleanupParts` 用 `strings.Contains` 匹配该中缀 |

### 1.2 现状付出的代价

1. **没有字节级进度**：`sftp -b` 批处理模式不打印进度表（实测：stdio 接管道 stderr 0 字节；即使 stderr 接 pty 也只有 `sftp> get …` 回显，无百分比）。前端 `TransferQueue` 只有「处理中/完成/失败 + 耗时」，`size` 恒为 0（`SftpView.vue:358-372`、`TransferQueue.vue:19-26`）。
2. **不能取消**：没有可取消的传输句柄（只验过 kill 进程的可行性，无实现）。
3. **Windows 上每次操作一次全握手**：实测一次不复用的 `sftp -b` 探测 ≈250ms（本机 localhost）；Linux 走 ControlMaster ≈69ms/次。
4. **半截文件落在最终文件名上**：OpenSSH `get` 直接写目标路径，中断后目标名上出现「看起来完整」的截断文件。
5. **脆弱文本层**：列目录正确性依赖回显分块、`ls -la` 格式、stderr 原文匹配。
6. **依赖 `sftp` 二进制**（`README.md:22`），而 `ssh` 本来就是必需的。

### 1.3 先考虑过、但未采纳的路

- **pty + 解析回显**（沿用 `sftp` 二进制）：交互式 sftp 能出进度表，但要在 Go 里引入伪终端；**Windows 的 ConPTY 支持是主要风险点**，且仍是「解析人类可读输出」。
- **轮询落盘文件大小**：下载方向可行（本地 `os.Stat`），但上传方向的分子在远端，Windows 无连接复用 → 每秒一次全握手；目录传输没有现成分母。
- **纯 Go SSH**（`x/crypto/ssh` + `pkg/sftp`，完全不依赖系统 ssh）：见 §2.4，成本高一个数量级。

---

## 2. 结论与实测证据

### 2.1 决定：B2

> **传输层换成 `github.com/pkg/sftp`；连接层仍由系统 `ssh` 提供** —— 用 `ssh … host -s sftp` 起 sftp 子系统，把它的 stdin/stdout 交给 `sftp.NewClientPipe(rd, wr)`。

`~/.ssh/config`、agent（Linux `SSH_AUTH_SOCK` / Windows Pageant）、`known_hosts` 校验、ProxyJump/ProxyCommand、证书与密码策略**全部零漂移**（仍由 OpenSSH 实现），而传输、列举、删除、递归改由库直接驱动协议。

### 2.2 spike 实测（本机 OpenSSH 8.9p1，localhost，320MB；复现见 §14）

137 行一次性 spike（在 `/tmp` 构建、**未入库**，跑完已删除；两轮评审均无法复现，故 §14 给出复现步骤）：

```
client up in 216ms                                        ← 一个 ssh -s 会话，之后长期复用
stat remote size=335544320
  [download]  33.7% → 69.8% → 100.0%   320MB in 857ms      ← 计数 writer 的真实进度
  [upload]     7.0% → … → 100.0%       320MB in 4.771s     ← 计数 reader，17 个 tick
readdir . -> 122 entries                                  ← 列目录不再解析 ls 文本
  [cancel] closing client mid-transfer
cancelled transfer: bytes=95813632 err=... elapsed=305ms   ← 取消 305ms 内生效
```

其他实测结论与**边界**：

- `ssh -s host sftp` 在裸连与 ControlMaster 复用两种情况下都可用（退出码 0，无 `subsystem request failed`）。评审 B 独立复核：`-s` 放在主机名之后依赖 getopt 置换，故**阶段 1 要同时验前置与后置两种写法**（Win32 是否置换未验证）。
- **取消 = 关会话**：`io.Copy` 立刻返回错误，已写字节保留在目标文件（95MB）——D15 之后会落在 `.part` 上。
- **本 spike 没有覆盖取消后的可观察状态**（目标名是否存在、`.part` 是否为前缀、字节是否 flush）→ 这些转为 §9 的强制 e2e 断言（评审 A 的意见）。

### 2.3 关键 API 事实（两轮评审各自核对源码后确认）

| 事实 | 出处 |
|---|---|
| `func NewClientPipe(rd io.Reader, wr io.WriteCloser, opts ...ClientOption) (*Client, error)` | `pkg/sftp` client.go:226 |
| 可用面：`ReadDir` / `ReadDirContext` / `Stat` / `Lstat` / `Walk` / `Open` / `OpenFile` / `Create` / `Remove` / `RemoveDirectory` / `Mkdir` / `MkdirAll` / `Rename` / `PosixRename` / `RealPath` / `Getwd` / `StatVFS` / `HasExtension` | 同上；行号：ReadDir 371、ReadDirContext 379、Create 303-305、HasExtension 359、OpenFile 664、PosixRename 912 |
| **只有 `ReadDirContext` 是 ctx 感知的**；`File.Read/Write` 无 ctx（其余走 `sendPacket(context.Background())`）→ **文件传输无逐请求取消**，取消只能关会话 | client.go:379 等 |
| `Client.Rename` 发 plain `SSH_FXP_RENAME`：OpenSSH sftp-server 走 `link()`，**目标已存在必失败**（sftp-server.c:1278-1307）；而现状 `sftp rename` 实测**覆盖**已存在目标 → 迁移必须用 `PosixRename`（评审 B 本机 sshd 实测） | sftp-server.c；client.go:912 |
| `PosixRename` 在 Win32-OpenSSH 上**真覆盖**已存在目标（Task 0：返回 nil + 读回新内容 + statSize 旁证，6 次观测；**无独立失败观测**，零次构造「扩展缺失」对照）→ 因此**必须保留 backup-swap 回退**（见全局约束） | probe.md §6/§7 |
| `Client.Create` = `O_RDWR\|O_CREATE\|O_TRUNC`（部分服务器不支持读写同开）→ 上传用 `OpenFile(O_WRONLY\|O_CREATE\|O_TRUNC)` | client.go:303-305 |
| `File.Write` 走 `f.writeAt(b, f.offset)`（client.go:1621-1631），`File.Seek` 就是改 `f.offset`（client.go:2098+），`writeChunkAt` 把 offset 放进 `SSH_FXP_WRITE` 包（client.go:1634）→ **上传续传用普通 `O_WRONLY` 句柄 + `Seek(partSize)` 即可**（协议级显式 offset、跨服务端），不必依赖 `O_APPEND` 的「服务端忽略 offset」行为 | 本机核对 client.go |
| `File.WriteTo` = 并发读 + **顺序写**（Reduce 阶段串行 `w.Write`）→ 中断文件是前缀；上传默认不并发写 | client.go:1425-1586 |
| OpenSSH `sftp-server` 的 readdir 用 `lstat` → 不跟随目录符号链接 | sftp-server.c:1151（评审 B 核实） |
| 最新版 v1.13.11；**BSD-2** 许可；传递依赖 `github.com/kr/fs v0.1.0` | proxy 与 LICENSE |
| Go→JS 事件走 `ExecJS` → `mainWindow.Invoke`，**不是丢帧**：Wails v2.15.0 的 `ControlBase.Invoke` 是**无界 `dispatchq` + `PostMessage` + 整队 drain**（`windows/winc/controlbase.go:427-436, 557-563`）→ 节流是为了**往返成本与 UI 抖动**（320MB 下载内部 `w.Write` 约 5000 次），末帧是保证 UI 归位（三审 F3 的更正） |

> 行号口径：`pkg/sftp` 行号取自 v1.13.11；`sftp-server.c` 行号取自 OpenSSH 9.0p1（本机 8.9p1 有几十行偏移，语义一致）。

### 2.4 不选纯 Go SSH（B1）的理由

要自己实现 OpenSSH 的连接与鉴权语义：`~/.ssh/config`（HostName/User/Port/IdentityFile/ProxyCommand/ProxyJump/CertificateFile/Match/Include）、agent（Linux `SSH_AUTH_SOCK`；Windows Pageant/named pipe）、`known_hosts` 校验策略、密码/keyboard-interactive、MFA。仓库现有的 `kevinburke/ssh_config` 只用于**列别名**（`internal/config/parser.go:84` 的 `ssh -G`）。风险是「某些用户本来能连变成连不上」，而收益 B2 全都能拿到。

---

## 3. 目标与非目标

### 3.1 本期目标（能力②）

1. 传输底座换代（B2），**行为等价**：现有全部 SFTP 功能不回归（含 `Rename/Move` 的「覆盖已存在目标」语义，见 D6）。
2. **字节级进度**：单文件与目录传输都是「真实字节 / 总量」，含速度与 ETA，且**末帧必达**。
3. **中途取消**：**整批语义**（取消当前项 + 停止后续项）。
4. **手动重试** + **下载与上传双向续传**（仅同一次运行内，D10）。
5. **原子落盘**：临时名 + 原子改名，最终文件名不出现半截文件（D15）。
6. 运行期可回退：配置项 `[app] sftp_transport` + 环境变量（D3）。

### 3.2 非目标（明确不做）

- 并发/并行传输（批量串行）、传输限速；
- 校验和比对 UI；
- **跨应用重启的续传**（本期续传只在同一次运行内有效，D10）；
- 自动重试；
- 引入 rsync 等新远端依赖；
- 批量总体进度条（D17）；
- **`internal/sync` 侧不享受新能力**（保持四参 legacy 语义与它自带的 `.part`+rename；D4）；
- 不改 `Item.ModTime` 的字符串格式（爆炸半径见 D5）；
- 拖拽/选择/位置预设等已交付能力（v0.6.0）不改语义。

---

## 4. 决策记录

### D1 连接归 OpenSSH，传输归库

- **决定**：`ssh -o BatchMode=yes -o IdentitiesOnly=yes -o ConnectTimeout=10 [-o User=…] -s host sftp` 起子系统（`-s` **前置**：不依赖 getopt 置换；Task 0 实测 Win32-OpenSSH 9.5p1 前后置都可用，前置是两平台都无歧义的写法）（与现状 `ctrl.go:69-71` 的选项对齐），`pkg/sftp.NewClientPipe` 建客户端。**不引入 `x/crypto/ssh` 直连**。
- **阶段 1 已实测（Task 0，Win32-OpenSSH 9.5p1）**：`-s` 前置与后置**都可用**（`[pre]/[post] client ok`），故采用前置写法；同一程序在 Linux OpenSSH 8.9p1 上后置亦可（早期 spike）。
- **Task 0 回写（环境限制，进 README 已知限制）**：Session 0（服务/非交互会话）里由 Go 进程 spawn 的 `ssh.exe` 会卡在 SFTP INIT 之后（≥15–25s 无响应，未观察到更长等待下的恢复；被测的 4 个 console 创建标志均无效，window station/desktop 未试）。改到交互桌面会话（Session 1）后全部正常 ⇒ **应用必须运行在交互桌面会话**，不得作为服务启动。
- **Task 0 回写（stderr）**：`pkg/sftp.NewClientPipe` 以 `stderr=nil` 调用 `newClientPipe`（client.go:227），库不读 `ssh` 的 stderr ⇒ 由 raw-pipe 原语后台 drain（drain 是**源码推断必要**的保守做法；「写满 64KB 会反压」本轮未实测，只观测到 ssh 确实会写 stderr）。
- **理由**：鉴权/配置/跳板机语义零漂移；实测建会话 216ms 后长期复用。
- **代价**：仍依赖系统 `ssh`（`README.md:22` 由 ssh+sftp 收窄为只需 ssh；仅 `batch` 回退需要 `sftp`）；远端必须启用 sftp 子系统（被禁用/被替换实现的场景要在错误文案里说清）；必须在交互会话运行（见上）。

### D2 会话模型：池化 + 传输独占 + 显式状态机

- **状态**：`idle`（可被复用）/ `busy`（被一个传输独占）/ `closing`（已发起关闭，等子进程退出）/ `dead`。
- **分配策略**：传输独占一个会话；列表/变更类复用 `idle` 会话。
- **并发与容量（自审简化：从构造上消除评审 A 的 S8 死锁类）**：**传输并发上限 1**（FIFO 排队；批量本身串行）；每个传输独占**新建**会话。列表/变更类**优先复用 idle 会话、池空则新建**（idle 池上限 2，超出关闭最久未用者）→ 列表/探活**永不排队、也不会因池满失败**；子进程软上限 = 1(传输) + 2(idle)。
- **探活**：只用 `ReadDirContext`（库唯一 ctx 感知 API，5s deadline）；需要家目录等其它探测时用「goroutine + 超时后走关会话路径」，不假设 `RealPath` 有 ctx。超时即 `close` 并丢弃该会话（不静默复用）。
- **取消**：`close(client)` → 关 stdin → **有界等待子进程退出（5s，定值）** → 超时 `Process.Kill`；槽在子进程确认退出后释放。
- **保活**：`-o ServerAliveInterval=30 -o ServerAliveCountMax=3`。
- **代价**：每个活跃传输 + 每个 idle 会话各一个 `ssh` 子进程；并发上限 1 + idle ≤2 ⇒ 常态 ≤3 个子进程。

### D3 开关：配置项 + 环境变量（懒解析），旧实现保留一个发布周期

- **优先级**：环境变量 `SSHORE_SFTP_TRANSPORT` > 配置项 `[app] sftp_transport` > 内置默认；非法值忽略并写日志后回退，**不阻断启动**。
- **接线（评审 A/B 的接线点问题）**：`NewCtrl(runner, emit)` 保持可用（按 env + 默认），**新增** `NewCtrlWith(runner, emit, selector func() string)`，由 `app.go:126` 注入「读取 `a.cfg.App.SftpTransport`」的**懒解析闭包**（不依赖 `Init`/`startup`/`OnShutdown` 的先后顺序；**`a.cfg == nil` 时必须回退内置默认**，不能 panic —— 三审 R11）。测试里直接构造的场景（`app_test.go:54`、`internal/sync/e2e_test.go:32`）按 env + 默认走。
- **配置落点三处（缺一即等于没配）**：`internal/config/store.go:17-24` 字段、`:28` `Normalize()`、`:172-183` `DefaultAppConfig()`（`AppConfig.normalize()` 在 `:156` 调它）。
- **必须同步前端（否则会被静默清空）**：`app.go:230-239` 的 `SetSettings` 是 `a.cfg.App = s` **整结构覆盖**，而 `frontend/src/stores/settings.js:94-102` 的 `save()` 只提交 6 个字段 → `sftp_transport` 不进 load/save 就会在用户每次保存设置时丢值。必做：重生成 `frontend/wailsjs/go/models.ts`（`AppSettings`，该文件已入库）并改 `settings.js` 的 load/save；`SettingsDialog` 露出下拉（§12.2 g，已确认）。
- **降级安全**：`store.go:193` 用 `toml.DecodeFile` 不查 Undecoded → 旧二进制忽略新键；新二进制读旧配置由 `Normalize` 填默认。
- **默认切换时机**：内置默认先 `batch`；**Task 16 已按计划切到 `gosftp`**（`internal/sftp/backend.go` 的 `defaultTransport = KindGo`，由 `TestDefaultTransportIsGo` 钉住）。Windows 真机验收清单已交付（`docs/windows-acceptance-checklist.md`），由人工执行；验收失败时一行回退为 `KindBatch`。
- **寿命**：batch 后端与开关保留一个发布周期，**v0.8 删除**（含 `parser.go`/`listmany_parse.go` 与批处理专用测试）。

### D4 包布局、门面双面与运行原语

```
internal/sftp/
  api.go        // Item / SearchOutcome / Progress / TransferRequest（新增）
  backend.go    // Backend 接口 + 后端选择（新增）
  ctrl.go       // 门面 Ctrl：legacy 四参面 + 新传输面 + 日志事件（保留 NewCtrl 签名）
  batch.go      // 现 ctrl.go 重命名 → BatchBackend（旧实现原样保留）
  gosftp.go     // GoBackend（新增）
  session.go    // 会话池状态机（新增）
  copy.go       // 树遍历 / 续传决策 / 进度聚合（纯函数优先）
  partname.go   // 临时文件前缀与命名（前后端常量各自单测钉住）
```

- **门面双面（评审 A 的 S3，编译级约束）**：`Ctrl` 保留四参 legacy 面（`List/ListMany/Home/Get/GetRecursive/Put/PutRecursive/Remove/RemoveRecursive/Mkdir/Rename/Connected/Disconnect/CloseAll`）供 `internal/sync`（`adapters.go:21`、`sync/ctrl.go:815`）与既有测试使用；**新增**传输面（带 `id`/进度/取消/resume）供绑定层使用。**两参 `Get(req)` 不与四参 `Get` 并存**。sync 路径不享受新能力（它自带 `.part`+rename 原子性）。
- **`NewCtrl` 的调用点**（三审 M11 修正）：**生产**只有 `app.go:126`；**测试**有 `internal/sync/e2e_test.go:32`、`app_test.go:54`，以及 `internal/sftp` 包内 `ctrl_test.go`/`listmany_test.go`/`search_test.go` **36 处**（`ctrl_test.go` 27 / `listmany_test.go` 4 / `search_test.go` 5 —— 三审 F1 的精确计数）。
- **新增 raw-pipe 运行原语（评审 A/B 的遗漏）**：现有 `internal/osutil` 的长驻原语 `StartStream` 用 `scanLines`（`runner.go:172-182`：`bufio.Scanner` + `TrimSpace`）**不能承载二进制 SFTP 协议**。需新增长驻原语，返回 `stdin/stdout/stderr` 三路裸管道 + `Wait()/Kill()/Signal()` 语义；**`stderr` 必须独立可读且默认由后台 goroutine drain 到有界缓冲**——若没人读，64KB 管道写满会让 `ssh` 假死（三审 R3），验收含「不读 stderr 也不阻塞」的测试。
- **构造函数更名（三审 R2 的硬阻塞）**：batch 实现构造改为 `NewBatchBackend(runner, emit) *BatchBackend`（`buildBatch`/`run` 留在它身上），`NewCtrl` 变为门面构造；包内 36 处测试只做**构造名机械替换**、方法调用不变，机械替换写进阶段 2 验收。
- **理由**：`ctrl_test.go`（650 行，断言批处理参数）继续测 `BatchBackend`，**本期不重写**。删除 batch 后端时其测试一并删除。

### D5 Item 形状与时间格式（爆炸半径比想象大）

- `Item{Name,Size,IsDir,Mode,ModTime}` 字段与语义不变。
- **`ModTime` 必须逐字保持 `2006-01-02 15:04`**：除 `FilePane.vue:57,161` 的展示与排序外，`watch/scan.go:140` 存 Meta、`watch/poll.go:160` 与 `sync/ctrl.go:683` 做**字符串相等**比较、`sync/ctrl.go:582/745` 还把它持久化为 RemoteMTime —— 格式一变就是全量误判「远端文件变了」→ 重传风暴（评审 B 的遗漏项）。
- 缺失/为 0 的 mtime → 空串（前端 `it.modTime || '—'`）+ 单测。
- `Mode`：Go `FileMode.String()` 对符号链接输出大写 `L`（本地 `app.go:643` 已如此），batch 解析是小写 `l`；UI 不用 Mode，保持字段填充即可。
- `parser.go` / `listmany_parse.go` 本期保持不动（batch 后端仍需要），batch 删除时一并删除。

### D6 门面语义等价清单（含参数基准与 rename 覆盖语义）

| 能力 | 必须保持的语义 |
|---|---|
| `List` / `ListMany` | **缺失的 key = 该目录未知，绝不是空目录**（`ctrl.go:334-336`）；`Search`（`search.go:71-75`）与 `watch/scan.go:112-119` 都依赖它 |
| 参数基准（评审 A 的 S2） | `Get/GetRecursive` 的 `local` = **目标文件 / 目标目录本身**；`Put/PutRecursive` 的 `remote` = 目标文件 / **目标父目录**（远端建 `<remoteDir>/<base(local)>`，同名已存在则**并入**，`ctrl.go:537-544` 是权威描述）。与前端 `planTasks` 传 `targetDir/name` 一致，e2e 固化 |
| `Home` | 远端家目录绝对路径（新实现用 `Getwd()`/`RealPath(".")`） |
| **`Rename` / `Move`** | **必须覆盖已存在目标**：实测现状 `sftp rename` 覆盖；而 `Client.Rename`（plain `SSH_FXP_RENAME`）在 OpenSSH 上目标存在必失败 → 新实现用 `Client.PosixRename`（client.go:912）；**扩展缺失时明确失败并提示**，不做「先删后改」的有损序列（评审 B 的实测发现） |
| `RemoveRecursive` | 拒绝根路径；目录不可读即整体失败；不静默漏删 |
| 通配符拒绝 | batch 时代防远端 glob 误删（`ctrl.go:596`）。库直连无 glob 展开，保留作纵深防御 |
| `Mkdir` / `Remove` | 语义不变 |
| `GetRecursive` / `PutRecursive` | OpenSSH 的合并语义（D11）；D15 的逐文件 `.part` 不改变它 |

### D7 进度来自真实字节 + 末帧必达

- 下载 = 目标文件计数 writer；上传 = 源文件计数 reader；`done` 是**已落盘/已发送的字节**。
- 节流 ≤5 次/秒（≥200ms）；**完成时必须强制发最后一帧**（理由不是丢帧 —— `Invoke` 是无界队列，见 §2.3；真实成本是每次事件一次 `PostMessage`→JS 回调，320MB 下载内部 `w.Write` 约 5000 次，不节流会堆积大量往返与 UI 抖动；三审 F3）。前端最终状态**以绑定返回值为准**，避免 UI 停在 99%。
- `total` 取不到时为 `-1`（不定进度），绝不显示 NaN 或假装 100%。

### D8 目录传输先枚举再传输（单批可中断）

- 两相：`phase="scan"` → `phase="transfer"`。
- 扫描相用 `ReadDirContext`（库唯一 ctx 感知 API）→ **单批即可中断**，而不是只能"批次之间检查"。
- 阈值与降级见 D16。

### D9 取消：整批语义

- 取消 = **当前项 + 停止该批量的后续项**（已完成项保留）；实现 = 关该传输独占的会话（D2 的有界退出流程）。
- **`.part` 保留**（续传锚点）；**不自动重试**（D10）；取消**幂等**，已结束的 id 返回 `false`。
- **e2e 必须断言**（评审 A 的 S6）：目标名**不存在**、`.part` 存在、`.part` 是源的**前缀**（下载方向做 sha256 前缀比对）、随后续传得到的最终文件 sha256 == 一次性完整传输。

### D10 重试与续传（下载 + 上传，仅同一次运行内）

- **`PartPath` 必须进传输表并随 `Progress` 回传**，`TransferRequest` 携带 `PartPath`；**禁止靠扫目录猜锚点**（否则 `a.txt` 与 `a.txt.bak` 的 `.part` 互为前缀、多次中断后无从选择 —— 评审 A 的 S1）。
- **下载续传**：本地 `.part` size ≤ 远端 size，且远端 size/mtime 与开始时记录一致 → `Seek(partSize)` 续写。
- **上传续传（校验口径）**：远端 `.part` size ≤ 本地源 size、本地源 size+mtime 与开始时记录一致、源路径未被替换（POSIX 可加 inode）→ 从 `partSize` 续传；**写入用不带 `O_TRUNC` 的 `OpenFile(O_WRONLY)` + `Seek(partSize)`**（协议级显式 offset，`SSH_FXP_WRITE` 包自带 Offset 字段 → 跨服务端成立）；**不使用 `O_APPEND`**（它依赖「服务端忽略 offset」这一 OpenSSH 实现细节）。
- **`part size == 记录 total` ⇒ 跳过数据阶段直接走提交（rename）**，避免"续传 0 字节"（评审 A 的次要 3）。
- **去重**：传输表按 `(host, direction, target)` 唯一；重复触发排队/拒绝；「重试」前等旧会话确认关闭，避免旧调用随后 rename 出旧内容（评审 A 的次要 8）。
- **续传粒度（三审 S4）**：仅**单文件项**支持续传（`PartPath` 是单值）；**目录项只支持「重试」**（重试前清理该子树内已知 `.part`），目录级「逐文件跳过/续传」列 P3.1（§12.3 a）。
- **前缀假设与兜底**：假定「远端 `.part` 是本地源前缀」（scp/rsync 同级假设）。**任一校验不过 → 删除 `.part`（本地/远端各按方向）整份重传**，并明示「未能续传，已重新开始」。
- **范围**：只在同一次运行内有效；跨重启续传 P3.1（需持久化源身份）。
- **必须被验证**：续传结果 sha256 == 一次性完整传输；源被改动后点续传 → 整份重传。

### D11 递归语义与 OpenSSH 一致

不借换代顺手改语义（不做整树替换、不清理远端独有文件、默认不保留 mode/mtime）；`.part` 是逐文件行为，不改变目录合并规则。

### D12 错误模型

- 统一 `TransferError{Op, Host, Path, RemoteMsg, Err}`；日志继续走 `forward.Event{SourceType:"sftp"}`。
- **用户可见文案保留远端原文**（`ctrl.go:407-420` 的 `commandErr` 就是这个用途）：ssh 握手失败/host key/权限/子系统缺失必须原样透出。
- `ssh -s` 的 **stderr 不在管道里**，必须单独读取并与管道错误合并。
- **能力缺失要说清**：远端无 `posix-rename@openssh.com`、或对 `O_WRONLY` 打开有特殊限制时，报错要指向具体能力而不是笼统失败。

### D13 SftpConnect / Disconnect / Connected 新语义

- `Connected` = 池内该 host 是否有活会话（探活，遵守 D2 的非阻塞与超时）；Windows 不再需要内存标记（`ctrl.go:190-199` 的 `active` map 随 batch 后端废弃）。
- `Disconnect` = 关闭该 host 的**空闲**会话并清理池；进行中的传输不被它打断（由取消处理）；**永不排队**。

### D14 生命周期与临时文件清理

- `CloseAll()`（`app.go:968 OnShutdown` → `:982`）：close client → close stdin → 有界 `Wait` → `Process.Kill`。**Windows 无进程组语义**（`internal/osutil/cancel_windows.go`），必须显式 Kill + 关管道（评审 B 的遗漏 11）。
- **优雅退出清理**：best-effort 删除所有已知 `.part`（本地与远端）—— 续传仅同一次运行内有效，退出后无保留价值（评审 B 的遗漏 5：远端也要清）。
- **崩溃遗留**：本地启动时清理 **> 7 天**且**名字含 `.sshore-sftppart-` 中缀**的临时文件（含 `bak`；匹配用 `strings.Contains`，与 sync 的 `CleanupParts` 同思路 —— 注意我们的名字是**中缀**不是前缀）；远端遗留无法自动发现，文档写明并建议重试/删除（§12.2 d，已确认）。
- 测试断言「传输/取消/退出后无残留 `ssh` 子进程」。

### D15 落盘原子性：临时名 + 原子改名

- **命名**：`<name>.sshore-sftppart-<id8>-<rand>`（同目录）。**与 sync 的 `.sshore-part-<ruleid8>-` 明确区分** —— 后者的清理靠 `strings.Contains`（`transfer.go:68`），同前缀会互相误删（评审 A 的次要 4）。
- **文件名长度（自审新增）**：临时名/备份名会让名字加长约 30 字符，接近文件系统 NAME_MAX（ext4/NTFS 均约 255）时会 `ENAMETOOLONG` → 超过阈值（**200 字节**，本轮定值）时退化为同目录短名 `.sshore-sftppart-<id8>-<rand>`；归属由传输表的 `PartPath` 决定，**不依赖名字里的原名**；备份名超限时同样退化为 `.sshore-sftppart-bak-<rand>`。
- **Windows 上的本地提交**：Go 的 `os.Rename` 在 Windows 走 `MoveFileEx(..., MOVEFILE_REPLACE_EXISTING)`（`internal/syscall/windows/syscall_windows.go:366`），且 sync 的 `internal/sync/transfer.go:49` 已长期依赖该覆盖语义 → 下载提交在 Windows 同样直接覆盖，无需 backup-swap。
- **Windows 上传提交（Task 0 回写）**：真机（Win32-OpenSSH 9.5p1 客户端）实测服务端**通告** `posix-rename@openssh.com`（证据弱：仅 `ext=true`/`value="1"`，6 次观测、无扩展缺失的对照），且 `PosixRename` **真的覆盖**已存在目标（证据强：返回 `<nil>` + 读回 `NEW2` + `statSize=4` + 磁盘 4 字节，6 次观测）⇒ Windows 常态走 `PosixRename` 原子提交，backup-swap 只是扩展缺失时的退路。plain `Rename` 到已存在目标按期望失败（`SSH_FX_FAILURE`，目标内容不变）。
- **`Seek(partSize)` 续写（Task 0 回写）**：真机实测有效 —— `Seek(3)+Write` 得 `ABCXY`；补边界 `Seek(5)`（恰好末尾）得 `ABCDEYZ`（7 B）、`Seek(7)`（越过 EOF）得 `ABCDE\x00\x00XY`（9 B，**空洞补 0**）。上传续传在 Windows 成立，无需退回「整份重传」；注意越界续写会以零填充空洞，因此续传偏移必须来自真实 part 大小而不是可推断值。
- **下载**：写同目录临时文件 → 本地 `os.Rename`（覆盖式）提交。
- **上传**：写远端同目录临时文件 → 提交用 `PosixRename`（原子覆盖）；**连接后探测 `HasExtension("posix-rename@openssh.com")`**（返回 `(string, bool)`，client.go:359 —— 三审 R12 的更正），不靠「试失败再回退」。
- **扩展缺失或提交失败 → backup-swap**：`Rename(target → target.sshore-sftppart-bak-<rand>)`（**复用同一中缀**，这样清理/面板过滤/内部忽略一套规则就覆盖它，避免孤儿文件） → `Rename(part → target)` → 删除 bak；任一步失败则**回滚 bak** 并报出明确错误。**绝不先删目标**（评审 A 的 S5/A-S4：先删会让目标消失，比现状更糟）。**journal（三审 R4）**：swap 前把 `(target, bak, part)` 三元组写入状态目录（与 sync 的 state 同处），成功/回滚后删除；若会话正好在两次 `Rename` 之间断开（目标名暂缺 + bak 存在），下次启动/下次连接时按 journal 恢复并校验，恢复失败必须报出 `bak` 路径而不是静默。
- **打开方式（两套 flags，勿混）**：新建上传 = `OpenFile(O_WRONLY|O_CREATE|O_TRUNC)`（不用 `Create` 的 `O_RDWR`）；**续传 = `OpenFile(O_WRONLY)`（绝不带 `O_TRUNC`）+ `Seek(partSize)`**。
- **提交前置条件**：`done == 开始时记录的 total`（单文件总有 Stat 得到的 total）；否则报错并保留 `.part` —— 防止源在传输中被截断时把半截文件改成「完整」（评审 A 的 S5）。
- **收益**：最终名不出半截文件；覆盖语义由「立刻截断」变为「成功后替换」；续传有锚点。
- **代价**：传输期间两份空间；退化路径下目标短暂改名（§8）。
- **递归目录**：逐文件同一套机制；目录本身按 D11 合并语义创建。

### D16 目录枚举：超阈值降级 + 字段闭环

- 文件数 > **20000** 或耗时 > **5s** ⇒ 放弃枚举，转 `Total=-1 && FilesTotal=-1 && Phase="transfer"`（§7 渲染 `filesTotal<0` 的"已完成 N 个文件"）。
- 「5s」的语义 = 停止发起下一批（单批由 `ReadDirContext` 的 deadline 中断）；**只放弃枚举、不关会话**（`ReadDirContext` 可被 ctx 取消，是库唯一 ctx 感知 API，与「取消传输要靠关会话」区分开 —— 三审 R9）；常量为具名常量便于调整。

### D17 进度 UI 范围

- 形态：进度条 + 百分比 + 速度 + ETA（内联 `TransferQueue`）；**不做批量总体进度条**，队列只显示当前项进度 + 成功/跳过/失败/进行中计数（`summarizeQueue` 已有）。
- 因此 §7 **不引入** `aggregateProgress` 之类的批级聚合函数（死代码，评审 A 的次要 1）。

### D18 内部临时文件治理（新）

- **面板隐藏**：前端的 item 级过滤必须用**中缀包含**（`name.includes('.sshore-sftppart-')`），**不能**依赖 `FilePane.vue:49` 现有的 `startsWith('.')` 隐藏规则 —— 我们的名字以**原名**开头（`<name>.sshore-sftppart-…`），不是点开头（三审 M1 的修正）。短名形态与 `bak` 由同一中缀覆盖。
- **sync/watch 内置忽略**：我们的临时前缀是**内部产物**，不能依赖各规则自己的 `excludes`；在 `sync.inScope`（`sync/ctrl.go:542`）与 `watch.MatchExclude`（`watch/scan.go:39`）路径加「内部临时文件」判定，并更新 `internal/config/store.go:103-104` 那句已过时的注释（「远端不会出现」）。
- **e2e**：传输中远端出现 `.part` 时，sync 不得把它当新文件下载（`ScanTree`/`align` 路径）。
- 前后端前缀常量各自单测钉住同一字面量。

---

## 5. 架构

```
前端 (Vue/Pinia)
  │  SftpGet(id,…) / SftpPut(id,…) / SftpTransferCancel(id)   ← Wails 绑定（阻塞调用 + 事件回推）
  ▼
app.go 绑定层（id 登记、进度事件转发、最近位置记录）
  ▼
internal/sftp 门面 Ctrl
  ├── legacy 四参面（List/ListMany/Home/Get/GetRecursive/…）→ sync 与既有测试（不享受新能力）
  └── 新传输面（id / 进度 / 取消 / resume / .part）
        ├── BatchBackend（旧：一次性 sftp -b；灰度期兜底，v0.8 删除）
        └── GoBackend
              ├── session.Pool  ssh -s sftp 长驻会话（状态机/保活/探活/重建；idle 上限 2 + 传输独占新建、并发 1）
              ├── osutil 新 raw-pipe 原语（二进制管道；StartStream 只行扫描，不能复用）
              ├── pkg/sftp.Client（协议）
              └── copy.go / partname.go（树遍历、进度、续传决策、临时命名）
  ▲
  └── sync（经 internal/sync/adapters.go 的 Transferer/ListMany，接口不变）
  其余 ssh 用法（forward 隧道、watch inotifywait、sshconn ControlMaster）不动
```

---

## 6. 接口契约

### 6.1 Go 侧

```go
type Progress struct {
    ID         string // 传输 id
    Host       string
    Direction  string // "download" | "upload"
    Name       string // 当前文件
    Done       int64  // 已传字节
    Total      int64  // 总字节；<0 未知
    FilesDone  int
    FilesTotal int    // <0 未知（含"枚举被放弃"）
    Phase      string // "scan" | "transfer"
    PartPath   string // 临时文件路径（本地或远端，按 direction）
}

type TransferRequest struct {
    ID, Host, User string
    Remote, Local  string
    Resume         bool   // 续传（双向，校验见 D10）
    PartPath       string // 续传时的精确锚点（不扫目录）
    Atomic         bool   // true=新面：我方 .part + 提交；false=legacy 面：直写目标，由 sync 自带 .part+rename 保证原子性（三审 R6）
}

// Backend：新传输面（legacy 四参面保留在门面 Ctrl 上，见 D4）
type Backend interface {
    List(host, user, path string) ([]Item, error)
    ListMany(host, user string, paths []string) (map[string][]Item, error)
    Home(host, user string) (string, error)
    Get(req TransferRequest, report func(Progress)) error
    GetTree(req TransferRequest, report func(Progress)) error
    Put(req TransferRequest, report func(Progress)) error
    PutTree(req TransferRequest, report func(Progress)) error
    Remove / RemoveRecursive / Mkdir / Rename(host, user, …) error
    Connected(host string) bool
    Disconnect(host string) error
    CloseAll()
}
```

**门面双面的方法名（Go 无重载，必须显式命名；三审 M8）**：legacy 面保持 `Get/GetRecursive/Put/PutRecursive`（sync 与既有测试用）；新传输面命名为 `TransferGet/TransferGetTree/TransferPut/TransferPutTree(req, report)` 与 `Cancel(id) bool`（绑定层与前端用）。整批语义由前端编排层落实（取消当前项 + 不再派发后续项）。`Rename` 在 GoBackend 内部使用 `PosixRename`（D6）。

### 6.2 绑定层变更

| 绑定 / 文件 | 变更 |
|---|---|
| `SftpGet / SftpGetDir / SftpPut / SftpPutRecursive` | 首参加 `id`；新增 `resume`/`partPath`。**生产调用方只有前端**；测试里 `app_test.go` 直接调 `a.SftpGet/a.SftpPut`（8 处）与直接构造 `sftp.NewCtrl`（`app_test.go:54`），签名变更要一并改（评审 B 的事实更正 2） |
| `SftpTransferCancel(id string) bool` | 新增 |
| `sftp:transfer-progress` | 新增事件（`runtime.EventsEmit`），末帧必达（D7） |
| `internal/config/store.go` | 字段 + `Normalize` + `DefaultAppConfig` 三处（D3） |
| `frontend/src/stores/settings.js` | load/save 必须带上 `sftp_transport`（否则 `SetSettings` 整结构覆盖会清空它） |
| `frontend/wailsjs/go/models.ts`、`.../main/App.js` | 重新生成（已入库，须一并提交；`make` 的 build 目标带 `-skipbindings`，所以要显式跑 `wails generate module`） |
| 其余绑定 | 不变 |

**工具链提醒**：新增绑定后必须跑 `wails generate module`，否则 `npm run build` 会在 Rollup 阶段报「SftpTransferCancel is not exported by wailsjs/go/main/App.js」—— v0.6.0 已踩过一次。验收必须包含 `npm run build`。

### 6.3 事件与订阅生命周期

- 进度事件形如 `{id, host, direction, name, done, total, filesDone, filesTotal, phase, partPath}`。
- 前端**必须**保存 `EventsOn` 的退订函数并在 `onDeactivated` 退订：实现先例在 `frontend/src/views/SftpView.vue:695/704`（KeepAlive），`App.vue:22-24` 是「不退订会叠加注册」的教训（评审 B 的行号更正）。
- 深搜进度 `sftp:search-progress`（`app.go:758`）保持不变。

---

## 7. 前端设计

- `TransferQueue.vue`：进度条 + `done/total` + 速度 + ETA；`phase="scan"` 显示「准备中…」；`total<0` 显示不定进度条；`filesTotal<0` 显示「已完成 N 个文件」（不显示百分比）；处理中项给「取消」（= **取消整批**），失败/取消项给「重试」「续传」「清理」（「清理」删该传输的 `.part`，见 §8/D14）。
- **末帧与终态**：以绑定调用返回值为准（`rec.status='完成'`），事件最后一帧仅用于把进度条推到 100%（D7）。
- `utils/queue.js` 新增纯函数 + vitest：`percentOf(t)`（clamp 0..100；**`total<0` 才返回 `null`**；`total==0 && done==0` 视为 100%，与 §8「0 字节文件完成即 100%」一致 —— 三审 S5）、`speedOf(t, now)`、`etaOf(t, now)`；用例含 0 字节、`total` 未知、超 100%、乱序事件回退。**不引入**批级聚合函数（D17）。
- `SftpView.runBatch`（`:335`）：每项生成稳定 id（`t<seq>-<n>`），事件按 id 落到 `rec` 并保存 `partPath`；取消 = `SftpTransferCancel(rec.id)` + 停止后续项；失败/取消项保留 id/`partPath` 供重试/续传。
- **隐藏内部临时文件**：两个面板过滤 `.sshore-sftppart-*`（D18）。
- 本地 copy/move 队列项不涉及远端进度，保持现状。

---

## 8. 边界情况

| 场景 | 行为 |
|---|---|
| 0 字节文件 | 完成即 100%，不显示速度/ETA |
| 覆盖写已存在的更大目标 | 目标在提交前不动；进度从 0 开始；临时文件与目标并存需额外空间 |
| 退化改名路径（无 posix-rename） | backup-swap：目标**短暂改名**为 `*.sshore-sftppart-bak-*`，提交后删除；失败则回滚并报错。**正常路径目标不会消失**；若会话在两次 `Rename` 之间断开，按 journal 恢复（D15/R4），恢复不了必须提示 bak 路径 |
| 同一目录的 `rename/move` 目标已存在 | 用 `PosixRename` 覆盖（保持现状语义）；扩展缺失 → 明确失败并提示先删除目标。**与上传提交的 backup-swap 不矛盾**：用户主动改名的目标可能是他不想被替换的内容，做有损序列风险高（明确失败最安全）；而**我们自己的临时文件提交**必须收敛（否则已传字节白费），所以用可回滚的 backup-swap（三审 M10） |
| 失败/取消（**单文件项**） | 保留 `.part`；最终名不出现半截文件；「重试」先删 `.part`；「续传」校验通过则续写 |
| 失败/取消（**目录项**） | 只提供「重试」= 整项重传（先清理该子树内已知 `.part`）；目录级逐文件续传列 P3.1（D10/§12.3 a） |
| `part size == total` | 跳过数据阶段直接提交（不演"续传 0 字节"） |
| 上传续传校验不通过 | 删远端 `.part` + 整份重传 + 明示「未能续传，已重新开始」 |
| 源文件在传输中被截断 | `done != total` ⇒ **不得提交**，报错并保留 `.part`（防"假完整"） |
| 崩溃遗留 `.part` | 本地启动清理 >7 天；远端由文档说明（D14） |
| 取消后同一次运行内 | 队列项提供「清理」动作立即删本地/远端 `.part`（对 7 天窗口的补偿，D14） |
| 文件名接近长度上限 | 临时名/备份名退化为同目录短名 `.sshore-sftppart-<id8>-<rand>`（归属靠 `PartPath`），不会 `ENAMETOOLONG` |
| 会话中途断线 / 探活超时 | 传输报错并标为可重试；该会话丢弃（close），下次重建 |
| **Session 0（服务/非交互会话）** | Task 0 在本机这一次的混装组合（客户端 9.5p1 + 旧安装服务端 sshd 9.2p1；本机没有第二套 sshd 可作对照）下观测到：Session 0 里由 Go 进程 spawn 的 ssh.exe **≥15–25s 无响应**（recvVersion→recvPacket→io.ReadFull；从未观察到更长等待后的恢复，也没有做「更长等待是否自行恢复」的实验；window station/desktop 未试）。改到交互桌面会话（Session 1）后全部正常。**应用必须运行在交互会话**（真机验收一律在 Session 1 执行）。注：「WebView2 在 Session 0 不可用」是此前 v0.6.0 会话的既有观察，**不是本 task 的证据**。**另（修复轮 1 附带发现，2/2 复现）**：Session 0 里把 stdout 与 stderr 合并重定向到同一文件时 ssh 同样无响应（rc=124），只重定向一路或分开写文件都正常；根因未定位，且**与上一条是否同因未证明**（修复轮的 T1–T4 子进程流接线未记录，存在混杂变量） |
| 会话容量（自审后已无「池满失败」） | 传输并发上限 1（超出 FIFO 排队）；idle 池满（2）时 `List`/`Connected` 仍**复用或新建**、永不排队也不会失败（D2） |
| 远端磁盘满 / 权限拒绝 | 远端原文上屏；提示额外空间需求（临时文件与目标并存） |
| 子系统缺失（subsystem request failed） | 明确文案 + 检查远端 sshd_config 的 `Subsystem sftp`；阶段 1 同时验 `-s` 前置/后置写法 |
| 符号链接 | **文件**软链跟随（与今天一致）；**目录**软链不跟随（OpenSSH readdir 用 lstat，`sftp-server.c:1151` 已核实） |
| 非 OpenSSH 服务端 | 两条路径分开：**上传提交**无 `posix-rename` → 走 backup-swap 并**在日志里明示发生了降级**（不是静默）；**用户主动 `rename/move`** → 明确失败并提示先删目标（见下一行）。显式 offset 续写是协议自带能力（`SSH_FXP_WRITE` 带 Offset），无需探测 |
| 同目标重复触发 | 传输表按 `(host, direction, target)` 去重（D10） |
| 远端路径含控制字符 | 库直连无批处理注入风险，仍拒绝（纵深防御，保留既有断言） |

---

## 9. 测试与验证策略

0. **决策→验证落点矩阵**（三审 M4/M5 要求闭合）：D1→§9.4/§10.1；D2→§9.1/§9.2；D3→§9.1/§9.2(设置页回归)/§10.2；D4→§9.1(raw-pipe)；D5→§9.1(ModTime)；D6→§9.2(参数基准/rename 覆盖)；D7→§9.2(单调/终值/末帧)；D8→§9.2(扫描相取消)；D9→§9.2(取消四断言+幂等)；D10→§9.1/§9.2(双向续传/去重/旧会话关闭)；D11→§9.2/R3；D12→§9.1(错误映射+stderr 合并)；D13→§9.2；D14→§9.2(崩溃清理+优雅退出清理+无残留进程)；D15→§9.1/§9.2(提交前置/backup-swap 回滚)；D16→§9.1(阈值降级)；D17→§9.3；D18→§9.1/§9.2/§9.3。

1. **Go 单测（无网络）**：进度聚合与 percent/ETA、`.part` 命名与提交前置条件（`done==total`）、双向续传决策（含「源被改动 → 整份重传」「part==total → 直接提交」）、`ModTime` 格式化（含缺失/0）、错误映射、门面 legacy/新面委派、后端选择（env > config > 默认；非法值回退）、会话池状态机与并发/容量规则（传输并发 1、idle 复用与上限 2）、**临时名长度退化（NAME_MAX）**、**raw-pipe 原语**（二进制往返 + `Wait/Kill/Signal` —— 现有 `runner_test` 只覆盖行扫描的 `StartStream`）、**D16 阈值降级**（>2 万文件或 >5s ⇒ `Total=-1 && FilesTotal=-1 && Phase=transfer`）、**backup-swap 回滚**（注入提交失败后目标内容不变）、**截断源单测**（注入中途截断的 reader：`ReadFrom` 对 `io.EOF/ErrUnexpectedEOF` 返回 `(n, nil)`（client.go:2087-2091）⇒ `done != total` 时必须拒绝提交 —— 三审 R13 指出这是唯一防线）、**stderr 合并**（管道错误与独立读取的 `ssh` stderr 都要出现在最终文案）、门面双面命名（legacy 四参 vs `TransferGet/TransferPut`）。
2. **e2e（临时 sshd）**：沿用 `e2e/test_local.sh`（它已启 `Subsystem sftp internal-sftp` 并导出 `SSHORE_E2E_HOST/REMOTE`）与 `internal/sync/e2e_test.go` 的 skip 约定（**`exec.LookPath("sftp")` 在 :28**，要改成 `ssh` —— 评审 B 的行号更正）：
   - 单文件往返 sha256、递归树、参数基准（D6）；
   - 进度回调单调递增、终值 == total、**末帧必达**；
   - 取消：目标名不存在 + `.part` 存在 + `.part` 是源前缀（sha256）+ 续传后 sha256 == 一次性完整；
   - 双向续传 sha256 相等；源被改动 → 整份重传；`part==total` → 直接提交；
   - **传输中截断源 → 必须失败且不提交**；
   - `rename/move` 覆盖已存在目标（保持现状语义）；
   - 覆盖写：目标内容在成功前保持旧内容；
   - 崩溃遗留 `.part` 的启动清理（可用改系统时间的注入点或直接构造旧文件）；
   - 远端 `.part` 不被 sync 当新文件（`ScanTree`/`align` 路径）；
   - 会话复用（第二次调用不再握手）、**idle 池已满时 `List` 仍成功且不排队**、探活超时丢弃会话、无残留进程；
   - **取消幂等**（对已结束 id 返回 `false`）、**扫描相取消 → 不进入 `transfer` 且不新建会话**、**优雅退出后本地与远端 `.part` 均消失**（D14）、**目录项只提供重试**、**D10 去重与「重试前等旧会话关闭」**（同目标二次触发排队/拒绝）。
   - **后端矩阵**：`SSHORE_SFTP_TRANSPORT=batch` 与 `gosftp` 各跑一遍 —— `e2e/test_local.sh:228-231` 目前是单次内联 env，需加循环（评审 B 的遗漏 9）。
   - **垫片透传**（自验发现）：`e2e/test_local.sh:215-227` 会生成只作用于本次 `go test` 的 `ssh`/`sftp` 垫片（`-F` 指向临时 ssh 配置）；gosftp 后端走 `ssh … -s sftp`，垫片必须**原样透传 `-s`**，否则 e2e 连不上。另：`e2e/test_local.sh:231` 目前**只跑 `./internal/sync/`**，必须改成同时跑 `./internal/sftp/`（`make e2e` 的包落点 —— 三审 R7）。
3. **前端 vitest**：`queue.js` 新纯函数 + 前缀常量谓词（`includes` 而非 `startsWith`）+ **设置页回归**（保存其它设置后 `sftp_transport` 不被清空 —— 对应 R14）；现有 **98** 个用例必须全绿。
4. **真机**：Linux 桌面（XTEST 流程）+ **Windows 客户机**（按 `real-machine-testing` 的成熟手段：VirtualBox win10 + NAT `sshfwd 127.0.0.1:2222→22`）：
   - `ssh -s` 前置/后置两种写法、`posix-rename` **能否覆盖已存在文件**、`Seek(partSize)` 续传；
   - 进度条与取消手感（人眼）；
   - **残留进程三点断言**：传输后 / 取消后 / 退出后各跑一次 `tasklist /FI "IMAGENAME eq ssh.exe"`（评审 B 的遗漏 10）；
   - GUI 进程注入 `SSHORE_SFTP_TRANSPORT` 的方式要验证。
5. **回归门**：`make ci`（`Makefile:94-98` = `npm run build` + `go vet` + `go test -race` + vitest 98/98）。

---

## 10. 迁移、开关与发布

1. **真机前置验证（最先做）**：Windows 客户机上手工验 `ssh -s` 两种写法 + 20 行 Go 探测程序（读目录、传文件、**覆盖已存在目标的 rename**、`Seek(partSize)` 续写）。**这是最大未知，先证伪再写代码。**
2. 会话层（状态机 + raw-pipe 原语含 stderr）+ `Backend` 接口 + 门面双面（`NewBatchBackend` 更名 + 36 处测试机械替换）+ 配置项三处 + 前端 load/save + 选择器注入 + `go.mod/go.sum` 依赖升级（三审 R8）（默认仍 `batch`，`make ci` 全绿）。
3. GoBackend 单文件 `Get/Put`（`.part` + 能力探测 + backup-swap + `done==total` 前置）+ 进度事件（含末帧）+ 取消（整批）。
4. 目录/递归（枚举 + 阈值降级 + 聚合进度）+ 单文件双向续传（D10）+ `rename`/`move` 走 `PosixRename` + 其余能力迁移（**`RemoveRecursive` 必须自实现 Walk 并保留 `ctrl.go:592-644` 的失败语义与路径逐字匹配** —— 三审 R10）。
5. 前端进度/取消/重试/续传 UI + 临时文件隐藏 + vitest。
6. e2e 扩展：**新增 `internal/sftp/e2e_test.go`**（当前只有 sync 侧 e2e）+ `e2e/test_local.sh` 增加一条 `go test ./internal/sftp/` 调用与**后端矩阵循环** + 断言语义（取消四断言、双向续传、崩溃清理、sync 忽略）+ Linux 真机验收。
7. Windows 真机验收（含三点进程断言）+ **内置默认切 `gosftp`** + 文档更新（`README.md:22` 依赖说明、架构清单 `README.md:164-174`、配置段新增 `sftp_transport`）+ 发布说明（v0.7.0）。
8. 清理阶段（v0.8）：删除 batch 后端、`parser.go`、`listmany_parse.go`、批处理专用逻辑、开关与配置键，**并删除 `e2e/test_local.sh` 里的批处理时代用例与探针**：TEST 3（`:102-111`）、PROBE A/B/C（`:113-195`，含 `failedDeletePaths` 的 stderr 形状断言）、以及 `:215-227` 的 `sftp` 垫片（三审 M6，已自验）。

---

## 11. 风险与缓解

| # | 风险 | 缓解 |
|---|---|---|
| R1 | **Win32-OpenSSH 的 `ssh -s sftp` / `posix-rename` 覆盖行为**（最大未知；评审 A/B 均未能验证） | 阶段 1 先做真机探测（含两种 `-s` 写法与覆盖测试），失败则该路线当场重新评估 |
| R2 | 长驻会话/子进程泄漏、僵尸进程 | D2/D14 + 三点进程断言（e2e + 真机） |
| R3 | 递归语义漂移（`put -r` 并入语义） | e2e 探针固化 + 旧后端可回退 |
| R4 | 进度事件过密导致 UI 抖动与回调堆积（**不是丢帧**，三审 F3） | 200ms 节流 + 完成时强制末帧 + 前端以绑定返回值为准 |
| R5 | 错误文案回归 | D12 + 错误映射单测清单 |
| R6 | 会话数增长或池满导致 UI 卡住 | 传输并发上限 1（FIFO）+ idle 池上限 2（LRU 关闭）+ 列表/变更**复用或新建、永不排队** |
| R7 | 换代后大改难以归因 | 配置项/环境变量开关 + 保留 batch 后端一个发布周期 |
| R8 | **上传续传的前缀假设**（无法在不读回远端的前提下验证内容） | 三重校验 + 不满足即整份重传 + 明示 + e2e sha256 断言 |
| R9 | 提交失败（无 `posix-rename`、目标被占用、`.part` 提交前置不满足） | 能力探测 + backup-swap 回滚 + 明确报错 + 保留 `.part` |
| R10 | 临时文件与目标并存导致空间不足 | 错误文案点明；文档写明该代价 |
| R11 | 退化路径下目标短暂改名（backup-swap 窗口） | 写进 §8；窗口内目标名以 `.sshore-bak-*` 存在，失败必回滚 |
| R12 | 源在传输中被截断 → 半截文件变「完整」 | `done==total` 提交前置 + e2e 断言 |
| R13 | 非 OpenSSH 服务端能力缺失（`posix-rename` / 只读句柄限制） | D12 明说能力缺失 + 显式 `OpenFile` flags + 阶段 1 真机以 OpenSSH 为主 |
| R14 | **`SetSettings` 整结构覆盖清空新配置键** | D3 的前端 load/save 必做项 + 设置页回归用例 |
| R15 | 依赖升级的连带影响：`pkg/sftp v1.13.11` 要求 go1.25.0 + `x/crypto v0.54.0` + `x/sys v0.47.0`（仓库现为 0.53.0/0.46.0） | `go.mod/go.sum` 变更 + **双平台构建（CI go-windows/build-windows）复验**（三审 R8） |
| R16 | raw-pipe 的 `stderr` 无人读导致 `ssh` 假死 | 原语默认后台 drain stderr 到有界缓冲 + 「不读 stderr 也不阻塞」测试（三审 R3） |

---

## 12. 决策确认记录（2026-09-18）

### 12.1 用户已拍板（前 5 条 + 默认项）

| # | 议题 | 决定 |
|---|---|---|
| 1 | 落盘方式 | 临时名 + 原子改名（D15） |
| 2 | 取消语义 | 取消整批；`.part` 保留；不自动重试（D9/D10） |
| 3 | 目录进度 | 先枚举，超阈值（2 万文件 / 5s）降级为不定进度（D16） |
| 4 | 续传范围 | **下载 + 上传都做**；仅同一次运行内（D10） |
| 5 | 开关形式与寿命 | 配置项 `[app] sftp_transport` + 环境变量（env 优先）；batch 后端 v0.8 删除（D3） |
| 6 | 进度 UI | 进度条 + 百分比 + 速度 + ETA；不做批量总体进度条（D17） |
| 7 | 并发传输 | 本期不做 |
| 8 | 重试 | 纯手动，不自动 |
| 9 | 符号链接 / 权限 / mtime | 与 OpenSSH 保持一致 |
| 10 | 事件节流 | 200ms/次（+ 末帧必达） |
| 11 | 真机验收协作 | 客观数据为主 + 出 Windows 产物供手工点验进度/取消手感 |
| 12 | 独立评审 | 已做（A：架构正确性；B：事实核查/可执行性），意见已合入本版 |
| 13 | 发布节奏 | 本轮 v0.7.0；batch 后端 v0.8 删除 |

### 12.2 冷评审后的建议项（用户已于 2026-09-18 全部接受，按决定执行）

| # | 议题 | 已确认的决定 | 备选（仅溯源） |
|---|---|---|---|
| a | `.part` 是否在面板可见 | **隐藏**（前后端共用前缀常量） | 可见（透明但传输期间会跳动；卡住时用户可手删） |
| b | 无 `posix-rename` 时的提交 | **backup-swap**（目标短暂改名，绝不消失） | 退回直接写目标（= 现状语义，放弃原子性） |
| c | sync 是否享受新能力 | **不享受**（四参 legacy + 自带 `.part`+rename） | 也走新面（要改 `adapters.go`/`transfer.go` 并处理两套 `.part`） |
| d | 崩溃遗留 `.part` 清理 | 本地启动清 >7 天 + **优雅退出清已知项（含远端）** | 每次启动全清（多实例误删风险） |
| e | 探活超时 / 池满策略 | 探活 5s 超时即弃会话；**列表/变更永不排队**（复用或新建，自审后从构造上消除该失败类）；**传输并发上限 1**、超出排队 | 全部排队（极端情况 UI 卡在"连接中"） |
| f | `part==total` | 跳过数据阶段直接提交 | 走一次 0 字节续传 |
| g | 设置界面是否露出开关 | **露出下拉**（Auto/新/旧） | 只进 load/save 不露 UI |

### 12.3 三审后由实现方拍板的小决定（可推翻，改动需在此追加记录）

| # | 议题 | 定值 | 理由 |
|---|---|---|---|
| a | 目录项续传 | **不支持续传，只支持重试**（整项重传，先清该子树内已知 `.part`） | 单值 `PartPath` 表达不了树内多锚点；目录级逐文件跳过/续传并入 P3.1（与跨重启续传同批） |
| b | 临时名长度阈值 | **200 字节**；超限用同目录短名（`.sshore-sftppart-<id8>-<rand>` / `.sshore-sftppart-bak-<rand>`） | 避免 `ENAMETOOLONG`；归属靠传输表的 `PartPath` |
| c | 临时文件匹配语义 | **中缀 `strings.Contains/`前端的 `includes`**（不是 glob 前缀、不是 `startsWith('.')`） | 名字形如 `<name>.sshore-sftppart-…`，不在行首 |
| d | 传输并发 | **1**（FIFO 排队），不做并行 | 与 §3.2 非目标一致；避免 N 会话与磁盘争抢 |
| e | 目录枚举阈值 | **20000 文件 / 5s** | 小中目录拿到准确分母；超大目录降级为不定进度（实现期若实测不合适，改这里并同步 D16） |
| f | 构造函数命名 | `NewCtrl` = 门面；`NewBatchBackend` = 旧实现；包内 36 处测试构造机械替换 | 三审 R2：否则阶段 2 到不了「`make ci` 全绿」 |
| g | raw-pipe 原语 | 返回 `stdin/stdout/stderr` 三路裸管道 + `Wait/Kill/Signal`，stderr 默认后台 drain | 三审 R3：不读 stderr 会让 `ssh` 因 64KB 管道写满而假死 |
| h | 提交中断恢复 | `journal`（`target/bak/part` 三元组）写在状态目录，启动/下次连接时恢复 | 三审 R4：会话在两次 `Rename` 之间断开时，光靠「失败则回滚」不可执行 |
| i | legacy 面的原子性 | `TransferRequest.Atomic=false`（直写目标，sync 自带 `.part`+rename）；新面 `Atomic=true` | 三审 R6：否则 `sync` 的 `Get` 在 gosftp 下会多套一层 `.part` |

---

## 13. 附：现状 → 目标 映射

| 现状（batch） | 目标（GoBackend） |
|---|---|
| `ls -la` + `ParseLsLf` + 回显分块 + stderr 反查 | `ReadDir`/`ReadDirContext`（失败目录直接 error；门面"缺失 key"语义保留） |
| `sftp> pwd` 输出解析 | `Getwd()`/`RealPath` |
| `get` / `get -r` / `put` / `put -r` | `Open/OpenFile` + `io.Copy` + 自实现树遍历 + `.part`/提交（D15） |
| `rename` 批处理（覆盖已存在目标） | `PosixRename`（**不能用 `Rename`**，见 D6） |
| `-rm`/`-rmdir` 批量 + stderr 反查失败路径 | `Remove`/`RemoveDirectory` + `Walk` |
| `mkdir` 批处理 | `Mkdir`/`MkdirAll` |
| ControlMaster 复用（Linux）/ 每次全握手（Windows） | 长驻会话（两端一致）；ControlMaster 只服务 watch/forward |
| 无进度 / 无取消 / 半截文件落在最终名 | 计数 reader/writer 进度 + 会话级取消 + `.part` 原子落盘 |

---

## 14. 附：spike 复现步骤（§2.2 数据的可核对性）

两轮评审都指出「spike 未入库、数字不可复现」。复现方式（约 3 分钟，**不需要改仓库**）：

1. 目录：`mkdir -p /tmp/sftpspike/src`，用隔离的模块缓存避免污染全局：`export GOMODCACHE=/tmp/sftpspike/gomodcache GOPATH=/tmp/sftpspike/gopath GOFLAGS=`。
2. `go mod init spike && go get github.com/pkg/sftp@v1.13.11`。
3. `main.go` 要点（约 100 行）：
   - `exec.Command("ssh","-o","BatchMode=yes","-o","StrictHostKeyChecking=no","localhost","-s","sftp")`，取 `StdinPipe/StdoutPipe`，`cmd.Stderr = os.Stderr`，`cmd.Start()`；
   - `cl, _ := sftp.NewClientPipe(stdout, stdin)`；
   - 下载：`io.Copy(countWriter{file, meter}, remoteFile)`；上传：`io.Copy(remoteFile, countReader{sourceFile, meter})`；`countWriter/Reader` 每 300ms 打印 `done/total + MB/s`；
   - 取消：`time.AfterFunc(300*time.Millisecond, func(){ cl.Close() })`，观察 `io.Copy` 返回错误的时间与已写字节；
   - `cl.ReadDir(".")` 验证列举。
4. 本机需要一个可用的 sshd 与免密别名（`ssh -o BatchMode=yes localhost true` 成功即可）。
5. 复现指标：建会话 ≈200ms 量级；320MB 下载 ≈0.9s、上传 ≈4.8s（速率随机器/磁盘变化，**只看"进度单调递增 + 取消在百毫秒级生效"这两个定性结论**）。
6. 清理：`chmod -R u+w /tmp/sftpspike && rm -rf /tmp/sftpspike`（Go 模块缓存是只读的，直接 `rm -rf` 会因权限失败）。
