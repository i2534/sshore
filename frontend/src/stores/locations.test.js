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
}))

import { AddLocalRecent, AddRemoteRecent, ListPresets } from '../../wailsjs/go/main/App'
import { useLocationsStore } from './locations'

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

  it('后端返回 null 时兜底为空数组', async () => {
    ListPresets.mockResolvedValueOnce(null)
    const s = useLocationsStore()
    await s.load()
    expect(s.presetsForPane('local')).toEqual([])
    expect(s.disksForPane('local')).toEqual([])
    expect(s.presetsForPane('remote')).toEqual([])
  })
})
