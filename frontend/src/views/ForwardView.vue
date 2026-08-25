<script setup>
import { ref, onMounted } from 'vue'
import { ListTunnels, ListHosts, CreateTunnel, UpdateTunnel, DeleteTunnel, ImportCommand } from '../../wailsjs/go/main/App'
import RuleCard from '../components/RuleCard.vue'
import LogPanel from '../components/LogPanel.vue'

const tunnels = ref([])
const hosts = ref([])
const cmd = ref('')
const loadError = ref('')

const showForm = ref(false)
const editingId = ref(null)
const form = ref(newTunnel())

function newTunnel() {
  return {
    name: '',
    mode: 'local',
    host: '',
    listen_bind: '127.0.0.1',
    listen_port: 5432,
    target_host: '',
    target_port: 5432,
    proxy_jump: '',
    auto_reconnect: true,
  }
}

async function refresh() {
  try { tunnels.value = await ListTunnels() } catch (e) { loadError.value = String(e) }
}
async function loadHosts() {
  try { hosts.value = await ListHosts() } catch (e) { loadError.value = String(e) }
}
async function doImport() {
  await ImportCommand(cmd.value)
  cmd.value = ''
  await refresh()
}
function openCreate() {
  editingId.value = null
  form.value = newTunnel()
  showForm.value = true
}
function openEdit(t) {
  editingId.value = t.id
  form.value = { ...t }
  showForm.value = true
}
async function submit() {
  if (!form.value.host) return
  if (editingId.value) await UpdateTunnel(form.value)
  else await CreateTunnel(form.value)
  showForm.value = false
  editingId.value = null
  form.value = newTunnel()
  await refresh()
}
async function remove(t) {
  if (!window.confirm(`删除规则「${t.name || t.host}」？`)) return
  await DeleteTunnel(t.id)
  await refresh()
}
onMounted(async () => { await loadHosts(); await refresh() })
</script>

<template>
  <div class="fwd">
    <div class="panel">
      <div class="toolbar">
        <button @click="openCreate">+ 新建规则</button>
        <button @click="refresh">刷新</button>
      </div>

      <form v-if="showForm" class="form" @submit.prevent="submit">
        <div class="row">
          <label>名称 <input v-model="form.name" placeholder="e.g. prod-db" /></label>
          <label>模式
            <select v-model="form.mode">
              <option value="local">local (-L)</option>
              <option value="remote">remote (-R)</option>
              <option value="dynamic">dynamic SOCKS (-D)</option>
            </select>
          </label>
          <label>主机
            <select v-model="form.host" required>
              <option value="" disabled>选择主机</option>
              <option v-if="form.host && !hosts.includes(form.host)" :value="form.host">{{ form.host }}</option>
              <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
            </select>
          </label>
        </div>
        <div class="row">
          <label>监听地址 <input v-model="form.listen_bind" /></label>
          <label>监听端口 <input v-model.number="form.listen_port" type="number" /></label>
          <template v-if="form.mode !== 'dynamic'">
            <label>目标主机 <input v-model="form.target_host" placeholder="127.0.0.1" /></label>
            <label>目标端口 <input v-model.number="form.target_port" type="number" /></label>
          </template>
        </div>
        <div class="row">
          <label>跳板机 <input v-model="form.proxy_jump" placeholder="optional (ssh alias)" /></label>
          <button type="submit">{{ editingId ? '保存' : '创建' }}</button>
          <button type="button" @click="showForm = false">取消</button>
        </div>
      </form>

      <RuleCard v-for="t in tunnels" :key="t.id" :tunnel="t"
        @edit="openEdit" @delete="remove" @changed="refresh" />
      <p v-if="loadError" class="empty err">加载失败: {{ loadError }}</p>
      <p v-else-if="!tunnels.length" class="empty">暂无规则，点「+ 新建规则」或导入 ssh 命令</p>

      <div class="import">
        <input v-model="cmd" placeholder="ssh -N -L 5432:127.0.0.1:5432 prod-db">
        <button @click="doImport">导入</button>
      </div>
    </div>
    <div class="panel"><LogPanel /></div>
  </div>
</template>

<style scoped>
.fwd { display: flex; height: 100%; gap: 12px; }
.panel { flex: 1; border: 1px solid var(--border); background: var(--surface); border-radius: 8px; padding: 12px; overflow: auto; }
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
.form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-elev); }
.row { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; }
.row label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-dim); }
.import { display: flex; gap: 6px; margin-top: 12px; }
.import input { flex: 1; }
.empty { color: var(--text-faint); }
.empty.err { color: var(--danger); }
</style>
