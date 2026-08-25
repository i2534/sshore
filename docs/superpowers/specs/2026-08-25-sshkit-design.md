# sshkit — 设计规格文档

- **日期**: 2026-08-25
- **状态**: 已确认设计方向
- **技术栈**: Go 1.26 + Wails v2 + Vue 3

## 1. 目标

一个跨平台（Windows / Linux / macOS）桌面 GUI 工具，用于管理：

1. **SSH 端口转发**（本地 `-L` / 远程 `-R` / 动态 SOCKS `-D`）
2. **SFTP 文件管理**（浏览 / 上传 / 下载 / 删除 / 重命名 / 新建文件夹，**不含远程编辑**）

核心约束：**只读解析 `~/.ssh/config` 获取主机信息，不维护凭据库**。密钥、agent、密码、跳板机全部交由系统 OpenSSH 处理。

## 2. 关键决策（已确认）

| 决策点 | 选择 | 理由 |
|---|---|---|
| 技术栈 | Go + Wails v2 + Vue 3 | 单二进制跨平台分发，Go 管理 ssh 子进程干净，前端 GUI 灵活 |
| SSH 底层 | **系统 `ssh` binary** | config/密钥/agent/jump host 免费接管，零重依赖，最契合"只读 config"极简定位 |
| SFTP 底层 | **系统 `sftp` binary（批处理 `-b`）** | 复用 OpenSSH 实现，避免自建 SSH 通道 |
| 端口转发范围 | `-L` / `-R` / `-D`（SOCKS5）全支持 | 三种标准模式都要 |
| SFTP 范围 | 仅文件管理，不含远程编辑 | 明确不做远程编辑 |
| 凭据 | 只读 `~/.ssh/config`，不存凭据 | 密钥走 ssh-agent / IdentityFile |
| 配置格式 | **TOML**（`~/.config/sshkit/sshkit.toml`） | 用户指定 |

## 3. 架构

```
┌──────────────────────────────────────────────────┐
│ Wails Desktop (win/linux/mac)                     │
│  ┌────────────┐      ┌─────────────────────────┐ │
│  │  Vue 3      │ IPC  │  Go 后端 (Wails)         │ │
│  │ ┌────────┐ │◄────►│  ┌───────────────────┐ │ │
│  │ │转发视图 │ │      │  │ ForwardCtrl       │ │ │
│  │ │ 规则列表│ │      │  │ SFTPCtrl          │ │ │
│  │ │ 日志区  │ │      │  │ ConfigParser      │ │ │
│  │ └────────┘ │      │  │ ConfigStore(TOML) │ │ │
│  │ │SFTP双栏 │ │      │  └─────────┬─────────┘ │ │
│  │ └────────┘ │      └────────────┼────────────┘ │
│  └────────────┘                   │ spawn         │
└───────────────────────────────────┼───────────────┘
                    ┌───────────────┼───────────────┐
             ssh -N -L/-R/-D   sftp -b          ssh（测试连接/端口探测）
                    │               │                │
              ┌─────┴───────────────┴───────┐
              │  ~/.ssh/config (只读)         │
              │  ssh-agent / IdentityFile     │
              └───────────────────────────────┘
  日志流向: ForwardCtrl/SFTPCtrl --Events.Emit{source_type,source_id,ts,level,message}--> 前端日志store(内存环形缓冲)
```

## 4. 模块划分

### 4.1 ConfigParser（`config/`）
- 只读解析 `~/.ssh/config`，抽取主机条目字段：`Host / HostName / User / Port / IdentityFile / ProxyJump`。
- 生成主机列表供前端下拉选择。
- **高级语法处理（v1 定案）**：
  - `Include`：**展开**为主文件 + 被引入文件的合并条目（OpenSSH 语义）；解析用成熟库（非手写正则）。
  - `Match block`：**按不匹配处理**（即 ignored，不采纳其中 HOST/OPTION），仅解析全局/无 Match 的条目。
  - 重复 `Host` / 同名条目：按 OpenSSH **每选项 first-match-wins**。
  - 通配条目（`Host *`、`Host 10.*` 等）：**不进下拉可选项**，仅作为 config 解析上下文，不生成 UI 主机条目。
- 不写回 config。
- 解析失败 → 返回结构化错误。

### 4.2 ConfigStore（`config/store.go`）
- 读写应用自身配置 `~/.config/sshkit/sshkit.toml`（Windows 用 `%APPDATA%`）。
- 结构（TOML）：
```toml
# ===== 应用级 =====
[app]
auto_reconnect_default = true

# ===== 转发规则列表 =====
# 字段语义（统一用"监听侧/目标侧"，避免 L/R 方向混淆）：
#   listen_* = 监听侧。local(-L)为本地监听; remote(-R)为远程监听; dynamic(-D)为本地SOCKS监听。
#   target_* = 目标侧。local(-L)为远程目标; remote(-R)为本地目标; dynamic(-D)无目标(忽略 target_*)。
[[tunnels]]
id = "uuid"
name = "prod-db"
mode = "local"                  # local | remote | dynamic
host = "prod-db"                # 对应 ssh config 中的 Host 别名；裸 host 规则为原始 IP/域名
listen_bind = "127.0.0.1"
listen_port = 5432
target_host = "127.0.0.1"
target_port = 5432
proxy_jump = ""                 # 可选, 非空则追加 -J（跳板机别名）。与 config 中 ProxyJump 叠加生效（config 的恒生效，此处为追加）
auto_reconnect = true
enabled = false
# 以下仅"裸 host 规则"（host 不在 config 中）需要，来自导入命令的 -p/-l；config 内别名规则留空即可
# user = "deploy"
# port = 22

# ===== SFTP 最近连接（用于回填表单，限 N 条按时间倒序）=====
[[recent_sftp]]
host = "prod-db"
remote_dir = "~/logs"
local_dir = "/home/user/downloads"
ts = "2026-08-25T10:00:00Z"
```
- 使用 `BurntSushi/toml` 库。

### 4.3 ForwardCtrl（`forward/`）
- 每条 `tunnel` 规则映射一个受管的 `ssh -N` 子进程，由 goroutine 托管。
- 命令拼接（`listen` 在前表示监听侧，`target` 在后表示目标侧；host 来自 `Host` 别名；`-o ConnectTimeout=10` 用于建连超时，`-o BatchMode=yes` 用于无 TTY 下关闭交互式认证）：
  - `local`:   `ssh -N -o ConnectTimeout=10 -o BatchMode=yes -L <listen_bind>:<listen_port>:<target_host>:<target_port> <host>`
  - `remote`:  `ssh -N -o ConnectTimeout=10 -o BatchMode=yes -R <listen_bind>:<listen_port>:<target_host>:<target_port> <host>`
  - `dynamic`: `ssh -N -o ConnectTimeout=10 -o BatchMode=yes -D <listen_bind>:<listen_port> <host>`
  - 若 `proxy_jump` 非空 → 追加 `-J <proxy_jump>`（紧跟上述参数之后、`<host>` 之前）。
  - 全部通过 `exec.Command` 参数数组传入，**禁止 shell 拼接**。
  - `host` 别名做白名单校验（`^[A-Za-z0-9._-]+$`），非法则拒绝启动，防止参数注入。
- **生命周期状态机**：`stopped → connecting → connected → error →(重连)→ connecting`。
  - **`connected` 判定（按模式）**：
    - `local`/`dynamic`：用 `net.Dial` 探测本地监听端口（监听 socket 在远端连接建立后打开，是可靠的代理信号），同时进程存活且无认证错误。
    - `remote`：无本地可探端口，采用"进程存活 + 无认证错误（stderr 为空）"启发式。
- **自动重连**：指数退避（**规范值 1s/2s/4s…上限 30s**），`auto_reconnect` 关闭则不重连。重连等待期间用户停止 → 取消 pending 重连。
- **状态推送 / 日志**：Wails `Events.Emit`，payload 为 `{source_type, source_id, ts, level, message}`。`source_type ∈ tunnel|sftp`，`level ∈ info/warn/error`。转发的 `source_id` = tunnel 规则 id，SFTP 的 `source_id` = 操作 id。`message` 含状态变化与原始 ssh/sftp stderr。前端日志 store 按 `source_type`/`source_id`/`level` 过滤。
- **停止**：平台抽象 —— Unix：先发 SIGINT，grace 期（默认 5s）后 SIGKILL；Windows：`TerminateProcess`（或隐藏控制台 + CTRL_BREAK 事件）。`-o ConnectTimeout` 用于建连超时。
- **应用退出清理**：Wails `OnShutdown` 统一终止并回收全部受管子进程，避免 `ssh.exe` 变孤儿长期占用端口。
- **端口占用预检**：
  - `local`/`dynamic`：启动前 `net.Listen`（指定 bind 地址）探测本地监听端口是否被占用；`target` 侧端口不做预检。探测后到 spawn 存在 TOCTOU 窗口，由 ssh 启动后 stderr 的"Address already in use"再一次性兜底报错。
  - `remote`：listen 在远端无法本地探测，改为**规则级去重**，键为 `(host, listen_bind, listen_port)`（仅同 host 同端口才冲突）。
- **运行中超时**：存在"强杀超时"（grace 期 5s 见上）；规则运行中编辑（改端口）→ 视为先停后重启；backoff 等待中停止 → 取消重连（见状态机）。

### 4.4 SFTPCtrl（`sftp/`）
- 基于系统 `sftp` 二进制 + 批处理模式 `sftp -b`。
- **核心机制（v1 定案）：每个文件操作 = 一个独立的 sftp 进程**（单命令批处理），按子进程 exit code 判定 done/error。不采用"多命令批处理"。
  - 理由：OpenSSH `sftp -b` 在**任一命令失败即中止整批**（除非命令前缀 `-`），且批处理模式不产生逐文件事件流，多命令批处理无法给出文件级完成状态。
  - 代价：每文件一次 SSH 握手，与 v1"无字节级进度"定位一致，可接受。
- **批处理脚本送达**：统一用临时文件（权限 0600，用后清理），不用 stdin（Windows 无 `/dev/fd` 进程替换）。
- **能力**（每项 = 一次独立调用）：
  - `ls`（列目录）→ **规定用 `ls -l`**（行式、脚本可解析）+ 解析规则
  - `get`（下载）/ `put`（上传）→ **绝对路径形式**：`get /remote/path /local/path`、`put /local/path /remote/path`
  - `rm`（删除）/ `rename`（重命名，正名，`mv` 为别名）/ `mkdir`（新建文件夹）
  - 本地文件已存在时的覆盖策略（覆盖 / 跳过——初版默认覆盖，UI 提示）
- **路径转义**：批处理命令内对含空格/引号/换行的路径做 sftp 引号包裹转义。
- **进度报量（v1 定位）**：`sftp -b` 无内置字节级进度，v1 只能给**文件级完成状态**（每文件 done/error + 队列整体完成数），UI 以文件为单位反馈，**不是平滑进度条**。字节级百分比条标记为 v1.1 增强（届时切换交互式 stdin 驱动 + 解析回显，或改用 Go 库 + SFTP 扩展实现）。
- **传输队列**：后台队列管理多个上传/下载任务，支持**取消**（杀进程），不支持文件中途暂停（v1 不可实现）。
- **无状态说明**：每个操作独立连接、无会话复用，频繁浏览目录的开销由 UI 加载态缓解（明确承认此取舍）。

### 4.5 导入命令（`import/`）
- 解析粘贴的 `ssh -L/-R/-D ...` 命令行，提取参数批量创建 tunnel 规则。
- **解析规格**：
  - `host` → 取自命令行末尾、且在 config 中有 Host 别名的 `<host>`；若为**不在 config 中的裸 IP/域名**，需同命令内携带 `-p`/`-l` 等 → 落到规则 `host`(原始地址) + `user`/`port` 字段，标注为"裸 host 规则"（其 User/Port 已存于规则，重启可直接恢复；config 内别名规则这些字段留空）。
  - `-L/-R/-D` → 识别 `bind:port:host:port` 四段形式，映射到 `listen_*`/`target_*`；`-D` 仅 `bind:port`。
  - `-j/-J`(ProxyJump) → 映射到规则的 `proxy_jump` 字段。
  - `-p`/`-l` → 映射规则 `port`/`user`（仅用于裸 host 规则）。
  - 同命令多个 `-L`/`-R`/`-D` → 拆分为多条规则。
  - 与既有规则 `listen_port`/`(host,bind,port)` 冲突 → 拒绝并提示，不静默覆盖。
  - 校验失败（未知标志、非法 host、参数段数不符）→ 返回结构化错误。
- **注入防护**：导入必须经 tokenizer 解析 + 标志白名单 + host 校验后**重建** `exec.Command` 参数，**绝不重放原始字符串**。

### 4.6 UI（`frontend/`，Vue 3 + Wails）

**整体骨架：左侧模块切换 + 右侧主工作区跟随**（比三标签更轻）。

- **顶栏**：标题 + 全局操作按钮：主机选择器（来自 config 下拉）、新建转发、导入命令。
- **左侧导航栏**：只做「端口转发 / SFTP」两个模块切换（很轻）。顶部留出工具栏。
- **右侧主工作区**：随左侧所选渲染对应视图。

**转发视图（右侧）= 规则列表（左） + 日志区（右，常驻可折叠）**
```
┌───────────────────────────────┬──────────────────────────────┐
│ 转发规则列表                    │ 运行日志区(常驻,可折叠)          │
│ ┌──────────────────────────┐   │ ● 12:01:03 prod-db connecting│
│ │ ● prod-db  5432  运行中  │   │ ● 12:01:04 prod-db connected │
│ │ ○ staging  8080  已停止  │   │ ● 12:01:07 staging reconnect │
│ │ ● bastion  1080 SOCKS 运行│   │ ● 12:01:09 prod-db error     │
│ └──────────────────────────┘   │ (最新在上; 按规则/等级过滤;清空)│
│ [+ 新建规则]  [+ 导入]          │ ▏▏▏ 内存环形缓冲(默认1000条)    │
└───────────────────────────────┴──────────────────────────────┘
```
- **规则列表**：卡片，每行显示 名称 / 模式 / 端口 / 状态指示灯(绿-运行,灰-停止,红-错误) / 启停开关；点击行可展开该规则配置，并可**过滤出仅该规则的日志流**（等价于日志区按 `source_type==tunnel` + `source_id==该规则` 过滤）。
- **日志区**：右侧常驻，实时滚动显示**全部转发规则**的运行日志（时间戳 + 来源 + 级别 + 信息），支持按来源/级别过滤、清空、自动滚动到最新。

**SFTP 视图（右侧）= 双栏文件浏览器 + 底部传输队列**
```
┌──────────────────────────────┬──────────────────────────────┐
│ 本地文件树                    │ 远程文件树                      │
│ (浏览/选择)                   │ (上传/下载/删除/重命名/新建)      │
└──────────────────────────────┴──────────────────────────────┘
┌──────────────────────────────────────────────────────────────┐
│ 传输队列: 文件名 + 状态(排队/处理中/完成/失败)                    │
└──────────────────────────────────────────────────────────────┘
```

**日志一等公民（`frontend/src/stores/logs`）**
- 前端维护一个 `logs` store，接收后端 `Events.Emit{source_type, source_id, ts, level, message}` 事件。
- 用**内存环形缓冲**（默认 1000 条，新进覆盖最旧），应用关闭即清空（**不落盘**）。
- 转发与 SFTP 事件共用同一 store（`source_type` 区分隧道/ SFTP，`source_id` 区分具体对象），便于统一排查。
- 提供：按来源过滤（source_type / source_id）、按级别过滤、清空、自动滚动。日志条数上限可配置（默认 1000）。

## 5. 数据流

1. 启动：ConfigParser 读 `~/.ssh/config` → 填充主机列表；ConfigStore 读 TOML → 恢复规则。
2. **自动启动**：若规则 `enabled=true` → ForwardCtrl 于应用启动后自动拉起（**明确 auto-start 行为**）。
3. 用户新建/导入规则 → ConfigStore 写入 TOML。
4. 用户点"启动"/"停止" → ForwardCtrl spawn/终止 ssh 子进程 → 以 `{source_type:"tunnel", source_id, ts, level, message}` 事件推前端，前端写入日志 store（内存环形缓冲）并在状态机/日志区刷新；停止规则时**回写 `enabled=false`** 到 TOML。
5. SFTP：用户选主机+路径 → SFTPCtrl 拼 sftp -b 脚本 → spawn → `ls -l`/done/error 等结果 + 日志事件（`source_type:"sftp"`）推前端（目录列表渲染到远程树，事件写入同一日志 store）。
6. **日志统一入口**：所有 ForwardCtrl/SFTPCtrl 行为都以 `Events.Emit{source_type, source_id, ts, level, message}` 进同一前端 store（环形缓冲），UI 日志区实时渲染。

## 6. 错误处理

- 转发/SFTP 子进程 `stderr` 捕获 → 结构化错误信息，Wails Event 推前端展示。
- **端口冲突**：`local`/`dynamic` 本地端口占用（`net.Listen` 探测 + `Address already in use` 兜底）；`remote` 按 `(host,bind,port)` 去重 → 启动前拦截并提示。
- **config 解析失败**、**ssh / sftp 不在 PATH**（均 `exec.LookPath`）→ 启动时检测并提示。
- **认证失败**：GUI 无 TTY，密码/keyboard-interactive 不可用 → ssh/sftp 统一加 `-o BatchMode=yes`，将认证失败 stderr 归一化为「该主机未配置密钥/agent 认证」提示。
- 子进程非零退出 → 更新状态为 error 并带退出信息。
- **日志**：`Events.Emit` 事件前台进内存环形缓冲（默认 1000 条）；若缓冲溢出（应用超长运行）仅覆盖最旧日志，不影响功能。
## 7. 测试策略

- **Go 单元测试**：
  - `configparser`：解析 fixtures（含通配符/Match/Include/多用户/ProxyJump 条目）。
  - `forwardctrl`：mock 版 `ssh` 命令验证参数拼接（含 `-o BatchMode`）、状态机生命周期；验证 `Events.Emit{source_type,source_id,ts,level,message}` 日志事件载荷。
  - `sftpctrl`：mock 版 `sftp` 进程 + fixture 批处理输出解析测试（`ls -l`、done/error 判定、日志事件）。
- **E2E（较晚阶段）**：本地 sshd / docker 容器，验证真实 `-L/-R/-D` 与 SFTP 传输。
- **前端**：Vue 组件（状态显示、规则表单、日志 store 环形缓冲/过滤）在后续补充，其中日志 store（环形缓冲 + 按规则/级别过滤）为**必测**。

## 8. 明确不做（YAGNI）

- 不含 SSH 终端。
- 不做 SFTP 远程编辑。
- 不做加密 vault / 凭据存储（依赖系统 OpenSSH）。
- 不做主/从（master/slave）连接复用（初版每个规则独立连接、每 SFTP 操作独立连接）。
- 不做 X11 转发、远程桌面等高级特性。
- 不做字节级传输进度条（v1.1 再评估）。
