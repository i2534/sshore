// 拖拽载荷与落点判定：纯函数，输入是坐标/矩形/任务形状，便于单测。
// pane 取值固定为 'local' | 'remote'。
export const DRAG_MIME = 'application/x-sshore'

export function payloadFor(pane, names) {
  return JSON.stringify({ pane, names })
}

export function parsePayload(text) {
  try {
    const v = JSON.parse(text)
    if (!v || (v.pane !== 'local' && v.pane !== 'remote') || !Array.isArray(v.names)) return null
    return { pane: v.pane, names: v.names }
  } catch {
    return null
  }
}

export function hitPane({ x, y }, rects) {
  for (const pane of ['local', 'remote']) {
    const r = rects[pane]
    if (!r) continue
    if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return pane
  }
  return null
}

export function isSubPath(parent, child) {
  if (parent === child) return true
  const p = parent.replace(/\/+$/, '')
  return child.startsWith(p + '/')
}

// item 缺省用 { name, isDir }；跨面板投递在源面板侧调用时 target 为对侧。
export function canDropInto({ sourcePane, targetPane, item, sourceDir, targetDir, connected }) {
  if (targetPane === 'remote' && !connected) return { ok: false, reason: '远程未连接' }
  if (sourcePane === targetPane) {
    if (!item || !item.isDir) return { ok: false, reason: '只能放到目录上' }
    if (!targetDir || !sourceDir) return { ok: false, reason: '目标未知' }
    // 拖到目录自己所在的那一行也属于「移动到自身」：item.name === 目标末段即同一路径。
    const targetInsideItem = isSubPath(sourceDir + '/' + item.name, targetDir)
    const targetIsTheItem = sourceDir.replace(/\/+$/, '') + '/' + item.name === targetDir.replace(/\/+$/, '')
    if (targetInsideItem || targetIsTheItem) {
      return { ok: false, reason: '不能移动到自身或其子目录' }
    }
  }
  return { ok: true, reason: '' }
}

// 最近一次「系统拖入」在 DOM drop 上命中的面板（由 onPaneDrop 记录）。
// 它比 Wails 传来的坐标可靠：坐标单位 / DPI 缩放不可信（真机复现：拖到本地面板正中央
// 仍被判「落点无效」——坐标按屏幕像素给，而面板矩形是页面坐标），而 DOM drop 的目标
// 就是浏览器自己命中的那个面板。TTL 只用来挡住「上一次拖入的陈旧记录」。
export const DROP_PANE_TTL_MS = 3000

// 系统拖入的落点判定：
//   1) 坐标命中就用坐标（正常路径）；
//   2) 否则用最近的 DOM drop 面板记录（须在 TTL 内、取值合法）；
//   3) 都没有才返回 null ⇒ 调用方提示落点无效，绝不猜。
export function pickDropPane({ x, y }, rects, recent, now = Date.now()) {
  const byCoord = hitPane({ x, y }, rects)
  if (byCoord) return byCoord
  if (!recent || (recent.pane !== 'local' && recent.pane !== 'remote')) return null
  if (typeof recent.ts !== 'number' || now - recent.ts > DROP_PANE_TTL_MS) return null
  return recent.pane
}

export function isSystemDrop(types) {
  return Array.isArray(types) && types.includes('Files')
}

// 从**原生 DragEvent** 取本应用的面板互拖载荷。
// 三种必须返回 null 而不是抛错的情况（都在真机上出现过）：
//   1) 系统（资源管理器）拖入：事件里没有任何应用 MIME，getData 返回空串；
//   2) 事件为空/参数形状不匹配：曾经 onPaneDrop(targetPane, { event }) 被模板用原生事件
//      调用，解构出 undefined，读 dataTransfer 抛 TypeError 弹「界面错误」红条；
//   3) 拖拽数据在 dragover 阶段不可读（受保护模式）——此时同样只应放弃，不应崩。
export function payloadFromDragEvent(ev) {
  const dt = ev && ev.dataTransfer
  if (!dt || typeof dt.getData !== 'function') return null
  return parsePayload(dt.getData(DRAG_MIME))
}
