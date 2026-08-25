<script setup>
import { ref } from 'vue'
import { StartTunnel, StopTunnel } from '../../wailsjs/go/main/App'

const props = defineProps({ tunnel: { type: Object, required: true } })
const busy = ref(false)

async function toggle() {
  busy.value = true
  try {
    if (props.tunnel.enabled) await StopTunnel(props.tunnel.id)
    else await StartTunnel(props.tunnel.id)
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
    <button :disabled="busy" @click="toggle">
      {{ tunnel.enabled ? '停止' : '启动' }}
    </button>
  </div>
</template>

<style scoped>
.rule { display: flex; align-items: center; gap: 8px; padding: 6px 0; border-bottom: 1px solid #f0f0f0; }
.dot { width: 10px; height: 10px; border-radius: 50%; }
.dot.on { background: #2a2; }
.dot.off { background: #aaa; }
.name { font-weight: 600; }
.meta { color: #888; font-size: 12px; flex: 1; }
</style>
