import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './style.css'

const app = createApp(App)
app.use(createPinia())

// Capture any render/component error so the UI never silently goes blank.
// reportError 同时被 errorHandler 与 unhandledrejection 复用：
// 后者兜住事件处理器之外（如 setInterval 回调）逃逸的 promise rejection。
function reportError(err) {
  console.error('[sshore] unhandled:', err)
  window.dispatchEvent(new CustomEvent('sshore:error', { detail: String(err && err.message || err) }))
}
app.config.errorHandler = (err) => reportError(err)
window.addEventListener('unhandledrejection', (e) => reportError(e.reason))

app.mount('#app')
