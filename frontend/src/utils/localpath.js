// 本地路径助手：纯字符串处理，不访问文件系统。
// 语义：按"路径自身"的风格工作——路径里出现 '\' 或形如 C:\ / C:/ / \\server\share 时
// 按 Windows 规则，否则按 POSIX 规则；分隔符优先沿用路径里已有的那个，不擅自改写。
// 远程（SFTP）路径一律用 POSIX 语义，见 SftpView.vue 的 posixJoin / posixParentOf。

const WIN_DRIVE = /^[A-Za-z]:[\\/]/
const WIN_DRIVE_BARE = /^[A-Za-z]:$/
const WIN_DRIVE_ROOT = /^[A-Za-z]:[\\/]$/
const UNC_SHARE = /^\\\\[^\\/]+[\\/][^\\/]+$/
const TRAILING_SEP = /[\\/]+$/

export function isWindowsPath(p) {
  const s = p || ''
  return WIN_DRIVE.test(s) || s.startsWith('\\\\')
}

// 盘符裸写 "D:" 归一为 "D:\"：Windows 上 "D:" 表示"D 盘的当前目录"，
// 只有带分隔符的 "D:\" 才是盘根。
export function normalize(p) {
  const s = p || ''
  return WIN_DRIVE_BARE.test(s) ? s + '\\' : s
}

export function sepOf(p) {
  const s = normalize(p)
  if (s.includes('\\')) return '\\'
  if (s.includes('/')) return '/'
  return isWindowsPath(s) ? '\\' : '/'
}

// 盘根（C:\）、POSIX 根（/）、UNC 共享根（\\srv\share）——它们的上级是它们自己。
export function isRoot(p) {
  const s = normalize(p)
  if (!s) return false
  if (s === '/') return true
  if (WIN_DRIVE_ROOT.test(s)) return true
  return UNC_SHARE.test(s.replace(TRAILING_SEP, ''))
}

export function join(base, name) {
  const b = normalize(base)
  if (!b) return name || ''
  const sep = sepOf(b)
  const body = b.replace(TRAILING_SEP, '')
  return body === '' ? sep + name : body + sep + name
}

export function parentOf(p) {
  const s = normalize(p)
  if (!s) return '/'
  if (isRoot(s)) return s
  const trimmed = s.replace(TRAILING_SEP, '')
  const idx = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  if (idx < 0) return '.'
  const head = trimmed.slice(0, idx)
  if (WIN_DRIVE_BARE.test(head)) return head + sepOf(s)
  if (head === '') return sepOf(s)
  return head
}

// 切出 { dir, name }；dir 为空字符串表示"没有目录部分"（相对路径的首层项）。
export function splitPath(p) {
  const s = normalize(p)
  const idx = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  if (idx < 0) return { dir: '', name: s }
  const dir = s.slice(0, idx)
  const name = s.slice(idx + 1)
  if (dir === '') return { dir: sepOf(s), name }
  if (WIN_DRIVE_BARE.test(dir)) return { dir: dir + sepOf(s), name }
  return { dir, name }
}

// 把"根相对路径"（深搜命中项，恒用 '/' 分隔）拼到基目录后，按基目录的风格输出分隔符。
export function joinRel(base, rel) {
  const parts = String(rel || '').split(/[\\/]+/).filter(Boolean)
  if (!parts.length) return normalize(base)
  return join(base, parts.join(sepOf(base)))
}
