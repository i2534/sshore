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
  AddBookmark: vi.fn(async () => {}),
  RemoveBookmark: vi.fn(async () => {}),
  AddLocalRecent: vi.fn(async () => {}),
  AddRemoteRecent: vi.fn(async () => {}),
}))

import { AddLocalRecent, AddRemoteRecent } from '../../wailsjs/go/main/App'
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
})
