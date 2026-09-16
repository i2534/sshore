// 多选模型：纯函数、无 DOM 依赖。
// keys 是"选中集合"，可包含当前被过滤隐藏的项（面板头负责如实显示数量）。
// anchor 是 Shift 区间的起点；改锚点只发生在 single/toggle。
export function createSelection() { return { keys: new Set(), anchor: null } }

export function single(sel, key) {
  sel.keys = new Set([key])
  sel.anchor = key
}

export function toggle(sel, key) {
  const next = new Set(sel.keys)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  sel.keys = next
  sel.anchor = key
}

// visibleKeys：当前可见（showAll ∩ filter，排序后）的名字数组，作为区间唯一真源。
export function rangeTo(sel, visibleKeys, key) {
  const anchor = sel.anchor
  if (!anchor || !visibleKeys.includes(anchor) || !visibleKeys.includes(key)) {
    single(sel, key)
    return
  }
  const i = visibleKeys.indexOf(anchor)
  const j = visibleKeys.indexOf(key)
  const lo = Math.min(i, j)
  const hi = Math.max(i, j)
  sel.keys = new Set(visibleKeys.slice(lo, hi + 1))
}

export function all(sel, visibleKeys) { sel.keys = new Set(visibleKeys) }

export function clear(sel) { sel.keys = new Set(); sel.anchor = null }

export function isSelected(sel, key) { return sel.keys.has(key) }

// 删除/移动完成后调用：把已消失的项移出集合，锚点失效则清空。
export function remove(sel, keys) {
  const next = new Set(sel.keys)
  for (const k of keys) next.delete(k)
  sel.keys = next
  if (sel.anchor && !next.has(sel.anchor)) sel.anchor = null
}

export function selectedNames(sel, items) {
  return items.filter((it) => sel.keys.has(it.name)).map((it) => it.name)
}
