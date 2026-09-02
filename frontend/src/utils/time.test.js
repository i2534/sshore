import { describe, it, expect } from 'vitest'
import { fmtLogTime } from './time'

describe('fmtLogTime', () => {
  it('formats RFC3339 (Go event ts) to yyyy-MM-dd HH:mm:ss', () => {
    // 注意: 输出按本地时区;测试用无时区偏移的固定输入不可靠,
    // 这里用本地时区构造期望值,仅验证格式与秒级截断。
    const ts = '2026-09-02T21:43:06+08:00'
    const got = fmtLogTime(ts)
    expect(got).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })

  it('truncates milliseconds (ISO with .000Z)', () => {
    const ts = '2026-09-02T13:43:06.123Z'
    const got = fmtLogTime(ts)
    expect(got).not.toContain('.')
    expect(got).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })

  it('round-trips a known local time', () => {
    const d = new Date(2026, 8, 2, 21, 43, 6) // 2026-09-02 21:43:06 本地
    expect(fmtLogTime(d.toISOString())).toBe('2026-09-02 21:43:06')
  })

  it('falls back to raw string for unparseable input', () => {
    expect(fmtLogTime('not-a-date')).toBe('not-a-date')
    expect(fmtLogTime('')).toBe('')
    expect(fmtLogTime(undefined)).toBe('')
  })
})
