<script setup>
import { ref, computed, watch, nextTick } from 'vue'
import { useLogStore } from '../stores/logs'
import { fmtLogTime } from '../utils/time'

const props = defineProps({
  // 可选:隧道规则列表(forward 视图传入)。有值时显示"按规则过滤"chips,
  // 每个规则可单独切换到只看它自己的日志;SFTP 视图不传则仅保留手动过滤。
  tunnels: { type: Array, default: () => [] },
  // 可选:只显示这些 source_type 的事件(如 ['tunnel'] / ['sftp']);
  // 空数组 = 不过滤(全部类型)。无 source_type 的事件(视图本地错误)
  // 始终显示。
  sourceTypes: { type: Array, default: () => [] },
})

const logStore = useLogStore()
const linesEl = ref(null)
// 用户是否停留在底部附近:是→新日志自动滚动;向上翻阅历史时不打扰。
const stick = ref(true)

const chips = computed(() =>
  props.tunnels.map(t => ({ id: t.id, label: t.name || t.host })).filter(c => c.label))

// filterSource 命中的若是某条规则,输入框显示规则名而非 32 位 id。
const displaySource = computed(() => {
  const s = logStore.filterSource
  if (!s) return ''
  const t = props.tunnels.find(t => t.id === s)
  return t ? (t.name || t.host) : s
})

// 视图内可见日志:按 source_type 过滤(转发面板看不到 SFTP 日志,反之亦然)。
const visibleLogs = computed(() => {
  const out = logStore.filtered
  if (!props.sourceTypes.length) return out
  return out.filter(l => !l.source_type || props.sourceTypes.includes(l.source_type))
})

function toggleChip(id) {
  logStore.filterSource = logStore.filterSource === id ? '' : id
}

function onScroll() {
  const el = linesEl.value
  if (!el) return
  stick.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

function scrollToBottom() {
  const el = linesEl.value
  if (el) el.scrollTop = el.scrollHeight
}

// 日志数量变化(可见日志)时自动滚到底部(仅当用户停留在底部)。
watch(() => visibleLogs.value.length, async () => {
  if (!stick.value) return
  await nextTick()
  scrollToBottom()
})

// 切换过滤条件后强制回到底部。
watch(() => [logStore.filterSource, logStore.filterLevel], async () => {
  stick.value = true
  await nextTick()
  scrollToBottom()
})
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
      <input
        :value="displaySource"
        @input="logStore.filterSource = $event.target.value"
        placeholder="filter source_id (规则名/主机)"
      />
      <select v-model="logStore.filterLevel">
        <option value="">全部</option>
        <option value="info">INFO</option>
        <option value="warn">WARN</option>
        <option value="error">ERROR</option>
      </select>
      <button @click="logStore.clear()">清空</button>
    </div>
    <div ref="linesEl" class="loglines" @scroll="onScroll">
      <div v-for="(l, i) in visibleLogs" :key="i" :class="'level-' + l.level">
        {{ fmtLogTime(l.ts) }} [{{ l.level.toUpperCase() }}] {{ l.message }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.logpanel { display: flex; flex-direction: column; height: 100%; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 6px; }
.chip {
  font-size: var(--fs-12); padding: 3px 8px; border-radius: 10px;
  background: transparent; border: 1px solid var(--border); color: var(--text-dim);
  cursor: pointer;
}
.chip:hover { background: var(--surface-hover); color: var(--text); }
.chip.active { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
.logbar { display: flex; gap: 8px; margin-bottom: 6px; }
/* header(过滤栏)控件与日志行统一字号，避免反差过大 */
.logbar input, .logbar select, .logbar button { font-size: var(--fs-12); }
.logbar input, .logbar button { padding: 4px 8px; }
.logbar select { padding: 4px 24px 4px 8px; } /* 右侧给自定义下拉箭头留位 */
.loglines { flex: 1; overflow: auto; font-family: monospace; font-size: var(--fs-12); color: var(--text-dim); }
.level-error { color: var(--danger); }
.level-warn { color: var(--warning); }
</style>
