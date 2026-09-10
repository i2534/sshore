# 远端文件夹监控同步（Remote Folder Sync）设计

- **日期**: 2026-09-10
- **状态**: 待评审（第 2 稿，已合并两轮独立复审）
- **关联**: `docs/superpowers/specs/2026-08-25-sshkit-design.md`、`2026-08-26-auto-reconnect-design.md`（状态机与退避沿用其模式）
- **路径判定**: 架构级（新子系统：新探测层 + 新同步引擎 + 新配置模型 + 新前端模块）

> **修订说明**：第 1 稿经"自审 + 两个独立 subagent 复审"后重写为第 2 稿（改动集中在**数据安全**与**两条探测路径的元信息同源**）；第 3 稿把 §6.1/§6.2 中原本靠文档推断的行为**全部改为实测结论**（inotify-tools 3.22.1.0 与 3.22.6.0 两台机器对照，逐条证据见 §6.2 与 §13）。逐条修订记录见 §13，**仍未证实的项集中在 §11.1**。

---

## 1. 目标

监控**远端主机上指定的路径**（目录或单个文件），把发生的变化同步到**本地对应的路径**。

- 目录支持**限深**：`max_depth = 0` 仅本层文件，`N` 递归 N 层，`-1` 无限。
- 单文件是独立规则类型（`kind = "file"`），不复用 `max_depth` 表达。
- 探测采用**混合策略**：优先远端 `inotifywait`（近实时），不可用自动回退 SFTP 轮询。
- 默认**只增不删**；默认**不覆盖用户改动**。

## 2. 非目标

- 不做双向同步。
- 不引入 rsync（依赖面保持"只要有 ssh / sftp"）。
- **不改变 `internal/sftp` 现有方法的语义**；允许新增只读方法 `ListMany`（见 §6.4）。
- 不做系统通知。

## 3. 已确认决策

| # | 决策 | 结论 |
|---|---|---|
| 1 | 变更探测机制 | **混合**：优先 `inotifywait`，不可用回退轮询；每规则独立判定、降级可观测 |
| 2 | 删除传播 | **默认关闭**；规则级 `mirror_delete` 可开，配 §7.6 的多重删除闸门 |
| 3 | 监控源类型 | 目录或单文件；目录支持 `max_depth` 限深 |
| 4 | 传输方式 | 复用现有 SFTP 逐文件拉取，零新依赖 |
| 5 | 首次启用行为 | **先全量对齐**，再转入持续监控；首轮语义见 §7.8 |
| 6 | 生命周期 | 对齐隧道模式：`enabled` + `auto_reconnect` + 全局 `AutoStartOnLaunch`；日志按状态变化记录 |
| 7 | 本地改动冲突 | 检测到本地被改过 → 不覆盖 + 告警 + 冲突处理界面 |
| 8 | 内部架构 | 分层双包：`internal/watch` + `internal/sync` |
| 9 | 底层改动 | 抽 `internal/sshconn`；扩 `internal/osutil` 的 stdout 流式能力；`internal/sftp` 新增 `ListMany` |
| 10 | 状态落盘 | 独立状态文件 `<UserConfigDir>/sshore/state/sync-<id>.json` |
| 11 | inotify 的 max_depth 限制 | 内核 watch 覆盖全层级、`max_depth` 仅过滤事件；**注释写明**；真实后果见 §11 W1 |
| 12 | poll 的时间精度 | 接受；且**明确不得用 mtime 做"未变化"的判定**（见 §6.4、§11 W2） |
| 13 | 重复日志 | `sync` 与 `sftp` 两条来源**先都保留**；量级与风险见 §9.4、§11 W7 |
| 14 | 进度展示 | sync 自带轻量进度区；不复用 SftpView 的 `transfers` 局部状态（**仓库里不存在"传输 store"**） |
| 15 | `kind = "file"` 的 `mirror_delete` | **无效**（源消失一律不删本地），UI 置灰并说明 |
| 16 | 单文件退避重试 | 1s / 4s / 16s，共 3 次（**与 §8.2 的重连退避是两套参数**） |
| 17 | 删除阈值待确认 | **内存态、不持久化**；确认绑定轮次指纹并逐条复核 |
| 18 | 重连后对账 | **强制**执行；且对账必须能发现删除（见 §7.8） |
| 19 | 规则编辑 | 编辑运行中的规则 = 先 Stop 再保存；**删除规则一并删除状态文件与本地临时残留** |
| 20 | 绑定命名 | 统一 `SyncRule*` 前缀，避开既有 `SyncWindowBackground` |
| 21 | 自动启动落点 | `AutoStartEnabled` 扩展为同时启动 `enabled` 的同步规则（见 §8.5） |

## 4. 架构与包边界

```
        app.go（Wails 绑定 + 事件转发，只做接线）
           │
     ┌─────┴──────┐
     ▼            ▼
internal/sync  internal/sftp（现有方法语义不变，新增 ListMany）
 引擎 + 编排       │
     │            │
     ▼            ▼
internal/watch  internal/sshconn（ControlMaster socket 的唯一来源）
 探测层            │
     │            │
     └────┬───────┘
          ▼
   internal/osutil（扩展：stdout 流式回调）
   internal/config（扩展：SyncRule）
```

### 4.1 新增 `internal/watch`

```go
type Kind string

const (
    KindCreate   Kind = "create"     // 新文件出现
    KindWrite    Kind = "write"      // 已有文件被修改
    KindDelete   Kind = "delete"     // 文件消失
    KindDirAdded Kind = "dir_added"  // 目录进入监控树（含 mv 进来）→ 触发该子树对账
    KindDirGone  Kind = "dir_gone"   // 目录离开监控树 → 触发该子树对账
    KindOverflow Kind = "overflow"   // 内核队列溢出 → 强制全量对账 + 本轮禁止删除
    KindRootGone Kind = "root_gone"  // 根目录 UNMOUNT / DELETE_SELF / IGNORED
)

// Event 只携带"路径 + 意图"。**不携带 Size / ModTime**——原因见 §6.4：
// 两条探测路径的元信息必须同源，唯一同源来源是 ls 结果。
type Event struct {
    RelPath string // 相对 remote_root，"/" 分隔；空串表示根目录自身
    Kind    Kind
}

type Info struct {
    Mode     string // "inotify" | "poll"
    Reason   string // 降级原因（Mode=="poll" 时必填且具体）
    Interval time.Duration
}

type Source interface {
    Start(ctx context.Context) (<-chan Event, error) // 仅在 Start 内创建一次
    Info() Info
    Close() error // 幂等；**必须关闭 Start 返回的 channel**，否则引擎 range 永不退出
}
```

- `rename` **归一成 delete + create**，避免平台差异泄漏到引擎层。
- 引擎层永远不知道事件来自哪条探测路径——这是"混合探测不失控"的全部秘密。
- 契约强度（现实版）：两条路径在**同一份脚本化的世界状态变化**下，产出的**文件级**结论（路径集合 + 增/改/删）等价。inotify 会额外产出 `dir_* / overflow / root_gone` 这类辅助事件，poll 以"完整扫描 + diff"隐式表达。**不要求逐事件相等**——inotify 一次保存可能发多个 `write`，poll 一轮只发一个。

### 4.2 新增 `internal/sync`

编排（每规则一个 goroutine）+ 引擎（去抖、决策、冲突）+ 扫描器（§6.4 共用）+ 状态持久化 + 校验。

校验放在这里（`internal/sync/validate.go`），**不能放 `internal/config`**：`config` → `forward` → `config` 会形成导入环，Go 编译不过。

### 4.3 新增 `internal/sshconn`

```go
// ControlPath 的 key **必须包含 user**：同一 host 上不同 user 的规则若共用
// 一个 master，ssh 要么拒绝复用、要么用错身份读写远端文件。既有实现
// （sftp/ctrl.go:37-41）只对 host 做 PathEscape，本特性必须修正。
func ControlPath(host, user string) string

// EnsureMaster 幂等建立 ControlMaster。可用性判定**必须**用
// ssh -O check 的退出码，不能用 os.Stat（陈旧 socket 文件会一直 stat 成功，
// 见 sftp/ctrl.go:179-188 的既有做法与其缺陷）。
func EnsureMaster(ctx context.Context, host, user string) error

// Exec 执行一次性远端命令用于探测。**必须可取消**：
// osutil.Runner 内部是 exec.Command，不带 context 也无 Kill 句柄，
// 用它做超时只会泄漏子进程与 goroutine。对齐 config/parser.go:84 的既有做法。
func Exec(ctx context.Context, host, user, cmd string) (osutil.Outcome, error)
```

**必须抽出的理由**：监听连接与文件传输要挂在**同一个 ControlMaster socket** 上才有连接复用。同时这里有一个既存事实必须写明：

> `sftp.Ctrl` 里**只有 `Connect()` 建立 master**（`ctrl.go:134`，`-o ControlMaster=yes -N -f`），执行操作的 `run()` 用的是 `-o ControlMaster=no`，**只复用、不建立**。按 `man ssh_config`，socket 不存在时 ssh 会静默回落为普通连接。也就是说：**用户没点过「连接」时，sftp 的每条命令其实各自建连**。

因此 sync 不能假设 master 存在：

- 规则启动时调 `EnsureMaster`；失败则**降级为每命令独立连接并记一条日志**（功能正确，只是慢），而不是失败；
- 用户中途点「断开」会关掉 master（`ctrl.go:167-175`），sync 需在下次命令失败时重建。

**与 `sftp.CloseAll` 的耦合**：`CloseAll()` 会 `os.RemoveAll(controlDir)`（`ctrl.go:90`），`app.go:572` 的 `OnShutdown` 会调用它。共享 socket 目录后，退出流程会连带拆掉 sync 正在多路复用的连接——**顺序见 §8.6**。

### 4.4 改动现有代码

1. **`internal/osutil`**：新增流式启动能力。

   ```go
   type StreamHandlers struct {
       OnStdout func(string)
       OnStderr func(string)
   }
   type Streamer interface {
       StartStream(name string, args []string, h StreamHandlers) (*Process, error)
   }
   ```

   三条**必须写进实现**的契约：

   - **回调为 `nil` ⇒ 不接对应管道**。现有 `realSpawner.Start` 在回调为 nil 时根本不建 stderr 管道（`runner.go:58-72`）；无条件建管道而调用方不消费，子进程写满管道缓冲后会**永久阻塞**。"Start 委托后行为不变"这一等价性恰恰依赖此规则。
   - **`cmd.Start()` 失败必须返回 `nil, err`**。现有实现失败时仍返回非 nil 的 `Process`，而它的 `done` channel 既无人写也无人关（`runner.go:73-76`）→ 任何 `Wait()` 调用**永久阻塞**。forward 现有调用方先查 err 所以没暴露，但 sync 若写 `defer p.Wait()` 就会挂死。
   - 旧 `Spawner.Start` 签名不动（改签名会破坏 `forward/ctrl_test.go` 的 fakeSpawner），内部委托给 `StartStream`；**上面两条改动必须由现有 `forward` 测试守住**（§10.1）。

2. **`internal/sftp/ctrl.go`**：
   - `controlPathFor(host)` → `sshconn.ControlPath(host, user)`。**公开方法签名一个不动**：`Ctrl` 内部维护 `users map[string]string`（host → 最近一次使用的 user），`run`/`Connect` 写入，`Disconnect`/`Connected`/`disconnectLocked`/`CloseAll` 读取（取不到时退化为空 user）。`CloseAll` 改为遍历这张 map，**不再反解 socket 文件名**（带 `+user` 的路径解不出正确的 host）。
   - 新增批量只读方法 `ListMany`（§6.4）。现有方法的语义与签名不变。

### 4.5 依赖方向

`sync → watch`（接口与扫描器）、`sync → sftp`（**仅经 `adapters.go` 的 `NewSftpAdapter`**）、`sync → forward`（仅复用 `ValidateHost`）、`watch → sftp`（只依赖 `sftp.Item` 这个数据形状）、`watch/sync → sshconn`、`sshconn → osutil`、`sync/watch/sftp → config`。**`config` 不 import 上述任何包**（无导入环）。

## 5. 配置模型

`sshore.toml` 只放**规则定义**（轻）；快照与状态独立落盘（重、可删、可重建）。

### 5.1 规则定义

```go
type SyncRule struct {
    ID         string   `toml:"id" json:"id"`
    Name       string   `toml:"name" json:"name"`
    Host       string   `toml:"host" json:"host"`
    User       string   `toml:"user,omitempty" json:"user,omitempty"`
    Kind       string   `toml:"kind" json:"kind"` // dir | file
    RemotePath string   `toml:"remote_path" json:"remote_path"`
    LocalPath  string   `toml:"local_path" json:"local_path"` // **恒为目录**（见下）
    MaxDepth   int      `toml:"max_depth" json:"max_depth"`
    Excludes   []string `toml:"excludes" json:"excludes"`
    MirrorDelete  bool  `toml:"mirror_delete" json:"mirror_delete"`
    ForcePoll     bool  `toml:"force_poll" json:"force_poll"`   // 反向字段：零值 == 期望默认
    PollIntervalS int   `toml:"poll_interval_s" json:"poll_interval_s"`
    AutoReconnect bool  `toml:"auto_reconnect" json:"auto_reconnect"`
    Enabled       bool  `toml:"enabled" json:"enabled"`
}
```

双 tag 是必须的：`json` 字段名是前端契约，且 `frontend/wailsjs/go/models.ts` 要重新生成（§12）。

**`local_path` 恒为目录**。`kind = "file"` 时本地文件名**强制等于远端 basename**，不提供独立的本地文件路径——避免"本地叫 a.conf、远端叫 b.conf"这种无法用单一规则表达的映射。

### 5.2 默认值与归一化（**必须有，否则缺键静默改变行为**）

`LoadConfig` 先构造默认值再 Decode，但 `[[syncs]]` 数组里的元素是**解码时新建的零值结构体**，预置默认值不生效。因此必须有：

```go
func (r *SyncRule) Normalize() // 由 AppConfig.normalize() 逐条调用
```

| 字段 | 缺省/零值处理 |
|---|---|
| `kind` | 空 → `"dir"` |
| `max_depth` | 零值 `0` 是**合法值**（仅本层），无法区分"未填" → 语义定为：`0` = 仅本层；`-1` = 无限；`< -1` → 归一为 `-1` |
| `prefer_inotify` | **缺键=false ≠ 期望的 true** → 改为反向字段 `force_poll`（默认 false），语义与零值一致 |
| `poll_interval_s` | `< 1` → `5`；`> 3600` → `3600` |
| `excludes` | `nil` → `DefaultExcludes()`；显式 `[]` → 保留为空（用户显式清空） |
| `auto_reconnect` | 缺键 → 取全局 `App.AutoReconnectDefault`（对齐 `Tunnel` 的既有做法） |
| `enabled` | 保持零值 false（不自动启动是安全默认） |

```toml
[[syncs]]
id             = "3f2a1c..."       # 生成，同 NewTunnelID：hex.EncodeToString(16 字节)，
                                   # 32 位纯十六进制、**无前缀**（与"同构"保持一致）
user           = ""                # 空 = 用 ~/.ssh/config
kind           = "dir"             # dir | file
remote_path    = "/etc/myapp/conf"
local_path     = "/home/lan/work/conf"
max_depth      = 2                 # 0=仅本层  N=递归 N 层  -1=无限
excludes       = [".git/", "node_modules/", "*.swp", "*~", ".DS_Store"]  # 唯一一份默认值定义
mirror_delete  = false
force_poll     = false             # true = 强制轮询（零值即"优先 inotify"）
poll_interval_s = 5
auto_reconnect = true
enabled        = true
```

`excludes` 过滤的是**远端**路径。**不要**把 `*.part` 放进默认值：`.sshore-part-*` 写在本地，远端不会出现，那是本地临时文件的清理责任（§7.5）。

### 5.3 状态文件

路径：`<UserConfigDir>/sshore/state/sync-<id>.json`（目录 0700、文件 0600、`f.Sync()`，对齐 `store.go:109/127/135`）

```json
{
  "version": 1,
  "fingerprint": {
    "host": "prod-01", "user": "", "kind": "dir",
    "remote_root": "/etc/myapp/conf", "local_root": "/home/lan/work/conf",
    "max_depth": 2, "excludes": [".git/", "..."]
  },
  "entries": {
    "app.yaml": {
      "remote_size": 1234, "remote_mtime": "2026-09-10 15:47",
      "local_size": 1234,  "local_mtime": "2026-09-10 15:47",
      "written_at": "2026-09-10T15:47:02Z", "adopted": false
    }
  },
  "conflicts": [
    { "rel_path": "db.conf", "remote_size": 88, "remote_mtime": "...",
      "local_size": 91, "local_mtime": "...", "detected_at": "..." }
  ],
  "failed": [ { "rel_path": "secret.key", "err": "permission denied", "at": "..." } ]
}
```

**字段级写入规则（必须严格遵守，否则会静默丢失用户数据）**：

| 字段 | 唯一写入时机 |
|---|---|
| `remote_size` / `remote_mtime` | **只由 `ls` 结果写入**（扫描或决策前补齐，§6.4）。绝不由 inotify 事件或本地值写入 |
| `local_size` / `local_mtime` | 只在两种情况写入：① 一次成功 `rename` 之后；② **首轮采纳**（§7.8，本地与远端大小一致时登记） |
| `written_at` | 只在情况 ① 写入；`adopted` 标记情况 ② |
| `conflicts` / `failed` | 由引擎与冲突处理写入；**不持久化"待确认删除"**（§7.6） |

**判定以"该条目是否存在 `local_size`"为准**（存在 ⟺ 我们建立了本地基线），`written_at` 仅用于审计与展示。**`CONFLICT` 的文件不写 `entries`**——**这是最容易踩的坑**：若把冲突文件的 `local_*` 也填上，下次远端再改它就会走进「有记录且一致 → 远端赢」而**覆盖用户数据**。

**`fingerprint` 与规则不匹配 → 丢弃整个状态文件，按 §7.8 首轮规则重来**。理由：`local_*` 是"我们写过的本地状态"，改了 `local_path` / `kind` / `host` 之后旧记录描述的是**另一份文件**，继续参与判定就是拿错误的依据做冲突决策。

**`entries` 兼任远端基线快照**：上一轮的远端世界由 `remote_*` 描述，本轮扫描与之 diff 即产出事件。不要另建一套快照——两份状态必然漂移。被 `excludes`/`max_depth` 排除的路径从不进入 `entries`，因此也不参与 diff。

**并发写**：状态对象由单把 mutex 保护，落盘走串行化的 `flush()`；**但传输一律由规则 goroutine 执行，绝不在持锁状态下做 I/O**（§7.6）。

## 6. 探测层（`internal/watch`）

### 6.1 降级判定

规则启动时执行一次（**每次重连成功后重新判定**）：

```
ssh -o BatchMode=yes -o ControlPath=<sock> <host> "command -v inotifywait"
  ├─ exit 0 且 !force_poll → Mode=inotify
  └─ 否则                 → Mode=poll
```

- `Exec` **必须带 context**（§4.3）：`osutil.Runner` 不可取消，用它做超时只会泄漏子进程与 goroutine。"降级"必须同时意味着"不留泄漏"。
- `Info.Reason` 必须具体：`远端无 inotifywait（非 0 退出，实测为 1，不是 127）` / `用户选择了强制轮询` / `探测超时` / `远端路径不存在` / `远端 watch 配额耗尽`（§6.2）。
- **日志记录粒度**：`Mode` 变化**必记**；`Mode` 不变而 `Reason` 变化时，按 §8.3 的 5 次汇总（避免在两种失败原因间抖动时刷屏）。
- **静默降级是不允许的**：用户以为在实时同步而实际是 5s 轮询，是这个特性最危险的误信状态。

### 6.2 inotify 路径

```
ssh -tt -o BatchMode=yes -o ControlPath=<sock> <host> \
    "exec inotifywait -m -r --format '%T|%w%f|%e' --timefmt '%s' <remote_root_escaped>"
```

> **本节的每条行为都已实测**（`inotifywait` 3.22.6.0 / kernel 6.12 / aarch64 与 3.22.1.0 / kernel 6.8 / x86_64 两台机器逐项一致，见 §11 W11）。下面的真实样本直接作为 §10.2 的表驱动输入——**不要凭直觉写解析器**。

**`-tt` 是必需的，理由已实测**：

- **不加 `-tt`**：杀掉本地 ssh 后，远端 `inotifywait` **会变成孤儿进程继续运行**（实测观察到存活进程）。
- **加 `-tt`**：**SIGTERM 与 SIGKILL（模拟应用崩溃）都不留孤儿**。
- 代价有二，都必须处理：
  1. 远端 stderr 混入 stdout：`Setting up watches.  Beware: ...` 与 `Watches established.` 会出现在输出流里，必须按格式校验丢弃；
  2. **pty 的 `OPOST|ONLCR` 把每个换行翻译成 CR+LF**（实测 `od -c` 为 `\r \n`）→ **解析前必须 `strings.TrimRight(line, "\r\n")`**。否则最右段变成 `CLOSE_WRITE\r`，与任何事件名都不相等，**整条路径产出 0 个事件**，而告警还会把它误诊为"版本差异"。
**解析规则（按实测输出写）**：

1. 先 `TrimRight(line, "\r\n")`；
2. 从右往左 split 两次 `|`（路径可能含 `|`），最左段是秒级时间戳；
3. **路径必须归一化尾斜杠**：`*_SELF` 事件的 `%w%f` 会以 `/` 结尾且 `%f` 为空（实测 `root/d1/`、`root/`）；
4. **`%e` 是逗号分隔的 token 列表，不是单个事件名**。实测值为 `CLOSE_WRITE,CLOSE`、`CREATE,ISDIR`、`OPEN,ISDIR`、`ACCESS,ISDIR`、`CLOSE_NOWRITE,CLOSE,ISDIR`、`MOVED_TO,ISDIR`、`DELETE_SELF`、`MOVE_SELF` ……**必须先按 `,` 切分成 token 集合再做归约**。用整串相等去比对 `CLOSE_WRITE` 会**全部匹配失败**——每次保存都收不到 `write` 事件。
- `remote_root` **必须做远端 shell 单引号转义**后拼接（§4.3 的 `Exec` 走同一条转义路径）。含空格 / 分号 / 单引号 / `$` 的路径会被远端 shell 解释。
- 事件映射：

  | token 集合特征 | 归一为 |
  |---|---|
  | 含 `CREATE` / `MOVED_TO`，**不含** `ISDIR` | `create` |
  | 含 `CLOSE_WRITE` 或 `MODIFY`，不含 `ISDIR` | `write` |
  | 含 `DELETE` / `MOVED_FROM`，不含 `ISDIR` | `delete` |
  | 含 `CREATE` / `MOVED_TO` 且**含** `ISDIR` | **`dir_added` → 触发该子树的对账扫描** |
  | 含 `DELETE` / `MOVED_FROM` 且含 `ISDIR`；或路径等于根 | **`dir_gone` → 触发该子树对账**（根则为 `root_gone`） |
  | 仅含 `OPEN` / `ACCESS` / `ATTRIB` / `CLOSE` / `CLOSE_NOWRITE` / `MOVE_SELF` | 丢弃 |

  > **判定顺序很重要**：`CLOSE_NOWRITE,CLOSE,ISDIR` 里同时含 `CLOSE`。若先按 `CLOSE` 丢弃再判 `ISDIR`，就会误伤目录事件——**必须先判 `ISDIR`，再在文件分支里判有效 token**。

- **噪声比例（实测）**：写一个文件产生 **5 行**（`CREATE`/`OPEN`/`MODIFY`/`CLOSE_WRITE,CLOSE`；追加写为 4 行）；每个目录还会产生 `OPEN,ISDIR`/`ACCESS,ISDIR`/`CLOSE_NOWRITE,CLOSE,ISDIR`。过滤表不精确会让事件量膨胀数倍。
- **重复上报必须幂等容忍**：删除一个目录实测**同时**产生 `d1/|DELETE_SELF` 与 `d1|DELETE,ISDIR`；移出目录产生 `d2|MOVED_FROM,ISDIR` 与 `d2/|MOVE_SELF`。
  但**移入目录的 `MOVE_SELF` 是竞态的**——为移入目录补挂 watch 的时机与事件赛跑，两台机器上表现不同（一台有、一台没有）。因此：**去重必须是幂等的，但绝不能反过来依赖重复一定出现**。

  > **目录事件绝不可丢弃（已实测）**：把一个目录 `mv` 进监控树（很常见的部署方式）时，内核只产生针对该目录的一条 `MOVED_TO,ISDIR`，**目录里已有的文件不产生任何事件**——实测中移入目录内的 `preexisting.txt` 确实存在，但零事件。丢弃就是整棵子树永久漏同步。
- **`inotifywait` 的 stderr 是一等信号**（不是噪声）：

  | 输出 | 阶段 | 处置 |
  |---|---|---|
  | `Couldn't watch <path>: No such file or directory`（实测 exit 1） | **启动** | 规则进入 `error`（配置错，不重连），Reason = `远端路径不存在` |
  | `Failed to watch ... upper limit on watches reached` | 启动 | 直接 `error`，Reason = `远端 watch 配额耗尽` |
  | `Couldn't watch new directory` | **运行** | 标记"watch 不完整" → 降级 poll 或至少**禁止一切删除**并记 warn |
  | 内核队列溢出 | 运行 | 产出 `KindOverflow` → 强制全量对账 + **本轮禁止删除** |

  > 注意 `Couldn't watch` 前缀在**启动**阶段是致命的、在**运行**阶段（新目录补挂）只是降级——必须按阶段区分，不能只匹配前缀。

- 不匹配格式的行计数，超阈值记一条 warn，**warn 文案必须附原始行样本**（把"版本差异"和"真故障"区分开）。
- **退出码语义**（`--help` 明文，实测一致）：`0` = 收到事件；`1` = 收到未请求的事件（通常是 `delete_self`/`unmount`）**或发生错误**；`2` = `--timeout` 到期无事件。
  > **`1` 是二义的**：`root_gone` 与"启动失败"都返回 1。区分依据必须是**当前阶段（是否已收到过 `Watches established.`）+ stderr 文案**；只看退出码必然误判。
- **根目录消失只能靠事件，不能靠进程退出（已实测）**：`rm -rf` 掉被监控的根目录后，只产生一条 `root/|DELETE_SELF`（尾斜杠、`%f` 为空），**`inotifywait` 进程继续运行**。因此 `root_gone` 的检测**不能挂在 `Process.Wait()` 上**，必须解析事件——否则这个场景会静默漏到底。
- 进程退出（`Process.Wait()` 返回，且 `err == nil` 已检查）→ 状态机进 `reconnecting`，按 §8.2 退避重启。
- **孤儿兜底**：正常路径靠 `-tt`（实测 SIGTERM/SIGKILL 都无残留）。若实现时仍发现残留，用启动前的远端清扫，**但必须避开 `pkill -f` 的自杀陷阱**——远端 shell 的命令行本身包含该模式，实测中 `pkill -f 'inotifywait.*<tag>'` **把自己的 shell 杀掉了**，导致后续清理根本没执行。正确写法是方括号技巧：`pkill -f '[i]notifywait.*<tag>'`。
- **重连成功后强制对账**（见 §7.8）——inotify 断开期间的变更不产生任何事件。

### 6.3 poll 路径

每 `poll_interval_s` 秒扫描一轮，与 `entries` 的远端字段 diff，产出 `create/write/delete`。

**一轮 = 一次事务**（这条决定了 poll 模式状态机的全部行为）：

- 一轮要么**整体成功且完整**，要么**不产出任何事件**；
- `Snapshot.Complete == false`（任一子目录未知）或整轮失败 → **零事件**，失败计数 +1，按 §8.2 退避重试；
- **连续 5 轮失败** → `error`；
- "远端路径不存在"由"启动阶段第一轮即失败"来区分（启动失败不重连，§8.2）。

### 6.4 元信息同源：扫描器与 `ListMany`

**问题**：`inotifywait --format` 里没有大小字段，`Event.Size` 无从填充；inotify 的时间戳是"事件发生的时刻"，不是远端文件 mtime。若两条路径各自往同一份 `entries` 里写不同来源的值，模式切换（§6.1 允许）后基线全部失配 → 每个文件产生一次虚假 `write` → 全量重下或刷出一片冲突。

**解法**：基线收敛到唯一来源——**`ls` 结果**。

- `watch.Event` 只携带"路径 + 意图"（§4.1）；
- 引擎在决策前统一做一次**元信息补齐**：对所有待处理路径跑一次 `ListMany`，得到 `map[relpath]Meta{Size, ModTime}`；
- `entries` 的 `remote_*` **只能**由这个结果写入。

**扫描器**（poll 每轮、inotify 对账、目录 move-in 子树扫描三处共用同一份代码）：

```go
type Meta struct { Size int64; ModTime string }
type Snapshot struct {
    Entries  map[string]Meta // relpath → 元信息
    Complete bool            // false = 有目录未知，禁止据此产生任何 delete
}
func ScanTree(ctx context.Context, l ListManyFunc, root string, maxDepth int, excludes []string) (Snapshot, error)
```

**`ListMany` 契约**（`internal/sftp` 新增）：

```go
// 批处理每行加 '-' 前缀抑制逐命令中止：man sftp 明确 "sftp will abort if any of
// the following commands fail: ... ls ..."，而单条命令前缀 '-' 可抑制中止。
// 不加前缀时，扫描途中有任意一个子目录不可读（权限/正在被删/NFS 抖动），
// 批次就断在那里，后面的目录从未被列出。
//
// 返回的 map 中**缺失的 key 表示"未知"，绝不表示"空目录"**——
// 把它当空目录会让 diff 为整棵子树产出 delete 事件，进而删除本地文件。
// 调用方必须用 Complete=false 表达"本轮不完整"。
ListMany(host, user string, paths []string) (map[string][]Item, error)
```

**分段归属规则**（**这里没有「现成的解析代码」可复用**）：`ParseLsLf` 只返回扁平 `[]Item` 并跳过命令回显行（`parser.go:28`），目录归属信息会丢失。因此 `ListMany` **必须新增按命令回显分段**的代码：**不使用 `@` 前缀**（它会抑制回显，而回显正是分段依据），按回显行切分输出块，逐块喂给 `ParseLsLf`。这属于 §4.4 的底层改动，不是"复用现成代码"。

**`ModTime` 的使用纪律**（重要）：`Item.ModTime` 是字符串，精度到分钟；**超过约 6 个月的老文件只有日期**；且 `parseModTime` 对"时间形式"的字段用 `time.Now().Year()` 猜年份（`parser.go:90`），**跨年瞬间会让一批文件集体产生虚假 `write`**。因此：

- 变化判定 = `size 变化` **或** `ModTime 字符串变化`（保守：宁可多传一次）；
- **绝不允许用 `ModTime` 相同来判定"没变化"**（它不足以证明未变）；
- 跨年误报的代价是"多下载一次"，不是数据损坏 —— 可接受，但**写进 §11 W2**。

## 7. 同步引擎（`internal/sync`）

**流水线**：`事件 → 去重合并 + 300ms 去抖 → 元信息补齐(ListMany) → 决策 → 串行传输 → 落盘状态`

### 7.1 去抖与合并

- 按 `RelPath` 合并，静默 **300ms** 后触发。`create` + `write` → 一次 GET；`create` 后紧跟 `delete`（临时文件）→ 队列中直接消掉。
- **背压**：解析 goroutine **只做"写入按路径去重的 map + 唤醒去抖"，绝不做阻塞 I/O**。map 以 `RelPath` 为键，天然有界于"不同路径数"。
- 不同路径数超过阈值（5000）→ **丢弃细粒度事件、标记"需要全量对账"**（等价 `overflow`），而不是阻塞。理由：解析 goroutine 一旦阻塞，本地 ssh 的 stdout 管道写满 → 远端 inotifywait 阻塞 → 内核队列溢出 → **事件被静默丢弃**。

### 7.2 路径安全（硬防线）

远端可回传任意文件名，不可盲信：

- 拒绝 `..`、绝对路径、含 NUL 的路径；
- 拒绝**含控制字符**（换行等）的路径——这类名字拼进 `sftp -b` 会把一条命令劈成两条，而批处理里以 `!` 开头的行会执行**本地** shell 命令（`quoteArg` 已有此逻辑，见 `ctrl.go:194`）；
- **但 `quoteArg` 是包私有的，且不检查 `..` 与绝对路径** → 校验做成**导出函数**由 `sftp` 与 `sync` 共用，避免两处漂移；
- 拼出的本地绝对路径必须用 `filepath.Rel` 确认**仍在 `local_root` 之内**；比较前先按平台做 `filepath.Clean`（Windows 上反斜杠是分隔符、盘符前缀有特殊语义，纯字符串检查可被绕过）。

### 7.3 决策表

**`hasBaseline` = 该条目存在 `local_size`**（§5.3）。`CONFLICT` 的条目不在 `entries` 中。

| 远端事件 | 有基线 | 磁盘现状 | 动作 |
|---|---|---|---|
| create/write | 否 | 不存在 | **GET** |
| create/write | 否 | 已存在，且 **size 相同** | **采纳**（登记 baseline，不下载，记 info"未校验内容"） |
| create/write | 否 | 已存在，size 不同 | **CONFLICT** |
| create/write | 是 | 与基线一致 | **GET**（远端赢） |
| create/write | 是 | 与基线不一致（用户改过） | **CONFLICT** |
| create/write | 是 | **不存在**（用户删了本地） | **GET**（重新下载，无不一致的数据可失去） |
| delete | 是 | 与基线一致 | 过 §7.6 删除闸门 → 删 / SKIP |
| delete | 是 | 与基线不一致 | 保留 + warn |
| delete | 否 | 存在 | SKIP |

> **这一格极易被漏掉**：若不显式规定，它会落进「不一致 → CONFLICT」，导致用户删掉本地文件后远端再也同步不下来。

### 7.4 传输与原子写

- 下载到目标同级目录的 **`<name>.sshore-part-<ruleid8>-<rand>`**，成功后 `os.Rename` 原子替换。
  - **绝不能用进程 pid 命名**：同进程内所有规则与 UI 触发的传输**共享同一个 pid**，两条规则把同一路径同步到重叠的本地目录时会写**同一个临时文件** → 内容交错 → rename 后**静默损坏**。规则 id 与随机串才真正保证每次传输唯一。
- **原子性由 sync 层负责**：`sftp.Ctrl.Get` 直接 `get remote local` 写目标路径（`ctrl.go:392`，无临时文件），调用方式是 `Get(host, user, remote, tmpPath)` 后自行 rename。
- 目标父目录按需 `os.MkdirAll`（0755）。
- 状态记录取**写完之后实际 stat 到的值**（不依赖 `sftp get` 是否保留 mtime）。
- **临时文件清理责任**：规则启动时清理本规则 `local_root` 下匹配自身命名模式的残留；规则删除时一并清理（**不清理的话，崩溃或 Stop 之后会永远堆积**；文件是否属于本规则只能靠命名模式识别）。

### 7.5 跨规则冲突与串行

- **传输串行**：规则内一次只搬一个文件；规则之间并行（各一个 goroutine）。
- **`CreateSyncRule` / `UpdateSyncRule` 拒绝精确重复**（同 `host` + `remote_path` + `local_path` + `kind`），对齐 `app.go:199/223` 的 `CheckRemoteConflict` 做法。
- **部分重叠不检测**（例如规则 A 同步 `/conf` → `~/x`，规则 B 同步 `/conf/sub` → `~/x/sub`）：此时同一目标文件可能被两条规则先后覆盖，**结果由 rename 的先后决定（last-writer-wins），但不会损坏**（临时文件名含规则 id）。这是**已知局限**，写进 §11。

### 7.6 删除闸门（六重，任一命中即禁止本轮删除）

**千万不要用「待删数 > max(10, 现存条目×10%)」这类带常量下界的阈值**：5 个文件的目录判据是 `5 > max(10, 0.5) = 10` → 假 → 挂载点掉线时 5 个文件被全部删除，而 5 个文件的配置目录恰恰是最常见的规模。闸门必须对**小集合**同样生效：

1. **本轮扫描不完整**（`Complete == false`）→ 禁止删除；
2. **本轮远端条目数为 0 且上一轮非空** → 禁止删除 + warn（挂载点掉线的典型特征）；
3. **规则刚启动 / 重连后 / 模式切换后的第一轮** → 禁止删除；
4. 收到 `root_gone` / `overflow` → 暂停删除直到对账重新确认；
5. **阈值**：`待删数 ≥ 1 且 (待删数 ≥ 10 或 待删数 ≥ 现存条目 × 50%)` → 挂起待确认；
6. **确认动作绑定"轮次号 + 路径集合指纹"**，执行前**逐条重新校验远端仍不存在**；任一条已恢复 → 整批作废并提示（挂载点恢复后用户在陈旧卡片上点确认，语义上就是删一批本不该删的文件）。

待确认状态**是内存态、不持久化**（防误删的临时闸门；持久化会让一个陈旧的"待确认"在下一次意外场景里被当成通行证）。触发时**只挂起删除，下载照常**。

目录类删除的本地语义：`dir_gone` **不做 `RemoveAll`**（超出文件级语义）；只清理该子树内**有基线且与基线一致**的文件。

### 7.7 冲突队列

持久化在状态文件。三个动作：

- `keep_local`：以本地为准，**只写 `local_*`（取当前磁盘值）并登记基线**，`remote_*` **保持为本次冲突时观测到的远端值**（§5.3 字段级规则）。
  **语义边界**：只对**这一次**冲突有效，不构成长期保护——远端下次再改该文件，仍按 §7.3 覆盖本地。要"这个文件永远别动"，正确做法是加进 `excludes` 或收窄规则范围。
- `take_remote`：覆盖本地并更新记录。
- `save_as`：远端版本另存为 `<name>.remote-<ts>`，本地保留。

**`ResolveConflict` 只做"入队 + 通知"，绝不自己执行传输**——真正的传输一律由该规则的 goroutine 串行消费。理由：若绑定线程持状态 mutex 去下载，而引擎 goroutine 持"串行传输"锁等状态 mutex，就是 ABBA 死锁；且两条传输路径共享同一份临时文件名（§7.4）。

### 7.8 首轮对齐与对账

**首轮**（`entries` 为空，或该条目无 `local_size`）：扫描远端，按 §7.3 逐条走。**"本地已存在且 size 相同 → 采纳"这条规则是必须的**——否则"删除状态文件后重建"与"规则首次指向非空本地目录"这两条最常见路径会变成一条都不同步、外加一屏冲突，与决策 5 的"先全量对齐"直接矛盾。

**对账扫描**（三处触发：规则启动、重连成功、目录 `dir_added`/`overflow`）：

- 对账的定义是 **"本轮远端扫描 vs `entries` 的 `remote_*`"**，与 poll 共用同一份 diff 代码；
- **inotify 路径必须同样维护 `remote_*`**：若不维护基线，断开期间的**删除**就永远无从发现（扫描只能看到还存在的文件），`mirror_delete` 下就是永久漏同步；
- **模式切换（inotify ↔ poll）后必须重建基线**：跑一次完整扫描、刷新 `remote_*`，本轮**不产出除删除以外的事件**，且按 §7.6 第 3 条**禁止删除**。

### 7.9 单文件规则（`kind = "file"`）

- 不做目录 BFS，`RelPath` 恒为远端 basename；
- **远端源文件消失** → 记 warn、本地保留、规则**不进入 `error`**（文件很可能稍后回来）；**不受 `mirror_delete` 影响**（"源消失"更可能是路径配错、文件被临时挪走或目录未挂载；UI 上 `mirror_delete` 置灰并说明）；**不参与 `root_gone` 之外的删除闸门**（集合里只有一个文件）；
- 该状态由 `SyncRuleStats.SourceMissing` 表达，**不新增第六个状态字符串**（§8.1 的五态保持不变），卡片在圆点旁渲染独立警示徽章。

### 7.10 输入校验

落点 `internal/sync/validate.go`（**放 `config` 会造成 `config → forward → config` 导入环**）：

- `host`：复用 `forward.ValidateHost`（实际正则是 `^[A-Za-z0-9][A-Za-z0-9._-]*$`，不只是"禁止前导 `-`"）；
- `remote_path`：非空、绝对路径或 `~` 开头，且**拒绝会破坏远端 shell 引用的字符**（在 `sshconn` 层做单引号转义兜底）；
- `local_path`：非空、可 `MkdirAll`；
- `kind ∈ {dir, file}`；`max_depth ∈ {-1} ∪ [0, 64]`；`poll_interval_s ∈ [1, 3600]`；
- 校验在**创建/编辑时**即拒绝（对齐 `forward.ValidateTunnel`）；但归一化（§5.2）必须能兜住**手改配置文件**的情况。

## 8. 生命周期与错误处理

### 8.1 状态

沿用 `forward` 的五个字符串：`stopped / connecting / connected / reconnecting / error`（`connected` = 监控中）。首轮对齐期间的进度、活跃计数、`SourceMissing` 等**一律走 `SyncRuleStats`**，不塞进状态、不新增第六个状态。

### 8.2 重连与退避

- **启动失败不重连，运行中断开才重连**。但这条二分在 **poll 路径上没有"进程退出"这个信号源**，必须补定义（§6.3）：一轮失败 → 零事件 + 计数 + 退避；连续 5 轮失败 → `error`；"远端路径不存在"由"启动第一轮即失败"区分。
- 退避：1s → 2s → 4s → 8s → 16s → 30s 封顶；连续在线稳定满 60s 清零计数（沿用 `forward` 的 `stableThreshold` 语义）。
- **`auto_reconnect = false` 时意外断开进 `error` 不重连**（对齐 `Tunnel.AutoReconnect` 的行为，默认取全局 `App.AutoReconnectDefault`）。
- 已知局限（写进 §11）：退避是确定性序列且无抖动，多规则与多隧道同时抖动会同步重试。

### 8.3 日志纪律

状态变化记一条；连续失败每 5 次汇总一条；不逐次刷屏。**日志事件消息表**（用户可见面，必须固定措辞）：

| 事件 | 级别 | 文案模板 |
|---|---|---|
| 探测方式确定 | info | `监控已启动：<mode>（<reason>）` |
| 降级 | warn | `已降级为轮询：<reason>` |
| 断开 | warn | `探测连接断开，正在重连（第 N 次）` |
| 重连成功 | info | `已重连，开始对账扫描` |
| 单文件完成 | info | `下载 <relpath>（远端<create\|变更>，<size>）` |
| 单文件失败 | error | `下载 <relpath> 失败：<err>（已重试 3 次）` |
| 冲突 | warn | `<relpath> 本地已修改，未覆盖` |
| 采纳 | info | `<relpath> 本地已存在且大小一致，未下载（未校验内容）` |
| 删除挂起 | warn | `本轮待删 N 个文件（上限 M），已挂起等待确认` |
| 禁止删除 | warn | `本轮扫描不完整/远端为空，已禁止删除` |

### 8.4 并发

- 规则内传输串行、规则之间并行。
- **不变式：同一规则不得被 UI 与引擎并发传输**（§7.7 的入队模型保证）。
- 单文件失败：退避重试 3 次（1s / 4s / 16s）后标记 `failed` 并**继续处理其它文件**，规则整体不中断。**注意这是与 §8.2 并存的第二套退避参数**，不要合并成一套。

### 8.5 配置变更与自动启动

- 编辑运行中的规则 = **先 Stop 再保存**，不热更新（对齐 `UpdateTunnel`）。
- 删除规则：**一并删除状态文件与本地残留临时文件**。删除状态文件的实际后果是「下次按 §7.8 首轮规则重建」——**不是简单的"慢但正确"**：本地与远端大小不一致的文件会逐条进冲突队列。
- **自动启动的落点必须显式写明**：现有唯一执行点是 `main.go` 的延迟 1s goroutine 调用 `app.AutoStartEnabled()`，而它只遍历 `a.cfg.Tunnels`。本特性要求**扩展 `AutoStartEnabled`，同时启动 `enabled` 的同步规则**（沿用同一个 1s 延迟约束），否则 `enabled = true` 的规则会永远停在 `stopped`。

### 8.6 应用退出

`OnShutdown` 顺序**必须是**：**停止所有规则并关闭探测进程 → flush 状态文件 → 最后 `sftp.CloseAll()`**。理由：`CloseAll` 会 `os.RemoveAll(controlDir)`（`ctrl.go:90`），先执行会拆掉 sync 正在多路复用的连接。

## 9. 前端与绑定

### 9.1 绑定（统一 `SyncRule*` 前缀，避开既有 `SyncWindowBackground`）

```go
func (a *App) ListSyncRules() []config.SyncRule
func (a *App) CreateSyncRule(r config.SyncRule) (config.SyncRule, error)
func (a *App) UpdateSyncRule(r config.SyncRule) error      // 运行中先 Stop
func (a *App) DeleteSyncRule(id string) error              // 连带清理状态文件与本地残留
func (a *App) StartSyncRule(id string) error
func (a *App) StopSyncRule(id string) error

func (a *App) SyncRuleStates() map[string]string           // id → 五态字符串（对齐 forward.States）
func (a *App) SyncRuleStats() map[string]SyncRuleStat

type SyncRuleStat struct {
    Mode, Reason   string
    PollIntervalS  int
    Pending, Done, Failed, Conflicts int
    AlignScanned, AlignTotal         int  // 首轮对齐进度
    CurrentFile                      string
    SourceMissing                    bool
    DeletePending                    int  // 挂起待确认的删除数
}

func (a *App) SyncRuleConflicts(id string) []Conflict
func (a *App) ResolveSyncConflict(id, relPath, action string) error // 只入队，不传输
func (a *App) RetrySyncRuleFailures(id string) error
func (a *App) ConfirmSyncRuleDeletes(id string, fingerprint string) error // §7.6 第 6 条
```

### 9.2 界面

- 左导航加第三项「同步」。
- 新增 `SyncCard.vue`：**不复用 `RuleCard.vue`**（它的 prop 是 required 的 `tunnel`、直接 import `StartTunnel/StopTunnel`、meta 用 `modeLabel(tunnel.mode)` 与 `listen_bind:listen_port`）。只复用 CSS 与视觉语言。
  卡片要素：状态圆点、**探测徽章**（`inotify` / `轮询 Ns`，降级时黄 + 悬浮显示 `Reason`）、`SourceMissing` 警示徽章、四组计数、首轮对齐进度、「日志」按钮、「冲突」按钮（>0 时）、「确认删除」按钮（挂起时）、开始/停止。
- 新建/编辑对话框：主机用 **`ListHosts`**（与两个现有视图一致；`ListHostsDetailed` 在前端零引用，且它对每个 alias 跑 `ssh -G`，放对话框打开路径上会明显卡顿）、远端路径、本地路径（`PickLocalDir`）、`kind`、`max_depth`、`excludes`、`mirror_delete`、`force_poll`、`poll_interval_s`、`auto_reconnect`、`enabled`。
- 新增 `SyncConflictsDialog.vue`：逐条显示两侧大小时间，三个动作，支持批量。
- 进度区：自带，不复用 SftpView 的 `transfers` 局部状态（§3 决策 14）。

### 9.3 实时性（必须照现有约束写，否则会破坏已修复的隔离）

- **不新增第二个 `EventsOn`**。`App.vue:19-34` 已持有全局 `log` 订阅且注释警告 HMR 下重复注册。
- 照 `ForwardView` 的做法：**订阅 pinia store 后防抖刷新**（`logStore.$subscribe` 过滤 `source_type === 'sync'` → 防抖调 `SyncRuleStates()/SyncRuleStats()`），**不轮询**。
- `App.vue` 用 `<KeepAlive>`，`onUnmounted` 不会触发 → 用 `onActivated/onDeactivated` 管理订阅与定时器（`SftpView.vue:364-373` 已有先例）。

### 9.4 日志隔离（**与现有机制直接冲突，必须显式决策**）

`LogPanel` 按 `sourceTypes` 做**视图级隔离**：`ForwardView` 传 `['tunnel','system']`、`SftpView` 传 `['sftp','system']`（`LogPanel.vue:35-36`）。因此：

- **决策**：`SyncView` 传 `['sync','system']`。**不把 `sftp` 加进去**——否则 SftpView 里所有手动 SFTP 操作的日志会涌进同步视图，破坏刚刚修好的隔离。
- **不要指望「两条日志都出现在同步视图里、且可分别过滤」**：sftp 日志的 `source_id` 是 **host**，不是规则 id，而卡片的「日志」按钮按规则 id 过滤（`LogPanel.vue:24-26/40/82` 的 `filterSource` 是自由文本过滤）→ **永远命不中**。所以：`sftp` 传输日志只出现在 SFTP 视图；同步视图只有 `sync` 一源。
- **量级**：`Ctrl.Get` 每次**成功记两条 info**（`ctrl.go:397` 开始、`ctrl.go:416` done），失败再记 error（`ctrl.go:405/409`）；加上 `sync` 决策日志，**每个文件 3–4 条**（不是直觉上的两条）。环形缓冲只有 1000 条（`stores/logs.js:6`）→ 约 300 个文件即可刷满。决策 13 选择"先都保留"，**风险与首要缓解手段（抑制 sftp 的 done 行）记入 §11 W7**。

## 10. 测试策略

### 10.1 `internal/osutil`（本次重构的回归护栏）

- **`Start` 委托 `StartStream` 后行为不变**：既有 `forward` 全量测试必须原样通过。
- 回调为 `nil` ⇒ **不建对应管道**（否则子进程写满缓冲后永久阻塞）。
- `cmd.Start()` 失败 ⇒ **返回 `nil, err`**（现在返回的半成品 `Process` 会让 `Wait()` 永久阻塞）。

### 10.2 `internal/watch`

- 事件解析表驱动：输入**直接用 §6.2 实测抓到的真实输出**，最少覆盖：
  - 带 `\r\n` 行尾的行（**只有这类样本能发现 pty ONLCR 缺陷**）与不带 CR 的行；
  - `CLOSE_WRITE,CLOSE`（**token 列表**，验证不会被整串比较漏掉）、`CREATE,ISDIR`、`CLOSE_NOWRITE,CLOSE,ISDIR`（验证**先判 ISDIR 再判文件 token** 的顺序）；
  - `d1/|DELETE_SELF` 与 `d1|DELETE,ISDIR` 成对出现 → **幂等去重后只产出一个 `dir_gone`**；
  - `root/|DELETE_SELF`（尾斜杠、`%f` 为空）→ `root_gone`，且**断言它不依赖进程退出**；
  - `Setting up watches.  Beware: ...` / `Watches established.` 噪声行；带空格 / 带 `|` 的文件名；
  - `Couldn't watch <path>: No such file or directory`（启动）vs `Couldn't watch new directory`（运行）→ **分类必须不同**。
  - **竞态容忍**：移入目录的 `MOVE_SELF` **有时有、有时没有**（实测两台机器不一致）→ 两种输入序列都必须得到相同的最终结论。
- poll 快照 diff 表驱动；**不完整轮次（`Complete=false`）必须产出零事件**。
- 降级判定：假 Runner 返回 exit 0 / **exit 1（实测值，不是 127）** / 超时 → 期望 Mode 与 Reason；**超时必须真取消子进程**（用真实短命进程验证无泄漏）。
- **契约测试**：两条路径在同一份脚本化世界状态变化下，**文件级**结论（路径集合 + 增/改/删）等价；不断言逐事件相等。

### 10.3 `internal/sync`

- 决策表**逐行**测试（含 §7.3 全部 9 行、`keep_local` 后的字段写入、`take_remote`、`adopted` 路径）。
- **路径穿越安全测试**：`../../etc/passwd`、绝对路径、`a/../../b`、Windows 反斜杠/盘符变体，必须被拒绝且**不触碰文件系统**。
- **删除闸门逐条测试**：六条各一个用例；特别是"5 个条目的目录全部待删"必须挂起（**带常量下界的阈值在这个用例上会失败**）。
- **确认后的复核**：挂起 → 远端恢复 → 确认 → **整批作废**（不得删除）。
- **原子写与临时文件**：传输失败 → 目标不被破坏；两条规则写同一目标 → 临时文件不冲突；残留清理。
- **不做阻塞 I/O 的断言**：解析侧在传输卡住时仍能持续消费（防背压死锁）。
- 状态文件：字段级写入规则（`remote_*` 不被本地值污染）、`fingerprint` 不匹配 → 丢弃重来、损坏/缺失 → 首轮规则、并发 `flush`。
- **重连对账测试**（§11 W3，正确性关键路径）：断开期间远端**新增/修改/删除** → 重连后必须全部补齐。
- **`ResolveConflict` 并发测试**：UI 侧发起 `take_remote` 时引擎正在传输 → 无死锁、无临时文件冲突。
- 计时器注入（对齐 `forward` 的 `after func(time.Duration)` 模式），**不靠 sleep**。
- 传输接口抽象（`ListDir` / `Get` / `ListMany`）注入假实现 → **引擎测试完全脱离网络**。

### 10.4 `internal/config` / `internal/sshconn`

- `SyncRule.Normalize()` 逐字段：**手写配置缺键**（尤其 `force_poll` 与 `excludes`）不得静默改变行为；旧配置向后兼容。
- `sshconn`: `ControlPath` 的 key 含 user（同 host 异 user **不得**共用 socket）；`EnsureMaster` 对**陈旧 socket**（文件在但 master 已死）必须不走"已可用"分支（用 `ssh -O check` 的退出码）。

### 10.5 绑定与 E2E

- 绑定层沿用 `app_test.go` 的契约风格（空切片而非 nil、JSON 字段名）。
- **E2E 的驱动方式必须写明**：`e2e/test_local.sh` 是纯 bash、直接调 `ssh/sftp` 二进制，**没有 CLI 入口能触达 `internal/sync`**。方案：脚本起好临时 sshd 后，调用一个**新增的 Go 测试**（`TestSyncE2E`，从环境变量接收端口/别名）来驱动引擎；脚本层只负责准备宿主机与断言 OpenSSH 行为。本机没有 `inotifywait` 时**跳过并打印原因，不静默通过**。
- `inotifywait` 的运行时行为**已在两台真实机器上实测**（§11 W11），E2E 若目标环境没有该依赖，仍须**跳过并打印原因**，不得静默通过。

## 11. 已知弱点

| # | 弱点 | 真实影响 | 处置 |
|---|---|---|---|
| W1 | `inotifywait -r` 无 maxdepth，内核 watch 覆盖全部层级 | **不是"消耗配额"这么轻**：配额耗尽时启动即失败（`Failed to watch`）→ 按 §8.2 进无限重连风暴，每轮重建全部 watch；运行时补挂失败（`Couldn't watch new directory`）则**带残缺 watch 静默漏同步**，而徽章仍显示 inotify | 两类消息提升为一等信号（§6.2）；逃生舱 `force_poll = true`；代码注释必须写明 |
| W2 | poll 的 mtime 精度 | 老文件（>6 个月）**只到天**；且 `parseModTime` 用 `time.Now().Year()` 猜年份 → **跨年瞬间一批文件集体产生虚假 `write`** → 全体重下一次 | 代价仅"多传一次"，可接受；**纪律：绝不用 mtime 相同判定"未变化"**（§6.4） |
| W3 | inotify 断开期间的变更不产生事件 | 必须靠重连后的对账补齐；**且对账必须能发现删除**，否则 `mirror_delete` 下永久漏同步 | §7.8 已覆盖，**测试必须落地**（§10.3）。**不要以为这是唯一的正确性缺口**：目录 move-in（§6.2）是另一条常态缺口 |
| W4 | ~~`inotifywait` 运行时行为未实测~~ → **已实测，该项关闭** | `-tt` 在 SIGTERM 与 SIGKILL 下均无孤儿；**不加 `-tt` 必留孤儿**；`-m -t N` 空闲 N 秒后以 **exit 2** 退出 | 结论已并入 §6.2。`-t` **不作为**孤儿兜底（空闲目录会被反复重启 + 每次触发对账）；兜底用**方括号技巧**的 `pkill` |
| W5 | `-tt` 使远端 stderr 混入 stdout，且换行被翻译成 CR+LF | 解析必须裁 CR（否则 0 事件），噪声行需严格校验 | §6.2 已覆盖；样本测试必须含 CR |
| W6 | 大文件无断点续传 | 中断需整体重来 | 已接受（选传输方式 A 的固有代价）；出现几百 MB 级文件时需重新评估 |
| W7 | 同步 + sftp 双来源日志量 = 每文件 3–4 条 | 1000 条环形缓冲约 300 个文件即刷满 | 决策 13 选择先都保留；**首要缓解手段：抑制 `sftp` 的 `get done` 行**，实现时若确认噪声过大应立即启用 |
| W8 | 部分重叠的规则本地目标由 last-writer-wins 决定 | 结果不确定（但**不会损坏**，临时文件名含规则 id） | 不检测（检测成本高）；精确重复已在 §7.5 拒绝 |
| W9 | 退避是确定性序列且无抖动 | 多规则与多隧道同时抖动会同步重试 | 未实测；仅作观察记录 |
| W10 | 首轮"采纳"只比对 size，不校验内容 | 本地同 size 不同内容的文件不会被下载 | §7.3/§7.8 明确；用户可用 `take_remote` 或删除本地文件后重下 |
| W11 | **（正面结论）** 关键行为在 inotify-tools **3.22.1.0 / 3.22.6.0**、kernel **6.8 / 6.12**、**x86_64 / aarch64** 上逐项一致 | —— | **不需要按版本分支**；§6.2 的解析规则可直接按实测实现 |

### 11.1 仍未证实的（不要当成已验证）

1. `Failed to watch ... upper limit on watches reached`（watch 配额耗尽）：需耗尽 `max_user_instances=128` / `max_user_watches=524288`，未做。
2. 运行阶段新目录补挂失败的 `Couldn't watch new directory` 文案。
3. 内核队列溢出（`IN_Q_OVERFLOW`）的实际输出形态（`KindOverflow` 的触发条件因此仍是推测）。
4. 挂载点 `UNMOUNT` 事件。
5. 非 Linux 远端、真实网络断连、以及 ControlMaster 与 `-tt` 的组合行为。

## 12. 影响面

**新增**

- `internal/sshconn/`（`ControlPath` / `EnsureMaster` / `Exec`）+ `*_test.go`
- `internal/watch/`（`event.go` 契约、`inotify_parse.go`、`detect.go`、`inotify.go`、`scan.go`、`poll.go`）+ `*_test.go`
- `internal/sync/`（`ctrl.go`、`decide.go`、`paths.go`、`state.go`、`transfer.go`、`adapters.go`、`delete_gate.go`、`conflict.go`、`validate.go`、`fs.go`）+ `*_test.go`
- `frontend/src/views/SyncView.vue`、`frontend/src/components/SyncCard.vue`、`frontend/src/components/SyncConflictsDialog.vue`
- 运行期目录 `<UserConfigDir>/sshore/state/`

**修改**

| 文件 | 改动 |
|---|---|
| `internal/osutil/runner.go` | 新增 `Streamer`/`StreamHandlers`/`StartStream`；nil 回调不接管道；`Start` 失败返回 `nil, err`；`Start` 签名不变 |
| `internal/osutil/runner_test.go` | 上述三条的回归测试 |
| `internal/sftp/ctrl.go` | `controlPathFor` → `sshconn.ControlPath(host, user)`；新增 `ListMany`（`-` 前缀 + 回显分段）；**现有方法语义不变** |
| `internal/config/store.go` | 新增 `SyncRule`、`SyncRule.Normalize()`、`AppConfig.Syncs`、`NewSyncID()` |
| `internal/config/store_test.go` | 归一化与向后兼容测试 |
| `app.go` | 新增 §9.1 绑定；`AutoStartEnabled` 扩展（§8.5）；`OnShutdown` 顺序（§8.6）；`CreateSyncRule` 的重复检测（§7.5） |
| `app_test.go` | 绑定契约、自动启动、退出顺序 |
| `frontend/wailsjs/go/main/App.d.ts` / `App.js` / `models.ts` | **这三个生成物被 git 跟踪**；Makefile 全部构建带 `-skipbindings`，**必须显式重新生成**（不是自动的） |
| `frontend/src/App.vue` | 第三项导航与视图 |
| `README.md` / `README.zh-CN.md` | 功能特性与架构小节 |
| `e2e/test_local.sh` + 新增 `TestSyncE2E` | 见 §10.5 |

## 13. 第 2 稿修订记录

| 来源 | 修订 |
|---|---|
| 自审 | `sshconn` 补 `EnsureMaster`/`Exec`（原稿未说明 master 由谁建立）；`ListMany` 计入影响面；`excludes` 默认值去掉 `*.part`；`entries` 明确兼任基线；`keep_local` 语义边界；`kind=file` 的 `mirror_delete` 无效；契约测试断言强度；`wailsjs` 生成物 |
| 复审 A（后端） | **B1** 删除阈值对小集合恒不触发；**B2** pty CR 导致整条 inotify 路径 0 事件；**B3** `sftp -b` 遇 `ls` 失败中止整批 + 缺失 key 语义；**B4** 冲突文件被写入本地基线 → 绕过冲突保护；**M1** 对账发现不了删除；**M2** 目录 move-in 整棵子树漏同步；**M3** W1 被写轻；**M4** `Runner` 不可取消；**M5** 两条路径元信息不可比；**M6** 删除状态文件后果与决策 5 矛盾；**M7** poll 无"断开"信号源；**M8** `EnsureMaster` 判据不足；**M9** `ResolveConflict` 死锁与临时文件冲突；**M10** `Start` 三条未声明契约；**M11** ControlPath 不含 user + `CloseAll` 耦合；N1–N10 全部采纳 |
| 复审 B（配置/前端） | `SyncRule.Normalize()`（缺键静默改变行为）；状态文件缺规则指纹；`AutoStartEnabled` 无落点；`List`/`Get`/`ListMany` 命名不一致；两个 `excludes` 默认值；`LogPanel` 的 `sourceTypes` 冲突与"可分别过滤"不成立；实时性（KeepAlive、不二次 `EventsOn`）；绑定缺签名；`SyncWindowBackground` 撞名；`RuleCard` 不可复用；`ListHostsDetailed` 零引用；校验落点会形成导入环；`NewTunnelID` 无前缀；e2e 无 CLI 驱动；§3 决策表漏记 5 条 |
| 代码事实核实 | `man sftp` 的中止清单与 `-` 前缀 ✅；`parseModTime` 的 `time.Now().Year()` ✅；`Start` 失败仍返回半成品 `Process` ✅；`controlPathFor` 不含 user ✅；`CloseAll` 会 `RemoveAll` ✅ |
| **第 3 轮：两台真实机器实测** | 在 `pi`（inotify-tools 3.22.6.0 / kernel 6.12 / aarch64）与本机（3.22.1.0 / kernel 6.8 / x86_64）上用同一套探针逐项对照：**证实** `-tt` 的 CRLF、`-tt` 的孤儿回收（含 SIGKILL）、无 `-tt` 必留孤儿、目录 move-in 不补发子事件、`-m -t` 的 exit 2；**新发现并修正** `%e` 是逗号分隔 token 列表、`*_SELF` 路径带尾斜杠、根目录被删后进程不退出、`command -v` 缺失返回 1（非 127）、退出码 1 二义、`pkill -f` 自杀陷阱、移入目录的 `MOVE_SELF` 竞态。**仍未证实项见 §11.1** |
