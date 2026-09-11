<script setup>
import { ref, onActivated, onDeactivated, onUnmounted } from 'vue'
import {
  ListSyncRules, CreateSyncRule, UpdateSyncRule, DeleteSyncRule,
  SyncRuleStates, SyncRuleStats, SyncRuleConflicts, ResolveSyncConflict,
  ConfirmSyncRuleDeletes, ListHosts, PickLocalDir,
} from '../../wailsjs/go/main/App'
import SyncCard from '../components/SyncCard.vue'
import SyncConflictsDialog from '../components/SyncConflictsDialog.vue'
import LogPanel from '../components/LogPanel.vue'
import AppDialog from '../components/AppDialog.vue'
import { useLogStore } from '../stores/logs'
import { useSettingsStore } from '../stores/settings'
import {
  newSyncRuleForm, syncRuleToForm, formToSyncRule, validateSyncRuleForm,
  statsOf, ruleLabel,
  canMirrorDelete, mirrorDeleteHint, shouldRefresh,
  dialogErrorText, shouldSubscribe,
} from '../utils/sync'

// 只有同步与系统日志进这个视图：sftp 传输日志的 source_id 是 host 而不是规则 id，
// 加进来既污染隔离、又永远命中不了按规则过滤（spec §9.4）。
const SOURCE_TYPES = ['sync', 'system']

const rules = ref([])
const hosts = ref([])
const states = ref({})
const stats = ref({})
const loadError = ref('')
const opError = ref('')
const logStore = useLogStore()
const settingsStore = useSettingsStore()

const dialog = ref({ visible: false, title: '', message: '' })
let dialogResolve = null
function openConfirm(title, message) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, title, message }
    dialogResolve = resolve
  })
}
function onDialogOk() { dialog.value.visible = false; if (dialogResolve) { dialogResolve(true); dialogResolve = null } }
function onDialogCancel() { dialog.value.visible = false; if (dialogResolve) { dialogResolve(null); dialogResolve = null } }

const showForm = ref(false)
const editingId = ref(null)
const form = ref(newSyncRuleForm())

const conflictsVisible = ref(false)
const conflictsFor = ref({ id: '', name: '' })
const conflicts = ref([])
// FIX 1：裁决失败的原因必须显示在对话框内部（左侧 opError 被遮罩挡住），
// 且批量在途时要防重复触发。
const conflictsError = ref('')
const resolveBusy = ref(false)

async function refresh() {
  try {
    rules.value = (await ListSyncRules()) || []
    states.value = (await SyncRuleStates()) || {}
    stats.value = (await SyncRuleStats()) || {}
    loadError.value = ''
  } catch (e) { loadError.value = String(e) }
}

async function loadHosts() {
  try { hosts.value = (await ListHosts()) || [] } catch (e) { loadError.value = String(e) }
}

function openCreate() {
  editingId.value = null
  form.value = newSyncRuleForm()
  // S3：auto_reconnect 的纯函数兜底是 true，真正的默认取全局设置
  // （后端 App.AutoReconnectDefault，经 settings store 暴露）。
  form.value.auto_reconnect = settingsStore.autoReconnectDefault
  opError.value = ''
  showForm.value = true
}

function openEdit(rule) {
  editingId.value = rule.id
  form.value = syncRuleToForm(rule)
  opError.value = ''
  showForm.value = true
}

async function submit() {
  const msg = validateSyncRuleForm(form.value)
  if (msg) { opError.value = msg; return }
  opError.value = ''
  try {
    const payload = formToSyncRule(form.value)
    if (editingId.value) await UpdateSyncRule(payload)
    else await CreateSyncRule(payload)
    showForm.value = false
    editingId.value = null
    form.value = newSyncRuleForm()
    await refresh()
  } catch (e) { opError.value = String(e) }
}

async function pickLocalDir() {
  try {
    const dir = await PickLocalDir()
    if (dir) form.value.local_path = dir
  } catch (e) { opError.value = String(e) }
}

async function remove(rule) {
  const ok = await openConfirm('确认删除', '删除同步规则「' + ruleLabel(rule) + '」？这会同时清理它的状态文件与本地临时残留。')
  if (!ok) return
  opError.value = ''
  try {
    await DeleteSyncRule(rule.id)
    await refresh()
  } catch (e) { opError.value = String(e) }
}

function toggleLog(rule) {
  logStore.filterSource = logStore.filterSource === rule.id ? '' : rule.id
}

async function openConflicts(rule) {
  opError.value = ''
  conflictsError.value = '' // FIX 1b：打开对话框时清空上一次的错误
  try {
    conflictsFor.value = { id: rule.id, name: ruleLabel(rule) } // S6：与卡片同一回退顺序
    conflicts.value = (await SyncRuleConflicts(rule.id)) || []
    conflictsVisible.value = true
  } catch (e) { opError.value = String(e) }
}

// FIX 1c：无论裁决成功还是抛错（例如并发对账已把该路径解掉），末尾都要重取冲突列表，
// 否则对话框会一直展示已解决的陈旧行，用户再点只会继续失败。
async function refetchConflicts() {
  try {
    conflicts.value = (await SyncRuleConflicts(conflictsFor.value.id)) || []
  } catch (e) {
    conflictsError.value = dialogErrorText(e)
  }
}

async function resolveOne({ rel, action }) {
  conflictsError.value = '' // FIX 1b：每个裁决动作开始前清空错误
  try {
    await ResolveSyncConflict(conflictsFor.value.id, rel, action)
    await refresh()
  } catch (e) {
    conflictsError.value = dialogErrorText(e)
  } finally {
    await refetchConflicts()
  }
}

async function resolveAll(action) {
  if (resolveBusy.value) return // FIX 1d：批量在途时拒绝再次触发
  resolveBusy.value = true
  conflictsError.value = ''
  try {
    for (const c of conflicts.value.slice()) {
      await ResolveSyncConflict(conflictsFor.value.id, c.rel_path, action)
    }
    await refresh()
  } catch (e) {
    conflictsError.value = dialogErrorText(e)
  } finally {
    await refetchConflicts()
    resolveBusy.value = false
  }
}

async function confirmDeletes({ rule, fingerprint, count }) {
  const ok = await openConfirm('确认删除本地文件', '将按远端状态删除 ' + count + ' 个本地文件，此操作不可撤销。确认继续？')
  if (!ok) return
  opError.value = ''
  try {
    await ConfirmSyncRuleDeletes(rule.id, fingerprint)
    await refresh()
  } catch (e) { opError.value = String(e) }
}

// 状态转换经 'log' 事件流到达；App.vue 已持有全局订阅，这里只订 pinia store
// 后防抖刷新 —— 不二次 EventsOn、不轮询（spec §9.3）。
let unsubLogs = null
let refreshTimer = null
// FIX 3：loadHosts()/refresh() 的 await 期间视图可能被 KeepAlive 切走。active 由
// onActivated/onDeactivated 维护，subscribe() 只在激活且未订阅时注册，避免在已隐藏
// 的实例上挂订阅与 300ms 定时器（切走本身会 unsubscribe，只有 unmount 中途才会永久泄漏）。
let active = false
function subscribe() {
  if (!shouldSubscribe(active, !!unsubLogs)) return
  unsubLogs = logStore.$subscribe((mutation, state) => {
    const logs = state.logs
    const last = logs[logs.length - 1]
    if (!last || !shouldRefresh(last.source_type, SOURCE_TYPES)) return
    clearTimeout(refreshTimer)
    refreshTimer = setTimeout(refresh, 300)
  })
  refreshTimer = setTimeout(refresh, 300)
}
function unsubscribe() {
  if (unsubLogs) { unsubLogs(); unsubLogs = null }
  clearTimeout(refreshTimer)
  refreshTimer = null
}

// App.vue 用 <KeepAlive>：切走不会触发 onUnmounted，所以必须用
// onActivated/onDeactivated 管理订阅与定时器（先例 SftpView.vue）。
onActivated(async () => {
  active = true
  if (!hosts.value.length) await loadHosts()
  await refresh()
  subscribe() // 若 await 期间已 onDeactivated，这里会因 active=false 而跳过
})
onDeactivated(() => { active = false; unsubscribe() })
onUnmounted(() => { active = false; unsubscribe() })
</script>

<template>
  <div class="sync">
    <div class="panel ui-panel">
      <div class="toolbar">
        <button @click="openCreate">+ 新建规则</button>
        <button @click="refresh">刷新</button>
      </div>

      <form v-if="showForm" class="form" @submit.prevent="submit">
        <div class="row">
          <label>名称 <input v-model="form.name" placeholder="e.g. prod-conf" /></label>
          <label>主机
            <select v-model="form.host" required>
              <option value="" disabled>选择主机</option>
              <option v-if="form.host && !hosts.includes(form.host)" :value="form.host">{{ form.host }}</option>
              <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
            </select>
          </label>
          <label>SSH 用户 <input v-model="form.user" placeholder="留空用 ssh 配置默认" /></label>
          <label>类型
            <select v-model="form.kind">
              <option value="dir">目录</option>
              <option value="file">单文件</option>
            </select>
          </label>
        </div>

        <div class="row">
          <label class="grow">远端路径 <input v-model="form.remote_path" placeholder="/srv/conf 或 ~/conf" required /></label>
          <label class="grow">本地路径
            <input v-model="form.local_path" placeholder="本地目录" required />
          </label>
          <button type="button" @click="pickLocalDir">选择目录</button>
        </div>

        <div class="row">
          <label>递归深度 <input v-model.number="form.max_depth" type="number" /></label>
          <label>轮询间隔（秒） <input v-model.number="form.poll_interval_s" type="number" min="1" max="3600" /></label>
          <label class="check"><input v-model="form.force_poll" type="checkbox" /> 强制轮询</label>
          <label class="check"><input v-model="form.auto_reconnect" type="checkbox" /> 断线自动重连</label>
          <!-- S4：编辑模式下后端 UpdateSyncRule 强制 Enabled=false，勾选毫无意义；
               只在创建时渲染，编辑时改为说明运行状态由开始/停止按钮控制。 -->
          <label v-if="!editingId" class="check"><input v-model="form.enabled" type="checkbox" /> 启用</label>
          <span v-else class="hint">运行状态由卡片上的开始/停止按钮控制</span>
        </div>

        <div class="row">
          <label class="check">
            <input v-model="form.mirror_delete" type="checkbox" :disabled="!canMirrorDelete(form.kind)" />
            镜像删除（远端删除时同步删除本地）
          </label>
          <span v-if="mirrorDeleteHint(form.kind)" class="hint">{{ mirrorDeleteHint(form.kind) }}</span>
        </div>

        <label class="block">排除项（每行一条，支持 .git/ 或 *.swp）
          <textarea v-model="form.excludesText" rows="3" placeholder=".git/"></textarea>
          <span class="hint">已预填后端默认排除项（.git/、node_modules/、*.swp、*~、.DS_Store，镜像 internal/config/store.go:83-85）。清空文本框表示不排除任何路径，请谨慎。</span>
        </label>

        <div class="row">
          <button type="submit">{{ editingId ? '保存' : '创建' }}</button>
          <button type="button" @click="showForm = false">取消</button>
        </div>
      </form>

      <SyncCard v-for="r in rules" :key="r.id" :rule="r" :state="states[r.id]" :stats="statsOf(stats, r.id)"
        @edit="openEdit" @remove="remove" @log="toggleLog" @conflicts="openConflicts"
        @confirmDeletes="confirmDeletes" @changed="refresh" />

      <p v-if="loadError" class="empty err">加载失败: {{ loadError }}</p>
      <p v-if="opError" class="empty err">{{ opError }}</p>
      <p v-else-if="!rules.length" class="empty">暂无同步规则，点「+ 新建规则」</p>
    </div>

    <div class="panel ui-panel">
      <!-- S9：把规则传给 LogPanel，它的逐步 chips 才能显示规则名而不是 32 位 id；
           LogPanel 内部用 name || host 作 chip 文案。 -->
      <LogPanel :tunnels="rules" :source-types="SOURCE_TYPES" />
    </div>

    <SyncConflictsDialog :visible="conflictsVisible" :conflicts="conflicts"
      :rule-name="conflictsFor.name" :error-text="conflictsError" :busy="resolveBusy"
      @close="conflictsVisible = false"
      @resolve="resolveOne" @resolveAll="resolveAll" />

    <AppDialog :visible="dialog.visible" mode="confirm" :title="dialog.title" :message="dialog.message"
      @ok="onDialogOk" @cancel="onDialogCancel" />
  </div>
</template>

<style scoped>
.sync { display: flex; height: 100%; gap: 12px; }
.panel { flex: 1; padding: 12px; overflow: auto; }
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
.form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-elev); }
.row { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; }
.row label { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-12); color: var(--text-dim); }
.row label.check { flex-direction: row; align-items: center; gap: 6px; }
.row label.grow { flex: 1; min-width: 220px; }
.block { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-12); color: var(--text-dim); }
.hint { color: var(--text-faint); font-size: var(--fs-12); }
.empty { color: var(--text-faint); }
.empty.err { color: var(--danger); }
</style>
