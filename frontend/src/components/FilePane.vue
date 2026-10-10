<script setup>
import { ref, computed, watch, onUnmounted } from 'vue'
import { positionGroups } from '../utils/positions'
import { isImeCommit } from '../utils/pathbar'
import { selectionLabel, filterClearVisible, EMPTY_FILTER_HINT } from '../utils/panefooter'
import { nextSort } from '../utils/sort'

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
  // 排序状态已上收（规格 §7.3）：本组件不再持有 sortKey/sortDir，只按 props 渲染、
  // 点列头时用 utils/sort.nextSort 算好「下一个方向」emit 出去，由父组件（T11 起是 sftpui store）持久化。
  // 默认值是硬要求：父层接线前不传这两个 prop，缺省必须能正常渲染。
  sortKey: { type: String, default: 'name' },
  sortDir: { type: Number, default: 1 },
})
const emit = defineEmits(['select', 'open', 'context', 'visible', 'focus', 'dragstart', 'dropon', 'clear', 'action', 'pick-position', 'toggle-bookmark', 'search', 'navigate', 'sort'])
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
// 选择清空后批量菜单必须一起收起：`v-if="selKeys.length"` 会把 .batch 整个隐藏，
// 但 menuOpen 若还留着 true，用户重新选中时菜单会「未点先弹」（M1）。
watch(() => props.selKeys.length, (n) => { if (!n) menuOpen.value = false })

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
const POS_MENU_WIDTH = 220
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
// 展开/收起是同一颗按钮：控件始终存在（见 :199 的 v-if="g.expandable"），
// 避免"展开后按钮消失 → 焦点节点被摘下 → 弹层 focusout 收起"这一 Windows/WebView2 缺陷（W1）。
function toggleGroup(key) { posExpanded.value = { ...posExpanded.value, [key]: !posExpanded.value[key] } }
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

// ===== T10 右键触发面（spec §5.2）=====
// 空白 / 列头(.hdrow) / 过滤栏(.fbar) 背景一律算「面板级」⇒ emit { item: null }；
// 文件行与 '..' 行由行自身的 @contextmenu.prevent 负责。三条刻意选择（别顺手改）：
// ① 面板根**不带 .prevent**：路径栏(.curpath) / 过滤框(.filter) 的原生右键复制粘贴必须留着，
//    所以由本 handler 逐分支决定是否 preventDefault()，而不是让修饰符无条件吞掉。
// ② 对任何 li **无条件 yield**：行处理器的 @contextmenu.prevent 只 preventDefault、**不**
//    stopPropagation ⇒ 事件照样冒泡到面板根。若不 yield，'..' 行会「行处理器 + 面板处理器」
//    各 emit 一次 = 双菜单（自审修正 1）。
// ③ 已知边界（审核 FF19）：loading 期间 .list.busy{pointer-events:none} 让行上的右键落到
//    .list-holder/.pane ⇒ 命中面板级菜单而不是行级。loading 时行本来就不可交互，本轮不另修。
function onPaneContext(e) {
  const t = e.target
  // 1) 可编辑控件放行原生菜单（保住路径栏/过滤框的复制粘贴）。
  //    contenteditable="false" 必须排除（审核 FF19：否则它也会被当成可编辑）。
  //    以后新增可编辑控件（如行内重命名输入框）要补进这个选择器，否则会被面板根劫持。
  //    ⚠️ 但**仅**豁免这个选择器不足以让行内可编辑控件（如重命名输入框）拿回原生右键菜单：
  //    这类控件位于文件行 <li> 内，li 自己的 @contextmenu.prevent 处理器会先 preventDefault()
  //    并把行级菜单 emit 出去 ⇒ 还必须同时豁免它所在那个 li 的处理器（本轮只记注释，未实现）。
  if (t.closest && t.closest('input, textarea, [contenteditable]:not([contenteditable="false"])')) return
  // 2) 位置弹层（.pos-wrap）内右键：先收起弹层，且不弹我们的菜单
  //    （审核 FF7：否则 .posmenu 与 ContextMenu 两个 z-index:1000 浮层同屏压在一起）
  if (t.closest && t.closest('.pos-wrap')) { closePos(); e.preventDefault(); return }
  // 3) 任何 li（含 '..' 行）都由行自己的 @contextmenu.prevent 处理器负责 —— 必须无条件 yield（见 ②）
  if (t.closest && t.closest('li')) return
  e.preventDefault()
  emit('context', { item: null, event: e })
}

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

// Sort state（上收为 props）: key in 'name' | 'size' | 'modTime'; dir 1 = asc, -1 = desc.
// 面板聚焦态：仅用于视觉提示；父组件凭 focus 事件决定键盘作用于哪个面板。
const focus = ref(false)

// 只负责算「下一个方向」并上抛：同列取反、换列归升序（语义与旧内部 ref 版逐字一致），
// 实现唯一来源是 utils/sort.nextSort（T6），这里绝不复制一份取反逻辑。
function sortBy(key) {
  emit('sort', nextSort({ key: props.sortKey, dir: props.sortDir }, key))
}

const visible = computed(() => {
  let list = props.showHidden ? props.items : props.items.filter(it => !it.name.startsWith('.'))
  const key = props.sortKey
  const dir = props.sortDir
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

// 底部过滤栏的计数文案（唯一来源：utils/panefooter.js）
const selLabel = computed(() => selectionLabel({
  selected: props.selKeys.length,
  total: shown.value.length,
  hidden: props.hiddenSelected,
}))
function hit(text) {
  const q = filterText.value.trim().toLowerCase()
  const i = q ? text.toLowerCase().indexOf(q) : -1
  if (i < 0) return null
  return { pre: text.slice(0, i), mid: text.slice(i, i + q.length), post: text.slice(i + q.length) }
}

// 列头箭头读源必须是 props（审核 FF1）：它与 visible() 同时不持有状态，
// 只要漏改一处就是渲染期 ReferenceError、整块面板白屏。
function arrow(key) {
  return props.sortKey === key ? (props.sortDir === 1 ? '▲' : '▼') : ''
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
    @focusout="onPaneFocusOut" @mousedown="onPaneMouseDown" @keydown="onPaneKeydown"
    @contextmenu="onPaneContext">
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
    <div class="list-holder">
      <!-- ② 列头行：只放三个列头；grid 轨道与 .list li 完全相同（宽度只来自 --col-size/--col-time） -->
      <div class="hdrow" data-role="columns">
        <span class="col-name sortable" :class="{ active: sortKey === 'name' }" @click="sortBy('name')">名称 {{ arrow('name') }}</span>
        <span class="col-size sortable" :class="{ active: sortKey === 'size' }" @click="sortBy('size')">大小 {{ arrow('size') }}</span>
        <span class="col-time sortable" :class="{ active: sortKey === 'modTime' }" @click="sortBy('modTime')">修改时间 {{ arrow('modTime') }}</span>
      </div>
      <ul class="list" :class="{ busy: loading }" @click.self="onListBlankClick">
        <li class="up" data-role="up" :class="{ sel: upActive }" @click="onUpClick" @dblclick="openUp"
          @contextmenu.prevent="emit('context', { item: null, event: $event })">
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
    <!-- ③ 底部过滤栏：过滤 + 计数/已选（批量菜单只在有选中时出现） -->
    <div class="fbar" data-role="filterbar">
      <span class="fwrap">
        <input class="filter" data-role="filter" v-model="filterText" :placeholder="EMPTY_FILTER_HINT" />
        <button v-if="filterClearVisible(filterText)" class="fx" data-role="filter-clear" title="清空过滤" @click="filterText = ''">✕</button>
      </span>
      <!-- 可见的多选入口：Ctrl/Shift 点击之外的低门槛入口（原先只有批量菜单里的全选/清空） -->
      <button class="mini" data-role="select-all" :disabled="!shown.length" title="全选当前可见项" @click="emit('action', 'select-all')">全选</button>
      <button class="mini" data-role="invert" :disabled="!shown.length" title="反选当前可见项" @click="emit('action', 'invert')">反选</button>
      <button class="mini" data-role="clear" :disabled="!selKeys.length" title="清空选择" @click="emit('action', 'clear')">清空</button>
      <span class="selinfo" data-role="selinfo">{{ selLabel }}</span>
      <div v-if="selKeys.length" class="batch" @click.stop>
        <button class="chip" @click="menuOpen = !menuOpen">已选 {{ selKeys.length }} ▾</button>
        <div v-if="menuOpen" class="bmenu">
          <button v-for="a in actions" :key="a.name" :disabled="a.disabled" @click="pick(a.name)">{{ a.label }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 列宽唯一来源：列头行与列表行都引用这两个变量 */
.pane { --col-size: 88px; --col-time: 140px; flex: 1; display: flex; flex-direction: column; overflow: hidden; }
.pane.focus { border-color: var(--accent); }
.navrow { display: flex; align-items: center; gap: 6px; padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); }
.title { font-weight: 600; }
.curpath { flex: 1; min-width: 0; font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); text-align: left; background: transparent; border: 1px solid transparent; border-radius: 3px; padding: 1px 6px; margin: 0 8px; font-family: inherit; }
.curpath:hover { border-color: var(--border); }
.curpath:focus { border-color: var(--accent); color: var(--text); outline: none; }
.pos { max-width: 150px; font-size: var(--fs-11); }
.pos.on { border-color: var(--accent); color: var(--accent); }
/* position: fixed + z-index 1000：与 ContextMenu.vue 的既有做法一致；对话框浮层是 2000，菜单在其之下。
   宽度定长 POS_MENU_WIDTH（220px）是为了和 "r.right - POS_MENU_WIDTH" 的锚点算法对齐。 */
.posmenu { position: fixed; z-index: 1000; box-sizing: border-box; max-height: 60vh; overflow: auto; text-align: left; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 4px; box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4); }
.posfilter { width: 100%; box-sizing: border-box; font-size: var(--fs-11); margin-bottom: 4px; }
.poslabel { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 4px 6px 2px; font-size: var(--fs-11); color: var(--text-faint); }
.poslabel .more { font-size: var(--fs-11); height: auto; }
.positem { box-sizing: border-box; display: block; width: 100%; height: auto; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; text-align: left; padding: 4px 8px; background: none; border: none; color: var(--text); font-size: var(--fs-12); cursor: pointer; }
.positem:hover { background: var(--surface-hover); }
.posempty { padding: 6px 8px; font-size: var(--fs-12); color: var(--text-faint); }
.star.on { color: var(--seed); }
.batch { position: relative; }
.batch .chip { font-size: var(--fs-11); height: auto; }
.bmenu { position: absolute; right: 0; top: 100%; z-index: 20; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 3px 0; min-width: 160px; }
.bmenu button { display: block; width: 100%; height: auto; text-align: left; padding: 4px 10px; background: none; border: none; color: var(--text); font-size: var(--fs-12); cursor: pointer; }
.bmenu button:hover:not(:disabled) { background: var(--surface-hover); }
.bmenu button:disabled { color: var(--text-faint); cursor: default; }
/* 表头与列表在同一个滚动容器里：表头 sticky，两者内容盒宽度必然一致。
   若把表头放在滚动容器外，列表出现纵向滚动条时内容会窄 ~15px，表头与行会再次错位。 */
.list-holder { flex: 1; display: flex; flex-direction: column; position: relative; min-height: 0; overflow: auto; }
.hdrow { display: grid; grid-template-columns: minmax(0,1fr) var(--col-size) var(--col-time); column-gap: 6px; align-items: center; position: sticky; top: 0; z-index: 5; padding: 4px 8px; font-size: var(--fs-11); color: var(--text-faint); background: var(--bg-elev); border-bottom: 1px solid var(--border); white-space: nowrap; }
.hdrow .col-size, .hdrow .col-time { text-align: right; }
.list { list-style: none; margin: 0; padding: 0; flex: none; }
.list.busy { opacity: 0.4; pointer-events: none; }
.loading { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--text-faint); font-size: var(--fs-12); pointer-events: none; }
.list li { display: grid; grid-template-columns: minmax(0,1fr) var(--col-size) var(--col-time); column-gap: 6px; align-items: center; padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: var(--fs-13); color: var(--text); white-space: nowrap; }
.list li:hover { background: var(--surface-hover); }
.list li.sel { background: var(--surface-hover); color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); }
.list li.anchor { outline: 1px dashed var(--seed); outline-offset: -1px; }
.list li.up { color: var(--text-dim); border-bottom: 1px solid var(--border); }
.list li.up.sel { background: var(--surface-hover); color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); border-bottom-color: var(--accent); }
mark { background: var(--accent); color: var(--on-accent); }
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
/* 过滤栏内的迷你按钮：height:auto 是必须的——全局 button 变高时若不 opt-out 会把过滤栏撑高 */
.mini { height: auto; font-size: var(--fs-11); padding: 1px 6px; }
/* 批量菜单向上弹出：过滤栏是面板最后一个子元素，top:100% 会开在面板外被 overflow:hidden 裁掉 */
.fbar .batch { position: relative; }
.fbar .bmenu { position: absolute; right: 0; bottom: 100%; top: auto; z-index: 20; }
</style>
