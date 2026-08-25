<script setup>
import { computed } from 'vue'

const props = defineProps({
  title: String,
  path: String,
  items: { type: Array, default: () => [] },
  selected: String,
  showHidden: { type: Boolean, default: true },
  loading: { type: Boolean, default: false },
})
const emit = defineEmits(['select', 'open', 'context'])

const visible = computed(() => {
  if (props.showHidden) return props.items
  return props.items.filter(it => !it.name.startsWith('.'))
})

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
  <div class="pane">
    <div class="head">
      <span class="title">{{ title }}</span>
      <span class="curpath">{{ path }}</span>
      <span class="count">{{ visible.length }} 项</span>
    </div>
    <div class="list-holder">
      <div class="columns">
        <span class="col-name">名称</span>
        <span class="col-size">大小</span>
        <span class="col-time">修改时间</span>
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
.pane { flex: 1; border: 1px solid var(--border); background: var(--surface); border-radius: 8px; display: flex; flex-direction: column; overflow: hidden; }
.head { padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); display: flex; justify-content: space-between; align-items: center; }
.title { font-weight: 600; }
.curpath { font-weight: 400; font-size: 11px; color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 0 8px; flex: 1; text-align: center; }
.count { font-weight: 400; font-size: 11px; color: var(--text-faint); }
.columns { display: flex; padding: 4px 8px; font-size: 11px; color: var(--text-faint); border-bottom: 1px solid var(--border); }
.list-holder { flex: 1; display: flex; flex-direction: column; position: relative; min-height: 0; }
.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }
.list.busy { opacity: 0.4; pointer-events: none; }
.loading { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--text-faint); font-size: 12px; pointer-events: none; }
.list li { display: flex; align-items: center; padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: 13px; color: var(--text); white-space: nowrap; }
.list li:hover { background: var(--surface-hover); }
.list li.sel { background: var(--surface-hover); color: var(--accent); }
.list li.up { color: var(--text-dim); border-bottom: 1px solid var(--border); }
.col-name, .cell-name { flex: 1; overflow: hidden; text-overflow: ellipsis; }
.col-size, .cell-size { width: 72px; text-align: right; color: var(--text-dim); }
.col-time, .cell-time { width: 120px; text-align: right; color: var(--text-faint); }
</style>
