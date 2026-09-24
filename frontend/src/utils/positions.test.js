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

  it('默认每区 4 行（用户反馈「最近太长」）', () => {
    expect(POSITION_SECTION_ROWS).toBe(4)
    const recents = Array.from({ length: POSITION_SECTION_ROWS * 2 + 1 }, (_, i) => ({ key: 'r' + i, label: 'r' + i, path: '/r' + i }))
    const g = positionGroups({ recents }).find((x) => x.key === 'recent')
    expect(g.rows).toHaveLength(4)
    expect(g.expandable).toBe(true)
    expect(g.total).toBe(POSITION_SECTION_ROWS * 2 + 1)
  })

  it('每区默认只展示阈值行数（POSITION_SECTION_ROWS），total 是筛选后的真实条数', () => {
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
  it('expandable 由筛选后的条数决定：超过 POSITION_SECTION_ROWS 为 true，未超过为 false', () => {
    const recent = positionGroups({ recents }).find((x) => x.key === 'recent')
    expect(recent.total).toBe(20)
    expect(recent.expandable).toBe(true)

    // 恰好阈值条数：rows.length === total，旧判据（total > rows.length）为 false，expandable 也必须 false
    const exactlyRows = positionGroups({ recents: recents.slice(0, POSITION_SECTION_ROWS) }).find((x) => x.key === 'recent')
    expect(exactlyRows.total).toBe(POSITION_SECTION_ROWS)
    expect(exactlyRows.rows).toHaveLength(POSITION_SECTION_ROWS)
    expect(exactlyRows.expandable).toBe(false)

    // 少于阈值条数
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

  it('expandable 跟随筛选结果：筛选把条数压到阈值及以下时转 false', () => {
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
