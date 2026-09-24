# SFTP 面板排版修复（列头共轨 · 过滤栏下移 · 控件统一高度 · 多选入口）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修掉用户在 Windows 实机上报的两处缺陷——「过滤框占据『修改时间』列头位置导致该列无法排序」与「按钮/输入框高度不一致」——并落实已确认的 10 项排版/交互调整（列头与列表共用列轨道、过滤栏下移、控件统一 28px、位置弹层收窄、队列空态不占高、日志可拖、文案中文化、多选入口可见化 + Shift+↑↓ 区间）。

**Architecture:** 全部改动落在前端。把「列宽」「控件高度」「弹层宽度」「分段行数」四类魔数收敛为唯一来源（CSS 变量 + 常量），把可测逻辑抽成纯函数模块（`panefooter.js` / `logpane.js`，并扩展 `selection.js` / `keys.js` / `positions.js`），组件只负责渲染与上抛。跨文件形状沿用仓库既有的「读源码不变量」测试，渲染沿用 `@vue/server-renderer` 的 SSR 断言。**不改 Go 逻辑、不改绑定、不改 IPC、不加依赖。**

**Tech Stack:** Vue 3.5 + Pinia + vite 7 + vitest 4（无 jsdom / 无 @vue/test-utils，见 `frontend/src/components/TransferQueue.render.test.js` 的说明）；真机验收用 Xvfb/WebKitGTK（Linux）与 VirtualBox Windows 客户机（WebView2）。

**Spec:** 本轮无独立 spec；设计与取证见 `docs/superpowers/sketches/sftp-ui-proposed-wireframe.png`（源 `sftp-ui-proposed-wireframe.html`）与 `sftp-ui-current-wireframe.png`，逐项决策见下节。交互口径沿用 `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md` 的决策 6（单击=单选/双击=打开）与决策 32（可见顺序的唯一真源是 `showAll ∩ filter`）。

## 设计决策（已获用户确认）

| # | 问题 | 决策 | 取证/影响 |
|---|---|---|---|
| Q1 | 过滤框挤在列头行里 | **下移到面板底部一条过滤栏**（24–30px） | 实测：表头比列表多一个 132px 的过滤框 → 「大小」右边缘 163 vs 294、「修改时间」325 vs 450；过滤框 x 320–451 正好压在时间值上方，点它命中 input，`sortBy('modTime')` 收不到 |
| Q2 | 列头与列表列错位 | **两者共用同一套 grid 轨道** `minmax(0,1fr) / var(--col-size) / var(--col-time)` | 同上；列头行不再放任何非列头控件 |
| Q3 | 计数重复 | 未选中只显示淡灰「N 项」；选中显示「已选 n / N 项 ▾」（▾ 打开批量菜单） | 用户确认 |
| Q4 | 按钮/输入框高度不一致 | **全局 `--ctl-h: 28px`** + `box-sizing: border-box; height; line-height` | 实测同屏 34px（工具条）/ 29px（面板头、过滤框）/ ≈17px（路径框）三种高度 |
| Q5 | 位置弹层盖住面板且有横向滚动条 | 260 → **220px**，子项 `box-sizing: border-box`（防御性） | 实拍 P3 底部有 1566px 的 `#8B8B8B` 横向滚动条；根因是 v0.7.2 时代无全局 box-sizing 时 `width:100% + padding` 溢出——**该根因已被 Task 3 的全局规则消除**，Task 4 保留显式声明作为组件自身不变量 |
| Q6 | 位置分段太长 | `POSITION_SECTION_ROWS` 8 → **4** | 用户确认（上一轮已提过「最近太长」） |
| Q7 | 传输队列空态占一行 | 空态**不渲染**（高度 0） | 用户确认 |
| Q8 | 日志固定 140px | 加 **7px 可拖分隔条**（80–420px 夹取，**仅会话内记忆**） | 用户确认（不写设置文件 = 不碰 Go 配置结构）。**原计划的「chips 行固定高度」作废**：SFTP 页不传 `tunnels` ⇒ chips 恒空，去掉 v-if 只会留一条空带（reviewer 实测） |
| Q9 | 中英混排 | UI 文案统一中文；**日志正文保留英文** | 用户确认 |
| Q10 | 多选不好用 | **全选 / 反选 / 清空** 三个可见按钮放进过滤栏 + **Shift+↑/↓** 键盘区间扩展；**不加行内复选框** | 用户选 A：现有能力（Ctrl/Shift 点击、Ctrl+A、Esc、批量菜单里的全选/清空）已够，缺的是可见入口 |

## Global Constraints

- **无新增依赖**：不引入 jsdom / @vue/test-utils 类库；组件渲染测试沿用 `TransferQueue.render.test.js` 的 `renderToString` 做法。
- **不改 Go**：本轮零 Go 文件改动、零绑定改动、零 IPC 改动（日志高度只做会话内记忆，不落设置文件）。
- **列宽唯一来源**：`--col-size: 88px` 与 `--col-time: 140px` 只允许定义在 `FilePane.vue` 的 `.pane` 上；列头行与列表行都必须写 `grid-template-columns: minmax(0,1fr) var(--col-size) var(--col-time)`，任何组件不得再出现字面量 `88px`/`140px` 作为列宽。
- **控件高度唯一来源**：`--ctl-h: 28px` 只定义在 `frontend/src/style.css` 的 `:root`；`button/input/select` 统一 `box-sizing: border-box; height: var(--ctl-h)`。菜单项/胶囊类按钮（`.positem`/`.bmenu button`/`.chip`/`.act`/`.mini`/`.fx`）必须显式 `height: auto` 并保留自己的 padding。
- **弹层宽度唯一来源**：`POS_MENU_WIDTH = 220`（既用于内联宽度也用于锚点计算）。
- **键盘守卫不回归**：`utils/keys.js` 的 `isEditableTarget` 必须先于任何新增键位判断；焦点在 INPUT/TEXTAREA/SELECT/浮层内时，Delete / Ctrl+A / Esc / Shift+↑↓ 一律不处理（既有 `keys.test.js` 已钉住 INPUT 分支，新增键位要补同款用例）。
- **交互契约不变**：FilePane 现有 emits（`select / open / context / visible / focus / dragstart / dropon / clear / action / pick-position / toggle-bookmark / search / navigate`）语义全部不变；「..」仍然绝不进入 `selKeys`；`shown`（`showAll ∩ filter`）仍是可见顺序唯一真源，区间选择只作用于 `shown`。
- **主题无关**：只用既有 CSS 变量（`--bg / --bg-elev / --surface / --surface-hover / --border / --text / --text-dim / --text-faint / --accent`），不得出现字面色值；深浅主题都要截图确认。
- **提交规范**：简体中文 conventional commit，一次一块；**push / 打 tag 需另行获得用户授权**。
- **验收必须上真机**，且 **Windows 必测**（用户报的缺陷在 Windows/WebView2 上复现）。

## File Structure

| 文件 | 责任 | 变动 |
|---|---|---|
| `frontend/src/utils/panefooter.js` | 过滤栏文案与按钮可用性的纯函数（计数/已选/隐藏项、✕ 显隐） | Create |
| `frontend/src/utils/panefooter.test.js` | 上者单测 | Create |
| `frontend/src/utils/logpane.js` | 日志区高度夹取纯函数 | Create |
| `frontend/src/utils/logpane.test.js` | 上者单测 | Create |
| `frontend/src/utils/selection.js` | 选择模型：新增 `invert`、`cursor` 与 `extend` | Modify |
| `frontend/src/utils/selection.test.js` | 上者单测（补反选/区间扩展用例） | Modify |
| `frontend/src/utils/keys.js` | 新增 Shift+↑/↓ → `extend-up`/`extend-down` | Modify |
| `frontend/src/utils/keys.test.js` | 上者单测 | Modify |
| `frontend/src/utils/positions.js` | `POSITION_SECTION_ROWS` 8 → 4 | Modify |
| `frontend/src/components/FilePane.vue` | 导航行 / 列头行 / 列表 / 底部过滤栏四段结构；列轨道与过滤栏 | Modify（主要工作量） |
| `frontend/src/views/SftpView.vue` | 日志分隔条、`invert` 与 `extend-*` 分派、清空批量菜单里的重复项 | Modify |
| `frontend/src/components/TransferQueue.vue` | 空态不渲染 | Modify |
| `frontend/src/components/LogPanel.vue` | 中文文案（**保留** chips 的 `v-if`，不去掉） | Modify |
| `frontend/src/style.css` | `--ctl-h` 与控件统一尺寸 | Modify |
| `frontend/src/App.vue` / `SettingsDialog.vue` / `UpdateSection.vue` | 菜单类/图标类按钮 opt-out（侧栏按钮不 opt-out 会被压成 28px 裁字） | Modify |
| `frontend/src/styleTokens.test.js` | 四类唯一来源 + 菜单项 opted-out 的源码不变量 | Create |
| `frontend/src/components/filePaneWiring.test.js` | 面板结构不变量（列头共轨、过滤栏、三按钮） | Modify |
| `frontend/src/components/TransferQueue.render.test.js` | 空态不渲染的 SSR 断言 | Modify |
| `frontend/src/components/logPanel.test.js` | chips 仍带 v-if + 中文文案不变量 | Create |
| `docs/superpowers/reports/2026-09-24-sftp-pane-layout-acceptance.md` | 真机验收记录（判据 + 命令 + 原始输出 + 截图） | Create（Task 7） |

---


### Task 1: FilePane 结构重排（导航行 / 列头共轨 / 底部过滤栏）

**Files:**
- Create: `frontend/src/utils/panefooter.js`
- Create: `frontend/src/utils/panefooter.test.js`
- Modify: `frontend/src/components/FilePane.vue`（模板 183-253 行、样式 256-302 行）
- Test: `frontend/src/components/filePaneWiring.test.js`

**Interfaces:**
- Consumes: 无（本轮第一个任务）；沿用既有 props/emits。
- Produces:
  - `selectionLabel({ selected, total, hidden })` → `string`；`filterClearVisible(text)` → `boolean`；`EMPTY_FILTER_HINT = '按名称过滤…'`
  - DOM 契约：`[data-role="columns"]`（**必须位于 `.list-holder` 内**、`position: sticky`）、`[data-role="filterbar"]`、`[data-role="filter"]`、`[data-role="selinfo"]`、`[data-role="filter-clear"]`
  - CSS 变量 `--col-size: 88px` / `--col-time: 140px`（只定义在 `.pane` 上）

- [ ] **Step 1: 写失败的纯函数单测**

```js
// frontend/src/utils/panefooter.test.js
import { describe, it, expect } from 'vitest'
import { selectionLabel, filterClearVisible, EMPTY_FILTER_HINT } from './panefooter'

describe('selectionLabel', () => {
  it('未选中时只给总数（不再常驻「已选 0 项 ▾」入口）', () => {
    expect(selectionLabel({ selected: 0, total: 84 })).toBe('84 项')
  })
  it('选中时给出 已选 n / N 项', () => {
    expect(selectionLabel({ selected: 3, total: 84 })).toBe('已选 3 / 84 项')
  })
  it('含被过滤隐藏的已选项时如实标注', () => {
    expect(selectionLabel({ selected: 3, total: 84, hidden: 2 })).toBe('已选 3 / 84 项（含 2 项被过滤）')
  })
  it('无 hidden 时不带括号', () => {
    expect(selectionLabel({ selected: 3, total: 84, hidden: 0 })).toBe('已选 3 / 84 项')
  })
})

describe('filterClearVisible', () => {
  it('过滤框为空时不显示 ✕', () => {
    expect(filterClearVisible('')).toBe(false)
    expect(filterClearVisible(undefined)).toBe(false)
  })
  it('有内容时显示 ✕', () => {
    expect(filterClearVisible('a')).toBe(true)
  })
})

describe('EMPTY_FILTER_HINT', () => {
  it('文案明确是「按名称」（代码只匹配 it.name）', () => {
    expect(EMPTY_FILTER_HINT).toBe('按名称过滤…')
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/panefooter.test.js`
Expected: FAIL —— `Failed to resolve import "./panefooter"`

- [ ] **Step 3: 写最小实现**

```js
// frontend/src/utils/panefooter.js
// 过滤栏（面板底部）的文案与可用性纯函数：无 DOM 依赖，便于单测。
// 计数语义的唯一来源，FilePane 模板不得再自己拼字符串。

// 过滤框占位文案：代码只按 it.name 匹配（见 FilePane 的 hit()/shown），
// 所以必须写明「按名称」，否则用户会以为它能过滤大小/时间。
export const EMPTY_FILTER_HINT = '按名称过滤…'

export function selectionLabel({ selected, total, hidden = 0 }) {
  const n = Number(selected) || 0
  const m = Number(total) || 0
  if (!n) return m + ' 项'
  const base = '已选 ' + n + ' / ' + m + ' 项'
  return hidden ? base + '（含 ' + Number(hidden) + ' 项被过滤）' : base
}

export function filterClearVisible(text) {
  return String(text || '').length > 0
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/panefooter.test.js`
Expected: PASS（**7 个用例**）

- [ ] **Step 5: 先加结构不变量测试（此时必失败）**

在 `frontend/src/components/filePaneWiring.test.js` 末尾追加：

```js
describe('面板四段结构与列共轨', () => {
  // 抠出列头行那一小段：不要用 split('list-holder') 切，列头行改到 .list-holder 内部后那样会切错
  const hdrowOf = () => (filePane.match(/<div class="hdrow" data-role="columns">[\s\S]*?<\/div>/) || [''])[0]

  it('列头与列表共用同一套 grid 轨道（宽度只来自两个变量，没有字面量列宽）', () => {
    expect(filePane).toMatch(/--col-size:\s*88px/)
    expect(filePane).toMatch(/--col-time:\s*140px/)
    const grids = filePane.match(/grid-template-columns:\s*minmax\(0,1fr\) var\(--col-size\) var\(--col-time\)/g) || []
    expect(grids).toHaveLength(2) // .hdrow 与 .list li 各一条（grid 轨道必须逐选择器声明）
    expect(filePane.match(/grid-template-columns:[^;]*\b(88px|140px)\b/g) || []).toHaveLength(0)
  })
  it('表头与列表在同一个滚动容器里，表头 sticky（否则出现纵向滚动条时两行又会差一个滚动条宽）', () => {
    const holder = filePane.split('class="list-holder"')[1] || ''
    expect(holder).toContain('data-role="columns"')
    expect(holder).toContain('class="list"')
    expect(filePane).toMatch(/\.hdrow\s*\{[^}]*position:\s*sticky/)
    expect(filePane).toMatch(/\.list-holder\s*\{[^}]*overflow:\s*auto/)
    expect(filePane).toMatch(/\.list\s*\{[^}]*flex:\s*none/)
  })
  it('列头行只放三个列头、不再含输入框、且 nowrap', () => {
    const hd = hdrowOf()
    expect(hd).toContain('col-name')
    expect(hd).toContain('col-size')
    expect(hd).toContain('col-time')
    expect(hd).not.toContain('<input')
    expect(filePane).toMatch(/\.hdrow\s*\{[^}]*white-space:\s*nowrap/)
  })
  it('.list li 就地改成 grid（不要另写合并选择器，否则与旧 flex 规则同特异性、谁在后面谁赢）', () => {
    expect(filePane).toMatch(/\.list li\s*\{[^}]*display:\s*grid/)
    expect(filePane).not.toMatch(/\.hdrow, \.list li/)
  })
  it('底部过滤栏承载过滤框 + 计数；批量入口只在有选中时出现', () => {
    const bar = filePane.split('data-role="filterbar"')[1] || ''
    expect(bar).toContain('data-role="filter"')
    expect(bar).toContain('data-role="selinfo"')
    expect(bar).toContain('{{ selLabel }}')
    expect(filePane).toContain('selectionLabel(')      // 在 <script> 里，别限定在 bar 内断言
    expect(filePane).toMatch(/v-if="selKeys\.length"/)
    expect(filePane).not.toContain('selKeys.length || actions.length')
  })
  it('导航行只留 标题/路径/位置/收藏/深搜（计数与批量已下移）', () => {
    const nav = filePane.split('class="navrow"')[1].split('data-role="columns"')[0]
    expect(nav).toContain('data-role="path"')
    expect(nav).toContain('data-role="position"')
    expect(nav).toContain("emit('search')")
    expect(nav).not.toContain('class="count"')
    expect(nav).not.toContain('class="batch"')
  })
  it('过滤栏的批量菜单向上弹出（面板 overflow:hidden 会裁掉向下的菜单）', () => {
    expect(filePane).toMatch(/\.fbar \.bmenu\s*\{[^}]*bottom:\s*100%/)
    expect(filePane).toMatch(/\.fbar \.bmenu\s*\{[^}]*top:\s*auto/)
  })
})
```

- [ ] **Step 6: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/filePaneWiring.test.js`
Expected: FAIL —— `--col-size` 未定义 / `data-role="columns"` 不在 `.list-holder` 内

- [ ] **Step 7: 改 FilePane.vue 模板（三段：导航行 / 列头行 / 过滤栏）**

把 `<div class="head">…</div>` 整段**只**替换成导航行（列头行不在这里）：

```html
    <!-- ① 导航行：标题 + 路径 + 位置 + 收藏 + 深搜（计数/批量已下移到底部过滤栏） -->
    <div class="navrow">
      <span class="title">{{ title }}</span>
      <input class="curpath" data-role="path" spellcheck="false" :value="pathDraft"
        @input="pathDraft = $event.target.value"
        @keydown.enter="commitPath" @keyup.esc="resetPath" @blur="resetPath(); focus = false"
        @focus="focus = true; emit('focus')" />
      <div class="pos-wrap">
        <button class="pos" data-role="position" :class="{ on: posOpen }" title="位置" @click="togglePos">📍 位置 ▾</button>
        <div v-if="posOpen" class="posmenu" tabindex="-1" @click.stop
          :style="{ left: posAnchor.left + 'px', top: posAnchor.top + 'px', width: POS_MENU_WIDTH + 'px' }">
          <input class="posfilter" v-model="posFilter" placeholder="筛选位置…" />
          <template v-for="g in groups" :key="g.key">
            <div class="poslabel">
              <span>{{ g.label }}</span>
              <button v-if="g.expandable" class="more" @click="toggleGroup(g.key)">{{ posExpanded[g.key] ? '收起' : '展开全部 ' + g.total }}</button>
            </div>
            <button v-for="o in g.rows" :key="o.key" class="positem" :title="o.path" @click="pickPos(o.path)">{{ o.label }}</button>
          </template>
          <div v-if="!groups.length" class="posempty">没有匹配的位置</div>
        </div>
      </div>
      <button class="star" :class="{ on: bookmarked }" :title="bookmarked ? '取消收藏' : '收藏当前目录'" @click="emit('toggle-bookmark')">☆</button>
      <button class="find" title="递归深搜" @click="emit('search')">🔍</button>
    </div>
```

把列头行**放进 `.list-holder` 内部**（与 `<ul class="list">` 同一个滚动容器）——这是「表头与列表列永远对齐」的关键，也顺手让列头在滚动时保持可见：

```html
    <div class="list-holder">
      <!-- ② 列头行：只放三个列头；grid 轨道与 .list li 完全相同（宽度只来自 --col-size/--col-time） -->
      <div class="hdrow" data-role="columns">
        <span class="col-name sortable" :class="{ active: sortKey === 'name' }" @click="sortBy('name')">名称 {{ arrow('name') }}</span>
        <span class="col-size sortable" :class="{ active: sortKey === 'size' }" @click="sortBy('size')">大小 {{ arrow('size') }}</span>
        <span class="col-time sortable" :class="{ active: sortKey === 'modTime' }" @click="sortBy('modTime')">修改时间 {{ arrow('modTime') }}</span>
      </div>
      <ul class="list" :class="{ busy: loading }" @click.self="onListBlankClick">
        <!-- 既有 up 行与 v-for 行原样保留 -->
      </ul>
      <div v-if="loading" class="loading">加载中…</div>
    </div>
```

并在 `.list-holder` 之后、面板闭合 `</div>` 之前插入过滤栏：

```html
    <!-- ③ 底部过滤栏：过滤 + 计数/已选（批量菜单只在有选中时出现） -->
    <div class="fbar" data-role="filterbar">
      <span class="fwrap">
        <input class="filter" data-role="filter" v-model="filterText" :placeholder="EMPTY_FILTER_HINT" />
        <button v-if="filterClearVisible(filterText)" class="fx" data-role="filter-clear" title="清空过滤" @click="filterText = ''">✕</button>
      </span>
      <span class="selinfo" data-role="selinfo">{{ selLabel }}</span>
      <div v-if="selKeys.length" class="batch" @click.stop>
        <button class="chip" @click="menuOpen = !menuOpen">已选 {{ selKeys.length }} ▾</button>
        <div v-if="menuOpen" class="bmenu">
          <button v-for="a in actions" :key="a.name" :disabled="a.disabled" @click="pick(a.name)">{{ a.label }}</button>
        </div>
      </div>
    </div>
```

- [ ] **Step 8: 改 FilePane.vue 脚本（新增 import 与 computed）**

```js
import { selectionLabel, filterClearVisible, EMPTY_FILTER_HINT } from '../utils/panefooter'
// …既有 import 之后

// 底部过滤栏的计数文案（唯一来源：utils/panefooter.js）
const selLabel = computed(() => selectionLabel({
  selected: props.selKeys.length,
  total: shown.value.length,
  hidden: props.hiddenSelected,
}))
```

- [ ] **Step 9: 改 FilePane.vue 样式**

**必须删除/就地修改的既有规则**（漏删会让 Task 7 判据 A 失败）：

| 位置 | 旧规则 | 处理 |
|---|---|---|
| FilePane.vue:257 | `.pane { flex: 1; display: flex; flex-direction: column; overflow: hidden; }` | 就地加 `--col-size: 88px; --col-time: 140px;`（其余不动） |
| FilePane.vue:259 | `.head { padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); display: flex; justify-content: space-between; align-items: center; }` | **删除**（被 `.navrow` 取代） |
| FilePane.vue:283 | `.columns { display: flex; padding: 4px 8px; … }` | **删除**（被 `.hdrow` 取代） |
| FilePane.vue:287 | `.filter { width: 110px; font-size: var(--fs-11); }` | **删除**（改为 `.fwrap .filter`） |
| FilePane.vue:288 | `.list-holder { flex: 1; display: flex; flex-direction: column; position: relative; min-height: 0; }` | 就地加 `overflow: auto;`（滚动容器换成它） |
| FilePane.vue:289 | `.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }` | 改成 `… flex: none;` 且**去掉 `overflow: auto`** |
| FilePane.vue:292 | `.list li { display: flex; align-items: center; … }` | **就地改成 grid**（见下），不要另加合并选择器 |
| FilePane.vue:299 | `.col-name, .cell-name { flex: 1; overflow: hidden; text-overflow: ellipsis; padding-right: 16px; }` | **删除** |
| FilePane.vue:300 | `.col-size, .cell-size { width: 88px; text-align: right; padding-right: 16px; color: var(--text-dim); }` | **删除**——`padding-right:16px` 会把列表「大小」值推左 16px，列宽改由 `--col-size` 决定 |
| FilePane.vue:301 | `.col-time, .cell-time { width: 140px; text-align: right; color: var(--text-faint); }` | **删除**——同上，列宽改由 `--col-time` 决定 |

新增/替换的规则：

```css
/* 列宽唯一来源：列头行与列表行都引用这两个变量 */
.pane { --col-size: 88px; --col-time: 140px; flex: 1; display: flex; flex-direction: column; overflow: hidden; }
.navrow { display: flex; align-items: center; gap: 6px; padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); }
/* 表头与列表在同一个滚动容器里：表头 sticky，两者内容盒宽度必然一致。
   若把表头放在滚动容器外，列表出现纵向滚动条时内容会窄 ~15px，表头与行会再次错位。 */
.list-holder { flex: 1; display: flex; flex-direction: column; position: relative; min-height: 0; overflow: auto; }
.hdrow { display: grid; grid-template-columns: minmax(0,1fr) var(--col-size) var(--col-time); column-gap: 6px; align-items: center; position: sticky; top: 0; z-index: 5; padding: 4px 8px; font-size: var(--fs-11); color: var(--text-faint); background: var(--bg-elev); border-bottom: 1px solid var(--border); white-space: nowrap; }
.hdrow .col-size, .hdrow .col-time { text-align: right; }
.list { list-style: none; margin: 0; padding: 0; flex: none; }
.list li { display: grid; grid-template-columns: minmax(0,1fr) var(--col-size) var(--col-time); column-gap: 6px; align-items: center; padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: var(--fs-13); color: var(--text); white-space: nowrap; }
.sortable { cursor: pointer; user-select: none; }
.sortable:hover { color: var(--text-dim); }
.sortable.active { color: var(--accent); }
.cell-name { overflow: hidden; text-overflow: ellipsis; }
.cell-size { text-align: right; color: var(--text-dim); }
.cell-time { text-align: right; color: var(--text-faint); }
/* 底部过滤栏 */
.fbar { display: flex; align-items: center; gap: 6px; padding: 3px 8px; border-top: 1px solid var(--border); background: var(--bg-elev); flex: none; }
.fwrap { position: relative; flex: 1; min-width: 90px; display: flex; }
.fwrap .filter { flex: 1; min-width: 0; font-size: var(--fs-11); padding-right: 22px; }
.fx { position: absolute; right: 2px; top: 50%; transform: translateY(-50%); height: auto; padding: 0 4px; border: none; background: none; color: var(--text-faint); cursor: pointer; }
.selinfo { font-size: var(--fs-11); color: var(--text-faint); white-space: nowrap; }
/* 批量菜单向上弹出：过滤栏是面板最后一个子元素，top:100% 会开在面板外被 overflow:hidden 裁掉 */
.fbar .batch { position: relative; }
.fbar .bmenu { position: absolute; right: 0; bottom: 100%; top: auto; z-index: 20; }
```

`.list li.up`/`.sel`/`.anchor`/`:hover`、`mark`、`.loading`、`.posmenu` 等规则保持不动。副作用说明：`.loading` 现在位于滚动容器内，滚动时覆盖层会跟着内容走（仅加载期间可见，可接受）。

- [ ] **Step 10: 跑前端测试与构建**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部 PASS；build 无告警

- [ ] **Step 11: 提交**

```bash
git add frontend/src/utils/panefooter.js frontend/src/utils/panefooter.test.js \
        frontend/src/components/FilePane.vue frontend/src/components/filePaneWiring.test.js
git commit -m "fix(sftp): 列头与列表共用列轨道并同处滚动容器，过滤框下移到底部过滤栏"
```


### Task 2: 选择模型扩展（反选 / 区间扩展）+ 过滤栏三按钮 + Shift+↑↓

**Files:**
- Modify: `frontend/src/utils/selection.js`
- Modify: `frontend/src/utils/selection.test.js`
- Modify: `frontend/src/utils/keys.js`
- Modify: `frontend/src/utils/keys.test.js`
- Modify: `frontend/src/components/FilePane.vue`
- Modify: `frontend/src/views/SftpView.vue`
- Test: `frontend/src/components/filePaneWiring.test.js`

**Interfaces:**
- Consumes: Task 1 的 `[data-role="filterbar"]`；既有 `rangeTo(sel, visibleKeys, key)`。
- Produces: `invert(sel, visibleKeys)`（可见项取反，隐藏项保留）；`extend(sel, visibleKeys, delta)`（delta = ±1，以 `anchor` 为区间起点、`cursor` 为游标）；选择对象新增字段 `cursor`；`actionFor` 新增返回值 `'extend-up'` / `'extend-down'`；FilePane 新增 `[data-role="select-all"|"invert"|"clear"]` 按钮并 emit `action` 的 `'invert'`。

- [ ] **Step 1: 写失败的单测（selection）**

```js
// frontend/src/utils/selection.test.js
// 1) 把文件顶部既有的 import 行补成：
//    import { createSelection, single, toggle, rangeTo, all, invert, extend, clear, remove, isSelected, selectedNames }
//      from './selection'
// 2) 文件末尾追加：

describe('invert（反选）', () => {
  const vis = ['a', 'b', 'c']
  it('未选中时反选 = 选中全部可见项', () => {
    const s = createSelection()
    invert(s, vis)
    expect([...s.keys].sort()).toEqual(['a', 'b', 'c'])
  })
  it('已选 a/b 时反选 = 只剩 c', () => {
    const s = createSelection()
    single(s, 'a'); rangeTo(s, vis, 'b')
    invert(s, vis)
    expect([...s.keys]).toEqual(['c'])
  })
  it('不吞掉被过滤隐藏的已选项（与 all() 语义有意不同）', () => {
    const s = createSelection()
    s.keys = new Set(['hidden'])
    invert(s, vis)
    expect([...s.keys].sort()).toEqual(['a', 'b', 'c', 'hidden'])
  })
})

describe('all 与 extend 的配合（Ctrl+A 之后再按 Shift+↑/↓）', () => {
  const vis = ['a', 'b', 'c', 'd']
  it('全选后锚点在首项、游标在末项，Shift+↑ 从末尾逐格收窄（不会塌成一两项）', () => {
    const s = createSelection()
    all(s, vis)
    expect([...s.keys]).toEqual(vis)
    expect(s.anchor).toBe('a')
    expect(s.cursor).toBe('d')
    extend(s, vis, -1)
    expect([...s.keys]).toEqual(['a', 'b', 'c'])
  })
  it('空列表全选不炸，锚点/游标为 null', () => {
    const s = createSelection()
    all(s, [])
    expect(s.keys.size).toBe(0)
    expect(s.anchor).toBe(null)
    expect(s.cursor).toBe(null)
  })
})

describe('extend（Shift+↑/↓）', () => {
  const vis = ['a', 'b', 'c', 'd']
  it('从游标向下扩展一格并保持 anchor 不动', () => {
    const s = createSelection()
    single(s, 'b')
    extend(s, vis, 1)
    expect([...s.keys]).toEqual(['b', 'c'])
    expect(s.anchor).toBe('b')
    extend(s, vis, 1)
    expect([...s.keys]).toEqual(['b', 'c', 'd'])
  })
  it('向上扩展回缩区间', () => {
    const s = createSelection()
    single(s, 'c')
    extend(s, vis, -1)
    expect([...s.keys]).toEqual(['b', 'c'])
  })
  it('越过两端时夹取，不越界', () => {
    const s = createSelection()
    single(s, 'a')
    extend(s, vis, -1)
    expect([...s.keys]).toEqual(['a'])
    single(s, 'd'); extend(s, vis, 1)
    expect([...s.keys]).toEqual(['d'])
  })
  it('空可见列表是 no-op', () => {
    const s = createSelection()
    extend(s, [], 1)
    expect(s.keys.size).toBe(0)
  })
  it('清空后游标也复位', () => {
    const s = createSelection()
    single(s, 'a'); clear(s)
    expect(s.cursor).toBe(null)
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/selection.test.js`
Expected: FAIL —— `invert is not a function`

- [ ] **Step 3: 实现 invert / cursor / extend**

```js
// selection.js：createSelection 增加 cursor
export function createSelection() { return { keys: new Set(), anchor: null, cursor: null } }

export function single(sel, key) {
  sel.keys = new Set([key])
  sel.anchor = key
  sel.cursor = key
}

export function toggle(sel, key) {
  const next = new Set(sel.keys)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  sel.keys = next
  sel.anchor = key
  sel.cursor = key
}

// rangeTo：只更新 keys 与游标，anchor 是区间起点、必须保持不变（Shift+↑↓ 依赖这一点）
export function rangeTo(sel, visibleKeys, key) {
  const anchor = sel.anchor
  if (!anchor || !visibleKeys.includes(anchor) || !visibleKeys.includes(key)) {
    single(sel, key)
    return
  }
  const i = visibleKeys.indexOf(anchor)
  const j = visibleKeys.indexOf(key)
  sel.keys = new Set(visibleKeys.slice(Math.min(i, j), Math.max(i, j) + 1))
  sel.cursor = key
}

// invert：只对可见项取反；被过滤隐藏的已选项保留（反选不该吞掉用户先前的选择）。
// 注意这与 all() 有意不同——all() 是「把选中集替换为全部可见项」。
export function invert(sel, visibleKeys) {
  const next = new Set()
  for (const k of visibleKeys) if (!sel.keys.has(k)) next.add(k)
  for (const k of sel.keys) if (!visibleKeys.includes(k)) next.add(k)
  sel.keys = next
  sel.anchor = null
  sel.cursor = null
}

// extend：Shift+↑/↓ 的区间扩展。anchor 固定、cursor 移动；越界夹取。
export function extend(sel, visibleKeys, delta) {
  if (!visibleKeys.length) return
  const from = (sel.cursor && visibleKeys.includes(sel.cursor)) ? sel.cursor
    : (sel.anchor && visibleKeys.includes(sel.anchor)) ? sel.anchor : null
  const idx = from ? visibleKeys.indexOf(from) : -1
  const nextIdx = idx < 0
    ? (delta > 0 ? 0 : visibleKeys.length - 1)
    : Math.min(visibleKeys.length - 1, Math.max(0, idx + delta))
  const key = visibleKeys[nextIdx]
  if (!sel.anchor || !visibleKeys.includes(sel.anchor)) sel.anchor = key
  const i = visibleKeys.indexOf(sel.anchor)
  sel.keys = new Set(visibleKeys.slice(Math.min(i, nextIdx), Math.max(i, nextIdx) + 1))
  sel.cursor = key
}

// all：把选中集替换成全部可见项，并让锚点/游标有确定含义——
// 锚点=首项、游标=末项，这样 Ctrl+A 之后再按 Shift+↑ 是从末尾收窄，而不是塌成一两项。
export function all(sel, visibleKeys) {
  sel.keys = new Set(visibleKeys)
  sel.anchor = visibleKeys.length ? visibleKeys[0] : null
  sel.cursor = visibleKeys.length ? visibleKeys[visibleKeys.length - 1] : null
}

export function clear(sel) { sel.keys = new Set(); sel.anchor = null; sel.cursor = null }

// remove：顺带清理失效游标
export function remove(sel, keys) {
  const next = new Set(sel.keys)
  for (const k of keys) next.delete(k)
  sel.keys = next
  if (sel.anchor && !next.has(sel.anchor)) sel.anchor = null
  if (sel.cursor && !next.has(sel.cursor)) sel.cursor = null
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/selection.test.js`
Expected: PASS（既有用例 + 新增 8 个）

- [ ] **Step 5: 写失败的键位单测**

```js
// 追加到 frontend/src/utils/keys.test.js
it('Shift+↑/↓ 映射为区间扩展', () => {
  expect(actionFor({ ...ev('ArrowUp'), shiftKey: true })).toBe('extend-up')
  expect(actionFor({ ...ev('ArrowDown'), shiftKey: true })).toBe('extend-down')
})
it('不带 Shift 的方向键不产生动作（交给浏览器滚动）', () => {
  expect(actionFor(ev('ArrowDown'))).toBe(null)
})
it('焦点在输入框时 Shift+↑/↓ 不处理', () => {
  expect(actionFor({ ...ev('ArrowDown', { target: input() }), shiftKey: true })).toBe(null)
})
```

- [ ] **Step 6: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/keys.test.js`
Expected: FAIL —— `expected null to be 'extend-up'`

- [ ] **Step 7: 实现键位映射**

```js
// keys.js：ACTION 映射集中在一处，守卫判断必须最先执行
const ARROW_EXTEND = { ArrowUp: 'extend-up', ArrowDown: 'extend-down' }

export function actionFor(ev) {
  if (!ev || isEditableTarget(ev.target)) return null
  if (ev.key === 'Delete') return 'delete'
  if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') return 'select-all'
  if (ev.shiftKey && ARROW_EXTEND[ev.key]) return ARROW_EXTEND[ev.key]
  if (ev.key === 'Escape') return 'escape'
  return null
}
```

- [ ] **Step 8: 过滤栏三按钮 + SftpView 分派**

FilePane 过滤栏（Task 1 的 `.fbar`）里、`selinfo` 之前插入：

```html
      <button class="mini" data-role="select-all" :disabled="!shown.length" title="全选当前可见项" @click="emit('action', 'select-all')">全选</button>
      <button class="mini" data-role="invert" :disabled="!shown.length" title="反选当前可见项" @click="emit('action', 'invert')">反选</button>
      <button class="mini" data-role="clear" :disabled="!selKeys.length" title="清空选择" @click="emit('action', 'clear')">清空</button>
```

样式：`.mini { height: auto; font-size: var(--fs-11); padding: 1px 6px; }`

SftpView **不需要改 import**：它已经用 `import * as sel from '../utils/selection'`（SftpView.vue:13），新增的 `invert`/`extend` 会自动挂在 `sel` 上。

SftpView 的 `onPaneAction` 增加一行、`onKeydown` 增加两支：

```js
// onPaneAction
if (name === 'invert') return sel.invert(s, visibleFor(pane))

// onKeydown（既有 delete/select-all/escape 分支之后）
else if (action === 'extend-up') { ev.preventDefault(); sel.extend(s, vis, -1) }
else if (action === 'extend-down') { ev.preventDefault(); sel.extend(s, vis, 1) }
```

同时从 `actionsFor(pane)` 的 tail 里**只删菜单项** `{ name: 'select-all', label: '全选' }` 与 `{ name: 'clear', label: '清空' }`（避免同一功能两个入口）。

**`onPaneAction` 里 `select-all` / `clear` 两支必须保留**：过滤栏三个按钮走的就是 `emit('action', ...)` → `onPaneAction` 这条既有链路，删了按钮就失效。

- [ ] **Step 9: 补结构不变量测试**

```js
describe('过滤栏的多选入口', () => {
  it('三个按钮都在过滤栏里，且都 emit(action, ...)', () => {
    const bar = filePane.split('data-role="filterbar"')[1]
    expect(bar).toContain('data-role="select-all"')
    expect(bar).toContain('data-role="invert"')
    expect(bar).toContain('data-role="clear"')
    expect(bar).toMatch(/emit\('action', 'invert'\)/)
  })
  it('SftpView 分派 invert 与 extend-up/extend-down', () => {
    // sftp 是本文件顶部既有的 readFileSync(new URL('../views/SftpView.vue', import.meta.url)) 常量
    expect(sftp).toContain("name === 'invert'")
    expect(sftp).toContain("action === 'extend-up'")
    expect(sftp).toContain("action === 'extend-down'")
  })
})
```

- [ ] **Step 10: 跑全套前端测试 + 构建**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部 PASS

- [ ] **Step 11: 提交**

```bash
git add frontend/src/utils/selection.js frontend/src/utils/selection.test.js \
        frontend/src/utils/keys.js frontend/src/utils/keys.test.js \
        frontend/src/components/FilePane.vue frontend/src/views/SftpView.vue \
        frontend/src/components/filePaneWiring.test.js
git commit -m "feat(sftp): 过滤栏加全选/反选/清空，支持 Shift+上下键区间选择"
```


### Task 3: 全局控件尺寸统一（--ctl-h）

**Files:**
- Modify: `frontend/src/style.css`
- Modify: `frontend/src/App.vue`、`frontend/src/components/FilePane.vue`、`frontend/src/components/LogPanel.vue`、`frontend/src/components/TransferQueue.vue`、`frontend/src/components/ContextMenu.vue`、`frontend/src/components/SettingsDialog.vue`、`frontend/src/components/UpdateSection.vue`（菜单类/胶囊类/图标类按钮 opt-out）
- Create: `frontend/src/styleTokens.test.js`

**Interfaces:**
- Consumes: Task 1 的 `.fbar`/`.hdrow`、Task 2 的 `.mini`。
- Produces:
  - `--ctl-h: calc(28px * var(--ui-scale))`（唯一来源；随用户「字号」档位 0.9/1/1.15 缩放）
  - 高度规则选择器 `button, input:not([type="checkbox"]):not([type="radio"]), select` **只准放盒模型属性**（`box-sizing`/`height`/`line-height`）
  - 视觉属性（`background`/`border`/`border-radius`/`padding`）必须留在**元素级**规则 `button, input, select` 里

> **特异性红线（reviewer 实测指出）**：`input:not([type="checkbox"]):not([type="radio"])` 的特异性是 **(0,2,1)**，会压过 scoped 的 `.curpath[data-v-x]` **(0,2,0)**。若把 `border/border-radius/background` 放进前者，路径框会从「静默、hover 才显边框」变成常显边框 + 6px 圆角——这就是本任务最容易踩的回归。

- [ ] **Step 1: 写失败的源码不变量测试**

```js
// frontend/src/styleTokens.test.js
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8')

describe('控件尺寸的单一来源', () => {
  const css = read('./style.css')
  // 注意：CSS 里这条选择器写成多行，必须用正则匹配，精确子串会切不到
  const heightBlock = () => (css.split(/button,\s*input:not\(\[type="checkbox"\]\):not\(\[type="radio"\]\),\s*select\s*\{/)[1] || '').split('}')[0]
  const baseBlock = () => (css.split(/^button, input, select \{/m)[1] || '').split('}')[0]

  it('--ctl-h 只定义一次，且随 --ui-scale 缩放', () => {
    const hits = css.match(/--ctl-h:\s*[^;]+;/g) || []
    expect(hits).toHaveLength(1)
    expect(hits[0]).toContain('var(--ui-scale)')
  })
  it('高度规则只含盒模型属性（不能含视觉属性，否则压过 scoped 的 .curpath）', () => {
    const b = heightBlock()
    expect(b).toContain('box-sizing: border-box')
    expect(b).toContain('height: var(--ctl-h)')
    expect(b).toContain('line-height')
    expect(b).not.toContain('background')
    expect(b).not.toContain('border-radius')
    expect(b).not.toMatch(/border:\s*1px/)
  })
  it('元素级规则仍是 button, input, select（低特异性），padding 改成 0 10px', () => {
    const b = baseBlock()
    expect(b).toContain('padding: 0 10px')
    expect(b).not.toContain('height: var(--ctl-h)')   // 高度只在上面那条规则里
  })
  it('复选框/单选框的 native 口径保留（不被统一高度波及）', () => {
    expect(css).toMatch(/input\[type="checkbox"\], input\[type="radio"\]\s*\{/)
  })
  it('菜单类/胶囊类/图标类按钮显式 opt-out（清单见计划表，一处都不能少）', () => {
    const pane = read('./components/FilePane.vue')
    expect(pane).toMatch(/\.positem\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.poslabel \.more\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.batch \.chip\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.bmenu button\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.mini\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.fx\s*\{[^}]*height:\s*auto/)
    expect(read('./components/LogPanel.vue')).toMatch(/\.chip\s*\{[^}]*height:\s*auto/)
    expect(read('./components/TransferQueue.vue')).toMatch(/\.act\s*\{[^}]*height:\s*auto/)
    expect(read('./components/TransferQueue.vue')).toMatch(/\.copy\s*\{[^}]*height:\s*auto/)
    expect(read('./components/ContextMenu.vue')).toMatch(/:deep\(button\)\s*\{[^}]*height:\s*auto/)
    expect(read('./App.vue')).toMatch(/\.sidebar button\s*\{[^}]*height:\s*auto/)
    expect(read('./App.vue')).toMatch(/\.fatal-close\s*\{[^}]*height:\s*auto/)
    expect(read('./components/SettingsDialog.vue')).toMatch(/\.dclose\s*\{[^}]*height:\s*auto/)
    expect(read('./components/UpdateSection.vue')).toMatch(/\.link\s*\{[^}]*height:\s*auto/)
  })
  it('列宽魔数不再散落（88px/140px 只作为 --col-size/--col-time 的值出现）', () => {
    const pane = read('./components/FilePane.vue')
    expect(pane.match(/grid-template-columns:[^;]*\b(88px|140px)\b/g) || []).toHaveLength(0)
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/styleTokens.test.js`
Expected: FAIL —— `--ctl-h` 未定义

- [ ] **Step 3: 改 style.css（两条规则，分工明确）**

```css
  --seed: #f59e0b;
  /* 所有 button/input/select 的统一高度：唯一来源。
     修 Windows/WebView2 上「同屏 34/29/17 三种高度」的根因——
     此前只统一了 padding，没有 height/line-height/box-sizing。
     乘 --ui-scale 是为了跟随用户「字号」档位（0.9 / 1 / 1.15）。 */
  --ctl-h: calc(28px * var(--ui-scale));
```

把既有 `button, input, select { … padding: 6px 10px; }` 的 padding 改成 `padding: 0 10px`（**选择器不动**，保持 (0,0,1) 低特异性，让 `.curpath` 的 `padding: 0 6px` 继续生效），并**新增**一条只放盒模型属性的规则：

```css
button,
input:not([type="checkbox"]):not([type="radio"]),
select {
  box-sizing: border-box;
  height: var(--ctl-h);
  line-height: normal;
}
/* 单行文本框：把行高设成「高度 - 上下边框」，文字才会垂直居中 */
input:not([type="checkbox"]):not([type="radio"]) { line-height: calc(var(--ctl-h) - 2px); }
/* button/select 用 flex 居中，避免 Chromium/WebKit 行盒差异 */
button, select { display: inline-flex; align-items: center; justify-content: center; line-height: 1; }
```

**不要**把 `background/border/border-radius` 搬进上面的 `:not()` 规则——那会按特异性压过 `.curpath[data-v]`，把静默路径框变成常显边框（见本任务开头的红线）。

- [ ] **Step 4: 给 13 处菜单类/胶囊类/图标类按钮加 opt-out（补 `height: auto``，各自 padding 保留）**

| 文件 | 选择器 | 为什么必须 opt-out |
|---|---|---|
| App.vue | `.sidebar button`（:106，`padding: 12px 16px`） | 不 opt-out 会从 ~44px 压到 28px，上下 padding 合计 24px > 28px → **16px 文字被裁切**（reviewer 实测指出的回归） |
| App.vue | `.fatal-close`（:113） | 图标 ✕，`padding: 0 4px` |
| FilePane.vue | `.positem` | 位置弹层菜单项 |
| FilePane.vue | `.poslabel .more` | 「展开全部 N / 收起」小按钮 |
| FilePane.vue | `.batch .chip` | 批量入口胶囊 |
| FilePane.vue | `.bmenu button` | 批量菜单项 |
| FilePane.vue | `.mini`（Task 2 新增） | 过滤栏「全选/反选/清空」 |
| FilePane.vue | `.fx`（Task 1 新增） | 过滤框 ✕ |
| LogPanel.vue | `.chip` | 来源胶囊 |
| TransferQueue.vue | `.act` | 取消/重试/续传/清理小按钮 |
| TransferQueue.vue | `.copy`（**现无样式规则**，需新增 `.copy { height: auto }`） | 「复制失败清单」按钮在 `.qsum` 里，不能变成 28px 撑高汇总行 |
| ContextMenu.vue | `.menu :deep(button)`（:15） | 右键菜单项 |
| SettingsDialog.vue | `.dclose`（:144） | 对话框关闭 ✕ |
| UpdateSection.vue | `.update .link`（:239） | 行内文字链接（否则变 inline-flex 居中） |

复选框/单选框被 `:not()` 排除在高度规则外（`style.css:117` 的 native 口径不变），但会失去旧的 `padding: 6px 10px` → 命中区域略变小（功能不变），Step 6 逐页确认。

- [ ] **Step 5: 跑测试确认通过 + 构建**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部 PASS

- [ ] **Step 6: 真机逐页视觉自检（改动面最大的一步）**

Run: 用 `real-machine-testing` skill 的 Linux 通道启动**真实应用**（`wails build` + Xvfb + `WEBKIT_DISABLE_COMPOSITING_MODE=1`），依次打开 端口转发 / 文件同步 / SFTP / 设置 / 右键菜单 / 位置弹层 / 深搜浮层，逐页截图
Expected: 同一行内控件等高；侧栏导航按钮高度不变、文字不裁切；菜单项/胶囊 chip 没被撑高；复选框没被撑成 28px；无文字被裁切
加做一档字号：把设置的「字号」调到最大档（`--ui-scale: 1.15`，见 `stores/settings.js:12-16`）再截一次 SFTP 页，控件与文字不得互相裁切。
（`npm run dev` 只有 Vite 壳、没有 Go 绑定，列表会是空的、看不了真实排版，不能作为本步证据）

- [ ] **Step 7: 提交**

```bash
git add frontend/src/style.css frontend/src/styleTokens.test.js \
        frontend/src/App.vue frontend/src/components/FilePane.vue \
        frontend/src/components/LogPanel.vue frontend/src/components/TransferQueue.vue \
        frontend/src/components/ContextMenu.vue frontend/src/components/SettingsDialog.vue \
        frontend/src/components/UpdateSection.vue
git commit -m "fix(ui): 统一 button/input/select 高度并随字号缩放，补全菜单类按钮 opt-out"
```

> **风险**：这是全局样式，会影响所有页面。必须单独一个 commit，便于整体 revert。

### Task 4: 位置弹层收窄与溢出修复（220px / box-sizing / 默认 4 行）

**Files:**
- Modify: `frontend/src/components/FilePane.vue`（`POS_MENU_WIDTH`、`.positem`/`.posfilter` 样式）
- Modify: `frontend/src/utils/positions.js`
- Modify: `frontend/src/utils/positions.test.js`、`frontend/src/components/filePaneWiring.test.js`

**Interfaces:**
- Consumes: Task 1 的列轨道（弹层锚点算法不变）；**Task 3 的全局 `box-sizing: border-box`**（见下方前提纠正）。
- Produces: `POS_MENU_WIDTH = 220`；`POSITION_SECTION_ROWS = 4`（仍是唯一常量来源）。

> **前提纠正（Task 4 实现+审查后补记）**：本文档最初写的「项目没有全局 `box-sizing: border-box`，故 `width:100% + padding` 必然横向溢出」在 **Task 3 落地后已过期**——Task 3 新增的 `button, input:not([type=checkbox]):not([type=radio]), select { box-sizing: border-box }` 已经覆盖 `.positem`(`<button>`)/`.posfilter`(`<input>`)，**横向滚动条实际是 Task 3 修掉的**；Task 4 里那两处显式声明是**防御性**的（组件不依赖全局规则、并被源码断言钉住），不是修复本身。
> 证据：Task 3 之后拍的 `/tmp/sshkit-t3/shots/09-posmenu.png`（当时弹层仍是 260px）底部**没有**滚动条；而 v0.7.2 基线 `/tmp/rel072-shots/P3-scrolled-to-last-row.png` 底部有 1566px 的 `#8B8B8B` 带。最终判定仍以 Task 7 的 W3 真机判据为准。

- [ ] **Step 1: 写失败的测试**

```js
// 追加到 frontend/src/components/filePaneWiring.test.js
it('弹层宽度收窄到 220px，且锚点算法与内联宽度用同一个常量', () => {
  expect(filePane).toMatch(/POS_MENU_WIDTH\s*=\s*220/)
  expect(filePane).toContain('r.right - POS_MENU_WIDTH')
})
// 防御性声明：Task 3 的全局规则已覆盖这两个元素，但组件自身的「不溢出」不变量
// 不应依赖全局规则（未来被 scope 掉就会静默回归），所以显式声明 + 源码断言钉住。
// 真正的「无横向滚动条」由 Task 7 的 W3 真机像素判据验证。
it('弹层子项显式 border-box（组件自身不变量，不依赖全局规则）', () => {
  expect(filePane).toMatch(/\.positem\s*\{[^}]*box-sizing:\s*border-box/)
  expect(filePane).toMatch(/\.posfilter\s*\{[^}]*box-sizing:\s*border-box/)
})
```

```js
// 1) 追加到 frontend/src/utils/positions.test.js
it('默认每区 4 行（用户反馈「最近太长」）', () => {
  expect(POSITION_SECTION_ROWS).toBe(4)
  const recents = Array.from({ length: 9 }, (_, i) => ({ key: 'r' + i, label: 'r' + i, path: '/r' + i }))
  const g = positionGroups({ recents }).find((x) => x.key === 'recent')
  expect(g.rows).toHaveLength(4)
  expect(g.expandable).toBe(true)
  expect(g.total).toBe(9)
})

// 2) **必须同步改既有用例**：positions.test.js:38-41 现在写死了 8
//    const exactly8 = positionGroups({ recents: recents.slice(0, 8) }).find((x) => x.key === 'recent')
//    expect(exactly8.total).toBe(POSITION_SECTION_ROWS)      ← 常量改 4 后这条会红
//    改成与常量同源，别留字面量：
//    const exactlyRows = positionGroups({ recents: recents.slice(0, POSITION_SECTION_ROWS) }).find((x) => x.key === 'recent')
//    expect(exactlyRows.total).toBe(POSITION_SECTION_ROWS)
//    expect(exactlyRows.rows).toHaveLength(POSITION_SECTION_ROWS)
//    expect(exactlyRows.expandable).toBe(false)
//    并把上一行的 recents 数组长度改为 POSITION_SECTION_ROWS * 2 + 1（原先按 9 条写，改 4 后仍要 > 2×阈值）
```

> 历史文档不要回改：`docs/superpowers/reports/2026-09-20-sftp-pane-ux-polish-acceptance.md` 里「最近 ≤8 行」记录的是 v0.7.2 当时的真机行为，属历史事实，保持原样；阈值变更只写进本轮 Task 7 的新报告。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/positions.test.js src/components/filePaneWiring.test.js`
Expected: FAIL —— `expected 8 to be 4` / 找不到 `POS_MENU_WIDTH = 220`

- [ ] **Step 3: 改实现**

```js
// FilePane.vue
const POS_MENU_WIDTH = 220
```

```js
// positions.js
// 每区默认显示行数：用户反馈「最近/搜索展开太长」，从 8 收到 4（唯一来源）
export const POSITION_SECTION_ROWS = 4
```

```css
.positem { box-sizing: border-box; /* 防御性：全局规则已覆盖，但组件不变量不应依赖全局 */ /* ...既有规则 */ }
.posfilter { box-sizing: border-box; /* 同上 */ /* ...既有规则 */ }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/positions.test.js src/components/filePaneWiring.test.js`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/FilePane.vue frontend/src/utils/positions.js \
        frontend/src/utils/positions.test.js frontend/src/components/filePaneWiring.test.js
git commit -m "fix(sftp): 位置弹层收窄到 220px 并修掉横向溢出，分段默认 4 行"
```

---

### Task 5: 传输队列空态不占高

**Files:**
- Modify: `frontend/src/components/TransferQueue.vue`
- Test: `frontend/src/components/TransferQueue.render.test.js`

**Interfaces:**
- Consumes: 无。
- Produces: `<TransferQueue>` 在 `transfers.length === 0` 时渲染空内容（无 `.queue` 根元素、无「无传输任务」文案）。

- [ ] **Step 1: 写失败的 SSR 断言**

```js
// 追加到 TransferQueue.render.test.js
it('无任务时不渲染任何内容（空态不再占一行高度）', async () => {
  // render(transfers) 是本文件顶部既有的辅助：renderToString(createSSRApp(...))
  const html = await render([])
  expect(html).not.toContain('class="queue"')
  expect(html).not.toContain('无传输任务')
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/TransferQueue.render.test.js`
Expected: FAIL —— 输出里含 `无传输任务`

- [ ] **Step 3: 改模板**

```html
<template>
  <div v-if="transfers.length" class="queue">
    ...
    <div class="qsum"> ... </div>
  </div>
</template>
```

删除 `<div v-if="!transfers.length" class="empty">无传输任务</div>` 与 `.empty` 样式；`.qsum` 的 `v-if="transfers.length"` 可一并去掉（外层已保证）。

注意：组件整体 `v-if` 为假时 Vue 渲染的是注释节点，注释节点不是 flex item，所以父容器 `.sftp{gap:8px}` **不会**因此多出一个 8px 空隙——不需要额外包一层。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/components/TransferQueue.render.test.js`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add frontend/src/components/TransferQueue.vue frontend/src/components/TransferQueue.render.test.js
git commit -m "fix(sftp): 传输队列空态不占高度"
```


### Task 6: 日志区分隔条可拖 + 文案中文化 + 键盘守卫

**Files:**
- Create: `frontend/src/utils/logpane.js`、`frontend/src/utils/logpane.test.js`
- Create: `frontend/src/components/logPanel.test.js`
- Modify: `frontend/src/views/SftpView.vue`
- Modify: `frontend/src/components/LogPanel.vue`
- Modify: `frontend/src/utils/keys.js`（把 `[role="separator"]` 纳入键盘守卫，见 Step 5）
- Test: `frontend/src/utils/keys.test.js`（补一条「焦点在分隔条上时 Delete / Shift+↑↓ 都不处理」）

**Interfaces:**
- Consumes: 既有 `<LogPanel :source-types="['sftp','system']" />`。
- Produces: `clampLogHeight(v, { min, max })` → `number`；`LOG_MIN_H = 80` / `LOG_MAX_H = 420` / `LOG_DEFAULT_H = 140`；SftpView 新 DOM 契约 `[data-role="log-splitter"]`（`role="separator"` + `tabindex="0"`，支持鼠标拖与键盘 ↑/↓）。

- [ ] **Step 1: 写失败的纯函数单测**

```js
// frontend/src/utils/logpane.test.js
import { describe, it, expect } from 'vitest'
import { clampLogHeight, LOG_MIN_H, LOG_MAX_H, LOG_DEFAULT_H } from './logpane'

describe('clampLogHeight', () => {
  it('夹在上下限之间', () => {
    expect(clampLogHeight(10)).toBe(LOG_MIN_H)
    expect(clampLogHeight(9999)).toBe(LOG_MAX_H)
    expect(clampLogHeight(200)).toBe(200)
  })
  it('非数字回落到默认高度', () => {
    expect(clampLogHeight(undefined)).toBe(LOG_DEFAULT_H)
    expect(clampLogHeight(NaN)).toBe(LOG_DEFAULT_H)
  })
  it('默认高度在范围内', () => {
    expect(LOG_DEFAULT_H).toBeGreaterThanOrEqual(LOG_MIN_H)
    expect(LOG_DEFAULT_H).toBeLessThanOrEqual(LOG_MAX_H)
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/logpane.test.js`
Expected: FAIL —— 模块不存在

- [ ] **Step 3: 写最小实现**

```js
// frontend/src/utils/logpane.js
// 日志区高度的唯一来源与夹取。仅会话内记忆，不落设置文件（不碰 Go 配置结构）。
export const LOG_MIN_H = 80
export const LOG_MAX_H = 420
export const LOG_DEFAULT_H = 140

export function clampLogHeight(v, { min = LOG_MIN_H, max = LOG_MAX_H } = {}) {
  const n = Number(v)
  if (!Number.isFinite(n)) return LOG_DEFAULT_H
  return Math.min(max, Math.max(min, Math.round(n)))
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/utils/logpane.test.js`
Expected: PASS

- [ ] **Step 5: SftpView 接分隔条（鼠标拖 + 键盘 ↑↓）**

模板：把 `<div class="logpane ui-panel">...` 换成

```html
    <div class="splitter" data-role="log-splitter" role="separator" aria-orientation="horizontal"
      tabindex="0" title="拖动调整日志高度" @mousedown="onSplitterDown" @keydown="onSplitterKey"></div>
    <div class="logpane ui-panel" :style="{ height: logHeight + 'px' }"><LogPanel :source-types="['sftp', 'system']" /></div>
```

样式（替换既有 `.logpane { height: 140px; ... }` 的固定高度）：

```css
.splitter { height: 7px; flex: none; cursor: row-resize; background: var(--bg-elev); border-top: 1px solid var(--border); border-bottom: 1px solid var(--border); }
.logpane { flex: none; padding: 12px; overflow: auto; }
```

脚本：

```js
import { clampLogHeight, LOG_DEFAULT_H } from '../utils/logpane'
const logHeight = ref(LOG_DEFAULT_H)   // 仅会话内记忆：窗口重开回到默认
let splitterStart = null
function onSplitterDown(ev) {
  splitterStart = { y: ev.clientY, h: logHeight.value }
  window.addEventListener('mousemove', onSplitterMove)
  window.addEventListener('mouseup', onSplitterUp)
  ev.preventDefault()
}
function onSplitterMove(ev) {
  if (!splitterStart) return
  logHeight.value = clampLogHeight(splitterStart.h + (splitterStart.y - ev.clientY))
}
function onSplitterUp() {
  splitterStart = null
  window.removeEventListener('mousemove', onSplitterMove)
  window.removeEventListener('mouseup', onSplitterUp)
}
function onSplitterKey(ev) {
  // Shift+↑/↓ 是「文件选择区间扩展」的键位（Task 2）：焦点在分隔条上时不要抢，直接让给全局处理
  if (ev.shiftKey) return
  if (ev.key === 'ArrowUp') { logHeight.value = clampLogHeight(logHeight.value + 16); ev.preventDefault() }
  else if (ev.key === 'ArrowDown') { logHeight.value = clampLogHeight(logHeight.value - 16); ev.preventDefault() }
}
```

纵向净增：分隔条 7px + 多出来的一对 8px gap ≈ 15px（.sftp 的 gap: 8px 对每个相邻 flex item 都生效）。这 15px 由可拖高度补偿，不做负 margin 之类的 hack。

**还要补键盘守卫（reviewer 实测）**：分隔条是 `div[role="separator"][tabindex="0"]`，不在 `utils/keys.js` 的 `isEditableTarget` 守卫内 → 焦点在分隔条上按 Delete 会删文件、按 Shift+↑/↓ 会同时扩展选择。修 `keys.js`：

```js
// isEditableTarget：把「焦点在应用自己的控件上」一并纳入守卫
// （分隔条这类 tabindex=0 的 UI 控件不属于文件列表，Delete/Ctrl+A/Shift+↑↓ 都不该作用于文件）
  if (typeof el.closest !== 'function') return false
  if (el.closest('[role="separator"]')) return true
  return !!(el.closest('.ui-overlay') || el.closest('[role="dialog"]') || el.closest('.search-overlay'))
```

并在 `keys.test.js` 补一条：
```js
it('焦点在分隔条上时所有面板动作都不处理', () => {
  const sep = { tagName: 'DIV', isContentEditable: false, closest: (s) => (s === '[role="separator"]' ? {} : null) }
  expect(actionFor({ key: 'Delete', target: sep })).toBe(null)
  expect(actionFor({ ...ev('ArrowDown', { target: sep }), shiftKey: true })).toBe(null)
})
```

并在**两处**既有清理里都补 `onSplitterUp()`：`onDeactivated` 与 `onUnmounted`（SftpView.vue:930-933 一带已有成对摘除 keydown/click/dragend 的先例，SftpView 被 KeepAlive 缓存，只摘一处会残留 window mousemove/mouseup 监听）。

- [ ] **Step 6: LogPanel 文案中文化（**不要**去掉 chips 的 v-if）**

> **前提纠正（reviewer B 实测）**：`chips` 只来自 `props.tunnels`（`LogPanel.vue:21-22`），而 `SftpView.vue:983` **没有传 `:tunnels`**（只有 `ForwardView`/`SyncView` 传）⇒ SFTP 页 chips 恒为空。原计划「去掉 v-if + min-height 防跳」会变成常驻一条空带，因此**保留 `v-if="chips.length"`**：SFTP 页本来就不存在 chips 跳动问题，这条需求作废（设计草图里 SFTP 日志面板画的三个 chip 是示意，实际产不出来——草图相应位置已标注）。

```html
    <!-- chips 保持原有 v-if：SFTP 页不传 tunnels，恒为空，不能去掉 v-if 留空带 -->
    <div v-if="chips.length" class="chips">
      <button :class="['chip', { active: !logStore.filterSource }]" @click="logStore.filterSource = ''">全部</button>
      <button v-for="c in chips" :key="c.id" ...>{{ c.label }}</button>
    </div>
    ...
        placeholder="按来源筛选（规则名/主机）"
```

```css
.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 6px; }  /* 保持原样：不加 min-height，别留空带 */
```

- [ ] **Step 7: 补 LogPanel 不变量测试**

```js
// frontend/src/components/logPanel.test.js
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./LogPanel.vue', import.meta.url), 'utf8')

describe('日志面板', () => {
  it('chips 保留 v-if（SFTP 页不传 tunnels，chips 恒空；去掉 v-if 会留一条空带）', () => {
    expect(src).toMatch(/<div v-if="chips\.length" class="chips"/)
    expect(src).not.toMatch(/\.chips\s*\{[^}]*min-height/)
  })
  it('文案中文化：不再出现英文 filter source_id 占位符', () => {
    expect(src).not.toContain('filter source_id')
    expect(src).toContain('按来源筛选（规则名/主机）')
  })
})
```

- [ ] **Step 8: 跑全套测试 + 构建**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部 PASS

- [ ] **Step 9: 提交**

```bash
git add frontend/src/utils/logpane.js frontend/src/utils/logpane.test.js \
        frontend/src/components/logPanel.test.js \
        frontend/src/views/SftpView.vue frontend/src/components/LogPanel.vue
git commit -m "feat(sftp): 日志区可拖拽调整高度，文案中文化，分隔条纳入键盘守卫"
```


### Task 7: 门禁 + 真机验收（Linux + Windows）+ 沉淀

**Files:**
- Create: `docs/superpowers/reports/2026-09-24-sftp-pane-layout-acceptance.md`
- 无生产代码改动（本任务只验证与记录）

**Interfaces:**
- Consumes: Task 1–6 的全部产出。
- Produces: 验收报告（判据表 + 命令 + 原始输出 + 截图路径），供合并评审。

- [ ] **Step 1: 跑仓库门禁**

Run: `make ci`
Expected: RC=0 —— `go vet` / `go test -race` 全 ok、`vitest run` 全 PASS、`npm run build` 成功
（本轮开工前的基线实测：`MAKE_CI_RC=0`，`Test Files 25 passed / Tests 335 passed`——用作对照）

- [ ] **Step 2: Linux 真机几何验收（Xvfb + XTEST + 像素探针）**

用 `real-machine-testing` skill 的 Linux 通道启动（Xvfb + `WEBKIT_DISABLE_COMPOSITING_MODE=1` + 真实 GTK/WebKit），SFTP 页连到一次性 sshd，截图后跑几何判据。

**先修探针本身（reviewer B 实测的假通过根因）**：ImageMagick Q16 输出 16 位图时 hex 是 **12 位**（`convert -size 4x1 xc:'#22304A' txt:-` → `#222230304A4A`），老式子串截前 6 位会得到 `222230`，与背景色永不相等 ⇒ 每个像素都被当成前景、判据恒 PASS。**必须带 `-depth 8`**（实测加后 → `#22304A`），窗口宽用 `identify` 现取：

```bash
SHOT=shot.png
W=$(identify -format '%w' "$SHOT")
# 主题色值必须传参：Linux 下主题跟随系统深浅色
# 深色 SURF=22304A ELEV=1E293B BORD=33415F BG=1B2636 ／ 浅色 SURF=F6F8FB ELEV=FFFFFF BORD=CBD5E1 BG=EEF2F7
SURF=22304A; ELEV=1E293B; BORD=33415F; BG=1B2636

# 横向探针：打印第 $2 行的「非背景像素」x 区间
hprobe() { convert "$1" -depth 8 -crop "${W}x1+0+$2" +repage txt:- \
  | sed -n '2,$p' | sed 's/^\([0-9]*\),\([0-9]*\):.*#\([0-9A-F]\{6\}\).*/\1 \3/' \
  | awk -v a="$SURF" -v b="$ELEV" -v c="$BORD" -v d="$BG" '$2!=a && $2!=b && $2!=c && $2!=d{print $1}' \
  | awk 'NR==1{s=$1;p=$1;next} $1>p+3{printf "%s-%s ",s,p; s=$1} {p=$1} END{if(NR)printf "%s-%s\n",s,p}'; }

# 竖向探针：打印第 $2 列上「边框色」出现的 y
vprobe() { convert "$1" -depth 8 -crop "1x\$(identify -format '%h' "$1")+$2+0" +repage txt:- \
  | sed -n '2,$p' | sed 's/^\([0-9]*\),\([0-9]*\):.*#\([0-9A-F]\{6\}\).*/\2 \3/' \
  | awk -v c="$BORD" '$2==c{printf "%s ", $1}'; echo; }

# 定位窗格内的水平边框线：第一条 = 面板顶，其后第一条 = 列头行底边框（navrow 40 + hdrow 26 + 1）
echo "borders at x=250:"; vprobe "$SHOT" 250
```

判据与命令（每条都要把数值写进报告；任一 `exit 1` 即失败）：

```bash
# 判据 A：列头「修改时间」右边缘 == 首个列表行时间值右边缘（修 bug 前差 132px）
HD_ROW=$(vprobe "$SHOT" 250 | awk '{print $2+1}')      # 列头行底边框 y（按上一步输出填实际值）
hd=$(hprobe "$SHOT" $((HD_ROW-10)) | awk '{n=split($0,a," "); print a[n]}' | cut -d- -f2)
row=$(hprobe "$SHOT" $((HD_ROW+40)) | awk '{n=split($0,a," "); print a[n]}' | cut -d- -f2)
diff=$((hd>row?hd-row:row-hd)); echo "A: header=$hd row=$row diff=$diff"; test "$diff" -le 2 || exit 1

# 判据 A2（reviewer 必修）：让列表长到出现纵向滚动条后重跑判据 A（滚动条会吃掉 ~15px 内容宽）
#   做法：进入一个条目数 > 可见行数的目录（如 /usr/bin），重截一张，再跑上面的 A

# 判据 B：列头行内不得出现输入框色带（过滤框已移出列头行）
echo "B:"; hprobe "$SHOT" $((HD_ROW-10))   # 只应看到三段列头文字，不应出现整段背景填充

# 判据 C：同一行内 4 个控件等高（路径框/位置/☆/🔍 的上下边框 y 完全一致）
for x in 200 360 396 430; do echo "C x=$x:"; vprobe "$SHOT" "$x"; done

# 判据 D：点三个列头各一次，每次前后截图做像素 diff（不要只靠肉眼）
for y in 名称 大小 修改时间; do
  : # XTEST 点击 (列头文字中心 x, HD_ROW-10)
  # 点击前 shot-before.png → 点击后 shot-after.png
  compare -metric AE shot-before.png shot-after.png null: 2>&1   # 必须 > 0
done
# 判据 D2：再点一次「修改时间」，箭头必须反向（AE 再次 > 0 且列头箭头位置不同）
```

- [ ] **Step 3: Windows 真机验收（必做：用户缺陷只在 Windows 复现）**

**通道（先纠正一个错误假设）**：Wails 在 Windows 上总会给 WebView2 传 `--disable-features=msSmartScreenProtection`，于是 `AdditionalBrowserArguments` 非空、WebView2 **忽略** `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` ⇒ **CDP 不可用**（`real-machine-testing` skill §2.9 #1）。本任务**不要**尝试开 CDP 端口。可用的驱动/取证组合是：

```bash
# 1) 起 GUI 到交互会话（SSH 启动会落 Session 0，WebView2 报 800700aa）
#    schtasks /create ... /it + /run（或 VBoxManage guestcontrol run）
# 2) 交互：宿主侧 XTEST 打 VirtualBox 控制台窗口
#    坐标映射：宿主绝对 = 控制台窗口绝对偏移 + 客户机显示区在窗口内的偏移（本机 title+menu ≈ 52px）
#    先用「开始按钮」这类大目标校准一次，再批量点
xdpyinfo | head -3            # 确认宿主有 X
# 3) 取证：控制台截图（headless 也可用）
VBoxManage controlvm <vm> screenshotpng /tmp/win-NN.png
# 4) 几何/颜色：复用 Step 2 的 hprobe/vprobe（同样必须 -depth 8）
```

逐项复验（每条都要有可判定数值或前后对比图）：

| 判据 | 操作 | 数值口径 |
|---|---|---|
| W1（用户缺陷①） | 宿主 XTEST 点「修改时间」列头 | 前后截图 `compare -metric AE` > 0 且箭头落在「修改时间」；再点一次箭头反向 |
| W2（用户缺陷②） | 四屏各截一张：SFTP（工具条+面板头）、设置对话框、端口转发、文件同步 | 每个控件高度 = 28（字号 1.0 档）；用 `vprobe` 量 `.fbar .filter / .navrow .curpath / .navrow .pos` 的上下边框 y 差 ≤ 1 |
| W2b | 同上四屏 | 同排控件互为等高（例如「连接/刷新/下拉」三者上下边框 y 相同）；侧栏导航按钮高度 ≥ 40px（没被压成 28px） |
| W3 | 点「📍 位置」 | ① 弹层宽 = 220（`hprobe` 量弹层左右边框 x 之差；同时横向文本不得被裁）② **底部 8px 带内不得出现连续 > 100px 的 `#8B8B8B`**（横向滚动条；对照基线 `/tmp/rel072-shots/P3-scrolled-to-last-row.png` 确有该带）③ 「展开全部 N」文字可见、分段默认 4 行（`最近` 类分组超过 4 条时出现「展开全部 N」） |
| W4 | Ctrl+点击 / Shift+点击 / Ctrl+A / Shift+↑↓ / 过滤栏三按钮 | 每次截图后读底栏计数文本，须依次出现 `已选 1 / N 项` → 区间 → `已选 N / N 项` |
| W4b | 点「已选 n ▾」 | 菜单**向上**展开：菜单顶边 y < 底栏顶边 y，且整块在面板矩形内（不被 `overflow:hidden` 裁掉）；能点到「删除」 |
| W5 | 空态队列 | 面板底边框到日志分隔条上边框的距离 ≤ 12px（有任务时应被队列撑到 ≥ 30px）——两态各截一张对比 |
| W6 | 按住分隔条拖 40px | 日志面板高度变化 ∈ [30, 50]px；拖到顶/底后高度仍在 80–420 |
| W7 | 过滤框内用中文输入法打「测试」再回车 | 过滤生效、**不触发路径跳转**；若客户机 IME 不可用，报告里必须写「未验证」，不得默认通过 |
| W8 | 面板头 | 路径框宽度 ≥ 180px（现状仅 ~108px） |

Expected: W1–W8 全通过；任一项失败则回到对应 Task 修复并重跑 Step 1–3

- [ ] **Step 4: 写验收报告**

`docs/superpowers/reports/2026-09-24-sftp-pane-layout-acceptance.md` 必须包含：判据表（编号 + 操作 + 期望 + 实测数值）、每项的命令与原始输出片段、截图路径、未通过项与原因、以及「本次未触碰用户数据」的说明（临时目录、用户 `%APPDATA%\sshore` 与既有 exe 的哈希前后对比）。

- [ ] **Step 5: 提交**

```bash
git add docs/superpowers/reports/2026-09-24-sftp-pane-layout-acceptance.md
git commit -m "docs: 记录 SFTP 面板排版修复的 Linux/Windows 真机验收"
```

## 风险与回滚

| 风险 | 影响面 | 缓解 / 回滚 |
|---|---|---|
| 全局控件高度（Task 3） | 所有页面与浮层 | 单独 commit；`styleTokens.test.js` 钉住 opt-out 列表；Step 6 全页面自检；出问题 `git revert` 单 commit |
| `.list li` 由 flex 改 grid | 行内三列、hover/sel 高亮、拖拽命中 | 仍是同一个 `li` 元素，事件绑定不动；Task 7 判据 A 钉住对齐 |
| 过滤栏占底部 ~30px | 列表少约 1 行 | 已获用户确认；日志区可拖可部分补偿 |
| Shift+↑/↓ 与浏览器滚动冲突 | 列表滚动 | 仅在 `actionFor` 返回扩展动作时 `preventDefault`，且焦点在输入框时不处理 |
| 位置分段 8 → 4 | 常用路径需要多点一次「展开全部」 | 用户已确认（嫌长） |
| 隐藏项语义 | `invert` 与 `all` 行为不同 | 在 `selection.js` 注释写明并单测钉住；UI 用 `hiddenSelected` 如实标注 |
| 批量菜单/位置弹层被面板裁切 | 批量操作入口不可达 | 过滤栏的 `.bmenu` 改 `bottom:100%` 向上弹出并加源码断言；`.posmenu` 已是 `position:fixed`；真机验收需实际点开一次菜单 |
| 真机环境 | Windows VM / Xvfb 不可用 | 至少完成 Linux 几何验收并明确标注「Windows 未验证」，不得默认通过 |

## 两阶段审核与修复记录

计划写完后按 `two-stage-review` skill 做了两轮：先自审并修复，再派 **2 个干净上下文的只读 reviewer**（新会话、不带本轮背景）分别审 Task 1/2/4 与 Task 3/5/6/7，并逐条复现其结论后再改。

### 第一轮：自审（13 处）

| 严重度 | 问题 | 修法 |
|---|---|---|
| Blocking | 批量菜单 `.bmenu{top:100%}` 移到底部过滤栏后会开在面板外、被 `.pane{overflow:hidden}` 裁掉（上一轮位置弹层同类坑） | 过滤栏内改 `bottom:100%; top:auto` + 源码断言 + Windows 判据 W4b |
| Important | 全局 `height` 会把 7 个复选框撑成 28px | 高度规则用 `input:not([type=checkbox]):not([type=radio])` 口径 |
| Important | `positions.test.js:38` 写死 `slice(0, 8)`，常量改 4 后必红 | 要求同步改成与常量同源，并注意不要回改历史验收报告 |
| Important | 漏删 `.cell-size{padding-right:16px}` 会让列表「大小」值偏左 16px、判据 A 假失败 | 列出必须删除/就地修改的 10 条旧规则（含行号与原因） |
| Important | 合并选择器写 grid 会让源码里模板只出现 1 次，与 `toBe(2)` 断言冲突 | `.list li` 就地改 + `.hdrow` 独立规则 |
| Important | Linux 探针写死深色板色值，浅色系统上恒不匹配 | 色值参数化 |
| Important | 用 `npm run dev` 做视觉自检（无 Go 绑定，列表空） | 改为 Xvfb 真实应用逐页截图 |
| Important | Task 2 措辞会让人删掉 `onPaneAction` 的 `select-all/clear`（过滤栏按钮的实现路径） | 明确「只删菜单项、必须保留分支」 |
| Minor ×5 | 日志纵向净增未说明；SftpView 已 `import * as sel` 与两处清理钩子；ContextMenu opt-out 选择器不明确；新增测试是否被 `make ci` 覆盖；既有 CSS 断言是否受影响 | 逐条补进计划并附 file:line |

### 第二轮：干净 reviewer（A=3 Blocking / 2 Important / 5 Minor，B=2 / 6 / 6）

已逐条复现后修复：

| 来源 | 结论 | 处理 |
|---|---|---|
| A-B1 | `positions.test.js:38` 硬编码 8（与第一轮同） | 已在第一轮修；本轮复核确认 |
| A-B2 | `grids.length` 断言与合并选择器矛盾 | 已在第一轮修；复核确认 |
| A-B3 | `expect(bar).toContain('selectionLabel(')` 取不到（模板里是 `{{ selLabel }}`） | 断言拆开：`filePane` 整体含 `selectionLabel(`，`bar` 含 `{{ selLabel }}` |
| A-I2 | **表头在滚动容器外，出现纵向滚动条时列表窄 ~15px，列再次错位** | Task 1 重构：`.hdrow` 移入 `.list-holder`（该容器成为滚动容器）、`.hdrow{position:sticky}`、`.list{flex:none}`；新增 sticky/同容器断言；判据 A2「带滚动条再测一次」 |
| A-M2 | `all()` 未设 cursor/anchor，Ctrl+A 后 Shift+↑↓ 会塌成一两项 | `all()` 设 anchor=首项、cursor=末项 + 2 条单测 |
| A-M3 | `.fwrap .filter{width:100%}` 在 Task 3 之前无 border-box 会溢出 | 改 `flex:1; min-width:0` |
| A-M4 | 用例数 6→7、`FilePane.vue:255`→257 | 已改 |
| A/B-M1 | 全局 `input:not(...)`(0,2,1) 压过 scoped `.curpath`(0,2,0)，路径框会变常显边框 | Task 3 重构：高度规则**只放盒模型属性**，视觉属性留在元素级 `button, input, select` 里；并加断言禁止视觉属性进入前者 |
| B-B1 | ImageMagick Q16 输出 16 位图时 hex 是 **12 位**，老式探针截 6 位 ⇒ 判据恒 PASS（假通过） | 探针全部加 `-depth 8`；窗口宽用 `identify` 现取；判据 A 打印 diff 并 `test -le 2 || exit 1`；判据 B 改为「列头行内无输入框色带」；C 补竖向探针；D 改像素 diff |
| B-B2 | Windows 通道没有鼠标注入，且 **Wails 上 CDP 不可用**（§2.9 #1），原 CDP 判据无法执行 | Task 7 Step 3 重写：宿主 XTEST 打 VirtualBox 控制台窗口（§2.7/§2.9 #6 坐标映射）+ `controlvm screenshotpng` 取证 + 像素探针；明确禁止尝试 CDP |
| B-I1 | opt-out 清单漏 `App.vue .sidebar button`（会被压成 28px 裁字） | 加入清单与断言（现表格 13 项） |
| B-I2 | `styleTokens.test.js` 并未钉住完整 opt-out 清单 | 断言扩到全部 13 处（含 `.more`/`.fx`/`.batch .chip`/`.copy`/ContextMenu/App/SettingsDialog/UpdateSection） |
| B-I3 | KeepAlive 切标签不触发 `onUnmounted` | 明确挂 `onDeactivated` + `onUnmounted` 两处 |
| B-I4 | SFTP 页不传 `tunnels` ⇒ chips 恒空，「去掉 v-if + min-height 防跳」会留空带 | Q8 纠正：**保留 `v-if`**、去掉 min-height 改动；草图中 SFTP 日志面板的 chip 仅为示意，实为产不出来 |
| B-I5 / B-M3 | 分隔条不在键盘守卫内：焦点在它上面按 Delete 会删文件、Shift+↑↓ 会同时 resize + 扩展选择 | `keys.js` 守卫纳入 `[role="separator"]`（+单测）；`onSplitterKey` 遇 `ev.shiftKey` 直接 return |
| B-I6 / B-M4 / B-M5 | W2 覆盖面窄、W3/W5/W6 无数值口径、判据 D 纯肉眼、字号档位未验 | W2 扩到四屏 + W2b/W8；W3/W5/W6 各给数值口径；D 改像素 diff；Step 6 加 `--ui-scale 1.15` 截图 |
| B-M6 | opt-out 还漏 `.fatal-close`/`.dclose`/`.update .link` | 已加入清单（`.copy` 注明现无规则需新增） |

> reviewer 读到的是修复前的版本，故 A-B1/A-B2/A-I1 等条目与第一轮修复重合；凡我未采纳的一律给出理由（如 chips 的 min-height 改为保留 v-if）。

## 自检（写计划时已核对）

1. **决策覆盖**：Q1→Task1 Step7/9、Q2→Task1 Step5/9、Q3→Task1 Step1/7、Q4→Task3、Q5→Task4、Q6→Task4、Q7→Task5、Q8→Task6、Q9→Task1 Step1 + Task6 Step6、Q10→Task2。用户报的两个缺陷分别由 Task1（过滤框离开列头行）+ Task3（控件等高）修掉，Task7 逐条真机复验。
2. **占位符扫描**：无 TBD/TODO；每个改动步骤都给了可直接粘贴的代码或 CSS，每个测试步骤都给了可运行的命令与预期输出。
3. **命名一致性**：`selectionLabel/filterClearVisible/EMPTY_FILTER_HINT`（Task1 定义 → Task1 使用）、`invert/extend/cursor`（Task2 定义 → Task2 使用）、`clampLogHeight/LOG_*`（Task6 定义 → Task6 使用）、`--ctl-h`（Task3 定义 → Task1/3/6 使用）、`POS_MENU_WIDTH`/`POSITION_SECTION_ROWS`（Task4 定义并测试）、`--col-size/--col-time`（Task1 定义 → Task1/3 测试）。无跨任务改名。
4. **测试与实现同批**：每个 Task 都是「先写失败测试 → 跑失败 → 实现 → 跑通过 → 提交」，符合仓库的 `make ci` 门禁。
5. **既有 CSS 断言已核对**：全仓测试里断言样式的只有 `filePaneWiring.test.js:89/95/96`（`.posmenu` 的 `text-align:left` / `position:fixed` / `box-sizing:border-box`，Task 4 全部保留）、`searchOverlay.test.js:43`（`.hits` 的 `max-height`）、`updateSection.test.js:244/245/251/252`（对话框与设置字段）——Task 1/3/4 均不触碰这些选择器，不会误伤既有断言。
6. **测试会被跑到**：`frontend` 没有独立 vitest 配置（只有 `vite.config.js` 与 `npm test = vitest run`），vitest 走默认 glob（`**/*.test.js`），因此新增的 `src/styleTokens.test.js` 与 `src/components/logPanel.test.js` 会被 `make ci` 覆盖。
7. **未纳入**：拖框选择（rubber band）、行内复选框、列宽拖拽调整、传输/日志合并 Tab —— 均经用户明确排除或建议另开一轮。