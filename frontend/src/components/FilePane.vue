<script setup>
import { ref, computed, watch } from 'vue'

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
const emit = defineEmits(['select', 'open', 'context', 'visible', 'focus', 'dragstart', 'dropon', 'clear', 'action', 'pick-position', 'toggle-bookmark', 'search'])
const menuOpen = ref(false)
function pick(name) { menuOpen.value = false; emit('action', name) }

// 位置下拉：选中后立刻清零，避免同一个位置连选两次不触发 change。
function onPick(e) { const v = e.target.value; e.target.value = ''; if (v) emit('pick-position', v) }

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
  <div class="pane ui-panel" :class="{ focus }" tabindex="0" @focus="focus = true; emit('focus')" @blur="focus = false">
    <div class="head">
      <span class="title">{{ title }}</span>
      <span class="curpath">{{ path }}</span>
      <select class="pos" :value="''" @change="onPick($event)">
        <option value="" disabled selected>📍 位置</option>
        <optgroup v-if="presets.length" label="预设">
          <option v-for="p in presets" :key="'p' + p.path" :value="p.path" :title="p.path">{{ p.name }}</option>
        </optgroup>
        <optgroup v-if="disks.length" label="磁盘">
          <option v-for="d in disks" :key="'d' + d.path" :value="d.path" :title="d.path">{{ d.name }}</option>
        </optgroup>
        <optgroup v-if="bookmarks.length" label="书签">
          <option v-for="b in bookmarks" :key="'b' + b.path" :value="b.path">{{ b.name || b.path }}</option>
        </optgroup>
        <optgroup v-if="recents.length" label="最近">
          <option v-for="r in recents" :key="'r' + r.path" :value="r.path">{{ r.path }}</option>
        </optgroup>
      </select>
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
      <ul class="list" :class="{ busy: loading }" @click.self="emit('clear')">
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
</template>

<style scoped>
.pane { flex: 1; display: flex; flex-direction: column; overflow: hidden; }
.pane.focus { border-color: var(--accent); }
.head { padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); display: flex; justify-content: space-between; align-items: center; }
.title { font-weight: 600; }
.curpath { font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 0 8px; flex: 1; text-align: center; }
.count { font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); }
.pos { max-width: 150px; font-size: var(--fs-11); }
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
mark { background: var(--accent); color: var(--on-accent); }
.col-name, .cell-name { flex: 1; overflow: hidden; text-overflow: ellipsis; padding-right: 16px; }
.col-size, .cell-size { width: 88px; text-align: right; padding-right: 16px; color: var(--text-dim); }
.col-time, .cell-time { width: 140px; text-align: right; color: var(--text-faint); }
</style>
