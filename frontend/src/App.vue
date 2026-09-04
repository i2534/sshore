<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { useLogStore } from './stores/logs'
import { useSettingsStore } from './stores/settings'
import ForwardView from './views/ForwardView.vue'
import SftpView from './views/SftpView.vue'
import SettingsDialog from './components/SettingsDialog.vue'

const active = ref('forward')
const settingsVisible = ref(false)
const logStore = useLogStore()
const settingsStore = useSettingsStore()
const fatal = ref('')

function onErr(evt) { fatal.value = evt.detail }
function dismissFatal() { fatal.value = '' }
let offLog = null
onMounted(() => {
  // EventsOn 返回退订函数：不保存并在卸载时调用的话，dev 模式 HMR 重挂载会
  // 叠加注册，导致每条日志重复入 store（M6b）。
  offLog = EventsOn('log', (evt) => logStore.add(evt))
  window.addEventListener('sshore:error', onErr)
  // 加载并应用用户设置（主题/字号/字体/启动自动连接）。失败不阻断界面，
  // 仅借全局错误通道提示，避免设置读取失败导致整个应用挂掉。
  settingsStore.load().catch((e) => {
    console.error('[sshore] settings load failed:', e)
    window.dispatchEvent(new CustomEvent('sshore:error', { detail: '设置加载失败: ' + String(e && e.message || e) }))
  })
})
onUnmounted(() => {
  if (offLog) offLog()
  window.removeEventListener('sshore:error', onErr)
})
</script>

<template>
  <div class="app">
    <nav class="sidebar">
      <button :class="{ active: active === 'forward' }" @click="active = 'forward'">端口转发</button>
      <button :class="{ active: active === 'sftp' }" @click="active = 'sftp'">SFTP</button>
      <button class="settings" @click="settingsVisible = true">⚙ 设置</button>
    </nav>
    <main class="workspace">
      <div v-if="fatal" class="fatal">⚠ 界面错误: {{ fatal }} <button class="fatal-close" @click="dismissFatal">×</button></div>
      <KeepAlive>
        <ForwardView v-if="active === 'forward'" key="forward" />
        <SftpView v-else key="sftp" />
      </KeepAlive>
    </main>
    <SettingsDialog :visible="settingsVisible" @close="settingsVisible = false" />
  </div>
</template>

<style>
.app { display: flex; height: 100vh; }
.sidebar { width: 140px; border-right: 1px solid var(--border); padding-top: 8px; flex-shrink: 0; display: flex; flex-direction: column; }
.sidebar button { display: block; width: 100%; text-align: left; padding: 12px 16px; border: none; background: none; cursor: pointer; color: var(--text-dim); }
.sidebar button:hover { background: var(--surface); color: var(--text); }
.sidebar button.active { background: var(--surface-hover); color: var(--text); border-left: 3px solid var(--accent); }
.sidebar button.settings { margin-top: auto; border-top: 1px solid var(--border); }
.workspace { flex: 1; padding: 12px; overflow: auto; text-align: left; }
.fatal { background: var(--danger); color: var(--on-danger); padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.fatal-close { background: transparent; border: none; color: var(--on-danger); font-size: var(--fs-16); cursor: pointer; line-height: 1; padding: 0 4px; }
</style>
