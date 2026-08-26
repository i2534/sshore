# sshkit 项目审查报告

- **日期**: 2026-08-26
- **范围**: 全项目（Go 后端 / Vue 前端 / 测试 / 工具链 / 文档 / 仓库卫生）
- **方法**: 三路并行深度代码审查 + 机械验证
- **机械验证结果**: `go vet ./...` ✅ 通过 · `go test ./...` ✅ 全部通过 · `npx vitest run` ✅ 3/3 通过

> **修复进度（2026-08-26）**:
> - 第一轮: **H1–H6 + M1 + M2** 已修复（见文末「已修复项」）
> - 第二轮: **M3–M6、M6b、M8–M11 + 工程化（CI/make e2e/make ci/npm test 脚本）** 已修复
> - 待办: M7 死代码处置（接线 or 删除，待决策）、L1–L7/L9–L11 打磨项、LICENSE（待选择）、自动重连实现（H3 关联）

---

## 一、做得好的地方

- 架构清晰：internal 按职责分包（config / forward / sftp / importer / osutil），边界合理
- 无密码存储：认证完全复用系统 OpenSSH（key/agent/config），无凭据泄露面
- `.gitignore` 正确：根目录二进制、build/bin、.omo 均未入库；package-lock.json 已入库
- README 与实现基本一致，测试命令可直接运行
- 现有测试全绿，osutil 覆盖率 88.9%

---

## 二、高优先级问题（正确性 / 数据安全）

### H1. 隧道配置的 User/Port 字段被静默忽略
- **位置**: `internal/forward/ctrl.go` BuildArgs
- **现象**: BuildArgs 从不输出 `-p <port>` / `-l <user>`。导入器会从 `ssh -p 2222 -l bob host -L ...` 正确解析出 Port/User 并存入规则，但启动时完全不生效——实际连到默认端口 22、默认用户。
- **影响**: 所有使用非标端口/指定用户的规则行为静默错误。
- **修复**: BuildArgs 中按需追加 `-p t.Port`、`-l t.User`（仅当非零/非空时）。

### H2. 损坏的配置文件会被静默清空
- **位置**: `app.go` startup() + `internal/config/store.go`
- **现象**: LoadConfig 解析失败 → a.cfg 保持 nil → Init() 用空对象填充 → 下次保存直接以空配置覆盖原文件。
- **影响**: 用户所有隧道规则丢失，且无任何错误提示。
- **修复**: 加载失败时保留原始文件（备份为 .bak）、向上抛错、前端展示错误，禁止用空配置覆盖。

### H3. ssh 进程退出无人监控（幽灵"已连接"状态）
- **位置**: `internal/forward/ctrl.go`
- **现象**: spawn 后立即标记 StateConnected，没有 goroutine 监控进程退出（osutil 的 Wait()/done channel 从未被消费）。认证失败/网络掉线后 UI 永远显示已连接。
- **关联**: 设计文档 §4.3 承诺的自动重连（指数退避）未实现；`Tunnel.AutoReconnect`、`AppSettings.AutoReconnectDefault` 是死字段。
- **修复**: 每个 spawn 启动监控 goroutine 读 proc.Wait() → 进程退出时发 error 事件 + 状态迁移到 Error；实现 AutoReconnect 重连逻辑。

### H4. Start 失败路径泄漏孤儿 ssh 进程
- **位置**: `internal/forward/ctrl.go`
- **现象**: 对已在运行的隧道再次 Start → 端口预检失败 → setState(Error) 直接**替换** map 中的旧 process 条目 → 正在运行的进程句柄丢失，永远无法 Stop，端口持续被占。
- **修复**: setState 替换条目前先 Kill 旧 proc；或 Start 入口对非 Stopped 状态先执行 Stop。

### H5. 传输失败永远显示"处理中"
- **位置**: `frontend/src/views/SftpView.vue` download()/upload() 的 catch 分支
- **现象**: SftpGet/SftpPut 抛错时只写日志，t.status 不更新；TransferQueue 的"失败"样式分支是死代码，失败任务永久 tick。
- **修复**: catch 中设置 `t.status='失败'; t.elapsed=…`。

### H6. SFTP 视图错误零反馈
- **位置**: `SftpView.vue` err() 只写 Pinia store；LogPanel 仅挂在 ForwardView
- **现象**: SFTP 页所有删除/上传/下载/重命名失败用户看不到任何提示。
- **修复**: SFTP 视图内联日志面板，或对操作错误加 toast/对话框反馈。

---

## 三、中优先级问题（健壮性 / 竞态 / 安全加固）

### M1. sftp 批处理命令注入
- **位置**: `internal/sftp/ctrl.go` buildBatch/writeBatch
- **现象**: 远程路径未经转义直接拼进 batch 文件（设计文档 §4.4 明确要求引号包裹转义，未做）。含换行的路径可注入第二条命令；sftp 的 `!command` 会执行**本地 shell**。
- **缓解因素**: 实际路径多来自 ls 解析（换行会被拆行过滤），主要暴露面是用户手输路径与程序化调用。
- **修复**: 校验/拒绝路径中的控制字符，路径加引号转义。

### M2. 数据竞争（Stop 写 state 无锁）
- **位置**: `forward/ctrl.go` Stop() 中 `p.state = StateStopped` 未持 c.mu，而 State() 在 c.mu 下读取。
- **修复**: 统一状态读写都走锁。

### M3. 前端连接竞态
- **位置**: `SftpView.vue` connect()/loadRemote()
- **现象**: await 期间切换 host，旧 promise 返回后 connected=true + loadRemote 用新 host 配旧路径，UI 与后端连接错位。
- **修复**: 捕获 host 快照传入所有 Sftp* 调用，或用请求序号丢弃过期响应。

### M4. sftp 无 ConnectTimeout
- **位置**: `sftp/ctrl.go` Connect()/run()/disconnectLocked
- **现象**: host 不可达时 Wails 调用阻塞约 2 分钟（forward 有 10s 超时，sftp 没有）。
- **修复**: 补 `-o ConnectTimeout=10`。

### M5. 错误处理策略不统一
- RuleCard.toggle / ForwardView doImport / submit / remove 均**无 catch** → 失败落到全局"界面错误"横幅（误导性强且无关闭按钮），且列表状态不同步
- SftpView.loadHosts **无 try/catch** → ListHosts 失败中断整个 onMounted 初始化，本地面板空白（ForwardView 同函数有 catch，不一致）
- **修复**: 统一"操作失败→用户可见反馈"约定；fatal 横幅加 dismiss。

### M6. 切标签丢状态
- **位置**: `App.vue` v-if 销毁重建视图
- **现象**: 连接状态/当前目录/选中项全丢，而后端连接仍存活；`SftpConnected(host)` 绑定已生成但前端从未调用。
- **修复**: `<KeepAlive>` 缓存视图 + 挂载时用 SftpConnected 恢复真实状态。

### M6b. EventsOn('log') 从不退订
- **位置**: `App.vue`
- **现象**: Wails dev 模式 HMR 重挂载后重复注册 → 日志翻倍累积。
- **修复**: 保存 EventsOn 返回的 cancel 函数并在 onUnmounted 调用。

### M7. 死代码 / 未接线的能力
| 项 | 位置 | 说明 |
|---|---|---|
| CheckRemoteConflict | forward/portcheck.go | 已实现+已测但从未被调用（远端端口冲突去重没生效） |
| ResolveViaSSH_G | config/parser.go | 仅测试引用；README 宣称"经 ssh -G 权威解析"实际未接 |
| RecentSFTP | config/store.go | 从未写入/读取 |
| Tunnel.AutoReconnect | config/store.go | 死字段 |
| PickLocalDir / SftpConnected / HomeDir | wailsjs 绑定 | 前端零引用 |

### M8. importer 边界缺陷
- **位置**: `internal/importer/parser.go`
- 带引号参数 `-L "8080:localhost:80"` → 引号进入 token，解析错误
- IPv6 地址（`[::1]:1080`）解析崩溃为 bind="[", port=0
- `user@host` 形式被 ValidateHost 拒绝
- **修复**: Fields 后剥离成对引号；支持 bracketed IPv6；host 含 @ 时拆分 user/host。

### M9. DeleteLocal 无纵深防御
- **位置**: `app.go` DeleteLocal → os.RemoveAll(任意 JS 传入路径)
- **修复**: 至少限制在 Cwd 可达范围内 / 拒绝根目录与 home 本身。（前端有确认对话框，但 API 层无防护）

### M10. AutoStartEnabled 首败即止 + 静默
- **位置**: `app.go` AutoStartEnabled + `main.go` `_ = app.AutoStartEnabled()`
- **现象**: 第一个隧道启动失败（如端口占用）就中止整个循环，后续隧道不启动，且完全无日志。
- **修复**: 收集每个失败继续循环；失败项记日志/事件。

### M11. SaveConfig 非原子写
- **位置**: `config/store.go`
- **现象**: O_TRUNC 直接覆写，写一半崩溃即损坏；Wails 方法并发调用可能交错写。
- **修复**: 写临时文件 + rename 原子替换。

---

## 四、低优先级问题（打磨）

| # | 问题 | 位置 |
|---|---|---|
| L1 | AppDialog 无 focus trap / autofocus / aria-modal；Esc 仅输入框生效 | components/AppDialog.vue |
| L2 | FilePane / LogPanel 无虚拟滚动（大目录、1000 条日志全量渲染 DOM） | FilePane.vue / LogPanel.vue |
| L3 | 传输队列只增不清：完成的无法清除、进行中的无法取消 | TransferQueue.vue |
| L4 | 中英文混杂：按钮中文、placeholder 英文、index.html lang="en"；"filter source_id" 泄漏后端术语 | 多处 |
| L5 | fmtSize 双份、dialog openConfirm 逻辑两个视图各抄一份、'处理中'/'完成' 魔法字符串跨组件硬编码 | FilePane/TransferQueue/SftpView/ForwardView |
| L6 | symlink 条目名带 " -> target"，点击会用错误路径 | sftp/parser.go nameAfterDate |
| L7 | 右键菜单 y 方向无边界 clamp（x 有），窗口底部溢出 | SftpView.vue showMenu |
| L8 | package.json 无 test/lint 脚本；无 eslint/prettier 配置 | frontend/package.json |
| L9 | 每秒 tick 即使无活跃传输也重渲染 TransferQueue | SftpView.vue clockTimer |
| L10 | 端口校验缺失：0/负数/>65535 均可提交到后端 | ForwardView 表单 + CreateTunnel |
| L11 | 无单实例锁：双开应用会同端口抢跑 ssh -N | main.go |

---

## 五、工程化缺口

### 测试覆盖（go test ./... -cover 实测）

| 包 | 覆盖率 | 主要未测内容 |
|---|---|---|
| internal/osutil | 88.9% | Signal 死进程/Kill 幂等等边缘 |
| internal/config | 71.6% | 通配符 Host/Match/Include、损坏 TOML 加载 |
| internal/importer | 66.2% | -R、四段式 bind:port:host:port、-D bind:port、-p/-l、全部错误路径 |
| internal/forward | 49.1% | **Start 成功路径、Stop、OnShutdown、状态机生命周期全未测** |
| 根包 (app.go) | 36.4% | StartTunnel/StopTunnel/AutoStartEnabled/全部 Sftp*/本地文件操作 |
| internal/sftp | 27.3% | **除 batch 构建外全部真实操作未测**（Connect/List/Home/Get/Put/Rename...） |

前端仅 1 个测试文件（logs store 3 用例）；组件/视图零测试，无 @vue/test-utils、无 jsdom。

### CI / E2E / 文档

- **CI 完全没有**：无 .github/workflows
- **E2E 是摆设**：docker-compose.yml 的 PUBLIC_KEY 为占位符跑不起来；test_local.sh 可独立运行但从不调用 sshkit 二进制（只验证 OpenSSH 行为）；Makefile 无 e2e 目标
- Makefile 缺 lint / coverage / e2e 目标
- 设计文档承诺未实现清单：自动重连（§4.3）、net.Dial 连通性探测（§4.3）、导入冲突拒绝（§4.5）、exec.LookPath 启动检查（§6）、batch 路径转义（§4.4）、RecentSFTP 回填（§4.2）、传输取消
- 计划文档 94 个 checkbox 全部未勾选（工作实际已完成）
- 仓库无 LICENSE 文件
- frontend/wailsjs/ 生成文件入库且当前 dirty（每次 wails build 会 churn）

---

## 六、建议修复顺序

1. **H1** User/Port 参数生效 —— 改动小收益高（BuildArgs 两行）
2. **H2** 配置损坏保护 —— 报错而非清空
3. **H3 + H4** forward 生命周期 —— 进程退出监控 + 孤儿泄漏修复 + 补单测（最大正确性债务）
4. **H5 + H6** 前端反馈链 —— 传输失败状态 + SFTP 错误可见化
5. **M1** sftp 批处理转义（安全修复，顺手补 Home() 解析测试）
6. M2–M6 按需推进；M7 死代码要么接线要么删除
7. 工程化：补 CI（vet+test+vitest+wails build）→ make e2e 目标 → LICENSE

---

## 附：本次审查证据

- `go vet ./...` exit 0
- `go test ./...` 6 包全 ok（cached）
- `npx vitest run` Test Files 1 passed / Tests 3 passed
- git ls-files 确认：根二进制/build/bin/.omo 未入库；wailsjs 已入库且 dirty
- 设计/计划文档：docs/superpowers/specs/2026-08-25-sshkit-design.md · plans/2026-08-25-sshkit.md

---

## 附：已修复项（2026-08-26 第一轮，全部 TDD，未提交）

| 项 | 修复内容 | 新增测试 |
|---|---|---|
| H1 | `BuildArgs` 条件追加 `-p <port>` / `-l <user>`（转发参数前） | TestBuildArgsUserAndPort · TestBuildArgsNoUserNoPort |
| H2 | 新增 `loadOrBackupConfig`：解析失败先字节级备份为 `<path>.bak-<unix-ts>` 再以空配置启动；错误经 Init 补发 error 事件（emit 在 startup 之后才可用，已注释说明）；抽出 `config.DefaultAppConfig()` | TestLoadOrBackupConfigCorruptBacksUp · TestLoadOrBackupConfigMissingNoBackup + Init 发事件断言 |
| H3 | spawn 成功后启动 `watchExit` goroutine 消费 `proc.Wait()`：进程自行退出 → 状态迁移 StateError + error 事件；指针身份比较防止 Stop/OnShutdown/条目替换后误报 | TestStartMonitorsProcessExit（真实短命进程验证完整监控链路） |
| H4 | `Start` 入口守卫：已有存活进程直接返回 "tunnel already running"，不再替换 map 条目；同时统一锁纪律（State 读、Stop/OnShutdown 写均走 p.mu），修复数据竞争 | TestStartTwiceRejectsSecond |
| M1 | `quoteArg`：拒绝控制字符路径（防 batch 换行注入 / `!` 本地 shell 执行）+ 双引号包裹并转义 `\` 和 `"`；`buildBatch` 改返回 error，rename 路由进来补上漏网插值点，7 处调用点全部检查错误 | TestBuildBatchQuoting（含 Windows 路径）· TestBuildBatchRejectsControlChars |
| M2 | 随 H4 一并修复：p.state 全部读写纳入 p.mu | `go test -race -count=1` 全绿 |
| H5 | download/upload 的 catch 置 `t.status='失败'` 并冻结 elapsed（t 提升到 try 外声明） | 前端无组件测试设施，以 build+vitest 验证 |
| H6 | SftpView 底部挂载 LogPanel（复用 ForwardView 视觉语言，140px 固定高不挤压文件面板） | 同上 |

**第一轮后全量验证**: `go vet ./...` ✅ · `go test ./... -race -count=1` ✅ 6 包 · `npx vitest run` ✅ · `npm run build` ✅
