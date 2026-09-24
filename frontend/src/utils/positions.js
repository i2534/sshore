// 位置弹层的分组 / 截断 / 展开 / 筛选：纯函数，输入是四个位置数据源，输出可直接渲染的分组。
// 顺序固定为 预设 → 磁盘 → 书签 → 最近（spec 决策 34），空组不渲染。
// 「最近」在后端最多存 20 条（internal/config/store.go 的 maxRecent），
// 但下拉里默认只展示 POSITION_SECTION_ROWS 行，其余靠「展开全部」显式取用——
// 这是本轮「位置里的最近太长」的修复点。
export const POSITION_SECTION_ROWS = 8

const norm = (s) => String(s == null ? '' : s).toLowerCase()

function matches(row, filter) {
  if (!filter) return true
  const q = norm(filter)
  return norm(row.label).includes(q) || norm(row.path).includes(q)
}

export function positionGroups({ presets = [], disks = [], bookmarks = [], recents = [], expanded = {}, filter = '' } = {}) {
  const defs = [
    // key 语义必须沿用原 select 的 :key：预设是 name+path+host（后端去重键），
    // 磁盘是 path（盘符天然唯一），书签是 path，最近是 path（同路径已去重）。
    { key: 'preset', label: '预设', rows: presets.map((p) => ({ key: 'p' + p.name + p.path + (p.host || ''), label: p.name, path: p.path })) },
    { key: 'disk', label: '磁盘', rows: disks.map((d) => ({ key: 'd' + d.path, label: d.name, path: d.path })) },
    { key: 'bookmark', label: '书签', rows: bookmarks.map((b) => ({ key: 'b' + b.path, label: b.name || b.path, path: b.path })) },
    { key: 'recent', label: '最近', rows: recents.map((r) => ({ key: 'r' + r.path, label: r.path, path: r.path })) },
  ]
  const out = []
  for (const d of defs) {
    const hit = d.rows.filter((row) => matches(row, filter))
    if (!hit.length) continue
    // expandable 只看筛选后的条数，**不随 expanded 变化**：展开控件因此可以常驻 DOM。
    // 旧写法在模板里用 total > rows.length 当渲染条件，展开后按钮被移除，焦点节点消失，
    // Chromium/WebView2 会给面板子树发 focusout(relatedTarget=null)，弹层被整个收起（W1）。
    out.push({
      key: d.key, label: d.label, total: hit.length, expandable: hit.length > POSITION_SECTION_ROWS,
      rows: expanded[d.key] ? hit : hit.slice(0, POSITION_SECTION_ROWS),
    })
  }
  return out
}
