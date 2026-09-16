import { describe, it, expect } from 'vitest'
import { payloadFor, parsePayload, hitPane, isSubPath, canDropInto, isSystemDrop } from './dnd'

const rects = {
  local: { left: 0, top: 100, right: 400, bottom: 600 },
  remote: { left: 410, top: 100, right: 810, bottom: 600 },
}

describe('dnd', () => {
  it('载荷往返', () => {
    expect(parsePayload(payloadFor('remote', ['a', 'b']))).toEqual({ pane: 'remote', names: ['a', 'b'] })
    expect(parsePayload('not-json')).toBe(null)
  })
  it('坐标命中面板，落在面板外返回 null', () => {
    expect(hitPane({ x: 200, y: 300 }, rects)).toBe('local')
    expect(hitPane({ x: 500, y: 300 }, rects)).toBe('remote')
    expect(hitPane({ x: 200, y: 20 }, rects)).toBe(null)
  })
  it('isSubPath 识别子树（含相等）', () => {
    expect(isSubPath('/a', '/a/b')).toBe(true)
    expect(isSubPath('/a', '/a')).toBe(true)
    expect(isSubPath('/a', '/ab')).toBe(false)
  })
  it('面板内移动：仅目录可作落点', () => {
    const item = { name: 'x.txt', isDir: false }
    expect(canDropInto({ sourcePane: 'local', targetPane: 'local', item, targetDir: '/a/b', connected: true }).ok).toBe(false)
  })
  it('面板内移动：目录拖进自己的子树被拒绝', () => {
    const item = { name: 'a', isDir: true }
    const r = canDropInto({ sourcePane: 'local', targetPane: 'local', item, sourceDir: '/x', targetDir: '/x/a/sub', connected: true })
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('子目录')
  })
  it('跨面板投递：远程未连接时拒绝', () => {
    const r = canDropInto({ sourcePane: 'local', targetPane: 'remote', item: { name: 'a', isDir: false }, targetDir: '/r', connected: false })
    expect(r.ok).toBe(false)
  })
  it('面板内移动：拖到目录自己那一行也拒绝（不能移动到自身）', () => {
    const r = canDropInto({ sourcePane: 'local', targetPane: 'local', item: { name: 'a', isDir: true }, sourceDir: '/x', targetDir: '/x/a', connected: true })
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('子目录')
  })

  it('跨面板投递：本地面板永远可用', () => {
    const r = canDropInto({ sourcePane: 'remote', targetPane: 'local', item: { name: 'a', isDir: false }, targetDir: '/l', connected: false })
    expect(r.ok).toBe(true)
  })
  it('系统拖入靠 dataTransfer.types 判定', () => {
    expect(isSystemDrop(['Files'])).toBe(true)
    expect(isSystemDrop(['text/plain'])).toBe(false)
  })
})
