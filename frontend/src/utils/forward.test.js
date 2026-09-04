import { describe, it, expect } from 'vitest'
import { FORWARD_MODES, modeLabel, modeDesc } from './forward'

describe('forward mode metadata', () => {
  it('每种模式都带 code/label/desc', () => {
    expect(FORWARD_MODES.local.code).toBe('-L')
    expect(FORWARD_MODES.remote.code).toBe('-R')
    expect(FORWARD_MODES.dynamic.code).toBe('-D')
    for (const m of Object.values(FORWARD_MODES)) {
      expect(m.label).toBeTruthy()
      expect(m.desc).toBeTruthy()
    }
  })

  it('modeLabel 返回中文标签，未知回退原值', () => {
    expect(modeLabel('local')).toBe('本地转发')
    expect(modeLabel('remote')).toBe('远程转发')
    expect(modeLabel('dynamic')).toBe('动态 SOCKS')
    expect(modeLabel('bogus')).toBe('bogus')
  })

  it('modeDesc 返回方向说明，未知返回空', () => {
    expect(modeDesc('local')).toContain('监听本机端口')
    expect(modeDesc('remote')).toContain('监听远端端口')
    expect(modeDesc('dynamic')).toContain('SOCKS')
    expect(modeDesc('bogus')).toBe('')
  })
})
