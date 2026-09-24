# SFTP 面板交互打磨（路径栏可编辑 · 「..」双击 · 深搜/位置列表收敛）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除 SFTP 双栏的四处交互割裂——「..」行与普通目录行手势不一致、本地/远程路径只读、深搜浮层打开即全量列出（最多 500 条）、位置下拉里的「最近」最长 20 行——并全部用可回归的测试钉住。

**Architecture:** 全部改动落在前端 3 个生产组件（FilePane.vue / SftpView.vue / SearchOverlay.vue）+ 3 个新的纯函数模块（pathbar / positions / searchbox）。交互判定与分组/截断逻辑抽成纯函数单测，跨文件形状（事件接线、模板结构）沿用仓库既有的「读源码不变量」测试，浮层的初始渲染沿用 @renderToString@ 的 SSR 断言。**不改 Go 逻辑、不改绑定、不加依赖。**

**Tech Stack:** Vue 3.5 + Pinia + vite 7 + vitest 4（无 jsdom / 无 @vue/test-utils，见 `TransferQueue.render.test.js` 的说明）。

**Spec:** 无独立 spec（本轮是用户验收反馈的直接修复）；实现口径沿用 `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md` 的决策 6（单击=单选/双击=打开）、决策 19/34（位置下拉的分区与顺序）、决策 17（深搜的深度/条数语义）。

## 设计决策（已获用户确认，均取 A）

| # | 问题 | 决策 |
|---|---|---|
| Q1 | 「..」单击 vs 其它行双击 | **A**：「..」= 单击置高亮并清空当前选中（等价于点空白处），双击打开。**「..」绝不进入 @selKeys`**，删除/上传/下载/重命名路径零改动 |
| Q2 | 位置下拉太长 | **A**：原生 @<select>` 换成自定义分区弹层，固定顺序 预设 → 磁盘 → 书签 → 最近，每区默认最多 8 行，超出显示「展开全部 N」，顶部筛选框，弹层整体限高 60vh |
| Q3 | 深搜列表太长 | **A**：打开浮层**不再自动搜索**（空 pattern 不查询，显示空态提示）；结果区限高 46vh。500 条命中上限与所有结束态文案保留 |
| Q4 | 路径只读 | **A**：路径栏改为常驻可编辑输入框（外观与现在的路径文本一致，hover/聚焦才显边框），Enter 跳转、Esc/失焦还原；跳转采用「乐观提交 + 失败整体回滚」 |

## Global Constraints

- **无新增依赖**：不引入 jsdom / @vue/test-utils 类库；组件渲染测试沿用 `TransferQueue.render.test.js` 的做法（`@vue/server-renderer` 的 `renderToString`）。
- **不改 Go 逻辑**：唯一的 Go 文件改动是 `internal/preset/preset.go` 第 19 行的注释指向（`REMOTE_HOME` 常量从 SftpView.vue 迁到 `frontend/src/utils/pathbar.js`）。
- **FilePane 对外事件契约不变**：`select / open / context / visible / focus / dragstart / dropon / clear / action / pick-position / toggle-bookmark / search` 全部保留原语义；**只新增一个 `navigate`**（路径栏提交）。
- **「..」不进选择集**：修改后 `SftpView.vue` 的 `runBatchFor`（直接取 `[...s.keys]`）、`removeSelected`、`renameItem`、`hiddenSelectedCount` 必须零改动。
- **空 pattern 不得发起深搜**：后端把空 pattern 解释为「匹配全部」（权威语义在 `internal/namepattern/match.go` 第 16-19 行的 `if pattern == "" { return true }`，`internal/sftp/search.go` 第 32 行的注释是同一约定；`internal/localfs/search.go` 第 71 行只是调用点），打开即查询 = 无条件全量扫描 + 最多 500 条命中。
- **每区默认 8 行**：`POSITION_SECTION_ROWS = 8`，是唯一常量来源，UI 不得再写死数字。
- **键盘守卫不回归**：路径栏与位置筛选框都是 `<input>`，`utils/keys.js` 的 `isEditableTarget` 已把 INPUT 排除，因此全局 Delete / Ctrl+A / Esc 不会在输入时误触发面板动作。**不需要新增用例**：既有 `frontend/src/utils/keys.test.js`（第 24-27 行）已经钉住「焦点在 INPUT 时不处理」。
- **样式要点**：全局 CSS 有 `html { text-align: center }`，新弹层必须显式 `text-align: left`（设置页排版整理那轮的教训）。
- 提交信息用简体中文、一次一块；**push 需另行获得用户指示**。

## File Structure

| 文件 | 责任 | 变动 |
|---|---|---|
| `frontend/src/utils/pathbar.js` | 路径栏输入的纯解析（本地/远程/`~`/引号/裸盘符/同路径） | Create |
| `frontend/src/utils/pathbar.test.js` | 上者的单测 | Create |
| `frontend/src/utils/positions.js` | 位置弹层的分区/截断/展开/筛选纯函数 | Create |
| `frontend/src/utils/positions.test.js` | 上者的单测 | Create |
| `frontend/src/utils/searchbox.js` | 深搜空模式守卫 + 空态文案常量 | Create |
| `frontend/src/utils/searchbox.test.js` | 上者的单测 | Create |
| `frontend/src/components/FilePane.vue` | 面板：路径栏输入框、「..」行手势、位置弹层 | Modify |
| `frontend/src/views/SftpView.vue` | 面板编排：`goLocal` / `goRemote` / `goRemoteHome` / `onNavigate`，模板接 `navigate` | Modify |
| `frontend/src/components/SearchOverlay.vue` | 深搜浮层：去掉打开即搜、空态提示、结果区限高 | Modify |
| `frontend/src/components/filePaneWiring.test.js` | FilePane/SftpView 形状不变量（读源码） | Create |
| `frontend/src/components/searchOverlay.test.js` | 浮层 SSR 初始渲染 + 不自动搜索的不变量 | Create |
| `internal/preset/preset.go` | 注释指向新常量位置 | Modify（仅注释） |

---

### Task 1: 路径栏可编辑（含失败回滚）

**Files:**
- Create: `frontend/src/utils/pathbar.js`
- Create: `frontend/src/utils/pathbar.test.js`
- Modify: `frontend/src/components/FilePane.vue`
- Modify: `frontend/src/views/SftpView.vue`
- Modify: `internal/preset/preset.go`:19（注释）
- Test: `frontend/src/components/filePaneWiring.test.js`（本任务只加「路径栏」一组）

**Interfaces:**
- Produces: `resolvePathInput({ pane, raw, hasHost, connected, current }) -> { kind, path?, reason? }`，`kind` ∈ `'noop' | 'navigate' | 'home' | 'connect'`；`reason` ∈ `'empty' | 'same' | 'no-host' | 'local-home'`
- Produces: `REMOTE_HOME = '~'`（从 SftpView.vue 迁出，Go 侧 `preset.RemoteHomeToken` 与之配对）
- Produces: FilePane 事件 `navigate`，载荷是**用户输入原文**（不做 trim，由父层解析）
- Consumes: `utils/localpath.js` 的 `normalize`（裸盘符 `D:` → `D:\`）

- [ ] **Step 1: 写失败测试（纯函数）**

Create `frontend/src/utils/pathbar.test.js`:

~~~js
import { describe, it, expect } from 'vitest'
import { resolvePathInput, REMOTE_HOME } from './pathbar'

const local = (raw, current = 'C:\\base') => resolvePathInput({ pane: 'local', raw, current, hasHost: false, connected: false })
const remote = (raw, opts = {}) => resolvePathInput({ pane: 'remote', raw, current: '/base', hasHost: true, connected: true, ...opts })

describe('resolvePathInput：路径栏输入 → 导航意图', () => {
  it('空串 / 纯空白 / null 一律 noop(empty)，不发请求', () => {
    expect(local('')).toEqual({ kind: 'noop', reason: 'empty' })
    expect(local('   ')).toEqual({ kind: 'noop', reason: 'empty' })
    expect(local(null)).toEqual({ kind: 'noop', reason: 'empty' })
  })

  it('兼容 Windows「复制为路径」的引号包裹', () => {
    expect(local('"C:\\Program Files"')).toEqual({ kind: 'navigate', path: 'C:\\Program Files' })
  })

  it('本地裸盘符归一为盘根（D: → D:\\）', () => {
    expect(local('D:')).toEqual({ kind: 'navigate', path: 'D:\\' })
  })

  it('本地与当前目录相同 → noop(same)', () => {
    expect(local('C:\\base')).toEqual({ kind: 'noop', reason: 'same' })
  })

  it('本地不做 ~ 展开：明确提示而不是把 ~ 交给后端去报错', () => {
    expect(local('~')).toEqual({ kind: 'noop', reason: 'local-home' })
  })

  it('远程 ~ 且已选主机 → home', () => {
    expect(remote(REMOTE_HOME)).toEqual({ kind: 'home' })
  })

  it('远程 ~ 但未选主机 → noop(no-host)', () => {
    expect(remote(REMOTE_HOME, { hasHost: false })).toEqual({ kind: 'noop', reason: 'no-host' })
  })

  it('远程未连接普通路径 → connect(path)，由 connect() 消费', () => {
    expect(remote('/var/log', { connected: false })).toEqual({ kind: 'connect', path: '/var/log' })
  })

  it('远程未连接时输入"当前显示的路径"也要去连，不被 same 短路', () => {
    expect(remote('/base', { connected: false })).toEqual({ kind: 'connect', path: '/base' })
  })

  it('远程已连接 → navigate(path)；与当前相同 → noop(same)', () => {
    expect(remote('/var/log')).toEqual({ kind: 'navigate', path: '/var/log' })
    expect(remote('/base')).toEqual({ kind: 'noop', reason: 'same' })
  })

  it('远程未选主机 → noop(no-host)', () => {
    expect(remote('/x', { hasHost: false, connected: false })).toEqual({ kind: 'noop', reason: 'no-host' })
  })
})
~~~

- [ ] **Step 2: 跑测试确认红**

Run: `cd frontend && npx vitest run src/utils/pathbar.test.js`
Expected: FAIL，报 `Failed to load url ./pathbar`（文件还不存在）

- [ ] **Step 3: 写实现**

Create `frontend/src/utils/pathbar.js`:

~~~js
// 路径栏（可编辑）输入的纯解析：不访问文件系统、不触碰 DOM，便于单测。
// 语义：
//   本地 —— 引号/空白归一、裸盘符补分隔符（复用 localpath.normalize）；不做 ~ 展开。
//   远程 —— '~' 是「主目录」sentinel（与 Go 侧 preset.RemoteHomeToken 字面量配对）；
//           未连接时返回 connect，由 SftpView 走既有的 pendingPath + connect() 分支。
import { normalize } from './localpath'

// 远程「主目录」预设的 sentinel：必须与 Go 侧 preset.RemoteHomeToken 字面量一致。
export const REMOTE_HOME = '~'

export function resolvePathInput({ pane, raw, hasHost, connected, current }) {
  let p = String(raw == null ? '' : raw).trim()
  // 兼容 Windows 资源管理器「复制为路径」的 "C:\Program Files" 形态
  if (p.length > 1 && p.startsWith('"') && p.endsWith('"')) p = p.slice(1, -1).trim()
  if (!p) return { kind: 'noop', reason: 'empty' }

  if (pane === 'local') {
    if (p === REMOTE_HOME) return { kind: 'noop', reason: 'local-home' }
    const path = normalize(p)
    if (path === current) return { kind: 'noop', reason: 'same' }
    return { kind: 'navigate', path }
  }

  if (!hasHost) return { kind: 'noop', reason: 'no-host' }
  if (p === REMOTE_HOME) return { kind: 'home' }
  // 未连接时先"去连"再比 same：否则在远程面板输入当前显示的路径（未连接时一般是 '/'）
  // 会被 same 短路，用户看到的是"回车没反应"。
  if (!connected) return { kind: 'connect', path: p }
  if (p === current) return { kind: 'noop', reason: 'same' }
  return { kind: 'navigate', path: p }
}
~~~

- [ ] **Step 4: 跑测试确认绿**

Run: `cd frontend && npx vitest run src/utils/pathbar.test.js`
Expected: PASS，11 个用例（`Tests  11 passed`）

- [ ] **Step 5: 写接线失败测试**

Create `frontend/src/components/filePaneWiring.test.js`（本任务先写第一组）：

~~~js
// FilePane / SftpView 的形状不变量（仓库无 jsdom / 无 @vue/test-utils，
// 组件事件无法派发，跨文件形状靠读源码钉死，仿 views/sftpDropWiring.test.js）。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const filePane = readFileSync(new URL('./FilePane.vue', import.meta.url), 'utf8')
const sftp = readFileSync(new URL('../views/SftpView.vue', import.meta.url), 'utf8')

describe('路径栏（可编辑）', () => {
  it('FilePane 用可编辑 input 承载路径，并上抛 navigate 原文', () => {
    expect(filePane).toContain('data-role="path"')
    expect(filePane).toMatch(/<input[^>]*data-role="path"/)
    expect(filePane).not.toContain('{{ path }}</span>')
    expect(filePane).toContain('@keyup.enter="commitPath"')
    expect(filePane).toContain("@keyup.esc=\"resetPath\"")
    expect(filePane).toContain("emit('navigate'")
    expect(filePane).toMatch(/defineEmits\(\[[^\]]*'navigate'/) // 新增事件必须在 emits 里声明（否则只有 dev 警告，测试不红）
  })

  it('SftpView 两个面板都接 navigate，并按面板分派', () => {
    expect(sftp).toContain("onNavigate('local'")
    expect(sftp).toContain("onNavigate('remote'")
    expect(sftp).toMatch(/async function onNavigate\(pane, raw\)/)
  })

  it('REMOTE_HOME 只有 pathbar.js 一个来源', () => {
    expect(sftp).not.toMatch(/const REMOTE_HOME = '~'/)
    expect(sftp).toContain("from '../utils/pathbar'")
  })
})
~~~

- [ ] **Step 6: 跑测试确认红**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js`
Expected: FAIL（FilePane 里还没有 `data-role="path"`，SftpView 里还没有 `onNavigate`）

- [ ] **Step 7: 确认本任务不引入 Task 4 的依赖**

Run: `grep -c "utils/positions" frontend/src/components/FilePane.vue`
Expected: `0` —— `positionGroups` 的 import 由 Task 4 Step 3 才加入。**这样每个 Task 结束时仓库都是可跑的绿状态**，而不是留下一棵引用了不存在模块的红树。

- [ ] **Step 8: 改 FilePane.vue —— 声明 navigate 事件 + 增加路径栏状态**

先把第 26 行的 `defineEmits([...])` 数组末尾补上 `'navigate'`：

~~~js
const emit = defineEmits(['select', 'open', 'context', 'visible', 'focus', 'dragstart', 'dropon', 'clear', 'action', 'pick-position', 'toggle-bookmark', 'search', 'navigate'])
~~~

> 为什么必须声明：Vue 的 `emit()` 在事件既不在 emits 选项、也没有 `onNavigate` prop 时会打印
> `Component emitted event "navigate" but it is neither declared in the emits option nor as an "onNavigate" prop.`，
> 并把 `onNavigate` 当成 fallthrough attr 挂到面板根节点上。功能不会断，但每个回车一条 dev 警告，且与"事件都在 emits 里声明"的仓库约定不符。

然后在 `const menuOpen = ref(false)` 之后插入：

~~~js
// ===== 路径栏（可编辑）=====
// draft 是输入框的唯一真源；prop.path 变化（导航完成、失败回滚）时同步回来，
// 这样 Enter 提交后无论成功还是被父层回滚，输入框显示的都与真实目录一致。
const pathDraft = ref(props.path || '')
function commitPath(e) {
  if (e && e.isComposing) return // 中文输入法里按 Enter 确认候选，不该触发跳转
  emit('navigate', pathDraft.value)
  // 失焦即回读 props.path：父层 noop / 失败时输入框自动回到真实目录；
  // 成功时 props.path 已被乐观提交改掉，回读到的就是新目录。
  if (e && e.target && e.target.blur) e.target.blur()
}
function resetPath() { pathDraft.value = props.path || '' }
// 「..」行的单击高亮：不并入 selKeys（父层的删除/传输直接取 selKeys，'..' 进去是危险语义）
const upActive = ref(false)
watch(() => props.path, (p) => {
  pathDraft.value = p || ''
  upActive.value = false
})
watch(() => props.selKeys.length, (n) => { if (n) upActive.value = false })
~~~

- [ ] **Step 9: 改 FilePane.vue —— 模板路径栏**

把第 101 行的 `<span class="curpath">{{ path }}</span>` 换成：

~~~html
      <input class="curpath" data-role="path" spellcheck="false" :value="pathDraft"
        @input="pathDraft = $event.target.value"
        @keyup.enter="commitPath" @keyup.esc="resetPath" @blur="resetPath(); focus = false"
        @focus="focus = true; emit('focus')" />
~~~

> `focus = false` 必须写在 input 自己的 `blur` 上：原生 blur 不冒泡，根节点第 98 行的 `@blur="focus = false"` 收不到子 input 的失焦，否则点过路径栏后面板的聚焦边框会一直亮着。

- [ ] **Step 10: 改 FilePane.vue —— 样式**

把 `.curpath` 规则替换为：

~~~css
.curpath { flex: 1; min-width: 0; font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); text-align: left; background: transparent; border: 1px solid transparent; border-radius: 3px; padding: 1px 6px; margin: 0 8px; font-family: inherit; }
.curpath:hover { border-color: var(--border); }
.curpath:focus { border-color: var(--accent); color: var(--text); outline: none; }
~~~

- [ ] **Step 11: 改 SftpView.vue —— 用共享常量，删掉本地定义**

- 第 29–31 行的注释与 `const REMOTE_HOME = '~'` 删除，改为在 import 区加：
~~~js
import { resolvePathInput, REMOTE_HOME } from '../utils/pathbar'
~~~
- 第 264 行 `if (path === REMOTE_HOME)` 保持可编译（常量已从 pathbar 导入）。
- 历史计划 `docs/superpowers/plans/2026-09-16-sftp-location-presets.md` 第 60/345 行仍写「REMOTE_HOME 定义在 SftpView.vue」：迁移后这两处表述失效（历史文档不回改，以 `utils/pathbar.js` 为准）。

- [ ] **Step 12: 改 SftpView.vue —— 新增跳转函数**

在 `async function pickPosition(pane, path)` 之前插入：

~~~js
// 乐观提交 + 失败整体回滚：输入框立刻反映用户输入，打不开时路径与目录内容一起复原，
// 并把后端原始错误写进日志面板（用户看得见，不是"点了没反应"）。
// 本地也要自己的请求序号：乐观提交引入了回滚，慢的失败响应若晚于更晚的成功返回，
// 就会把目录回滚成旧值（远程早有 remoteSeq 守这件事，本地原来没有回滚所以不需要）。
let localSeq = 0

async function goLocal(target) {
  const prevPath = localPath.value
  const prevItems = localItems.value
  const seq = ++localSeq
  localPath.value = target
  localLoading.value = true
  try {
    const items = (await ListLocal(target || '/')) || []
    if (seq !== localSeq) return
    localItems.value = items
    sel.clear(localSelection)
    await locations.addLocalRecent(target)
  } catch (e) {
    if (seq !== localSeq) return
    localPath.value = prevPath
    localItems.value = prevItems
    err('无法打开 ' + target + '：' + String((e && e.message) || e))
  } finally { if (seq === localSeq) localLoading.value = false }
}

// 远程同形；remoteSeq 的过期丢弃语义与 loadRemote 保持一致（旧主机的响应不得污染当前视图）。
async function goRemote(target) {
  const prevPath = remotePath.value
  const prevItems = remoteItems.value
  const seq = ++remoteSeq
  remotePath.value = target
  remoteLoading.value = true
  try {
    const items = (await SftpList(host.value, '', target || '/')) || []
    if (seq !== remoteSeq) return
    remoteItems.value = items
    sel.clear(remoteSelection)
    await locations.addRemoteRecent(host.value, target)
  } catch (e) {
    if (seq !== remoteSeq) return
    remotePath.value = prevPath
    remoteItems.value = prevItems
    err('无法打开 ' + target + '：' + String((e && e.message) || e))
  } finally { if (seq === remoteSeq) remoteLoading.value = false }
}

// 远程「主目录」：未连接时把 pendingPath 清空再 connect()（connect 的 home 分支），
// 已连接则直接解析 SftpHome 后跳转。行为变化（有意）：home 也会记入「最近」。
async function goRemoteHome() {
  if (!host.value) { err('请先选择主机'); return }
  if (!connected.value) { pendingPath.value = ''; await connect(); return }
  let home = '/'
  try { home = await SftpHome(host.value) } catch (e) { err(e); return }
  return goRemote(home)
}

// 面板头路径栏提交（唯一入口是 FilePane 的 @navigate）。
async function onNavigate(pane, raw) {
  const r = resolvePathInput({
    pane, raw,
    hasHost: !!host.value,
    connected: connected.value,
    current: pane === 'local' ? localPath.value : remotePath.value,
  })
  if (r.kind === 'noop') {
    if (r.reason === 'no-host') err('请先选择主机')
    else if (r.reason === 'local-home') err('本地路径不支持 ~ 写法，请填绝对路径')
    return
  }
  if (r.kind === 'home') return goRemoteHome()
  if (r.kind === 'connect') {
    pendingPath.value = r.path
    await connect()
    // connect() 失败时不改 remotePath、也不清 pendingPath（SftpView.vue:254-257 的既有行为）。
    // 残留的 pendingPath 会让**下一次**成功连接落到上次失败的目录，所以这里显式清掉。
    if (pendingPath.value === r.path) pendingPath.value = ''
    return
  }
  if (pane === 'local') return goLocal(r.path)
  return goRemote(r.path)
}
~~~

- [ ] **Step 13: 改 SftpView.vue —— pickPosition 复用同一批跳转**

把 `async function pickPosition(pane, path)` 的函数体替换为：

~~~js
async function pickPosition(pane, path) {
  if (pane === 'local') return goLocal(path)
  if (path === REMOTE_HOME) return goRemoteHome()
  if (host.value && connected.value) return goRemote(path)
  pendingPath.value = path
  await connect()
}
~~~

> 语义对照（两处**有意**的行为变化，均已写进文末风险表）：
> 1. 原实现对本地/已连接远程都会 `addRecent`，goLocal/goRemote 保留；未连接远程仍是 `pendingPath + connect()`。**新增**：home 跳转也会记一次「最近」（原来不记），因为 goRemoteHome 复用了 goRemote。
> 2. goLocal/goRemote 在成功后会 `sel.clear(...)`（原 pickPosition **不**清空选中）；这两个函数同时被路径栏 `onNavigate` 复用，所以**路径栏回车也会清空该面板选中**。这是与 `openLocal`/`openRemote` 对齐——spec 决策 6 的「切目录=清空该面板选中」；若不接受，把这两行 `sel.clear` 删掉即可，不影响其余逻辑。

- [ ] **Step 14: 改 SftpView.vue —— 模板接 navigate**

本地面板（第 880–888 行的 `<FilePane title="本地" …`）追加一行属性：

~~~html
          @navigate="onNavigate('local', $event)"
~~~

远程面板（第 891–899 行）追加：

~~~html
          @navigate="onNavigate('remote', $event)"
~~~

- [ ] **Step 15: 改 Go 注释**

`internal/preset/preset.go` 第 19 行：

~~~go
// 前端对应常量：frontend/src/utils/pathbar.js 的 REMOTE_HOME。
~~~

- [ ] **Step 16: 跑接线测试确认绿**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/utils/pathbar.test.js`
Expected: PASS（路径栏一组 3 个用例 + 纯函数 11 个）

- [ ] **Step 17: 跑全量前端测试，确认没打破既有 290 个用例**

Run: `cd frontend && npx vitest run`
Expected: 全绿；`Test Files` 数 = 22（基线 20 + pathbar + filePaneWiring），`Tests` ≥ 304（基线 290 + 14）

- [ ] **Step 18: 提交**

~~~bash
git add frontend/src/utils/pathbar.js frontend/src/utils/pathbar.test.js \
        frontend/src/components/FilePane.vue frontend/src/components/filePaneWiring.test.js \
        frontend/src/views/SftpView.vue internal/preset/preset.go
git commit -m "feat(web): SFTP 路径栏可编辑，回车跳转失败整体回滚"
~~~

---

### Task 2: 「..」行与普通目录行统一为「单击选中 + 双击打开」

**Files:**
- Modify: `frontend/src/components/FilePane.vue`
- Test: `frontend/src/components/filePaneWiring.test.js`（加「..」一组）

**Interfaces:**
- Consumes: 既有 `emit('open', { name: '..', isDir: true })` 与 `emit('clear')`（父层 `openLocal` / `openRemote` / `sel.clear` 已实现，无需改动）
- Produces: 无新事件

- [ ] **Step 1: 写失败测试**

在 `filePaneWiring.test.js` 追加：

~~~js
describe('「..」行与普通目录行统一', () => {
  it('up 行是「单击高亮 + 双击打开」，不再单击直接 open', () => {
    const i = filePane.indexOf('class="up"')
    expect(i, '找不到 up 行').toBeGreaterThan(-1)
    // 只截 up 行自身：上界 200 保证不会切进下一行的 @dblclick="emit('open', it)"
    const up = filePane.slice(i, i + 200)
    expect(up).toContain('@dblclick="openUp"')
    expect(up).toContain('@click="onUpClick"')
    expect(up).not.toContain("emit('open'")
  })

  it('「..」不进 selKeys：单击只清空选中并置本地高亮', () => {
    expect(filePane).toMatch(/function onUpClick\(\)\s*\{[^}]*emit\('clear'\)/)
    expect(filePane).toContain('const upActive = ref(false)')
    // sel.* 全在父层调用；父层绝不能把 '..' 放进选择集，否则 runBatchFor 会把它当成传输项
    expect(sftp).not.toMatch(/sel\.(single|toggle|rangeTo)\([^)]*'\.\.'/)
  })
})
~~~

- [ ] **Step 2: 跑测试确认红**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js`
Expected: FAIL（当前 up 行是 `class="up" @click="emit('open', …)`，且没有 `openUp`）

- [ ] **Step 3: 改 FilePane.vue —— script 增加两个函数**

在 Task 1 的 `upActive` 声明之后补：

~~~js
function onUpClick() {
  upActive.value = true
  emit('clear') // 与「点面板空白处」同义：单击「..」先取消当前选中
}
function openUp() { emit('open', { name: '..', isDir: true }) }
~~~

- [ ] **Step 4: 改 FilePane.vue —— 模板 up 行**

~~~html
        <li class="up" data-role="up" :class="{ sel: upActive }" @click="onUpClick" @dblclick="openUp">
          <span class="cell-name">📁 ..</span><span class="cell-size">—</span><span class="cell-time">—</span>
        </li>
~~~

- [ ] **Step 5: 改 FilePane.vue —— 样式（让 up 行的高亮真的可见）**

在 `.list li.up` 之后加一条（`.list li.up.sel` 的特异度本就高于 `.list li.up`，放前放后都赢；放在后面只是与既有规则聚在一起）：

~~~css
.list li.up.sel { background: var(--surface-hover); color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); border-bottom-color: var(--accent); }
~~~

- [ ] **Step 6: 跑测试确认绿**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js`
Expected: PASS（5 个用例）

- [ ] **Step 7: 人工确认没有连带伤害（读代码核对，不是跑测试）**

在 `frontend/src/views/SftpView.vue` 中确认以下函数**一行未改**：`runBatchFor`、`removeSelected`、`renameItem`、`hiddenSelectedCount`、`onKeydown`。把 `git diff frontend/src/views/SftpView.vue` 的输出贴进提交说明或验收报告。

- [ ] **Step 8: 提交**

~~~bash
git add frontend/src/components/FilePane.vue frontend/src/components/filePaneWiring.test.js
git commit -m "fix(web): SFTP「..」行与普通目录行统一为双击打开"
~~~

---

### Task 3: 深搜浮层不再「打开即全量搜索」，结果区限高

**Files:**
- Create: `frontend/src/utils/searchbox.js`
- Create: `frontend/src/utils/searchbox.test.js`
- Create: `frontend/src/components/searchOverlay.test.js`
- Modify: `frontend/src/components/SearchOverlay.vue`

**Interfaces:**
- Produces: `shouldSearch(pattern) -> boolean`、`EMPTY_PATTERN_HINT`（字符串常量）
- Consumes: 既有 `SftpSearch / LocalSearch / SearchCancel` 绑定与 `sftp:search-progress` 事件（不改）

- [ ] **Step 1: 写失败测试（纯函数）**

Create `frontend/src/utils/searchbox.test.js`:

~~~js
import { describe, it, expect } from 'vitest'
import { shouldSearch, EMPTY_PATTERN_HINT } from './searchbox'

describe('shouldSearch：空模式绝不发起深搜', () => {
  it('空串 / 空格 / null / undefined → false', () => {
    for (const v of ['', '   ', null, undefined]) expect(shouldSearch(v)).toBe(false)
  })

  it('通配符或关键词 → true（前后空白不算空）', () => {
    for (const v of ['*', '?', 'app.log', '  app  ']) expect(shouldSearch(v)).toBe(true)
  })

  it('空态文案可被 SSR 断言依赖', () => {
    expect(EMPTY_PATTERN_HINT).toContain('回车')
  })
})
~~~

- [ ] **Step 2: 跑测试确认红**

Run: `cd frontend && npx vitest run src/utils/searchbox.test.js`
Expected: FAIL，`Failed to load url ./searchbox`

- [ ] **Step 3: 写实现**

Create `frontend/src/utils/searchbox.js`:

~~~js
// 深搜浮层的输入判定与文案：纯函数 + 常量，便于单测（浮层本身只在 SSR 下渲染）。
// 为什么必须有这道守卫：后端的空 pattern 语义是「匹配全部」
// （internal/sftp/search.go 的注释、internal/namepattern.Match('')），
// 打开浮层就查询 = 无条件全量扫描远端目录 + 最多 500 条命中——既烧远端 IO，又让结果区变成一屏路径墙。
export const EMPTY_PATTERN_HINT = '输入名称或通配符（支持 * ?）后回车搜索'

export function shouldSearch(pattern) {
  return String(pattern == null ? '' : pattern).trim().length > 0
}
~~~

- [ ] **Step 4: 跑测试确认绿**

Run: `cd frontend && npx vitest run src/utils/searchbox.test.js`
Expected: PASS（3 个用例）

- [ ] **Step 5: 写浮层的不变量测试（先在旧实现上确认红）**

Create `frontend/src/components/searchOverlay.test.js`:

~~~js
// SearchOverlay：SSR 初始渲染 + 「打开不自动搜索」的结构不变量。
// 仓库无 jsdom / 无 @vue/test-utils：可渲染的断言走 renderToString，跨文件形状靠读源码。
import { describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import SearchOverlay from './SearchOverlay.vue'
import { EMPTY_PATTERN_HINT } from '../utils/searchbox'

vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }))
vi.mock('../../wailsjs/go/main/App', () => ({
  SftpSearch: async () => ({}), LocalSearch: async () => ({}), SearchCancel: () => {},
}))

const src = readFileSync(new URL('./SearchOverlay.vue', import.meta.url), 'utf8')

describe('SearchOverlay：打开时不得自动全量搜索', () => {
  it('SSR 打开态渲染「先输入」提示，且没有任何命中行', async () => {
    const html = await renderToString(createSSRApp({
      render: () => h(SearchOverlay, { visible: true, root: '/', scope: 'remote', host: 'h' }),
    }))
    expect(html).toContain(EMPTY_PATTERN_HINT)
    expect(html).not.toContain('<li')
  })

  it('打开分支不再调用 run()', () => {
    const start = src.indexOf('watch(() => props.visible')
    // 结束点必须带 '() => {'：文件顶部的 import 行也含 'onUnmounted('，不带后缀会切出空串让断言假绿
    const end = src.indexOf('onUnmounted(() => {', start)
    expect(start).toBeGreaterThan(-1)
    expect(end).toBeGreaterThan(start)
    const watchBlock = src.slice(start, end)
    expect(watchBlock).not.toContain('run()')
    expect(watchBlock).toContain('EMPTY_PATTERN_HINT')
  })

  it('run() 用 shouldSearch 守卫空模式', () => {
    const body = src.slice(src.indexOf('async function run()'), src.indexOf('function cancel()'))
    expect(body).toContain('shouldSearch(')
  })

  it('结果区限高，浮层不再吃掉整屏', () => {
    expect(src).toMatch(/\.hits\s*\{[^}]*max-height/)
    expect(src).toContain('data-role="hits"')
  })
})
~~~

- [ ] **Step 6: 跑测试确认红**

Run: `cd frontend && npx vitest run src/components/searchOverlay.test.js`
Expected: FAIL：提示文案不存在（旧实现打开就 `run()`，初始渲染出「命中 0 项」而不是提示），且 `.hits` 没有 `max-height`

- [ ] **Step 7: 改 SearchOverlay.vue —— script**

- import 区加：
~~~js
import { EMPTY_PATTERN_HINT, shouldSearch } from '../utils/searchbox'
~~~
- 状态区加（初值就是提示文案：SSR/首帧直接是空态，不闪「命中 0 项」）：
~~~js
const hint = ref(EMPTY_PATTERN_HINT)
~~~
- `run()` 加守卫并维护 hint（下面是**整函数**，只有前 4 行是新增的）：
~~~js
async function run() {
  // 空模式不查询：后端把空 pattern 解释为「匹配全部」（internal/sftp/search.go 的注释），
  // 一打开就查 = 无条件全量扫描 + 最多 500 条命中，见 utils/searchbox.js。
  if (!shouldSearch(pattern.value)) { hint.value = EMPTY_PATTERN_HINT; return }
  hint.value = ''
  const id = 's' + (++seq)
  running.value = true
  cancelled.value = false
  errorText.value = ''
  hits.value = []
  scanned.value = 0
  const req = { id, root: props.root, pattern: pattern.value, maxDepth: 5, limit: 500 }
  try {
    const out = props.scope === 'remote'
      ? await SftpSearch({ ...req, host: props.host })
      : await LocalSearch(req)
    if (id !== 's' + seq) return
    hits.value = (out && out.hits) || []
    scanned.value = (out && out.scanned) || 0
    unreadable.value = (out && out.unreadable) || 0
    truncated.value = !!(out && out.truncated)
    cancelled.value = !!(out && out.cancelled)
  } catch (e) {
    if (id === 's' + seq) errorText.value = String(e)
  } finally {
    if (id === 's' + seq) running.value = false // 过期响应不得清掉新搜索的运行态
  }
}
~~~
- `watch(() => props.visible, …)` 整块替换为（打开分支补齐重置项、**删掉 `run()` 调用**；关闭分支保持原样）：
~~~js
watch(() => props.visible, (v) => {
  if (v) {
    // 聚焦输入框：@keyup.esc 挂在遮罩上，只有焦点在子树里才会触发（spec §8.2 要求 Esc 可关）。
    nextTick(() => { const el = document.querySelector('.search-overlay input'); if (el) el.focus() })
    pattern.value = ''
    hits.value = []
    scanned.value = 0
    unreadable.value = 0
    truncated.value = false
    cancelled.value = false
    errorText.value = ''
    // 打开即提示「先输入」；这里不再自动发起搜索 —— 空模式全量搜索正是「列表太长」的根因
    hint.value = EMPTY_PATTERN_HINT
    running.value = false // 不自动搜索了，运行态必须是干净的（防将来重入留下 stale 的"搜索中…"）
    offProgress = EventsOn('sftp:search-progress', (p) => { if (p && p.id === 's' + seq) scanned.value = p.scanned })
  } else {
    // 关闭即取消：否则远端 BFS 会继续空跑（spec §8.2）。
    SearchCancel('s' + seq)
    running.value = false
    if (offProgress) { offProgress(); offProgress = null }
  }
})
~~~
> 两条约束：① 打开分支**只能**提示、不得调用 `run()`；② 上面这行注释里**不要出现 `run()` 字面量**——`searchOverlay.test.js` 用 `expect(watchBlock).not.toContain('run()')` 钉住「打开不自动搜索」，注释里的字面量会让它变红（这也是它写成"不再自动发起搜索"而不是"不再调用 run()"的原因）。

- [ ] **Step 8: 改 SearchOverlay.vue —— 模板与样式**

汇总行改为三态：

~~~html
      <p v-if="errorText" class="err">{{ errorText }}</p>
      <p v-else-if="hint" class="dim" data-role="hint">{{ hint }}</p>
      <p v-else class="dim">
        命中 {{ hits.length }} 项<template v-if="truncated"> · 触发上限（最多 500 条）</template>
        <template v-if="unreadable"> · {{ unreadable }} 个目录不可读</template>
        <template v-if="cancelled"> · 已取消（结果为已扫描部分）</template>
      </p>
      <ul class="hits" data-role="hits">
~~~

样式：

~~~css
.hits { list-style: none; margin: 0; padding: 0; overflow: auto; max-height: 46vh; font-family: monospace; font-size: var(--fs-12); }
~~~

> 面板仍是 `max-height: 80vh`，但有了结果区上限后，满屏 500 条不再把浮层撑到 80% 屏高。

- [ ] **Step 9: 跑测试确认绿**

Run: `cd frontend && npx vitest run src/components/searchOverlay.test.js src/utils/searchbox.test.js`
Expected: PASS（4 + 3 个用例）

- [ ] **Step 10: 跑全量前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全绿

- [ ] **Step 11: 提交**

~~~bash
git add frontend/src/utils/searchbox.js frontend/src/utils/searchbox.test.js \
        frontend/src/components/SearchOverlay.vue frontend/src/components/searchOverlay.test.js
git commit -m "fix(web): 深搜浮层不再空模式自动全量搜索，结果区限高"
~~~

---

### Task 4: 位置下拉改分区弹层，最近默认 8 条 + 展开全部

**Files:**
- Create: `frontend/src/utils/positions.js`
- Create: `frontend/src/utils/positions.test.js`
- Modify: `frontend/src/components/FilePane.vue`
- Test: `frontend/src/components/filePaneWiring.test.js`（加「位置弹层」一组）

**Interfaces:**
- Produces: `POSITION_SECTION_ROWS = 8`、`positionGroups({ presets, disks, bookmarks, recents, expanded, filter }) -> [{ key, label, total, rows: [{ key, label, path }] }]`
- Consumes: FilePane 既有的 `presets / disks / bookmarks / recents` props（由 `locations.js` 过滤后传入，本任务不改 store）
- 对外仍是 `emit('pick-position', path)`（含远程 `~` sentinel）；父层 `pickPosition` 见 Task 1 Step 13

- [ ] **Step 1: 写失败测试（纯函数）**

Create `frontend/src/utils/positions.test.js`:

~~~js
import { describe, it, expect } from 'vitest'
import { positionGroups, POSITION_SECTION_ROWS } from './positions'

const presets = [{ name: '主目录', path: '~', host: '' }, { name: '日志', path: '/var/log', host: 'prod' }]
const disks = [{ name: 'C:\\', path: 'C:\\' }, { name: 'D:\\', path: 'D:\\' }]
const bookmarks = [{ name: 'a', path: '/a' }]
const recents = Array.from({ length: 20 }, (_, i) => ({ host: 'prod', path: '/r' + i }))

describe('positionGroups：分区顺序 / 截断 / 展开 / 筛选', () => {
  it('固定顺序 预设 → 磁盘 → 书签 → 最近，空组不渲染', () => {
    expect(positionGroups({ presets, disks, bookmarks, recents }).map((g) => g.key))
      .toEqual(['preset', 'disk', 'bookmark', 'recent'])
    expect(positionGroups({ bookmarks }).map((g) => g.key)).toEqual(['bookmark'])
    expect(positionGroups()).toEqual([])
  })

  it('每区默认最多 8 行，total 是筛选后的真实条数', () => {
    const g = positionGroups({ recents }).find((x) => x.key === 'recent')
    expect(g.rows).toHaveLength(POSITION_SECTION_ROWS)
    expect(g.total).toBe(20)
  })

  it('展开全部后给出该区全量', () => {
    const g = positionGroups({ recents, expanded: { recent: true } }).find((x) => x.key === 'recent')
    expect(g.rows).toHaveLength(20)
    expect(g.total).toBe(20)
  })

  it('筛选同时匹配名称与路径，大小写不敏感', () => {
    const byPath = positionGroups({ presets, recents, filter: 'var/log' })
    expect(byPath.map((g) => g.key)).toEqual(['preset'])
    expect(byPath[0].rows.map((r) => r.label)).toEqual(['日志'])
    const byName = positionGroups({ bookmarks, filter: 'A' })
    expect(byName[0].rows.map((r) => r.label)).toEqual(['a'])
  })

  it('无命中 → 空数组（UI 渲染「没有匹配的位置」）', () => {
    expect(positionGroups({ presets, disks, bookmarks, recents, filter: 'zzz' })).toEqual([])
  })

  it('预设 key 保留 name+path+host 语义，同名同路径不同 host 不撞 key', () => {
    const two = [{ name: 'x', path: '/p', host: 'a' }, { name: 'x', path: '/p', host: 'b' }]
    const g = positionGroups({ presets: two })[0]
    expect(new Set(g.rows.map((r) => r.key)).size).toBe(2)
  })
})
~~~

- [ ] **Step 2: 跑测试确认红**

Run: `cd frontend && npx vitest run src/utils/positions.test.js`
Expected: FAIL，`Failed to load url ./positions`

- [ ] **Step 3: 写实现 + FilePane 补 import**

Create `frontend/src/utils/positions.js`:

~~~js
// 位置弹层的分组 / 截断 / 展开 / 筛选：纯函数，输入是四个位置数据源，输出可直接渲染的分组。
// 顺序固定为 预设 → 磁盘 → 书签 → 最近（spec 决策 34），空组不渲染。
// 「最近」在后端最多存 20 条（internal/config/store.go 的 maxRecent），
// 但下拉里默认只展示 POSITION_SECTION_ROWS 行，其余靠「展开全部」显式取用——
// 这是本轮「位置里的最近太长」的修复点。
export const POSITION_SECTION_ROWS = 8

const norm = (s) => String(s == null ? '' : s).toLowerCase()

function matches(row, filter) {
  if (!filter) return true
  const q = norm(filter)
  return norm(row.label).includes(q) || norm(row.path).includes(q)
}

export function positionGroups({ presets = [], disks = [], bookmarks = [], recents = [], expanded = {}, filter = '' } = {}) {
  const defs = [
    // key 语义必须沿用原 select 的 :key：预设是 name+path+host（后端去重键），
    // 磁盘是 path（盘符天然唯一），书签是 path，最近是 path（同路径已去重）。
    { key: 'preset', label: '预设', rows: presets.map((p) => ({ key: 'p' + p.name + p.path + (p.host || ''), label: p.name, path: p.path })) },
    { key: 'disk', label: '磁盘', rows: disks.map((d) => ({ key: 'd' + d.path, label: d.name, path: d.path })) },
    { key: 'bookmark', label: '书签', rows: bookmarks.map((b) => ({ key: 'b' + b.path, label: b.name || b.path, path: b.path })) },
    { key: 'recent', label: '最近', rows: recents.map((r) => ({ key: 'r' + r.path, label: r.path, path: r.path })) },
  ]
  const out = []
  for (const d of defs) {
    const hit = d.rows.filter((row) => matches(row, filter))
    if (!hit.length) continue
    out.push({ key: d.key, label: d.label, total: hit.length, rows: expanded[d.key] ? hit : hit.slice(0, POSITION_SECTION_ROWS) })
  }
  return out
}
~~~

同时在 `FilePane.vue` 的 import 区加（Task 1 Step 7 提到的那一行）：

~~~js
import { positionGroups } from '../utils/positions'
~~~

- [ ] **Step 4: 跑测试确认绿**

Run: `cd frontend && npx vitest run src/utils/positions.test.js`
Expected: PASS（6 个用例）

- [ ] **Step 5: 加接线失败测试**

在 `filePaneWiring.test.js` 追加：

~~~js
describe('位置弹层', () => {
  it('不再用原生 select，改用分区弹层 + 纯函数分组', () => {
    expect(filePane).not.toMatch(/<select class="pos"/)
    expect(filePane).toContain("from '../utils/positions'")
    expect(filePane).toContain('positionGroups(')
    expect(filePane).toContain('data-role="position"')
  })

  it('对外契约不变：仍是 emit(pick-position, path)，且旧 onPick 已删', () => {
    expect(filePane).toContain("emit('pick-position'")
    expect(filePane).not.toContain('onPick($event)')
  })

  it('弹层显式左对齐（全局 html{text-align:center} 会把它带歪）', () => {
    expect(filePane).toMatch(/\.posmenu\s*\{[^}]*text-align:\s*left/)
  })

  // 钉住"弹层不能被 .pane 的 overflow:hidden 裁掉"：absolute 版在默认窗口下就被裁，
  // 回归时只表现为"展开全部后看不到最后几行"，没有这几条断言会静默通过。
  it('弹层用 position:fixed + 现算锚点（absolute 会被 .pane{overflow:hidden} 裁掉）', () => {
    expect(filePane).toMatch(/\.posmenu\s*\{[^}]*position:\s*fixed/)
    expect(filePane).toMatch(/\.posmenu\s*\{[^}]*box-sizing:\s*border-box/)
    expect(filePane).toContain('getBoundingClientRect()')
    expect(filePane).toContain('r.right - POS_MENU_WIDTH') // 锚点与宽度必须同源
    expect(filePane).toContain("width: POS_MENU_WIDTH + 'px'")
  })

  it('点面板内非弹层区域收起弹层（焦点会落到 .pane 自身，focusout 判不出"离开"）', () => {
    expect(filePane).toContain('@mousedown="onPaneMouseDown"')
    expect(filePane).toMatch(/function onPaneMouseDown\(e\)\s*\{[^}]*closest\('\.pos-wrap'\)/)
  })
})
~~~

- [ ] **Step 6: 跑测试确认红**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js`
Expected: FAIL（还是 `<select class="pos">`）

- [ ] **Step 7: 改 FilePane.vue —— script**

- 第 2 行的 import 改成 `import { ref, computed, watch, onUnmounted } from 'vue'`（新增 onUnmounted）。
- 删掉 `function onPick(e)`（原生 select 的清零技巧不再需要），加弹层状态：

~~~js
// ===== 位置弹层（📍 位置）=====
// 为什么是 position: fixed + 打开时现算坐标，而不是 absolute 挂在按钮下方：
// .pane 是 overflow: hidden（FilePane.vue:170），absolute 弹层会被面板裁掉——默认 720 高的窗口里，
// 传输队列非空时面板只剩 ~380px，而 60vh 的菜单要 432px，底部几行会永久够不到。
// fixed 不受祖先 overflow 裁剪，坐标由按钮 rect 现算：**定位方式**与 ContextMenu.vue 一致，
// 但**关闭机制刻意不同**——不用 document 全局监听（SftpView 被 KeepAlive 缓存会留残留），
// 改用面板自身的 focusout + mousedown。
const posOpen = ref(false)
const posFilter = ref('')
const posExpanded = ref({})
const posAnchor = ref({ left: 0, top: 0 })
// 弹层宽度只有这一个来源：模板内联 style 用它，锚点也用它。
// 配合 .posmenu 的 box-sizing: border-box，"右对齐按钮"才是精确的（否则 padding+border 会让外框宽 10px）。
const POS_MENU_WIDTH = 260
const groups = computed(() => positionGroups({
  presets: props.presets, disks: props.disks, bookmarks: props.bookmarks, recents: props.recents,
  expanded: posExpanded.value, filter: posFilter.value,
}))
function closePos() {
  if (!posOpen.value) return
  posOpen.value = false
  window.removeEventListener('resize', closePos) // 窗口尺寸变了锚点就失效：直接收起（幂等，可重复调用）
}
function togglePos(e) {
  if (posOpen.value) { closePos(); return }
  const r = e.currentTarget.getBoundingClientRect()
  posAnchor.value = { left: Math.max(4, Math.round(r.right - POS_MENU_WIDTH)), top: Math.round(r.bottom + 2) }
  posFilter.value = ''
  posExpanded.value = {}
  posOpen.value = true
  window.addEventListener('resize', closePos)
}
function pickPos(path) { closePos(); emit('pick-position', path) }
function expandGroup(key) { posExpanded.value = { ...posExpanded.value, [key]: true } }
// 焦点离开「面板 + 弹层」这一整棵子树才收起；面板内按钮之间移动不收起。
// 用面板自身的 focusout 而不是 document 全局监听：SftpView 被 KeepAlive 缓存，全局监听会留残留。
function onPaneFocusOut(e) {
  if (e && e.relatedTarget && e.currentTarget.contains(e.relatedTarget)) return
  closePos()
}
// 点面板内**非弹层**区域就收起：.pane 自身是 tabindex="0"，点文件行 / 列表空白时浏览器
// 把焦点落到 .pane 上，relatedTarget 就是面板本身 —— 只靠 focusout 判"是否离开子树"会恒为真，
// position:fixed 的弹层就会长期盖在列表上。弹层内（.pos-wrap）的点击不收起。
function onPaneMouseDown(e) {
  if (e.target && e.target.closest && e.target.closest('.pos-wrap')) return
  closePos()
}
function onPaneKeydown(e) { if (e.key === 'Escape') closePos() }
onUnmounted(() => window.removeEventListener('resize', closePos))
~~~

> 说明：`closePos` 幂等（未打开时直接 return），所以 `onPaneKeydown`/`onPaneMouseDown` 可以无条件调用。弹层开着时切走标签（KeepAlive 的 deactivated 不触发 `onUnmounted`）会让 resize 监听一直留到下次激活，届时它只做一次 `closePos()`（幂等、无副作用）——**不是**"只影响一次窗口缩放"，这里是描述上的取舍而非正确性问题。

> 按钮上**不加** `.stop`：点击继续冒泡到 SftpView 的 window click（`outsideClick → closeMenu`），
> 否则右键菜单开着时点「📍 位置」不会关闭它；而 `ContextMenu.vue` 的 z-index 同样是 1000、DOM 又更靠后，会盖住本弹层。
> 弹层内部（`.posmenu`）保留 `.stop`，避免点条目时把外面的事件再打一遍。

- [ ] **Step 8: 改 FilePane.vue —— 模板 head 里的 select → 弹层**

把第 102–120 行的 `<select class="pos" …>…</select>` 换成：

~~~html
      <div class="pos-wrap">
        <button class="pos" data-role="position" :class="{ on: posOpen }" title="位置" @click="togglePos">📍 位置 ▾</button>
        <div v-if="posOpen" class="posmenu" tabindex="-1" @click.stop
          :style="{ left: posAnchor.left + 'px', top: posAnchor.top + 'px', width: POS_MENU_WIDTH + 'px' }">
          <input class="posfilter" v-model="posFilter" placeholder="筛选位置…" />
          <template v-for="g in groups" :key="g.key">
            <div class="poslabel">
              <span>{{ g.label }}</span>
              <button v-if="g.total > g.rows.length" class="more" @click="expandGroup(g.key)">展开全部 {{ g.total }}</button>
            </div>
            <button v-for="o in g.rows" :key="o.key" class="positem" :title="o.path" @click="pickPos(o.path)">{{ o.label }}</button>
          </template>
          <div v-if="!groups.length" class="posempty">没有匹配的位置</div>
        </div>
      </div>
~~~

> `tabindex="-1"` 不是装饰：收起逻辑挂在面板根的 `focusout` 上，点击弹层里的非焦点元素（分区标题、内边距）时 `relatedTarget` 会是 `null`；给弹层自身可聚焦能力后，这类点击会把焦点落到弹层上、被判为"仍在子树内"，弹层不会莫名收起。


- [ ] **Step 9: 改 FilePane.vue —— 根节点挂 focusout/keydown**

第 98 行的 `<div class="pane ui-panel" :class="{ focus }" tabindex="0" …>` 改为：

~~~html
  <div class="pane ui-panel" :class="{ focus }" tabindex="0"
    @focus="focus = true; emit('focus')" @blur="focus = false"
    @focusout="onPaneFocusOut" @mousedown="onPaneMouseDown" @keydown="onPaneKeydown">
~~~

- [ ] **Step 10: 改 FilePane.vue —— 样式**

把 `.pos { max-width: 150px; font-size: var(--fs-11); }` 替换为：

~~~css
.pos-wrap { position: relative; }
.pos { max-width: 150px; font-size: var(--fs-11); }
.pos.on { border-color: var(--accent); color: var(--accent); }
/* position: fixed + z-index 1000：与 ContextMenu.vue 的既有做法一致；对话框浮层是 2000，菜单在其之下。
   宽度定长 260px 是为了和 Step 7 里 "r.right - 260" 的锚点算法对齐。 */
.posmenu { position: fixed; z-index: 1000; box-sizing: border-box; max-height: 60vh; overflow: auto; text-align: left; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 4px; box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4); }
.posfilter { width: 100%; font-size: var(--fs-11); margin-bottom: 4px; }
.poslabel { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 4px 6px 2px; font-size: var(--fs-11); color: var(--text-faint); }
.poslabel .more { font-size: var(--fs-11); }
.positem { display: block; width: 100%; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; text-align: left; padding: 4px 8px; background: none; border: none; color: var(--text); font-size: var(--fs-12); cursor: pointer; }
.positem:hover { background: var(--surface-hover); }
.posempty { padding: 6px 8px; font-size: var(--fs-12); color: var(--text-faint); }
~~~

- [ ] **Step 11: 跑测试确认绿**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js src/utils/positions.test.js`
Expected: PASS（10 + 6 个用例）

- [ ] **Step 12: 跑全量前端测试 + Go 侧无影响的确认**

Run: `cd frontend && npx vitest run`
Expected: 全绿；`Test Files` = 25，`Tests` ≥ 324（基线 290 + 34）

Run: `go vet ./... && go test ./internal/preset/ -count=1`
Expected: `ok  sshore/internal/preset`（注释改动不影响行为）

- [ ] **Step 13: 提交**

~~~bash
git add frontend/src/utils/positions.js frontend/src/utils/positions.test.js \
        frontend/src/components/FilePane.vue frontend/src/components/filePaneWiring.test.js
git commit -m "fix(web): 位置下拉改分区弹层，最近默认 8 条并支持展开全部"
~~~

---

### Task 5: 门禁 + 真机验收 + 沉淀

**Files:**
- Create: `docs/superpowers/reports/2026-09-20-sftp-pane-ux-polish-acceptance.md`（验收报告，含原始输出与截图清单）
- Create: `.superpowers/sdd/2026-09-20-sftp-pane-ux-polish/progress.md`（逐 Task 进展 + 证据路径，仓库既有约定：`.superpowers/sdd/<task>/progress.md`）
- Create: `.superpowers/sdd/2026-09-20-sftp-pane-ux-polish/task-<N>-report.md`（每个 Task 的命令与原始输出，仿 `.superpowers/sdd/2026-09-16-sftp-location-presets/task-1-report.md`）

- [ ] **Step 1: 本地全量门禁**

Run: `make ci`
Expected: RC=0；前端 build 成功、`go vet` 干净、`go test ./... -race -count=1` 全部 `ok`、vitest 全绿。把输出尾部贴进报告。

- [ ] **Step 2: 真界面验收（逐条截图，Windows 客户机或本机 GUI 均可）**

| # | 操作 | 期望 | 禁用行为 |
|---|---|---|---|
| 1 | 单击「..」 | 该行高亮、原选中项取消，**不跳转** | 不得进上级目录 |
| 2 | 双击「..」 | 跳到上级目录 | — |
| 3 | 在本地路径栏输入一个不存在的路径后回车 | 错误写入日志面板，路径栏与列表**整体回滚**原值 | 不得停在错误路径或清空列表 |
| 4 | 远程路径栏输入 `~` 回车；再从「📍 位置」弹层点「主目录」（同为 `~`） | 两条入口都跳远端 home | 未连接时不得静默无反应（应连上后落 home） |
| 5 | 先构造 >8 条最近，再点「📍 位置」 | 分区显示；「最近」默认 ≤8 行并有「展开全部 N」；点「展开全部 N」后**能滚到最后一行且整行可见**（弹层不被面板裁掉） | 不得出现整屏几十行；不得出现"最后几行看不到、只能盲滚" |
| 6 | 点开 🔍 | 头部显示「输入名称或通配符（支持 * ?）后回车搜索」、无任何命中行，且 3 秒内**不**出现「搜索中… 已扫描 N 个目录」 | **不得**自动搜索。可观测判据：无「搜索中…」文案、无进度刷新（硬证据可临时给 `BatchBackend.ListMany` 加一行日志确认未被调用，插桩不入库） |
| 7 | 输入 `*` 回车 | 正常深搜，结果区内部滚动、最多 500 条 | 结果区不得撑满整屏 |
| 8 | 弹层打开时点本面板的文件行 / 列表空白 / ☆ / 🔍 | 弹层立即收起 | 不得长期盖在列表上（只能靠点另一面板、Esc 或再点按钮才关） |

- [ ] **Step 3: 报告落盘**

把 Step 1 的命令与输出、Step 2 的截图文件名与观察结论写进 `docs/superpowers/reports/2026-09-20-sftp-pane-ux-polish-acceptance.md`（未实测项必须写「未验证」，不得用「应该/大概」）。
同时按仓库 `.superpowers/sdd/` 约定落两份过程证据：`progress.md`（逐 Task 一行：做了什么 / 命令 / 结果 / 证据文件路径）与 `task-<N>-report.md`（该 Task 的原始命令输出，未实测项同样标注）。

- [ ] **Step 4: 提交（push 需另行授权）**

~~~bash
git add docs/superpowers/reports/2026-09-20-sftp-pane-ux-polish-acceptance.md .superpowers/sdd/2026-09-20-sftp-pane-ux-polish/
git commit -m "docs: 记录 SFTP 面板交互打磨的验收证据"
~~~

---

## 风险与回滚

| 风险 | 说明 | 缓解 |
|---|---|---|
| 路径栏「乐观提交」把自己写坏 | 失败时必须同时回滚 path 与 items | 单测覆盖 noop 分支；回滚分支靠真机 Step 2 第 3 条验证；远程沿用 remoteSeq 丢弃过期响应 |
| 「..」被误当普通项 | 若进 selKeys，Delete/传输会拿到 `..` | 本方案不进 selKeys，且不变量测试禁止 `sel.single/toggle(…, '..')` |
| 位置弹层收起时机 | 面板内点击（文件行/列表空白）会让焦点落到 `.pane` 自身（`tabindex="0"`），只靠 `focusout` 判"是否离开子树"恒为真 → 弹层长期盖在列表上 | 面板节点额外挂 `@mousedown`：点击不在 `.pos-wrap` 内即 `closePos()`；弹层 `tabindex="-1"` 承接弹层内空白点击；**不**引入 document 全局监听（KeepAlive 残留） |
| **位置弹层被面板裁剪** | `.pane { overflow: hidden }`（FilePane.vue:170）会裁掉 absolute 弹层：默认 720 高窗口下传输队列非空时面板仅 ~380px，而 60vh 菜单需 432px，absolute 版的底部几行（含滚动条）会被裁掉，只能靠滚轮盲滚 | 弹层用 `position: fixed` + 打开时按按钮 `getBoundingClientRect()` 现算坐标、宽度取自 `POS_MENU_WIDTH`（与 CSS 同源、`box-sizing: border-box`），window resize 或点面板非弹层区域即收起 |
| 深搜行为变化被误读为「搜索坏了」 | 打开不再自动搜 | 空态文案显式说明「输入后回车搜索」；文档与验收报告都写明这是有意行为 |
| 行为变化：home 现在会记一次「最近」 | goRemoteHome 复用 goRemote | 已在 Task 1 Step 13 显式标注；若不接受，改为 goRemoteHome 里直连 SftpList 不记 recent |
| 行为变化：**路径栏回车 / 位置下拉**的跳转都会清空该面板选中 | goLocal/goRemote 成功后 `sel.clear(...)`，原 pickPosition 不清空 | 与 `openLocal`/`openRemote` 一致（spec 决策 6「切目录=清空该面板」）；若不接受，删掉这两行 `sel.clear` 即可 |

回滚：Task 3 / Task 4 的提交可单点 `git revert`；**Task 1 与 Task 2 必须作为一个原子单元回滚**——Task 2 依赖 Task 1 引入的 `upActive` 与 Task 1 新建的 `filePaneWiring.test.js`，单独 revert Task 1 会让 Task 2 引用未定义变量、并删掉承载其断言的测试文件。逆序回滚：Task 5 → 4 → 3 → (2+1)。不涉及 Go 逻辑与产物，不影响 v0.7.1。

## 自检（写计划时已核对）

1. **覆盖**：Q1→Task 2，Q2→Task 4，Q3→Task 3，Q4→Task 1；四项都有独立的红→绿步骤与验收条目。
2. **占位符**：无 TBD/TODO；每个代码步骤都给了可直接粘贴的实现与测试。
3. **类型一致**：`resolvePathInput` 的 `kind/`reason` 取值在 pathbar.js、SftpView.onNavigate、pathbar.test.js 三处一致；`positionGroups` 的 `{ key, label, total, rows }` 在 positions.js、FilePane 模板、positions.test.js 三处一致；`EMPTY_PATTERN_HINT` 在 searchbox.js、SearchOverlay.vue、searchOverlay.test.js 三处一致。
4. **顺序**：Task 1 只依赖 pathbar.js；FilePane 对 positions.js 的 import 明确放到 Task 4，保证每个 Task 结束时仓库都是可跑的绿状态。
