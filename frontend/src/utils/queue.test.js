import { describe, it, expect } from 'vitest'
import { statusClass, summarizeQueue, directionArrow, failureText,
  percentOf, speedOf, etaOf, fmtSpeed, isInternalTempName, progressText,
  activeActions, failedActions, applyProgress, TRANSFER_PROGRESS_EVENT, PART_MARKER } from './queue'

describe('queue 辅助', () => {
  it('状态映射：跳过必须是独立态，不能落进处理中', () => {
    expect(statusClass('完成')).toBe('done')
    expect(statusClass('失败')).toBe('err')
    expect(statusClass('跳过')).toBe('skip')
    expect(statusClass('处理中')).toBe('doing')
    expect(statusClass('')).toBe('doing')
  })

  it('汇总统计四类条数', () => {
    const list = [{ status: '完成' }, { status: '完成' }, { status: '跳过' }, { status: '失败' }, { status: '处理中' }]
    expect(summarizeQueue(list)).toEqual({ ok: 2, skipped: 1, failed: 1, running: 1, total: 5 })
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

describe('isInternalTempName', () => {
  it('认出常规名与退化短名（中缀判定）', () => {
    expect(isInternalTempName('a.txt.sshore-sftppart-t1-ab12')).toBe(true)
    expect(isInternalTempName('.sshore-sftppart-t1-ab12')).toBe(true)
  })
  it('不误判普通文件', () => {
    expect(isInternalTempName('a.txt')).toBe(false)
    expect(isInternalTempName('a.txt.bak')).toBe(false)
    expect(isInternalTempName('')).toBe(false)
  })
  it('PART_MARKER 与 Go 侧 sftp.PartMarker 字面量一致（Go 侧 TestPartMarker 钉同一值）', () => {
    expect(PART_MARKER).toBe('.sshore-sftppart-')
  })
})

describe('progressText：事件渲染契约', () => {
  it('事件名钉死：改名后前端订阅会静默失效，所以必须断言字面量', () => {
    expect(TRANSFER_PROGRESS_EVENT).toBe('sftp:transfer-progress')
  })
  it('无帧（降级 batch 后端从不发帧）给可用性占位，不显示 0% 卡条', () => {
    expect(progressText({ status: '处理中', startedAt: 1, partPath: '/tmp/a.txt.sshore-sftppart-t1-0' })).toBe('等待进度上报…')
    expect(progressText({ status: '处理中' })).toBe('等待进度上报…')
  })
  it('scan 相显示准备中', () => {
    expect(progressText({ status: '处理中', phase: 'scan', done: 3, total: 3 })).toBe('准备中…')
  })
  it('total<0（预算截断的降级扫描）显示未知/部分进度，绝不显示 100% 或当失败', () => {
    expect(progressText({ status: '处理中', done: 5, total: -1, filesDone: 2, filesTotal: -1 })).toBe('已完成 2 个文件')
    expect(progressText({ status: '处理中', done: 5, total: -1 })).toBe('未知进度')
  })
  it('total 为 0（0 字节）不显示百分比', () => {
    expect(progressText({ status: '处理中', done: 0, total: 0 })).not.toContain('%')
  })
  it('有帧且总量已知时给出百分比与字节', () => {
    expect(progressText({ status: '处理中', done: 512, total: 1024 })).toBe('50% · 512B/1.0KB')
  })
  it('完成 = 操作结果，不因取消请求改写（取消可能发生在提交之后）', () => {
    expect(progressText({ status: '完成', cancelRequested: true })).toBe('取消过晚（已完成）')
    expect(progressText({ status: '完成' })).toBe('完成')
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
  it('失败项无 partPath（如扫描相失败）只给重试', () => {
    expect(failedActions({ status: '失败', partPath: '' })).toEqual(['retry'])
    expect(failedActions({ status: '失败' })).toEqual(['retry'])
  })
  it('失败/取消项有 partPath 给重试+续传+清理', () => {
    expect(failedActions({ status: '失败', partPath: '/l/a.part' })).toEqual(['retry', 'resume', 'clean'])
    expect(failedActions({ status: '取消', partPath: '/r/a.part' })).toEqual(['retry', 'resume', 'clean'])
  })
  it('目录项不提供续传（目录级续传是 P3.1），但有 partPath 仍可清理', () => {
    expect(failedActions({ status: '失败', isDir: true, partPath: '/l/a.part' })).toEqual(['retry', 'clean'])
    expect(failedActions({ status: '失败', isDir: true })).toEqual(['retry'])
  })
  it('完成/跳过项没有动作', () => {
    expect(failedActions({ status: '完成', partPath: '/l/a.part' })).toEqual([])
    expect(failedActions({ status: '跳过', partPath: '/l/a.part' })).toEqual([])
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

describe('failureText 既有形状不变（Task 9 队列项仍在用）', () => {
  it('拼出 源 → 目标：原因', () => {
    expect(failureText({ src: 'a', dst: 'b', reason: 'boom' })).toBe('a → b：boom')
    expect(failureText({ name: 'a' })).toBe('a → ：未知原因')
  })
})
