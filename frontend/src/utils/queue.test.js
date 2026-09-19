import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { statusClass, summarizeQueue, directionArrow, failureText,
  percentOf, speedOf, etaOf, fmtSpeed, isInternalTempName, activeActions, failedActions,
  applyProgress, TRANSFER_PROGRESS_EVENT, PART_MARKER,
  outcomeStatus, applyOutcome, applyCancelResult, applyBatchCancel, shouldDispatch,
  rowNote, rowProgressText, barModel,
  CANCELLED_LATE_TEXT, CANCELLING_TEXT, CANCEL_FAILED_TEXT, WAITING_FRAME_TEXT, SCANNING_TEXT } from './queue'

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
  it('scan 相显示准备中，且字面量与 Go 侧 PhaseScan **双向**互钉', () => {
    expect(rowProgressText({ status: '处理中', phase: 'scan', done: 3, total: 3 })).toBe('准备中…')
    expect(rowProgressText({ status: '处理中', phase: 'scan', done: 0, total: -1 })).toBe(SCANNING_TEXT)
    // 生产侧：GetTree/PutTree 在枚举之前发 Phase=PhaseScan 首帧（internal/sftp/copy.go 的
    // scanProgress/treeProgress.scan）。Go 常量一旦改名，这里必须一起红 —— 否则 UI 分支
    // 会重新变成不可达的死代码（Task 17 修复波 d）。
    const go = src('../../../internal/sftp/api.go')
    const m = go.match(/PhaseScan\s+Phase = "([^"]+)"/)
    expect(m, 'internal/sftp/api.go 里找不到 PhaseScan 常量').toBeTruthy()
    expect(m[1]).toBe('scan')
    expect(SCANNING_TEXT).toBe('准备中…')
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
  // —— I3 修复轮 2：两个 promise 的到达顺序不确定，终态必须可被后到的 Cancel 结果修正 ——
  it('顺序 B：无关错误先落地、Cancel 后返回 false ⇒ 必须修正为「失败」+ 原始原因，绝不标成取消', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false, partPath: '/l/a.part' }
    // 1) 用户点了取消：置 cancelRequested/pendingCancel，终态等传输自己返回
    const plan = applyBatchCancel([rec], rec)
    expect(plan.inFlight).toBe(true)
    expect(rec.status).toBe('处理中')
    // 2) 传输的**无关**失败（磁盘满）先于 Cancel 返回值到达
    expect(applyOutcome(rec, { ok: false, error: new Error('disk full') })).toBe('取消')
    expect(rec.status).toBe('取消')
    expect(rec.cancelFailed).toBe(false)
    // 3) Cancel 的 false 后到：必须回头把「取消」修正成「失败」，并写 cancelFailed
    expect(applyCancelResult(rec, false)).toBe('failed')
    expect(rec.status).toBe('失败')
    expect(rec.cancelFailed).toBe(true)
    expect(rec.pendingCancel).toBe(false)
    expect(rec.reason).toBe('disk full')
    expect(rowNote(rec)).toBe('disk full')
    expect(rowNote(rec)).not.toBe('已取消')
    expect(rowProgressText(rec)).toBe('') // 终态行不再显示「取消中…」/「未能取消」
  })
  it('顺序 A（对照）：Cancel 先返回 false、无关错误后落地 ⇒ 同一个「失败」终态', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false }
    applyBatchCancel([rec], rec)
    expect(applyCancelResult(rec, false)).toBe('failed')
    expect(rec.cancelFailed).toBe(true)
    expect(rec.status).toBe('处理中') // 操作还没返回，终态未定
    expect(applyOutcome(rec, { ok: false, error: new Error('disk full') })).toBe('失败')
    expect(rec.status).toBe('失败')
    expect(rec.reason).toBe('disk full')
    expect(rowNote(rec)).toBe('disk full')
  })
  it('顺序 C（对照）：Cancel 返回 true、操作随后抛错 ⇒ 仍然是真「取消」，不被修正', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false }
    applyBatchCancel([rec], rec)
    expect(applyCancelResult(rec, true)).toBe('cancelling')
    expect(applyOutcome(rec, { ok: false, error: new Error('context canceled') })).toBe('取消')
    expect(rec.status).toBe('取消')
    // 同一个 Cancel==true 的迟到场景再次调用：不得把合法取消改成失败
    expect(applyCancelResult(rec, true)).toBe('done')
    expect(rec.status).toBe('取消')
    expect(rec.cancelFailed).toBe(false)
  })
  it('顺序 B 的「完成」对照：Cancel 后到 false 绝不改正已提交的成功结果', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false, partPath: '/l/a.part' }
    applyBatchCancel([rec], rec)
    expect(applyOutcome(rec, { ok: true })).toBe('完成')
    expect(applyCancelResult(rec, false)).toBe('done')
    expect(rec.status).toBe('完成')
    expect(rec.partPath).toBe('')
    expect(rowNote(rec)).toBe(CANCELLED_LATE_TEXT)
  })

  // —— 修复轮 3：真取消锁存（cancelOk）。GoBackend.Cancel 首次成功后删除注册表条目，
  // 重复/并发第二发必返回 false；若后面的 false 能把真取消翻成「失败」就是新缺陷。 ——
  it('重复取消 C(true) C(false) 然后 M(error)：真取消锁存，false 不得翻盘 ⇒ 收敛「取消」', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false }
    applyBatchCancel([rec], rec)
    expect(applyCancelResult(rec, true)).toBe('cancelling') // 第一发真取消：锁存
    expect(applyCancelResult(rec, false)).toBe('done')      // 第二发假 false：必须被锁存丢弃
    expect(rec.cancelOk).toBe(true)
    expect(rec.cancelFailed).toBe(false)                    // 绝不写 cancelFailed
    expect(rec.pendingCancel).toBe(false)
    expect(rec.status).toBe('处理中')                        // 终态仍等操作返回
    expect(applyOutcome(rec, { ok: false, error: new Error('context canceled') })).toBe('取消')
    expect(rec.status).toBe('取消')
    expect(rec.cancelFailed).toBe(false)
    expect(rec.reason).toBe('context canceled')
    expect(failedActions(rec)).toContain('retry')
  })
  it('重复取消 M(error) C(true) C(false)：终态本已「取消」，后到的 false 不得翻成「失败」', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false }
    applyBatchCancel([rec], rec)
    // 无关失败先落，此时 cancelFailed 仍为 false ⇒ 按「取消」落终态（与顺序 B 相同的起点）
    expect(applyOutcome(rec, { ok: false, error: new Error('disk full') })).toBe('取消')
    expect(applyCancelResult(rec, true)).toBe('done')  // 真取消到达：锁存，终态保持「取消」
    expect(applyCancelResult(rec, false)).toBe('done') // 第二发假 false：被锁存丢弃
    expect(rec.cancelOk).toBe(true)
    expect(rec.cancelFailed).toBe(false)
    expect(rec.status).toBe('取消')                     // 绝不因 false 变成「失败」
    expect(rec.pendingCancel).toBe(false)
  })
  it('未锁存前先到 false、后到 true（顺序 false→true）：true 纠正 cancelFailed 并收敛「取消」', () => {
    const rec = { id: 't1-0', status: '处理中', pending: false }
    applyBatchCancel([rec], rec)
    expect(applyCancelResult(rec, false)).toBe('failed')
    expect(rec.cancelFailed).toBe(true)
    expect(applyCancelResult(rec, true)).toBe('cancelling') // 真取消锁存并清掉 false 的污染
    expect(rec.cancelOk).toBe(true)
    expect(rec.cancelFailed).toBe(false)
    expect(applyOutcome(rec, { ok: false, error: new Error('context canceled') })).toBe('取消')
    expect(rec.status).toBe('取消')
  })
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

// —— e-weak 结构性收紧（I3 修复轮 2，修复轮 3 收紧白名单）：终态赋值只允许发生在 queue.js ——
//
// 旧的互钉只是字面量 pin（断言 SftpView 里有 applyOutcome(rec…），把**成功分支**
// rec.status = '完成' 内联回 SftpView 后仍然命中，变异存活（评审 e-weak）。这里改成
// 结构性断言：扫描两个 SFC 的“写入赋值”，任何没被显式白名单批准的 status/partPath
// 赋值都失败。白名单里的每一条都必须**逐条经得起追问**（修复轮 3 评审：有一条注释
// 把 uploadPicked 的外层兜底错说成无 id 状态机，实际那个 t 有 transferID）：
//   · 非终态写点：dispatchTransfer 重置运行态、retryItem/cleanItem 维护 partPath 锚点；
//   · 终态写点：只剩**就地构造的无 id legacy 记录**（removeSelected / onMoveDrop /
//     系统拖入 copy），它们根本不走 id 身份状态机，只能就地写终态。
// uploadPicked 的有 id 兜底已改走 applyOutcome（共享终态落点），不再需要白名单条目。
// 除此之外的所有终态写入都必须经由 queue.js 的 applyOutcome/finalizeOutcome。
// 键 = 该行 trim 后的原文；重复项用重复条目表达（同一行两个赋值也各自一条）；不引用行号。
const SFC_ASSIGN_ALLOWLIST = {
  // SftpView.vue 的允许写点。**不写行号**（会随改动漂移），改成引用所在函数名 + 语句原文；
  // 断言本身按「trim 后的整行原文」多重集精确相等，行号只是噪音。
  //
  // 除前三条非终态写点外，其余每一条都在**就地构造的无 id legacy 记录**上：
  // 这些记录不走 id 身份状态机（无绑定 id、无进度订阅、无取消/续传/重试锚点），
  // 只能就地写终态，是评审显式允许的白名单；任何**有 id** 的终态写入都必须经 queue.js。
  'SftpView.vue': [
    { text: "rec.status = '处理中'", why: "dispatchTransfer 开头：重置为运行态（终态决策仍交给 applyOutcome/finalizeOutcome）" },
    { text: "rec.partPath = keep", why: "retryItem：保留续传锚点，不是终态写入" },
    { text: "rec.partPath = ''", why: "cleanItem：清掉已删除的锚点，不是终态写入" },
    { text: "rec.status = '完成'", why: "removeSelected：就地构造的无 id 删除记录（只有 direction/name/src/dst/size/status/startedAt）" },
    { text: "rec.status = '失败'", why: "removeSelected：同一条无 id 删除记录的失败分支" },
    { text: "rec.status = '完成'", why: "onMoveDrop：就地构造的无 id move 记录" },
    { text: "rec.status = '失败'", why: "onMoveDrop：同一条无 id move 记录的失败分支" },
    { text: "try { await CopyLocal(i.path, dst); rec.status = '完成' } catch (e) { rec.status = '失败'; rec.reason = String((e && e.message) || e); err(e) }", why: "handleSystemDrop：就地构造的无 id 系统拖入 copy 记录；成功/失败两个赋值同一行，故列两条" },
    { text: "try { await CopyLocal(i.path, dst); rec.status = '完成' } catch (e) { rec.status = '失败'; rec.reason = String((e && e.message) || e); err(e) }", why: "handleSystemDrop：同上，同一行第二个赋值（重复条目表达多重集）" },
  ],
  // TransferQueue.vue：只读渲染，任何赋值都不允许
  'TransferQueue.vue': [],
}
// 匹配 `rec.status = …` / `t.partPath = …`；对象字面量里的 `status: '…'` 与
// 模板/比较用的 `t.status === '…'`（只有一个 =）都不匹配。
const statusAssignmentRe = /(?:^|[^=!<>])\b(?:rec|t)\.(status|partPath)\s*=\s*(?!=)/g

function assignmentsIn(file, source) {
  const out = []
  source.split('\n').forEach((line, i) => {
    for (const m of line.matchAll(statusAssignmentRe)) {
      out.push({ file, line: i + 1, prop: m[1], text: line.trim() })
    }
  })
  return out
}

describe('终态写入的结构性约束：status/partPath 只能在 queue.js 里被赋终态（e-weak）', () => {
  it('SftpView.vue 的 status/partPath 赋值集合被白名单精确钉死', () => {
    const allow = SFC_ASSIGN_ALLOWLIST['SftpView.vue'].map((a) => a.text)
    const actual = assignmentsIn('SftpView.vue', sftpView).map((a) => a.text)
    // 精确多重集：多一个内联终态赋值（成功分支或失败分支）都会在这里断。
    expect(actual.sort()).toEqual(allow.sort())
  })
  it('TransferQueue.vue 零 status/partPath 赋值（只读渲染）', () => {
    expect(assignmentsIn('TransferQueue.vue', transferQueue)).toEqual([])
  })
})

describe('failureText 既有形状不变（Task 9 队列项仍在用）', () => {
  it('拼出 源 → 目标：原因', () => {
    expect(failureText({ src: 'a', dst: 'b', reason: 'boom' })).toBe('a → b：boom')
    expect(failureText({ name: 'a' })).toBe('a → ：未知原因')
  })
})
