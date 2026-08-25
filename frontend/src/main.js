import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './style.css'

const app = createApp(App)
app.use(createPinia())

// Capture any render/component error so the UI never silently goes blank.
app.config.errorHandler = (err) => {
  console.error('[sshkit] unhandled:', err)
  window.dispatchEvent(new CustomEvent('sshkit:error', { detail: String(err && err.message || err) }))
}

app.mount('#app')
