<script setup>
import { useLogStore } from '../stores/logs'
const logStore = useLogStore()
</script>

<template>
  <div class="logpanel">
    <div class="logbar">
      <input v-model="logStore.filterSource" placeholder="filter source_id">
      <select v-model="logStore.filterLevel">
        <option value="">全部</option>
        <option value="info">info</option>
        <option value="warn">warn</option>
        <option value="error">error</option>
      </select>
      <button @click="logStore.clear()">清空</button>
    </div>
    <div class="loglines">
      <div v-for="(l, i) in logStore.filtered" :key="i" :class="'level-' + l.level">
        {{ l.ts }} [{{ l.level }}] {{ l.message }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.logpanel { display: flex; flex-direction: column; height: 100%; }
.logbar { display: flex; gap: 8px; margin-bottom: 6px; }
.loglines { flex: 1; overflow: auto; font-family: monospace; font-size: 12px; color: var(--text-dim); }
.level-error { color: var(--danger); }
.level-warn { color: var(--warning); }
</style>
