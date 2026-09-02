<script setup>
import { ref, computed } from 'vue'
import { StartTunnel, StopTunnel } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'

const props = defineProps({
  tunnel: { type: Object, required: true },
  state: { type: String, default: 'stopped' },
})
const emit = defineEmits(['edit', 'delete', 'changed'])
const busy = ref(false)
const logStore = useLogStore()

const dotCls = computed(() =>
  props.state === 'connected' ? 'on'
  : props.state === 'reconnecting' ? 'warn'
  : props.state === 'error' ? 'err' : 'off')

function toggleLog() {
  logStore.filterSource = logStore.filterSource === props.tunnel.id ? '' : props.tunnel.id
}

async function toggle() {
  busy.value = true
  try {
    if (props.tunnel.enabled) await StopTunnel(props.tunnel.id)
    else await StartTunnel(props.tunnel.id)
  } catch (e) {
    // 启停失败不再冒泡到全局 fatal 横幅：写入日志面板（转发视图可见），
    // 并照常刷新列表以同步后端真实状态。
    logStore.add({
      source_id: 'rule',
      level: 'error',
      message: `${props.tunnel.name || props.tunnel.host}: ${String(e)}`,
      ts: new Date().toISOString(),
    })
  } finally {
    emit('changed')
    busy.value = false
  }
}
</script>

<template>
  <div class="rule">
    <span :class="['dot', dotCls]"></span>
    <span class="name">{{ tunnel.name || tunnel.host }}</span>
    <span class="meta">{{ tunnel.mode }} {{ tunnel.listen_bind }}:{{ tunnel.listen_port }}<template v-if="props.state === 'reconnecting'"> · 重连中</template></span>
    <div class="actions">
      <button class="ghost" @click="emit('edit', tunnel)">编辑</button>
      <button class="ghost danger" @click="emit('delete', tunnel)">删除</button>
      <button class="ghost" :class="{ on: logStore.filterSource === tunnel.id }" @click="toggleLog">日志</button>
      <button :disabled="busy" @click="toggle">
        {{ tunnel.enabled ? '停止' : '启动' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.rule { display: flex; align-items: center; gap: 8px; padding: 8px 0; border-bottom: 1px solid var(--border); }
.dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
.dot.on { background: var(--success); }
.dot.off { background: var(--text-faint); }
.dot.warn { background: #f0ad4e; }
.dot.err { background: var(--danger); }
.name { font-weight: 600; color: var(--text); white-space: nowrap; }
.meta { color: var(--text-dim); font-size: 12px; flex: 1 1 auto; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.actions { display: flex; gap: 6px; flex-shrink: 0; align-items: center; }
.actions button { white-space: nowrap; }
button.ghost { background: transparent; border-color: var(--border); color: var(--text-dim); }
button.ghost:hover { background: var(--surface-hover); color: var(--text); }
button.ghost.danger:hover { color: var(--danger); border-color: var(--danger); }
button.ghost.on { color: var(--accent); border-color: var(--accent); }
</style>
