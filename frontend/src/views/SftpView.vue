<script setup>
import { ref, onMounted } from 'vue'
import { ListHosts, SftpList, SftpGet } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import FilePane from '../components/FilePane.vue'
import TransferQueue from '../components/TransferQueue.vue'

const logStore = useLogStore()
const hosts = ref([])
const host = ref('')
const remotePath = ref('~')
const remoteItems = ref([])
const localPath = ref('.')
const localItems = ref([])
const selected = ref(null)
const transfers = ref([])

async function loadHosts() {
  hosts.value = await ListHosts()
  if (hosts.value.length && !host.value) host.value = hosts.value[0]
}
async function loadRemote() {
  try {
    remoteItems.value = await SftpList(host.value, '', remotePath.value)
  } catch (e) {
    logStore.add({ source_id: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
  }
}
async function download() {
  if (!selected.value || selected.value.isDir) return
  const t = { name: selected.value.name, status: '处理中' }
  transfers.value.push(t)
  try {
    await SftpGet(host.value, '', remotePath.value + '/' + selected.value.name, localPath.value + '/' + selected.value.name)
    t.status = '完成'
  } catch (e) {
    t.status = '失败'
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
      <button @click="download">⬇ 下载选中</button>
      <button @click="loadRemote">刷新</button>
    </div>
    <div class="panes">
      <FilePane title="本地" :items="localItems" :selected="selected && selected.name" @select="selected = $event" />
      <FilePane title="远程" :items="remoteItems" :selected="selected && selected.name" @select="selected = $event" />
    </div>
    <TransferQueue :transfers="transfers" />
  </div>
</template>

<style scoped>
.sftp { display: flex; flex-direction: column; height: 100%; gap: 8px; }
.toolbar { display: flex; gap: 8px; }
.panes { display: flex; gap: 8px; flex: 1; }
</style>
