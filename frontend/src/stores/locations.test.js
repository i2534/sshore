import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../../wailsjs/go/main/App', () => ({
  ListLocations: vi.fn(async () => ({
    bookmarks: [{ name: '日志', scope: 'remote', host: 'prod', path: '/var/log' }],
    localRecents: [{ path: '/home/u/a', ts: '2' }],
    remoteRecents: [
      { host: 'prod', path: '/var/log', ts: '2' },
      { host: 'db', path: '/srv', ts: '1' },
    ],
  })),
  ListPresets: vi.fn(async () => ({
    local: [{ name: '项目', path: '/work/proj' }, { name: '主目录', path: '/home/u' }, { name: '根目录', path: '/' }],
    localDisks: [],
    remote: [
      { name: '主目录', path: '~' },
      { name: '根目录', path: '/' },
      { name: '生产日志', path: '/var/log', host: 'prod' },
    ],
  })),
  AddBookmark: vi.fn(async () => {}),
  RemoveBookmark: vi.fn(async () => {}),
  AddLocalRecent: vi.fn(async () => {}),
  AddRemoteRecent: vi.fn(async () => {}),
  // sftpui store 的两个绑定：locations 的镜像**只走内存**，这两个 mock 的存在正是
  // 为了钉死"镜像前后调用次数都是 0"（规格 §7.2 零新增 IPC）。
  GetSftpUI: vi.fn(async () => ({})),
  SaveSftpUI: vi.fn(async () => {}),
}))

import {
  AddLocalRecent, AddRemoteRecent, ListPresets, GetSftpUI, SaveSftpUI,
} from '../../wailsjs/go/main/App'
import { useLocationsStore } from './locations'
import { useSftpUIStore } from './sftpui'

describe('locations store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('load 填充三组数据', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.bookmarks.length).toBe(1)
    expect(s.localRecents.length).toBe(1)
    expect(s.remoteRecents.length).toBe(2)
  })

  it('远程最近位置按主机过滤', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.recentsForPane('remote', 'prod').map((r) => r.path)).toEqual(['/var/log'])
    expect(s.recentsForPane('remote', 'db').map((r) => r.path)).toEqual(['/srv'])
  })

  it('本地面板的最近位置与主机无关', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.recentsForPane('local', 'prod').map((r) => r.path)).toEqual(['/home/u/a'])
  })

  it('书签按面板/主机过滤', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.bookmarksForPane('remote', 'prod').map((b) => b.path)).toEqual(['/var/log'])
    expect(s.bookmarksForPane('remote', 'db')).toEqual([])
  })

  it('addBookmark 后本地列表同步追加', async () => {
    const s = useLocationsStore()
    await s.load()
    await s.addBookmark({ name: 'x', scope: 'local', host: '', path: '/tmp' })
    expect(s.bookmarks.some((b) => b.path === '/tmp')).toBe(true)
  })

  it('重复记录同一最近位置时短路：不发 IPC、列表不变', async () => {
    const s = useLocationsStore()
    await s.load()
    const beforeL = JSON.stringify(s.localRecents)
    const beforeR = JSON.stringify(s.remoteRecents)
    AddLocalRecent.mockClear()
    AddRemoteRecent.mockClear()

    // 队首分别是 /home/u/a 与 (prod, /var/log)，重复记录必须短路
    await s.addLocalRecent('/home/u/a')
    await s.addRemoteRecent('prod', '/var/log')
    expect(AddLocalRecent).not.toHaveBeenCalled()
    expect(AddRemoteRecent).not.toHaveBeenCalled()
    expect(JSON.stringify(s.localRecents)).toBe(beforeL)
    expect(JSON.stringify(s.remoteRecents)).toBe(beforeR)

    // 不同路径仍要照常写盘并置顶
    await s.addLocalRecent('/tmp/other')
    await s.addRemoteRecent('db', '/srv')
    expect(AddLocalRecent).toHaveBeenCalledTimes(1)
    expect(AddRemoteRecent).toHaveBeenCalledTimes(1)
    expect(s.localRecents[0].path).toBe('/tmp/other')
    expect(s.remoteRecents[0].host).toBe('db')
  })

  // ---- 最后路径的内存镜像（§7.2）：唯一落盘出口在后端，前端只镜像成功结果 ----

  it('addRemoteRecent 成功后镜像 sftpui 最后路径（零新 IPC）', async () => {
    const s = useLocationsStore()
    await s.load()
    const ui = useSftpUIStore()
    expect(ui.lastPathFor('remote', 'prod')).toBe('') // 镜像初值：本会话还没 load sftpui

    AddRemoteRecent.mockClear()
    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()
    // 镜像发生前：两个 SFTP UI 绑定的调用次数都是 0
    expect(GetSftpUI).toHaveBeenCalledTimes(0)
    expect(SaveSftpUI).toHaveBeenCalledTimes(0)

    await s.addRemoteRecent('prod', '/srv/app')

    // 镜像后仍然是 0，且 AddRemoteRecent 只多了它自己那一次
    expect(GetSftpUI).toHaveBeenCalledTimes(0)
    expect(SaveSftpUI).toHaveBeenCalledTimes(0)
    expect(AddRemoteRecent).toHaveBeenCalledTimes(1)
    expect(AddRemoteRecent).toHaveBeenCalledWith('prod', '/srv/app')

    expect(ui.lastPathFor('remote', 'prod')).toBe('/srv/app')
    // 镜像只负责最后路径：不得顺带碰 loaded/sort（§7.1 的其它状态各有唯一写入口）
    expect(ui.loaded).toBe(false)
    expect(ui.sort).toEqual({ local: { key: 'name', dir: 1 }, remote: { key: 'name', dir: 1 } })
  })

  it('addLocalRecent 成功后镜像 sftpui 本地最后路径（零新 IPC）', async () => {
    const s = useLocationsStore()
    await s.load()
    const ui = useSftpUIStore()

    AddLocalRecent.mockClear()
    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()
    expect(GetSftpUI).toHaveBeenCalledTimes(0)
    expect(SaveSftpUI).toHaveBeenCalledTimes(0)

    await s.addLocalRecent('/tmp/work')

    expect(GetSftpUI).toHaveBeenCalledTimes(0)
    expect(SaveSftpUI).toHaveBeenCalledTimes(0)
    expect(AddLocalRecent).toHaveBeenCalledTimes(1)
    expect(AddLocalRecent).toHaveBeenCalledWith('/tmp/work')

    expect(ui.lastPathFor('local')).toBe('/tmp/work')
    // 本地镜像不得污染远程键空间
    expect(ui.lastPathFor('remote', 'prod')).toBe('')
  })

  it('写盘失败时不镜像（错误照旧抛出、列表也不变）', async () => {
    const s = useLocationsStore()
    await s.load()
    const ui = useSftpUIStore()
    const beforeL = JSON.stringify(s.localRecents)
    const beforeR = JSON.stringify(s.remoteRecents)

    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()
    AddRemoteRecent.mockRejectedValueOnce(new Error('写配置失败: boom'))
    await expect(s.addRemoteRecent('prod', '/srv/boom')).rejects.toThrow('写配置失败: boom')
    AddLocalRecent.mockRejectedValueOnce(new Error('写配置失败: boom'))
    await expect(s.addLocalRecent('/tmp/boom')).rejects.toThrow('写配置失败: boom')

    expect(ui.lastPathFor('remote', 'prod')).toBe('')
    expect(ui.lastPathFor('local')).toBe('')
    expect(JSON.stringify(s.localRecents)).toBe(beforeL)
    expect(JSON.stringify(s.remoteRecents)).toBe(beforeR)
    expect(GetSftpUI).toHaveBeenCalledTimes(0)
    expect(SaveSftpUI).toHaveBeenCalledTimes(0)
  })

  it('队首同值短路：不发 IPC，但镜像仍对齐（幂等无害，FF17）', async () => {
    const s = useLocationsStore()
    // 队首分别是 (prod, /var/log) 与 /home/u/a
    await s.load()
    const ui = useSftpUIStore()

    AddLocalRecent.mockClear()
    AddRemoteRecent.mockClear()
    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()

    await s.addLocalRecent('/home/u/a')
    await s.addRemoteRecent('prod', '/var/log')

    // 短路语义不变：不写盘
    expect(AddLocalRecent).not.toHaveBeenCalled()
    expect(AddRemoteRecent).not.toHaveBeenCalled()
    expect(GetSftpUI).toHaveBeenCalledTimes(0)
    expect(SaveSftpUI).toHaveBeenCalledTimes(0)
    // 但镜像要对齐：load() 失败时镜像是空串而 locations 队首非空，短路正是修正机会（FF17）
    expect(ui.lastPathFor('local')).toBe('/home/u/a')
    expect(ui.lastPathFor('remote', 'prod')).toBe('/var/log')
  })

  it('load 同时拉取三组固定预设', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.presetsForPane('local', '').map((p) => p.path)).toEqual(['/work/proj', '/home/u', '/'])
    expect(s.presetsForPane('remote', 'prod').map((p) => p.path)).toEqual(['~', '/', '/var/log'])
    expect(s.disksForPane('local')).toEqual([])
  })

  it('远程预设按当前主机过滤（host 留空 = 所有主机）', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.presetsForPane('remote', 'db').map((p) => p.path)).toEqual(['~', '/'])
    // 还没选主机时不筛，退化为"全部显示"
    expect(s.presetsForPane('remote', '').map((p) => p.path)).toEqual(['~', '/', '/var/log'])
    expect(s.presetsForPane('remote').length).toBe(3)
  })

  it('磁盘组只属于本地面板', async () => {
    const s = useLocationsStore()
    await s.load()
    s.presets.localDisks = [{ name: 'C:', path: 'C:\\' }]
    expect(s.disksForPane('local').map((p) => p.name)).toEqual(['C:'])
    expect(s.disksForPane('remote')).toEqual([])
  })

  it('后端上报预设文件错误时存进 presetsError（供日志面板提示）', async () => {
    ListPresets.mockResolvedValueOnce({ local: [], localDisks: [], remote: [], err: '解析预设文件 x: boom' })
    const s = useLocationsStore()
    await s.load()
    expect(s.presetsError).toBe('解析预设文件 x: boom')
  })

  it('后端返回 null 时兜底为空数组', async () => {
    ListPresets.mockResolvedValueOnce(null)
    const s = useLocationsStore()
    await s.load()
    expect(s.presetsForPane('local')).toEqual([])
    expect(s.disksForPane('local')).toEqual([])
    expect(s.presetsForPane('remote')).toEqual([])
    expect(s.presetsError).toBe('')
  })
})
