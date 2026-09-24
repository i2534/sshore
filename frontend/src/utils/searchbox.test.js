import { describe, it, expect } from 'vitest'
import { shouldSearch, EMPTY_PATTERN_HINT } from './searchbox'

describe('shouldSearch：空模式绝不发起深搜', () => {
  it('空串 / 空格 / null / undefined → false', () => {
    for (const v of ['', '   ', null, undefined]) expect(shouldSearch(v)).toBe(false)
  })

  it('通配符或关键词 → true（前后空白不算空）', () => {
    for (const v of ['*', '?', 'app.log', '  app  ']) expect(shouldSearch(v)).toBe(true)
  })

  it('空态文案可被 SSR 断言依赖', () => {
    expect(EMPTY_PATTERN_HINT).toContain('回车')
  })
})
