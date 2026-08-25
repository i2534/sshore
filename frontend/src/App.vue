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
    <nav class="sidebar">
      <button :class="{ active: active === 'forward' }" @click="active = 'forward'">端口转发</button>
      <button :class="{ active: active === 'sftp' }" @click="active = 'sftp'">SFTP</button>
    </nav>
    <main class="workspace">
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
</style>
