<script setup>
const props = defineProps({
  transfers: { type: Array, default: () => [] },
  now: { type: Number, default: () => Date.now() },
})

function fmtSize(bytes) {
  if (!bytes) return '--'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + units[i]
}

function fmtElapsed(startedAt) {
  if (!startedAt) return ''
  const s = Math.max(0, Math.floor((props.now - startedAt) / 1000))
  if (s < 60) return s + 's'
  const m = Math.floor(s / 60)
  return m + 'm ' + (s % 60) + 's'
}

function statusClass(status) {
  if (status === '完成') return 'done'
  if (status === '失败') return 'err'
  return 'doing'
}
</script>

<template>
  <div class="queue">
    <div v-for="(t, i) in transfers" :key="i" class="t">
      <span class="name">{{ t.name }}</span>
      <span class="meta">{{ fmtSize(t.size) }} | {{ fmtElapsed(t.startedAt) }}</span>
      <span class="status" :class="statusClass(t.status)">{{ t.status }}</span>
    </div>
    <div v-if="!transfers.length" class="empty">无传输任务</div>
  </div>
</template>

<style scoped>
.queue { border-top: 1px solid var(--border); max-height: 140px; overflow: auto; font-family: monospace; font-size: 12px; color: var(--text-dim); }
.t { display: flex; gap: 10px; padding: 3px 8px; align-items: center; }
.name { flex: 1; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta { color: var(--text-faint); white-space: nowrap; }
.status.doing { color: var(--accent); }
.status.done { color: var(--success); }
.status.err { color: var(--danger); }
.empty { padding: 3px 8px; color: var(--text-faint); }
</style>
