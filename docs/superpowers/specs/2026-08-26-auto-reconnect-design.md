# 自动重连（Auto-Reconnect）设计

- **日期**: 2026-08-26
- **状态**: 已评审（自审 6 项修正 + 独立复审 2 blocker/2 major/5 minor 全部采纳）
- **关联**: 主设计文档 §4.3（指数退避承诺）、审查报告 H3（退出监控已就位，重连未实现）
- **已确认决策**: 无限退避（封顶 30s）｜仅意外退出触发｜新状态 + UI 展示

---

## 1. 目标

`Enabled` 且 `AutoReconnect=true` 的隧道在 ssh 进程**意外退出**时自动重连，
指数退避直至成功或用户手动停止。手动停止、应用退出、启动失败均不触发。

## 2. 状态机扩展（internal/forward/ctrl.go）

```
                 Start(守卫通过)      respawn 尝试成功
   stopped ────────────────▶ connecting ──▶ connected ◀────┐
                               │               │            │
                         spawn 失败            │ 意外退出     │
                               ▼               │ AutoReconnect=false
                             error             ▼            │
                                               │       [回到 connected]
                             AutoReconnect=true ▼            
                                          reconnecting ──┘
                              退避到点：trySpawn ┐
                                成功 → 上行回 connected
                                失败 → 计数+1，留在 reconnecting 继续退避

  任意运行态 ──Stop/OnShutdown/删除──▶ stopped（同时 close cancel）
```

- `State` 枚举新增 `StateReconnecting`（序列化值 `"reconnecting"`）。
- 启动失败（端口占用/非法 host 等）**不进入**重连循环——那是配置错误，
  无限重试只会掩盖问题并刷屏（已确认决策 #2）。
- 重连期间的 spawn 失败不同于首启失败：它计入失败次数、加深退避，
  不跳出循环（瞬态故障自愈，已确认决策 #1 的组成部分）。

## 3. 重连循环机制

### 3.1 入口

现 `watchExit`（H3 引入的进程监控 goroutine）在判定"意外退出"后：

- `!entry.autoReconnect` → 现状：`StateError` + error 事件；
- 否则 → 置 `StateReconnecting` 并进入 `respawnLoop(entry, t)`。

### 3.2 退避参数

| 参数 | 值 | 说明 |
|---|---|---|
| 初始延迟 | 1s | |
| 递增 | ×2 | 1s→2s→4s→8s→16s→30s |
| 上限 | 30s | 第 6 次起固定 30s |
| 次数上限 | 无限 | 直到成功 / 用户 Stop / 应用退出 |

### 3.3 单次尝试

抽出 `trySpawn(t config.Tunnel) (*osutil.Process, error)`：
现 `Start` 的端口预检 + spawn + 参数构建逻辑下沉为内部方法，
公开 `Start` = 守卫检查 + `trySpawn`；重连循环直接调 `trySpawn`
（绕过"already running"对外守卫——自己重启自己是合法的，H4 守卫只防外部重复调用）。

- 尝试失败 → 计入失败次数，继续下一轮退避（端口被抢等瞬态场景自愈）。
- 尝试成功 → 经 **post-spawn 终检闸门**（见 §3.5）后状态回 `StateConnected`，
  按 §3.4 的稳定阈值规则处理计数，发 `info` 事件 `"reconnected"`。

### 3.4 防抖动（关键修正）

若"计数在成功时清零"，一个每 2 秒崩溃一次的 flapping 隧道会永远以 1s 间隔
重试——30s 封顶被完全绕过，形成重试风暴。因此：

> **连续在线时长 ≥ 60s 才清零失败计数**；不足 60s 再次断开，
> 沿用累计计数继续退避。

实现：entry 记录 `lastStableAt`（进入 connected 的时刻）；watchExit 触发时
`now - lastStableAt >= 60s` 才重置计数，否则保留并递增。

### 3.5 取消与快照语义

- `process` 结构体新增字段：
  - `cancel chan struct{}` —— spawn 成功时创建；`Stop`/`OnShutdown` close；
  - `tunnel config.Tunnel` —— spawn 时的**配置快照**（含 AutoReconnect）。
    重连一律基于快照重建参数：用户改配置必须走 UpdateTunnel（其现状会
    stop + enabled=false），不存在循环读取半更新配置的窗口。
- **状态写入原语（关键约束）**：重连循环内的所有状态迁移必须**原地变更**
  （c.mu + p.mu 下改写 `entry.state` 等字段），**严禁调用 setState**——
  现 setState 会以全新 `&process{state:s}` 整体替换条目，抹掉 proc/cancel/
  快照/lastStableAt。setState 仅保留给外部 Start 的失败路径（即"替换语义"
  本身）。
- **post-spawn 终检闸门（双 blocker 修复）**：`trySpawn` 返回成功后，在
  c.mu + p.mu 内一次性复核，任一不满足则 **Kill 刚 spawn 的进程**并以
  对应终态退出循环：
  1. 身份仍匹配（`c.procs[id] == entry`）——否则外部 Start 已介入，
     自己的进程若存活会成为孤儿（不可 Stop、端口泄漏）；
  2. cancel 未关闭且 `entry.state == StateReconnecting`——Stop/OnShutdown
     是原地变更（指针不变），仅靠身份校验无法识别"已被停止"，缺失此检查
     会导致用户停止后隧道原地复活。
  该闸门同时封堵两个竞态窗口：外部 Start 并发插入的 TOCTOU 双 spawn，
  与 Stop-during-spawn 的复活窗口。
- 循环每个睡眠与尝试前同样 `select` 检查 cancel。
- 身份校验沿用 H3 的指针比较模式；反之，外部 Start 在重连等待期介入时
  会替换条目 → 旧循环经上述闸门自行退出（取代语义，有专项测试覆盖）。
- 删除规则路径：`DeleteTunnel` 已先调 `Stop` → 取消自动发生，无泄漏。

### 3.6 事件流

| 时机 | 级别 | 消息 |
|---|---|---|
| 进入重连 | warn | `连接断开（{classifyError 结果}），{delay}s 后进行第 {N} 次重连` |
| 每次实际尝试 | info | `第 {N} 次重连中…` |
| 成功恢复 | info | `reconnected（共尝试 {N} 次）` |
| 因停止而退出循环 | info | `已停止重连` |

进入重连的 warn 必须携带 classifyError 的失败原因——否则认证类永久故障
对用户表现为无限的"1s 后重连"而无任何错误线索。

### 3.7 可测试性

`sleep` 函数注入 `Ctrl`（生产用 `time.Sleep` 包装，测试用假时钟），
退避测试零真实等待；fake spawner 支持"第一次进程死、第二次活"脚本化序列。

## 4. 配置语义

- 生效开关：`Tunnel.AutoReconnect`（既有字段，本设计开始真正消费）。
- 默认值：新建规则由前端 `newTunnel()` 给 `true`（现状不变）；
  `AppSettings.AutoReconnectDefault` 保留不动，待将来做设置页再接（YAGNI）。

## 5. 前端呈现（关键前置缺口）

**现状缺陷**：`ListTunnels()` 只含 `enabled` bool，Go 侧 State 枚举从不出口，
前端无从区分 已连接/已断/重连中。本设计补齐：

1. 新增轻量绑定 `TunnelStates() map[string]string`（id → state 字符串），
   `ForwardView.refresh()` 时一并拉取，作为 prop 传给 RuleCard。
2. **实时性**：状态转换发生在两次 refresh 之间，仅靠拉取黄点会"看不见"。
   App.vue 已持有全局 `'log'` 订阅（勿二次 EventsOn），ForwardView 改用
   pinia `logStore.$subscribe` 监听新增条目，过滤 `source_type === 'tunnel'`
   后防抖 300ms 触发一次 refresh——不新增事件通道、不轮询、不重复订阅。
3. `RuleCard.vue` 圆点四态：🟢 connected、🟡 reconnecting（文案「重连中」，
   次数详情看日志面板）、🔴 error、⚪ stopped。不做倒计时轮询（YAGNI）。
4. wailsjs 绑定手工补齐（沿用 M7 模式：App.js / App.d.ts / models.ts 如需）。

## 6. 边界情况

| 场景 | 行为 |
|---|---|
| 重连等待中用户点停止 | cancel → stopped，不再尝试 |
| 重连 spawn 失败（端口被抢等） | 计入失败，继续退避 |
| OnShutdown | 全部取消，进程照旧 kill |
| 秒死循环（配置错但曾成功启动过） | 30s 封顶自然限频 |
| 系统休眠唤醒 | 进程死亡检测天然覆盖 |
| **进程活着但隧道已死**（remote 转发远端不可达、网络分区不断进程） | **明确不在本设计范围**——触发条件是进程退出；此类场景留待将来的连通性探测（spec §4.3 net.Dial 承诺） |
| UpdateTunnel 运行中修改 | 维持现状：stop + enabled=false；用户重新 enable 走正常首启 |

## 7. 测试策略（严格 TDD）

1. fake spawner 脚本化序列（死→死→活）驱动完整重连路径，断言事件序列与最终状态
2. 退避序列断言：注入假时钟记录 sleep 调用序列 == [1s,2s,4s,...,30s,30s]
3. **防抖动**：连接保持 <60s 即再死 → 计数不清零（退避继续加深）；
   保持 ≥60s 后再死 → 计数从 1 重来
4. 取消：等待期 Stop → 立即 stopped 且无后续尝试
5. 取代语义：重连等待期外部 Start 成功 → 旧循环经终检闸门退出，不双跑
6. **post-spawn 终检·孤儿分支**：spawn 成功后条目已被外部 Start 替换 →
   刚 spawn 的进程被 Kill，map 无泄漏条目
7. **post-spawn 终检·复活分支**：trySpawn 执行期间 Stop 关闭 cancel →
   循环不写 connected，最终态 stopped，新进程被 Kill
8. 回归：AutoReconnect=false 意外退出 → StateError（H3 行为不变）
9. DeleteTunnel during backoff 无 goroutine 泄漏（配合 -race）
10. app 层：`TunnelStates` 绑定返回各 id 当前态

## 8. 涉及文件

| 文件 | 改动 |
|---|---|
| internal/forward/ctrl.go | 状态机、respawnLoop、trySpawn 下沉、cancel channel、原地变更原语（核心）；**NewCtrl 增加 sleep 注入参数（签名变更）** |
| internal/forward/ctrl_test.go | 全部新测试 + 既有 NewCtrl 调用点适配 |
| **app.go** | `TunnelStates()` 绑定 + **NewCtrl 调用点适配签名变更** |
| app_test.go | TunnelStates 测试 |
| internal/importer/parser.go | 导入隧道补 `AutoReconnect: true`（与前端 newTunnel 默认对齐） |
| frontend/wailsjs/go/main/{App.js,App.d.ts} (+models.ts 如需) | 手工补绑定 |
| frontend/src/views/ForwardView.vue | refresh 拉 states 并下发 + logStore.$subscribe 防抖刷新 |
| frontend/src/components/RuleCard.vue | 四态圆点 |

预计规模：Go ~280 行（含测试），前端 ~40 行。
