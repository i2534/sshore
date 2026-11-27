// V7：失败上传的「清理」用当前主机 host.value，但 partPath 属于发起上传那台主机；
// 队列项原来不记 host ⇒ 切主机后清理会删错机器。修复：入队时记 host，cleanItem 用 rec.host
// （旧记录无 host 字段时兜底当前主机，保持现状语义）。Go 侧绑定签名不变。
// 通过 sfcHarness 最小挂载真实走 uploadPicked（失败上传）→ 切主机 → cleanItem，
// 断言 SftpRemove 收到的 host 是发起上传的 A。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { runSetup } from './sfcHarness'
import { makeSftpEnv } from './sftpTestEnv'
import * as App from '../../wailsjs/go/main/App'
import * as runtime from '../../wailsjs/runtime/runtime'

vi.mock('../../wailsjs/go/main/App', async () => (await import('./sftpMockBindings')).makeAppMock())
vi.mock('../../wailsjs/runtime/runtime', async () => (await import('./sftpMockBindings')).makeRuntimeMock())

describe('SftpView 失败上传清理的主机归属（V7）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    App.PickLocalFile.mockResolvedValue('/local/f.bin')
    App.StatLocal.mockResolvedValue(10)
    App.SftpPut.mockRejectedValue(new Error('boom'))
    App.SftpRemove.mockResolvedValue(undefined)
    App.SftpList.mockResolvedValue([])
  })

  function mount() {
    return runSetup(new URL('./SftpView.vue', import.meta.url), makeSftpEnv({
      App, EventsOn: runtime.EventsOn,
      win: { innerWidth: 1000, innerHeight: 800, addEventListener() {}, removeEventListener() {} },
      doc: { querySelector: () => null },
    }), ['uploadPicked', 'cleanItem', 'transfers', 'host'])
  }

  it('主机 A 的失败上传，切到 B 后清理：SftpRemove 收到的 host 是 A', async () => {
    const v = mount()
    v.host.value = 'A'
    await v.uploadPicked() // SftpPut reject ⇒ 失败上传项入队
    const rec = v.transfers.value.at(-1)
    expect(rec.status).toBe('失败')
    expect(rec.direction).toBe('upload')
    // 入队时必须记下发起上传的主机（修复前无 host 字段）
    expect(rec.host).toBe('A')
    // 模拟后端进度帧在失败前已报告 .part 锚点（续传/清理靠它）
    rec.partPath = '/remote/.sshore-sftppart-f.bin'

    // 切到 B 再清理
    v.host.value = 'B'
    App.SftpRemove.mockClear()
    await v.cleanItem(rec)
    expect(App.SftpRemove).toHaveBeenCalledWith('A', '', '/remote/.sshore-sftppart-f.bin')
    expect(rec.partPath).toBe('') // 清理成功后锚点清空
  })

  it('旧记录无 host 字段：兜底当前主机（保持现状语义）', async () => {
    const v = mount()
    v.host.value = 'B'
    const old = { direction: 'upload', partPath: '/remote/.part-old', src: 'x' }
    App.SftpRemove.mockClear()
    await v.cleanItem(old)
    expect(App.SftpRemove).toHaveBeenCalledWith('B', '', '/remote/.part-old')
  })
})