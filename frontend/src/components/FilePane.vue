<script setup>
import { ref, computed, watch, onUnmounted } from 'vue'
import { positionGroups } from '../utils/positions'
import { isImeCommit } from '../utils/pathbar'

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
  // P2：面板头的位置下拉/收藏/深搜入口。位置数据由父组件按面板+主机过滤后传入。
  pane: { type: String, default: 'remote' },
  host: { type: String, default: '' },
  bookmarks: { type: Array, default: () => [] },
  recents: { type: Array, default: () => [] },
  bookmarked: { type: Boolean, default: false },
  // 位置下拉里的固定预设与 Windows 盘符，由父组件按面板给出（内容来自 ListPresets）。
  presets: { type: Array, default: () => [] },
  disks: { type: Array, default: () => [] },
})
const emit = defineEmits(['select', 'open', 'context', 'visible', 'focus', 'dragstart', 'dropon', 'clear', 'action', 'pick-position', 'toggle-bookmark', 'search', 'navigate'])
const menuOpen = ref(false)

// ===== 路径栏（可编辑）=====
// draft 是输入框的唯一真源；prop.path 变化（导航完成、失败回滚）时同步回来，
// 这样 Enter 提交后无论成功还是被父层回滚，输入框显示的都与真实目录一致。
const pathDraft = ref(props.path || '')
function commitPath(e) {
  // IME 守卫必须由 keydown 触发（见 pathbar.isImeCommit）：WebKit 在 compositionend 之后
  // 仍会发 keyup 且 isComposing=false，keyup 版守卫在本项目的 Linux/WebKitGTK 目标上等于没有。
  if (isImeCommit(e)) return // 中文输入法里按 Enter 确认候选，不该触发跳转
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

// ===== 位置弹层（📍 位置）=====
// 为什么是 position: fixed + 打开时现算坐标，而不是 absolute 挂在按钮下方：
// .pane 是 overflow: hidden（FilePane.vue:255），absolute 弹层会被面板裁掉——默认 720 高的窗口里，
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
// Esc 清空选中：面板 keydown 先于 SftpView 的 window 处理器冒泡，
// 在这里同步复位「..」高亮（否则 selKeys 恒为 0，watch 的 n→0 守卫不会触发）。
function onPaneKeydown(e) { if (e.key === 'Escape') { upActive.value = false; closePos() } }
onUnmounted(() => window.removeEventListener('resize', closePos))

function onUpClick() {
  upActive.value = true
  emit('clear') // 与「点面板空白处」同义：单击「..」先取消当前选中
}
function openUp() { emit('open', { name: '..', isDir: true }) }
// 清空选中的第二条路径（点列表空白）。此时 selKeys.length 恒为 0，
// watch(() => selKeys.length) 的 n→0 守卫不会触发，必须在这里同步复位「..」高亮，
// 否则「..」行会保留 .sel 而面板头显示「已选 0 项」——用户可能误以为「..」被选中而按 Delete/传输。
function onListBlankClick() { upActive.value = false; emit('clear') }

function pick(name) { menuOpen.value = false; emit('action', name) }

// Sort state: key in 'name' | 'size' | 'modTime'; dir 1 = asc, -1 = desc.
const sortKey = ref('name')
const sortDir = ref(1)
// 面板聚焦态：仅用于视觉提示；父组件凭 focus 事件决定键盘作用于哪个面板。
const focus = ref(false)

function sortBy(key) {
  if (sortKey.value === key) {
    sortDir.value *= -1 // same column: toggle direction
  } else {
    sortKey.value = key
    sortDir.value = 1 // new column: default ascending
  }
}

const visible = computed(() => {
  let list = props.showHidden ? props.items : props.items.filter(it => !it.name.startsWith('.'))
  const key = sortKey.value
  const dir = sortDir.value
  const sorted = [...list].sort((a, b) => {
    let cmp
    if (key === 'size') {
      cmp = (Number(a.size) || 0) - (Number(b.size) || 0)
    } else if (key === 'modTime') {
      cmp = String(a.modTime).localeCompare(String(b.modTime))
    } else {
      cmp = String(a.name).localeCompare(String(b.name), undefined, { numeric: true, sensitivity: 'base' })
    }
    return cmp * dir
  })
  return sorted
})

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

function arrow(key) {
  return sortKey.value === key ? (sortDir.value === 1 ? '▲' : '▼') : ''
}

function fmtSize(bytes) {
  if (bytes == null || bytes === 0 || bytes === '') return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = Number(bytes)
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + units[i]
}
</script>

<template>
  <div class="pane ui-panel" :class="{ focus }" tabindex="0"
    @focus="focus = true; emit('focus')" @blur="focus = false"
    @focusout="onPaneFocusOut" @mousedown="onPaneMouseDown" @keydown="onPaneKeydown">
    <div class="head">
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
              <button v-if="g.total > g.rows.length" class="more" @click="expandGroup(g.key)">展开全部 {{ g.total }}</button>
            </div>
            <button v-for="o in g.rows" :key="o.key" class="positem" :title="o.path" @click="pickPos(o.path)">{{ o.label }}</button>
          </template>
          <div v-if="!groups.length" class="posempty">没有匹配的位置</div>
        </div>
      </div>
      <button class="star" :class="{ on: bookmarked }" :title="bookmarked ? '取消收藏' : '收藏当前目录'" @click="emit('toggle-bookmark')">☆</button>
      <button class="find" title="递归深搜" @click="emit('search')">🔍</button>
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
      <ul class="list" :class="{ busy: loading }" @click.self="onListBlankClick">
        <li class="up" data-role="up" :class="{ sel: upActive }" @click="onUpClick" @dblclick="openUp">
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
</template>

<style scoped>
.pane { flex: 1; display: flex; flex-direction: column; overflow: hidden; }
.pane.focus { border-color: var(--accent); }
.head { padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); display: flex; justify-content: space-between; align-items: center; }
.title { font-weight: 600; }
.curpath { flex: 1; min-width: 0; font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); text-align: left; background: transparent; border: 1px solid transparent; border-radius: 3px; padding: 1px 6px; margin: 0 8px; font-family: inherit; }
.curpath:hover { border-color: var(--border); }
.curpath:focus { border-color: var(--accent); color: var(--text); outline: none; }
.count { font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); }
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
.star.on { color: var(--seed); }
.batch { position: relative; }
.batch .chip { font-size: var(--fs-11); }
.bmenu { position: absolute; right: 0; top: 100%; z-index: 20; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 3px 0; min-width: 160px; }
.bmenu button { display: block; width: 100%; text-align: left; padding: 4px 10px; background: none; border: none; color: var(--text); font-size: var(--fs-12); cursor: pointer; }
.bmenu button:hover:not(:disabled) { background: var(--surface-hover); }
.bmenu button:disabled { color: var(--text-faint); cursor: default; }
.columns { display: flex; padding: 4px 8px; font-size: var(--fs-11); color: var(--text-faint); border-bottom: 1px solid var(--border); }
.sortable { cursor: pointer; user-select: none; }
.sortable:hover { color: var(--text-dim); }
.sortable.active { color: var(--accent); }
.filter { width: 110px; font-size: var(--fs-11); }
.list-holder { flex: 1; display: flex; flex-direction: column; position: relative; min-height: 0; }
.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }
.list.busy { opacity: 0.4; pointer-events: none; }
.loading { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--text-faint); font-size: var(--fs-12); pointer-events: none; }
.list li { display: flex; align-items: center; padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: var(--fs-13); color: var(--text); white-space: nowrap; }
.list li:hover { background: var(--surface-hover); }
.list li.sel { background: var(--surface-hover); color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); }
.list li.anchor { outline: 1px dashed var(--seed); outline-offset: -1px; }
.list li.up { color: var(--text-dim); border-bottom: 1px solid var(--border); }
.list li.up.sel { background: var(--surface-hover); color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); border-bottom-color: var(--accent); }
mark { background: var(--accent); color: var(--on-accent); }
.col-name, .cell-name { flex: 1; overflow: hidden; text-overflow: ellipsis; padding-right: 16px; }
.col-size, .cell-size { width: 88px; text-align: right; padding-right: 16px; color: var(--text-dim); }
.col-time, .cell-time { width: 140px; text-align: right; color: var(--text-faint); }
</style>
