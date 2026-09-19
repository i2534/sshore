import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setSystemDropHandler, dispatchSystemDrop, systemDropHandler } from './systemDrop'

describe('systemDrop：系统拖入的单通道分发', () => {
  beforeEach(() => setSystemDropHandler(null))

  it('注册后按 {x, y, paths} 调用处理器', () => {
    const fn = vi.fn()
    setSystemDropHandler(fn)
    expect(dispatchSystemDrop({ x: 10, y: 20, paths: ['C:\\a.txt'] })).toBe(true)
    expect(fn).toHaveBeenCalledWith({ x: 10, y: 20, paths: ['C:\\a.txt'] })
  })

  it('没注册（切到端口转发/文件同步标签）时静默丢弃：不调用、不抛错', () => {
    expect(systemDropHandler()).toBe(null)
    expect(dispatchSystemDrop({ x: 1, y: 2, paths: ['/tmp/a'] })).toBe(false)
  })

  it('空载荷 / 空 paths 不进处理器（避免 StatPaths([]) 后误报落点无效）', () => {
    const fn = vi.fn()
    setSystemDropHandler(fn)
    expect(dispatchSystemDrop(null)).toBe(false)
    expect(dispatchSystemDrop({})).toBe(false)
    expect(dispatchSystemDrop({ x: 0, y: 0, paths: [] })).toBe(false)
    expect(fn).not.toHaveBeenCalled()
  })

  it('注销后不再调用（KeepAlive 切标签必须摘，否则别的标签拖入会投到 SFTP 面板）', () => {
    const fn = vi.fn()
    setSystemDropHandler(fn)
    setSystemDropHandler(null)
    expect(dispatchSystemDrop({ x: 1, y: 1, paths: ['/tmp/a'] })).toBe(false)
    expect(fn).not.toHaveBeenCalled()
  })

  it('注册非函数等价于注销（防止把 undefined 当处理器存进去）', () => {
    const fn = vi.fn()
    setSystemDropHandler(fn)
    setSystemDropHandler(undefined)
    expect(systemDropHandler()).toBe(null)
    expect(dispatchSystemDrop({ x: 1, y: 1, paths: ['/tmp/a'] })).toBe(false)
    expect(fn).not.toHaveBeenCalled()
  })
})
