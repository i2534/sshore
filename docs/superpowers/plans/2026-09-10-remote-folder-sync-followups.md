# 远程文件夹同步（后端）后续跟进

> 来源：2026-09-10 远程文件夹同步后端特性（worktree `feat-remote-folder-sync`，HEAD 2a4a52d）。
> 本文件只记录后续工作项与过程留痕，不修改任何已交付代码。

## 一、已批准的后续工作

### 1. SftpConnect / SftpHome 传入空 user 导致连接状态不一致

`app.go` 的 `SftpConnect(host)` 与 `SftpHome(host)` 走 `internal/sftp` 时恒传空 user，而
`rememberUser` 会无条件用该值覆盖该 host 已记住的 user。于是 `Connected(host)` /
`Disconnect(host)` 依据的 socket 可能不是当前实际使用的那一条（同 host 异 user 会算出不同
ControlPath）：UI 连接状态误报；`Disconnect` 可能关不掉真正的 master，留下 ControlMaster
进程。

本特性刻意没有一并修：自然的修法是改 `internal/sftp` 既有方法的语义（`rememberUser`），
而计划的 Global Constraints 明文禁止在本特性里改变 `internal/sftp` 现有方法的语义。

建议修法：让 `rememberUser` 只在 user 非空时才覆盖（这同时修好 `SftpHome`）。

### 2. SyncRuleStat 缺 json tag，绑定类型悬空

`sync.SyncRuleStat` 没有 json tag，`SyncRuleStats` 序列化出 PascalCase 键，而其它绑定都是
snake_case。生成的 `frontend/wailsjs/go/main/App.d.ts` 声明为
`Record<string, sync.SyncRuleStat>`，但 `models.ts` 里没有这个类——wails v2.15.0 不为 map
的 value 结构体类型生成 TS 类型。

当前不会破坏构建（`frontend/` 下无 tsconfig，`vite build` 只转译不做类型检查），但会卡住
后续前端工作。

建议修法：补 json tag，并把 API 改成 wails 能生成的形状（具名结构体或 slice），然后重新
生成绑定。

## 二、计划与最终实现的有意偏离

以下为执行期间由控制者裁定、超出计划更正范围的**有意偏离**，仅作 provenance（过程留痕），
不是待办：

- `executeDeletes` 增加针对「本地已修改」文件的 keep 检查：删除前在锁外重新 stat，本地
  大小/时间与基线不一致则保留不删。
- 执行用户在冲突卡片上裁决的动作（`r.resolved`），而不是对同一状态重跑 `Decide` 再推导。
- `ConfirmDeletes` 先对删除批次做快照，`fetchMeta` 网络往返后在 `r.mu` 下重新校验指纹，
  只删用户确认过的那一批。
- 事件路径为新建条目补写 `Remote*` 元信息，避免下一轮对账重复下载。
- `ent == nil` 且本地已存在 ⇒ 记为 `ActionConflict`（不再保守下载覆盖本地）。
- 区分 `os.Stat` 的「读不到」与「确实不存在」：非 ENOENT 的错误本轮跳过，不落进重下载分支。
- 任何已建立的 watch 失败都按致命处理（不再只在启动期致命）。
- 事件队列边界与对账删除候选循环共用同一个 `inScope` 过滤谓词。
- keep_local 遇到「远端元信息未知」的冲突时删除条目，而不是写入零值基线（避免下一轮回下载
  覆盖用户刚选择保留的文件）。

## 三、已知取舍与延后项

以下延后的小问题值得后续择机复查：

- 死的 `RootGone`/`blockDels` 闸门臂：`align` 在闸门读取 `blockDels` 之前就把它清零，
  该臂实际不可达。
- 轮询盲区：远端大小相同、修改时间落在 mtime 精度之内时，内容变化可能检测不到。
- `e2e/test_local.sh` 硬编码 `/usr/bin/ssh` 与 `/usr/bin/sftp`，ssh 不在该前缀的机器上会
  硬失败（建议改用 `command -v` 解析真实路径）。
- e2e harness 内联 `python3`（只用 stdlib）；按项目规则「执行 Python 必须处于虚拟环境」，
  宜包进 venv。
- 前端测试套件在执行环境未能运行（`frontend/node_modules` 未安装），最终复审需说明前端侧
  未经验证。
