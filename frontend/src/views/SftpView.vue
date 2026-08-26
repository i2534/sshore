<script setup>
import { ref, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { ListHosts, SftpList, SftpGet, SftpPut, SftpRemove, SftpMkdir, SftpRename, SftpConnect, SftpDisconnect, SftpConnected, ListLocal, DeleteLocal, MkdirLocal, RenameLocal, StatLocal, PickLocalFile, Cwd, SftpHome } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import FilePane from '../components/FilePane.vue'
import TransferQueue from '../components/TransferQueue.vue'
import LogPanel from '../components/LogPanel.vue'
import ContextMenu from '../components/ContextMenu.vue'
import AppDialog from '../components/AppDialog.vue'

const logStore = useLogStore()
const hosts = ref([])
const host = ref('')

// remote pane — start at filesystem root so '..' can navigate the whole tree
const remotePath = ref('/')
const remoteItems = ref([])
const remoteSel = ref(null)
const remoteLoading = ref(false)

// local pane — start at cwd so '..' can navigate up to '/'
const localPath = ref('')
const localItems = ref([])
const localSel = ref(null)
const localLoading = ref(false)

// shared "show hidden files" toggle (both panes)
const showAll = ref(false)

// explicit connect/disconnect state for the remote host
const connected = ref(false)

const transfers = ref([])

// clock ticks every second so the transfer queue's elapsed times repaint.
const now = ref(Date.now())
let clockTimer = null

// context menu state
const menu = ref({ visible: false, x: 0, y: 0, pane: 'remote', item: null })

// modal dialog state (replaces native window.confirm/prompt)
const dialog = ref({ visible: false, mode: 'confirm', title: '', message: '', placeholder: '', initial: '' })
let dialogResolve = null

function openConfirm(title, message) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, mode: 'confirm', title, message, placeholder: '', initial: '' }
    dialogResolve = resolve
  })
}
function openPrompt(title, message, initial) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, mode: 'prompt', title, message, placeholder: '', initial: initial || '' }
    dialogResolve = resolve
  })
}
function onDialogOk(value) {
  dialog.value.visible = false
  if (dialogResolve) { dialogResolve(value || true); dialogResolve = null }
}
function onDialogCancel() {
  dialog.value.visible = false
  if (dialogResolve) { dialogResolve(null); dialogResolve = null }
}

// remote pane request sequence：任何新请求或切换主机都自增，
// 过期响应直接丢弃，防止旧主机/旧目录的结果污染当前视图（M3）。
let remoteSeq = 0

function closeMenu() { menu.value.visible = false }
function outsideClick() { closeMenu() }

async function loadHosts() {
  try { hosts.value = (await ListHosts()) || [] } catch (e) { err(e) }
  if (hosts.value.length && !host.value) host.value = hosts.value[0]
}
async function err(e) {
  logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
}

async function loadRemote() {
  if (!host.value) return
  const seq = ++remoteSeq
  remoteLoading.value = true
  try {
    const items = (await SftpList(host.value, '', remotePath.value || '/')) || []
    if (seq === remoteSeq) remoteItems.value = items // 已被更新请求取代则丢弃
  }
  catch (e) { err(e) }
  finally { if (seq === remoteSeq) remoteLoading.value = false }
}

// Host dropdown change: reset connection state, do NOT auto-connect.
function onHostChange() {
  remoteSeq++ // 使在途的远程请求全部过期
  connected.value = false
  remoteItems.value = []
  remoteSel.value = null
}

async function connect() {
  const h = host.value
  if (!h) return
  try {
    await SftpConnect(h)
    let home = '/'
    try { home = await SftpHome(h) } catch { home = '/' }
    if (host.value !== h) return // await 期间已切换主机，丢弃结果
    remotePath.value = home
    connected.value = true
    await loadRemote()
  } catch (e) {
    err(e)
    if (host.value === h) connected.value = false
  }
}

async function disconnect() {
  if (!host.value) return
  try { await SftpDisconnect(host.value) } catch (e) { err(e) }
  connected.value = false
  remoteItems.value = []
  remoteSel.value = null
}
async function loadLocal() {
  localLoading.value = true
  try { localItems.value = (await ListLocal(localPath.value || '/')) || [] }
  catch (e) { err(e) }
  finally { localLoading.value = false }
}

function openRemote(it) {
  if (it.name === '..') { remotePath.value = parentOf(remotePath.value); loadRemote(); return }
  if (!it.isDir) return
  remotePath.value = join(remotePath.value, it.name)
  loadRemote()
}
function openLocal(it) {
  if (it.name === '..') { localPath.value = parentOf(localPath.value); loadLocal(); return }
  if (!it.isDir) return
  localPath.value = join(localPath.value, it.name)
  loadLocal()
}

// Absolute-path helpers (POSIX-style). '/' is the root; going up stops there.
function join(base, name) {
  if (base === '/' || base === '') return '/' + name
  return base.replace(/\/+$/, '') + '/' + name
}
function parentOf(p) {
  if (!p || p === '/') return '/'
  const trimmed = p.replace(/\/+$/, '')
  const idx = trimmed.lastIndexOf('/')
  if (idx <= 0) return '/'
  return trimmed.slice(0, idx)
}

function showMenu(pane, { item, event }) {
  const rect = window.innerWidth
  menu.value = {
    visible: true,
    x: Math.min(event.clientX, rect - 180),
    y: event.clientY,
    pane,
    item,
  }
}

async function download() {
  const it = menu.value.item || remoteSel.value
  if (!it || it.isDir || it.name === '..') { closeMenu(); return }
  // Save directly to the local pane's current directory (no picker dialog).
  const dir = localPath.value || '.'
  const t = { name: it.name, size: it.size || 0, status: '处理中', startedAt: Date.now() }
  try {
    transfers.value.push(t)
    await SftpGet(host.value, '', join(remotePath.value, it.name), join(dir, it.name))
    t.status = '完成'
    t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
    await loadLocal()
  } catch (e) {
    err(e)
    t.status = '失败'
    t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
  }
  closeMenu()
}

async function remove() {
  const it = menu.value.item || remoteSel.value
  const pane = menu.value.pane
  if (!it || it.name === '..') { closeMenu(); return }
  const ok = await openConfirm('确认删除', `删除${pane === 'local' ? '本地' : '远程'}「${it.name}」？`)
  if (!ok) { closeMenu(); return }
  try {
    if (pane === 'local') {
      await DeleteLocal(join(localPath.value, it.name))
      await loadLocal()
    } else {
      await SftpRemove(host.value, '', join(remotePath.value, it.name))
      await loadRemote()
    }
  } catch (e) { err(e) }
  closeMenu()
}

async function rename() {
  const it = menu.value.item || remoteSel.value
  if (!it || it.name === '..') { closeMenu(); return }
  const newName = await openPrompt('重命名', '新名称：', it.name)
  if (!newName || newName === it.name) { closeMenu(); return }
  try {
    if (menu.value.pane === 'local') {
      await renameLocal(join(localPath.value, it.name), join(localPath.value, newName))
      await loadLocal()
    } else {
      await SftpRename(host.value, '', join(remotePath.value, it.name), join(remotePath.value, newName))
      await loadRemote()
    }
  } catch (e) { err(e) }
  closeMenu()
}

// renameLocal renames a local file/dir via the Go binding.
async function renameLocal(oldPath, newPath) {
  await RenameLocal(oldPath, newPath)
}

async function mkdir() {
  const pane = menu.value.pane
  closeMenu()
  const name = await openPrompt('新建文件夹', '新建文件夹名称：', '')
  if (!name) return
  try {
    if (pane === 'local') {
      await MkdirLocal(join(localPath.value, name))
      await loadLocal()
    } else {
      await SftpMkdir(host.value, '', join(remotePath.value, name))
      await loadRemote()
    }
  } catch (e) { err(e) }
}

async function upload() {
  const it = menu.value.item
  const pane = menu.value.pane
  closeMenu()
  let t = null
  try {
    // If a specific local file was right-clicked, upload it directly
    // (no system dialog). Otherwise (toolbar / remote pane) pick a file.
    let local, name
    if (pane === 'local' && it && it.name !== '..' && !it.isDir) {
      local = join(localPath.value, it.name)
      name = it.name
    } else {
      local = await PickLocalFile()
      if (!local) return
      name = local.split(/[\\/]/).pop()
    }
    t = { name, size: 0, status: '处理中', startedAt: Date.now() }
    try { t.size = await StatLocal(local) } catch (e) { t.size = 0 }
    transfers.value.push(t)
    await SftpPut(host.value, '', local, join(remotePath.value, name))
    t.status = '完成'
    t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
    await loadRemote()
  } catch (e) {
    err(e)
    if (t) {
      t.status = '失败'
      t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
    }
  }
}

async function doAction(name) {
  switch (name) {
    case 'download': return download()
    case 'upload': return upload()
    case 'remove': return remove()
    case 'mkdir': return mkdir()
    case 'rename': return rename()
  }
}

onMounted(async () => {
  await loadHosts()
  // local starts at current working dir
  try { localPath.value = await Cwd() } catch (e) { localPath.value = '/' }
  await loadLocal()
})

function startClock() {
  if (!clockTimer) clockTimer = setInterval(() => { now.value = Date.now() }, 1000)
}
function stopClock() {
  if (clockTimer) { clearInterval(clockTimer); clockTimer = null }
}

// syncConnection 用后端 SftpConnected 校正本地状态（M6）：KeepAlive 下切标签
// 不再销毁视图，但首次挂载/后端 ControlPersist 过期时 UI 可能与真实连接不符。
async function syncConnection() {
  const h = host.value
  if (!h || connected.value) return
  try {
    const ok = await SftpConnected(h)
    if (host.value !== h || connected.value) return // await 期间状态已变
    if (!ok) { connected.value = false; return }
    connected.value = true
    try { remotePath.value = await SftpHome(h) } catch { remotePath.value = '/' }
    await loadRemote()
  } catch { /* 状态查询失败时保持现状 */ }
}

// KeepAlive 生命周期：切入时挂菜单监听、重启时钟并同步连接状态；切出时停时钟。
onActivated(() => {
  window.addEventListener('click', outsideClick)
  startClock()
  syncConnection()
})
onDeactivated(() => {
  window.removeEventListener('click', outsideClick)
  stopClock()
})
onUnmounted(() => {
  window.removeEventListener('click', outsideClick)
  stopClock()
})
</script>

<template>
  <div class="sftp">
    <div class="toolbar">
      <select v-model="host" @change="onHostChange">
        <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
      </select>
      <button v-if="!connected" @click="connect" :disabled="!host">🔌 连接</button>
      <button v-else @click="disconnect">⏏ 断开</button>
      <button @click="loadRemote" :disabled="!connected">刷新</button>
      <label class="hidden-toggle">
        <input type="checkbox" v-model="showAll" /> 显示隐藏文件
      </label>
    </div>
    <div class="panes">
      <FilePane title="本地" :path="localPath || '/'" :items="localItems" :selected="localSel && localSel.name" :show-hidden="showAll" :loading="localLoading"
        @select="localSel = $event" @open="openLocal" @context="showMenu('local', $event)" />
      <FilePane title="远程" :path="remotePath" :items="remoteItems" :selected="remoteSel && remoteSel.name" :show-hidden="showAll" :loading="remoteLoading"
        @select="remoteSel = $event" @open="openRemote" @context="showMenu('remote', $event)" />
    </div>
    <TransferQueue :transfers="transfers" :now="now" />
    <div class="logpane"><LogPanel /></div>

    <ContextMenu :visible="menu.visible" :x="menu.x" :y="menu.y" @close="closeMenu">
      <template v-if="menu.pane === 'remote'">
        <button @click="doAction('download')"><span class="ic">⬇</span>下载…</button>
        <button @click="doAction('upload')"><span class="ic">⬆</span>上传…</button>
        <button @click="doAction('rename')"><span class="ic">✏</span>重命名</button>
        <button class="sep" @click="doAction('remove')"><span class="ic">🗑</span>删除</button>
        <button @click="doAction('mkdir')"><span class="ic">📁</span>新建文件夹</button>
      </template>
      <template v-else>
        <button @click="doAction('upload')"><span class="ic">⬆</span>上传到远程</button>
        <button @click="doAction('rename')"><span class="ic">✏</span>重命名</button>
        <button class="sep" @click="doAction('remove')"><span class="ic">🗑</span>删除</button>
        <button @click="doAction('mkdir')"><span class="ic">📁</span>新建文件夹</button>
      </template>
    </ContextMenu>

    <AppDialog
      :visible="dialog.visible"
      :mode="dialog.mode"
      :title="dialog.title"
      :message="dialog.message"
      :initial="dialog.initial"
      @ok="onDialogOk"
      @cancel="onDialogCancel"
    />
  </div>
</template>

<style scoped>
.sftp { display: flex; flex-direction: column; height: 100%; gap: 8px; }
.toolbar { display: flex; gap: 8px; align-items: center; }
.panes { display: flex; gap: 8px; flex: 1; min-height: 0; }
.logpane { height: 140px; flex-shrink: 0; border: 1px solid var(--border); background: var(--surface); border-radius: 8px; padding: 12px; overflow: auto; }
.hidden-toggle { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--text-dim); }
</style>
