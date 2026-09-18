import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { statusClass, summarizeQueue, directionArrow, failureText,
  percentOf, speedOf, etaOf, fmtSpeed, isInternalTempName, activeActions, failedActions,
  applyProgress, TRANSFER_PROGRESS_EVENT, PART_MARKER,
  outcomeStatus, applyOutcome, applyCancelResult, applyBatchCancel, shouldDispatch,
  rowNote, rowProgressText, barModel,
  CANCELLED_LATE_TEXT, CANCELLING_TEXT, CANCEL_FAILED_TEXT, WAITING_FRAME_TEXT } from './queue'

const src = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8')
const sftpView = src('../views/SftpView.vue')
const transferQueue = src('../components/TransferQueue.vue')
const queueJs = src('./queue.js')

describe('queue 辅助', () => {
  it('状态映射：跳过/取消都是独立态，不能落进处理中', () => {
    expect(statusClass('完成')).toBe('done')
    expect(statusClass('失败')).toBe('err')
    expect(statusClass('跳过')).toBe('skip')
    expect(statusClass('取消')).toBe('cancel')
    expect(statusClass('处理中')).toBe('doing')
    expect(statusClass('')).toBe('doing')
  })

  it('汇总统计五类条数（取消独立计数，整批取消后不再显示"进行中 N"）', () => {
    const list = [{ status: '完成' }, { status: '完成' }, { status: '跳过' }, { status: '失败' }, { status: '取消' }, { status: '处理中' }]
    expect(summarizeQueue(list)).toEqual({ ok: 2, skipped: 1, failed: 1, cancelled: 1, running: 1, total: 6 })
  })

  it('方向箭头覆盖四类', () => {
    expect(directionArrow('download')).toBe('⬇')
    expect(directionArrow('upload')).toBe('⬆')
    expect(directionArrow('copy')).toBe('📋')
    expect(directionArrow('move')).toBe('➡')
  })
})

describe('percentOf', () => {
  it('total 未知（<0）返回 null（不定进度）', () => {
    expect(percentOf({ done: 5, total: -1 })).toBe(null)
  })
  it('0 字节文件完成即 100%', () => {
    expect(percentOf({ done: 0, total: 0 })).toBe(100)
  })
  it('超过 100% 时钳制', () => {
    expect(percentOf({ done: 120, total: 100 })).toBe(100)
  })
  it('乱序事件导致回退也钳到 0', () => {
    expect(percentOf({ done: -5, total: 100 })).toBe(0)
  })
  it('缺失/非法 total 一律 null，不抛 NaN', () => {
    expect(percentOf({ done: 1 })).toBe(null)
    expect(percentOf({ done: 1, total: 'x' })).toBe(null)
    expect(percentOf(null)).toBe(null)
  })
})

describe('speedOf / etaOf', () => {
  it('有起点与时间差时给出速度（字节/秒）', () => {
    const t = { done: 1000, total: 2000, startedAt: 0 }
    expect(speedOf(t, 2000)).toBe(500) // 1000B / 2s
  })
  it('起点为 0 或时间差为 0 时速度为 0（不产生 Infinity/NaN）', () => {
    expect(speedOf({ done: 10, total: 10, startedAt: 0 }, 0)).toBe(0)
  })
  it('未知总量时 eta 为 null', () => {
    expect(etaOf({ done: 10, total: -1, startedAt: 0 }, 1000)).toBe(null)
  })
  it('已知总量但速度为 0 时 eta 为 null；正常时给出剩余秒数', () => {
    expect(etaOf({ done: 0, total: 100, startedAt: 0 }, 1000)).toBe(null)
    expect(etaOf({ done: 500, total: 1000, startedAt: 0 }, 1000)).toBe(1)
  })
  it('fmtSpeed：0/非法输入给占位符，不做 0 除', () => {
    expect(fmtSpeed(0)).toBe('--')
    expect(fmtSpeed(NaN)).toBe('--')
    expect(fmtSpeed(500)).toBe('500 B/s')
  })
})

describe('isInternalTempName / PART_MARKER 互钉', () => {
  it('认出常规名与退化短名（中缀判定）', () => {
    expect(isInternalTempName('a.txt.sshore-sftppart-t1-ab12')).toBe(true)
    expect(isInternalTempName('.sshore-sftppart-t1-ab12')).toBe(true)
  })
  it('不误判普通文件', () => {
    expect(isInternalTempName('a.txt')).toBe(false)
    expect(isInternalTempName('a.txt.bak')).toBe(false)
    expect(isInternalTempName('')).toBe(false)
  })
  it('字面量本身钉死', () => {
    expect(PART_MARKER).toBe('.sshore-sftppart-')
  })
  it('与 Go 侧 sftp.PartMarker **双向**互钉：读 Go 源常量比对（只改 Go 一侧这里就红）', () => {
    const go = src('../../../internal/sftp/partname.go')
    const m = go.match(/const PartMarker = "([^"]+)"/)
    expect(m, 'internal/sftp/partname.go 里找不到 const PartMarker = "…"').toBeTruthy()
    expect(PART_MARKER).toBe(m[1])
  })
})

describe('行内渲染：rowNote / rowProgressText / barModel（生产路径，TransferQueue.vue 直接调用）', () => {
  it('事件名钉死：改名后前端订阅会静默失效，所以必须断言字面量', () => {
    expect(TRANSFER_PROGRESS_EVENT).toBe('sftp:transfer-progress')
  })
  it('SftpView 必须用该常量订阅（拷字面量改名会在这里断）', () => {
    expect(sftpView).toContain('EventsOn(TRANSFER_PROGRESS_EVENT')
  })
  it('无帧（降级 batch 后端从不发帧）给可用性占位，且绝不画进度条', () => {
    const rec = { status: '处理中', startedAt: 1, partPath: '/tmp/a.txt.sshore-sftppart-t1-0' }
    expect(rowProgressText(rec)).toBe(WAITING_FRAME_TEXT)
    expect(barModel(rec)).toEqual({ show: false, indeterminate: false, width: null })
    expect(rowProgressText({ status: '处理中' })).toBe(WAITING_FRAME_TEXT)
    expect(barModel({ status: '处理中' }).show).toBe(false)
  })
  it('取消请求在飞时显示取消中（终态仍由操作结果决定）', () => {
    expect(rowProgressText({ status: '处理中', pendingCancel: true })).toBe(CANCELLING_TEXT)
  })
  it('scan 相显示准备中', () => {
    expect(rowProgressText({ status: '处理中', phase: 'scan', done: 3, total: 3 })).toBe('准备中…')
  })
  it('total<0（预算截断的降级扫描）显示未知/部分进度，绝不显示 100% 或以失败渲染', () => {
    const partial = { status: '处理中', done: 5, total: -1, filesDone: 2, filesTotal: -1, hasProgress: true }
    expect(rowProgressText(partial)).toBe('已完成 2 个文件')
    expect(rowProgressText({ status: '处理中', done: 5, total: -1, hasProgress: true })).toBe('未知进度')
    expect(rowProgressText(partial)).not.toContain('%')
    expect(rowProgressText(partial)).not.toBe('失败')
    expect(rowNote(partial)).toBe('')
    expect(barModel(partial)).toEqual({ show: true, indeterminate: true, width: null })
  })
  it('total 为 0（0 字节）不显示百分比，进度条也是不定条', () => {
    expect(rowProgressText({ status: '处理中', done: 0, total: 0, hasProgress: true })).not.toContain('%')
    expect(barModel({ status: '处理中', done: 0, total: 0, hasProgress: true })).toEqual({ show: true, indeterminate: true, width: null })
  })
  it('有帧且总量已知时给出百分比与字节、确定宽度', () => {
    expect(rowProgressText({ status: '处理中', done: 512, total: 1024 })).toBe('50% · 512B/1.0KB')
    expect(barModel({ status: '处理中', done: 512, total: 1024 })).toEqual({ show: true, indeterminate: false, width: '50.0%' })
  })
  it('失败行渲染原因是 rowNote 的职责，与进度文案解耦', () => {
    const rec = { status: '失败', reason: '已传输 2 个文件/1KB，剩余文件数未知', partPath: '', id: 't1-0' }
    expect(rowProgressText(rec)).toBe('')
    expect(rowNote(rec)).toBe('已传输 2 个文件/1KB，剩余文件数未知')
    expect(failedActions(rec)).toEqual(['retry'])
  })
  it('完成 = 操作结果，不因取消请求改写（取消可能发生在提交之后）', () => {
    expect(rowNote({ status: '完成', cancelRequested: true })).toBe(CANCELLED_LATE_TEXT)
    expect(rowNote({ status: '完成' })).toBe('')
    expect(rowProgressText({ status: '完成', cancelRequested: true })).toBe('')
    expect(barModel({ status: '完成', cancelRequested: true }).show).toBe(false)
  })
})

describe('终态决策 outcomeStatus / applyOutcome（SftpView.dispatchTransfer 唯一落点）', () => {
  it('Cancel 返回 true 但操作结果 nil：必须「完成」，绝不「取消」', () => {
    expect(outcomeStatus({ ok: true, cancelRequested: true, cancelFailed: false })).toBe('完成')
    const rec = { id: 't1-0', status: '处理中', cancelRequested: true, cancelFailed: false, partPath: '/l/a.part' }
    expect(applyOutcome(rec, { ok: true })).toBe('完成')
    expect(rec.status).toBe('完成')
    expect(rec.partPath).toBe('') // 提交后锚点失效，不再诱导清理/续传
    expect(rowNote(rec)).toBe(CANCELLED_LATE_TEXT)
    expect(rowNote(rec)).not.toBe('已取消')
  })
  it('真取消到 + 操作抛错：渲染「取消」', () => {
    const rec = { id: 't1-0', status: '处理中', cancelRequested: true, cancelFailed: false }
    expect(applyOutcome(rec, { ok: false, error: new Error('context canceled') })).toBe('取消')
    expect(rec.reason).toBe('context canceled')
  })
  it('取消未遂后操作抛错：渲染「失败」，绝不栽给取消（I3）', () => {
    const rec = { id: 't1-0', status: '处理中', cancelRequested: true, cancelFailed: true }
    expect(applyOutcome(rec, { ok: false, error: new Error('disk full') })).toBe('失败')
    expect(rec.reason).toBe('disk full')
    expect(rowNote(rec)).toBe('disk full')
  })
  it('未请求过取消的抛错就是失败', () => {
    expect(applyOutcome({ status: '处理中' }, { ok: false, error: 'boom' })).toBe('失败')
  })
})

describe('取消结果 applyCancelResult（SftpTransferCancel 返回值唯一落点，I3）', () => {
  it('返回 false：转「未能取消」，不再显示「取消中…」', () => {
    const rec = { id: 't1-0', status: '处理中', cancelRequested: true, pendingCancel: true }
    expect(applyCancelResult(rec, false)).toBe('failed')
    expect(rec.cancelFailed).toBe(true)
    expect(rec.pendingCancel).toBe(false)
    expect(rowProgressText(rec)).toBe(CANCEL_FAILED_TEXT)
    expect(rowProgressText(rec)).not.toBe(CANCELLING_TEXT)
  })
  it('返回 true：保持「取消中…」，等该项自己的返回', () => {
    const rec = { id: 't1-0', status: '处理中', cancelRequested: true }
    expect(applyCancelResult(rec, true)).toBe('cancelling')
    expect(rec.pendingCancel).toBe(true)
    expect(rec.cancelFailed).toBe(false)
    expect(rowProgressText(rec)).toBe(CANCELLING_TEXT)
  })
  it('项已是终态时不覆盖终态（取消晚于提交的竞态）', () => {
    const rec = { id: 't1-0', status: '完成', cancelRequested: true }
    expect(applyCancelResult(rec, false)).toBe('done')
    expect(rec.status).toBe('完成')
    expect(rec.pendingCancel).toBe(false)
  })
})

describe('批量取消落点 applyBatchCancel / shouldDispatch（I2）', () => {
  it('一个在飞 + 点它后面 pending 行：被点行自己立即取消且不再派发，在飞行不动', () => {
    const inflight = { id: 't1-0', status: '处理中', pending: false }
    const clicked = { id: 't1-1', status: '处理中', pending: true }
    const later = { id: 't1-2', status: '处理中', pending: true }
    const other = { id: 't2-0', status: '处理中', pending: true }
    const list = [inflight, clicked, later, other]
    const plan = applyBatchCancel(list, clicked)
    expect(plan.inFlight).toBe(false)
    expect(plan.cancelled).toEqual([later, clicked])
    expect(clicked.status).toBe('取消')
    expect(clicked.batchAborted).toBe(true)
    expect(clicked.pending).toBe(false)
    expect(shouldDispatch(clicked)).toBe(false) // 派发循环据此 break，绝不派发它
    expect(later.status).toBe('取消')
    expect(later.batchAborted).toBe(true)
    expect(inflight.status).toBe('处理中')
    expect(inflight.cancelRequested).toBeUndefined()
    expect(inflight.pendingCancel).toBeUndefined()
    expect(other.status).toBe('处理中')
    expect(other.batchAborted).toBeUndefined()
  })
  it('点在飞行：请求取消在飞项 + 同批后续 pending 立即取消', () => {
    const inflight = { id: 't1-0', status: '处理中', pending: false }
    const later = { id: 't1-1', status: '处理中', pending: true }
    const list = [inflight, later]
    const plan = applyBatchCancel(list, inflight)
    expect(plan.inFlight).toBe(true)
    expect(inflight.cancelRequested).toBe(true)
    expect(inflight.pendingCancel).toBe(true)
    expect(inflight.status).toBe('处理中') // 终态等它自己的返回
    expect(later.status).toBe('取消')
    expect(later.batchAborted).toBe(true)
  })
  it('只按同批 id 前缀处理，别的批次不受连坐', () => {
    const clicked = { id: 't7-0', status: '处理中', pending: true }
    const sameBatch = { id: 't7-1', status: '处理中', pending: true }
    const otherBatch = { id: 't8-0', status: '处理中', pending: true }
    applyBatchCancel([clicked, sameBatch, otherBatch], clicked)
    expect(sameBatch.status).toBe('取消')
    expect(otherBatch.status).toBe('处理中')
    expect(otherBatch.batchAborted).toBeUndefined()
  })
  it('shouldDispatch：未取消项可派发，取消项不可', () => {
    expect(shouldDispatch({ status: '处理中' })).toBe(true)
    expect(shouldDispatch({ status: '处理中', batchAborted: true })).toBe(false)
    expect(shouldDispatch(null)).toBe(false)
  })
  it('按 runBatch 的循环形状模拟：点后面的 pending 行后，它绝不会被派发', () => {
    const inflight = { id: 't1-0', status: '处理中', pending: false }
    const clicked = { id: 't1-1', status: '处理中', pending: true }
    const later = { id: 't1-2', status: '处理中', pending: true }
    const batch = [inflight, clicked, later]
    applyBatchCancel(batch, clicked)
    const dispatched = []
    for (const rec of batch) {
      if (!shouldDispatch(rec)) break
      dispatched.push(rec.id)
    }
    expect(dispatched).toEqual(['t1-0']) // 只有已在飞的那一项被派发过；被点的行没有
    expect(clicked.status).toBe('取消')
  })
})

describe('行内动作表', () => {
  it('legacy 四参面没有 id：绝不提供取消（Cancel("") 恒 false）', () => {
    expect(activeActions({ status: '处理中', id: '' })).toEqual([])
    expect(activeActions({ status: '处理中' })).toEqual([])
  })
  it('有 id 的处理中项提供取消', () => {
    expect(activeActions({ status: '处理中', id: 't1-0' })).toEqual(['cancel'])
  })
  it('失败项无 id（removeSelected/onMoveDrop/系统拖入 copy）：一个死按钮都不画（I4）', () => {
    expect(failedActions({ status: '失败', partPath: '' })).toEqual([])
    expect(failedActions({ status: '失败' })).toEqual([])
    expect(failedActions({ status: '失败', partPath: '/l/a.part' })).toEqual([])
    expect(failedActions({ status: '取消', partPath: '/l/a.part' })).toEqual([])
  })
  it('有 id 的失败项无 partPath（如扫描相失败）只给重试', () => {
    expect(failedActions({ status: '失败', id: 't1-0', partPath: '' })).toEqual(['retry'])
    expect(failedActions({ status: '失败', id: 't1-0' })).toEqual(['retry'])
  })
  it('失败/取消项有 id 与 partPath 给重试+续传+清理', () => {
    expect(failedActions({ status: '失败', id: 't1-0', partPath: '/l/a.part' })).toEqual(['retry', 'resume', 'clean'])
    expect(failedActions({ status: '取消', id: 't1-1', partPath: '/r/a.part' })).toEqual(['retry', 'resume', 'clean'])
  })
  it('目录项不提供续传（目录级续传是 P3.1），但有 partPath 仍可清理', () => {
    expect(failedActions({ status: '失败', id: 't1-0', isDir: true, partPath: '/l/a.part' })).toEqual(['retry', 'clean'])
    expect(failedActions({ status: '失败', id: 't1-0', isDir: true })).toEqual(['retry'])
  })
  it('完成/跳过项没有动作', () => {
    expect(failedActions({ status: '完成', id: 't1-0', partPath: '/l/a.part' })).toEqual([])
    expect(failedActions({ status: '跳过', id: 't1-0', partPath: '/l/a.part' })).toEqual([])
  })
})

describe('applyProgress：事件按 id 落项', () => {
  const frame = (over = {}) => ({ id: 't1-0', done: 10, total: 100, filesDone: 1, filesTotal: 2, phase: 'transfer', partPath: '/l/a.part', ...over })
  const list = () => [{ id: 't1-0', status: '处理中' }, { id: 't1-1', status: '处理中' }]

  it('按 id 落帧并保存 partPath（续传锚点）', () => {
    const ts = list()
    expect(applyProgress(ts, frame())).toBe(true)
    expect(ts[0].done).toBe(10)
    expect(ts[0].total).toBe(100)
    expect(ts[0].filesDone).toBe(1)
    expect(ts[0].partPath).toBe('/l/a.part')
    expect(ts[0].hasProgress).toBe(true)
    expect(ts[1].hasProgress).toBeUndefined()
  })
  it('未知 id / 空帧忽略，不污染队列', () => {
    const ts = list()
    expect(applyProgress(ts, frame({ id: 't9-9' }))).toBe(false)
    expect(applyProgress(ts, null)).toBe(false)
    expect(applyProgress(ts, {})).toBe(false)
  })
  it('终态项不再被迟到帧改写（终态只认操作结果）', () => {
    const ts = list()
    ts[0].status = '完成'
    expect(applyProgress(ts, frame())).toBe(false)
    expect(ts[0].done).toBeUndefined()
  })
  it('帧里的空 partPath 不覆盖已保存的锚点', () => {
    const ts = list()
    ts[0].partPath = '/l/keep.part'
    applyProgress(ts, frame({ partPath: '' }))
    expect(ts[0].partPath).toBe('/l/keep.part')
  })
})

describe('生产路径互钉：SFC 必须消费 queue.js 的纯函数（评审 I1）', () => {
  it('SftpView 的终态与取消决策不在组件里内联', () => {
    expect(sftpView).toContain('applyOutcome(rec')
    expect(sftpView).toContain('applyCancelResult(rec')
    expect(sftpView).toContain('applyBatchCancel(transfers.value, rec)')
    expect(sftpView).toContain('shouldDispatch(rec)')
    // 禁止把终态三元组写回组件（那正是 I1 的变异点）
    expect(sftpView).not.toMatch(/rec\.status\s*=\s*rec\.cancelRequested/)
  })
  it('TransferQueue 的备注/进度/进度条都来自 queue.js 纯函数', () => {
    expect(transferQueue).toContain('rowNote(t)')
    expect(transferQueue).toContain('rowProgressText(t)')
    expect(transferQueue).toContain('barModel(t)')
    // 「取消过晚」文案只允许出现在 queue.js，组件里内联就会在这里断（变异 b）
    expect(transferQueue).not.toContain('取消过晚')
  })
  it('queue.js 是这些决策的唯一来源（组件不重复实现）', () => {
    expect(queueJs).toContain('export function applyOutcome')
    expect(queueJs).toContain('export function applyCancelResult')
    expect(queueJs).toContain('export function applyBatchCancel')
    expect(queueJs).toContain('export function rowProgressText')
    expect(queueJs).toContain('export function barModel')
  })
})

describe('failureText 既有形状不变（Task 9 队列项仍在用）', () => {
  it('拼出 源 → 目标：原因', () => {
    expect(failureText({ src: 'a', dst: 'b', reason: 'boom' })).toBe('a → b：boom')
    expect(failureText({ name: 'a' })).toBe('a → ：未知原因')
  })
})
