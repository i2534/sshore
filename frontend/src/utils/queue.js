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
