// 键盘分派与守卫：纯函数，输入是 KeyboardEvent 形状的对象，便于单测。
// 守卫规则（spec §6.1）：焦点在输入控件或浮层/对话框内时，Delete/Ctrl+A/Esc 交还输入控件。
const EDITABLE_TAGS = new Set(['INPUT', 'TEXTAREA', 'SELECT'])

export function isEditableTarget(el) {
  if (!el) return false
  if (EDITABLE_TAGS.has(el.tagName)) return true
  if (el.isContentEditable) return true
  if (typeof el.closest !== 'function') return false
  return !!(el.closest('.ui-overlay') || el.closest('[role="dialog"]') || el.closest('.search-overlay'))
}

export function actionFor(ev) {
  if (!ev || isEditableTarget(ev.target)) return null
  if (ev.key === 'Delete') return 'delete'
  if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') return 'select-all'
  if (ev.key === 'Escape') return 'escape'
  return null
}
