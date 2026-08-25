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
onMounted(() => {
  EventsOn('log', (evt) => logStore.add(evt))
  window.addEventListener('sshkit:error', onErr)
})
onUnmounted(() => window.removeEventListener('sshkit:error', onErr))
</script>

<template>
  <div class="app">
    <nav class="sidebar">
      <button :class="{ active: active === 'forward' }" @click="active = 'forward'">端口转发</button>
      <button :class="{ active: active === 'sftp' }" @click="active = 'sftp'">SFTP</button>
    </nav>
    <main class="workspace">
      <div v-if="fatal" class="fatal">⚠ 界面错误: {{ fatal }}</div>
      <ForwardView v-if="active === 'forward'" />
      <SftpView v-else />
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
.fatal { background: var(--danger); color: #fff; padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; }
</style>
