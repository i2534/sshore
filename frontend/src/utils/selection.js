// 多选模型：纯函数、无 DOM 依赖。
// keys 是"选中集合"，可包含当前被过滤隐藏的项（面板头负责如实显示数量）。
// anchor 是 Shift 区间的起点；cursor 是 Shift+↑/↓ 的游标。改锚点只发生在 single/toggle。
export function createSelection() { return { keys: new Set(), anchor: null, cursor: null } }

export function single(sel, key) {
  sel.keys = new Set([key])
  sel.anchor = key
  sel.cursor = key
}

export function toggle(sel, key) {
  const next = new Set(sel.keys)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  sel.keys = next
  sel.anchor = key
  sel.cursor = key
}

// visibleKeys：当前可见（showAll ∩ filter，排序后）的名字数组，作为区间唯一真源。
// rangeTo 只更新 keys 与游标，anchor 是区间起点、必须保持不变（Shift+↑↓ 依赖这一点）。
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
  sel.cursor = key
}

// invert：只对可见项取反；被过滤隐藏的已选项保留（反选不该吞掉用户先前的选择）。
// 注意这与 all() 有意不同——all() 是「把选中集替换为全部可见项」，隐藏的已选项会被丢掉。
export function invert(sel, visibleKeys) {
  const next = new Set()
  for (const k of visibleKeys) if (!sel.keys.has(k)) next.add(k)
  for (const k of sel.keys) if (!visibleKeys.includes(k)) next.add(k)
  sel.keys = next
  sel.anchor = null
  sel.cursor = null
}

// extend：Shift+↑/↓ 的区间扩展。anchor 固定为区间起点、cursor 移动；越界夹取。
// 游标缺失/失效时从 anchor 起步，两者都没有则落到列表首/末项。
export function extend(sel, visibleKeys, delta) {
  if (!visibleKeys.length) return
  const from = (sel.cursor && visibleKeys.includes(sel.cursor)) ? sel.cursor
    : (sel.anchor && visibleKeys.includes(sel.anchor)) ? sel.anchor : null
  const idx = from ? visibleKeys.indexOf(from) : -1
  const nextIdx = idx < 0
    ? (delta > 0 ? 0 : visibleKeys.length - 1)
    : Math.min(visibleKeys.length - 1, Math.max(0, idx + delta))
  const key = visibleKeys[nextIdx]
  if (!sel.anchor || !visibleKeys.includes(sel.anchor)) sel.anchor = key
  const i = visibleKeys.indexOf(sel.anchor)
  sel.keys = new Set(visibleKeys.slice(Math.min(i, nextIdx), Math.max(i, nextIdx) + 1))
  sel.cursor = key
}

// all：把选中集替换成全部可见项，并让锚点/游标有确定含义——
// 锚点=首项、游标=末项，这样 Ctrl+A 之后再按 Shift+↑ 是从末尾收窄，而不是塌成一两项。
export function all(sel, visibleKeys) {
  sel.keys = new Set(visibleKeys)
  sel.anchor = visibleKeys.length ? visibleKeys[0] : null
  sel.cursor = visibleKeys.length ? visibleKeys[visibleKeys.length - 1] : null
}

export function clear(sel) { sel.keys = new Set(); sel.anchor = null; sel.cursor = null }

export function isSelected(sel, key) { return sel.keys.has(key) }

// 删除/移动完成后调用：把已消失的项移出集合，锚点/游标失效则清空。
export function remove(sel, keys) {
  const next = new Set(sel.keys)
  for (const k of keys) next.delete(k)
  sel.keys = next
  if (sel.anchor && !next.has(sel.anchor)) sel.anchor = null
  if (sel.cursor && !next.has(sel.cursor)) sel.cursor = null
}

export function selectedNames(sel, items) {
  return items.filter((it) => sel.keys.has(it.name)).map((it) => it.name)
}
