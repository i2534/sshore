# SFTP P1：选择与批量投递（多选 · 批量 · 拖拽）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 SFTP 模块支持多选、批量投递（下载 / 上传 / 删除 / 移动）与四条拖拽路径，且每一步都可独立测试。

**Architecture:** 交互编排留在 `SftpView.vue`；把可测决策（选择模型、冲突策略、落点判定、键盘守卫、队列状态）抽成 `src/utils/*.js` 纯函数并单测；后端只**新增** `RemoveRecursive` / `PutRecursive` / `localfs.Copy` 与若干 ctx-free 绑定，沿用既有 `sftp -b` 批处理 + ControlMaster 复用，不引入新依赖、不开新命令通道。

**Tech Stack:** Go 1.26 + Wails v2.15.0 + Vue 3 (`<script setup>`) + Pinia + vitest；测试沿用 `internal/sftp` 的 `fakeRunner` mock 与一次性本地 sshd（`e2e/test_local.sh`）。

**Spec:** `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`（本计划实现其 P1；P2 见 `2026-09-16-sftp-p2-filter-search-locations.md`）

## Global Constraints

- **工具栏零新增控件**：批量操作只出现在各自面板的「已选 N 项 ▾」。
- **批量一律顺序执行**，不并发；单项失败不中断，结束汇总 `成功 N · 跳过 N · 失败 N`。
- **冲突策略三选一**：另存副本 / 跳过已存在项（**默认**）/ 覆盖全部；**单文件手势不弹窗、有意直接覆盖**（这是新行为，现有代码没有任何判重）。
- **远端目录删除必须走 `RemoveRecursive`**；删除批每条命令加 `-` 前缀；**不用 `@`**；路径含 `* ? [` **拒绝删除**。
- **判重用原始 items（未经 showAll 过滤）**；被过滤隐藏的项仍留在选中集合，且必须如实显示数量。
- **不做**传输进度 / 取消 / 重试 / 续传 / 校验（P3）；**不放**点了没反应的假按钮。
- **不改** `internal/sftp` 既有方法语义；**禁止** `internal/sftp → internal/watch` 依赖。
- 绑定变更后必须执行 `wails generate module -tags webkit2_41`（Makefile 用 `-skipbindings`，不会自动更新）。
- 新结构体**都带 json tag**；绑定签名里的类型**带包名限定**（如 `sftp.Item`）。
- **不得**使用 `DragAndDrop.DisableWebViewDrop`（会全局关掉 webview 拖放接收，连本源拖入一起废掉）。
- 提交信息用简体中文；每个任务结束时 `go vet ./...` 与 `cd frontend && npx vitest run` 必须通过。

---

## 任务与文件总览

| 任务 | 产出文件 | 可独立验收的交付物 |
|---|---|---|
| 1 | `e2e/test_local.sh`（改）、spec 的 R1/R2/R9 | 三条未证实项的实测结论 |
| 2 | `frontend/src/utils/selection.js` + 测试 | 多选模型（含锚点/区间/清理） |
| 3 | `frontend/src/utils/keys.js` + 测试 | 键盘分派与守卫 |
| 4 | `frontend/src/utils/queue.js` + 测试、`TransferQueue.vue`（改） | 队列状态映射与增强列 |
| 5 | `frontend/src/utils/batch.js` + 测试 | 任务规划、冲突策略、失败汇总 |
| 6 | `frontend/src/utils/dnd.js` + 测试 | 拖拽载荷与落点判定 |
| 7 | `internal/localfs/copy.go` + 测试 | 本地复制与子树判定 |
| 8 | `internal/sftp/ctrl.go`（改）、`ctrl_test.go`（改） | 远端递归删除 + `-` 前缀 + glob 拒绝 |
| 9 | `internal/sftp/ctrl.go`（改） | 远端递归上传 |
| 10 | `app.go`、`frontend/wailsjs/**` | 新绑定 + 绑定重生成 |
| 11 | `main.go` | 系统文件拖入通道 |
| 12 | `frontend/src/components/ConflictDialog.vue` | 冲突对话框组件 |
| 13 | `frontend/src/components/FilePane.vue`（改） | 多选渲染、聚焦态、可见顺序上抛 |
| 14 | `frontend/src/views/SftpView.vue`（改） | 批量编排 + 键盘分派 |
| 15 | `frontend/src/views/SftpView.vue`（改） | 四条拖拽路径接线 |
| 16 | `README.md`（改） | 功能说明与全量回归 |

---

### Task 1: e2e 探针：确定三条未证实语义

**Files:**
- Modify: `e2e/test_local.sh`（在 `== sync e2e (Go side) ==` 之前插入一段）
- Modify: `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`（R1/R2/R9 结论回填）

**Interfaces:**
- Consumes: 既有变量 `$TMPD` / `$HOME/.ssh/config`（别名 `e2e-test` 已在脚本前段就绪）
- Produces: 三个 verdict 行 `PUT-R:` / `RENAME-OVERWRITE:` / `GLOB-RM:`，供 Task 9（`PutRecursive` 语义）与 spec 的 R1/R2/R9 使用

- [ ] **Step 1: 写探针（未跑前先看它失败/报错也是合法的）**

在 `e2e/test_local.sh` 的 `echo "== sync e2e (Go side) =="` 这一行**之前**插入：

~~~bash
echo "== PROBE A/B/C: sftp 语义（供 spec R1/R2/R9 取值） =="
PB="$TMPD/home/probe"
SFTP_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=no -o IdentitiesOnly=yes
  -F "$HOME/.ssh/config" -o "UserKnownHostsFile /dev/null")
run_batch() { sftp "${SFTP_OPTS[@]}" -b "$1" e2e-test 2>&1 || true; }

# --- PROBE A: put -r 目标同名目录已存在：并入还是嵌套？ ---
rm -rf "$PB"; mkdir -p "$PB/localdir/sub" "$PB/remote/localdir"
echo A > "$PB/localdir/a.txt"
echo BB > "$PB/localdir/sub/b.txt"
echo EXISTING > "$PB/remote/localdir/existing.txt"
echo LOCAL_ONLY > "$PB/remote/localdir/a.txt"
printf 'put -r %s %s\n' "$PB/localdir" "$PB/remote" > "$PB/pa.bat"
PA_OUT="$(run_batch "$PB/pa.bat")"
echo "--- probe A output ---"; echo "$PA_OUT"
if [ -d "$PB/remote/localdir/localdir" ]; then
  echo "PUT-R: nest"
elif [ -f "$PB/remote/localdir/a.txt" ] && [ -f "$PB/remote/localdir/sub/b.txt" ]; then
  echo "PUT-R: merge"
  echo "PUT-R-OVERWRITE: $(cat "$PB/remote/localdir/a.txt")"
else
  echo "PUT-R: error-or-other"
fi

# --- PROBE B: rename 覆盖已存在目标 ---
printf 'OLD' > "$PB/r_old.txt"; printf 'NEW' > "$PB/r_new.txt"
printf 'rename %s %s\n' "$PB/r_old.txt" "$PB/r_new.txt" > "$PB/pb.bat"
PB_OUT="$(run_batch "$PB/pb.bat")"
echo "--- probe B output ---"; echo "$PB_OUT"
if [ -f "$PB/r_new.txt" ]; then echo "RENAME-OVERWRITE: yes content=$(cat "$PB/r_new.txt")"; else echo "RENAME-OVERWRITE: target-gone"; fi
[ -f "$PB/r_old.txt" ] && echo "RENAME-SOURCE: still-exists" || echo "RENAME-SOURCE: moved"

# --- PROBE C: rm 路径含 glob 元字符是否被远端展开 ---
mkdir -p "$PB/glob"
echo ONE > "$PB/glob/lit*name.txt"
echo TWO > "$PB/glob/litZZname.txt"
printf 'rm %s\n' "$PB/glob/lit*name.txt" > "$PB/pc.bat"
PC_OUT="$(run_batch "$PB/pc.bat")"
echo "--- probe C output ---"; echo "$PC_OUT"
echo "GLOB-RM: star-file=$([ -f "$PB/glob/lit*name.txt" ] && echo kept || echo deleted) zz-file=$([ -f "$PB/glob/litZZname.txt" ] && echo kept || echo deleted)"
~~~

- [ ] **Step 2: 跑探针**

Run: `make e2e`（需要 `/usr/sbin/sshd`、`ssh-keygen`、`python3`）
Expected: 出现 `PUT-R: merge|nest|error-or-other`、`RENAME-OVERWRITE: ...`、`GLOB-RM: star-file=... zz-file=...`，其余既有测试仍全部 PASS。

- [ ] **Step 3: 把结论回填 spec**

把三个 verdict 的确切值写进 spec 的风险行：`R1`（PUT-R 的取值）、`R2`（RENAME-OVERWRITE）、`R9`（GLOB-RM 的 star/zz 归属）。每条写成「实测结论（日期）：<verdict>」。
若 `GLOB-RM` 显示 `zz-file=deleted`，说明**远端 glob 确实会误删**，Task 8 的保守拒绝是必需的，不得放开。

- [ ] **Step 4: Commit**

~~~bash
git add e2e/test_local.sh docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md
git commit -m "test(e2e): 用一次性 sshd 实测 put -r/rename 覆盖/rm glob 三条语义"
~~~

---

### Task 2: 选择模型 `selection.js`

**Files:**
- Create: `frontend/src/utils/selection.js`
- Test: `frontend/src/utils/selection.test.js`

**Interfaces:**
- Produces: `createSelection()` → `{ keys: Set<string>, anchor: string|null }`；`single/toggle/rangeTo/all/clear/remove/isSelected/selectedNames`（Task 13/14 直接调用）

- [ ] **Step 1: 写失败测试**

~~~js
import { describe, it, expect } from 'vitest'
import { createSelection, single, toggle, rangeTo, all, clear, remove, isSelected, selectedNames }
  from './selection'

describe('selection', () => {
  it('单击：替换为单项并设锚点', () => {
    const s = createSelection()
    single(s, 'b.txt')
    expect([...s.keys]).toEqual(['b.txt'])
    expect(s.anchor).toBe('b.txt')
  })

  it('Ctrl 切换：加入/移除并移动锚点', () => {
    const s = createSelection()
    single(s, 'a'); toggle(s, 'b')
    expect(isSelected(s, 'a')).toBe(true)
    expect(isSelected(s, 'b')).toBe(true)
    expect(s.anchor).toBe('b')
    toggle(s, 'a')
    expect(isSelected(s, 'a')).toBe(false)
  })

  it('Shift 区间：按可见顺序取锚点到目标之间，且不改锚点', () => {
    const s = createSelection()
    const vis = ['a', 'b', 'c', 'd']
    single(s, 'b'); rangeTo(s, vis, 'd')
    expect([...s.keys].sort()).toEqual(['b', 'c', 'd'])
    expect(s.anchor).toBe('b')
  })

  it('区间反向选择等价', () => {
    const s = createSelection()
    const vis = ['a', 'b', 'c']
    single(s, 'c'); rangeTo(s, vis, 'a')
    expect([...s.keys].sort()).toEqual(['a', 'b', 'c'])
  })

  it('锚点已不可见时退化为单项选择', () => {
    const s = createSelection()
    single(s, 'gone'); rangeTo(s, ['a', 'b'], 'b')
    expect([...s.keys]).toEqual(['b'])
  })

  it('全选/清空/删除后清理', () => {
    const s = createSelection()
    all(s, ['a', 'b'])
    expect(s.keys.size).toBe(2)
    remove(s, ['a'])
    expect([...s.keys]).toEqual(['b'])
    remove(s, ['b'])
    expect(s.anchor).toBe(null)
    clear(s)
    expect(s.keys.size).toBe(0)
  })

  it('selectedNames 按传入 items 顺序返回', () => {
    const s = createSelection()
    all(s, ['b'])
    expect(selectedNames(s, [{ name: 'a' }, { name: 'b' }])).toEqual(['b'])
  })
})
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/selection.test.js`
Expected: FAIL —— `Failed to resolve import "./selection"`

- [ ] **Step 3: 最小实现**

~~~js
// 多选模型：纯函数、无 DOM 依赖。
// keys 是"选中集合"，可包含当前被过滤隐藏的项（面板头负责如实显示数量）。
// anchor 是 Shift 区间的起点；改锚点只发生在 single/toggle。
export function createSelection() { return { keys: new Set(), anchor: null } }

export function single(sel, key) {
  sel.keys = new Set([key])
  sel.anchor = key
}

export function toggle(sel, key) {
  const next = new Set(sel.keys)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  sel.keys = next
  sel.anchor = key
}

// visibleKeys：当前可见（showAll ∩ filter，排序后）的名字数组，作为区间唯一真源。
export function rangeTo(sel, visibleKeys, key) {
  const anchor = sel.anchor
  if (!anchor || !visibleKeys.includes(anchor) || !visibleKeys.includes(key)) {
    single(sel, key)
    return
  }
  const i = visibleKeys.indexOf(anchor)
  const j = visibleKeys.indexOf(key)
  const lo = Math.min(i, j)
  const hi = Math.max(i, j)
  sel.keys = new Set(visibleKeys.slice(lo, hi + 1))
}

export function all(sel, visibleKeys) { sel.keys = new Set(visibleKeys) }

export function clear(sel) { sel.keys = new Set(); sel.anchor = null }

export function isSelected(sel, key) { return sel.keys.has(key) }

// 删除/移动完成后调用：把已消失的项移出集合，锚点失效则清空。
export function remove(sel, keys) {
  const next = new Set(sel.keys)
  for (const k of keys) next.delete(k)
  sel.keys = next
  if (sel.anchor && !next.has(sel.anchor)) sel.anchor = null
}

export function selectedNames(sel, items) {
  return items.filter((it) => sel.keys.has(it.name)).map((it) => it.name)
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/selection.test.js`
Expected: PASS（7 个用例）

- [ ] **Step 5: Commit**

~~~bash
git add frontend/src/utils/selection.js frontend/src/utils/selection.test.js
git commit -m "feat(sftp): 多选模型 selection.js（锚点/区间/清理）与单测"
~~~

---

### Task 3: 键盘分派与守卫 `keys.js`

**Files:**
- Create: `frontend/src/utils/keys.js`
- Test: `frontend/src/utils/keys.test.js`

**Interfaces:**
- Produces: `actionFor(ev)` → `'delete' | 'select-all' | 'escape' | null`；`isEditableTarget(el)` → boolean（Task 14 的 keydown 处理只依赖这两个）

- [ ] **Step 1: 写失败测试**

~~~js
import { describe, it, expect } from 'vitest'
import { actionFor, isEditableTarget } from './keys'

function ev(key, opts = {}) {
  return { key, ctrlKey: false, metaKey: false, target: opts.target || { tagName: 'LI' } }
}
function input() {
  return { tagName: 'INPUT', isContentEditable: false, closest: () => null }
}

describe('keys 键盘分派', () => {
  it('Delete 映射为 delete', () => {
    expect(actionFor(ev('Delete'))).toBe('delete')
  })
  it('Ctrl/Cmd+A 映射为 select-all', () => {
    const e1 = { key: 'a', ctrlKey: true, target: { tagName: 'UL' } }
    const e2 = { key: 'A', metaKey: true, target: { tagName: 'UL' } }
    expect(actionFor(e1)).toBe('select-all')
    expect(actionFor(e2)).toBe('select-all')
  })
  it('Escape 映射为 escape', () => {
    expect(actionFor(ev('Escape'))).toBe('escape')
  })
  it('焦点在输入框时一律不处理（守卫）', () => {
    expect(actionFor(ev('Delete', { target: input() }))).toBe(null)
    expect(actionFor({ key: 'a', ctrlKey: true, target: input() })).toBe(null)
  })
  it('焦点在浮层/对话框内时不处理', () => {
    const inDialog = { tagName: 'DIV', isContentEditable: false, closest: (s) => (s.includes('.ui-overlay') ? {} : null) }
    expect(isEditableTarget(inDialog)).toBe(true)
    expect(actionFor(ev('Delete', { target: inDialog }))).toBe(null)
  })
  it('其他按键不处理', () => {
    expect(actionFor(ev('x'))).toBe(null)
  })
})
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/keys.test.js`
Expected: FAIL —— 无法解析 `./keys`

- [ ] **Step 3: 最小实现**

~~~js
// 键盘分派与守卫：纯函数，输入是 KeyboardEvent 形状的对象，便于单测。
// 守卫规则（spec §6.1）：焦点在输入控件或浮层/对话框内时，Delete/Ctrl+A/Esc 交还输入控件。
const EDITABLE_TAGS = new Set(['INPUT', 'TEXTAREA', 'SELECT'])

export function isEditableTarget(el) {
  if (!el) return false
  if (EDITABLE_TAGS.has(el.tagName)) return true
  if (el.isContentEditable) return true
  if (typeof el.closest !== 'function') return false
  return !!(el.closest('.ui-overlay') || el.closest('[role="dialog"]') || el.closest('.search-overlay'))
}

export function actionFor(ev) {
  if (!ev || isEditableTarget(ev.target)) return null
  if (ev.key === 'Delete') return 'delete'
  if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') return 'select-all'
  if (ev.key === 'Escape') return 'escape'
  return null
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/keys.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add frontend/src/utils/keys.js frontend/src/utils/keys.test.js
git commit -m "feat(sftp): 键盘分派与输入焦点守卫 keys.js 与单测"
~~~

---

### Task 4: 队列状态映射 `queue.js` + TransferQueue 增强

**Files:**
- Create: `frontend/src/utils/queue.js`
- Test: `frontend/src/utils/queue.test.js`
- Modify: `frontend/src/components/TransferQueue.vue:1-54`

**Interfaces:**
- Produces: `statusClass(status)`（`'done'|'err'|'skip'|'doing'`）、`summarizeQueue(transfers)` → `{ ok, skipped, failed, running, total }`、`directionArrow(direction)`、`failureText(t)`
- 传输记录形状（Task 14 写入）：`{ direction:'download'|'upload'|'copy'|'move', name, src, dst, size, status:'处理中'|'完成'|'跳过'|'失败', reason?, startedAt, elapsed? }`

- [ ] **Step 1: 写失败测试**

~~~js
import { describe, it, expect } from 'vitest'
import { statusClass, summarizeQueue, directionArrow } from './queue'

describe('queue 辅助', () => {
  it('状态映射：跳过必须是独立态，不能落进处理中', () => {
    expect(statusClass('完成')).toBe('done')
    expect(statusClass('失败')).toBe('err')
    expect(statusClass('跳过')).toBe('skip')
    expect(statusClass('处理中')).toBe('doing')
    expect(statusClass('')).toBe('doing')
  })

  it('汇总统计四类条数', () => {
    const list = [{ status: '完成' }, { status: '完成' }, { status: '跳过' }, { status: '失败' }, { status: '处理中' }]
    expect(summarizeQueue(list)).toEqual({ ok: 2, skipped: 1, failed: 1, running: 1, total: 5 })
  })

  it('方向箭头覆盖四类', () => {
    expect(directionArrow('download')).toBe('⬇')
    expect(directionArrow('upload')).toBe('⬆')
    expect(directionArrow('copy')).toBe('📋')
    expect(directionArrow('move')).toBe('➡')
  })
})
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/queue.test.js`
Expected: FAIL —— 无法解析 `./queue`

- [ ] **Step 3: 最小实现**

~~~js
export function statusClass(status) {
  if (status === '完成') return 'done'
  if (status === '失败') return 'err'
  if (status === '跳过') return 'skip'
  return 'doing'
}

export function summarizeQueue(transfers) {
  const out = { ok: 0, skipped: 0, failed: 0, running: 0, total: transfers.length }
  for (const t of transfers) {
    if (t.status === '完成') out.ok++
    else if (t.status === '跳过') out.skipped++
    else if (t.status === '失败') out.failed++
    else out.running++
  }
  return out
}

export function directionArrow(direction) {
  if (direction === 'download') return '⬇'
  if (direction === 'upload') return '⬆'
  if (direction === 'copy') return '📋'
  if (direction === 'move') return '➡'
  return '•'
}

export function failureText(transfer) {
  return `${transfer.src || transfer.name} → ${transfer.dst || ''}：${transfer.reason || '未知原因'}`
}
~~~

- [ ] **Step 4: 改 TransferQueue.vue 使用它**

把 `frontend/src/components/TransferQueue.vue` 的 `statusClass` 函数**删除**，改为顶部引入：

~~~js
import { computed } from 'vue'
import { statusClass, summarizeQueue, directionArrow } from '../utils/queue'
const summary = computed(() => summarizeQueue(props.transfers))
function line(t) { return directionArrow(t.direction) + ' ' + (t.src || t.name) + ' → ' + (t.dst || '') }
~~~

模板里每条改为显示 `line(t)`、`fmtSize(t.size)`、`fmtElapsed(t)`、`t.status`；在列表**下方**加汇总行：

~~~html
<div v-if="transfers.length" class="qsum">
  成功 {{ summary.ok }} · 跳过 {{ summary.skipped }} · 失败 {{ summary.failed }} · 进行中 {{ summary.running }}
  <button v-if="summary.failed" class="copy" @click="$emit('copy-failures')">复制失败清单</button>
</div>
~~~

样式补 `.status.skip { color: var(--text-faint); }` 与 `.qsum { padding: 3px 8px; color: var(--text-faint); display: flex; gap: 8px; align-items: center; }`；组件需 `defineEmits(['copy-failures'])`。

- [ ] **Step 5: 跑测试 + 构建**

Run: `cd frontend && npx vitest run src/utils/queue.test.js && npm run build`
Expected: 测试 PASS；构建成功（证明模板语法无误）

- [ ] **Step 6: Commit**

~~~bash
git add frontend/src/utils/queue.js frontend/src/utils/queue.test.js frontend/src/components/TransferQueue.vue
git commit -m "feat(sftp): 传输队列支持跳过态/方向/汇总，状态映射抽成纯函数"
~~~

---

### Task 5: 批量任务规划与冲突策略 `batch.js`

**Files:**
- Create: `frontend/src/utils/batch.js`
- Test: `frontend/src/utils/batch.test.js`

**Interfaces:**
- Consumes: 无（纯函数）
- Produces:
  - `POLICY_SKIP='skip'` / `POLICY_OVERWRITE='overwrite'` / `POLICY_RENAME='rename'`
  - `planTasks({ direction, names, sourceDir, targetDir, isDirMap })` → `Task[]`，`Task = { name, src, dst, isDir }`
  - `classify(tasks, existingNames)` → `{ clean: Task[], conflicts: Task[] }`
  - `copyName(name, takenNames)` → `'a (2).txt'`
  - `applyPolicy(conflicts, policy, takenNames)` → `{ run: Task[], skipped: Task[] }`
  - `needsConfirm({ conflictCount, hiddenSelected })` → boolean
  - `summarize(results)` → `{ ok, skipped, failed, failures: string[] }`

- [ ] **Step 1: 写失败测试**

~~~js
import { describe, it, expect } from 'vitest'
import { planTasks, classify, copyName, applyPolicy, needsConfirm, summarize, POLICY_SKIP, POLICY_RENAME }
  from './batch'

const tasks = planTasks({ direction: 'upload', names: ['a.txt', 'b/c'], sourceDir: '/l', targetDir: '/r' })
  .map((t) => ({ ...t, isDir: t.name === 'b/c' }))

describe('batch 冲突策略', () => {
  it('planTasks 拼出 src/dst', () => {
    const t = planTasks({ direction: 'download', names: ['a.txt'], sourceDir: '/r', targetDir: '/l' })[0]
    expect(t.src).toBe('/r/a.txt')
    expect(t.dst).toBe('/l/a.txt')
  })

  it('planTasks 用 isDirMap 标记目录项（决定执行时走递归传输）', () => {
    const t = planTasks({ direction: 'upload', names: ['logs'], sourceDir: '/l', targetDir: '/r', isDirMap: { logs: true } })[0]
    expect(t.isDir).toBe(true)
  })

  it('classify 用原始名字集合判重（含隐藏文件）', () => {
    const { clean, conflicts } = classify(tasks, ['a.txt'])
    expect(conflicts.map((t) => t.name)).toEqual(['a.txt'])
    expect(clean.map((t) => t.name)).toEqual(['b/c'])
  })

  it('copyName 保留扩展名，目录不加后缀', () => {
    expect(copyName('a.txt', ['a.txt'])).toBe('a (2).txt')
    expect(copyName('a.txt', ['a.txt', 'a (2).txt'])).toBe('a (3).txt')
    expect(copyName('logs', ['logs'])).toBe('logs (2)')
  })

  it('跳过策略：冲突项进 skipped，不产生 rename', () => {
    const { conflicts } = classify(tasks, ['a.txt'])
    const r = applyPolicy(conflicts, POLICY_SKIP, ['a.txt'])
    expect(r.run).toEqual([])
    expect(r.skipped.map((t) => t.name)).toEqual(['a.txt'])
  })

  it('另存副本：目标改名，跳过项为空', () => {
    const { conflicts } = classify(tasks, ['a.txt'])
    const r = applyPolicy(conflicts, POLICY_RENAME, ['a.txt'])
    expect(r.skipped).toEqual([])
    expect(r.run[0].dst).toBe('/r/a (2).txt')
  })

  it('弹框条件：有冲突或被过滤隐藏项才弹', () => {
    expect(needsConfirm({ conflictCount: 0, hiddenSelected: 0 })).toBe(false)
    expect(needsConfirm({ conflictCount: 1, hiddenSelected: 0 })).toBe(true)
    expect(needsConfirm({ conflictCount: 0, hiddenSelected: 2 })).toBe(true)
  })

  it('汇总失败清单可读', () => {
    const s = summarize([
      { name: 'a', status: '完成' },
      { name: 'b', status: '跳过' },
      { name: 'c', status: '失败', src: '/l/c', dst: '/r/c', reason: 'Permission denied' },
    ])
    expect(s.ok).toBe(1); expect(s.skipped).toBe(1); expect(s.failed).toBe(1)
    expect(s.failures[0]).toContain('Permission denied')
  })
})
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/batch.test.js`
Expected: FAIL —— 无法解析 `./batch`

- [ ] **Step 3: 最小实现**

~~~js
export const POLICY_SKIP = 'skip'
export const POLICY_OVERWRITE = 'overwrite'
export const POLICY_RENAME = 'rename'

function joinDir(dir, name) {
  if (!dir || dir === '/') return '/' + name
  return dir.replace(/\/+$/, '') + '/' + name
}

// isDirMap: { [name]: boolean }，由调用方从 items 构造。目录项决定执行时走递归传输。
export function planTasks({ direction, names, sourceDir, targetDir, isDirMap }) {
  const dirs = isDirMap || {}
  return names.map((name) => ({
    name,
    src: joinDir(sourceDir, name),
    dst: joinDir(targetDir, name),
    isDir: !!dirs[name],
  }))
}

// existingNames 必须来自**原始 items**（未经 showAll 过滤），否则隐藏同名项不会被判重。
export function classify(tasks, existingNames) {
  const taken = new Set(existingNames)
  const clean = []
  const conflicts = []
  for (const t of tasks) (taken.has(t.name) ? conflicts : clean).push(t)
  return { clean, conflicts }
}

export function copyName(name, takenNames) {
  const taken = new Set(takenNames)
  const dot = name.lastIndexOf('.')
  const hasExt = dot > 0
  const base = hasExt ? name.slice(0, dot) : name
  const ext = hasExt ? name.slice(dot) : ''
  for (let i = 2; i < 1000; i++) {
    const candidate = base + ' (' + i + ')' + ext
    if (!taken.has(candidate)) return candidate
  }
  return base + ' (copy)' + ext
}

export function applyPolicy(conflicts, policy, takenNames) {
  const taken = [...takenNames]
  if (policy === POLICY_SKIP) return { run: [], skipped: conflicts }
  if (policy === POLICY_OVERWRITE) return { run: conflicts, skipped: [] }
  const run = conflicts.map((t) => {
    const newName = copyName(t.name, taken)
    taken.push(newName)
    return { ...t, name: newName, dst: joinDir(t.dst.replace(/\/[^/]*$/, ''), newName) }
  })
  return { run, skipped: [] }
}

export function needsConfirm({ conflictCount, hiddenSelected }) {
  return conflictCount > 0 || hiddenSelected > 0
}

export function summarize(results) {
  const out = { ok: 0, skipped: 0, failed: 0, failures: [] }
  for (const t of results) {
    if (t.status === '完成') out.ok++
    else if (t.status === '跳过') out.skipped++
    else if (t.status === '失败') { out.failed++; out.failures.push((t.src || t.name) + ' → ' + (t.dst || '') + '：' + (t.reason || '未知原因')) }
  }
  return out
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/batch.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add frontend/src/utils/batch.js frontend/src/utils/batch.test.js
git commit -m "feat(sftp): 批量任务规划与冲突策略 batch.js 与单测"
~~~

---

### Task 6: 拖拽载荷与落点判定 `dnd.js`

**Files:**
- Create: `frontend/src/utils/dnd.js`
- Test: `frontend/src/utils/dnd.test.js`

**Interfaces:**
- Produces: `payloadFor(pane, names)`（JSON 字符串）、`parsePayload(text)`、`hitPane(pt, rects)`、`isSubPath(parent, child)`、`canDropInto({ sourcePane, targetPane, item, targetDir, connected })`、`isSystemDrop(dtTypes)`

- [ ] **Step 1: 写失败测试**

~~~js
import { describe, it, expect } from 'vitest'
import { payloadFor, parsePayload, hitPane, isSubPath, canDropInto, isSystemDrop } from './dnd'

const rects = {
  local: { left: 0, top: 100, right: 400, bottom: 600 },
  remote: { left: 410, top: 100, right: 810, bottom: 600 },
}

describe('dnd', () => {
  it('载荷往返', () => {
    expect(parsePayload(payloadFor('remote', ['a', 'b']))).toEqual({ pane: 'remote', names: ['a', 'b'] })
    expect(parsePayload('not-json')).toBe(null)
  })
  it('坐标命中面板，落在面板外返回 null', () => {
    expect(hitPane({ x: 200, y: 300 }, rects)).toBe('local')
    expect(hitPane({ x: 500, y: 300 }, rects)).toBe('remote')
    expect(hitPane({ x: 200, y: 20 }, rects)).toBe(null)
  })
  it('isSubPath 识别子树（含相等）', () => {
    expect(isSubPath('/a', '/a/b')).toBe(true)
    expect(isSubPath('/a', '/a')).toBe(true)
    expect(isSubPath('/a', '/ab')).toBe(false)
  })
  it('面板内移动：仅目录可作落点', () => {
    const item = { name: 'x.txt', isDir: false }
    expect(canDropInto({ sourcePane: 'local', targetPane: 'local', item, targetDir: '/a/b', connected: true }).ok).toBe(false)
  })
  it('面板内移动：目录拖进自己的子树被拒绝', () => {
    const item = { name: 'a', isDir: true }
    const r = canDropInto({ sourcePane: 'local', targetPane: 'local', item, sourceDir: '/x', targetDir: '/x/a/sub', connected: true })
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('子目录')
  })
  it('跨面板投递：远程未连接时拒绝', () => {
    const r = canDropInto({ sourcePane: 'local', targetPane: 'remote', item: { name: 'a', isDir: false }, targetDir: '/r', connected: false })
    expect(r.ok).toBe(false)
  })
  it('面板内移动：拖到目录自己那一行也拒绝（不能移动到自身）', () => {
    const r = canDropInto({ sourcePane: 'local', targetPane: 'local', item: { name: 'a', isDir: true }, sourceDir: '/x', targetDir: '/x/a', connected: true })
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('子目录')
  })

  it('跨面板投递：本地面板永远可用', () => {
    const r = canDropInto({ sourcePane: 'remote', targetPane: 'local', item: { name: 'a', isDir: false }, targetDir: '/l', connected: false })
    expect(r.ok).toBe(true)
  })
  it('系统拖入靠 dataTransfer.types 判定', () => {
    expect(isSystemDrop(['Files'])).toBe(true)
    expect(isSystemDrop(['text/plain'])).toBe(false)
  })
})
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/dnd.test.js`
Expected: FAIL —— 无法解析 `./dnd`

- [ ] **Step 3: 最小实现**

~~~js
export function payloadFor(pane, names) {
  return JSON.stringify({ pane, names })
}

export function parsePayload(text) {
  try {
    const v = JSON.parse(text)
    if (!v || (v.pane !== 'local' && v.pane !== 'remote') || !Array.isArray(v.names)) return null
    return { pane: v.pane, names: v.names }
  } catch {
    return null
  }
}

export function hitPane({ x, y }, rects) {
  for (const pane of ['local', 'remote']) {
    const r = rects[pane]
    if (!r) continue
    if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return pane
  }
  return null
}

export function isSubPath(parent, child) {
  if (parent === child) return true
  const p = parent.replace(/\/+$/, '')
  return child.startsWith(p + '/')
}

// item 缺省用 { name, isDir }；跨面板投递在源面板侧调用时 target 为对侧。
export function canDropInto({ sourcePane, targetPane, item, sourceDir, targetDir, connected }) {
  if (targetPane === 'remote' && !connected) return { ok: false, reason: '远程未连接' }
  if (sourcePane === targetPane) {
    if (!item || !item.isDir) return { ok: false, reason: '只能放到目录上' }
    if (!targetDir || !sourceDir) return { ok: false, reason: '目标未知' }
    // 拖到目录自己所在的那一行也属于「移动到自身」：item.name === 目标末段即同一路径。
    const targetInsideItem = isSubPath(sourceDir + '/' + item.name, targetDir)
    const targetIsTheItem = sourceDir.replace(/\/+$/, '') + '/' + item.name === targetDir.replace(/\/+$/, '')
    if (targetInsideItem || targetIsTheItem) {
      return { ok: false, reason: '不能移动到自身或其子目录' }
    }
  }
  return { ok: true, reason: '' }
}

export function isSystemDrop(types) {
  return Array.isArray(types) && types.includes('Files')
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/dnd.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add frontend/src/utils/dnd.js frontend/src/utils/dnd.test.js
git commit -m "feat(sftp): 拖拽载荷与落点判定 dnd.js 与单测"
~~~

---

### Task 7: 新包 `internal/localfs`：`Copy` 与 `IsSubPath`

**Files:**
- Create: `internal/localfs/copy.go`
- Test: `internal/localfs/copy_test.go`

**Interfaces:**
- Produces: `localfs.Copy(src, dst string) error`、`localfs.IsSubPath(parent, child string) bool`（Task 10 的 `CopyLocal` 依赖）

- [ ] **Step 1: 写失败测试**

~~~go
package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "b.txt")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
}

func TestCopyTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(src, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "inner", "x.txt"), []byte("x"), 0o644)
	dst := filepath.Join(dir, "out")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "inner", "x.txt")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
}

func TestCopySkipsSymlink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "keep.txt"), []byte("k"), 0o644)
	if err := os.Symlink(t.TempDir(), filepath.Join(src, "link")); err != nil {
		t.Skip("symlink not supported")
	}
	dst := filepath.Join(dir, "out")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy must not fail on symlinked dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.txt")); err != nil {
		t.Fatalf("regular file must be copied: %v", err)
	}
}

func TestCopyOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	_ = os.WriteFile(src, []byte("new"), 0o644)
	_ = os.WriteFile(dst, []byte("old"), 0o644)
	if err := Copy(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Fatalf("overwrite failed: %q", got)
	}
}

func TestIsSubPath(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"/a", "/a/b", true},
		{"/a", "/a", true},
		{"/a", "/ab", false},
		{"/a/", "/a/b", true},
	}
	for _, c := range cases {
		if got := IsSubPath(c.parent, c.child); got != c.want {
			t.Fatalf("IsSubPath(%q,%q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/localfs/ -run TestCopy -v`
Expected: FAIL —— `no Go files in .../internal/localfs`

- [ ] **Step 3: 最小实现**

~~~go
// Package localfs 提供本地文件系统的纯本地操作（复制/搜索），只依赖标准库，
// 不依赖 internal/sftp 或 internal/watch，便于单测与复用。
package localfs

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Copy 复制文件或目录树到 dst；调用方负责按冲突策略决定覆盖/改名/跳过。
// 覆盖已存在文件时直接截断重写（与"覆盖全部"策略一致）。
func Copy(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	// 符号链接一律跳过：不跟随（否则指向目录的链接会让整次复制以 EISDIR 半途失败），
	// 也不复制链接本身（本工具不承诺保留链接语义）。
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := Copy(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFile(src, dst, info.Mode())
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// IsSubPath 判断 child 是否位于 parent 之内（含相等）。用于拒绝"复制/移动到自身子树"。
func IsSubPath(parent, child string) bool {
	p := strings.TrimRight(filepath.Clean(parent), string(filepath.Separator))
	c := filepath.Clean(child)
	if p == c {
		return true
	}
	return strings.HasPrefix(c, p+string(filepath.Separator))
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/localfs/ -count=1`
Expected: PASS（4 个用例）

- [ ] **Step 5: Commit**

~~~bash
git add internal/localfs
git commit -m "feat(localfs): 新增纯本地复制与子树判定（含单测）"
~~~

---

### Task 8: `internal/sftp.RemoveRecursive`（`-` 前缀 + glob 拒绝）

**Files:**
- Modify: `internal/sftp/ctrl.go`（在 `Remove` 之后新增方法；`buildBatch` 不动）
- Modify: `internal/sftp/ctrl_test.go:12-34`（`fakeRunner` 支持排队返回 stdout，供 BFS 列目录）
- Test: `internal/sftp/ctrl_test.go`

**Interfaces:**
- Consumes: `c.run(host, user, batch)`、`c.ListMany(host, user, paths)`、`quoteArg`
- Produces: `func (c *Ctrl) RemoveRecursive(host, user, path string) error`（Task 10 的绑定依赖）；测试夹具 `mockLs(path, lines...)` 与 `lsLine(name, isDir, size)`（在 `ctrl_test.go`，供 P2 的 `search_test.go` 复用，不要重复定义）

- [ ] **Step 1: 扩展 fakeRunner 以支持排队 outcome**

把 `ctrl_test.go` 的 `fakeRunner` 结构体与 `run` 改为（其余测试不受影响，默认仍返回 ExitCode 0）：

~~~go
type fakeRunner struct {
	mu      sync.Mutex
	calls   []runnerCall
	batches [][]byte        // 每次批处理的实际内容（从 -b 指向的临时文件读回）
	queued  []osutil.Outcome // 非空时按序弹出，用于喂 ls 输出
}

func (f *fakeRunner) push(o osutil.Outcome) {
	f.mu.Lock()
	f.queued = append(f.queued, o)
	f.mu.Unlock()
}

func (f *fakeRunner) run(name string, args ...string) (osutil.Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{name: name, args: args})
	if i := indexOf(args, "-b"); i >= 0 && i+1 < len(args) {
		if b, err := os.ReadFile(args[i+1]); err == nil {
			f.batches = append(f.batches, b)
		}
	}
	var out osutil.Outcome
	if len(f.queued) > 0 {
		out = f.queued[0]
		f.queued = f.queued[1:]
	}
	f.mu.Unlock()
	for _, a := range args {
		if strings.HasPrefix(a, "ControlPath=") {
			_ = os.WriteFile(strings.TrimPrefix(a, "ControlPath="), nil, 0600)
		}
	}
	return out, nil
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}
~~~

- [ ] **Step 2: 写失败测试**

~~~go
// mockLs 生成**真实形状**的批处理输出：每段以 'sftp> -ls -la "<path>"' 回显开头。
// parseListMany 正是靠这个前缀切块并**按发送顺序**归属路径；缺了它整块会被丢弃，
// 测试会得到'目录未知'而不是'列出了内容'（见 internal/sftp/listmany_parse.go:52-76）。
// 另：ctrl_test.go 的 import 需补 "fmt"。
func mockLs(path string, lines ...string) string {
	out := "sftp> -ls -la \"" + path + "\"\n"
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func lsLine(name string, isDir bool, size int64) string {
	kind := "-"
	if isDir {
		kind = "d"
	}
	return fmt.Sprintf("%srw-r--r-- 1 u g %d Jan  1 00:00 %s", kind, size, name)
}

func TestRemoveRecursiveBuildsDashPrefixedBatch(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	// 第 1 批：列 /root → 1 个文件 + 1 个子目录
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("a.txt", false, 10), lsLine("sub", true, 0))})
	// 第 2 批：列 /root/sub → 回显在场、内容为空 ⇒ 存在但为空的目录
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/sub")})
	// 第 3 批：删除批
	fr.push(osutil.Outcome{ExitCode: 0})

	if err := c.RemoveRecursive("h", "", "/root"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.calls) != 3 {
		t.Fatalf("want 3 runner calls (list root, list sub, delete), got %d", len(fr.calls))
	}
	bat := fr.batches[len(fr.batches)-1]
	want := "-rm \"/root/a.txt\"\n-rmdir \"/root/sub\"\n-rmdir \"/root\"\n"
	if string(bat) != want {
		t.Fatalf("delete batch = %q, want %q", bat, want)
	}
}

func TestRemoveRecursiveSingleFileOnlyRm(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	// sftp ls -l <file> 返回它自己，且 Item.Name 可能是完整路径。
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/a.txt", lsLine("/root/a.txt", false, 10))})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/root/a.txt"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rm \"/root/a.txt\"\n"; got != want {
		t.Fatalf("single-file batch = %q, want %q", got, want)
	}
}

func TestRemoveRecursiveEmptyDirOnlyRmdir(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/empty")})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/empty"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rmdir \"/empty\"\n"; got != want {
		t.Fatalf("empty dir batch = %q, want %q", got, want)
	}
}

func TestRemoveRecursivePartialFailureIsReported(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("a.txt", false, 10))})
	// '-' 前缀让失败不中止整批，退出码仍是 0 —— 只能靠 stderr 发现部分失败。
	fr.push(osutil.Outcome{ExitCode: 0, Stderr: "Can't rm: \"/root/a.txt\": Permission denied\r\n"})
	err := c.RemoveRecursive("h", "", "/root")
	if err == nil || !strings.Contains(err.Error(), "/root/a.txt") {
		t.Fatalf("部分失败必须报错并带路径, got %v", err)
	}
}

func TestRemoveRecursivePathWithSpace(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/my dir", lsLine("a b.txt", false, 1))})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/my dir"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rm \"/my dir/a b.txt\"\n-rmdir \"/my dir\"\n"; got != want {
		t.Fatalf("space path batch = %q, want %q", got, want)
	}
}

func TestRemoveRecursiveRejectsGlobInChild(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("lit*name.txt", false, 1))})
	if err := c.RemoveRecursive("h", "", "/root"); err == nil {
		t.Fatal("子项名含通配符必须拒绝整次递归删除")
	}
	if len(fr.calls) != 1 { // 只应有列举调用，绝无删除批
		t.Fatalf("must not run delete batch, calls=%d", len(fr.calls))
	}
}

func TestRemoveRecursiveRejectsGlobPaths(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	if err := c.RemoveRecursive("h", "", "/root/lit*name.txt"); err == nil {
		t.Fatal("expected error for glob metachar path")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("must not spawn any process, got %d calls", len(fr.calls))
	}
}
~~~

**注意**：`ctrl_test.go` 的 import 需补 `"fmt"`（`lsLine` 用了 `fmt.Sprintf`）。


- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run TestRemoveRecursive -v`
Expected: FAIL —— `c.RemoveRecursive undefined`

- [ ] **Step 4: 最小实现**

~~~go
// RemoveRecursive 递归删除远端路径（文件或目录）。
// 约束（spec §5.1 / 决策 27、28）：
//  1) 每条命令加 '-' 前缀：否则任一条失败即中止整批，留下半棵树。
//  2) 不用 '@' 前缀：保留回显，便于按 stderr 反查失败路径。
//  3) 路径含 glob 元字符（* ? [）→ 直接拒绝：sftp 会对参数做远端 glob 展开，可能删错文件。
// 失败语义：部分删除不回滚，错误里带上远端原文。
func (c *Ctrl) RemoveRecursive(host, user, path string) error {
	if path == "" || path == "/" {
		return fmt.Errorf("拒绝递归删除根路径: %q", path)
	}
	if strings.ContainsAny(path, "*?[") {
		return fmt.Errorf("路径含通配符 %s，暂不支持递归删除（避免远端 glob 误删）", path)
	}
	c.logEvent(host, "info", "sftp rm -r "+path)

	var files []string
	var dirsByDepth []string // 自底向上
	queue := []string{path}
	for len(queue) > 0 {
		batch := queue
		if len(batch) > 64 {
			batch = queue[:64]
		}
		queue = queue[len(batch):]
		res, err := c.ListMany(host, user, batch)
		if err != nil {
			return fmt.Errorf("sftp rm -r %s: 列举失败: %w", path, err)
		}
		for _, dir := range batch {
			items, ok := res[dir]
			if !ok {
				return fmt.Errorf("sftp rm -r %s: 目录不可读 %s", path, dir)
			}
			// 单文件目标：sftp ls -l <file> 只返回它自己（实测 Item.Name 可能是完整路径，
			// 见 watch/scan.go:121-127 的同类处理）。此时只发 -rm，绝不 rmdir 父目录。
			if dir == path && len(items) == 1 && !items[0].IsDir &&
				pathpkg.Base(items[0].Name) == pathpkg.Base(path) {
				return c.removeBatch(host, user, []string{path}, nil)
			}
			for _, it := range items {
				full := dir + "/" + it.Name
				if strings.ContainsAny(full, "*?[") {
					// 子项名可能自带通配符：sftp 的 rm 会对参数做远端 glob 展开，
					// 顶层检查拦不住它（spec 决策 28 / R9），只能拒绝整次递归删除。
					return fmt.Errorf("子路径含通配符 %s，暂不支持递归删除（避免远端 glob 误删）", full)
				}
				if it.IsDir {
					queue = append(queue, full)
					dirsByDepth = append([]string{full}, dirsByDepth...) // 深度越深越靠前
				} else {
					files = append(files, full)
				}
			}
		}
	}

	return c.removeBatch(host, user, files, append(dirsByDepth, path))
}

// removeBatch 把删除命令压成一个批处理：每条加 '-' 前缀（一项失败不中止整批），
// 先删文件，再按深度倒序删空目录。dirsBottomUp 必须已自底向上排好。
func (c *Ctrl) removeBatch(host, user string, files, dirsBottomUp []string) error {
	var sb strings.Builder
	write := func(cmd, p string) error {
		q, err := quoteArg(p)
		if err != nil {
			return err
		}
		sb.WriteString("-" + cmd + " " + q + "\n")
		return nil
	}
	for _, f := range files {
		if err := write("rm", f); err != nil {
			return err
		}
	}
	for _, d := range dirsBottomUp {
		if err := write("rmdir", d); err != nil {
			return err
		}
	}
	out, err := c.run(host, user, []byte(sb.String()))
	if err != nil {
		c.logEvent(host, "error", "sftp rm -r failed: "+commandErr(out))
		return fmt.Errorf("sftp rm -r: %w (%s)", err, commandErr(out))
	}
	// '-' 前缀抑制了逐命令中止，退出码无法区分【全部成功】与【部分失败】：
	// 必须按 stderr 反查失败路径，否则调用方会把部分失败当成整批成功，
	// 进而把没删掉的项也从选中集合里移除。
	if failed := failedDeletePaths(out.Stderr, append(append([]string{}, files...), dirsBottomUp...)); len(failed) > 0 {
		msg := "删除失败: " + strings.Join(failed, ", ") + "（" + commandErr(out) + "）"
		c.logEvent(host, "error", "sftp rm -r partial: "+msg)
		return fmt.Errorf("sftp rm -r: %s", msg)
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp rm -r failed: "+commandErr(out))
		return fmt.Errorf("sftp rm -r failed: %s", commandErr(out))
	}
	c.logEvent(host, "info", "sftp rm -r done")
	return nil
}

// failedDeletePaths 按 stderr 原文反查哪些路径删除失败。
// 实测 OpenSSH 的客户端错误行形如 Can't rm: "<path>": <原因> / Can't rmdir: ...，
// 与 listmany_parse.go 的 stderrListFailure 同一思路（按请求路径逐字反查）。
func failedDeletePaths(stderr string, paths []string) []string {
	if stderr == "" {
		return nil
	}
	var out []string
	for _, p := range paths {
		for _, line := range strings.Split(stderr, "\n") {
			l := strings.TrimSpace(strings.TrimRight(line, "\r"))
			if strings.Contains(l, "\""+p+"\"") {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// 实现文件需在 import 里加 `pathpkg "path"`（与 search.go 的 path 不冲突）。
~~~

注意：`dirsByDepth` 用「前插」保证自底向上；实现里若更倾向稳定排序，可改为记录深度后 `sort.Slice`，但**测试断言的顺序必须与实际实现一致**（上面的实现产出 `sub` 在 `/root` 之前）。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/sftp/ -count=1`
Expected: PASS（含既有测试与新用例）

- [ ] **Step 6: Commit**

~~~bash
git add internal/sftp/ctrl.go internal/sftp/ctrl_test.go
git commit -m "feat(sftp): 远端递归删除（- 前缀失败隔离 + glob 路径拒绝）与单测"
~~~

---

### Task 9: `internal/sftp.PutRecursive`

**Files:**
- Modify: `internal/sftp/ctrl.go`（`buildBatch` 增 `putr` 分支；新增方法）
- Test: `internal/sftp/ctrl_test.go`

**Interfaces:**
- Consumes: Task 1 的 `PUT-R` 结论
- Produces: `func (c *Ctrl) PutRecursive(host, user, local, remoteDir string) error`

- [ ] **Step 1: 写失败测试**

~~~go
func TestBuildBatchPutRecursive(t *testing.T) {
	c := NewCtrl(nil, nil)
	b, err := c.buildBatch("putr", "/remote/dir", "/local/dir")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "put -r \"/local/dir\" \"/remote/dir\"\n" {
		t.Fatalf("putr batch = %q", got)
	}
}

func TestPutRecursiveRunsSftp(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	if err := c.PutRecursive("h", "", "/local/dir", "/remote/dir"); err != nil {
		t.Fatalf("PutRecursive: %v", err)
	}
	if len(fr.calls) != 1 || fr.calls[0].name != "sftp" {
		t.Fatalf("calls = %+v", fr.calls)
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run 'TestBuildBatchPutRecursive|TestPutRecursiveRunsSftp' -v`
Expected: FAIL —— `putr` 分支返回空 batch（`got ""`），`PutRecursive undefined`

- [ ] **Step 3: 实现**

在 `buildBatch` 的 switch 中加：

~~~go
	case "putr":
		// 递归上传：sftp put -r <local> <remoteDir>。
		l, err := quoteArg(local)
		if err != nil {
			return nil, err
		}
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("put -r %s %s\n", l, r)), nil
~~~

新增方法（注释里写清 Task 1 实测得到的语义）：

~~~go
// PutRecursive 递归上传本地目录到远端目录（sftp put -r）。
// 目标语义以 e2e 探针实测为准（spec R1）；实测前的实现只负责把命令送出去，
// 并在日志里记录本地与远端两个路径，便于事后对齐。
func (c *Ctrl) PutRecursive(host, user, local, remoteDir string) error {
	c.logEvent(host, "info", "sftp put -r "+local+" → "+remoteDir)
	batch, err := c.buildBatch("putr", remoteDir, local)
	if err != nil {
		c.logEvent(host, "error", "sftp put -r failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp put -r failed: "+commandErr(out))
		return fmt.Errorf("sftp put -r %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp put -r failed: "+commandErr(out))
		return fmt.Errorf("sftp put -r failed: %s", commandErr(out))
	}
	c.logEvent(host, "info", "sftp put -r done")
	return nil
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/sftp/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add internal/sftp/ctrl.go internal/sftp/ctrl_test.go
git commit -m "feat(sftp): 递归上传 PutRecursive（put -r）与单测"
~~~

---

### Task 10: app.go 绑定 + 绑定重生成

**Files:**
- Modify: `app.go`（在既有 Sftp* 方法附近新增）
- Test: `app_test.go`
- Regenerate: `frontend/wailsjs/go/main/App.{js,d.ts}`、`frontend/wailsjs/go/models.ts`

**Interfaces:**
- Produces（Task 13/14/15 调用的精确签名）：
  - `SftpRemoveRecursive(host, user, path string) error`
  - `SftpPutRecursive(host, user, local, remoteDir string) error`
  - `SftpMove(host, user, oldPath, newPath string) error`
  - `CopyLocal(src, dst string) error`
  - `StatPaths(paths []string) []PathInfo`，`PathInfo{Path,Name,IsDir,Size,Err}`（json: path/name/isDir/size/err）

- [ ] **Step 1: 写失败测试**

~~~go
func TestCopyLocalRejectsSubtree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	if err := os.MkdirAll(filepath.Join(src, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	if err := a.CopyLocal(src, filepath.Join(src, "inner", "copy")); err == nil {
		t.Fatal("expected rejection when dst is inside src")
	}
}

func TestCopyLocalCopiesFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(src, []byte("hi"), 0o644)
	a := &App{}
	if err := a.CopyLocal(src, filepath.Join(dir, "y.txt")); err != nil {
		t.Fatalf("CopyLocal: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "y.txt")); string(b) != "hi" {
		t.Fatalf("copy failed: %q", b)
	}
}

func TestStatPathsReportsTypeAndName(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(file, []byte("12345"), 0o644)
	a := &App{}
	got := a.StatPaths([]string{file, dir, filepath.Join(dir, "nope")})
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Name != "a.txt" || got[0].IsDir || got[0].Size != 5 {
		t.Fatalf("file info = %+v", got[0])
	}
	if !got[1].IsDir {
		t.Fatalf("dir info = %+v", got[1])
	}
	if got[2].Err == "" {
		t.Fatalf("missing path must carry Err: %+v", got[2])
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./ -run 'TestCopyLocal|TestStatPaths' -v`
Expected: FAIL —— `a.CopyLocal undefined`

- [ ] **Step 3: 实现**

~~~go
// PathInfo 描述一个本地路径（供前端处理系统拖入的文件/目录）。
type PathInfo struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Err   string `json:"err,omitempty"`
}

// StatPaths 批量 Lstat；单项失败不整体失败（前端据此跳过该项并提示）。
func (a *App) StatPaths(paths []string) []PathInfo {
	out := make([]PathInfo, 0, len(paths))
	for _, p := range paths {
		info := PathInfo{Path: p, Name: filepath.Base(p)}
		st, err := os.Lstat(p)
		if err != nil {
			info.Err = err.Error()
			out = append(out, info)
			continue
		}
		info.IsDir = st.IsDir()
		if !st.IsDir() {
			info.Size = st.Size()
		}
		out = append(out, info)
	}
	return out
}

// CopyLocal 复制本地文件/目录到 dst。拒绝把 src 复制进它自己的子树。
func (a *App) CopyLocal(src, dst string) error {
	sp, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	dp, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	if sp == dp {
		// 源与目标同一路径：继续下去 copyFile 会先把源文件 O_TRUNC 成 0 字节，
		// 再读到空内容（把文件拖到它自己所在面板即可命中）。
		return nil
	}
	if localfs.IsSubPath(sp, dp) {
		return fmt.Errorf("拒绝复制到自身子树: %s → %s", sp, dp)
	}
	return localfs.Copy(sp, dp)
}

// SftpRemoveRecursive 删除远端文件或目录（目录递归）。
func (a *App) SftpRemoveRecursive(host, user, path string) error {
	return a.sftp.RemoveRecursive(host, user, path)
}

// SftpPutRecursive 递归上传本地目录到远端目录。
func (a *App) SftpPutRecursive(host, user, local, remoteDir string) error {
	if err := a.sftp.PutRecursive(host, user, local, remoteDir); err != nil {
		return err
	}
	a.recordRecentSFTP(host, filepath.Dir(remoteDir), filepath.Dir(local))
	return nil
}

// SftpMove 语义等于远端 Rename（跨目录移动）。
func (a *App) SftpMove(host, user, oldPath, newPath string) error {
	return a.sftp.Rename(host, user, oldPath, newPath)
}
~~~

并在 `app.go` 的 import 块加入 `"sshore/internal/localfs"`。

- [ ] **Step 4: 跑测试确认通过 + vet**

Run: `go test ./ -run 'TestCopyLocal|TestStatPaths' -count=1 && go vet ./...`
Expected: PASS

- [ ] **Step 5: 重生成前端绑定**

Run: `$(go env GOPATH)/bin/wails generate module -tags webkit2_41`
Expected: `frontend/wailsjs/go/main/App.d.ts` 出现 `SftpRemoveRecursive` / `SftpPutRecursive` / `SftpMove` / `CopyLocal` / `StatPaths`；`models.ts` 出现 `PathInfo`。

校验命令：

~~~bash
grep -c 'SftpRemoveRecursive\|SftpPutRecursive\|SftpMove\|CopyLocal\|StatPaths' frontend/wailsjs/go/main/App.d.ts
~~~

Expected: 输出 ≥ 5

- [ ] **Step 6: Commit**

~~~bash
git add app.go app_test.go frontend/wailsjs
git commit -m "feat(app): 新增递归删除/上传/移动/本地复制/StatPaths 绑定并重生成 wailsjs"
~~~

---

### Task 11: 系统文件拖入通道

**Files:**
- Modify: `main.go:22-57`（`options.App` 增加 `DragAndDrop`；`OnStartup` 注册 `OnFileDrop`）
- Test: 手工（Wails 事件无法单测）；由 Task 15 的端到端验证承接

**Interfaces:**
- Produces: 事件名 `files:dropped`，载荷 `{ x, y, paths }`（Task 15 订阅）
- **不得**设置 `DisableWebViewDrop`（spec §13 R4）

- [ ] **Step 1: 改 main.go**

在 `wails.Run` 的 `options.App` 里加：

~~~go
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
		},
~~~

在 `OnStartup` 内、`app.Init(...)` 之后加：

~~~go
		// 系统文件拖入：Wails 只提供拖入 API（无拖出）。这里把 (x, y, paths)
		// 转给前端，由前端按落点坐标决定目标面板（本地面板=复制，远程面板=上传）。
		runtime.OnFileDrop(ctx, func(x, y int, paths []string) {
			runtime.EventsEmit(ctx, "files:dropped", map[string]any{
				"x": x, "y": y, "paths": paths,
			})
		})
~~~

- [ ] **Step 2: 编译验证**

Run: `go build ./... && go vet ./...`
Expected: 无输出（成功）。若报 `unknown field DragAndDrop`，说明 wails 版本 API 不符，检查 `go.mod` 是否为 v2.15.0。

- [ ] **Step 3: Commit**

~~~bash
git add main.go
git commit -m "feat(app): 打开 Wails 文件拖入并把 files:dropped 转给前端"
~~~

---

### Task 12: 冲突对话框组件

**Files:**
- Create: `frontend/src/components/ConflictDialog.vue`
- Test: 手工（组件无单测栈）；纯逻辑已在 Task 5 覆盖

**Interfaces:**
- Consumes: `POLICY_SKIP/POLICY_OVERWRITE/POLICY_RENAME`（Task 5）
- Produces: 组件 props `{ visible, target, total, conflicts: string[], hiddenSelected: number }`；事件 `confirm(policy)` / `cancel()`

- [ ] **Step 1: 写组件**

~~~vue
<script setup>
import { ref, watch } from 'vue'
import { POLICY_SKIP, POLICY_OVERWRITE, POLICY_RENAME } from '../utils/batch'

const props = defineProps({
  visible: Boolean,
  target: { type: String, default: '' },
  total: { type: Number, default: 0 },
  conflicts: { type: Array, default: () => [] },
  hiddenSelected: { type: Number, default: 0 },
})
const emit = defineEmits(['confirm', 'cancel'])
const policy = ref(POLICY_SKIP)
const MAX_LIST = 8

watch(() => props.visible, (v) => { if (v) policy.value = POLICY_SKIP })

function shown() { return props.conflicts.slice(0, MAX_LIST) }
function more() { return Math.max(0, props.conflicts.length - MAX_LIST) }
</script>

<template>
  <div v-if="visible" class="ui-overlay" @click.self="emit('cancel')" @keyup.esc="emit('cancel')">
    <div class="dialog" role="dialog" aria-label="投递确认">
      <div class="dtitle">{{ conflicts.length ? '目标已存在同名项' : '确认投递' }}</div>
      <p class="dmsg">
        目标 {{ target }}：本次 {{ total }} 项<template v-if="conflicts.length">，其中 {{ conflicts.length }} 项已存在</template>
        <template v-if="hiddenSelected">（另含 {{ hiddenSelected }} 项被过滤隐藏，不可见但会一起操作）</template>
      </p>
      <p v-if="conflicts.length" class="dlist">{{ shown().join(' · ') }}<template v-if="more()"> 等 {{ more() }} 项</template></p>
      <template v-if="conflicts.length">
        <label class="opt" :class="{ sel: policy === POLICY_OVERWRITE }">
          <input type="radio" :value="POLICY_OVERWRITE" v-model="policy" /> 覆盖全部
        </label>
        <label class="opt" :class="{ sel: policy === POLICY_SKIP }">
          <input type="radio" :value="POLICY_SKIP" v-model="policy" /> 跳过已存在项
        </label>
        <label class="opt" :class="{ sel: policy === POLICY_RENAME }">
          <input type="radio" :value="POLICY_RENAME" v-model="policy" /> 另存副本
        </label>
      </template>
      <div class="dbtns">
        <button @click="emit('cancel')">取消本次批量</button>
        <button class="primary" @click="emit('confirm', conflicts.length ? policy : POLICY_SKIP)">开始</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 20px; width: 440px; text-align: left; }
.dtitle { font-weight: 600; color: var(--text); }
.dmsg { color: var(--text-dim); font-size: var(--fs-13); }
.dlist { color: var(--text-faint); font-size: var(--fs-12); word-break: break-all; }
.opt { display: flex; gap: 6px; align-items: center; padding: 6px; border: 1px solid var(--border); border-radius: 5px; margin-top: 6px; color: var(--text); font-size: var(--fs-13); }
.opt.sel { border-color: var(--accent); background: var(--surface); }
.opt input { width: auto; }
.dbtns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
</style>
~~~

- [ ] **Step 2: 构建验证**

Run: `cd frontend && npm run build`
Expected: 构建成功

- [ ] **Step 3: Commit**

~~~bash
git add frontend/src/components/ConflictDialog.vue
git commit -m "feat(sftp): 投递/冲突确认对话框（默认跳过已存在项）"
~~~

---

### Task 13: FilePane 多选、聚焦态与可见顺序

**Files:**
- Modify: `frontend/src/components/FilePane.vue`（script 段与 template 段）
- Test: 纯逻辑已在 Task 2/6 覆盖；本任务以 `npm run build` + Task 14 联调验证

**Interfaces:**
- Consumes: `selection.js`（Task 2）
- Produces（Task 14/15 依赖的组件契约）：
  - props：`title, path, items, selKeys: string[], anchor: string|null, showHidden, loading, canWrite: boolean`
  - emits：`select({ item, index, event })`、`open(item)`、`context({ item, event })`、`visible(keys: string[])`、`focus()`、`dragstart(payload)`、`dropon({ item, event })`

- [ ] **Step 1: 改 props/emits 与可见顺序上抛**

`FilePane.vue` 顶部（替换原 props/emits）：

~~~js
const props = defineProps({
  title: String,
  path: String,
  items: { type: Array, default: () => [] },
  selKeys: { type: Array, default: () => [] },
  anchor: { type: String, default: null },
  showHidden: { type: Boolean, default: true },
  loading: { type: Boolean, default: false },
  // 批量动作由父组件按面板给出（本地面板=上传/…，远程面板=下载/…），
  // 面板本身不懂语义，只负责渲染与上抛（spec §6.2 / 决策 3）。
  actions: { type: Array, default: () => [] },
  hiddenSelected: { type: Number, default: 0 },
})
const emit = defineEmits(['select', 'open', 'context', 'visible', 'focus', 'dragstart', 'dropon', 'clear', 'action'])
const menuOpen = ref(false)
function pick(name) { menuOpen.value = false; emit('action', name) }
~~~

在既有 `visible` computed 之后加（注意 import 需补 `watch`）：

~~~js
const filterText = ref('')
// shown 必须先定义：可见顺序的唯一真源是 showAll ∩ filter（决策 32 / §8.1），
// 而不是只有 showAll 的 visible —— 否则 Ctrl+A / Shift 区间会把被过滤隐藏的行选进来。
const shown = computed(() => {
  const q = filterText.value.trim().toLowerCase()
  if (!q) return visible.value
  return visible.value.filter((it) => it.name.toLowerCase().includes(q))
})
const visibleKeys = computed(() => shown.value.map((it) => it.name))
watch(visibleKeys, (keys) => emit('visible', keys), { immediate: true })
function hit(text) {
  const q = filterText.value.trim().toLowerCase()
  const i = q ? text.toLowerCase().indexOf(q) : -1
  if (i < 0) return null
  return { pre: text.slice(0, i), mid: text.slice(i, i + q.length), post: text.slice(i + q.length) }
}
~~~

- [ ] **Step 2: 改模板（列头过滤框、聚焦态、多选行、拖拽）**

~~~html
<div class="pane ui-panel" :class="{ focus }" tabindex="0" @focus="focus = true; emit('focus')" @blur="focus = false">
  <div class="head">
    <span class="title">{{ title }}</span>
    <span class="curpath">{{ path }}</span>
    <span class="count">{{ shown.length }} 项</span>
    <div v-if="selKeys.length || actions.length" class="batch" @click.stop>
      <button class="chip" @click="menuOpen = !menuOpen">
        已选 {{ selKeys.length }} 项<template v-if="hiddenSelected">（含 {{ hiddenSelected }} 项被过滤）</template> ▾
      </button>
      <div v-if="menuOpen" class="bmenu">
        <button v-for="a in actions" :key="a.name" :disabled="a.disabled" @click="pick(a.name)">{{ a.label }}</button>
      </div>
    </div>
  </div>
  <div class="list-holder">
    <div class="columns">
      <span class="col-name sortable" :class="{ active: sortKey === 'name' }" @click="sortBy('name')">名称 {{ arrow('name') }}</span>
      <span class="col-size sortable" :class="{ active: sortKey === 'size' }" @click="sortBy('size')">大小 {{ arrow('size') }}</span>
      <span class="col-time sortable" :class="{ active: sortKey === 'modTime' }" @click="sortBy('modTime')">修改时间 {{ arrow('modTime') }}</span>
      <input class="filter" v-model="filterText" placeholder="过滤…" />
    </div>
    <ul class="list" :class="{ busy: loading }">
      <li class="up" @click="emit('open', { name: '..', isDir: true })">
        <span class="cell-name">📁 ..</span><span class="cell-size">—</span><span class="cell-time">—</span>
      </li>
      <li
        v-for="(it, i) in shown"
        :key="it.name"
        :class="{ sel: selKeys.includes(it.name), anchor: anchor === it.name }"
        draggable="true"
        @click="emit('select', { item: it, index: i, event: $event })"
        @dblclick="emit('open', it)"
        @contextmenu.prevent="emit('context', { item: it, event: $event })"
        @dragstart="emit('dragstart', { item: it, event: $event })"
        @dragover.prevent
        @drop.prevent="emit('dropon', { item: it, event: $event })"
      >
        <span class="cell-name">
          <template v-if="hit(it.name)"><span>{{ hit(it.name).pre }}</span><mark>{{ hit(it.name).mid }}</mark><span>{{ hit(it.name).post }}</span></template>
          <template v-else>{{ it.isDir ? '📁' : '📄' }} {{ it.name }}</template>
        </span>
        <span class="cell-size">{{ fmtSize(it.size) }}</span>
        <span class="cell-time">{{ it.modTime || '—' }}</span>
      </li>
    </ul>
    <div v-if="loading" class="loading">加载中…</div>
  </div>
</div>
~~~

script 段还需 `import { ref, computed, watch } from 'vue'` 与 `const focus = ref(false)`。样式补：

~~~css
.pane.focus { border-color: var(--accent); }
.list li.sel { background: var(--surface-hover); color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); }
.list li.anchor { outline: 1px dashed var(--seed); outline-offset: -1px; }
.filter { width: 110px; font-size: var(--fs-11); }
mark { background: var(--accent); color: var(--on-accent); }
.batch { position: relative; }
.batch .chip { font-size: var(--fs-11); }
.bmenu { position: absolute; right: 0; top: 100%; z-index: 20; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 3px 0; min-width: 160px; }
.bmenu button { display: block; width: 100%; text-align: left; padding: 4px 10px; background: none; border: none; color: var(--text); font-size: var(--fs-12); cursor: pointer; }
.bmenu button:hover:not(:disabled) { background: var(--surface-hover); }
.bmenu button:disabled { color: var(--text-faint); cursor: default; }
~~~

- [ ] **Step 3: 构建验证**

Run: `cd frontend && npm run build`
Expected: 构建成功

- [ ] **Step 4: Commit**

~~~bash
git add frontend/src/components/FilePane.vue
git commit -m "feat(sftp): 面板多选渲染、聚焦态、过滤框与可见顺序上抛"
~~~

---

### Task 14: SftpView 批量编排与键盘分派

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（script 段的选中状态/操作函数；template 的面板绑定与对话框）
- Test: 手工验收（spec §12.3 第 4/5/9 条）+ 纯逻辑由 Task 2/3/5 覆盖

**Interfaces:**
- Consumes: `selection.js`、`keys.js`、`batch.js`、`queue.js`、`ConflictDialog.vue`、Task 10 的绑定
- Produces: `runBatch({ direction, names, sourceDir, targetDir, sourceItems })`（Task 15 的拖拽复用同一入口）

- [ ] **Step 1: 替换选中状态为集合**

先在 `SftpView.vue` 顶部那条 `from '../../wailsjs/go/main/App'` 的 import 里补上本任务用到的绑定：`SftpRemoveRecursive, SftpPutRecursive, SftpMove, CopyLocal, StatPaths`（漏了会在构建时 `is not defined`）。

**旧 `remoteSel/localSel` 的全部引用点都必须改造**（只改声明会留下 `remoteSel is not defined`）：`SftpView.vue:18,24`（声明）、`:106`（onHostChange 置空）、`:139`（applyRecent 置空）、`:161`（disconnect 置空）、`:208`（download 取菜单项）、`:235`（remove 取菜单项）、`:253`（rename 取菜单项）、`:398-401`（模板绑定）。改造后行为：`:106/:139/:161` 一律改为 `sel.clear(remoteSelection)` / `sel.clear(localSelection)`，并在 `openRemote/openLocal` 成功后同样清空该面板（决策 6「切目录=清空该面板」）。

再把 `remoteSel/localSel`（`SftpView.vue:18,24`）替换为：

~~~js
import { reactive } from 'vue'
import * as sel from '../utils/selection'
import { actionFor } from '../utils/keys'
import { planTasks, classify, applyPolicy, needsConfirm, summarize } from '../utils/batch'
import ConflictDialog from '../components/ConflictDialog.vue'

const remoteSelection = reactive(sel.createSelection())
const localSelection = reactive(sel.createSelection())
const remoteVisible = ref([])
const localVisible = ref([])
const focusedPane = ref('remote')
const conflict = reactive({ visible: false, resolve: null, plan: null, hiddenSelected: 0 })
~~~

- [ ] **Step 2: 批量投递与冲突流程**

~~~js
// 面板头「已选 N 项 ▾」的动作表：语义按面板决定，面板本身不懂（spec §6.2）。
function actionsFor(pane) {
  const s = selectionFor(pane)
  const n = s.keys.size
  const tail = [
    { name: 'rename', label: '重命名', disabled: n !== 1 },
    { name: 'remove', label: '删除 (' + n + ')' },
    { name: 'select-all', label: '全选' },
    { name: 'clear', label: '清空' },
  ]
  if (n === 0 && pane === 'remote') return tail
  const head = pane === 'remote'
    ? [{ name: 'download', label: '下载到本地 (' + n + ')' }]
    : [{ name: 'upload', label: '上传到远程 (' + n + ')' }, { name: 'upload-picked', label: '上传文件…' }]
  return head.concat(tail)
}

// uploadPicked = 既有 upload() 的改名版（PickLocalFile 选文件上传），保持原行为。
function onPaneAction(pane, name) {
  const s = selectionFor(pane)
  if (name === 'select-all') return sel.all(s, visibleFor(pane))
  if (name === 'clear') return sel.clear(s)
  if (name === 'remove') return removeSelected(pane)
  if (name === 'upload-picked') return uploadPicked()
  if (name === 'rename') return renameItem(pane, itemsFor(pane).find((it) => s.keys.has(it.name)))
  if (name === 'download' || name === 'upload') return runBatchFor(pane, name)
}

function itemsFor(pane) { return pane === 'remote' ? remoteItems.value : localItems.value }

function runBatchFor(pane, name) {
  const s = selectionFor(pane)
  const names = [...s.keys]
  if (!names.length) return null
  if (name === 'download') {
    return runBatch({ direction: 'download', names, sourceDir: remotePath.value, targetDir: localPath.value || '/', sourceItems: remoteItems.value })
  }
  // 上传只可能来自本地面板（远程面板没有上传入口，spec §6.2）
  return runBatch({ direction: 'upload', names, sourceDir: localPath.value || '/', targetDir: remotePath.value, sourceItems: localItems.value })
}

function onSelect(pane, { item, event }) {
  const s = pane === 'remote' ? remoteSelection : localSelection
  const vis = pane === 'remote' ? remoteVisible.value : localVisible.value
  focusedPane.value = pane
  if (event && (event.ctrlKey || event.metaKey)) sel.toggle(s, item.name)
  else if (event && event.shiftKey) sel.rangeTo(s, vis, item.name)
  else sel.single(s, item.name)
}

// 用可见集合（@visible 上抛的 showAll ∩ filter 结果）而不是原始 items：
// 原始 items 里包含被过滤隐藏的 dotfile，拿它当可见集会把计数恒算成 0（spec 决策 15 / §8.1）。
function hiddenSelectedCount(selection, visibleKeys) {
  const vis = new Set(visibleKeys || [])
  let n = 0
  for (const k of selection.keys) if (!vis.has(k)) n++
  return n
}

function visibleFor(pane) { return pane === 'remote' ? remoteVisible.value : localVisible.value }
function hiddenFor(pane) {
  return hiddenSelectedCount(pane === 'remote' ? remoteSelection : localSelection, visibleFor(pane))
}
function selectionFor(pane) { return pane === 'remote' ? remoteSelection : localSelection }

function askConflict(plan, hiddenSelected) {
  return new Promise((resolve) => {
    conflict.visible = true
    conflict.resolve = resolve
    conflict.plan = plan
    conflict.hiddenSelected = hiddenSelected
  })
}
function onConflictConfirm(policy) { conflict.visible = false; if (conflict.resolve) conflict.resolve(policy) }
function onConflictCancel() { conflict.visible = false; if (conflict.resolve) conflict.resolve(null) }

// direction: 'download' | 'upload'
async function runBatch({ direction, names, sourceDir, targetDir, sourceItems, systemPaths }) {
  const sourceSelection = direction === 'upload' ? localSelection : remoteSelection
  const targetItems = direction === 'download' ? localItems.value : remoteItems.value
  const tasks = systemPaths
    ? systemPaths.map((i) => ({ name: i.name, src: i.path, dst: targetDir.replace(/\/+$/, '') + '/' + i.name, isDir: i.isDir }))
    : planTasks({ direction, names, sourceDir, targetDir, isDirMap: (sourceItems || []).reduce((m, it) => (m[it.name] = it.isDir, m), {}) })
  const hidden = hiddenSelectedCount(sourceSelection, visibleFor(direction === 'upload' ? 'local' : 'remote'))
  const existing = (targetItems || []).map((it) => it.name)
  const { clean, conflicts } = classify(tasks, existing)
  let run = clean
  let skipped = []
  if (needsConfirm({ conflictCount: conflicts.length, hiddenSelected: hidden })) {
    const policy = await askConflict({ conflicts, total: tasks.length, target: targetDir, hiddenSelected: hidden }, hidden)
    if (policy === null) return null
    const applied = applyPolicy(conflicts, policy, existing)
    skipped = applied.skipped
    run = clean.concat(applied.run)
  }
  const results = skipped.map((t) => ({ ...t, status: '跳过' }))
  for (const t of skipped) transfers.value.push({ direction, name: t.name, src: t.src, dst: t.dst, size: 0, status: '跳过', elapsed: 0 })
  for (const t of run) {
    const rec = { direction, name: t.name, src: t.src, dst: t.dst, size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (direction === 'download') await (t.isDir ? SftpGetDir(host.value, '', t.src, t.dst) : SftpGet(host.value, '', t.src, t.dst))
      else await (t.isDir ? SftpPutRecursive(host.value, '', t.src, t.dst) : SftpPut(host.value, '', t.src, t.dst))
      rec.status = '完成'
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      err(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
    results.push(rec)
  }
  await (direction === 'download' ? loadLocal() : loadRemote())
  const s = summarize(results)
  logStore.add({ source_id: 'sftp', source_type: 'sftp', level: s.failed ? 'error' : 'info', ts: new Date().toISOString(),
    message: '批量' + direction + ' ' + results.length + ' 项：成功 ' + s.ok + ' 跳过 ' + s.skipped + ' 失败 ' + s.failed })
  const doneNames = results.filter((r) => r.status === '完成').map((r) => r.name)
  sel.remove(sourceSelection, doneNames) // 只移除成功项：失败/留在原地的项仍应保持选中（spec §6.4）
  return s
}

// 「复制失败清单」：队列只负责 emit，落盘/剪贴板由编排层做（避免出现死按钮）。
async function copyFailures() {
  const failed = transfers.value.filter((t) => t.status === '失败')
  if (!failed.length) return
  const text = failed.map((t) => failureText(t)).join('\n')
  try { await navigator.clipboard.writeText(text) } catch (e) { err(e) }
}
~~~

删除（唯一不可撤销的批量动作，单独走确认）：

~~~js
async function removeSelected(pane) {
  const s = pane === 'remote' ? remoteSelection : localSelection
  const names = [...s.keys]
  if (!names.length) return
  const items = pane === 'remote' ? remoteItems.value : localItems.value
  const hidden = hiddenSelectedCount(s, items)
  const hasDir = (items || []).some((it) => s.keys.has(it.name) && it.isDir)
  const msg = '删除' + (pane === 'local' ? '本地' : '远程') + ' ' + names.length + ' 项？' +
    (hasDir ? '（含目录，将递归删除）' : '') + (hidden ? '（其中 ' + hidden + ' 项被过滤隐藏）' : '')
  const ok = await openConfirm('确认删除', msg)
  if (!ok) return
  const base = pane === 'local' ? (localPath.value || '/') : remotePath.value
  for (const n of names) {
    const full = base.replace(/\/+$/, '') + '/' + n
    const rec = { direction: pane === 'local' ? 'move' : 'download', name: n, src: full, dst: '', size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (pane === 'local') await DeleteLocal(full)
      else await SftpRemoveRecursive(host.value, '', full)
      rec.status = '完成'
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      err(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await (pane === 'local' ? loadLocal() : loadRemote())
  // 只把成功删掉的项移出选中集合：失败项还留在原地，保持选中便于重试（spec §6.4）。
  const removed = names.filter((n) => !transfers.value.some((t) => t.name === n && t.status === '失败'))
  sel.remove(s, removed)
}
~~~

- [ ] **Step 3: 模板接线 + 键盘**

~~~html
<FilePane title="本地" :path="localPath || '/'" :items="localItems" :sel-keys="[...localSelection.keys]" :anchor="localSelection.anchor"
  :show-hidden="showAll" :loading="localLoading" :actions="actionsFor('local')" :hidden-selected="hiddenFor('local')"
  @select="onSelect('local', $event)" @open="openLocal" @action="onPaneAction('local', $event)"
  @clear="sel.clear(localSelection)"
  @context="showMenu('local', $event)" @visible="localVisible = $event" @focus="focusedPane = 'local'" />

<FilePane title="远程" :path="remotePath" :items="remoteItems" :sel-keys="[...remoteSelection.keys]" :anchor="remoteSelection.anchor"
  :show-hidden="showAll" :loading="remoteLoading" :actions="actionsFor('remote')" :hidden-selected="hiddenFor('remote')"
  @select="onSelect('remote', $event)" @open="openRemote" @action="onPaneAction('remote', $event)"
  @clear="sel.clear(remoteSelection)"
  @context="showMenu('remote', $event)" @visible="remoteVisible = $event" @focus="focusedPane = 'remote'" />

<TransferQueue :transfers="transfers" :now="now" @copy-failures="copyFailures" />
~~~

键盘（`onActivated` 挂、`onDeactivated` 与 `onUnmounted` 摘）：

~~~js
function onKeydown(ev) {
  const action = actionFor(ev)
  if (!action) return
  const s = selectionFor(focusedPane.value)
  const vis = visibleFor(focusedPane.value)
  if (action === 'delete') { ev.preventDefault(); removeSelected(focusedPane.value) }
  else if (action === 'select-all') { ev.preventDefault(); sel.all(s, vis) }
  else if (action === 'escape') { sel.clear(s) }
}
// 必须成对挂摘：SftpView 被 <KeepAlive> 缓存，setup 只跑一次（App.vue:57-61）。
// 在 setup 顶层注册会让「端口转发/文件同步」标签下按 Delete 弹出 SFTP 的删除确认框。
// 因此注册写进**既有**的 onActivated（SftpView.vue:364-377，那里已经在挂 click 监听）：
//
//   onActivated(() =>   { window.addEventListener('keydown', onKeydown) })
//   onDeactivated(() => { window.removeEventListener('keydown', onKeydown) })
//   onUnmounted(() =>   { window.removeEventListener('keydown', onKeydown) })
~~~

右键菜单：把既有 `doAction` 的 download/upload/remove 改为走批量入口（右键项不在集合内时先 `single()`，与 §3 决策 5 一致）：

~~~js
async function doAction(name) {
  const pane = menu.value.pane
  const s = pane === 'remote' ? remoteSelection : localSelection
  const it = menu.value.item
  if (it && it.name !== '..' && !sel.isSelected(s, it.name)) sel.single(s, it.name)
  const names = [...s.keys]
  const sourceDir = pane === 'remote' ? remotePath.value : (localPath.value || '/')
  const targetDir = pane === 'remote' ? (localPath.value || '/') : remotePath.value
  const sourceItems = pane === 'remote' ? remoteItems.value : localItems.value
  closeMenu()
  if (name === 'download') return runBatch({ direction: 'download', names, sourceDir, targetDir, sourceItems })
  if (name === 'upload') return runBatch({ direction: 'upload', names, sourceDir, targetDir, sourceItems })
  if (name === 'remove') return removeSelected(pane)
  return legacyAction(name, pane, it)
}

// rename / mkdir 仍是单项手势，保持原有实现：
function legacyAction(name, pane, it) {
  if (name === 'rename') return renameItem(pane, it)
  if (name === 'mkdir') return mkdirIn(pane)
}
~~~

（把既有 `rename()`/`mkdir()` 的实现原样搬进 `renameItem/mkdirIn` 即可，不改行为。）

另外补"空白处单击清空"（§3 决策 6）：给 `FilePane` 的 `ul.list` 加 `@click.self="emit('clear')"，`SftpView` 收到后对该面板 `sel.clear(selection)`。

对话框挂载：

~~~html
<ConflictDialog :visible="conflict.visible" :target="conflict.plan && conflict.plan.target"
  :total="conflict.plan && conflict.plan.total" :conflicts="conflict.plan ? conflict.plan.conflicts.map((c) => c.name) : []"
  :hidden-selected="conflict.hiddenSelected" @confirm="onConflictConfirm" @cancel="onConflictCancel" />
~~~

- [ ] **Step 4: 构建 + 手工冒烟**

Run: `cd frontend && npm run build && npx vitest run`
Expected: 构建成功、既有前端测试全绿
手工：`make run`，Ctrl 多选 3 个远程文件 → 「已选 3 项 ▾ → 下载到本地」→ 目标已有同名时出现对话框且默认「跳过已存在项」。

- [ ] **Step 5: Commit**

~~~bash
git add frontend/src/views/SftpView.vue
git commit -m "feat(sftp): 批量投递编排（下载/上传/删除）、冲突流程与键盘分派"
~~~

---

### Task 15: 四条拖拽路径接线

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（拖拽处理 + 事件订阅）
- Test: 手工（spec §12.3 第 10 条）；纯判定由 Task 6 覆盖

**Interfaces:**
- Consumes: `dnd.js`、Task 14 的 `runBatch`、Task 10 的 `StatPaths/CopyLocal`、Task 11 的 `files:dropped`
- Produces: 终态接线

- [ ] **Step 1: 面板互拖与面板内移动**

~~~js
import { payloadFor, parsePayload, hitPane, canDropInto } from '../utils/dnd'
import { EventsOn } from '../../wailsjs/runtime/runtime'
// StatPaths / CopyLocal 已在 Task 14 的既有 import 里加过，这里**不要**重复声明
// （重复 import 同名标识符会让 vite build 直接 SyntaxError）。

function onDragStart(pane, { item, event }) {
  const s = pane === 'remote' ? remoteSelection : localSelection
  if (!sel.isSelected(s, item.name)) sel.single(s, item.name)
  event.dataTransfer.effectAllowed = 'copyMove'
  event.dataTransfer.setData('application/x-sshore', payloadFor(pane, [...s.keys]))
}

async function onPaneDrop(targetPane, { event }) {
  const payload = parsePayload(event.dataTransfer.getData('application/x-sshore'))
  if (!payload || payload.pane === targetPane) return
  const guard = canDropInto({ sourcePane: payload.pane, targetPane, item: { isDir: true }, connected: connected.value })
  if (!guard.ok) { err(guard.reason); return }
  if (payload.pane === 'remote') {
    await runBatch({ direction: 'download', names: payload.names, sourceDir: remotePath.value, targetDir: localPath.value, sourceItems: remoteItems.value })
  } else {
    await runBatch({ direction: 'upload', names: payload.names, sourceDir: localPath.value, targetDir: remotePath.value, sourceItems: localItems.value })
  }
}

async function onMoveDrop(pane, { item, event }) {
  const payload = parsePayload(event.dataTransfer.getData('application/x-sshore'))
  if (!payload || payload.pane !== pane || !payload.names.length) return
  if (!item.isDir) { err('只能放到目录上'); return }
  const base = pane === 'local' ? (localPath.value || '/') : remotePath.value
  const targetDir = base.replace(/\/+$/, '') + '/' + item.name
  for (const n of payload.names) {
    const guard = canDropInto({ sourcePane: pane, targetPane: pane, item: { name: n, isDir: true }, sourceDir: base, targetDir: targetDir + '/' + n, connected: connected.value })
    if (!guard.ok) { err(guard.reason); return }
  }
  // 目标子目录内容未加载 → 先按需加载一次再判重（spec 决策 25）；Windows 上
  // os.Rename 目标存在会直接失败，所以必须走同一套冲突策略而不是硬干。
  const targetItems = pane === 'local'
    ? await ListLocal(targetDir)
    : await SftpList(host.value, '', targetDir)
  const existing = (targetItems || []).map((it) => it.name)
  const prefix = base.replace(/\/+$/, '')
  const planned = payload.names.map((n) => ({ name: n, src: prefix + '/' + n, dst: targetDir + '/' + n }))
  const { clean, conflicts } = classify(planned, existing)
  let run = clean
  let skipped = conflicts
  if (conflicts.length) {
    const policy = await askConflict({ conflicts, total: planned.length, target: targetDir, hiddenSelected: 0 }, 0)
    if (policy === null) return
    const applied = applyPolicy(conflicts, policy, existing)
    run = clean.concat(applied.run)
    skipped = applied.skipped
  }
  for (const t of skipped) {
    transfers.value.push({ direction: 'move', name: t.name, src: t.src, dst: t.dst, size: 0, status: '跳过', elapsed: 0 })
  }
  for (const t of run) {
    const rec = { direction: 'move', name: t.name, src: t.src, dst: t.dst, size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (pane === 'local') await RenameLocal(t.src, t.dst)
      else await SftpMove(host.value, '', t.src, t.dst)
      rec.status = '完成'
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      err(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await (pane === 'local' ? loadLocal() : loadRemote())
  sel.remove(pane === 'local' ? localSelection : remoteSelection, payload.names)
}
~~~

模板：每个 FilePane 外面包一层放置区；`data-pane` 同时是 `onFilesDropped` 命中测试的锚点（缺了它系统拖入会直接 return）：

~~~html
<div class="pane-wrap" data-pane="local" @dragover.prevent @drop.prevent="onPaneDrop('local', $event)">
  <FilePane ... @dragstart="onDragStart('local', $event)" @dropon="onMoveDrop('local', $event)" />
</div>
<div class="pane-wrap" data-pane="remote" @dragover.prevent @drop.prevent="onPaneDrop('remote', $event)">
  <FilePane ... @dragstart="onDragStart('remote', $event)" @dropon="onMoveDrop('remote', $event)" />
</div>
~~~

样式：`.pane-wrap { flex: 1; display: flex; min-width: 0; }`（原 `.panes { display:flex; gap:8px }` 的伸缩由这层承接）。

- [ ] **Step 2: 系统拖入**

~~~js
let offDrop = null

function onFilesDropped(payload) {
  if (!payload || !payload.paths || !payload.paths.length) return
  const localEl = document.querySelector('[data-pane="local"]')
  const remoteEl = document.querySelector('[data-pane="remote"]')
  if (!localEl || !remoteEl) return
  const rects = { local: localEl.getBoundingClientRect(), remote: remoteEl.getBoundingClientRect() }
  // 坐标单位 / DPI 缩放需实测（spec R3）；命中失败只会提示"落点无效"，不会误操作。
  const pane = hitPane({ x: payload.x, y: payload.y }, rects)
  if (!pane) { err('落点无效：请拖到左侧本地或右侧远程面板'); return }
  handleSystemDrop(pane, payload.paths)
}

async function handleSystemDrop(pane, paths) {
  const infos = await StatPaths(paths)
  for (const i of infos) if (i.err) err(i.path + '：' + i.err)
  const ok = infos.filter((i) => !i.err)
  if (!ok.length) return
  if (pane === 'remote') {
    if (!connected.value) { err('远程未连接，无法上传'); return }
    await runBatch({ direction: 'upload', names: ok.map((i) => i.name), sourceDir: '', targetDir: remotePath.value, sourceItems: localItems.value, systemPaths: ok })
    return
  }
  const base = (localPath.value || '/').replace(/\/+$/, '')
  const existing = (localItems.value || []).map((it) => it.name)
  const conflicts = ok.filter((i) => existing.includes(i.name))
  let policy = 'skip'
  if (needsConfirm({ conflictCount: conflicts.length, hiddenSelected: 0 })) {
    const chosen = await askConflict({ conflicts: conflicts.map((i) => ({ name: i.name })), total: ok.length, target: base, hiddenSelected: 0 }, 0)
    if (chosen === null) return
    policy = chosen
  }
  for (const i of ok) {
    const hasConflict = existing.includes(i.name)
    if (hasConflict && policy === 'skip') {
      transfers.value.push({ direction: 'copy', name: i.name, src: i.path, dst: base + '/' + i.name, size: i.size, status: '跳过', elapsed: 0 })
      continue
    }
    let name = i.name
    if (hasConflict && policy === 'rename') {
      name = copyName(i.name, existing)
      existing.push(name) // 累积已占用的名字，否则两个同名源会算出同一个新名
    }
    const dst = base + '/' + name
    const rec = { direction: 'copy', name, src: i.path, dst, size: i.size, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try { await CopyLocal(i.path, dst); rec.status = '完成' } catch (e) { rec.status = '失败'; rec.reason = String((e && e.message) || e); err(e) }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await loadLocal()
}
~~~

订阅与退订：

~~~js
onActivated(() => { offDrop = EventsOn('files:dropped', onFilesDropped) })
onDeactivated(() => { if (offDrop) { offDrop(); offDrop = null } })
onUnmounted(() => { if (offDrop) { offDrop(); offDrop = null } })
~~~

（`copyName` 来自 `utils/batch.js`，需与 `needsConfirm` 一起 import。）

- [ ] **Step 3: 构建 + 手工四路径验收**

Run: `cd frontend && npm run build`
手工（spec §12.3 第 4/10 条）：① 远程→本地拖 3 个文件；② 本地→远程拖一个目录；③ 从系统文件管理器拖两个文件到远程面板；④ 面板内把文件拖到子目录行。非法落点（拖到日志区、拖进自身子树）必须被拒绝且有提示。

- [ ] **Step 4: Commit**

~~~bash
git add frontend/src/views/SftpView.vue
git commit -m "feat(sftp): 四条拖拽路径接线（面板互拖/面板内移动/系统拖入）"
~~~

Run: `cd frontend && npm run build`
手工（§12.3 第 4/10 条）：① 远程→本地拖 3 个文件；② 本地→远程拖一个目录；③ 从系统文件管理器拖两个文件到远程面板；④ 面板内把文件拖到子目录行。非法落点（拖到日志区、拖进自身子树）必须被拒绝且有提示。

- [ ] **Step 4: Commit**

~~~bash
git add frontend/src/views/SftpView.vue
git commit -m "feat(sftp): 四条拖拽路径接线（面板互拖/面板内移动/系统拖入）"
~~~

---

### Task 16: README 与全量回归

**Files:**
- Modify: `README.md:76-78`（SFTP 功能条目）
- Modify: `README.en.md`（对应段落）

- [ ] **Step 1: 更新文档**

把 README 的 SFTP 条目替换为（英文版同义翻译）：

~~~markdown
- **SFTP 文件管理**：双栏浏览 / 多选（Ctrl 点选、Shift 区间、Ctrl+A）/ 批量下载·上传·删除 /
  右键菜单 / 拖动投递（双栏互拖、从系统拖入、拖到子目录移动）/ 每面板的「最近位置」与收藏 /
  即时过滤与递归深搜；「显示隐藏文件」开关；传输队列表含方向、源到目标、跳过与失败原因
~~~

- [ ] **Step 2: 全量回归**

Run: `make ci && make e2e`
Expected: vet 无输出；Go 测试（含 `-race`）全绿；vitest 全绿；e2e（含 Task 1 的探针段）全绿。

- [ ] **Step 3: Commit**

~~~bash
git add README.md README.en.md
git commit -m "docs(readme): 补充 SFTP 多选/批量/拖拽能力说明"
~~~
