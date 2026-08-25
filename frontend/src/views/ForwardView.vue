<script setup>
import { ref, onMounted } from 'vue'
import { ListTunnels, ImportCommand } from '../../wailsjs/go/main/App'
import RuleCard from '../components/RuleCard.vue'
import LogPanel from '../components/LogPanel.vue'

const tunnels = ref([])
const cmd = ref('')

async function refresh() {
  tunnels.value = await ListTunnels()
}
async function doImport() {
  await ImportCommand(cmd.value)
  cmd.value = ''
  await refresh()
}
onMounted(refresh)
</script>

<template>
  <div class="fwd">
    <div class="panel">
      <div class="import">
        <input v-model="cmd" placeholder="ssh -N -L 5432:127.0.0.1:5432 prod-db">
        <button @click="doImport">导入</button>
      </div>
      <button @click="refresh">刷新</button>
      <RuleCard v-for="t in tunnels" :key="t.id" :tunnel="t" />
      <p v-if="!tunnels.length">暂无规则</p>
    </div>
    <div class="panel"><LogPanel /></div>
  </div>
</template>

<style scoped>
.fwd { display: flex; height: 100%; gap: 12px; }
.panel { flex: 1; border: 1px solid #ddd; padding: 8px; overflow: auto; }
.import { display: flex; gap: 6px; margin-bottom: 10px; }
.import input { flex: 1; }
</style>
