<script setup>
import { ref } from 'vue'
import { StartTunnel, StopTunnel } from '../../wailsjs/go/main/App'

const props = defineProps({ tunnel: { type: Object, required: true } })
const emit = defineEmits(['edit', 'delete', 'changed'])
const busy = ref(false)

async function toggle() {
  busy.value = true
  try {
    if (props.tunnel.enabled) await StopTunnel(props.tunnel.id)
    else await StartTunnel(props.tunnel.id)
    emit('changed')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="rule">
    <span :class="['dot', tunnel.enabled ? 'on' : 'off']"></span>
    <span class="name">{{ tunnel.name || tunnel.host }}</span>
    <span class="meta">{{ tunnel.mode }} {{ tunnel.listen_bind }}:{{ tunnel.listen_port }}</span>
    <div class="actions">
      <button class="ghost" @click="emit('edit', tunnel)">编辑</button>
      <button class="ghost danger" @click="emit('delete', tunnel)">删除</button>
      <button :disabled="busy" @click="toggle">
        {{ tunnel.enabled ? '停止' : '启动' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.rule { display: flex; align-items: center; gap: 8px; padding: 8px 0; border-bottom: 1px solid var(--border); }
.dot { width: 10px; height: 10px; border-radius: 50%; }
.dot.on { background: var(--success); }
.dot.off { background: var(--text-faint); }
.name { font-weight: 600; color: var(--text); }
.meta { color: var(--text-dim); font-size: 12px; flex: 1; }
.actions { display: flex; gap: 6px; }
button.ghost { background: transparent; border-color: var(--border); color: var(--text-dim); }
button.ghost:hover { background: var(--surface-hover); color: var(--text); }
button.ghost.danger:hover { color: var(--danger); border-color: var(--danger); }
</style>
