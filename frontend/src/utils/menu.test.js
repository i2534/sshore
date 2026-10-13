// 子菜单定位纯函数的单测（无 jsdom：这里只测算术，不碰 DOM）。
import { describe, it, expect } from 'vitest'
import { submenuPlacement } from './menu'

const VP = { width: 1000, height: 800 }

describe('submenuPlacement：右侧优先 + 右/下缘翻转', () => {
  it('右侧放得下时贴在父项右侧', () => {
    const got = submenuPlacement({ left: 100, right: 180, top: 50, bottom: 78 }, { width: 140, height: 90 }, VP)
    expect(got).toEqual({ left: 182, top: 50, maxHeight: 750 })
  })

  it('右缘放不下时左翻到父项左侧', () => {
    const got = submenuPlacement({ left: 900, right: 980, top: 50, bottom: 78 }, { width: 140, height: 90 }, VP)
    expect(got.left).toBe(900 - 2 - 140)
  })

  it('左翻后仍越界时贴左边缘', () => {
    // 右侧 112+900=1012 > 1000 ⇒ 左翻；30-2-900=-872 < 0 ⇒ 贴左
    const got = submenuPlacement({ left: 30, right: 110, top: 50, bottom: 78 }, { width: 900, height: 90 }, VP)
    expect(got.left).toBe(0)
  })

  it('下缘放不下时上移，并把 maxHeight 收到可用高度', () => {
    const got = submenuPlacement({ left: 100, right: 180, top: 700, bottom: 728 }, { width: 140, height: 300 }, VP)
    expect(got.top).toBe(500)
    expect(got.maxHeight).toBe(300)
  })

  it('上移后仍越界时贴顶，maxHeight 为整个视口高度', () => {
    const got = submenuPlacement({ left: 100, right: 180, top: 700, bottom: 728 }, { width: 140, height: 900 }, VP)
    expect(got.top).toBe(0)
    expect(got.maxHeight).toBe(800)
  })

  it('gap 默认 2', () => {
    const got = submenuPlacement({ left: 100, right: 180, top: 50, bottom: 78 }, { width: 100, height: 50 }, VP)
    expect(got.left).toBe(180 + 2)
  })
})
