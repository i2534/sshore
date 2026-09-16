import { describe, it, expect } from 'vitest'
import { statusClass, summarizeQueue, directionArrow } from './queue'

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
