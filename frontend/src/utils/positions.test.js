import { describe, it, expect } from 'vitest'
import { positionGroups, POSITION_SECTION_ROWS } from './positions'

const presets = [{ name: '主目录', path: '~', host: '' }, { name: '日志', path: '/var/log', host: 'prod' }]
const disks = [{ name: 'C:\\', path: 'C:\\' }, { name: 'D:\\', path: 'D:\\' }]
const bookmarks = [{ name: 'a', path: '/a' }]
const recents = Array.from({ length: 20 }, (_, i) => ({ host: 'prod', path: '/r' + i }))

describe('positionGroups：分区顺序 / 截断 / 展开 / 筛选', () => {
  it('固定顺序 预设 → 磁盘 → 书签 → 最近，空组不渲染', () => {
    expect(positionGroups({ presets, disks, bookmarks, recents }).map((g) => g.key))
      .toEqual(['preset', 'disk', 'bookmark', 'recent'])
    expect(positionGroups({ bookmarks }).map((g) => g.key)).toEqual(['bookmark'])
    expect(positionGroups()).toEqual([])
  })

  it('每区默认最多 8 行，total 是筛选后的真实条数', () => {
    const g = positionGroups({ recents }).find((x) => x.key === 'recent')
    expect(g.rows).toHaveLength(POSITION_SECTION_ROWS)
    expect(g.total).toBe(20)
  })

  it('展开全部后给出该区全量', () => {
    const g = positionGroups({ recents, expanded: { recent: true } }).find((x) => x.key === 'recent')
    expect(g.rows).toHaveLength(20)
    expect(g.total).toBe(20)
  })

  // Windows 缺陷 W1：展开控件的 v-if 若依赖 rows.length/total，展开后按钮被摘下 DOM，
  // 焦点节点消失 → 面板子树 focusout(relatedTarget=null) → 弹层整个收起。
  // 所以「是否可展开」必须来自一个不随 expanded 变化的字段。
  it('expandable 由筛选后的条数决定：>8 为 true，≤8 为 false', () => {
    const recent = positionGroups({ recents }).find((x) => x.key === 'recent')
    expect(recent.total).toBe(20)
    expect(recent.expandable).toBe(true)

    // 恰好 8 条：rows.length === total，旧判据（total > rows.length）为 false，expandable 也必须 false
    const exactly8 = positionGroups({ recents: recents.slice(0, 8) }).find((x) => x.key === 'recent')
    expect(exactly8.total).toBe(POSITION_SECTION_ROWS)
    expect(exactly8.rows).toHaveLength(POSITION_SECTION_ROWS)
    expect(exactly8.expandable).toBe(false)

    // 少于 8 条
    expect(positionGroups({ presets })[0].expandable).toBe(false)
    expect(positionGroups({ bookmarks })[0].expandable).toBe(false)
  })

  it('expandable 不随 expanded 变化（展开前后同为 true，控件节点不消失）', () => {
    const collapsed = positionGroups({ recents }).find((x) => x.key === 'recent')
    const expanded = positionGroups({ recents, expanded: { recent: true } }).find((x) => x.key === 'recent')
    expect(collapsed.rows).toHaveLength(POSITION_SECTION_ROWS)
    expect(expanded.rows).toHaveLength(20)
    expect(collapsed.expandable).toBe(true)
    expect(expanded.expandable).toBe(true)
  })

  it('expandable 跟随筛选结果：筛选把条数压到 ≤8 时转 false', () => {
    const many = positionGroups({ recents, filter: '/r1' }).find((x) => x.key === 'recent')
    expect(many.total).toBe(11) // /r1、/r10…/r19
    expect(many.expandable).toBe(true)

    const few = positionGroups({ recents, filter: '/r0' }).find((x) => x.key === 'recent')
    expect(few.total).toBe(1)
    expect(few.expandable).toBe(false)
  })

  it('筛选同时匹配名称与路径，大小写不敏感', () => {
    const byPath = positionGroups({ presets, recents, filter: 'var/log' })
    expect(byPath.map((g) => g.key)).toEqual(['preset'])
    expect(byPath[0].rows.map((r) => r.label)).toEqual(['日志'])
    const byName = positionGroups({ bookmarks, filter: 'A' })
    expect(byName[0].rows.map((r) => r.label)).toEqual(['a'])
  })

  it('无命中 → 空数组（UI 渲染「没有匹配的位置」）', () => {
    expect(positionGroups({ presets, disks, bookmarks, recents, filter: 'zzz' })).toEqual([])
  })

  it('预设 key 保留 name+path+host 语义，同名同路径不同 host 不撞 key', () => {
    const two = [{ name: 'x', path: '/p', host: 'a' }, { name: 'x', path: '/p', host: 'b' }]
    const g = positionGroups({ presets: two })[0]
    expect(new Set(g.rows.map((r) => r.key)).size).toBe(2)
  })
})
