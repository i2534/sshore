<script setup>
import { ref, computed } from 'vue'

const props = defineProps({
  title: String,
  path: String,
  items: { type: Array, default: () => [] },
  selected: String,
  showHidden: { type: Boolean, default: true },
  loading: { type: Boolean, default: false },
})
const emit = defineEmits(['select', 'open', 'context'])

// Sort state: key in 'name' | 'size' | 'modTime'; dir 1 = asc, -1 = desc.
const sortKey = ref('name')
const sortDir = ref(1)

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
  <div class="pane ui-panel">
    <div class="head">
      <span class="title">{{ title }}</span>
      <span class="curpath">{{ path }}</span>
      <span class="count">{{ visible.length }} 项</span>
    </div>
    <div class="list-holder">
      <div class="columns">
        <span class="col-name sortable" :class="{ active: sortKey === 'name' }" @click="sortBy('name')">名称 {{ arrow('name') }}</span>
        <span class="col-size sortable" :class="{ active: sortKey === 'size' }" @click="sortBy('size')">大小 {{ arrow('size') }}</span>
        <span class="col-time sortable" :class="{ active: sortKey === 'modTime' }" @click="sortBy('modTime')">修改时间 {{ arrow('modTime') }}</span>
      </div>
      <ul class="list" :class="{ busy: loading }">
        <li class="up" @click="$emit('open', { name: '..', isDir: true })">
          <span class="cell-name">📁 ..</span>
          <span class="cell-size">—</span>
          <span class="cell-time">—</span>
        </li>
        <li
          v-for="it in visible"
          :key="it.name"
          :class="{ sel: it.name === selected }"
          @click="$emit('select', it)"
          @dblclick="$emit('open', it)"
          @contextmenu.prevent="$emit('context', { item: it, event: $event })"
        >
          <span class="cell-name">{{ it.isDir ? '📁' : '📄' }} {{ it.name }}</span>
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
.head { padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); display: flex; justify-content: space-between; align-items: center; }
.title { font-weight: 600; }
.curpath { font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 0 8px; flex: 1; text-align: center; }
.count { font-weight: 400; font-size: var(--fs-11); color: var(--text-faint); }
.columns { display: flex; padding: 4px 8px; font-size: var(--fs-11); color: var(--text-faint); border-bottom: 1px solid var(--border); }
.sortable { cursor: pointer; user-select: none; }
.sortable:hover { color: var(--text-dim); }
.sortable.active { color: var(--accent); }
.list-holder { flex: 1; display: flex; flex-direction: column; position: relative; min-height: 0; }
.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }
.list.busy { opacity: 0.4; pointer-events: none; }
.loading { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--text-faint); font-size: var(--fs-12); pointer-events: none; }
.list li { display: flex; align-items: center; padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: var(--fs-13); color: var(--text); white-space: nowrap; }
.list li:hover { background: var(--surface-hover); }
.list li.sel { background: var(--surface-hover); color: var(--accent); }
.list li.up { color: var(--text-dim); border-bottom: 1px solid var(--border); }
.col-name, .cell-name { flex: 1; overflow: hidden; text-overflow: ellipsis; padding-right: 16px; }
.col-size, .cell-size { width: 88px; text-align: right; padding-right: 16px; color: var(--text-dim); }
.col-time, .cell-time { width: 140px; text-align: right; color: var(--text-faint); }
</style>
