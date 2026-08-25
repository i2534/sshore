<script setup>
defineProps({
  title: String,
  items: { type: Array, default: () => [] },
  selected: String,
})
defineEmits(['select'])
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
      >
        {{ it.isDir ? '📁' : '📄' }} {{ it.name }}
      </li>
    </ul>
  </div>
</template>

<style scoped>
.pane { flex: 1; border: 1px solid #ddd; display: flex; flex-direction: column; }
.head { padding: 6px; background: #f5f5f5; font-weight: 600; }
.list { list-style: none; margin: 0; padding: 0; overflow: auto; flex: 1; }
.list li { padding: 4px 8px; cursor: pointer; font-family: monospace; font-size: 13px; }
.list li.sel { background: #ddf; }
</style>
