// 拖拽载荷与落点判定：纯函数，输入是坐标/矩形/任务形状，便于单测。
// pane 取值固定为 'local' | 'remote'。
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

export function isSystemDrop(types) {
  return Array.isArray(types) && types.includes('Files')
}
