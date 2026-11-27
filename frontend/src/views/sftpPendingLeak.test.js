// V6：握手期间切主机，pendingPath/pendingHome 泄漏到下一次连接。onHostChange 不清 pending，
// connect() 的主机守卫又在消费 pending 之后 ⇒ 残留带着旧主机的目标路径连新主机。
// 修复：onHostChange 时清空属于旧主机的落点意图。通过 sfcHarness 最小挂载真实执行
// onHostChange/connect，断言 pending 已清且随后连接落到新主机 home（不带旧路径）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { runSetup } from './sfcHarness'
import { makeSftpEnv } from './sftpTestEnv'
import * as App from '../../wailsjs/go/main/App'
import * as runtime from '../../wailsjs/runtime/runtime'

vi.mock('../../wailsjs/go/main/App', async () => (await import('./sftpMockBindings')).makeAppMock())
vi.mock('../../wailsjs/runtime/runtime', async () => (await import('./sftpMockBindings')).makeRuntimeMock())

const EXPOSE = ['pendingPath', 'pendingHome', 'host', 'onHostChange', 'connect', 'remotePath', 'connected']

describe('SftpView 切主机清理 pending（V6）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    App.SftpHome.mockResolvedValue('/home/b')
    App.SftpConnect.mockResolvedValue(undefined)
    App.SftpList.mockResolvedValue([])
  })

  function mount() {
    return runSetup(new URL('./SftpView.vue', import.meta.url), makeSftpEnv({
      App, EventsOn: runtime.EventsOn,
      win: { innerWidth: 1000, innerHeight: 800, addEventListener() {}, removeEventListener() {} },
      doc: { querySelector: () => null },
    }), EXPOSE)
  }

  it('置 pending 后切主机：pending 已清，随后连接落到新主机 home 而非旧路径', async () => {
    const v = mount()
    v.host.value = 'A'
    // 模拟位置下拉点到未连接主机 A 时记下的落点意图
    v.pendingPath.value = '/old/target'
    v.pendingHome.value = true

    // 切到 B
    v.host.value = 'B'
    v.onHostChange()
    expect(v.pendingPath.value).toBe('')   // 修复前残留 '/old/target'
    expect(v.pendingHome.value).toBe(false) // 修复前残留 true

    // 随后连接 B：不得带着旧主机的目标路径
    await v.connect()
    expect(v.remotePath.value).toBe('/home/b')
    expect(v.connected.value).toBe(true)
  })

  it('正常流程不破坏：未切主机时 pendingPath 仍被 connect 消费并清空', async () => {
    const v = mount()
    v.host.value = 'A'
    v.pendingPath.value = '/want'
    await v.connect()
    expect(v.remotePath.value).toBe('/want')
    expect(v.pendingPath.value).toBe('')
  })
})