import { describe, it, expect } from 'vitest'
import { payloadFor, parsePayload, payloadFromDragEvent, DRAG_MIME, hitPane, pickDropPane, DROP_PANE_TTL_MS, isSubPath, canDropInto, isSystemDrop } from './dnd'

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

  // 真机事故（2026-09-19）：模板把原生 DragEvent 直接传给期望 { event } 的处理器，
  // 解构出 undefined ⇒ 读 dataTransfer 抛 TypeError ⇒ 顶部「界面错误」红条。
  // 这三条同时钉住"缺事件不得抛错"和"系统拖入不得被当成应用内载荷"。
  it('payloadFromDragEvent：原生 DragEvent 形状取到应用内载荷', () => {
    const ev = { dataTransfer: { getData: (t) => (t === DRAG_MIME ? payloadFor('remote', ['a']) : '') } }
    expect(payloadFromDragEvent(ev)).toEqual({ pane: 'remote', names: ['a'] })
  })
  it('payloadFromDragEvent：event 缺失/None 形状一律返回 null，绝不抛 TypeError', () => {
    expect(payloadFromDragEvent(undefined)).toBe(null)
    expect(() => payloadFromDragEvent(undefined)).not.toThrow()
    expect(payloadFromDragEvent({ event: undefined })).toBe(null) // 老的错误参数形状
    expect(payloadFromDragEvent({ dataTransfer: null })).toBe(null)
    expect(payloadFromDragEvent({ dataTransfer: {} })).toBe(null)  // getData 不是函数
  })
  it('payloadFromDragEvent：系统拖入（Files 类型、无应用 MIME）返回 null', () => {
    const ev = { dataTransfer: { types: ['Files'], getData: () => '' } }
    expect(isSystemDrop(ev.dataTransfer.types)).toBe(true)
    expect(payloadFromDragEvent(ev)).toBe(null)
  })

  // 真机事故（2026-09-19）：Wails 传来的拖入坐标按屏幕像素给，而面板矩形是页面坐标，
  // 于是「拖到本地面板正中央」被判成落点无效。spec R3 的降级就是这一条。
  it('pickDropPane：坐标命中优先（回退记录不参与）', () => {
    expect(pickDropPane({ x: 200, y: 300 }, rects, { pane: 'remote', ts: 1000 }, 1000)).toBe('local')
    expect(pickDropPane({ x: 500, y: 300 }, rects, null, 1000)).toBe('remote')
  })
  it('pickDropPane：坐标不可信时用 DOM drop 记录的面板（不许猜、不许丢）', () => {
    expect(pickDropPane({ x: 900, y: 500 }, rects, { pane: 'local', ts: 1000 }, 1000)).toBe('local')
    expect(pickDropPane({ x: 900, y: 500 }, rects, { pane: 'remote', ts: 1000 }, 1000 + DROP_PANE_TTL_MS)).toBe('remote')
  })
  it('pickDropPane：陈旧记录（超 TTL）与非法值一律 null（调用方据此提示落点无效）', () => {
    expect(pickDropPane({ x: 900, y: 500 }, rects, null, 1000)).toBe(null)
    expect(pickDropPane({ x: 900, y: 500 }, rects, { pane: 'local', ts: 1000 }, 1000 + DROP_PANE_TTL_MS + 1)).toBe(null)
    expect(pickDropPane({ x: 900, y: 500 }, rects, { pane: 'bogus', ts: 1000 }, 1000)).toBe(null)
    expect(pickDropPane({ x: 900, y: 500 }, rects, { pane: 'local' }, 1000)).toBe(null)
  })
})
