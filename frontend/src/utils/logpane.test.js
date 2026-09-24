import { describe, it, expect } from 'vitest'
import { clampLogHeight, LOG_MIN_H, LOG_MAX_H, LOG_DEFAULT_H } from './logpane'

describe('clampLogHeight', () => {
  it('夹在上下限之间', () => {
    expect(clampLogHeight(10)).toBe(LOG_MIN_H)
    expect(clampLogHeight(9999)).toBe(LOG_MAX_H)
    expect(clampLogHeight(200)).toBe(200)
  })
  it('非数字回落到默认高度', () => {
    expect(clampLogHeight(undefined)).toBe(LOG_DEFAULT_H)
    expect(clampLogHeight(NaN)).toBe(LOG_DEFAULT_H)
  })
  it('默认高度在范围内', () => {
    expect(LOG_DEFAULT_H).toBeGreaterThanOrEqual(LOG_MIN_H)
    expect(LOG_DEFAULT_H).toBeLessThanOrEqual(LOG_MAX_H)
  })
})
