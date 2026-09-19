// 系统文件拖入的单通道分发：webview 侧监听由 App.vue 注册（Wails JS 版 OnFileDrop），
// 当前视图只登记/摘除自己的处理器。
// 为什么不让每个视图各自注册 OnFileDrop：Wails 的 JS 版只允许注册一次
// （draganddrop.js: flags.registered），谁先注册谁独占；而且它的 dragleave/drop
// 监听负责 preventDefault，摘掉后拖文件到窗口会让 webview 直接导航到该文件。
// 因此监听挂在应用生命周期上，处理器随视图挂摘。
let handler = null

export function setSystemDropHandler(fn) {
  handler = typeof fn === 'function' ? fn : null
}

export function systemDropHandler() {
  return handler
}

export function dispatchSystemDrop(payload) {
  if (!handler || !payload || !Array.isArray(payload.paths) || !payload.paths.length) return false
  handler(payload)
  return true
}
