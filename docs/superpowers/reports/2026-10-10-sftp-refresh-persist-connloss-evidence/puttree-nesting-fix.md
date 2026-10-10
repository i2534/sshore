# 上传目录多套一层（PutTree / PutRecursive 目录语义）修复报告

- 仓库 / 分支：`sshkit` @ `feat/sftp-refresh-persist-connloss`（未切分支、未 push）
- 代码修复提交：`a5a6702`（GoBackend.PutTree）、`2420121`（BatchBackend.PutRecursive）
- 文档/报告提交：`0ceb67f`（本报告首版入库）；其后仅 `eb06494` 调整了一个测试期望值的构造方式
  （`putr` 脚本断言的本地路径按 `EvalSymlinks` 后构造，避免 Windows 上的大小写差异；**无行为变化**）
- 触发证据：`real/REGRESSION-B.md` B-9（真机复现：`updir/` → `/home/lan/acc-b` 落成 `/home/lan/acc-b/updir/updir/a.txt`）
- 本轮未改前端、未改刷新/持久化/自愈代码

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
