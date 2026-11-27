// V8：右键菜单只夹 X 不夹 Y，底部右键溢出视口。showMenu 的 x 有 Math.min(clientX, innerWidth-180)，
// y 直接用 clientY。修复对 y 做同风格夹取（菜单高度按最多 6 行 × 约 32px + 内边距，保守取 240px）。
// 通过 sfcHarness 最小挂载真实执行 showMenu，stub innerWidth/innerHeight，字面量断言夹取结果。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { runSetup } from './sfcHarness'
import { makeSftpEnv } from './sftpTestEnv'
import * as App from '../../wailsjs/go/main/App'
import * as runtime from '../../wailsjs/runtime/runtime'

vi.mock('../../wailsjs/go/main/App', async () => (await import('./sftpMockBindings')).makeAppMock())
vi.mock('../../wailsjs/runtime/runtime', async () => (await import('./sftpMockBindings')).makeRuntimeMock())

describe('SftpView 右键菜单 Y 夹取（V8）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function mount(win) {
    return runSetup(new URL('./SftpView.vue', import.meta.url), makeSftpEnv({
      App, EventsOn: runtime.EventsOn,
      win,
      doc: { querySelector: () => null },
    }), ['showMenu', 'menu'])
  }

  it('靠近视口底部右键：y 被夹取到不溢出（innerHeight=800 ⇒ y=560）', () => {
    const { showMenu, menu } = mount({ innerWidth: 1000, innerHeight: 800, addEventListener() {}, removeEventListener() {} })
    showMenu('remote', { item: { name: 'f.txt', isDir: false }, event: { clientX: 900, clientY: 790 } })
    expect(menu.value.visible).toBe(true)
    expect(menu.value.x).toBe(820) // min(900, 1000-180)：X 夹取保持既有
    expect(menu.value.y).toBe(560) // min(790, 800-240)：修复前恒 790，溢出视口
  })

  it('正常位置右键：y 不被夹取（clientY=100 原样保留）', () => {
    const { showMenu, menu } = mount({ innerWidth: 1000, innerHeight: 800, addEventListener() {}, removeEventListener() {} })
    showMenu('local', { item: null, event: { clientX: 50, clientY: 100 } })
    expect(menu.value.x).toBe(50)
    expect(menu.value.y).toBe(100)
  })
})