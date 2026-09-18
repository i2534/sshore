// 传输队列状态映射：纯函数，供 TransferQueue.vue 与批量编排复用。
// 状态取值来自后端/编排写入的中文文案：'处理中' | '完成' | '跳过' | '失败'。
export function statusClass(status) {
  if (status === '完成') return 'done'
  if (status === '失败') return 'err'
  if (status === '跳过') return 'skip'
  return 'doing'
}

export function summarizeQueue(transfers) {
  const out = { ok: 0, skipped: 0, failed: 0, running: 0, total: transfers.length }
  for (const t of transfers) {
    if (t.status === '完成') out.ok++
    else if (t.status === '跳过') out.skipped++
    else if (t.status === '失败') out.failed++
    else out.running++
  }
  return out
}

export function directionArrow(direction) {
  if (direction === 'download') return '⬇'
  if (direction === 'upload') return '⬆'
  if (direction === 'copy') return '📋'
  if (direction === 'move') return '➡'
  return '•'
}

export function failureText(transfer) {
  return `${transfer.src || transfer.name} → ${transfer.dst || ''}：${transfer.reason || '未知原因'}`
}

// —— Task 14：传输进度/取消/重试/续传的渲染决策（纯函数，供 TransferQueue.vue 与 SftpView.vue 复用）——

// 临时文件中缀：必须与 Go 侧 sftp.PartMarker 字面量一致（internal/sftp/partname.go:12）。
// 命名规则：<name>.sshore-sftppart-…（常规）或 .sshore-sftppart-…（退化短名），
// 备份名复用同一中缀，因此中缀判定一条覆盖全部（watch/scan.go:35 同理）。
export const PART_MARKER = '.sshore-sftppart-'

// 进度事件名**只在这里出现一次**：Go 侧 app.go:478 的事件名改名而这里没改，
// 订阅会静默失效（不报错、只是永远没有进度），所以 queue.test.js 钉死字面量。
export const TRANSFER_PROGRESS_EVENT = 'sftp:transfer-progress'

export function isInternalTempName(name) {
  return String(name || '').includes(PART_MARKER)
}

// percentOf：total<0（未知/降级扫描）返回 null ⇒ 不定进度条；
// total==0 视为 100%（0 字节文件完成即 100%，spec §8）；其余 clamp 到 0..100（乱序帧回退钳 0）。
export function percentOf(t) {
  const total = Number(t && t.total)
  const done = Number((t && t.done) || 0)
  // 只接受数字：缺失/字符串一律 null（Number(null)===0 会被误当成 0 字节文件 → 假 100%）。
  if (typeof (t && t.total) !== 'number' || !Number.isFinite(total) || total < 0) return null
  if (total === 0) return 100
  return Math.max(0, Math.min(100, (done / total) * 100))
}

// speedOf：以队列项的 startedAt（本地时钟）为起点算平均速度；时间差<=0 时返回 0，
// 绝不产生 Infinity/NaN（后台 batch 后端永不来帧，此时 done 为 undefined）。
export function speedOf(t, now) {
  const started = Number((t && t.startedAt) || 0)
  const secs = (Number(now) - started) / 1000
  if (!(secs > 0)) return 0
  return Number((t && t.done) || 0) / secs
}

export function etaOf(t, now) {
  const p = percentOf(t)
  if (p === null || p >= 100) return null
  const sp = speedOf(t, now)
  if (!(sp > 0)) return null
  const remain = Number(t.total) - Number(t.done || 0)
  return Math.max(0, Math.round(remain / sp))
}

export function fmtSpeed(bytesPerSec) {
  if (!(bytesPerSec > 0)) return '--'
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  let v = bytesPerSec, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + units[i]
}

function fmtBytes(bytes) {
  if (!(bytes > 0)) return '0B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + units[i]
}

// hasFrame：是否收到过进度帧。默认后端（batch）整批不上报进度（Task 9 评审 I2），
// 因此"没有帧"是常态而不是错误，UI 必须给可用性占位而不是卡在 0%。
function hasFrame(t) {
  return !!(t && (t.hasProgress || t.phase || t.done !== undefined || t.total !== undefined))
}

// progressText：单行的进度/结果文案。终态（完成/失败/取消）**只由操作结果决定**：
// SftpTransferCancel 在提交已完成的窄窗口里可以如实返回 true，而传输方法最终返回 nil，
// 此时必须渲染为已完成（"取消过晚"）、保留安装好的目标文件，绝不改写为取消（Task 10 评审）。
export function progressText(t) {
  if (!t) return ''
  const status = t.status
  if (status === '完成') return t.cancelRequested ? '取消过晚（已完成）' : '完成'
  if (status === '失败' || status === '取消') return status === '失败' ? '失败' : '已取消'
  if (status === '跳过') return '跳过'
  if (t.phase === 'scan') return '准备中…'
  if (!hasFrame(t)) return '等待进度上报…'
  // total<0：扫描被文件/时间预算截断的降级上报（Task 12/13）。这不是失败，
  // 也绝不能渲染成 100%：文件数已知就报已完成文件数，否则报未知进度。
  if (!(Number(t.total) > 0)) {
    if (Number(t.filesDone) > 0) return '已完成 ' + fmtNum(t.filesDone) + ' 个文件'
    return '未知进度'
  }
  const p = percentOf(t)
  return Math.round(p) + '% · ' + fmtBytes(t.done) + '/' + fmtBytes(t.total)
}

function fmtNum(n) {
  return String(Number(n) || 0)
}

// activeActions：处理中项的动作。legacy 四参面的队列项没有 id（绑定首参是 host，
// 取消表里也没有它），Cancel("") 恒 false，所以绝不能给它取消按钮（Task 10/13 裁决）。
export function activeActions(t) {
  if (!t || t.status !== '处理中') return []
  if (!t.id) return []
  return ['cancel']
}

// failedActions：失败/取消项的动作表。
//  - PartPath 为空（例如树预扫描失败，根本没有临时文件）⇒ 只给重试，不给续传/清理。
//  - 目录项无单文件锚点 ⇒ 不给续传（目录级续传是 P3.1）；有 partPath 时仍可清理。
export function failedActions(t) {
  if (!t || (t.status !== '失败' && t.status !== '取消')) return []
  if (!t.partPath) return ['retry']
  const out = ['retry']
  if (!t.isDir) out.push('resume')
  // 清理取决于 partPath 而不是 isDir：目录项没有单文件锚点，自然不会进这里。
  out.push('clean')
  return out
}

// applyProgress：把一帧 sftp:transfer-progress 载荷按 id 落到队列项上，返回是否命中。
//  - 未知 id / 空 frame 忽略；
//  - 终态项不再被迟到帧改写（帧可能晚于绑定返回）；
//  - 空 partPath 不覆盖已保存的锚点（续传/清理都靠它）。
export function applyProgress(transfers, frame) {
  if (!frame || !frame.id || !Array.isArray(transfers)) return false
  const rec = transfers.find((x) => x && x.id === frame.id)
  if (!rec) return false
  if (rec.status !== '处理中') return false
  if (frame.done !== undefined) rec.done = frame.done
  if (frame.total !== undefined) rec.total = frame.total
  if (frame.filesDone !== undefined) rec.filesDone = frame.filesDone
  if (frame.filesTotal !== undefined) rec.filesTotal = frame.filesTotal
  if (frame.phase !== undefined) rec.phase = frame.phase
  if (frame.name !== undefined) rec.progressName = frame.name
  if (frame.partPath) rec.partPath = frame.partPath
  rec.hasProgress = true
  return true
}
