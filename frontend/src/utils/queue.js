// 传输队列状态映射：纯函数，供 TransferQueue.vue 与批量编排复用。
// 状态取值来自后端/编排写入的中文文案：'处理中' | '完成' | '跳过' | '失败' | '取消'。
//
// Task 14 修复轮 1（评审 I1/I3/I4/M1）：终态、取消结果、进度/进度条的**决策全部在本文件**，
// SftpView.vue 与 TransferQueue.vue 只调用这里导出的纯函数。单测打的因此就是生产路径，
// 不再是「组件里内联一套、测试里断言另一套」。
export function statusClass(status) {
  if (status === '完成') return 'done'
  if (status === '失败') return 'err'
  if (status === '取消') return 'cancel'
  if (status === '跳过') return 'skip'
  return 'doing'
}

export function summarizeQueue(transfers) {
  const out = { ok: 0, skipped: 0, failed: 0, cancelled: 0, running: 0, total: transfers.length }
  for (const t of transfers) {
    if (t.status === '完成') out.ok++
    else if (t.status === '跳过') out.skipped++
    else if (t.status === '失败') out.failed++
    else if (t.status === '取消') out.cancelled++
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

// —— Task 14：传输进度/取消/重试/续传的渲染决策（纯函数）——

// 渲染文案常量：**唯一来源**。组件与用例都从这里取值，改文案必须在用例里一起改。
export const DONE_TEXT = '完成'
export const FAILED_TEXT = '失败'
export const CANCELLED_TEXT = '取消'
export const SKIPPED_TEXT = '跳过'
export const RUNNING_TEXT = '处理中'
// 提交已完成的窄窗口里取消可以如实返回 true，但传输方法最终返回 nil：必须渲染成
// 「取消过晚（已完成）」并保留目标文件，绝不改写为取消（Task 10 评审）。
export const CANCELLED_LATE_TEXT = '取消过晚（已完成）'
export const CANCELLING_TEXT = '取消中…'
// Cancel 明确返回 false（已提交/未知/从未开始/batch 后端恒 false）：诚实告知没取消到，
// 绝不停在「取消中…」的假象里（评审 I3）。
export const CANCEL_FAILED_TEXT = '未能取消'
export const WAITING_FRAME_TEXT = '等待进度上报…'
export const SCANNING_TEXT = '准备中…'
export const UNKNOWN_PROGRESS_TEXT = '未知进度'

// 临时文件中缀：必须与 Go 侧 sftp.PartMarker（internal/sftp/partname.go:12）逐字一致。
// 命名规则：<name>.sshore-sftppart-…（常规）或 .sshore-sftppart-…（退化短名），
// 备份名复用同一中缀，因此中缀判定一条覆盖全部（watch/scan.go 已改用 sftp.PartMarker）。
// 互钉是**双向**的：queue.test.js 读 partname.go 的常量比对，partname_test.go 读本文件的
// PART_MARKER 比对 —— 只改一侧（哪怕连自己那侧的字面量用例一起改）也会在另一侧变红（M2）。
export const PART_MARKER = '.sshore-sftppart-'

// 进度事件名**只在这里出现一次**：Go 侧 app.go 的事件名改名而这里没改，
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

// hasProgressFrame：是否收到过进度帧。默认后端（batch）整批不上报进度（Task 9 评审 I2），
// 因此「没有帧」是常态而不是错误，UI 必须给可用性占位而不是卡在 0%。
export function hasProgressFrame(t) {
  return !!(t && (t.hasProgress || t.phase || t.done !== undefined || t.total !== undefined))
}

// —— 终态决策（评审 I1/I3）——

// outcomeStatus：一次派发的操作结果 → 终态（"终态只由操作结果决定"的唯一落点）。
//  - ok=true（绑定返回 nil = 已提交）⇒ '完成'。**即使 Cancel 返回过 true**（提交已完成的
//    窄窗口）也必须是完成：目标文件已落地，绝不能说成取消/删掉；
//  - 抛错时，只有「确实请求过取消且 Cancel 没失败」才是 '取消'；
//  - Cancel 明确返回 false（未能取消）后，这次失败是**无关失败**，绝不栽给取消（I3）。
export function outcomeStatus({ ok, cancelRequested, cancelFailed } = {}) {
  if (ok) return DONE_TEXT
  if (cancelRequested && !cancelFailed) return CANCELLED_TEXT
  return FAILED_TEXT
}

// applyOutcome：把一次派发的结果写回队列项的**唯一落点**，返回终态。
// SftpView.dispatchTransfer 调用它；单测调用同一个函数，不再是两条路径。
export function applyOutcome(rec, { ok, error } = {}) {
  if (!rec) return ''
  const status = outcomeStatus({ ok, cancelRequested: rec.cancelRequested, cancelFailed: rec.cancelFailed })
  rec.status = status
  rec.pendingCancel = false
  if (status === DONE_TEXT) {
    rec.cancelFailed = false
    rec.partPath = '' // 提交后锚点已不存在，不留一个会误导「清理/续传」的路径
  } else {
    rec.reason = String((error && error.message) || error || '')
  }
  return status
}

// applyCancelResult：把 SftpTransferCancel 的返回值落到队列项上的**唯一落点**（I3）。
//  - true：确实取消到在飞的未提交传输 ⇒ 保持 pendingCancel，等那次传输自己返回终态；
//  - false：已提交/未知/从未开始/batch 后端恒 false ⇒ 立刻转成 cancelFailed，
//    UI 如实显示「未能取消」，绝不停在「取消中…」；这次传输后续若失败也不算取消；
//  - 项已是终态：不覆盖终态，只清掉 in-flight 标记（取消过晚的事实由 cancelRequested 表达）。
// 返回 'cancelling' | 'failed' | 'done'。
export function applyCancelResult(rec, ok) {
  if (!rec) return 'done'
  if (rec.status !== RUNNING_TEXT) {
    rec.pendingCancel = false
    return 'done'
  }
  if (ok) {
    rec.cancelFailed = false
    rec.pendingCancel = true
    return 'cancelling'
  }
  rec.cancelFailed = true
  rec.pendingCancel = false
  return 'failed'
}

// applyBatchCancel：把「取消 rec 所在批次」的意图落到队列上（I2 的唯一落点）。
//  - rec 是 pending（还没派发，永远不会有自己的操作结果）⇒ 它自己立即定「取消」并置
//    batchAborted，绝不派发它，也**绝不**对未注册的 id 调 Cancel（那是假的「取消中」）；
//  - rec 在飞（已派发）⇒ 只置 cancelRequested/pendingCancel，终态等它自己的返回；
//  - rec 之后同批的 pending 项：不再有结果，直接 batchAborted + 「取消」；
//  - rec 之前/别的批次一律不动（别的批次可能在等冲突确认/排队，不能连坐）。
// 返回 { cancelled, inFlight }。
export function applyBatchCancel(transfers, rec) {
  if (!rec) return { cancelled: [], inFlight: false }
  const prefix = String(rec.id || '').split('-')[0] + '-'
  const sameBatch = (o) => o && o !== rec && String(o.id || '').startsWith(prefix)
  const list = Array.isArray(transfers) ? transfers : []
  const idx = list.indexOf(rec)
  const tail = idx >= 0 ? list.slice(idx + 1) : list
  const cancelled = []
  for (const o of tail) {
    if (!sameBatch(o)) continue
    if (o.status !== RUNNING_TEXT || !o.pending) continue
    o.batchAborted = true
    o.status = CANCELLED_TEXT
    o.reason = '已取消，停止后续项'
    cancelled.push(o)
  }
  const clickedPending = rec.status === RUNNING_TEXT && !!rec.pending
  if (clickedPending) {
    rec.batchAborted = true
    rec.status = CANCELLED_TEXT
    rec.reason = '已取消'
    rec.pending = false
    rec.pendingCancel = false
    cancelled.push(rec)
    return { cancelled, inFlight: false }
  }
  rec.cancelRequested = true
  rec.cancelFailed = false
  rec.pendingCancel = true
  return { cancelled, inFlight: true }
}

// shouldDispatch：派发循环的唯一判定。被取消（batchAborted）的项绝不再派发 —— 否则
// dispatchTransfer 开头会把 cancelRequested 清掉并把该项真的发出去（I2）。
export function shouldDispatch(rec) {
  return !!rec && !rec.batchAborted
}

// —— 行内渲染决策（评审 I1）——

// rowNote：行内备注。终态文案只由操作结果 + 取消事实决定。
export function rowNote(t) {
  if (!t) return ''
  if (t.status === DONE_TEXT) return t.cancelRequested ? CANCELLED_LATE_TEXT : ''
  if (t.status === SKIPPED_TEXT) return ''
  return t.reason ? String(t.reason) : ''
}

// rowProgressText：行内进度/进行态文案。**所有状态都会调用它**（TransferQueue.vue 的
// v-if 只看文案是否为空），所以下面每条分支都在生产里真的会跑。
// 终态行返回空：状态列已表达；完成行的「取消过晚」由 rowNote 表达。
export function rowProgressText(t) {
  if (!t || t.status !== RUNNING_TEXT) return ''
  if (t.cancelFailed) return CANCEL_FAILED_TEXT
  if (t.pendingCancel) return CANCELLING_TEXT
  if (t.phase === 'scan') return SCANNING_TEXT
  if (!hasProgressFrame(t)) return WAITING_FRAME_TEXT
  // total<0：扫描被文件/时间预算截断的降级上报（Task 12/13）。这不是失败，
  // 也绝不能渲染成 100%：文件数已知就报已完成文件数，否则报未知进度。
  if (!(Number(t.total) > 0)) {
    if (Number(t.filesDone) > 0) return '已完成 ' + fmtNum(t.filesDone) + ' 个文件'
    return UNKNOWN_PROGRESS_TEXT
  }
  const p = percentOf(t)
  return Math.round(p) + '% · ' + fmtBytes(t.done) + '/' + fmtBytes(t.total)
}

function fmtNum(n) {
  return String(Number(n) || 0)
}

// barModel：进度条模型（无帧/未知总量都绝不假装确定进度）。
//  - 非运行中 / 没收到过帧 ⇒ 不画条（只留「等待进度上报…」占位，绝不画卡在 0% 的假条）；
//  - total<=0（未知/降级/0 字节）⇒ 不定条，绝不假装 0% 或 100%（评审 I1(b)(c)）；
//  - 其余 ⇒ 确定宽度。
export function barModel(t) {
  const hidden = { show: false, indeterminate: false, width: null }
  if (!t || t.status !== RUNNING_TEXT) return hidden
  if (!hasProgressFrame(t)) return hidden
  if (!(Number(t.total) > 0)) return { show: true, indeterminate: true, width: null }
  return { show: true, indeterminate: false, width: percentOf(t).toFixed(1) + '%' }
}

// activeActions：处理中项的动作。legacy 四参面的队列项没有 id（绑定首参是 host，
// 取消表里也没有它），Cancel("") 恒 false，所以绝不能给它取消按钮（Task 10/13 裁决）。
export function activeActions(t) {
  if (!t || t.status !== RUNNING_TEXT) return []
  if (!t.id) return []
  return ['cancel']
}

// failedActions：失败/取消项的动作表。
//  - 无 id 的 legacy 记录（removeSelected / onMoveDrop / 系统拖入 copy）没有可重试的
//    绑定身份：retryItem/resumeItem 都会 early-return，画出来就是死按钮（评审 I4）⇒ 不给动作；
//  - PartPath 为空（例如树预扫描失败，根本没有临时文件）⇒ 只给重试，不给续传/清理。
//  - 目录项无单文件锚点 ⇒ 不给续传（目录级续传是 P3.1）；有 partPath 时仍可清理。
export function failedActions(t) {
  if (!t || (t.status !== FAILED_TEXT && t.status !== CANCELLED_TEXT)) return []
  if (!t.id) return []
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
  if (rec.status !== RUNNING_TEXT) return false
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
