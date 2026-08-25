<script setup>
import { ref, onMounted } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { useLogStore } from './stores/logs'
import ForwardView from './views/ForwardView.vue'
import SftpView from './views/SftpView.vue'

const active = ref('forward')
const logStore = useLogStore()

onMounted(() => {
  EventsOn('log', (evt) => logStore.add(evt))
})
</script>

<template>
  <div class="app">
    <header class="topbar">sshkit</header>
    <div class="body">
      <nav class="sidebar">
        <button :class="{ active: active === 'forward' }" @click="active = 'forward'">端口转发</button>
        <button :class="{ active: active === 'sftp' }" @click="active = 'sftp'">SFTP</button>
      </nav>
      <main class="workspace">
        <ForwardView v-if="active === 'forward'" />
        <SftpView v-else />
      </main>
    </div>
  </div>
</template>

<style>
body { margin: 0; font-family: system-ui, sans-serif; }
.app { display: flex; flex-direction: column; height: 100vh; }
.topbar { padding: 12px; border-bottom: 1px solid #ddd; font-weight: 600; }
.body { display: flex; flex: 1; }
.sidebar { width: 140px; border-right: 1px solid #ddd; padding-top: 8px; }
.sidebar button { display: block; width: 100%; text-align: left; padding: 10px 16px; border: none; background: none; cursor: pointer; }
.sidebar button.active { background: #eef; }
.workspace { flex: 1; padding: 12px; overflow: auto; }
</style>
