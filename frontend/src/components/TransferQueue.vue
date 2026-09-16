<script setup>
import { computed } from 'vue'
import { statusClass, summarizeQueue, directionArrow } from '../utils/queue'

const props = defineProps({
  transfers: { type: Array, default: () => [] },
  now: { type: Number, default: () => Date.now() },
})
const emit = defineEmits(['copy-failures'])

const summary = computed(() => summarizeQueue(props.transfers))

function line(t) {
  // 兼容历史记录（无 direction/src/dst）：只显示文件名，保持既有渲染不变。
  if (!t.direction && !t.src && !t.dst) return t.name
  return directionArrow(t.direction) + ' ' + (t.src || t.name) + ' → ' + (t.dst || '')
}

function fmtSize(bytes) {
  if (!bytes) return '--'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + units[i]
}

function fmtElapsed(t) {
  // Finished/failed items freeze their duration (t.elapsed); only in-progress
  // items tick with the live clock.
  const s = t.status === '处理中'
    ? Math.max(0, Math.floor((props.now - t.startedAt) / 1000))
    : (t.elapsed || 0)
  if (s < 60) return s + 's'
  const m = Math.floor(s / 60)
  return m + 'm ' + (s % 60) + 's'
}
</script>

<template>
  <div class="queue">
    <div v-for="(t, i) in transfers" :key="i" class="t">
      <span class="name">{{ line(t) }}</span>
      <span class="meta">{{ fmtSize(t.size) }} | {{ fmtElapsed(t) }}</span>
      <span class="status" :class="statusClass(t.status)">{{ t.status }}</span>
    </div>
    <div v-if="!transfers.length" class="empty">无传输任务</div>
    <div v-if="transfers.length" class="qsum">
      成功 {{ summary.ok }} · 跳过 {{ summary.skipped }} · 失败 {{ summary.failed }} · 进行中 {{ summary.running }}
      <button v-if="summary.failed" class="copy" @click="emit('copy-failures')">复制失败清单</button>
    </div>
  </div>
</template>

<style scoped>
.queue { border-top: 1px solid var(--border); max-height: 140px; overflow: auto; font-family: monospace; font-size: var(--fs-12); color: var(--text-dim); }
.t { display: flex; gap: 10px; padding: 3px 8px; align-items: center; }
.name { flex: 1; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta { color: var(--text-faint); white-space: nowrap; }
.status.doing { color: var(--accent); }
.status.done { color: var(--success); }
.status.err { color: var(--danger); }
.status.skip { color: var(--text-faint); }
.qsum { padding: 3px 8px; color: var(--text-faint); display: flex; gap: 8px; align-items: center; }
.empty { padding: 3px 8px; color: var(--text-faint); }
</style>
