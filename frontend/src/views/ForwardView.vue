<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { ListTunnels, ListHosts, CreateTunnel, UpdateTunnel, DeleteTunnel, ImportCommand, TunnelStates } from '../../wailsjs/go/main/App'
import RuleCard from '../components/RuleCard.vue'
import LogPanel from '../components/LogPanel.vue'
import AppDialog from '../components/AppDialog.vue'
import { useLogStore } from '../stores/logs'

const tunnels = ref([])
const hosts = ref([])
const cmd = ref('')
const loadError = ref('')
const opError = ref('')
const logStore = useLogStore()
const states = ref({})

const dialog = ref({ visible: false, mode: 'confirm', title: '', message: '', initial: '' })
let dialogResolve = null
function openConfirm(title, message) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, mode: 'confirm', title, message, initial: '' }
    dialogResolve = resolve
  })
}
function onDialogOk() { dialog.value.visible = false; if (dialogResolve) { dialogResolve(true); dialogResolve = null } }
function onDialogCancel() { dialog.value.visible = false; if (dialogResolve) { dialogResolve(null); dialogResolve = null } }

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
  try {
    tunnels.value = (await ListTunnels()) || []
    states.value = (await TunnelStates()) || {}
  } catch (e) { loadError.value = String(e) }
}
async function loadHosts() {
  try { hosts.value = (await ListHosts()) || [] } catch (e) { loadError.value = String(e) }
}
async function doImport() {
  opError.value = ''
  try {
    await ImportCommand(cmd.value)
    cmd.value = ''
    await refresh()
  } catch (e) { opError.value = String(e) }
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
  opError.value = ''
  try {
    if (editingId.value) await UpdateTunnel(form.value)
    else await CreateTunnel(form.value)
    showForm.value = false
    editingId.value = null
    form.value = newTunnel()
    await refresh()
  } catch (e) { opError.value = String(e) }
}
async function remove(t) {
  const ok = await openConfirm('确认删除', `删除规则「${t.name || t.host}」？`)
  if (!ok) return
  opError.value = ''
  try {
    await DeleteTunnel(t.id)
    await refresh()
  } catch (e) { opError.value = String(e) }
}
onMounted(async () => { await loadHosts(); await refresh() })

// 状态转换经 'log' 事件流到达（source_type==='tunnel'）；App.vue 已持有全局订阅，
// 这里只订 pinia store，过滤后防抖刷新——不二次 EventsOn、不轮询。
// 本地 RuleCard 错误日志无 source_type（source_id==='rule'），被此过滤自然跳过。
let unsubLogs = null
let refreshTimer = null
onMounted(() => {
  unsubLogs = logStore.$subscribe((mutation, state) => {
    const logs = state.logs
    const last = logs[logs.length - 1]
    if (!last || last.source_type !== 'tunnel') return
    clearTimeout(refreshTimer)
    refreshTimer = setTimeout(refresh, 300)
  })
})
onUnmounted(() => {
  if (unsubLogs) unsubLogs()
  clearTimeout(refreshTimer)
})
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

      <RuleCard v-for="t in tunnels" :key="t.id" :tunnel="t" :state="states[t.id]"
        @edit="openEdit" @delete="remove" @changed="refresh" />
      <p v-if="loadError" class="empty err">加载失败: {{ loadError }}</p>
      <p v-if="opError" class="empty err">操作失败: {{ opError }}</p>
      <p v-else-if="!tunnels.length" class="empty">暂无规则，点「+ 新建规则」或导入 ssh 命令</p>

      <div class="import">
        <input v-model="cmd" placeholder="ssh -N -L 5432:127.0.0.1:5432 prod-db">
        <button @click="doImport">导入</button>
      </div>
    </div>
    <div class="panel"><LogPanel /></div>

    <AppDialog
      :visible="dialog.visible"
      :mode="dialog.mode"
      :title="dialog.title"
      :message="dialog.message"
      @ok="onDialogOk"
      @cancel="onDialogCancel"
    />
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
