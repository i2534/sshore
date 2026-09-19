<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { EventsOn, OnFileDrop, OnFileDropOff } from '../wailsjs/runtime/runtime'
import { dispatchSystemDrop } from './utils/systemDrop'
import { GetAppInfo } from '../wailsjs/go/main/App'
import { useLogStore } from './stores/logs'
import { useSettingsStore } from './stores/settings'
import { useUpdateStore } from './stores/update'
import { stateLabel, statusMessage } from './utils/update'
import ForwardView from './views/ForwardView.vue'
import SftpView from './views/SftpView.vue'
import SyncView from './views/SyncView.vue'
import SettingsDialog from './components/SettingsDialog.vue'

const active = ref('forward')
const settingsVisible = ref(false)
const logStore = useLogStore()
const settingsStore = useSettingsStore()
const updateStore = useUpdateStore()
const fatal = ref('')

// 角标是否显示只由 store 派生值 badgeVisible 决定（available/ready）；
// 文案复用 utils/update.js，与设置页「更新」区共用同一来源，不另造状态字符串。
const badgeText = computed(() => statusMessage(updateStore.info))
const versionLabel = computed(() => stateLabel(updateStore.info))

function openSettings() {
  settingsVisible.value = true
  updateStore.acknowledge()
}

function onErr(evt) { fatal.value = evt.detail }
function dismissFatal() { fatal.value = '' }
let offLog = null
// 系统文件拖入：必须由前端调用 Wails 的 **JS 版** OnFileDrop 才会注册 webview 的
// dragover/dragleave/drop 监听（Go 版 runtime.OnFileDrop 只订阅 wails:file-drop 事件，
// 单用它收不到任何拖入）。挂载点放应用级：监听常驻，才不会有「切到别的标签后拖入文件
// 被 webview 直接导航走」的窗口；useDropTarget=false 表示不依赖 --wails-drop-target 样式，
// 由各视图自己按落点坐标判定目标面板。
// 注意：Linux/WebKitGTK 上 Wails 拿不到真实路径（CanResolveFilePaths=false），外部拖入
// 只有 Windows/WebView2 生效；监听仍在，至少保证 drop 被 preventDefault 不会导航。
onMounted(() => {
  OnFileDrop((x, y, paths) => dispatchSystemDrop({ x, y, paths }), false)
  // EventsOn 返回退订函数：不保存并在卸载时调用的话，dev 模式 HMR 重挂载会
  // 叠加注册，导致每条日志重复入 store（M6b）。
  offLog = EventsOn('log', (evt) => logStore.add(evt))
  window.addEventListener('sshore:error', onErr)
  // HTML 标题与 Wails 窗口标题保持一致：均为「SSHore <版本>」。index.html 里
  // 只留静态兜底 "SSHore"，版本需运行时从后端取；失败则保持兜底标题。
  GetAppInfo()
    .then((info) => {
      if (!info || !info.name) return
      document.title = info.version ? `${info.name} ${info.version}` : info.name
    })
    .catch(() => {})
  // 加载并应用用户设置（主题/字号/字体/启动自动连接）。失败不阻断界面，
  // 仅借全局错误通道提示，避免设置读取失败导致整个应用挂掉。
  settingsStore.load().catch((e) => {
    console.error('[sshore] settings load failed:', e)
    window.dispatchEvent(new CustomEvent('sshore:error', { detail: '设置加载失败: ' + String(e && e.message || e) }))
  })
  // 更新提示的启动接线必须在 App.vue：UpdateSection 位于 SettingsDialog 的 v-if 子树内，
  // 其 onMounted 只有打开设置才执行，不能依赖它取快照。先订阅再取快照（hydrate 内部即
  // ensureListening）；hydrate 失败只落 store.error，这里再 .catch 兜底，避免未处理 rejection。
  updateStore.ensureListening()
  updateStore.hydrate().catch((e) => {
    console.error('[sshore] update hydrate failed:', e)
  })
})
onUnmounted(() => {
  if (offLog) offLog()
  OnFileDropOff()
  // 退订走 store 保存的退订函数（stopListening），不直接 EventsOff 事件名：
  // 后者会清掉同名事件的全部监听器，第二个订阅者会被静默误退订。
  updateStore.stopListening()
  window.removeEventListener('sshore:error', onErr)
})
</script>

<template>
  <div class="app">
    <nav class="sidebar">
      <button :class="{ active: active === 'forward' }" @click="active = 'forward'">端口转发</button>
      <button :class="{ active: active === 'sync' }" @click="active = 'sync'">文件同步</button>
      <button :class="{ active: active === 'sftp' }" @click="active = 'sftp'">SFTP</button>
      <button class="settings" :title="versionLabel" @click="openSettings">
        ⚙ 设置
        <span v-if="updateStore.badgeVisible" class="badge" :aria-label="badgeText" :title="badgeText" />
      </button>
    </nav>
    <main class="workspace">
      <div v-if="fatal" class="fatal">⚠ 界面错误: {{ fatal }} <button class="fatal-close" @click="dismissFatal">×</button></div>
      <KeepAlive>
        <ForwardView v-if="active === 'forward'" key="forward" />
        <SftpView v-else-if="active === 'sftp'" key="sftp" />
        <SyncView v-else key="sync" />
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
.sidebar button.settings { position: relative; margin-top: auto; border-top: 1px solid var(--border); }
.sidebar .badge { position: absolute; top: 10px; right: 14px; width: 8px; height: 8px; border-radius: 50%; background: var(--accent); }
.workspace { flex: 1; padding: 12px; overflow: auto; text-align: left; }
.fatal { background: var(--danger); color: var(--on-danger); padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.fatal-close { background: transparent; border: none; color: var(--on-danger); font-size: var(--fs-16); cursor: pointer; line-height: 1; padding: 0 4px; }
</style>
