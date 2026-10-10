# 上传目录多套一层（PutTree / PutRecursive 目录语义）修复报告

> **本文件有两轮**：第 1 轮 = 目录语义修复（正文 §1–§7，基线 `ada9030`，结束于 `3cc859a`）；
> 第 2 轮 = 复核轮定点修复（文末「## 复核轮修复（第二轮）」，基线 `3cc859a`，4 条复核意见）。

- 仓库 / 分支：`sshkit` @ `feat/sftp-refresh-persist-connloss`（未切分支、未 push）
- 代码修复提交：`a5a6702`（GoBackend.PutTree）、`2420121`（BatchBackend.PutRecursive）
- 测试可移植性提交：`eb06494`（`putr` 脚本断言的本地路径按 `EvalSymlinks` 后构造，避免 Windows 大小写差异；**无行为变化**）
- 文档提交：`0ceb67f`（本报告首版 + REGRESSION-B/spec/plan 勘误）、`c97445c`、`5e8cec1`（报告元信息短 SHA 补记）
- 基线（第 1 轮修复前）：`ada9030`。**第 1 轮精确清单以 `git log --oneline ada9030..3cc859a` 为准**
  （第 2 轮起于 `3cc859a`，清单见文末小节；写成闭区间是为了不随后续提交漂移）
- 触发证据：`real/REGRESSION-B.md` B-9（真机复现：`updir/` → `/home/lan/acc-b` 落成 `/home/lan/acc-b/updir/updir/a.txt`）
- 第 1 轮未改前端、未改刷新/持久化/自愈代码

---

## 1. 根因

前端两个方向传的都是**最终路径**（这条约定本身是对的）：

```js
// frontend/src/views/SftpView.vue:907
dst: posixJoin(remotePath.value, name)
// frontend/src/views/SftpView.vue:1039
dst: targetDir + '/' + n
```

下载侧（`GetTree`）按最终目录解释 `req.Local`，与前端一致；上传侧两个后端都按**父目录**解释目标参数：

```go
// internal/sftp/gosftp.go:1055（改前）—— 把 req.Remote 当父目录，再追加源目录名
rootRemote := path.Join(req.Remote, filepath.Base(filepath.Clean(req.Local)))
```
```go
// internal/sftp/batch.go（改前）—— OpenSSH 的 put -r 自带同样的父目录约定
return []byte(fmt.Sprintf("put -r %s %s\n", l, r)), nil   // put -r <local> <remoteDir>
```

⇒ `updir/`（含 `a.txt`、`sub/c.txt`）传到 `/t` 时落成 `/t/updir/a.txt`、`/t/updir/sub/c.txt`；
前端队列里显示的 `dst` 是 `/t/updir`，与实际落点不符（多套一层）。旧实现只在
`base(源目录) == 目标目录名` 时看起来「正确」，属于巧合而非契约。

---

## 2. 目标契约（本轮钉死）

**`TransferRequest.Remote`（上传）/ `Local`（下载）一律表示「最终目标目录」**，两个后端、两个方向一致：

| 场景 | 改前落点 | 改后落点 |
|---|---|---|
| `updir/`（`a.txt`、`sub/c.txt`）→ `/t` | `/t/updir/a.txt`、`/t/updir/sub/c.txt` | **`/t/a.txt`、`/t/sub/c.txt`** |
| `updir/` → `/home/lan/acc-b/updir`（前端真实传参） | `/home/lan/acc-b/updir/updir/a.txt` | **`/home/lan/acc-b/updir/a.txt`** |
| 下载 `Remote=/src` → `Local=/d/updir` | 内容直接落 `/d/updir/…`（不变） | 不变（`GetTree` 对外行为零改动） |

合并语义（D11）**不变**：目标目录/子目录已存在则并入，同名文件被覆盖，不清理远端独有的旧文件，
不做整树替换。空目录行为不变：上传侧仍按枚举结果建出空子目录（batch 侧改为显式 `-mkdir`）。

---

## 3. 逐文件改动

### 3.1 `internal/sftp/gosftp.go`（`GoBackend.PutTree`）

- `rootRemote := path.Join(req.Remote, filepath.Base(filepath.Clean(req.Local)))`
  → `rootRemote := path.Clean(req.Remote)`（只做规范化：去尾斜杠、`""`/`"."` → `"."`，不再追加源目录名）。
- 下游**全部**消费同一个 `rootRemote`，因此语义自洽（函数内已逐条写明）：
  - 建目录：`MkdirAll(rootRemote)` + 每个子目录 `path.Join(rootRemote, d)`；
  - 逐文件落点：`path.Join(rootRemote, f.rel)`（`f.rel` 是相对**源根**的 POSIX 路径）；
  - 续传/取消锚点：`partPath` 由 `PartNameRemote(remotePath, req.ID)` 生成，`remotePath` 变了它自然跟随
    （`req.PartPath/Resume` 在 PutTree 入口即被清空，目录项只重试不续传，P3.1）；
  - 并发去重键：`acquireInflight(host, user, "upload", req.Remote, id)` —— 键仍是 `req.Remote`，
    现在它就是最终目录，语义比改前更准（同父目录下两个**不同**最终目录可并行，改前会被误判成同目标）；
  - 进度/失败上报：`newTreeProgress(..., req.Remote, ...)` 用的是请求目标（未变），
    `TreeCounts/CommittedFiles/RemainingFiles` 与 `tp.fail(te.PartPath)` 均不受影响；
  - 错误文案：`TransferError.Op` 仍为 `"sftp put -r"`，`Path` 在 MkdirAll 失败时是 `rootRemote`（新语义下即最终目录）。
- 重试前的清理边界补了显式说明（见 §6）：`cleanupRemoteTreeParts(ctx, s, rootRemote, treeFileTargets(files), req.ID)`
  的 root 现在是**用户可能已有内容的最终目录**，判据必须继续依赖 `isPartForTarget`
  （「目标 basename 前缀 + 本次 id 短前缀」双重命中），绝不能扩大成整棵子树无差别清扫。

### 3.2 `internal/sftp/batch.go`（`BatchBackend.PutRecursive` / `buildBatch("putr", …)`）

- `buildBatch` 的 `putr` 分支不再拼 `put -r`，改为 `buildPutTreeBatch(remote, local)`：
  - 新增 `buildPutTreeBatch` / `putTreeBatchLines`：本地 `filepath.WalkDir` 枚举源树（源根软链先 `EvalSymlinks`；
    跟随文件软链、不递归目录软链，与 `scanLocalTree` 一致；内部临时文件 `IsInternalTemp` 不进清单，D18）；
  - 脚本形状：先 `-mkdir "<最终远端目标>"` 与每个子目录（WalkDir 保证父先于子；`-` 前缀抑制
    「目录已存在」这类**必须容忍**的失败 —— `sftp -b` 下未加前缀的命令失败会中止整批，与 `RemoveRecursive`
    的 `-rm`/`-rmdir` 同一手法），再逐文件 `put "<local>" "<remoteDir>/<相对源根路径>"`；
  - `put` **不加** `-` 前缀：文件级失败必须中止整批并把 stderr 透出，绝不静默漏传；
  - 远端路径一律 `path` 家族（POSIX），本地路径 `filepath` 家族 —— Windows 客户端的反斜杠绝不进远端路径；
  - **不套用** `GoBackend` 的 D16 枚举阈值（20000 文件 / 5s）：batch 没有进度通道，静默截断一次上传
    等于数据丢失且调用方无从察觉（宁可慢，不可少传）。这是与 GoBackend 的一处**有意差异**。
  - 不再依赖「目标 basename 恰好等于源 basename」：源目录名与目标目录名不同也落在最终路径。
- `PutRecursive` 的文档注释按新契约重写；`c.logEvent` 的文案保持 `"sftp put -r …"`（日志串未变，避免扩大面）。

### 3.3 测试（新用例先红后绿 + 受影响旧用例保意图更新）

| 文件 | 改动 |
|---|---|
| `internal/sftp/tree_test.go` | 新增 `TestGoBackendPutTreeLandsIntoFinalDir`（源 `src/` → 目标 `t`，断言 `t/x.txt`、`t/sub/y.txt` 且**不存在** `t/src`）；`TestGoBackendPutTreeFollowsLocalRootSymlink` 目标名改 `out` 并加「不得出现 out/link」；文件头补「目标路径契约」说明；其余 PutTree 用例把旧约定 `Remote: "."` 改为 `Remote: "a"`（源根也是 `a`，模拟生产「updir → <目标>/updir」），**断言与不变量原样保留**（合并/覆盖/不删远端独有/空目录/软链根/取消/降级/诚实计数/清理/哨兵 PartPath 等） |
| `internal/sftp/cleanup_test.go` | `TestGoBackendPutTreeRetryCleansStaleRemoteParts` 改用 `Remote: "a"`（清理 root 即最终目录），并**新增断言**：目标目录里「与失败项同名但无 marker 的普通业务文件」不得被清理逻辑删除/改写；别的 id 的 `.part` 依旧不得误删 |
| `internal/sftp/ctrl_test.go` | `TestBuildBatchPutRecursive` 重写为断言新脚本（`-mkdir` 各级目录 + 逐文件 `put` 到最终路径，且不含 `put -r`）；`TestPutRecursiveRunsSftp` 用真实临时源树，并断言脚本含最终路径 |
| `internal/sftp/e2e_test.go` | `TestTreeE2E`：目标目录**不再预建**（由实现自行建出）、远端独有文件改放 `target/keep.txt`、落点断言全部改为最终路径、嵌套指纹由 `target/a/a` 改为 `target/a`、下载断言语料同步；函数头说明同步 |
| `app_test.go` | `TestSftpOutputBindingsUsableUnderBatch`：递归上传现在会**真实遍历本地源树**，源目录改为真实临时目录（原 `/l/d` 不存在会直接报错）；用例意图（batch 下绑定可用 + Atomic 能力驱动）不变 |

未改：`internal/sftp/api.go`（`TransferRequest` 字段语义未变，仅解释基准变化）、`app.go`（绑定传参未变）、
`frontend/**`（本来就是最终路径约定）。

### 3.4 文档

- `real/REGRESSION-B.md`：B-9 增加「**已在 commit `a5a6702`/`2420121` 修复**」小节（改法、清理边界、验证方式），
  表格行同步标注；并写明 B-4 旧结论依赖 B-2 缺陷产生的远端树，重跑需重新取样。
- `docs/superpowers/specs/2026-09-18-sftp-gosftp-transport-design.md`：D6「参数基准」行由「**目标父目录**」更正为
  「**目标目录本身**」并注明 2026-10-10 修正；D11 补一句「与 OpenSSH 一致」只指**合并规则**、不含 `put -r` 的父目录参数约定。
- `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`：`PutRecursive` 语义注释块、需求表第 11 项、
  §6.3「并入」措辞三处勘误。
- `docs/superpowers/plans/2026-09-16-sftp-p1-selection-batch-drag.md`：Task 9 顶部加勘误块，历史代码块**保留**（不篡改
  已执行计划的历史记录），指向新契约与新实现。

---

## 4. RED → GREEN（原始输出）

### 4.1 RED（新用例 + 修复前源码）

命令（`/tmp/sshkit-red` 是 `HEAD` 的临时 git worktree，仅把两个生产文件回退到修复前版本 `ada9030`；主工作树未被触碰）：

```bash
git worktree add -q --detach /tmp/sshkit-red HEAD
cd /tmp/sshkit-red && git checkout ada9030 -- internal/sftp/gosftp.go internal/sftp/batch.go
GOCACHE=/home/lan/workspace/scripts/sshkit/.gocache \
  go test ./internal/sftp/ -run 'TestGoBackendPutTreeLandsIntoFinalDir|TestBuildBatchPutRecursive' -count=1 -v
```

原始输出：

```
=== RUN   TestBuildBatchPutRecursive
    ctrl_test.go:667: putr batch =
        "put -r \"/tmp/TestBuildBatchPutRecursive2008007475/001\" \"/remote/dir\"\n"
        want
        "-mkdir \"/remote/dir\"\n-mkdir \"/remote/dir/empty\"\n-mkdir \"/remote/dir/sub\"\nput \"/tmp/TestBuildBatchPutRecursive2008007475/001/sub/y.txt\" \"/remote/dir/sub/y.txt\"\nput \"/tmp/TestBuildBatchPutRecursive2008007475/001/x.txt\" \"/remote/dir/x.txt\"\n"
--- FAIL: TestBuildBatchPutRecursive (0.00s)
=== RUN   TestGoBackendPutTreeLandsIntoFinalDir
    tree_test.go:373: 最终目标目录下必须直接出现 x.txt: err=open /tmp/TestGoBackendPutTreeLandsIntoFinalDir2849306615/001/t/x.txt: no such file or directory content="" want="X"
--- FAIL: TestGoBackendPutTreeLandsIntoFinalDir (0.01s)
FAIL
FAIL	sshore/internal/sftp	0.016s
FAIL
```

### 4.2 GREEN（同一批用例，修复后）

```bash
GOCACHE=$PWD/.gocache go test ./internal/sftp/ \
  -run 'TestGoBackendPutTreeLandsIntoFinalDir|TestBuildBatchPutRecursive|TestPutRecursiveRunsSftp' -count=1 -v
```

```
=== RUN   TestBuildBatchPutRecursive
--- PASS: TestBuildBatchPutRecursive (0.00s)
=== RUN   TestPutRecursiveRunsSftp
--- PASS: TestPutRecursiveRunsSftp (0.00s)
=== RUN   TestGoBackendPutTreeLandsIntoFinalDir
--- PASS: TestGoBackendPutTreeLandsIntoFinalDir (0.01s)
PASS
ok  	sshore/internal/sftp	0.017s
```

### 4.3 中间态证据：既有用例确实编码了旧（父目录）约定

只改 `gosftp.go`、未动既有断言时，`go test ./internal/sftp/ -count=1` 变红 9 条，全部是「按旧约定摆放/查找文件」的目录用例：

```
--- FAIL: TestGoBackendPutTreeRetryCleansStaleRemoteParts (0.00s)
--- FAIL: TestGoBackendPutTreeMergesWithoutNesting (0.00s)
--- FAIL: TestGoBackendTreesNonAtomicDirectWrite (0.01s)
--- FAIL: TestGoBackendTreeEmptyDirs (0.01s)
--- FAIL: TestGoBackendPutTreeFollowsLocalRootSymlink (0.00s)
--- FAIL: TestGoBackendPutTreeIgnoresSinglePartPathAndResume (0.00s)
--- FAIL: TestGoBackendPutTreeDegradesByStoppingLocalWalk (0.00s)
--- FAIL: TestGoBackendPutTreeReconcilesFileGrownAfterEnumeration (0.00s)
--- FAIL: TestGoBackendPutTreeUploadClearPreventsSharedPartAnchor (0.00s)
FAIL	sshore/internal/sftp	18.145s
```

这些用例已按新契约改传参、**保留原有不变量断言**（见 §3.3），改后全绿。

### 4.4 真机 e2e（一次性真 sshd，两个后端各跑一遍）

`GOCACHE=$PWD/.gocache make e2e`（脚本 `e2e/test_local.sh` 起临时 sshd；`TestTreeE2E` 的目标目录**故意不预建**，
用来证明两个后端都会自己建出最终目录）：

```
>>> -run ^TestTreeE2E$ (迭代 backend=batch)
=== RUN   TestTreeE2E
    e2e_test.go:605: SSHORE_E2E_BACKEND=batch
    e2e_test.go:606: 目录传输实现 = BatchBackend.TransferGetTree/PutTree（backend=0, Atomic=false）
    e2e_test.go:681: 目录往返完成：3 个文件 + 空目录 + 合并语义（backend=0, Atomic=false）
--- PASS: TestTreeE2E (0.46s)

>>> -run ^TestTreeE2E$ (迭代 backend=gosftp)
=== RUN   TestTreeE2E
    e2e_test.go:605: SSHORE_E2E_BACKEND=gosftp
    e2e_test.go:606: 目录传输实现 = GoBackend.GetTree/PutTree（backend=1, Atomic=true）
    e2e_test.go:681: 目录往返完成：3 个文件 + 空目录 + 合并语义（backend=1, Atomic=true）
--- PASS: TestTreeE2E (0.44s)
```

```
== ALL E2E TESTS PASSED (观测后端: batch 迭代=batch, gosftp 迭代=gosftp; E2E_RUN=TestGoBackendE2E,TestCancelWholeBatchE2E,TestResumeE2E,TestTreeE2E,TestSyncE2E,TestCapabilitiesE2E,TestNoLeftoverOnCloseE2E,TestCloseAllCleansRegisteredPartsE2E) ==
EXIT=0
```

（完整日志 `/tmp/sshkit-e2e-full.log`，8 个 e2e 用例 × 2 迭代全绿。`make e2e` 不属于 `make ci`，此处作为额外真机证据。）

---

## 5. `make ci`

```bash
GOCACHE=$PWD/.gocache make ci
```

```
cd frontend && npm run build
go vet ./...
go test ./... -race -count=1
ok  	sshore	1.869s
ok  	sshore/internal/config	1.357s
ok  	sshore/internal/forward	1.883s
ok  	sshore/internal/importer	1.017s
ok  	sshore/internal/localfs	1.021s
ok  	sshore/internal/namepattern	1.016s
ok  	sshore/internal/osutil	6.446s
ok  	sshore/internal/preset	1.017s
ok  	sshore/internal/sftp	19.599s
ok  	sshore/internal/sshconn	1.018s
ok  	sshore/internal/sync	16.279s
ok  	sshore/internal/update	14.833s
ok  	sshore/internal/watch	6.819s
 Test Files  38 passed (38)
      Tests  489 passed (489)
EXIT=0
```

**退出码：`EXIT=0`**（build + vet + `go test ./... -race -count=1` + vitest 全绿）。

---

## 6. 残留风险与已知差异

1. **部分失败/取消时的清理边界（最重要）**：`cleanupRemoteTreeParts` 的 root 从「为本棵树新建的
   `<remote>/<base>` 专用目录」变成「**用户可能已有内容的最终目标目录**」。清理**不会**越界删别人东西，
   依据是 `isPartForTarget` 的双重判据（`<目标 basename>` + `PartMarker` + `本次 id` 短前缀）；
   本轮在 `cleanup_test.go` 补了「同名普通业务文件不得被删」的断言。但仍有两处**尽力而为**的边界：
   - 遍历硬上限 `cleanupTreeMaxEntries = 100000` / `cleanupTreeMaxDepth = 64`：目标目录极大或极深时，
     清理会提前放弃（残留的旧 `.part` 需靠重试/手工清理），而不是报错；
   - 枚举到「同名目标」的 `.part` 才会删；若上次失败发生在**目标目录被改名/移动**之后，锚点会找不到
     （与改前同样的性质，非本轮引入）。
2. **batch 后端的已知差异**（batch 无 `.part`/提交语义，`guardAtomic` 硬拒 `Atomic=true`，这是既有裁决）：
   - 不再有 OpenSSH `put -r` 的「一次命令」形态：现在是 `-mkdir`×N + `put`×N 的生成脚本。首个体级失败会
     中止整批（`put` 未加 `-` 前缀），已传文件保留 —— 与 `put -r` 中止时同样是**部分上传**，但**不会**有任何
     进度上报（batch 本就没有进度通道）；
   - 脚本长度随树规模线性增长（大树的单条批处理会很长）；不做 D16 截断（见 §3.2 的有意选择）；
   - 目录行先于文件行：`put` 不建父目录，顺序反了会整批失败（已用单测钉住用例中的顺序）；
   - `-mkdir` 的失败被抑制（`-` 前缀）：若同名路径是**普通文件**，mkdir 静默失败，随后的 `put` 会报错并中止整批
     —— 报错没有被吞掉，只是消息来自 `put`。
3. **`acquireInflight` 去重键语义变化（变准，但有行为差异）**：键仍是 `req.Remote`，而它的含义从「父目录」
   变成「最终目录」。改前「同一父目录下上传两个不同源目录」会命中同一个键而被判为「同一目标已在传输中」并**拒绝**；
   改后这两个请求的最终目录不同、不再互相拒绝（它们本来就不冲突）。反之，落到**同一**最终目录的两次上传仍会被拒绝 ——
   与「同目标并发绝不隐式串行」的既有契约一致。
4. **`Remote` 的等价写法不去重**：GoBackend 侧 `path.Clean` 会把 `"/t/"` 与 `"/t"` 归一到同一个 root，
   但 `acquireInflight` 的键用的是**原始字符串** `req.Remote`，所以 `"/t"` 与 `"/t/"` 的两次并发上传不会被判为重复。
   这是既有性质（改前同样按原始串比较），本轮未扩大范围去改。
5. **范围外观察（未改，仅记录）**：`app.go:944` 的 `a.recordRecentSFTP(host, path.Dir(remoteDir), …, false)` 记录的是
   `remoteDir` 的**父目录**。改前它也是父目录（`remoteDir` 当时是父目录 ⇒ 记录的是祖父），即「记录值」不随本轮
   变化而变化，**没有引入回归**；但按新语义更自然的取值是 `remoteDir` 本身。因本轮明确不碰持久化/最近目录代码，未改。
6. **未做**：未重跑真机 GUI 验收（本轮约束只要求 `make ci` 全绿 + 单测先红后绿；目录语义已由一次性真 sshd 的
   `TestTreeE2E` 双后端覆盖）。前端 dst 计算、刷新/持久化/自愈代码一律未动。

---

## 7. 复现命令速查

```bash
GOCACHE=$PWD/.gocache go test ./internal/sftp/ -run 'TestGoBackendPutTreeLandsIntoFinalDir|TestBuildBatchPutRecursive' -count=1 -v
GOCACHE=$PWD/.gocache make e2e            # 一次性真 sshd：TestTreeE2E 双后端（batch/gosftp）
GOCACHE=$PWD/.gocache make ci             # build + vet + go test -race + vitest
```

---

# 复核轮修复（第二轮）

- 修复基线：`3cc859a`（复核第一轮结束时的工作树/HEAD）
- 本轮**代码/测试**提交（**只本地提交、未 push**；精确清单 `git log --oneline 3cc859a..23d8336`）：
  写成闭区间是为了不随后续提交自指漂移；本节补记（本次 docs 提交）位于 `23d8336` 之后、不计入下表。
  | 短 SHA | 标题 | 覆盖条目 |
  |---|---|---|
  | `cd59f53` | fix(sftp): 上传侧重试清理只扫本树会写到的目录（不再递归既有子树） | Important-1 |
  | `dc26ab9` | fix(sftp): batch 递归上传逐级建出目标祖先目录（祖先缺失不再整批失败） | Minor-2 |
  | `ded63e6` | docs(sftp): 注释与实现对齐 —— 重试清理只清 .part，不清 backup-swap 的 .bak | Minor-3 |
  | `94003cb` | fix(sftp): 远端清理按「本目录自己的目标」判定（收窄 shortID 截断下的误删面） | Minor-4（收窄判据） |
  | `23d8336` | docs(sftp): 更正注释里的 sftp 版本（实测为 OpenSSH_8.9p1 的 sftp 客户端） | 注释准确性（纯文档） |
- 未动：`frontend/**`、刷新/持久化/自愈代码、下载方向语义（`GetTree`/`Put`/`Get`）。
- 验证：`make ci` **EXIT=0**、`make e2e`（一次性真 sshd，batch/gosftp 双迭代 8 用例）**EXIT=0**（见 §7）。

## 0. 处置总览

| # | 级别 | 位置 | 结论 | 提交 |
|---|---|---|---|---|
| 1 | Important | `internal/sftp/gosftp.go`（`PutTree` 调用点 + `cleanupRemoteTreeParts`） | 已修：扫描面收窄为「本树实际会写到的 目录 × 该目录里本树的目标」；不再递归既有子树，ReadDir 次数不随既有子树规模增长 | `cd59f53` |
| 2 | Minor | `internal/sftp/batch.go`（`putTreeBatchLines`） | 已修：对目标路径自上而下逐级 `-mkdir`（全部带 `-` 前缀，幂等）；祖先缺失时 batch 也能落盘 | `dc26ab9` |
| 3 | Minor | `gosftp.go` 注释（call site、`isPartForTarget`、清理块注释） | 已修：**改注释与实现一致**（明确只清 `.part`、**不清** `.bak`），未新增 bak 判据（拒绝扩大删除面）；新增断言把「bak 存活」钉死 | `ded63e6` |
| 4 | Minor | `shortID` 8 rune 截断 | 复核确认：第 1 条修完后原设想的「嵌套最终目录 ⇒ 跨任务删 `.part`」**已消失**；本轮再收窄判据，使残留条件收敛为「同一最终目标路径 + 8 rune 碰撞（真实同文件冲突）」，并加断言 | `94003cb` |

---

## 1. Important-1：清理扫描面过宽

### 1.1 改了什么

- 新增 `putTreeCleanupScope(files)`（`gosftp.go`）：由本次要传的 `files` 的 `rel` 推导出清理扫描面
  —— 每个文件贡献 `{ path.Dir(rel) → path.Base(rel) }`，即「本树**实际会写的目录** × 该目录里本树的 basename」，顺序稳定（根在前、其余字典序）；某目录里没有本树会写的文件时**连 `ReadDir` 都不发**。
- `cleanupRemoteTreeParts` 签名由 `(ctx, s, remoteRoot, targets, id)` 改为
  `(ctx, s, remoteRoot, scope []cleanupScope, id)`：对每个扫描单元恰好做**一次** `ReadDir`，
  **不再递归**；判定用该单元自己的目标集合。
- `PutTree` 调用点改为 `g.cleanupRemoteTreeParts(ctx, s, rootRemote, putTreeCleanupScope(files), req.ID)`。
- 新增注入点 `readRemoteDir`（与既有 `removeRemote`/`copyStream`/`scanCheckpoint` 同一手法）：
  让单测能**计数 ReadDir 调用**，把验收判据 ④ 变成可观测断言。
- 常量 `cleanupTreeMaxEntries/cleanupTreeMaxDepth` 保留但注释说明：它们现在只约束**本地侧**（`cleanupTreeParts`，整棵本地子树）；远端侧不再需要（扫描量与本树写入目录数同阶）。

### 1.2 修前行为

`cleanupRemoteTreeParts` 从 `rootRemote` 开始**递归遍历整棵既有目标子树**（每目录一次 `ReadDirContext`，
上限 `cleanupTreeMaxEntries=100000` / `cleanupTreeMaxDepth=64`），并用「全树 basename 并集」判定每个目录里的条目。
新语义下 `rootRemote` 是用户可能已有内容的最终目录，于是：
(a) 清理开销随用户既有子树规模线性增长；(b) `shortID` 只截 8 rune，碰撞时会扫到与本树无关的分支并删除其 `.part`。

### 1.3 修后行为

只扫「本树实际会写到的目录」各一次，且只认该目录里本树的目标：
目录数/往返次数与本树写入目录集合**同阶**，不随既有子树规模增长；本树不会写到的目录/名字不再进入删除面。

### 1.4 验收判据逐条

| 判据 | 落地断言 |
|---|---|
| ① 行为等价：本 id 的过期 `.part`（在本树会写到的目录里）仍被清掉 | `TestGoBackendPutTreeCleanupScopeIsWriteDirSet` 断言 `out/big.bin.<marker><id8>-stale1` 与 `out/sub/y.bin.<marker><id8>-stale2` 均被删；`TestGoBackendPutTreeRetryCleansStaleRemoteParts` 原断言保持全绿 |
| ② 行为收紧：本树**不会**写到的子目录里的过期 `.part` 不再被扫到 | 同用例断言 `out/unrelated/big.bin.<marker><collidingID8>-dead1` 原样留下（碰撞 id 场景）；`TestGoBackendPutTreeCleanupTargetsArePerDir` 再收窄一层 |
| ③ 既有「同名业务文件不得被删」断言仍绿 | `TestGoBackendPutTreeRetryCleansStaleRemoteParts`（`business` 文件内容必须仍是 `BIZ`） |
| ④ 扫描目录数/往返次数不随既有子树规模增长 | `TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet`：远端先造 40 个无关既有子目录，用 `readRemoteDir` 计数并断言目录集**恰为** `[out, out/sub]` |

### 1.5 RED（先写断言、跑红）

RED 环境：`git worktree add --detach /tmp/sshkit-red2 HEAD`（= `3cc859a`，主工作树未动），把本轮测试文件复制进去，并在 scratch 的 `gosftp.go` 里只做「让新 API 名可编译」的最小改动（补 `readRemoteDir` 注入点、把
`cleanupRemoteTreeParts` 签名换成新签名、补 `putTreeCleanupDirs`），**函数体保留旧的递归遍历**
（`_ = dirs`）。

```bash
cd /tmp/sshkit-red2
GOCACHE=/home/lan/workspace/scripts/sshkit/.gocache \
  go test ./internal/sftp/ -run 'TestGoBackendPutTreeCleanupScopeIsWriteDirSet|TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet|TestPutTreeCleanupDirsIsParentSetOfRel|TestBakFilesAreNotCleaned|TestBuildBatchPutRecursive|TestBuildBatchPutRecursiveCreatesMissingAncestors' -count=1 -v
```

原始输出（节选，完整见 `/tmp/sshkit-red2` 运行记录）：

```
=== RUN   TestGoBackendPutTreeCleanupScopeIsWriteDirSet
    cleanup_scope_test.go:99: 本树不会写到的目录里的 .part 不得被扫到/删除: /tmp/TestGoBackendPutTreeCleanupScopeIsWriteDirSet1850991904/001/out/unrelated/big.bin.sshore-sftppart-t10000-1-dead1 err=stat /tmp/.../out/unrelated/big.bin.sshore-sftppart-t10000-1-dead1: no such file or directory
--- FAIL: TestGoBackendPutTreeCleanupScopeIsWriteDirSet (0.01s)
=== RUN   TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet
    cleanup_scope_test.go:127: 清理扫描的远端目录 = [out out/sub out/unrelated out/unrelated/noise00 out/unrelated/noise01 ... out/unrelated/noise39], want [out out/sub]（必须只扫本树写入目录集合，不得递归既有子树）
--- FAIL: TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet (0.01s)
=== RUN   TestBuildBatchPutRecursive
    ctrl_test.go:676: putr batch =
        "-mkdir \"/remote/dir\"\n-mkdir \"/remote/dir/empty\"\n-mkdir \"/remote/dir/sub\"\nput \".../sub/y.txt\" \"/remote/dir/sub/y.txt\"\nput \".../x.txt\" \"/remote/dir/x.txt\"\n"
        want
        "-mkdir \"/remote\"\n-mkdir \"/remote/dir\"\n-mkdir \"/remote/dir/empty\"\n-mkdir \"/remote/dir/sub\"\nput ..."
--- FAIL: TestBuildBatchPutRecursive (0.00s)
=== RUN   TestBuildBatchPutRecursiveCreatesMissingAncestors
    ctrl_test.go:706: remoteDir="/remote/deep/nested/dir" 脚本过短: "-mkdir \"/remote/deep/nested/dir\"\nput \".../x.txt\" \"/remote/deep/nested/dir/x.txt\"\n"
--- FAIL: TestBuildBatchPutRecursiveCreatesMissingAncestors (0.00s)
FAIL	sshore/internal/sftp	0.048s
```

要点：删掉的是 `out/unrelated/...`——**本树不会写到的分支**，且 `id` 前 8 rune 与本次相同（`t10000-1`），
正是 Important-1 描述的「shortID 碰撞 + 扩大删除面」；ReadDir 扫了 43 个目录（含 40 个既有噪声目录），
而本树只会写 `out` 与 `out/sub`。

### 1.6 GREEN（同一批用例，修复后）

```bash
GOCACHE=$PWD/.gocache go test ./internal/sftp/ \
  -run 'TestGoBackendPutTreeCleanupScopeIsWriteDirSet|TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet|TestGoBackendPutTreeCleanupTargetsArePerDir|TestPutTreeCleanupScopeIsPerDirTargetSet|TestBakFilesAreNotCleaned|TestCleanupRemoteTreePartsRemovesOnlyKnownRemoteParts|TestGoBackendPutTreeRetryCleansStaleRemoteParts|TestIsPartForTargetRecognizesOnlyOwnParts' -count=1 -v
```

```
=== RUN   TestBakFilesAreNotCleaned
--- PASS: TestBakFilesAreNotCleaned (0.00s)
=== RUN   TestGoBackendPutTreeCleanupScopeIsWriteDirSet
--- PASS: TestGoBackendPutTreeCleanupScopeIsWriteDirSet (0.01s)
=== RUN   TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet
--- PASS: TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet (0.01s)
=== RUN   TestGoBackendPutTreeCleanupTargetsArePerDir
--- PASS: TestGoBackendPutTreeCleanupTargetsArePerDir (0.00s)
=== RUN   TestPutTreeCleanupScopeIsPerDirTargetSet
--- PASS: TestPutTreeCleanupScopeIsPerDirTargetSet (0.00s)
=== RUN   TestIsPartForTargetRecognizesOnlyOwnParts
--- PASS: TestIsPartForTargetRecognizesOnlyOwnParts (0.00s)
=== RUN   TestCleanupRemoteTreePartsRemovesOnlyKnownRemoteParts
--- PASS: TestCleanupRemoteTreePartsRemovesOnlyKnownRemoteParts (0.00s)
=== RUN   TestGoBackendPutTreeRetryCleansStaleRemoteParts
--- PASS: TestGoBackendPutTreeRetryCleansStaleRemoteParts (0.01s)
PASS
ok  	sshore/internal/sftp	0.052s
```

> `internal/sftp/cleanup_test.go` 的两处直接调用点按新签名改为
> `[]cleanupScope{{dir: ".", targets: []string{"a.bin"}}}`，**断言与语义不变**（该用例只写 `sub` 一个目录）。

---

## 2. Minor-2：缺祖先目录时 batch 整批失败

### 2.1 先做真机探针（决定写法）

`sftp` 的 `mkdir` 是**单级**语义，写法的取舍必须实测（本机 `openssh-client 1:8.9p1-3ubuntu0.17`，
一次性 sshd + 内存临时目录；`-` 前缀 = `sftp -b` 的「失败不中止整批」）：

| 探针 | 命令 | 结果 |
|---|---|---|
| 1 | `-mkdir <深路径>`，祖先全缺失 | stderr `remote mkdir ".../x/y/z": No such file or directory`，**什么也没建**，`EXIT=0`（被 `-` 抑制） |
| 2 | `mkdir <深路径>`（不带 `-`），祖先全缺失 | 同上报错，`EXIT=1`（中止整批） |
| 3 | `-mkdir <已存在目录>` | stderr `remote mkdir ".../dup": Failure`，`EXIT=0`；不带 `-` 时 `EXIT=1` |
| 4 | 自上而下逐级 `-mkdir <a1>` `<a1/b2>` `<a1/b2/c3>` + `put` | `EXIT=0`，三级目录与文件全部落盘 |
| 5 | `-mkdir <同名普通文件>` | stderr `Failure`，`EXIT=0`（随后 `put` 会报错并中止） |

⇒ **逐级 `-mkdir` + `-` 前缀**即可与 `MkdirAll` 等价，且天然幂等（第 3 行：已存在只报 `Failure`，
被前缀抑制），不需要先 `ls` 探测。

原始输出（节选）：

```
=== PROBE 1: -mkdir 深路径，祖先全部缺失 ===
remote mkdir "/tmp/tmp.kfYLgm5mzb/remote/x/y/z": No such file or directory
EXIT1=0
--- 落盘 ---                       ← 空
=== PROBE 3: -mkdir 幂等性 —— 已存在目录 ===
remote mkdir "/tmp/tmp.kfYLgm5mzb/remote/dup": Failure
EXIT3(with dash, existing)=0
remote mkdir "/tmp/tmp.kfYLgm5mzb/remote/dup": Failure
EXIT3b(no dash, existing)=1
=== PROBE 4: 逐级 -mkdir 自上而下（祖先缺失场景的修法候选） ===
EXIT4=0
--- 落盘 ---
/tmp/tmp.kfYLgm5mzb/remote/a1/b2/c3/leaf.txt
```

### 2.2 改了什么

- 新增 `mkdirAncestors(p)`（`batch.go`）：返回远端目录 `p` **自上而下**的全部层级（含 `p` 自身）；
  绝对路径到根为止（**不返回 `/`**：它一定存在，建它只会多一条无意义的 `Failure` 行），相对路径到第一级为止
  （**不返回 `.`**）；`p` 为 `""`/`"."`/`"/"` 时返回 `nil`。
- `putTreeBatchLines` 由「只建 `remoteRoot` 一条」改为「对 `mkdirAncestors(remoteRoot)` 逐级建」，
  全部走既有的 `-mkdir` 生成器（`-` 前缀不变），目录行仍全部排在 `put` 之前；源树子目录行与文件行逻辑未动。

### 2.3 修前 / 修后行为

- 修前：目标中间层缺失时 `-mkdir` 静默失败（被抑制），随后 `put` `dest open ...: No such file or directory`
  ⇒ batch **整批 EXIT=1**，一个文件也落不下（报错清楚、不静默，但不满足「自己补齐祖先」）。
- 修后：祖先逐级建出（幂等），目标中间层缺失也能完整落盘；`GoBackend` 侧行为不变（本来就 `MkdirAll`）。

### 2.4 RED（真 sshd：`TestTreeE2E` 新增的「祖先缺失」阶段）

```bash
cd /tmp/sshkit-red2   # 同上：pre-fix 生产代码 + 本轮测试
GOCACHE=$PWD/.gocache E2E_RUN=TestTreeE2E bash e2e/test_local.sh
```

```
--- 迭代 backend=batch ---
>>> -run ^TestTreeE2E$ (迭代 backend=batch)
=== RUN   TestTreeE2E
    e2e_test.go:607: SSHORE_E2E_BACKEND=batch
    e2e_test.go:690: 祖先目录缺失时 PutTree 必须自己补齐（batch 逐级 -mkdir / gosftp MkdirAll）: sftp put -r sshore-e2e: exit status 1 (Warning: Permanently added '[127.0.0.1]:22901' (ED25519) to the list of known hosts.
        remote mkdir "/tmp/tmp.62Lu9x1z6F/remote-conf/tree/missing-mid/leaf": No such file or directory
        remote mkdir "/tmp/tmp.62Lu9x1z6F/remote-conf/tree/missing-mid/leaf/b": No such file or directory
        remote mkdir "/tmp/tmp.62Lu9x1z6F/remote-conf/tree/missing-mid/leaf/b/c": No such file or directory
        remote mkdir "/tmp/tmp.62Lu9x1z6F/remote-conf/tree/missing-mid/leaf/emptydir": No such file or directory
        dest open "/tmp/tmp.62Lu9x1z6F/remote-conf/tree/missing-mid/leaf/b/c/z.txt": No such file or directory)
--- FAIL: TestTreeE2E (0.61s)
FAIL: backend=batch 没有名字**精确等于** E2E_RUN 名单项 'TestTreeE2E' 的用例真正 PASS
...
--- 迭代 backend=gosftp ---
=== RUN   TestTreeE2E
    e2e_test.go:608: 目录传输实现 = GoBackend.GetTree/PutTree（backend=1, Atomic=true）
--- PASS: TestTreeE2E (0.59s)
EXIT_RED_E2E=1
```

（同一阶段 `gosftp` 迭代 PASS，正好把差异归因到 batch 的 `-mkdir` 语义。）

### 2.5 GREEN

单元（脚本形状）：

```bash
GOCACHE=$PWD/.gocache go test ./internal/sftp/ \
  -run 'TestBuildBatchPutRecursive|TestBuildBatchPutRecursiveCreatesMissingAncestors|TestPutRecursiveRunsSftp' -count=1 -v
```

```
=== RUN   TestBuildBatchPutRecursive
--- PASS: TestBuildBatchPutRecursive (0.00s)
=== RUN   TestBuildBatchPutRecursiveCreatesMissingAncestors
--- PASS: TestBuildBatchPutRecursiveCreatesMissingAncestors (0.00s)
=== RUN   TestPutRecursiveRunsSftp
--- PASS: TestPutRecursiveRunsSftp (0.00s)
PASS
ok  	sshore/internal/sftp	0.013s
```

真机（`make e2e` 的 `TestTreeE2E`，两个迭代都含「祖先缺失」阶段）：

```
>>> -run ^TestTreeE2E$ (迭代 backend=batch)
    e2e_test.go:607: SSHORE_E2E_BACKEND=batch
    e2e_test.go:697: 目录往返完成：3 个文件 + 空目录 + 合并语义 + 祖先缺失补齐（backend=0, Atomic=false）
--- PASS: TestTreeE2E (0.60s)
>>> -run ^TestTreeE2E$ (迭代 backend=gosftp)
    e2e_test.go:607: SSHORE_E2E_BACKEND=gosftp
    e2e_test.go:697: 目录往返完成：3 个文件 + 空目录 + 合并语义 + 祖先缺失补齐（backend=1, Atomic=true）
--- PASS: TestTreeE2E (0.59s)
```

新增用例的把握点：`/remote/deep/nested/dir` → 行序必须是 `/remote` → `/remote/deep` → `/remote/deep/nested`
→ `/remote/deep/nested/dir`；每行都带 `-` 前缀；所有目录行必须排在所有 `put` 之前；另覆盖 `rel/dir`
（相对路径）与 `"."`（不产生任何 `-mkdir` 行）两个边界。

---

## 3. Minor-3：注释与实现不符（`.part`/`.bak`）

### 3.1 结论与理由（二选一：**改注释**）

选「改注释与实现一致」，**不**新增 bak 判据。理由：
1. `BakName`/`BakNameRemote` 的形状是 `<target><marker>bak-<rand>`（退化时 `<marker>bak-<rand>`），
   后缀是 `bak-`，永远过不了 `isPartForTarget` 的 `shortID(id)+"-"` 判据 —— 实测
   `a/big.bin.sshore-sftppart-bak-ab12cd` 在重试清理后**存活**（本用例已把它固化成断言）。
2. bak 名里**没有 id**：任何「本目标 + `bak-`」判据都无法区分「我们这次留下的」与「别人/别的时刻留下的」，
   加它等于**扩大删除面**——任务明确要求「不要为了对齐注释而扩大删除面」。
3. 旧 bak 是 swap 前的上一版数据，留下更安全；本地侧另有 7 天陈旧清理 `CleanupStaleLocalParts` 兜底。

### 3.2 改了什么

- `gosftp.go` `PutTree` 调用点注释：`只删…命中的远端 .part/.bak` → 明确「只删 `.part`；**不**清 backup-swap 的 `.bak`」。
- `gosftp.go` 清理块注释：补「清理的**不是** backup-swap 的 `.bak`：后缀是 `bak-` 而非 `<shortID>-`，
  过不了 `isPartForTarget`（由 `TestBakFilesAreNotCleaned` 钉住）」，并区分
  「本地侧仍是整棵子树 + 硬上限 / 远端侧不再递归」。
- `isPartForTarget` 注释：删掉误导性的「（bak 同理但前缀是 bak-）」，写明两个分支**都不**命中 bak，
  以及「要清 bak 必须新加判据、那会扩大删除面」的取舍。
- 新增 `internal/sftp/cleanup_bak_test.go`（`TestBakFilesAreNotCleaned`）：判据层（按**生成器真实形状**
  `BakNameRemote` 构造常规名与退化短名，都不得被认出）+ 清理层（远端真实存在的 bak 在
  `cleanupRemoteTreeParts` 之后必须原样留下）。

### 3.3 RED（反向：断言必须能抓住「按旧注释实现」这种错法）

临时把旧注释声称的行为真的实现出来（在 `isPartForTarget` 里加 bak 判据），跑断言：

```bash
# 临时补丁：isPartForTarget 的 suffix 分支加
#   if strings.HasPrefix(suffix, "bak-") { return true }
GOCACHE=$PWD/.gocache go test ./internal/sftp/ -run 'TestBakFilesAreNotCleaned' -count=1 -v
```

```
=== RUN   TestBakFilesAreNotCleaned
    cleanup_bak_test.go:27: bak 不得被认成本次传输的 .part（后缀是 bak- 而非 <shortID>-）: "big.bin.sshore-sftppart-bak-56fe8a" target="big.bin"
--- FAIL: TestBakFilesAreNotCleaned (0.01s)
FAIL
FAIL	sshore/internal/sftp	0.006s
```

（临时补丁已还原，未进任何提交。）

### 3.4 GREEN

见 §1.6 的 `--- PASS: TestBakFilesAreNotCleaned (0.00s)`。

---

## 4. Minor-4：`shortID` 截断 8 rune —— 复核结论

### 4.1 结论

**原设想的场景（「配合嵌套最终目录跨任务删 `.part`」）在第 1 条修完后已经消失**，本轮的处置是「再收窄判据 + 加断言」，**没有**改 `shortID` 本身。

推理链：
1. 修后扫描面只含「本树会写到的 目录 × 该目录里本树的目标」。**嵌套的、与本树无关的最终目录根本不进扫描面**
   —— 即便 id 前 8 rune 完全相同也不会被扫到。§1.5 的 RED 恰恰反证了这一点：旧实现会删
   `out/unrelated/...` 里的碰撞 id `.part`，新实现不删（断言已固化）。
2. 进一步检查「本树会写到的目录」内部仍有残留面：把**全树 basename 并集**交给每个待扫目录判定时，
   只要某 basename 在本树任意位置出现，本树写到的**任何**目录里同名 `.part` 都会被删。例如
   本树写 `out/big.bin` + `out/sub/y.bin` 时，`out/sub/big.bin.<marker><别的任务碰撞 id>-x`
   （别的任务的最终路径是 `out/sub/big.bin`，本树不写它）会被误删。
3. ⇒ 本轮把扫描单元改成「目录 + **该目录里**本树会写的 basename」（`cleanupScope`/`putTreeCleanupScope`）。
   于是跨任务误删的**必要条件**收敛为：
   `同一目录` ∧ `同一 basename` ∧ `shortID 前 8 rune 相同`
   ⇔ **同一最终目标路径 + 8 rune 碰撞** —— 也就是「两个任务在写同一个最终路径」的真实同文件冲突
   （此时被清掉 `.part` 的一方只是该次传输报错重试；已提交内容与目标文件不受影响，绝不发生原文所述的
   「跨目录/无关分支误删」）。
4. 未改 `shortID`（仍截 8 rune）：它参与 `partname.go` 的命名契约，前端 `queue.js` 硬编码同一
   `PART_MARKER` 且单测钉住字面量，动它会外溢到前后端共享契约；而收窄后的残留条件已是「同一路径冲突」，
   继续收窄也换不到安全收益（文件名里只有 8 rune id，判据无法再区分）。

### 4.2 RED（新断言 + 上一版实现 `cd59f53`）

```bash
GOCACHE=$PWD/.gocache go test ./internal/sftp/ -run 'TestGoBackendPutTreeCleanupTargetsArePerDir' -count=1 -v
```

```
=== RUN   TestGoBackendPutTreeCleanupTargetsArePerDir
    cleanup_scope_test.go:169: 本树在该目录里不写 big.bin，别的任务的 big.bin .part 不得被删: /tmp/TestGoBackendPutTreeCleanupTargetsArePerDir950053593/001/out/sub/big.bin.sshore-sftppart-t10000-1-dead1 err=stat /tmp/.../out/sub/big.bin.sshore-sftppart-t10000-1-dead1: no such file or directory
--- FAIL: TestGoBackendPutTreeCleanupTargetsArePerDir (0.01s)
FAIL
```

### 4.3 GREEN

见 §1.6 的 `--- PASS: TestGoBackendPutTreeCleanupTargetsArePerDir` 与
`--- PASS: TestPutTreeCleanupScopeIsPerDirTargetSet`。

---

## 5. 本轮新增/修改的测试清单

| 文件 | 内容 |
|---|---|
| `internal/sftp/cleanup_scope_test.go`（新增） | `countingReadRemoteDir` 计数助手；`TestGoBackendPutTreeCleanupScopeIsWriteDirSet`（①+②+碰撞 id 收紧）、`TestGoBackendPutTreeCleanupReadDirCountFollowsWriteDirSet`（④）、`TestGoBackendPutTreeCleanupTargetsArePerDir`（Minor-4 收窄 + 完备性）、`TestPutTreeCleanupScopeIsPerDirTargetSet`（纯函数形状） |
| `internal/sftp/cleanup_bak_test.go`（新增） | `TestBakFilesAreNotCleaned`（Minor-3：判据层 + 清理层） |
| `internal/sftp/cleanup_test.go` | 两处直接调用点适配新签名（断言与语义不变） |
| `internal/sftp/ctrl_test.go` | `TestBuildBatchPutRecursive` 期望值加祖先 `-mkdir` 行；新增 `TestBuildBatchPutRecursiveCreatesMissingAncestors`（行序/前缀/顺序/边界） |
| `internal/sftp/e2e_test.go` | `TestTreeE2E` 新增「祖先缺失」阶段（真 sshd，双后端）+ 函数头说明同步 |

---

## 6. 残留风险与本轮未做

1. **回收面已收敛但仍非零（Minor-4 残留）**：跨任务误删现在需要「同一最终目标路径 + `shortID` 8 rune 碰撞
   + 并发」。此时两个任务本来就在写同一个最终文件，被清 `.part` 的一方会失败重试，**不会**破坏已提交内容。
   若将来要把这最后一点也消掉，正确做法是让临时名带上可判别的完整 id（或随机 nonce 参与判据），
   而不是继续在 8 rune 前缀上加码 —— 那会动到前后端共享的命名契约，本轮不做。
2. **下载侧本地清理未动**：`cleanupTreeParts`（`GetTree`，本地 `filepath.WalkDir`）仍是整棵本地子树 +
   `cleanupTreeMaxEntries/MaxDepth` 硬上限。本轮范围明确在**上传侧远端**（Important-1 的位置），
   本地遍历没有 SFTP 往返开销，未扩大范围去改；如需要，可作为后续轮次对称处理。
3. **batch 脚本长度**：祖先行只增加 `O(路径深度)` 条，与既有「目录行 ×N + put ×N」量级一致；
   仍不做 D16 截断（沿用上一轮的有意选择）。
4. **`-mkdir` 遇同名普通文件**：仍是被抑制的 `Failure`，随后 `put` 报错并中止整批（报错不被吞掉，
   只是消息来自 `put`）—— 与上一轮记录一致，本轮未改变。
5. **未做**：未重跑真机 GUI 验收（本轮约束只要求 `make ci` + `make e2e`，且改动面全在 `internal/sftp`）；
   未动前端、刷新/持久化/自愈代码、下载方向语义。

---

## 7. 收尾验证（最终冻结版本）

```bash
GOCACHE=$PWD/.gocache make ci
```

```
ok  	sshore	1.866s
ok  	sshore/internal/config	1.344s
ok  	sshore/internal/forward	1.887s
ok  	sshore/internal/importer	1.018s
ok  	sshore/internal/localfs	1.031s
ok  	sshore/internal/namepattern	1.017s
ok  	sshore/internal/osutil	6.444s
ok  	sshore/internal/preset	1.017s
ok  	sshore/internal/sftp	19.656s
ok  	sshore/internal/sshconn	1.018s
ok  	sshore/internal/sync	16.284s
ok  	sshore/internal/update	14.848s
ok  	sshore/internal/watch	6.821s
 Test Files  38 passed (38)
      Tests  489 passed (489)
CI_EXIT=0        # 由外层 `echo "CI_EXIT=$?"` 捕获（`make ci` 的 stdout 在 /tmp/ci-final.log）
```

```bash
GOCACHE=$PWD/.gocache make e2e     # 一次性真 sshd；batch/gosftp 双迭代 × 8 用例
```

```
  TestTreeE2E                                 batch→batch  gosftp→gosftp
== ALL E2E TESTS PASSED (观测后端: batch 迭代=batch, gosftp 迭代=gosftp; E2E_RUN=TestGoBackendE2E,TestCancelWholeBatchE2E,TestResumeE2E,TestTreeE2E,TestSyncE2E,TestCapabilitiesE2E,TestNoLeftoverOnCloseE2E,TestCloseAllCleansRegisteredPartsE2E) ==
E2E_EXIT=0
```

**退出码：`make ci` EXIT=0、`make e2e` EXIT=0。** 命令与退出码原文：

```
$ { GOCACHE=$PWD/.gocache make ci > /tmp/ci-final.log 2>&1; echo "CI_EXIT=$?"; GOCACHE=$PWD/.gocache make e2e > /tmp/e2e-final.log 2>&1; echo "E2E_EXIT=$?"; }
CI_EXIT=0
E2E_EXIT=0
```

在**最终 HEAD** 上又把两条门重跑了一遍（该 HEAD 相对 `23d8336` 只多纯文档提交，无代码/测试变化）：

```
CI_EXIT=0
E2E_EXIT=0
```

（工作树干净：`git status --short` 无输出。日志：`/tmp/ci-final.log`、`/tmp/e2e-final.log`（`23d8336`）、
`/tmp/ci-final2.log`、`/tmp/e2e-final2.log`（最终 HEAD）。）
