# deferred minors 清理报告（2026-10-10 sftp-refresh-persist-connloss）

- 分支：`feat/sftp-refresh-persist-connloss`（未切分支、未 push、未合并）
- 清理起点 HEAD：`5bb55ec`（真机验收报告提交）；本报告提交前的代码提交：`a4760db`、`476e996`
- 记录台账：`.superpowers/sdd/2026-10-10-sftp-refresh-persist-connloss/progress.md`（**只读，未修改**；`git status` 全程干净即是佐证）
- 执行环境：`GOCACHE=$PWD/.gocache`（所有 Go 命令）；前端 `npx vitest run`
- 范围：只做本简报点名的 3 条必修项；点名"不修"的 5 条零代码改动

## 0. 处置总览

| 项 | 位置 | 生产代码改动 | 测试/注释改动 | 结论 |
|---|---|---|---|---|
| 1 | `frontend/src/views/SftpView.vue` `connect()` 记账 | **无**（生产修复已在 `d955146` 落地，见 §1 偏差说明） | `sftpRestoreWiring.test.js` +1 用例 | 断言已钉住，mutation 可红 |
| 2 | `app.go` `addLocalRecentLocked` 空路径守卫 | +5 行守卫 + 注释 | `app_test.go` +`TestAddLocalRecentEmptyPathIsNoop` | 真修（先红后绿） |
| 3a | `frontend/src/components/filePaneWiring.test.js` | 无 | 收紧 `visible` 读源断言 + `emit('context')` 计数 | mutation 可红 |
| 3b | `frontend/src/views/sftpMenuWiring.test.js` | 无 | 远程行/本地行分开校验 + 分派非空前提 | mutation 可红 |
| 3c | `frontend/src/components/FilePane.vue` | 无（仅注释） | — | 注释已补 |

三条"必须修"里，**第 1 条的生产修复在本轮起点之前就已落地**（`d955146`，即 progress.md line 118 所指的"收尾 triage"）。本轮对它的增量是**回归断言 + 判别力证据**，详见 §1。这一点属于对简报前提的偏离，已在 §5 顾虑中列明。

---

## 1. `connect()` 内的记账会误判连接失败

### 改了什么
`frontend/src/views/sftpRestoreWiring.test.js` 新增用例：

> `connect 内的记账自带 try/catch，写盘失败不再落进外层 catch 被判成「连接失败」`

断言三件事：① 记账那一行必须自带 `try { … } catch (e) { err(e) }`（形态与 `loadRemote`/`goRemote`/`goLocal` 同款）；② 该 catch 只记日志、不 `throw`（rethrow 等于没修）；③ 该行位于 `connect()` 的**外层 catch 之前**（放进外层 catch 里等于永不触发）。

### 偏差说明（重要）
简报描述"`connect()` 主 `try` 里有裸的 `await locations.addRemoteRecent(h, remotePath.value)`（约 `:430`）"——**该形态在起点 HEAD 已不存在**：

```
$ cd frontend && grep -n "addRemoteRecent(h, remotePath.value)" src/views/SftpView.vue
434:    try { await locations.addRemoteRecent(h, remotePath.value) } catch (e) { err(e) }
$ git log --oneline -1 -S "try { await locations.addRemoteRecent(h, remotePath.value) }" -- frontend/src/views/SftpView.vue
d955146 fix(sftp): 收尾 triage（失败耗时不含探活/重连、记账失败不再误判为连接/导航失败、setSort 忽略非法 pane、注释与断言修正）
```

`d955146` 的 diff 逐字如下（同轮还把 `goLocal`/`goRemote` 一并包上）：

```diff
-    await locations.addRemoteRecent(h, remotePath.value)
+    // 记账（配置写盘）失败不得被外层 catch 判成「连接失败」：只记日志，不改 connected/pending
+    // 语义（T11 复核 out-of-scope 观察；与 loadRemote 内的记账同形）。
+    try { await locations.addRemoteRecent(h, remotePath.value) } catch (e) { err(e) }
```

因此"修前行为/修后行为"以**该生产提交**为界：
- 修前（`d955146^`）：写盘（IPC）失败抛进 `connect()` 外层 `catch (e) { err(e); … connected.value = false }` ⇒ 记为「连接失败」并置 `connected = false`，而 SFTP 其实连上了。
- 修后（`d955146` 起，含本轮）：记账自带 `catch`，只 `err(e)` 记一条日志；`connected`、两个 pending 标志、`remotePath`/`remoteItems` 都不受影响。

本轮**不重复改生产代码**，只补钉住它的回归断言。

### RED（先证明这条断言真能抓回旧形态）
对生产文件做**突变**（把 `d955146` 的形态还原成 `d955146^` 的裸调用），跑测试：

```bash
cd frontend
cp src/views/SftpView.vue /tmp/SftpView.vue.bak
perl -0pi -e "s/    try \{ await locations\.addRemoteRecent\(h, remotePath\.value\) \} catch \(e\) \{ err\(e\) \}\n/    await locations.addRemoteRecent(h, remotePath.value)\n/" src/views/SftpView.vue
grep -n "addRemoteRecent(h, remotePath.value)" src/views/SftpView.vue   # → 434:    await locations.addRemoteRecent(h, remotePath.value)
npx vitest run src/views/sftpRestoreWiring.test.js
```

输出（节选）：

```
   × connect 内的记账自带 try/catch，写盘失败不再落进外层 catch 被判成「连接失败」 5ms
⎯⎯⎯⎯⎯⎯ Failed Tests 1 ⎯⎯⎯⎯⎯⎯⎯⎯
AssertionError: expected '    await locations.addRemoteRecent(h…' to match /try\s*\{.*await locations\.addRemoteR…/
      Tests  1 failed | 13 passed (14)
```

### GREEN（还原后）
```bash
cp /tmp/SftpView.vue.bak src/views/SftpView.vue
git diff --stat src/views/SftpView.vue        # 无输出 ⇒ 与 HEAD 逐字一致
npx vitest run src/views/sftpRestoreWiring.test.js src/views/sftpMenuWiring.test.js
```

```
 ✓ src/views/sftpRestoreWiring.test.js (14 tests) 7ms
 ✓ src/views/sftpMenuWiring.test.js (12 tests) 12ms
 Test Files  2 passed (2) / Tests  26 passed (26)
```

---

## 2. `AddLocalRecent("")` 与 `AddRemoteRecent` 不对齐

### 修前行为（探针实测，非推理）
临时探针（`zz_probe_empty_local_recent_test.go`，用完即删、未提交）在 `LastLocalDir="/keep"`、`LocalRecent=[{/l}]` 的前提下调用 `AddLocalRecent("")`：

```
AddLocalRecent("") err=<nil>
内存: LastLocalDir="" LocalRecent=[{Path: TS:2026-10-10T14:47:58+08:00} {Path:/l TS:2000-01-01T00:00:00Z}]
磁盘: stat err=<nil> | LastLocalDir="" LocalRecent=[{Path: TS:2026-10-10T14:47:58+08:00} {Path:/l TS:2000-01-01T00:00:00Z}] | load err=<nil>
```

结论（逐项回答简报的"先查清当前行为"）：
- **会**往 `LocalRecent` 追加一条空条目（`{Path: ""}`，带新 TS，置顶、把 `/l` 挤到第二）；
- **会**写盘：配置文件被创建/覆盖，盘上 `LastLocalDir` 已是 `""`、空条目已持久化；
- 此外还**清空了 `LastLocalDir`**（`/keep` → `""`）；
- 返回值是 `nil`（不报错）——即"静默损坏"。

### 修后行为
`app.go` 的 `addLocalRecentLocked` 顶部加守卫（与 `addRemoteRecentLocked` 的 `host == "" || path == ""` 对齐）：

```go
// 空路径 = 无操作，与 addRemoteRecentLocked 的 `host == "" || path == ""` 守卫对齐
if path == "" {
    return false
}
```

- 公开绑定 `AddLocalRecent("")`：`changed == false` ⇒ 立即 `return nil`，**不调 `saveConfig()`**；
- 内部 `addLocalRecentLocked("")`（供 `recordRecentSFTP` 用）：`return false` ⇒ 调用方不解锁后落盘；
- 两条入口语义一致：不报错、内存态（`LastLocalDir`/`LocalRecent`）不变、不落盘；
- 文档注释同步说明该守卫在 `*Locked` 内 ⇒ 公开绑定与内部路径同口径（与 `AddRemoteRecent` 同形；`AddRemoteRecent` 的守卫也只在 `*Locked` 内）。

不回归确认：`recordRecentSFTP` 的 5 个调用点里 `app.go:1204` 传 `localDir == ""`（SftpHome 路径，其余 4 处在 `:872/:885/:898/:944` 都传真实目录）。该路径修前靠调用点自己的 `localDir != ""` 守卫、修后是"调用点守卫 + `*Locked` 守卫"两层；新用例直接调 `a.recordRecentSFTP("home-host", "/home/u", "", false)` 断言不报错、不写空条目、不清 `LastLocalDir`。

### RED
```bash
GOCACHE=$PWD/.gocache go test ./ -run TestAddLocalRecentEmptyPathIsNoop -count=1 -v
```
```
=== RUN   TestAddLocalRecentEmptyPathIsNoop
    app_test.go:1214: 空路径不得改写 LastLocalDir，得到 ""（修前会被清成 ""）
--- FAIL: TestAddLocalRecentEmptyPathIsNoop (0.01s)
FAIL	sshore	0.011s
```

> 说明：RED 停在第一条 `Fatal`，所以"空条目 + 写盘"这两条事实由上文的**独立探针**取证（同一次未修改的生产代码），不是靠复述。

### GREEN
```bash
GOCACHE=$PWD/.gocache go test ./ -run TestAddLocalRecentEmptyPathIsNoop -count=1 -v
```
```
=== RUN   TestAddLocalRecentEmptyPathIsNoop
--- PASS: TestAddLocalRecentEmptyPathIsNoop (0.01s)
ok  	sshore	0.013s
```

```bash
GOCACHE=$PWD/.gocache go test ./ -count=1
```
```
ok  	sshore	0.670s
```

用例要点：`cfgPath` 指向 `filepath.Join(t.TempDir(), "sshore.toml")`（绝不碰用户真实配置）；断言（a）`LastLocalDir` 保持 `/keep`；（b）`LocalRecent` 仍只有 `{Path:"/l"}`、条数不变；（c）`os.Stat(cfgPath)` 必须 `IsNotExist`（**不写盘**）；（d）`recordRecentSFTP(..., localDir:"", ...)` 后内存态仍不变。

---

## 3. 断言/注释收紧（三处小改）

三处都遵循"新断言先写 → 突变生产代码证明它真能红 → 还原 → 绿"。**生产代码最终逐字未动**（每处都用 `git diff --stat` 确认还原后无输出）。

### 3a. `filePaneWiring.test.js`：`visible` 读源收紧 + `emit` 次数

改了什么：
- 把原断言 `expect(filePane).toMatch(/const visible = computed\(\(\)\s*=>\s*\{[\s\S]*?props\.sortKey[\s\S]*?props\.sortDir/)` 换成"先切出 computed 体（`const visible = computed(` … `return sorted`）再在切片内断言 `props.sortKey` / `props.sortDir`"；
- `分支顺序：可编辑放行 → .pos-wrap → 任何 li → preventDefault+emit` 内补 `expect(fn.match(/emit\('context'/g)).toHaveLength(1)`（`fn` = `onPaneContext` 体，钉住不重复 emit）。

RED 突变（同时做两处突变）：
```bash
cd frontend && cp src/components/FilePane.vue /tmp/FilePane.vue.bak
# ① visible 体内不再读 props（改读字面量）
perl -0pi -e "s/  const key = props\.sortKey\n  const dir = props\.sortDir\n/  const key = 'name'\n  const dir = 1\n/" src/components/FilePane.vue
# ② onPaneContext 里重复 emit 一次
perl -0pi -e "s/  e\.preventDefault\(\)\n  emit\('context', \{ item: null, event: e \}\)\n\}/  e\.preventDefault\(\)\n  emit('context', { item: null, event: e })\n  emit('context', { item: null, event: e })\n}/" src/components/FilePane.vue
```

先证明**旧断言**对该突变体是假绿（这正是收紧的理由）：
```
$ node -e "const s=require('fs').readFileSync('src/components/FilePane.vue','utf8');console.log('旧正则匹配:', /const visible = computed\(\(\)\s*=>\s*\{[\s\S]*?props\.sortKey[\s\S]*?props\.sortDir/.test(s))"
旧正则匹配: true        ← visible 体内读源已被删干净，旧正则仍然绿（跨函数懒匹配到 arrow()）
```

新断言 RED：
```
$ npx vitest run src/components/filePaneWiring.test.js
   × arrow() 与 visible() 的读源都是 props（两处必须同改） 10ms
   × 分支顺序：可编辑放行 → .pos-wrap → 任何 li → preventDefault+emit 2ms
⎯⎯⎯⎯⎯⎯ Failed Tests 2 ⎯⎯⎯⎯⎯
AssertionError: expected 'const visible = computed(() => {\n  l…' to contain 'props.sortKey'
AssertionError: expected [ 'emit(\'context\'', …(1) ] to have a length of 1 but got 2
      Tests  2 failed | 33 passed (35)
```

GREEN：
```
$ cp /tmp/FilePane.vue.bak src/components/FilePane.vue && git diff --stat src/components/FilePane.vue   # 无输出
$ npx vitest run src/components/filePaneWiring.test.js
 ✓ src/components/filePaneWiring.test.js (35 tests) 12ms
 Test Files  1 passed (1) / Tests  35 passed (35)
```

### 3b. `sftpMenuWiring.test.js`：两个行分支各自校验刷新 + 分派非空前提

改了什么：
- `菜单矩阵` 用例：在原 `blank`/`row` 切片之外，按锚点再切两段互不重叠的**远程行**（`menu.pane === 'remote'` → `'row-local-remote'`）与**本地行**（`'row-local-remote'` → `</ContextMenu>`），各自断言含一个 `doAction('refresh')`，并断言每段恰好 1 个刷新按钮（防锚点写错导致空切片/原文溢出）；顺带钉住各自独有动作（远程行 `download`、本地行 `upload`）。
- `菜单每个动作名都在 doAction/legacyAction 里有分派分支` 用例：补非空/可达性前提 —— `doAction` 体 `trim().length > 100` 且 `name === '` 分支数 `≥ 4`、体含 `name === 'refresh'`；`legacyAction` 体 `trim().length > 50`。原有 `names.length >= 6` 与 `toContain('upload-picked')` 保留。

RED 突变 A（删**远程行**刷新按钮，模板 1396 行）：
```
$ sed -i '1396d' src/views/SftpView.vue && npx vitest run src/views/sftpMenuWiring.test.js
   × 菜单矩阵：单一判据 + 各分支内容差异（审核 F22） 8ms
AssertionError: expected 'menu.pane === \'remote\'">\n         …' to contain 'doAction(\'refresh\')'
      Tests  1 failed | 11 passed (12)
$ node -e "…旧 row 切片判定: ", 旧断言（row 切片含 refresh）在该突变体下的判定: true   ← 旧断言仍绿
```

RED 突变 B（还原后删**本地行**刷新按钮，模板 1407 行）：
```
   × 菜单矩阵：单一判据 + 各分支内容差异（审核 F22） 8ms
AssertionError: expected ':key="\'row-local-remote\'">\n       …' to contain 'doAction(\'refresh\')'
      Tests  1 failed | 11 passed (12)
旧断言（row 切片含 refresh）在该突变体下的判定: true   ← 旧断言仍绿
```

RED 突变 C（`doAction` 体被切成空，模拟切片/正则写错）：
```
$ # 令 `async function doAction(name) {` 下一行立刻闭合，本体移入 doActionImpl
   × doAction 的 refresh 分支在 sel.single 之前（审核 FF6…） 4ms
   × 空选择防御只约束依赖选中的动作…（审核 F2） 2ms
   × 菜单每个动作名都在 doAction/legacyAction 里有分派分支… 1ms
AssertionError: doAction 体为空（切片正则写错？）: expected 0 to be greater than 100
      Tests  3 failed | 9 passed (12)
```
> 诚实边界：突变 C 下**同时**红了另外两条既有断言（它们也依赖 `doActionBody()` 切片），所以这条前提的增量价值主要是"失败原因直白"（`doAction 体为空`）而不是"唯一抓住"。它的另一半价值是 `names.length >= 6` 那类"空集恒绿"防护（上一轮 `feff286` 已加，本轮保留）。

GREEN：
```
$ cp /tmp/SftpView.vue.bak2 src/views/SftpView.vue && git diff --stat src/views/SftpView.vue   # 无输出
$ npx vitest run src/views/sftpMenuWiring.test.js
 Test Files  1 passed (1) / Tests  12 passed (12)
```

### 3c. `FilePane.vue`：注释（仅注释）
在 `onPaneContext` 分支 1 的注释后追加（无代码改动）：

```
//    ⚠️ 但**仅**豁免这个选择器不足以让行内可编辑控件（如重命名输入框）拿回原生右键菜单：
//    这类控件位于文件行 <li> 内，li 自己的 @contextmenu.prevent 处理器会先 preventDefault()
//    并把行级菜单 emit 出去 ⇒ 还必须同时豁免它所在那个 li 的处理器（本轮只记注释，未实现）。
```

该注释块受 `filePaneWiring.test.js:290` 的"注释护栏"用例保护（要求块内含 `stopPropagation`/`pointer-events`/`原生`），新增文字不影响该断言；`filePaneWiring.test.js` 35 passed 即为验证。

---

## 4. 全量证据与门禁

```bash
cd frontend && npx vitest run          # 38 files / 489 tests 全绿（基线 488，+1 为本轮新增用例）
GOCACHE=$PWD/.gocache go test ./ -count=1   # ok sshore 0.670s
gofmt -l app.go app_test.go main.go internal   # 无输出
GOCACHE=$PWD/.gocache make ci          # EXIT=0
```

`make ci` 原文（`/tmp/make-ci-deferred.log`，节选全绿行）：
```
✓ built in 1.19s
ok  	sshore	1.843s
ok  	sshore/internal/config	1.362s
ok  	sshore/internal/forward	1.886s
ok  	sshore/internal/importer	1.028s
ok  	sshore/internal/localfs	1.025s
ok  	sshore/internal/namepattern	1.018s
ok  	sshore/internal/osutil	6.443s
ok  	sshore/internal/preset	1.018s
ok  	sshore/internal/sftp	19.564s
ok  	sshore/internal/sshconn	1.019s
ok  	sshore/internal/sync	16.283s
ok  	sshore/internal/update	14.814s
ok  	sshore/internal/watch	6.832s
 Test Files  38 passed (38)
      Tests  489 passed (489)
EXIT=0
```

`make ci` 之后 `git status --short` 为空（无 `frontend/dist`、无 `runtime/**` 权限位抖动残留——本轮没有跑 `wails build/generate`，只跑了 `npm run build`）。报告文件是本轮唯一的后续写入（纯 docs，`make ci` 不读 `docs/**`）。

---

## 5. 不修项与理由

1. **`SearchOverlay` 的 `emit('failed')` 不分 scope** —— 规格 §8.4 第 13 项本就要求失败**无条件** emit；`verifyRemote` 的 `!connected` 早退已经挡住"未连接"这一唯一会误导的场景。收窄 scope 属于改规格语义，收益为零。
2. **`recoverConnection` 失败原因恒为「连接未建立」** —— 真因已由 `connect()` 内部的 `err(e)` 先落一条日志（不是丢失，只是没透传）；要透传真因必须改 `connect` 的契约（它现在不返回值也不 rethrow，正是审核 FF14 要求的"成功/失败判据只能是 `connected.value`"），改动面大于收益。
3. **`frontend/wailsjs/runtime/**` 的 755/644 mode 抖动** —— 是 `wails build`/`generate` 生成器行为、内容零改动；当前工作树已还原为已提交状态（本轮 `git status` 干净），本轮也不触发再生成。手动 chmod 回去会在下次生成时复现。
4. **锁外并发 `saveConfig()` 的 last-writer-wins** —— 既有行为（非本轮引入）；`SaveConfig` 的 tmp 名带 PID + 序号，并发写不会损坏文件，最坏是最后一次覆盖前一次，而各写入函数的 `changed` 门控已把多数无谓写盘消掉。
5. **`internal/sftp/ctrl_test.go` 的分派判别力依赖进程能列真实 `/`** —— 属 **loud failure**：Windows/受限容器上会**假红**（测试报错），而不是静默退化漏检；在 CI 的 linux 与 windows 目标上都有真实 `/`，收窄它会削弱分派覆盖而不换来等价保证。

---

## 6. 顾虑与边界

- **简报前提偏差（第 1 条）**：`connect()` 记账的生产修复已在起点 HEAD 之前的 `d955146` 落地（见 §1）。本轮因此**没有第 1 条的生产 diff**，只有回归断言；"修复前能红"用**生产者形态突变**取证（`d955146^` 的裸调用），而不是拿一份未修改的生产代码假装红。若验收要求"本轮必须有该处生产 diff"，需要重新裁定（例如把它当作历史修复复核项）。
- **空路径的残留边界（第 2 条）**：修后若将来真的出现空路径调用，前端 `stores/locations.js` 仍会在 IPC 成功后把 `localRecents`/`localRecents[0]` 镜像成空值，于是**内存镜像与盘上短暂不一致**（后端 no-op、前端已镜像）。今天该调用点不可达（前端恒传真路径；`goLocal`/`loadLocal` 的入参来自 `resolvePathInput`/`Cwd`），且修前行为是"直接落盘毁掉 `LastLocalDir`"，严格更差。若将来要把这条也收干净，正确做法是在 `locations.addLocalRecent` 加空路径早退（未做：超出本轮派单范围）。
- **突变证据的性质**：第 1、3a、3b 条的 RED 都来自"临时突变生产文件 → 跑红 → `cp` 还原 → `git diff --stat` 无输出"。`cp` 还原不受 harness 的文件版本护栏保护，故每次都额外用 `git diff --stat` 确认逐字还原（均无输出），并在最终 `make ci` 里再验一次全绿。
- **未执行**：Windows/WebView2 与真机右键（沿用既有边界声明，本轮无 UI 行为改动）。
