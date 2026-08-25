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
</script>

<template>
  <div class="pane">
    <div class="head">
      <span class="title">{{ title }}</span>
      <span class="curpath">{{ path }}</span>
      <span class="count">{{ loading ? '…' : visible.length + ' 项' }}</span>
    </div>
    <ul class="list">
      <li class="up" @click="$emit('open', { name: '..', isDir: true })">📁 ..</li>
      <li
        v-for="it in visible"
        :key="it.name"
        :class="{ sel: it.name === selected }"
        @click="$emit('select', it)"
        @dblclick="$emit('open', it)"
        @contextmenu.prevent="$emit('context', { item: it, event: $event })"
      >
        {{ it.isDir ? '📁' : '📄' }} {{ it.name }}
      </li>
    </ul>
  </div>
</template>

<style scoped>
.pane { flex: 1; border: 1px solid var(--border); background: var(--surface); border-radius: 8px; display: flex; flex-direction: column; overflow: hidden; }
.head { padding: 6px 8px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); display: flex; justify-content: space-between; align-items: center; }
.title { font-weight: 600; }
.curpath { font-weight: 400; font-size: 11px; color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 0 8px; flex: 1; text-align: center; }
.count { font-weight: 400; font-size: 11px; color: var(--text-faint); }
.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }
.list li { padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: 13px; color: var(--text); white-space: nowrap; }
.list li:hover { background: var(--surface-hover); }
.list li.sel { background: var(--surface-hover); color: var(--accent); }
.list li.up { color: var(--text-dim); border-bottom: 1px solid var(--border); }
</style>
