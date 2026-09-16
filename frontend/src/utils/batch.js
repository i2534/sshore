// 批量任务规划与冲突策略：纯函数，无 DOM、无 wailsjs 依赖。
// Task 14 用它把「选中项 + 方向 + 两端目录」翻译成可执行任务，并在执行前做冲突决策。
export const POLICY_SKIP = 'skip'
export const POLICY_OVERWRITE = 'overwrite'
export const POLICY_RENAME = 'rename'

function joinDir(dir, name) {
  if (!dir || dir === '/') return '/' + name
  return dir.replace(/\/+$/, '') + '/' + name
}

// isDirMap: { [name]: boolean }，由调用方从 items 构造。目录项决定执行时走递归传输。
export function planTasks({ direction, names, sourceDir, targetDir, isDirMap }) {
  const dirs = isDirMap || {}
  return names.map((name) => ({
    name,
    src: joinDir(sourceDir, name),
    dst: joinDir(targetDir, name),
    isDir: !!dirs[name],
  }))
}

// existingNames 必须来自**原始 items**（未经 showAll 过滤），否则隐藏同名项不会被判重。
export function classify(tasks, existingNames) {
  const taken = new Set(existingNames)
  const clean = []
  const conflicts = []
  for (const t of tasks) (taken.has(t.name) ? conflicts : clean).push(t)
  return { clean, conflicts }
}

export function copyName(name, takenNames) {
  const taken = new Set(takenNames)
  const dot = name.lastIndexOf('.')
  const hasExt = dot > 0
  const base = hasExt ? name.slice(0, dot) : name
  const ext = hasExt ? name.slice(dot) : ''
  for (let i = 2; i < 1000; i++) {
    const candidate = base + ' (' + i + ')' + ext
    if (!taken.has(candidate)) return candidate
  }
  return base + ' (copy)' + ext
}

export function applyPolicy(conflicts, policy, takenNames) {
  const taken = [...takenNames]
  if (policy === POLICY_SKIP) return { run: [], skipped: conflicts }
  if (policy === POLICY_OVERWRITE) return { run: conflicts, skipped: [] }
  const run = conflicts.map((t) => {
    const newName = copyName(t.name, taken)
    taken.push(newName)
    return { ...t, name: newName, dst: joinDir(t.dst.replace(/\/[^/]*$/, ''), newName) }
  })
  return { run, skipped: [] }
}

export function needsConfirm({ conflictCount, hiddenSelected }) {
  return conflictCount > 0 || hiddenSelected > 0
}

export function summarize(results) {
  const out = { ok: 0, skipped: 0, failed: 0, failures: [] }
  for (const t of results) {
    if (t.status === '完成') out.ok++
    else if (t.status === '跳过') out.skipped++
    else if (t.status === '失败') { out.failed++; out.failures.push((t.src || t.name) + ' → ' + (t.dst || '') + '：' + (t.reason || '未知原因')) }
  }
  return out
}
