<script setup>
import { ref, onMounted } from 'vue'
import { ListHosts, SftpList, SftpGet, SftpPut, SftpRemove, SftpMkdir, PickLocalFile } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import FilePane from '../components/FilePane.vue'
import TransferQueue from '../components/TransferQueue.vue'

const logStore = useLogStore()
const hosts = ref([])
const host = ref('')
const remotePath = ref('')
const remoteItems = ref([])
const localItems = ref([])
const selected = ref(null)
const transfers = ref([])

async function loadHosts() {
  hosts.value = await ListHosts()
  if (hosts.value.length && !host.value) { host.value = hosts.value[0]; remotePath.value = '.' }
}

async function loadRemote() {
  if (!host.value) return
  try {
    remoteItems.value = await SftpList(host.value, '', remotePath.value || '.')
  } catch (e) {
    logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
  }
}

function enterDir(it) {
  if (!it.isDir) return
  const base = remotePath.value === '.' ? '' : remotePath.value
  remotePath.value = (base + '/' + it.name).replace(/^\/+/, '')
  loadRemote()
}

function goUp() {
  if (remotePath.value === '.' || !remotePath.value) return
  const parts = remotePath.value.split('/')
  parts.pop()
  remotePath.value = parts.length ? parts.join('/') : '.'
  loadRemote()
}

async function download() {
  if (!selected.value || selected.value.isDir) return
  const name = selected.value.name
  const t = { name, status: '处理中' }
  transfers.value.push(t)
  try {
    const local = name
    await SftpGet(host.value, '', remotePath.value + '/' + name, local)
    t.status = '完成'
  } catch (e) {
    t.status = '失败'
    logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
  }
}

async function upload() {
  try {
    const local = await PickLocalFile()
    if (!local) return
    const name = local.split('/').pop()
    const t = { name, status: '处理中' }
    transfers.value.push(t)
    await SftpPut(host.value, '', local, remotePath.value + '/' + name)
    t.status = '完成'
    await loadRemote()
  } catch (e) {
    logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
  }
}

async function remove() {
  if (!selected.value) return
  const name = selected.value.name
  if (!window.confirm(`删除远程「${name}」？`)) return
  try {
    await SftpRemove(host.value, '', remotePath.value + '/' + name)
    await loadRemote()
  } catch (e) {
    logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
  }
}

async function mkdir() {
  const name = window.prompt('新建文件夹名称：')
  if (!name) return
  try {
    await SftpMkdir(host.value, '', remotePath.value + '/' + name)
    await loadRemote()
  } catch (e) {
    logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
  }
}

onMounted(async () => { await loadHosts(); await loadRemote() })
</script>

<template>
  <div class="sftp">
    <div class="toolbar">
      <select v-model="host" @change="loadRemote">
        <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
      </select>
      <span class="path">远程: {{ remotePath || '.' }}</span>
      <button @click="goUp" :disabled="remotePath === '.'">⬆ 上级</button>
      <button @click="loadRemote">刷新</button>
    </div>
    <div class="toolbar">
      <button @click="download" :disabled="!selected || selected.isDir">⬇ 下载</button>
      <button @click="upload">⬆ 上传</button>
      <button @click="remove" :disabled="!selected">🗑 删除</button>
      <button @click="mkdir">📁 新建文件夹</button>
    </div>
    <div class="panes">
      <FilePane title="远程" :items="remoteItems" :selected="selected && selected.name" @select="selected = $event" @open="enterDir" />
    </div>
    <TransferQueue :transfers="transfers" />
  </div>
</template>

<style scoped>
.sftp { display: flex; flex-direction: column; height: 100%; gap: 8px; }
.toolbar { display: flex; gap: 8px; align-items: center; }
.path { color: var(--text-dim); font-size: 12px; font-family: monospace; }
.panes { display: flex; gap: 8px; flex: 1; }
</style>
