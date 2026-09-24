// 键盘分派与守卫：纯函数，输入是 KeyboardEvent 形状的对象，便于单测。
// 守卫规则（spec §6.1）：焦点在输入控件或浮层/对话框内时，Delete/Ctrl+A/Esc 交还输入控件。
const EDITABLE_TAGS = new Set(['INPUT', 'TEXTAREA', 'SELECT'])

// Shift+↑/↓ = 以 anchor 为起点、cursor 为游标的区间扩展（SftpView 分派给 sel.extend）。
const ARROW_EXTEND = { ArrowUp: 'extend-up', ArrowDown: 'extend-down' }

export function isEditableTarget(el) {
  if (!el) return false
  if (EDITABLE_TAGS.has(el.tagName)) return true
  if (el.isContentEditable) return true
  if (typeof el.closest !== 'function') return false
  // 「焦点在应用自己的控件上」一并纳入守卫（分隔条这类 tabindex=0 的 UI 控件不属于
  // 文件列表，Delete/Ctrl+A/Shift+↑↓ 都不该作用于文件）。
  if (el.closest('[role="separator"]')) return true
  return !!(el.closest('.ui-overlay') || el.closest('[role="dialog"]') || el.closest('.search-overlay'))
}

export function actionFor(ev) {
  if (!ev || isEditableTarget(ev.target)) return null
  if (ev.key === 'Delete') return 'delete'
  if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') return 'select-all'
  if (ev.shiftKey && ARROW_EXTEND[ev.key]) return ARROW_EXTEND[ev.key]
  if (ev.key === 'Escape') return 'escape'
  return null
}
