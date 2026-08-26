<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { useLogStore } from './stores/logs'
import ForwardView from './views/ForwardView.vue'
import SftpView from './views/SftpView.vue'

const active = ref('forward')
const logStore = useLogStore()
const fatal = ref('')

function onErr(evt) { fatal.value = evt.detail }
function dismissFatal() { fatal.value = '' }
let offLog = null
onMounted(() => {
  // EventsOn 返回退订函数：不保存并在卸载时调用的话，dev 模式 HMR 重挂载会
  // 叠加注册，导致每条日志重复入 store（M6b）。
  offLog = EventsOn('log', (evt) => logStore.add(evt))
  window.addEventListener('sshkit:error', onErr)
})
onUnmounted(() => {
  if (offLog) offLog()
  window.removeEventListener('sshkit:error', onErr)
})
</script>

<template>
  <div class="app">
    <nav class="sidebar">
      <button :class="{ active: active === 'forward' }" @click="active = 'forward'">端口转发</button>
      <button :class="{ active: active === 'sftp' }" @click="active = 'sftp'">SFTP</button>
    </nav>
    <main class="workspace">
      <div v-if="fatal" class="fatal">⚠ 界面错误: {{ fatal }} <button class="fatal-close" @click="dismissFatal">×</button></div>
      <KeepAlive>
        <ForwardView v-if="active === 'forward'" key="forward" />
        <SftpView v-else key="sftp" />
      </KeepAlive>
    </main>
  </div>
</template>

<style>
.app { display: flex; height: 100vh; }
.sidebar { width: 140px; border-right: 1px solid var(--border); padding-top: 8px; flex-shrink: 0; }
.sidebar button { display: block; width: 100%; text-align: left; padding: 12px 16px; border: none; background: none; cursor: pointer; color: var(--text-dim); }
.sidebar button:hover { background: var(--surface); color: var(--text); }
.sidebar button.active { background: var(--surface-hover); color: var(--text); border-left: 3px solid var(--accent); }
.workspace { flex: 1; padding: 12px; overflow: auto; text-align: left; }
.fatal { background: var(--danger); color: #fff; padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.fatal-close { background: transparent; border: none; color: #fff; font-size: 16px; cursor: pointer; line-height: 1; padding: 0 4px; }
</style>
