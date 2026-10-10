import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../../wailsjs/go/main/App', () => ({
  GetSftpUI: vi.fn(),
  SaveSftpUI: vi.fn(),
}))

import { GetSftpUI, SaveSftpUI } from '../../wailsjs/go/main/App'
import { useSftpUIStore } from './sftpui'

// config.SftpUIState 的六个 json tag，逐字（Go 侧 encoding/json 精确匹配：
// camelCase 会被静默忽略 ⇒ 整个字段被清零，规格 §6.3.3 红线）。
const SIX_KEYS = [
  'last_local_dir', 'last_remote_dirs',
  'sort_local_key', 'sort_local_dir',
  'sort_remote_key', 'sort_remote_dir',
]

const snapshot = (over = {}) => ({
  last_local_dir: '/home/u/proj',
  last_remote_dirs: { prod: '/srv/app' },
  sort_local_key: 'name',
  sort_local_dir: 1,
  sort_remote_key: 'name',
  sort_remote_dir: 1,
  ...over,
})

describe('sftpui store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    GetSftpUI.mockResolvedValue(snapshot())
    SaveSftpUI.mockResolvedValue(undefined)
  })

  it('load 填充整体快照并把 loaded 置 true', async () => {
    GetSftpUI.mockResolvedValueOnce(snapshot({
      last_remote_dirs: { prod: '/srv/app', 'db-01': '/srv/db' },
      sort_local_key: 'size',
      sort_local_dir: -1,
    }))
    const s = useSftpUIStore()
    await s.load()
    expect(s.loaded).toBe(true)
    expect(s.lastLocalDir).toBe('/home/u/proj')
    expect(s.lastRemoteDirs).toEqual({ prod: '/srv/app', 'db-01': '/srv/db' })
    expect(s.sort).toEqual({ local: { key: 'size', dir: -1 }, remote: { key: 'name', dir: 1 } })
    expect(GetSftpUI).toHaveBeenCalledTimes(1)
  })

  it('load 归一非法 sort（key 非白名单 → name，dir 只认 ±1）', async () => {
    GetSftpUI.mockResolvedValueOnce(snapshot({
      sort_local_key: 'bogus',
      sort_local_dir: 7,
      sort_remote_key: 'size',
      sort_remote_dir: -3,
    }))
    const s = useSftpUIStore()
    await s.load()
    expect(s.sort.local).toEqual({ key: 'name', dir: 1 })
    expect(s.sort.remote).toEqual({ key: 'size', dir: -1 })
  })

  it('load 失败时抛出且 loaded 保持 false、state 不被写脏', async () => {
    GetSftpUI.mockRejectedValueOnce(new Error('读配置失败: boom'))
    const s = useSftpUIStore()
    await expect(s.load()).rejects.toThrow('读配置失败: boom')
    expect(s.loaded).toBe(false)
    expect(s.lastLocalDir).toBe('')
    expect(s.lastRemoteDirs).toEqual({})
    expect(s.sort).toEqual({ local: { key: 'name', dir: 1 }, remote: { key: 'name', dir: 1 } })
  })

  it('load 收到 null（绑定畸形）时兜底默认值，不留 undefined', async () => {
    GetSftpUI.mockResolvedValueOnce(null)
    const s = useSftpUIStore()
    await s.load()
    expect(s.loaded).toBe(true)
    expect(s.lastLocalDir).toBe('')
    expect(s.lastRemoteDirs).toEqual({})
    expect(s.sort).toEqual({ local: { key: 'name', dir: 1 }, remote: { key: 'name', dir: 1 } })
  })

  it('lastPathFor：local 取 lastLocalDir、remote 取 map、缺失返回空串', async () => {
    const s = useSftpUIStore()
    await s.load()
    expect(s.lastPathFor('local')).toBe('/home/u/proj')
    expect(s.lastPathFor('remote', 'prod')).toBe('/srv/app')
    expect(s.lastPathFor('remote', 'nope')).toBe('')
    // 本地面板与主机无关
    expect(s.lastPathFor('local', 'prod')).toBe('/home/u/proj')
  })

  it('setSort 值未变时整条短路：GetSftpUI/SaveSftpUI 一次都不调', async () => {
    const s = useSftpUIStore()
    await s.load()
    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()

    await s.setSort('local', 'name', 1)
    await s.setSort('remote', 'name', 1)

    // 连"读回"都不该发生：否则每次重复点击同一列都要多一次 IPC
    expect(GetSftpUI).not.toHaveBeenCalled()
    expect(SaveSftpUI).not.toHaveBeenCalled()
  })

  it('setSort 先 GetSftpUI 读回、再逐字六键写回，只覆盖 sort 四字段', async () => {
    const s = useSftpUIStore()
    await s.load()

    const order = []
    let saved
    GetSftpUI.mockImplementation(async () => {
      order.push('get')
      // 读回快照比 store 内存更新：多了本 store 不知道的 host
      return snapshot({ last_remote_dirs: { prod: '/srv/app', 'db-01': '/srv/db' } })
    })
    SaveSftpUI.mockImplementation(async (p) => {
      order.push('save')
      saved = p
    })

    await s.setSort('local', 'size', -1)

    // 读回必须在写回之前（审核 BF2 的先决条件）
    expect(order).toEqual(['get', 'save'])
    expect(GetSftpUI.mock.invocationCallOrder[0])
      .toBeLessThan(SaveSftpUI.mock.invocationCallOrder[0])

    expect(Object.keys(saved).sort()).toEqual([...SIX_KEYS].sort()) // 逐字 snake_case，无多余/缺漏
    expect(saved.last_local_dir).toBe('/home/u/proj') // 路径字段以读回值为准，不被整结构回滚
    expect(saved.last_remote_dirs).toEqual({ prod: '/srv/app', 'db-01': '/srv/db' })
    expect(saved.sort_local_key).toBe('size')
    expect(saved.sort_local_dir).toBe(-1)
    expect(saved.sort_remote_key).toBe('name')
    expect(saved.sort_remote_dir).toBe(1)
  })

  it('saveSnapshot 合并 last_remote_dirs：保留读回中 store 不知道的 host，store 知道的胜出', async () => {
    const s = useSftpUIStore()
    await s.load() // lastRemoteDirs = { prod: '/srv/app' }
    s.noteRemotePath('db-01', '/srv/db') // 同会话内存镜像（locations 落盘成功后调用）

    let saved
    SaveSftpUI.mockImplementation(async (p) => { saved = p })
    // 读回里 prod 是更旧的值（时序差），另有 store 不知道的 other
    GetSftpUI.mockResolvedValueOnce(snapshot({
      last_remote_dirs: { prod: '/srv/OLD', other: '/other' },
    }))

    await s.saveSnapshot()

    // 整体覆盖会抹掉 other；只取 store 会回滚 prod ⇒ 必须"读回为基底 + 本 store 知道的胜出"
    expect(saved.last_remote_dirs).toEqual({ prod: '/srv/app', other: '/other', 'db-01': '/srv/db' })
  })

  it('SaveSftpUI 失败时 setSort 抛出，内存保留新值（不假装成功、不回滚）', async () => {
    const s = useSftpUIStore()
    await s.load()
    SaveSftpUI.mockRejectedValueOnce(new Error('盘满: boom'))

    await expect(s.setSort('local', 'size', -1)).rejects.toThrow('盘满: boom')

    expect(s.sort.local).toEqual({ key: 'size', dir: -1 })
    expect(GetSftpUI).toHaveBeenCalledTimes(2) // load 一次 + 写前读回一次
  })

  it('saveSnapshot 读回失败时抛出且不调 SaveSftpUI', async () => {
    const s = useSftpUIStore()
    GetSftpUI.mockRejectedValueOnce(new Error('读回失败: boom'))
    await expect(s.saveSnapshot()).rejects.toThrow('读回失败: boom')
    expect(SaveSftpUI).not.toHaveBeenCalled()
  })

  it('setSort 白名单兜底：非法 key/方向不会写进 payload', async () => {
    const s = useSftpUIStore()
    await s.load()

    let saved
    SaveSftpUI.mockImplementation(async (p) => { saved = p })

    // 非法 key('bogus') + 非法方向(0) 归一回 name/1，恰与当前值相同 ⇒ 短路，零 IPC
    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()
    await s.setSort('local', 'bogus', 0)
    expect(s.sort.local).toEqual({ key: 'name', dir: 1 })
    expect(GetSftpUI).not.toHaveBeenCalled()
    expect(SaveSftpUI).not.toHaveBeenCalled()

    // 方向 -7 → -1（与后端 normalizeSortDir 同口径）
    await s.setSort('remote', 'name', -7)
    expect(s.sort.remote).toEqual({ key: 'name', dir: -1 })
    expect(saved.sort_remote_key).toBe('name')
    expect(saved.sort_remote_dir).toBe(-1)

    // 非白名单 key 单独出现时归一回 name，绝不把 'bogus' 写回后端
    await s.setSort('local', 'bogus', -1)
    expect(s.sort.local).toEqual({ key: 'name', dir: -1 })
    expect(saved.sort_local_key).toBe('name')
    expect(saved.sort_local_dir).toBe(-1)
  })

  it('noteLocalPath/noteRemotePath 只改内存：零 IPC、不碰 loaded/sort', async () => {
    const s = useSftpUIStore()
    await s.load()
    GetSftpUI.mockClear()
    SaveSftpUI.mockClear()

    const meta = JSON.stringify({ loaded: s.loaded, sort: s.sort })
    s.noteLocalPath('/tmp/other')
    s.noteRemotePath('db-01', '/srv/db')

    expect(GetSftpUI).not.toHaveBeenCalled()
    expect(SaveSftpUI).not.toHaveBeenCalled()
    expect(s.lastLocalDir).toBe('/tmp/other')
    expect(s.lastPathFor('remote', 'db-01')).toBe('/srv/db')
    // 不触碰 loaded / sort 等其它状态
    expect(JSON.stringify({ loaded: s.loaded, sort: s.sort })).toBe(meta)
  })
})
