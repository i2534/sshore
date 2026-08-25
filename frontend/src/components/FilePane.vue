<script setup>
defineProps({
  title: String,
  items: { type: Array, default: () => [] },
  selected: String,
})
defineEmits(['select', 'open'])
</script>

<template>
  <div class="pane">
    <div class="head">{{ title }}</div>
    <ul class="list">
      <li
        v-for="it in items"
        :key="it.name"
        :class="{ sel: it.name === selected }"
        @click="$emit('select', it)"
        @dblclick="$emit('open', it)"
      >
        {{ it.isDir ? '📁' : '📄' }} {{ it.name }}
      </li>
    </ul>
  </div>
</template>

<style scoped>
.pane { flex: 1; border: 1px solid var(--border); background: var(--surface); border-radius: 8px; display: flex; flex-direction: column; }
.head { padding: 6px; background: var(--bg-elev); font-weight: 600; color: var(--text-dim); }
.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }
.list li { padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: 13px; color: var(--text); }
.list li:hover { background: var(--surface-hover); }
.list li.sel { background: var(--surface-hover); color: var(--accent); }
</style>
