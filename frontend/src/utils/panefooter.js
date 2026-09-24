// 过滤栏（面板底部）的文案与可用性纯函数：无 DOM 依赖，便于单测。
// 计数语义的唯一来源，FilePane 模板不得再自己拼字符串。

// 过滤框占位文案：代码只按 it.name 匹配（见 FilePane 的 hit()/shown），
// 所以必须写明「按名称」，否则用户会以为它能过滤大小/时间。
export const EMPTY_FILTER_HINT = '按名称过滤…'

export function selectionLabel({ selected, total, hidden = 0 }) {
  const n = Number(selected) || 0
  const m = Number(total) || 0
  if (!n) return m + ' 项'
  const base = '已选 ' + n + ' / ' + m + ' 项'
  return hidden ? base + '（含 ' + Number(hidden) + ' 项被过滤）' : base
}

export function filterClearVisible(text) {
  return String(text || '').length > 0
}
