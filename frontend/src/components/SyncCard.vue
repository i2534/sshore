<script setup>
import { ref, computed } from 'vue'
import { StartSyncRule, StopSyncRule, RetrySyncRuleFailures } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import {
  stateLabel, dotClass, probeBadge, countsOf, alignText, currentFileName,
  deleteSummary, retryable, kindLabel, ruleLabel,
} from '../utils/sync'

const props = defineProps({
  rule: { type: Object, required: true },
  state: { type: String, default: 'stopped' },
  stats: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['edit', 'remove', 'log', 'conflicts', 'confirmDeletes', 'changed'])

const busy = ref(false)
const logStore = useLogStore()

const dot = computed(() => dotClass(props.state))
const badge = computed(() => probeBadge(props.stats))
const counts = computed(() => countsOf(props.stats))
const align = computed(() => alignText(props.stats))
const current = computed(() => currentFileName(props.stats))
const pending = computed(() => deleteSummary(props.stats))
const canRetry = computed(() => retryable(props.stats))
const sourceMissing = computed(() => !!props.stats.SourceMissing)
const conflicts = computed(() => Number(props.stats.Conflicts) || 0)
const logOn = computed(() => logStore.filterSource === props.rule.id)

// 启停失败写入日志面板（同步视图可见）而不是冒泡成全局 fatal，并照常刷新。
// S7：source_id 必须是规则 id —— 卡片的「日志」按钮按规则 id 过滤；写成恒定的
// 'rule' 会让恰好这些错误被过滤掉、永远看不见。
function report(msg) {
  logStore.add({
    source_id: props.rule.id,
    source_type: 'sync',
    level: 'error',
    message: ruleLabel(props.rule) + ': ' + msg,
    ts: new Date().toISOString(),
  })
}

async function toggle() {
  busy.value = true
  try {
    if (props.rule.enabled) await StopSyncRule(props.rule.id)
    else await StartSyncRule(props.rule.id)
  } catch (e) {
    report(String(e))
  } finally {
    emit('changed')
    busy.value = false
  }
}

async function retry() {
  busy.value = true
  try {
    await RetrySyncRuleFailures(props.rule.id)
  } catch (e) {
    report(String(e))
  } finally {
    emit('changed')
    busy.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="head">
      <span :class="['dot', dot]"></span>
      <span class="name">{{ ruleLabel(rule) }}</span>
      <span :class="['badge', badge.level]" :title="badge.title">{{ badge.text }}</span>
      <span v-if="sourceMissing" class="badge err" title="远端根目录不可见，已暂停删除直到对账确认">源缺失</span>
      <span class="meta">{{ kindLabel(rule.kind) }} · {{ rule.host }}:{{ rule.remote_path }} → {{ rule.local_path }}</span>
      <span class="state">{{ stateLabel(state) }}</span>
    </div>

    <div class="counts">
      <span v-for="c in counts" :key="c.key" :class="{ warn: c.key === 'conflicts' && c.value > 0 }">
        {{ c.label }} {{ c.value }}
      </span>
      <span v-if="align" class="align">{{ align }}</span>
      <span v-if="current" class="current" :title="current">· {{ current }}</span>
    </div>

    <div class="actions">
      <button class="ghost" @click="emit('edit', rule)">编辑</button>
      <button class="ghost danger" @click="emit('remove', rule)">删除</button>
      <button class="ghost" :class="{ on: logOn }" @click="emit('log', rule)">日志</button>
      <button v-if="conflicts > 0" class="ghost warn-btn" @click="emit('conflicts', rule)">冲突 {{ conflicts }}</button>
      <button v-if="pending" class="ghost warn-btn"
        @click="emit('confirmDeletes', { rule, fingerprint: pending.fingerprint, count: pending.count })">
        确认删除 {{ pending.count }}
      </button>
      <button v-if="canRetry" class="ghost" :disabled="busy" @click="retry">重试失败</button>
      <button :disabled="busy" @click="toggle">{{ rule.enabled ? '停止' : '启动' }}</button>
    </div>
  </div>
</template>

<style scoped>
.card { border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; margin-bottom: 8px; background: var(--bg-elev); }
.head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.name { font-weight: 600; color: var(--text); }
.meta { color: var(--text-dim); font-size: var(--fs-12); }
.state { margin-left: auto; color: var(--text-dim); font-size: var(--fs-12); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.dot.on { background: var(--success); }
.dot.warn { background: var(--warning); }
.dot.err { background: var(--danger); }
.dot.off { background: var(--text-faint); }
.badge { font-size: var(--fs-12); padding: 1px 6px; border-radius: 10px; border: 1px solid var(--border); color: var(--text-dim); }
.badge.ok { color: var(--text-dim); }
.badge.warn { color: var(--warning); border-color: var(--warning); }
.badge.err { color: var(--danger); border-color: var(--danger); }
.counts { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 6px; color: var(--text-dim); font-size: var(--fs-12); }
.counts .warn { color: var(--warning); }
.counts .align { color: var(--accent); }
.counts .current { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 40ch; }
.actions { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 8px; }
.ghost { background: transparent; }
.ghost.on { color: var(--accent); }
.ghost.danger { color: var(--danger); }
.ghost.warn-btn { color: var(--warning); }
</style>
