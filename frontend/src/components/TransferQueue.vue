<script setup>
import { computed } from 'vue'
import { statusClass, summarizeQueue, directionArrow,
  speedOf, etaOf, fmtSpeed, rowNote, rowProgressText, barModel, activeActions, failedActions } from '../utils/queue'

const props = defineProps({
  transfers: { type: Array, default: () => [] },
  now: { type: Number, default: () => Date.now() },
})
const emit = defineEmits(['copy-failures', 'cancel', 'retry', 'resume', 'clean'])

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

// —— Task 14：进度/速度/ETA/动作 ——
// 进度只对"运行中"的行有意义：完成/失败/取消项冻结，不再跟着事件变化。
const live = (t) => t.status === '处理中'

// 进度条模型（是否画 / 是否不定 / 确定宽度）来自 queue.js 纯函数，单测与生产同一条路径：
// 无帧不画假条、total<=0 用不定条，绝不假装 0%/100%（评审 I1）。
function bar(t) { return barModel(t) }

function speedText(t) {
  if (!live(t) || !t.hasProgress) return ''
  return fmtSpeed(speedOf(t, props.now))
}
function etaText(t) {
  if (!live(t) || !t.hasProgress) return ''
  const s = etaOf(t, props.now)
  if (s === null) return ''
  return s < 60 ? '剩余 ' + s + 's' : '剩余 ' + Math.floor(s / 60) + 'm ' + (s % 60) + 's'
}
function acts(t) { return live(t) ? activeActions(t) : failedActions(t) }
function onAct(name, t) { emit(name, t) }
</script>

<template>
  <div class="queue">
    <div v-for="(t, i) in transfers" :key="i" class="t">
      <div class="row">
        <span class="name">{{ line(t) }}</span>
        <span class="meta">
          {{ fmtSize(t.size) }} | {{ fmtElapsed(t) }}
          <template v-if="speedText(t)"> | {{ speedText(t) }}</template>
          <template v-if="etaText(t)"> | {{ etaText(t) }}</template>
        </span>
        <span class="status" :class="statusClass(t.status)">{{ t.status }}</span>
        <span v-if="rowNote(t)" class="note" :class="{ err: t.status === '失败' || t.status === '取消' }" :title="rowNote(t)">{{ rowNote(t) }}</span>
        <span v-if="acts(t).length" class="acts">
          <button v-for="a in acts(t)" :key="a" class="act" :data-act="a" @click="onAct(a, t)">
            {{ a === 'cancel' ? '取消' : a === 'retry' ? '重试' : a === 'resume' ? '续传' : '清理' }}
          </button>
        </span>
      </div>
      <div v-if="bar(t).show" class="pbar">
        <div class="pfill" :class="{ indet: bar(t).indeterminate }" :style="bar(t).width ? { width: bar(t).width } : {}"></div>
      </div>
      <div v-if="rowProgressText(t)" class="ptext">{{ rowProgressText(t) }}</div>
    </div>
    <div v-if="!transfers.length" class="empty">无传输任务</div>
    <div v-if="transfers.length" class="qsum">
      成功 {{ summary.ok }} · 跳过 {{ summary.skipped }} · 失败 {{ summary.failed }} · 取消 {{ summary.cancelled }} · 进行中 {{ summary.running }}
      <button v-if="summary.failed" class="copy" @click="emit('copy-failures')">复制失败清单</button>
    </div>
  </div>
</template>

<style scoped>
.queue { border-top: 1px solid var(--border); max-height: 140px; overflow: auto; font-family: monospace; font-size: var(--fs-12); color: var(--text-dim); }
.t { padding: 3px 8px; }
.row { display: flex; gap: 10px; align-items: center; }
.name { flex: 1; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta { color: var(--text-faint); white-space: nowrap; }
.note { color: var(--text-faint); max-width: 30%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.note.err { color: var(--danger); }
.status.doing { color: var(--accent); }
.status.done { color: var(--success); }
.status.err { color: var(--danger); }
.status.skip { color: var(--text-faint); }
.status.cancel { color: var(--text-faint); }
.acts { display: flex; gap: 6px; }
.act { font-size: var(--fs-11); padding: 0 6px; }
.pbar { height: 3px; background: var(--border); border-radius: 2px; margin-top: 2px; overflow: hidden; }
.pfill { height: 100%; background: var(--accent); transition: width .15s linear; }
/* total<0（未知/降级扫描）：不定条，绝不假装 0% 或 100% */
.pfill.indet { width: 35%; animation: indet 1.2s ease-in-out infinite; }
@keyframes indet { 0% { margin-left: -35%; } 100% { margin-left: 100%; } }
.ptext { color: var(--text-faint); }
.qsum { padding: 3px 8px; color: var(--text-faint); display: flex; gap: 8px; align-items: center; }
.empty { padding: 3px 8px; color: var(--text-faint); }
</style>
