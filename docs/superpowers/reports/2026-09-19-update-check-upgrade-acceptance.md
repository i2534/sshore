# 验收报告：检测更新与自升级（2026-09-19）

- 特性：检测更新（阶段 A）+ 应用内下载自升级（阶段 B）
- spec / plan：`docs/superpowers/specs/2026-09-19-update-check-upgrade-design.md`（v3）/ `docs/superpowers/plans/2026-09-19-update-check-upgrade.md`
- 分支：`feat/update-check-upgrade`（BASE `bfe486b`）
- 验收提交：`3c4a068`（真机修复）、`9d88a9c`（假源脚本）、`70e6a6a`（doc 批次）、本报告（验收报告）
- 原始证据目录（gitignored）：`.superpowers/update-2026-09-19/`（截图 shot1–shot6、make ci 日志、Windows 夹具）

## 结论

**DONE_WITH_CONCERNS** —— A/B/D/E/F 全部完成；C（真机）Linux 全链路完成、Windows 完成「脚本层真机 + go-windows 等价 + Defender 取证」，**Windows GUI 全链路（真窗口从 available 点升级）未做**（原因见 §C2.4）。

---

## 0. 交付清单（文件 × 行为）

以 `git diff --name-status bfe486b..70e6a6a` 为准，共 55 个特性文件（不含本报告）：

| 分组 | 文件 | 行为 |
|---|---|---|
| 版本/源/资产 | `internal/update/version.go`、`source.go`、`asset.go` | 版本类别判定与比较；`<source>/releases/latest` 客户端（含 `assets[].size`）；本平台资产与 `checksums.txt` 挑选 + 同源校验 |
| 下载/校验/解包 | `checksum.go`、`download.go`、`extract.go`、`freespace_unix.go`、`freespace_windows.go` | sha256sum 解析与比对；流式下载（进度节流/空闲超时/半截清理）+ 磁盘空间检查；白名单解包（拒遍历/目标同名链接/超限） |
| 计划/脚本/锁 | `plan.go`、`script.go`、`start_unix.go`、`start_windows.go`、`lock.go`、`lock_unix.go`、`lock_windows.go`、`scripts/update.sh`、`scripts/update.cmd` | 替换计划与 pending 判别；go:embed 脚本 + 参数/环境 + 分离启动；ExeDir 排他锁；两平台升级脚本 |
| 服务/接线 | `internal/update/service.go`、`app.go`、`app_update_test.go`、`internal/config/store.go` | 状态机/守卫/自检/调度；4 个配置字段 + Normalize；9 个 Wails 绑定 + App 级 cfg 锁 |
| 前端 | `frontend/src/utils/update.js`、`stores/update.js`、`stores/settings.js`、`components/UpdateSection.vue`、`components/SettingsDialog.vue`、`App.vue` + 4 个测试文件、`frontend/wailsjs/go/main/App.{js,d.ts}`、`models.ts` | 状态文案/按钮矩阵（纯函数）；seq 归约与角标派生；设置页更新区；侧栏角标（单一来源 store 派生）；绑定重新生成 |
| CI | `.github/workflows/ci.yml`、`.gitattributes` | checksums 生成、shellcheck 门禁、Windows 真实 cmd 跑脚本、`go-windows` job；脚本 CRLF/LF 属性 |
| 文档/夹具 | `README.md`、`README.en.md`、`e2e/fake_update_source.py`、spec、plan | 更新与升级说明（中/英）；本地假源端到端夹具 |

---

## A. 整体闸门（make ci）

命令：`make ci`（= `cd frontend && npm run build` + `go vet ./...` + `go test ./... -race -count=1` + `cd frontend && npx vitest run`）。

最终结果（`/tmp/make-ci-final.log`，同时归档到证据目录）：

```
✓ built in 1.16s            # vite build
go vet ./...                # 静默通过
ok  sshore  1.667s
ok  sshore/internal/config 1.331s
...（13 个包全 ok）...
ok  sshore/internal/update 14.570s
ok  sshore/internal/watch  6.820s
Test Files  20 passed (20)
     Tests  285 passed (285)
```

**诚实记录一次失败**：首次 `make ci`（`bash-347`）在 `internal/osutil` 出现
`--- FAIL: TestStartPipesDoesNotBlockWhenStderrUnread ... runner_test.go:259: read |0: file already closed`。
判定为**既有并发 flake，与本特性无关**（该测试不在本次改动面内，master 已存在）：单独 `-run` 连跑 8 次全 ok，全量闸门重跑 3 次全绿。按「只修必要、不改既有语义」原则**未改动该测试**，在此记账。

---

## B. 变异校验（DoD #10）

方法：对每个变体用 edit 临时改一处 → 跑指定测试确认**变红**（`-count=1` 禁缓存）→ `git checkout --` 逐字节还原（sha256 前后相等）。

| # | 变体（改了什么） | 命名测试 | 结果 | 还原 |
|---|---|---|---|---|
| ① | `plan.go`：`Compare(ver, current) > 0` → `>= 0` | `TestIsPendingNameUsesVersionOrder` | **红**：`IsPendingName("sshore.v0.6.0","v0.6.0") = true, want false` | ✅ 逐字节 |
| ② | `update.sh` 第 4 步删掉 pending/备份判别 | `TestScriptRunCleanupKeepsPendingAfterStep4` | **红**：`第 4 步不得删 pending: ... no such file or directory` | ✅ |
| ③ | `service.go` 删 `VerifyFile(plan.Pending, want)` | `TestApplyRejectsTamperedPendingSidecar` | **红**：`哈希不符必须拒绝 apply` | ✅ |
| ④ | `service.go` Check 拒绝分支去掉 `StateApplying` | `TestCheckRejectedWhileDownloadingOrReady` | **红**：`applying 期间必须拒绝检查` | ✅ |
| ⑤ | `update.sh` 第 8 步 `if ! kill -0` → `if false` | `TestScriptRunLaunchFailureRollsBack` | **红**：`新版起不来必须失败, log=` | ✅ |
| ⑥ | `stores/update.js` `BADGE_STATES` 去掉 `"ready"` | `stores/update.test.js` | **红**：2 failed（`ready 状态也亮角标` 等） | ✅ |
| ⑦（额外） | `update.sh` `fail()` 去掉 `args) exit 2`（一律退 3） | `TestScriptRunArgsValidation` | **红**：`参数类失败必须退 2，实际 3` | ✅ |
| ⑧（额外） | `service.go` 空间检查退回固定 `64<<20` | `TestDownloadFailsWhenFreeSpaceBelowAssetSize` | **红**：`Error = "未配置 HTTP 客户端", 应含中文空间不足文案`（说明空间门未拦住） | ✅ |

8/8 全部「变体 → 命名测试 → 红 → 逐字节还原」；工作树在变异结束后 `git status` 干净。

---

## C. 真机验收

### C1. Linux 端到端（完成）

夹具：`e2e/fake_update_source.py 8799 /tmp/upd-www v9.9.9`（本地 HTTP，提供 `/releases/latest`、`/assets/*`、`checksums.txt`）；现场构建 `VERSION=v0.6.0` 旧二进制与 `VERSION=v9.9.9` 新产物（tar.gz + sha256）。隔离 `HOME/XDG_CONFIG_HOME=/tmp/upd-home`，`update_source=http://127.0.0.1:8799`、`update_check_auto=true`、`update_check_interval_hours=0`；应用跑在 Xvfb :9，点击用 XTEST（ctypes，无需 xdotool）。

| 步骤 | 命令/动作 | 观察结果 |
|---|---|---|
| 假源就绪 | `curl /releases/latest` | `{"tag_name":"v9.9.9",...,"assets":[...]}`；`checksums.txt` 与 tar.gz 可下载 |
| 首查 → available | 启动旧版 `/tmp/upd-live/sshore` | 假源访问日志出现 `GET /releases/latest 200`；截图 `shot3.png` 显示「发现新版本 v9.9.9」+「下载更新/跳过此版本」；侧栏设置按钮右上角角标（`shot1/shot3`） |
| 下载 → ready | XTEST 点「下载更新」 | 状态变「v9.9.9 已就绪，点击「重启并升级」生效」(`shot4.png`)；磁盘出现 `sshore.v9.9.9`（12,022,704 B，sha256 `7b4eb4a0…` 与新构建逐字节相等）与 `sshore.v9.9.9.sha256`；无 `.part` 残留 |
| 自定义源二次确认 | 点「重启并升级」 | 弹出确认框「更新源为自定义地址，确认继续？」(`shot5.png`)，符合 spec §10.2 |
| 升级成功 | 确认 OK | `sshore` sha256 = `7b4eb4a0…`（新版）；备份 `sshore.v0.6.0` sha256 = `0276be8e…`（旧版）；`sshore-update.sh`、`sshore-update.log` 消失；`.sshore-update.lock` 保留（符合「不应手动删」）；`xwininfo` 标题从 `SSHore v0.6.0` 变为 **`SSHore v9.9.9`**（`shot6.png`） |

另以 httptest 级夹具复核：`go test ./internal/update/ -run TestDownloadSuccessWritesPendingSidecarAndNoResidue -count=1 -v` → PASS（真实 tar.gz fixture，事件 Seq 钉死 available→downloading→ready，pending 逐字节相等、sidecar 哈希正确、`.part` 已删）。

### C2. Windows VM（部分完成）

VM：VirtualBox `win10`（Windows 10 22H2 19045.6466），`ssh -p 2222 lan@127.0.0.1`，Defender 全开（`RealTimeProtectionEnabled=True`、`IsTamperProtected=True`）。

**C2.1 go-windows 等价（跨平台测试二进制在真 Windows 上跑）**

`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c` 交叉编译后 scp 到 VM 执行：

| 包 | 结果 |
|---|---|
| `internal/update` | **PASS**（62 个顶层用例，含 `TestAcquireExclusiveOnWindows`/`TestReleaseIdempotentOnWindows`） |
| `.`（root，含 app 接线） | **PASS**（补齐 `frontend/src/utils/queue.js` 的 repo 布局后） |
| `internal/config` | **PASS** |

**真机抓到两个真实缺陷（均已修复，见提交 `3c4a068`）：**

1. `internal/update/scripts/update.cmd` 成功路径退出码为 **1**（应为 0）。根因：cmd.exe 一旦发现批处理删掉了自己就立即中止，第 73 行的 `exit /b 0` 根本不会执行。CI 的成功路径 step 断言 `$LASTEXITCODE -eq 0`，因此在真实 windows runner 上 `go-windows` job 会因此**误红**。已改为经典 `(goto) 2>nul & del "%~f0" >nul 2>nul & exit /b 0`（同 VM 上最小复现：朴素 `del` 后 `exit /b` = 1；`(goto)` 惯用法 = 0）。
2. `internal/update/plan_test.go` 的 `TestPlanForDescribeAndDevBackupNames` 硬编码 POSIX 字面量，Windows 下 `filepath.Join` 产出反斜杠 → 该用例必红（同样会让 `go-windows` 红）。已改为用 `filepath.Join` 构造输入与期望。

修复后 VM 复跑：`internal/update` PASS、root PASS、config PASS。
（未逐一在 VM 重跑其余既有包；完整 `go test ./...` 由 CI 的 `go-windows` job 覆盖 —— 本次分支未 push，CI 未实际跑。）

**C2.2 update.cmd 真实 cmd.exe 成功/回滚（修复后复跑）**

成功路径用**未签名的 Go PE**（`sleep 30s` 的 sleeper.exe）当新版本，回滚路径用立刻退出的 `whoami.exe`：

```
SUCCESS PATH   success_exit=0；BACKUP_OK；replaced_size == pending_size；PENDING_CONSUMED；SCRIPT_GONE；LOG_GONE
ROLLBACK PATH  rollback_exit=3；RESULT=fail:launch；PENDING_KEPT；LOG_KEPT；restored_size 恢复为旧文件
CI-style       CI_STYLE_LASTEXITCODE=0；backup=True；newsize==size；pending_left=False；script_left=False；log_left=False
```

（失败/回滚语义与 CI 的两条断言一致；夹具 `win_task19_script_e2e.cmd`、`win_ci_style_ok.ps1` 已归档。）

**C2.3 Defender 证据（默认配置，未拦截）**

- `Get-MpComputerStatus`：`RealTimeProtectionEnabled=True`、`AntivirusEnabled=True`、`IsTamperProtected=True`；`Get-MpPreference`：`DisableRealtimeMonitoring=False`、`MAPSReporting=2`、`SubmitSamplesConsent=1`。
- `MpCmdRun.exe -Scan -ScanType 3 -File <未签名 Go PE 新版本>` → `found no threats`；对 `update.cmd` 同样 `found no threats`。
- `Get-MpThreatDetection` → **空**；Defender 事件日志 1006/1007/1116/1117 → **无条目**。
- 结论：**默认配置下替换未签名二进制未被拦**（未做阈值/控件的对照组，仅此 VM 的一次观察）。

**C2.4 未做：Windows GUI 全链路（诚实记录）**

`docs/.../task-19-brief.md` Step 4 的 1–5（真窗口 available → 下载 → ready → 点升级 → 标题变新版）**未执行**，原因：
1. 本机只有 `i686-w64-mingw32-gcc`，**没有 x86_64 mingw**，无法产出 `windows/amd64` 的 Wails GUI 构建（`windows/386` 有 WOW64 重定向坑且需交互会话）；
2. VM 上以 SSH 启动的 GUI 落在 **Session 0**（`tasklist` 显示 Services 会话），WebView2 建 controller 报 `800700aa`；要上交互会话需 `VBoxManage guestcontrol` 凭据或人工双击；
3. Wails 在 Windows 恒传 `--disable-features=msSmartScreenProtection`，导致 `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` 的 CDP 调试端口**不生效**（技能 §2.9 已记录）；
4. VM 上仅有用户既有的 `sshore.exe`，**不得覆盖**用户文件。

已尝试：VM 侦察（`ver`/`dir`/Defender）、交叉编译并真跑测试二进制、复制真实 PE 跑脚本成功/回滚、`MpCmdRun` 扫描。GUI 全链路依赖 CI 的 `go-windows` job + 人工在交互会话点一次。

### C3. §13.5 的 10 个变体

| # | 变体 | 覆盖/证据 |
|---|---|---|
| 1 | `update_skipped_version`=最新版 | `TestSkipAndClearSkippedTransitions`、`TestInitSelfCheckClassifiesPendingVersusBackup`（PASS） |
| 2 | 缺 `checksums.txt` | `TestPickArchiveAndChecksums`（no-checksum，PASS） |
| 3 | 无本平台资产 | `TestPickArchiveAndChecksums`（no-asset，PASS） |
| 4 | 哈希改一位 | `TestDownloadVerifyFailureLeavesProgramDirClean`（verify-failed + 零残留，PASS） |
| 5 | ExeDir 只读 | **overlay 临时测试**（不入库）`TestZZReadOnlyExeDirVariant` → PASS（io-failed + `Hint=manual-upgrade` + 零残留）；前端 Hint 文案由 `update.test.js`/`updateSection.test.js` 覆盖 |
| 6 | pending/备份判别 | `TestInitSelfCheckClassifiesPendingVersusBackup`、`TestResumePending`（PASS） |
| 7 | 脚本人为失败 | `TestScriptRunSizeMismatchKeepsTarget`、`TestScriptRunWaitTimeoutKeepsEverything`、`TestScriptBackupNotWritable`（PASS） |
| 8 | sidecar 被篡改 | `TestApplyRejectsTamperedPendingSidecar`（PASS） |
| 9 | 两实例同时 apply | `TestApplyLockFailureGivesChineseMessage`、`TestAcquireIsExclusiveWithinProcess`（PASS） |
| 10 | 成功升级后再次启动 | `TestScriptRunSuccessPath` + C1 真机目录/标题断言（PASS） |

`go test ./internal/update/ -count=1 -v`：**62 PASS / 0 FAIL / 0 SKIP**（Linux）。

---

## D. 13 项 doc 批次核对表

| # | 项 | 状态 | 落点 |
|---|---|---|---|
| 1 | spec F3 回写 | ✅ | spec §4 F3 = **GO**（Session 0/1 各一次 RENAME_OK + WRITE_ORIGINAL_OK，副本 SHA256 一致） |
| 2 | spec §5.1 措辞 | ✅ | extract 改为「**仅目标同名条目**的 symlink/hardlink 被拒、无关链接跳过」；`Download` → `(c *Client) Download(ctx ...)`、`Latest` → 方法签名；`PlanFor`/script.go 与实现核对一致 |
| 3 | plan Task 6 import 删 `"syscall"` | ✅ | 主 `script.go` import 块删除；平台文件的两个 `syscall` 保留 |
| 4 | plan Task 12 `--backup 父目录` 注释 | ✅（已一致） | plan 内**不存在**该注释；spec §13.2 已按 `RESULT=fail:args` 描述 |
| 5 | spec §8.3 `sleep` 措辞 | ✅ | Linux 步 2 从 0.2s 改为 1s（并注明分数 sleep 兼容性） |
| 6 | plan Task 8 `ClearSkipped` | ✅ | 去掉预置 `StateChecking`，直接 `Check`，并写明原因 |
| 7 | plan/spec `Asset.Size` | ✅ | plan 的 Produces/结构体/latestJSON/append 与 spec §5.1 全部补 `Size int64` |
| 8 | plan Task 10 字符集 + `resolveSource` | ✅ | 补 `[0-9A-Za-z.+-]` 白名单；`resolveSource` 改为 `url.Parse` + `isLoopbackHost` 精确比较 |
| 9 | plan Task 12 用例命名/期望 | ✅ | `TestScriptCleanupKeepsPending` → `TestScriptRunCleanupKeepsPendingAfterStep4`；断言 `"OLD\n"`；`-run TestScriptRunCleanup`。（plan 中 `append` argv 无该缺陷，无需改） |
| 10 | spec §12.1 表 | ✅ | `verify-failed/io-failed` 行补「检查更新」；§6/§12.1 间隔改 12/24/48/168/关闭 |
| 11 | plan Task 16 角标同源 | ✅ | Step 1 补「条件只写 `BADGE_STATES`、App.vue 只读 `badgeVisible`」说明 |
| 12 | README.en.md 英文节 | ✅ | 新增 `## Updates & upgrades`（源/校验/跳过/升级流程/文件表/隐私/手动升级/AV），命令与事实与 `README.md` 一致 |
| 13 | shellcheck/root 前提 | ✅ | spec §14.8 补 CI 的 `shellcheck -s sh` 门禁与非 root 前提引用 |

---

## E. micro-follow-up（Task 14 评审 M3）

`frontend/src/stores/update.js` 的 `stopListening()` 删除 `EventsOff(UPDATE_STATE_EVENT, UPDATE_PROGRESS_EVENT)` 兜底（同时删除已不再使用的 `EventsOff` 导入），只用 `EventsOn` 返回的退订函数。

验证：`npx vitest run src/stores/update.test.js` → **29 passed**；`npx vitest run` → **20 files / 285 tests passed**。

---

## F. DoD 对照（spec §14）

| # | 要求 | 结论 |
|---|---|---|
| 1 | `make ci` 全绿 | ✅（vet 静默、13 包 ok、vitest 285 passed；一次 osutil 既有 flake 已记账） |
| 2 | `go-windows` job 全绿 | ⚠️ 等价验证通过（VM 上 update/root/config 三包 PASS + scripts 真跑）；**完整 CI job 未实际运行**（分支未 push），且真机抓到并修掉两处会让它变红的缺陷 |
| 3 | Wails 绑定重新生成/构建/vitest | ✅（Task 11；`make ci` 的 `npm run build` 与 vitest 通过） |
| 4 | §13.2 脚本真跑（Linux + Windows） | ✅ Linux 全绿；Windows 真实 cmd.exe 成功/回滚均验证 |
| 5 | §13.4 端到端（Linux + Windows VM） | ⚠️ Linux 全链路 ✅；**Windows GUI 全链路未做**（§C2.4） |
| 6 | Defender 无拦截 | ✅ 默认配置一次观察：扫描 no threats、无检测、无 1006/1007/1116/1117 事件 |
| 7 | §13.5 10 变体 | ✅ 见 §C3（1 条用 overlay 临时测试验证，未入库） |
| 8 | shellcheck + 脚本禁忌串 + 无 `\r` | ⚠️ 禁忌串/CRLF 由 `TestScriptBytesAreSafeAndLFOnly` 与人工检查通过；**本机未装 shellcheck，未本地执行**（CI 强制门禁） |
| 9 | README 中英「更新与升级」 | ✅ |
| 10 | 6 条变异校验 | ✅ 6/6（另加 2 条，共 8/8，见 §B） |

---

## G. 已知限制（如实记录）

1. **AV/EDR**：仅在 Windows Defender 默认配置下验证「不拦截」；第三方 EDR、企业策略、受控文件夹访问（CFA）、镜像/只读目录等未验证。根因解法是代码签名（spec §10.3）。
2. **非 release 不自动检查**：`Version` 为 `dev`/describe 串时自动检查被门卫跳过（设计如此，手动可用）；本报告用 `VERSION=v0.6.0` 的干净构建才让自动检查发生。
3. **checksums 只防损坏不防篡改**：`checksums.txt` 与压缩包同源，挡不住源被攻破 / 上游账户被盗（spec §10.1）。
4. **Windows GUI 全链路未做**：见 §C2.4（无 amd64 mingw、Session 0/WebView2 限制、不得覆盖用户 exe）。
5. **`internal/osutil` 一个既有 flake**：`TestStartPipesDoesNotBlockWhenStderrUnread` 在高并发全量跑时偶发 `read |0: file already closed`；非本次改动面，未修。
6. **脚本存活探测依赖 util-linux `setsid` 的非 fork exec 语义**（spec §13.2/README）；busybox/sysvinit 变体可能误判，已在脚本注释与 spec 写明。
7. **§13.5 变体 5（ExeDir 只读）** 用 `go test -overlay` 临时测试验证，未把该测试入库（前端 Hint 文案有单测）。
8. **Windows 其余包的测试**未在 VM 逐一重跑（只跑了改动相关的 update/root/config）；完整矩阵依赖 CI 的 `go-windows` job。
9. **本机 shellcheck 未安装**，DoD #8 的静态检查只在 CI 执行。
