<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { ListHosts, SftpList, SftpGet, SftpPut, SftpRemove, SftpMkdir, ListLocal, PickLocalFile, PickLocalDir, HomeDir } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import FilePane from '../components/FilePane.vue'
import TransferQueue from '../components/TransferQueue.vue'
import ContextMenu from '../components/ContextMenu.vue'

const logStore = useLogStore()
const hosts = ref([])
const host = ref('')

// remote pane — start at filesystem root so '..' can navigate the whole tree
const remotePath = ref('/')
const remoteItems = ref([])
const remoteHidden = ref(true)
const remoteSel = ref(null)
const remoteLoading = ref(false)

// local pane — start at home dir so '..' can navigate up to '/'
const localPath = ref('')
const localItems = ref([])
const localHidden = ref(true)
const localSel = ref(null)
const localLoading = ref(false)

const transfers = ref([])

// context menu state
const menu = ref({ visible: false, x: 0, y: 0, pane: 'remote', item: null })

function closeMenu() { menu.value.visible = false }
function outsideClick() { closeMenu() }
onMounted(() => window.addEventListener('click', outsideClick))
onUnmounted(() => window.removeEventListener('click', outsideClick))

async function loadHosts() {
  hosts.value = await ListHosts()
  if (hosts.value.length && !host.value) host.value = hosts.value[0]
}
async function err(e) {
  logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
}

async function loadRemote() {
  if (!host.value) return
  remoteLoading.value = true
  try { remoteItems.value = await SftpList(host.value, '', remotePath.value || '/') }
  catch (e) { err(e) }
  finally { remoteLoading.value = false }
}
async function loadLocal() {
  localLoading.value = true
  try { localItems.value = await ListLocal(localPath.value || '/') }
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
  try {
    const dir = await PickLocalDir()
    if (!dir) { closeMenu(); return }
    const t = { name: it.name, status: '处理中' }
    transfers.value.push(t)
    await SftpGet(host.value, '', join(remotePath.value, it.name), join(dir, it.name))
    t.status = '完成'
  } catch (e) { err(e) }
  closeMenu()
}

async function upload() {
  closeMenu()
  try {
    const local = await PickLocalFile()
    if (!local) return
    const name = local.split('/').pop()
    const t = { name, status: '处理中' }
    transfers.value.push(t)
    await SftpPut(host.value, '', local, join(remotePath.value, name))
    t.status = '完成'
    await loadRemote()
  } catch (e) { err(e) }
}

async function remove() {
  const it = menu.value.item || remoteSel.value
  const pane = menu.value.pane
  if (!it || it.name === '..') { closeMenu(); return }
  if (!window.confirm(`删除${pane === 'local' ? '本地' : '远程'}「${it.name}」？`)) { closeMenu(); return }
  try {
    if (pane === 'local') {
      // local delete via os is not exposed; skip
      err('本地删除暂不支持')
    } else {
      await SftpRemove(host.value, '', join(remotePath.value, it.name))
      await loadRemote()
    }
  } catch (e) { err(e) }
  closeMenu()
}

async function mkdir() {
  closeMenu()
  const name = window.prompt('新建文件夹名称：')
  if (!name) return
  try {
    await SftpMkdir(host.value, '', join(remotePath.value, name))
    await loadRemote()
  } catch (e) { err(e) }
}

async function doAction(name) {
  switch (name) {
    case 'download': return download()
    case 'upload': return upload()
    case 'remove': return remove()
    case 'mkdir': return mkdir()
    case 'toggleRemote': remoteHidden.value = !remoteHidden.value; closeMenu(); return
    case 'toggleLocal': localHidden.value = !localHidden.value; closeMenu(); return
  }
}

onMounted(async () => {
  await loadHosts()
  try { localPath.value = await HomeDir() } catch (e) { localPath.value = '/' }
  await loadRemote()
  await loadLocal()
})
</script>

<template>
  <div class="sftp">
    <div class="toolbar">
      <select v-model="host" @change="loadRemote">
        <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
      </select>
      <button @click="upload">⬆ 上传</button>
      <button @click="loadRemote">刷新</button>
    </div>
    <div class="panes">
      <FilePane title="本地" :path="localPath || '/'" :items="localItems" :selected="localSel && localSel.name" :show-hidden="localHidden" :loading="localLoading"
        @select="localSel = $event" @open="openLocal" @context="showMenu('local', $event)" />
      <FilePane title="远程" :path="remotePath" :items="remoteItems" :selected="remoteSel && remoteSel.name" :show-hidden="remoteHidden" :loading="remoteLoading"
        @select="remoteSel = $event" @open="openRemote" @context="showMenu('remote', $event)" />
    </div>
    <TransferQueue :transfers="transfers" />

    <ContextMenu :visible="menu.visible" :x="menu.x" :y="menu.y" @close="closeMenu">
      <template v-if="menu.pane === 'remote'">
        <button @click="doAction('download')">⬇ 下载…</button>
        <button @click="doAction('upload')">⬆ 上传…</button>
        <button class="sep" @click="doAction('remove')">🗑 删除</button>
        <button @click="doAction('mkdir')">📁 新建文件夹</button>
        <button class="sep" @click="doAction('toggleRemote')">{{ remoteHidden ? '🙈 隐藏文件' : '👁 显示隐藏文件' }}</button>
      </template>
      <template v-else>
        <button @click="doAction('remove')">🗑 删除</button>
        <button class="sep" @click="doAction('toggleLocal')">{{ localHidden ? '🙈 隐藏文件' : '👁 显示隐藏文件' }}</button>
      </template>
    </ContextMenu>
  </div>
</template>

<style scoped>
.sftp { display: flex; flex-direction: column; height: 100%; gap: 8px; }
.toolbar { display: flex; gap: 8px; align-items: center; }
.panes { display: flex; gap: 8px; flex: 1; min-height: 0; }
</style>
