<script setup>
import { computed } from 'vue'
import { useLogStore } from '../stores/logs'

const props = defineProps({
  // 可选:隧道规则列表(forward 视图传入)。有值时显示"按规则过滤"chips,
  // 每个规则可单独切换到只看它自己的日志;SFTP 视图不传则仅保留手动过滤。
  tunnels: { type: Array, default: () => [] },
})

const logStore = useLogStore()

const chips = computed(() =>
  props.tunnels.map(t => ({ id: t.id, label: t.name || t.host })).filter(c => c.label))

function toggleChip(id) {
  logStore.filterSource = logStore.filterSource === id ? '' : id
}
</script>

<template>
  <div class="logpanel">
    <div v-if="chips.length" class="chips">
      <button :class="['chip', { active: !logStore.filterSource }]" @click="logStore.filterSource = ''">全部</button>
      <button
        v-for="c in chips" :key="c.id"
        :class="['chip', { active: logStore.filterSource === c.id }]"
        @click="toggleChip(c.id)"
      >{{ c.label }}</button>
    </div>
    <div class="logbar">
      <input v-model="logStore.filterSource" placeholder="filter source_id" />
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
.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 6px; }
.chip {
  font-size: 11px; padding: 2px 8px; border-radius: 10px;
  background: transparent; border: 1px solid var(--border); color: var(--text-dim);
  cursor: pointer;
}
.chip:hover { background: var(--surface-hover); color: var(--text); }
.chip.active { background: var(--accent); border-color: var(--accent); color: #fff; }
.logbar { display: flex; gap: 8px; margin-bottom: 6px; }
.loglines { flex: 1; overflow: auto; font-family: monospace; font-size: 12px; color: var(--text-dim); }
.level-error { color: var(--danger); }
.level-warn { color: var(--warning); }
</style>
