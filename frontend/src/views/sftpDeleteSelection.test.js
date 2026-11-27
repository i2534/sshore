// V4：删除后 removed 用「全队列按名字找失败记录」事后猜，早前它处同名失败会让本次成功删除
// 也被留在选中集（幽灵项）。修复：在删除循环里当场收集本批实际成功的名字。
// 通过 sfcHarness 最小挂载真实执行 removeSelected（手动确认对话框），断言选中集的真实增删。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { runSetup } from './sfcHarness'
import { makeSftpEnv } from './sftpTestEnv'
import * as sel from '../utils/selection'
import * as App from '../../wailsjs/go/main/App'
import * as runtime from '../../wailsjs/runtime/runtime'

vi.mock('../../wailsjs/go/main/App', async () => (await import('./sftpMockBindings')).makeAppMock())
vi.mock('../../wailsjs/runtime/runtime', async () => (await import('./sftpMockBindings')).makeRuntimeMock())

const EXPOSE = ['removeSelected', 'remoteSelection', 'transfers', 'host', 'remotePath', 'onDialogOk']

describe('SftpView 删除后选中集（V4）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    App.SftpRemoveRecursive.mockResolvedValue(undefined)
    App.SftpList.mockResolvedValue([])
  })

  function mount() {
    const v = runSetup(new URL('./SftpView.vue', import.meta.url), makeSftpEnv({
      App, EventsOn: runtime.EventsOn,
      win: { innerWidth: 1000, innerHeight: 800, addEventListener() {}, removeEventListener() {} },
      doc: { querySelector: () => null },
    }), EXPOSE)
    v.host.value = 'A'
    v.remotePath.value = '/data'
    return v
  }

  it('它处同名失败记录在场，本批删除成功 ⇒ 该项移出选中集', async () => {
    const v = mount()
    sel.single(v.remoteSelection, 'dup.txt')
    // 队列里先有一条**它处**同名失败记录（早前别的目录删失败过 dup.txt）
    v.transfers.value.push({ name: 'dup.txt', status: '失败', direction: 'download', src: '/elsewhere/dup.txt', dst: '' })

    const p = v.removeSelected('remote')
    v.onDialogOk(true) // 确认删除
    await p

    // 本批这条删除成功（SftpRemoveRecursive resolve）
    const rec = v.transfers.value.at(-1)
    expect(rec.status).toBe('完成')
    // 成功项必须移出选中集；修复前被它处同名失败记录牵连而残留
    expect(v.remoteSelection.keys.has('dup.txt')).toBe(false)
  })

  it('本批删除失败 ⇒ 保留选中便于重试', async () => {
    App.SftpRemoveRecursive.mockRejectedValueOnce(new Error('permission denied'))
    const v = mount()
    sel.single(v.remoteSelection, 'dup.txt')

    const p = v.removeSelected('remote')
    v.onDialogOk(true)
    await p

    expect(v.transfers.value.at(-1).status).toBe('失败')
    expect(v.remoteSelection.keys.has('dup.txt')).toBe(true)
  })
})