# 远端文件夹监控同步（Remote Folder Sync）设计

- **日期**: 2026-09-10
- **状态**: 待评审
- **关联**: 主设计文档（`docs/superpowers/specs/2026-08-25-sshkit-design.md`）、自动重连设计（`2026-08-26-auto-reconnect-design.md`，状态机与退避沿用其模式）
- **路径判定**: 架构级（新子系统：新探测层 + 新同步引擎 + 新配置模型 + 新前端模块）

---

## 1. 目标

监控**远端主机上指定的路径**（目录或单个文件），把发生的变化同步到**本地对应的路径**。

- 目录支持**限深**：`max_depth = 0` 仅本层文件，`N` 递归 N 层，`-1` 无限。
- 单文件是独立的一种规则类型（`kind = "file"`），不复用 `max_depth` 表达。
- 变更探测采用**混合策略**：优先远端 `inotifywait`（近实时），不可用时自动回退到 SFTP 轮询。
- 默认**只增不删**：远端删除不影响本地，除非规则显式打开 `mirror_delete`。
- 默认**不覆盖用户改动**：检测到本地文件被用户改过时不覆盖，进入冲突队列等待处理。

## 2. 非目标（明确不做）

- **不做双向同步**。本地 → 远端的推送不在本次范围。
- **不引入 rsync**。传输依赖面保持"只要有 ssh / sftp"，不扩大到"远端必须装 rsync"。
- **不改造 `internal/sftp` 的操作语义**。sync 只调用它现有的 `List` / `Get`，一行不改其行为。
- **不做系统通知**。失败与冲突只在卡片计数与日志面板体现。

## 3. 已确认决策

| # | 决策 | 结论 |
|---|---|---|
| 1 | 变更探测机制 | **混合**：优先 `inotifywait`，不可用回退轮询；每规则独立判定、降级可观测 |
| 2 | 删除传播 | **默认关闭**（只增不删）；规则级 `mirror_delete` 可开，配删除阈值保护 |
| 3 | 监控源类型 | **目录或单文件**；目录支持 `max_depth` 限深 |
| 4 | 传输方式 | **复用现有 SFTP 逐文件拉取**（`sftp -b get`），零新依赖 |
| 5 | 首次启用行为 | **先全量对齐**，再转入持续监控；对齐期间的事件按路径去重合并，不丢 |
| 6 | 生命周期 | **完全对齐隧道模式**：`enabled` + 设置项 `AutoStartOnLaunch` + 退避重连；日志按状态变化记录，不逐次刷屏 |
| 7 | 本地改动冲突 | **完整 B 方案**：检测到本地被改过 → 不覆盖 + 告警 + 冲突处理界面（保留本地 / 用远端覆盖 / 另存副本） |
| 8 | 内部架构 | **方案 1**：`internal/watch`（探测）+ `internal/sync`（引擎）分层双包 |
| 9 | 提及的底层改动 | 接受：抽 `internal/sshconn`、扩 `internal/osutil` 的 stdout 流式能力 |
| 10 | 状态落盘位置 | 接受独立状态文件 `<UserConfigDir>/sshore/state/sync-<id>.json` |
| 11 | inotify 的 max_depth 弱点 | 接受"内核 watch 覆盖全层级，max_depth 仅过滤事件"，**但必须在代码注释中写清楚** |
| 12 | poll 的时间精度弱点 | 接受 `ls -la` 的分钟级精度 |
| 13 | 重复日志 | `sync` 与 `sftp` 两条日志**先都保留**，后续看实际效果再决定是否加静默开关 |
| 14 | 进度展示 | sync **自带**轻量进度区，**不复用** SFTP `TransferQueue` 的 store |

## 4. 架构与包边界

```
        app.go（Wails 绑定 + 事件转发，只做接线）
           │
     ┌─────┴──────┐
     ▼            ▼
internal/sync  internal/sftp（行为一行不改，只被调用）
 引擎 + 编排       │
     │            │
     ▼            ▼
internal/watch  internal/sshconn（新：ControlMaster socket 的唯一来源）
 探测层            │
     │            │
     └────┬───────┘
          ▼
   internal/osutil（扩展：stdout 流式回调）
   internal/config（扩展：SyncRule）
```

### 4.1 新增 `internal/watch`

对外只暴露一个窄接口，两个实现共用：

```go
type Kind string

const (
    KindCreate Kind = "create"
    KindWrite  Kind = "write"
    KindDelete Kind = "delete"
)

type Event struct {
    RelPath string    // 相对 remote_root，"/" 分隔，不含 root 前缀
    Kind    Kind
    Size    int64
    ModTime time.Time
}

type Info struct {
    Mode     string // "inotify" | "poll"
    Reason   string // 降级原因（Mode=="poll" 时必填，且必须具体）
    Interval time.Duration
}

type Source interface {
    Start(ctx context.Context) (<-chan Event, error)
    Info() Info
    Close() error
}
```

- `rename` **归一成 delete + create**，避免平台差异泄漏到引擎层。
- 引擎层永远不知道事件来自哪条探测路径——**这是"混合探测不失控"的全部秘密**，也是契约测试（§10）要守住的东西。

### 4.2 新增 `internal/sync`

编排（每规则一个 goroutine）+ 引擎（去抖、决策、冲突判定）+ 状态持久化。

### 4.3 新增 `internal/sshconn`

从 `sftp.Ctrl.controlPathFor` 抽出的 SSH 连接参数（`ControlPath(host)` 等）。

**必须抽出的理由**：监听连接与文件传输要挂在**同一个 ControlMaster socket** 上才有连接复用；两个包各自算一遍路径，是给未来埋一个必然踩的坑。

### 4.4 改动现有代码（两处）

1. **`internal/osutil`**：新增流式启动能力
   ```go
   type StreamHandlers struct {
       OnStdout func(string)
       OnStderr func(string)
   }
   type Streamer interface {
       StartStream(name string, args []string, h StreamHandlers) (*Process, error)
   }
   ```
   `realSpawner` 同时实现 `Spawner` 与 `Streamer`；**旧 `Spawner.Start` 签名一行不动**（改签名会破坏 `forward` 现有测试 mock），其内部改为委托给 `StartStream`。
   动机：现有 `Start` 只抓 stderr（硬编码 `StderrPipe`），而 `inotifywait -m` 的事件流走 **stdout**。

2. **`internal/sftp/ctrl.go`**：`controlPathFor` 改为调用 `sshconn.ControlPath`。纯提取，行为不变。

### 4.5 新增依赖方向

`sync → watch`（接口）、`sync → sftp`（传输适配器）、`watch/sync → sshconn`、`sync/sshconn → osutil/config`。无反向依赖。

## 5. 配置模型

`sshore.toml` 只放**规则定义**（轻）；快照与状态独立落盘（重、可删、可重建）。

### 5.1 规则定义

```toml
[[syncs]]
id              = "s-8f3a1c..."    # 生成，风格同 NewTunnelID（16 字节 hex）
name            = "myapp 配置"
host            = "prod-01"        # ssh alias
user            = ""               # 空 = 用 ~/.ssh/config
kind            = "dir"            # dir | file
remote_path     = "/etc/myapp/conf"
local_path      = "/home/lan/work/conf"
max_depth       = 2                # 0=仅本层文件  N=递归 N 层  -1=无限；kind=file 时忽略
excludes        = [".git/", "node_modules/", "*.tmp", "*.swp", "*.part"]
mirror_delete   = false            # 默认关：远端删除不动本地
prefer_inotify  = true             # 关掉则强制轮询
poll_interval_s = 5
enabled         = true             # 应用启动时自动开始（仍受设置 AutoStartOnLaunch 约束）
```

- `kind` **显式声明**，不靠远端探测猜路径类型：可预测，且探测本身要付出一次往返。
- `excludes` 语义：以 `/` 结尾表示目录前缀匹配，其余用 `filepath.Match` 匹配相对路径与 basename。
- `host` 复用现有 `forward.ValidateHost` 做注入防护（禁止前导 `-`）。
- 新增 `config.NewSyncID()`，与 `NewTunnelID` 同构。

### 5.2 状态文件（不进 TOML）

路径：`<UserConfigDir>/sshore/state/sync-<id>.json`

```json
{
  "version": 1,
  "remote_root": "/etc/myapp/conf",
  "entries": {
    "app.yaml": {
      "remote_size": 1234, "remote_mtime": "2026-09-10T15:47:00Z",
      "local_size": 1234,  "local_mtime": "2026-09-10T15:47:02Z",
      "written_at": "2026-09-10T15:47:02Z"
    }
  },
  "conflicts": [
    { "rel_path": "db.conf", "remote_size": 88, "remote_mtime": "...",
      "local_size": 91, "local_mtime": "...", "detected_at": "..." }
  ],
  "failed": [ { "rel_path": "secret.key", "err": "permission denied", "at": "..." } ]
}
```

- `local_size` / `local_mtime` 记录的是**我们写入本地之后**的状态。**这就是冲突检测的全部依据**：磁盘现状 ≠ 记录 → 说明是用户改的，不是我们写的 → 不覆盖。
- 快照条目数随文件数增长，塞进 TOML 会让配置文件膨胀，且每次 `saveConfig` 都要全文重写 —— 这是分离的核心原因。
- 状态文件损坏/缺失的后果被限定为"下次全量重扫"，**不影响规则定义本身**。
- 写入方式对标 `SaveConfig` 的原子写（临时文件 + rename）。

## 6. 探测层（`internal/watch`）

### 6.1 降级判定

规则启动时执行一次（**每次重连成功后也重新判定一次**，结果变化则记一条日志）：

```
ssh -o BatchMode=yes -o ControlPath=<sock> <host> "command -v inotifywait"
  ├─ exit 0 且 prefer_inotify → Mode=inotify
  └─ 否则                     → Mode=poll
```

`Info.Reason` 必须具体，禁止只写"已降级"：

- `远端无 inotifywait (exit=127)`
- `用户关闭了 prefer_inotify`
- `探测超时`

Reason 同时出现在规则卡片徽章的悬浮提示与日志面板。**静默降级是不允许的**——用户以为在实时同步而实际是 5s 轮询，是这个特性最危险的误信状态。

### 6.2 inotify 路径

```
ssh -tt -o BatchMode=yes -o ControlPath=<sock> <host> \
    "exec inotifywait -m -r --format '%T|%w%f|%e' --timefmt '%s' <remote_root>"
```

- **`-tt` 是刻意的**：给远端会话一个 pty，本地 ssh 退出/被 Kill 时远端进程收到 SIGHUP，**不留孤儿**。代价是远端 stderr 混入 stdout → 靠严格格式校验丢弃非事件行；不匹配的行计数，超过阈值记一条 warn（可能提示版本差异）。
- 解析：对每行**从右往左** split 两次 `|`（路径里可能含 `|`），最左段是秒级时间戳。
- 事件映射：`CREATE` / `MOVED_TO` → create；`CLOSE_WRITE` / `MODIFY` → write；`DELETE` / `MOVED_FROM` → delete；`OPEN` / `ACCESS` / `ATTRIB` 丢弃；`CREATE,ISDIR` 直接丢弃，不产生传输事件（目录只作为路径前缀存在，本身不需要搬运）。
- 进程退出（`Process.Wait()` 返回）→ 状态机进入 `reconnecting`，按 §8 退避重启。
- **重连成功后强制触发一次对账扫描**：inotify 断开期间的变更不产生任何事件，不补扫就是**永久漏同步**。这是该路径唯一的正确性缺口。

### 6.3 poll 路径

每 `poll_interval_s` 秒扫描一轮远端、与上一轮快照比对，产出**同一种** `create/write/delete` 事件。

扫描走 **SFTP 递归 `ls -la`（BFS 到 `max_depth`）**，而不是远端 `find`：

- 复用现成的 `sftp.List()` + `ParseLsLf()` + 错误分类 + 日志链路，**零新增解析代码**；
- 依赖面与传输层完全一致（有 `sftp` 即可用），**Windows 远端也能工作**；`find -printf` 是 GNU 专有，Windows 远端无从谈起；
- `max_depth` 天然精确（只列到指定层）。
- 代价：大目录比 `find` 慢（每目录一次往返；可在一个 `sftp -b` 批处理里塞多个 `ls`，一轮一个进程）。

## 7. 同步引擎（`internal/sync`）

**流水线**：`事件 → 去抖/合并 → 决策 → 传输 → 落盘状态`

### 7.1 去抖与合并

编辑器存一次盘会连发多个 `MODIFY` / `CLOSE_WRITE`。按 `RelPath` 合并，静默 **300ms** 后触发处理。同一路径在队列中只保留"最后意图"：

- `create` + `write` → 一次 GET；
- `create` 后紧跟 `delete`（临时文件）→ 队列中直接消掉，不下载。

### 7.2 路径安全（硬防线）

远端可回传任意文件名，不可盲信。`RelPath` 必须校验：

- 拒绝 `..`、绝对路径、含 NUL 的路径；
- 拼出的本地绝对路径必须用 `filepath.Rel` 确认**仍在 `local_root` 之内**。

这是安全边界，不是可选优化。

### 7.3 决策表

| 远端事件 | 有本地记录 | 磁盘现状 | 动作 |
|---|---|---|---|
| create/write | 无 | 不存在 | **GET** |
| create/write | 无 | 已存在（用户自己放的同名文件） | **CONFLICT**，不覆盖 |
| create/write | 有 | 与记录一致 | **GET**（远端赢） |
| create/write | 有 | 与记录不一致（用户改过） | **CONFLICT**，不覆盖 |
| delete | 有 | 与记录一致 | `mirror_delete` ? 删除 : SKIP |
| delete | 有 | 不一致（用户改过） | 保留 + warn |
| delete | 无 | 存在 | SKIP |

过滤（`excludes`）与限深（`max_depth`）在事件入队前完成。

### 7.4 传输与原子写

- 下载到目标同级目录的 `<name>.sshore-part-<pid>`，成功后 `os.Rename` 原子替换。
- 中断/失败不污染目标文件，重试直接覆盖 `.part`。
- **原子性由 sync 层负责**：`sftp.Ctrl.Get` 是直接 `get remote local` 写目标路径的（`ctrl.go:392`，无临时文件），因此调用方式是 `Get(host, user, remote, tmpPath)` 后自行 rename。
- **目标目录按需创建**：写文件前对目标路径的父目录 `os.MkdirAll`（模式 0755）。
- **状态记录取"写完之后实际 stat 到的值"**，而不是期望值。这样即使 `sftp get` 对 mtime 的保留行为在 OpenSSH 各版本间不一致，冲突检测依然成立——我们只和"自己观察到的现实"比。
- 传输串行（见 §8）。

### 7.5 删除阈值保护

`mirror_delete = true` 时，单轮待删数量 > `max(10, 现存条目 × 10%)` → **暂停该轮删除**、发 warn、卡片显示待确认。

防御场景：远端挂载点掉线导致目录"看起来空了"，进而在本地清空整个目录。

### 7.6 冲突队列

持久化在状态文件（§5.2）。三个动作：

- `keep_local`：以本地为准，**并把记录更新为当前本地状态**（否则下次同步会反复告警同一个文件）；
- `take_remote`：覆盖本地并更新记录；
- `save_as`：远端版本另存为 `<name>.remote-<ts>`，本地原文件保留。

### 7.7 单文件失败

退避重试 3 次（1s / 4s / 16s），仍失败则标记 `failed` 并**继续处理其它文件**，规则整体不中断。

理由：一个权限错误的文件不应让整条规则停摆。卡片显示"N 个失败"并提供重试入口。

### 7.8 首次全量对齐

规则启动时先扫描一遍远端，按 §7.3 同一张决策表处理既有文件；**扫描期间到达的事件按 `RelPath` 去重后并入同一队列**，不丢失。

### 7.9 单文件规则的特殊情形

`kind = "file"` 时：

- 不做目录 BFS，`RelPath` 恒为远端文件的 basename；
- **远端源文件消失**（被删除/被移动）→ 记一条 warn、**本地文件保留不动**、规则**不进入 `error`**（文件很可能稍后回来）；卡片显示"源文件缺失"，文件恢复后自动继续；
- 删除阈值保护在此无意义（集合里只有一个文件），直接跳过该逻辑。

### 7.10 输入校验

`max_depth` 必须在 `-1` 或 `[0, 64]` 之间（上限防呆，避免误填巨大值导致扫描爆炸）；`remote_path` 必须是非空绝对路径或 `~` 开头；`local_path` 必须非空、可 `MkdirAll`；`poll_interval_s` 限制在 `[1, 3600]`。校验失败在创建/编辑时即拒绝（对齐 `forward.ValidateTunnel` 的做法），而不是等到启动时才报错。

## 8. 生命周期与错误处理

### 8.1 状态

**沿用 `forward` 的五个状态字符串**：`stopped / connecting / connected / reconnecting / error`。sync 语义下 `connected` = 监控中。

理由：前端状态圆点组件可直接复用，用户不需要学第二套心智模型。

**活跃度不塞进状态**：待同步 / 已同步 / 失败 / 冲突四组计数由独立绑定 `SyncStats` 返回。

### 8.2 重连

- **启动失败不重连，运行中断开才重连**（与 `forward` 同一条规则）：远端路径不存在、本地不可写、host 非法 → 直接 `error`（配置错误，无限重试只会刷屏掩盖问题）；探测进程意外退出、网络抖动 → `reconnecting`。
- **退避**：1s → 2s → 4s → 8s → 16s → 30s 封顶，无限重试直到用户 Stop。
- 连续在线稳定满 **60s** 清零失败计数（沿用现有 `stableThreshold` 语义，防 flapping 风暴）。

### 8.3 日志纪律

- 状态变化记一条；
- 连续失败每 5 次汇总一条；
- **不逐次刷屏**。

理由：笔记本合盖一天，一个规则就能把 1000 条的日志环形缓冲刷满，把真正有用的日志挤掉。

### 8.4 并发

- **规则内传输串行**：一次只搬一个文件。可预测、不放大远端压力、`.part` 命名不打架。
- **规则之间并行**：每条规则一个 goroutine，天然并行。
- 代价：大文件多时单条规则慢。后续若需要，加规则级 `max_parallel` 是增量改动，不影响架构。

### 8.5 配置变更

- **编辑运行中的规则 = 先 Stop 再保存**，不做热更新（对齐现有 `UpdateTunnel` 行为）。热更新需处理"远端根路径整个换了"的情况，收益小、出错面大。
- **删除规则时一并删除其状态文件**。理由：规则 id 重新生成后旧记录无法被复用，只会让 state 目录堆积；而"删掉重建 → 重新全量对齐"是安全行为（慢但正确）。

### 8.6 应用退出

`OnShutdown`：停止所有规则 → 关闭探测进程 → **flush 状态文件**。"停止监控"的语义是把当前状态落盘，不是丢在半路。

## 9. 前端与绑定

### 9.1 新增绑定（风格对齐现有 `*Tunnel` 系列）

```
ListSyncs / CreateSync / UpdateSync / DeleteSync
StartSync / StopSync
SyncStates / SyncStats
SyncConflicts / ResolveConflict / RetrySyncFailures
```

### 9.2 界面

- 左导航新增第三项「同步」（`App.vue` 现为 forward / sftp 两项 + 设置）。
- 新建 `SyncView.vue`：规则卡片列表，视觉语言复用 `RuleCard.vue`。每张卡：
  - 状态圆点（复用 forward 四/五态样式）；
  - **探测方式徽章**：`inotify` / `轮询 5s`；降级时黄色 + 悬浮显示 §6.1 的具体 Reason；
  - 计数：待同步 / 已同步 / 失败 / 冲突；
  - 「日志」按钮（按 `source_id` 过滤日志面板，与隧道卡片一致）；
  - 「冲突」按钮（仅冲突数 > 0 时出现）；
  - 开始 / 停止。
- 新建/编辑对话框：主机（复用 `ListHostsDetailed` 下拉）、远端路径、本地路径（复用 `PickLocalDir`）、kind、max_depth、excludes、mirror_delete、prefer_inotify、poll_interval、enabled。
- 新建 `SyncConflictsDialog.vue`：逐条显示远端/本地两侧的大小与时间，三个动作，支持批量。
- **进度区**：sync 自带轻量进度区（当前文件 + 队列长度 + 失败/冲突计数），**不复用 SFTP `TransferQueue` 的 store**（两者生命周期与归属不同：一条受规则长期管理，一条是用户一次性操作）。视觉样式复用，状态不共享。

### 9.3 日志

sync 事件走已有的 `forward.Event` 通道，`source_type = "sync"`、`source_id = <规则id>`，因此自动进入现有日志面板 / 环形缓冲 / chips 过滤，与隧道、SFTP 三种来源并列。

**已知副作用（已确认保留）**：`sftp.Ctrl` 会为每次 `Get` 记一条 `source_type="sftp"` 的日志，所以一次同步在日志面板会看到两条：

- `sync` 是决策层视角（"下载 app.yaml（远端变更）"）；
- `sftp` 是传输层视角（"sftp get ... (1.2 KB)"）。

两条都保留（信息不同、可分别过滤），后续视实际效果再决定是否给 `sftp.Ctrl` 加静默开关。

## 10. 测试策略

### 10.1 `internal/watch`

- 事件解析表驱动：真实 `inotifywait` 输出样本，覆盖带空格 / 带 `|` 的文件名、`CREATE,ISDIR`、噪声行。
- poll 快照 diff 表驱动：两轮快照 → 期望事件集合。
- 降级判定：假 Runner 返回 exit 0 / 127 / 超时 → 期望 Mode 与 Reason。
- **契约测试**：两个实现在同一份脚本化输入下产出**完全一致**的事件序列。这是"混合探测"能立住的关键证据。

### 10.2 `internal/sync`

- 决策表逐行测试（含冲突、本地被改、`mirror_delete` 关/开、删除阈值触发）。
- **路径穿越安全测试**：`../../etc/passwd`、绝对路径、`a/../../b` 必须被拒绝且不触碰文件系统。
- 去抖/合并测试：注入事件序列；计时器采用注入式（对齐 `forward` 现有的 `after func(time.Duration)` 模式），**不靠 sleep**。
- 原子写测试：模拟传输失败 → 目标文件不被破坏、`.part` 可清理。
- 状态文件损坏/缺失 → 优雅降级为全量重扫，不崩。
- **可测性前提**：sync 依赖传输接口抽象（`ListDir` / `Get`），生产实现是 `sftp.Ctrl` 的适配器，单测注入假实现 → 引擎测试**完全脱离网络**。

### 10.3 绑定层与 E2E

- 绑定层沿用 `app_test.go` 的契约风格（空切片而非 nil、JSON 字段名）。
- `e2e/test_local.sh` 扩展：临时 sshd + 真实远端目录，跑一次真实扫描与下载，验证 OpenSSH 层面的行为。本机没有 `inotifywait` 时**跳过并打印原因，不静默通过**。

## 11. 已知弱点与待实测确认项

这些是本设计**明确承认**的局限，不是待办事项的委婉说法。

| # | 弱点 | 影响 | 状态 |
|---|---|---|---|
| W1 | `inotifywait -r` 无 maxdepth，深层目录照样建内核 watch，`max_depth` 只在解析事件时过滤 | 大目录消耗内核 watch 配额 | 已接受；**必须在 `inotify.go` 顶部注释写明**"内核 watch 覆盖全部层级，max_depth 仅过滤事件；大目录请改用 `prefer_inotify=false`" |
| W2 | `ls -la` 时间精度只到分钟（`MMM DD HH:MM`） | "同一分钟内、大小不变"的内容修改轮询会漏 | 已接受；inotify 路径无此问题 |
| W3 | inotify 断开期间的变更不产生事件 | 需靠重连后的对账扫描补齐 | 设计已覆盖（§6.2），**实现必须落地，否则是永久漏同步** |
| W4 | `inotifywait -t`（无事件超时退出）在 monitor 模式下的确切语义**未经验证** | 影响能否用超时兜底孤儿 | **待实测确认**；当前孤儿由 `-tt` 解决，兜底方案是启动前跑一次带唯一标记的远端 `pkill -f` |
| W5 | `-tt` 使远端 stderr 混入 stdout | 事件流里出现非事件行 | 靠严格格式校验丢弃 + 计数告警（§6.2） |
| W6 | 大文件无断点续传（传输方式选 A 的固有代价） | 大文件传输中断需整体重来 | 已接受；若实际出现几百 MB 级文件，需重新评估 rsync 或分级策略 |

## 12. 影响面

**新增**

- `internal/sshconn/`（SSH 连接参数与 ControlMaster socket 的唯一来源）
- `internal/watch/`（`source.go` 契约、`inotify.go`、`poll.go`）
- `internal/sync/`（`engine.go`、`ctrl.go`、`state.go`、`paths.go`、`transfer.go`）
- `frontend/src/views/SyncView.vue`、`frontend/src/components/SyncConflictsDialog.vue`
- 状态目录 `<UserConfigDir>/sshore/state/`

**修改**

- `internal/osutil/runner.go`：新增 `Streamer` / `StreamHandlers` / `StartStream`（`Start` 签名不变，内部委托）
- `internal/sftp/ctrl.go`：`controlPathFor` 改为调用 `sshconn.ControlPath`（纯提取）
- `internal/config/store.go`：新增 `SyncRule`、`AppConfig.Syncs`、`NewSyncID()`
- `app.go`：新增 sync 绑定与事件转发、`OnShutdown` 停止规则
- `frontend/src/App.vue`：新增「同步」导航与视图
- `README.md` / `README.zh-CN.md`：功能特性与架构小节
- `e2e/test_local.sh`：新增同步扫描/下载断言
