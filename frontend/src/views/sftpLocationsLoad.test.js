// V2：onMounted 里 `await locations.load()` 无 try/catch，ListLocations reject 会中断后面的
// sftpui.load / 本地恢复 / loadLocal（旁边 sftpui.load 的注释专门防过同款问题）。
// 修复：给 locations.load() 包 try/catch（失败 err(e) 提示但继续后续恢复），与 sftpui.load 对齐。
// 通过 sfcHarness 捕获 onMounted 回调并真实触发，mock ListLocations reject，
// 断言后续本地恢复流程（Cwd / loadLocal / localPath 落点）仍执行。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { runSetup } from './sfcHarness'
import { makeSftpEnv } from './sftpTestEnv'
import * as App from '../../wailsjs/go/main/App'
import * as runtime from '../../wailsjs/runtime/runtime'

vi.mock('../../wailsjs/go/main/App', async () => (await import('./sftpMockBindings')).makeAppMock())
vi.mock('../../wailsjs/runtime/runtime', async () => (await import('./sftpMockBindings')).makeRuntimeMock())

describe('SftpView 启动恢复不被 locations.load 失败中断（V2）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    App.ListHosts.mockResolvedValue(['A'])
    App.GetSftpUI.mockResolvedValue({})
    App.ListLocal.mockResolvedValue([])
    App.Cwd.mockResolvedValue('/home/u')
    App.ListLocations.mockResolvedValue({ bookmarks: [], localRecents: [], remoteRecents: [] })
  })

  function mount() {
    const hooks = { mounted: [], unmounted: [], activated: [], deactivated: [] }
    const v = runSetup(new URL('./SftpView.vue', import.meta.url), makeSftpEnv({
      App, EventsOn: runtime.EventsOn, hooks,
      win: { innerWidth: 1000, innerHeight: 800, addEventListener() {}, removeEventListener() {} },
      doc: { querySelector: () => null },
    }), ['localPath'])
    return { v, hooks }
  }

  it('ListLocations reject 时，后续 sftpui.load / Cwd / 本地落点仍执行', async () => {
    App.ListLocations.mockRejectedValue(new Error('boom'))
    const { v, hooks } = mount()
    // 修复前：裸 await locations.load()  reject ⇒ onMounted 整体中断（这里 catch 住以便断言后续）
    await hooks.mounted[0]().catch(() => {})
    expect(App.GetSftpUI).toHaveBeenCalled()      // sftpui.load 仍跑
    expect(App.Cwd).toHaveBeenCalled()            // 本地恢复仍跑
    expect(v.localPath.value).toBe('/home/u')     // 本地面板落到 cwd，而非空着
  })

  it('locations.load 成功时正常恢复（兜底不吞成功路径）', async () => {
    const { v, hooks } = mount()
    await hooks.mounted[0]()
    expect(App.Cwd).toHaveBeenCalled()
    expect(v.localPath.value).toBe('/home/u')
  })
})