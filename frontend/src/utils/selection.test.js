import { describe, it, expect } from 'vitest'
import { createSelection, single, toggle, rangeTo, all, invert, extend, clear, remove, isSelected, selectedNames }
  from './selection'

describe('selection', () => {
  it('单击：替换为单项并设锚点', () => {
    const s = createSelection()
    single(s, 'b.txt')
    expect([...s.keys]).toEqual(['b.txt'])
    expect(s.anchor).toBe('b.txt')
  })

  it('Ctrl 切换：加入/移除并移动锚点', () => {
    const s = createSelection()
    single(s, 'a'); toggle(s, 'b')
    expect(isSelected(s, 'a')).toBe(true)
    expect(isSelected(s, 'b')).toBe(true)
    expect(s.anchor).toBe('b')
    toggle(s, 'a')
    expect(isSelected(s, 'a')).toBe(false)
  })

  it('Shift 区间：按可见顺序取锚点到目标之间，且不改锚点', () => {
    const s = createSelection()
    const vis = ['a', 'b', 'c', 'd']
    single(s, 'b'); rangeTo(s, vis, 'd')
    expect([...s.keys].sort()).toEqual(['b', 'c', 'd'])
    expect(s.anchor).toBe('b')
  })

  it('区间反向选择等价', () => {
    const s = createSelection()
    const vis = ['a', 'b', 'c']
    single(s, 'c'); rangeTo(s, vis, 'a')
    expect([...s.keys].sort()).toEqual(['a', 'b', 'c'])
  })

  it('锚点已不可见时退化为单项选择', () => {
    const s = createSelection()
    single(s, 'gone'); rangeTo(s, ['a', 'b'], 'b')
    expect([...s.keys]).toEqual(['b'])
  })

  it('全选/清空/删除后清理', () => {
    const s = createSelection()
    all(s, ['a', 'b'])
    expect(s.keys.size).toBe(2)
    remove(s, ['a'])
    expect([...s.keys]).toEqual(['b'])
    remove(s, ['b'])
    expect(s.anchor).toBe(null)
    clear(s)
    expect(s.keys.size).toBe(0)
  })

  it('selectedNames 按传入 items 顺序返回', () => {
    const s = createSelection()
    all(s, ['b'])
    expect(selectedNames(s, [{ name: 'a' }, { name: 'b' }])).toEqual(['b'])
  })
})
describe('invert（反选）', () => {
  const vis = ['a', 'b', 'c']
  it('未选中时反选 = 选中全部可见项', () => {
    const s = createSelection()
    invert(s, vis)
    expect([...s.keys].sort()).toEqual(['a', 'b', 'c'])
  })
  it('已选 a/b 时反选 = 只剩 c', () => {
    const s = createSelection()
    single(s, 'a'); rangeTo(s, vis, 'b')
    invert(s, vis)
    expect([...s.keys]).toEqual(['c'])
  })
  it('不吞掉被过滤隐藏的已选项（与 all() 语义有意不同）', () => {
    const s = createSelection()
    s.keys = new Set(['hidden'])
    invert(s, vis)
    expect([...s.keys].sort()).toEqual(['a', 'b', 'c', 'hidden'])
  })
})

describe('all 与 extend 的配合（Ctrl+A 之后再按 Shift+↑/↓）', () => {
  const vis = ['a', 'b', 'c', 'd']
  it('全选后锚点在首项、游标在末项，Shift+↑ 从末尾逐格收窄（不会塌成一两项）', () => {
    const s = createSelection()
    all(s, vis)
    expect([...s.keys]).toEqual(vis)
    expect(s.anchor).toBe('a')
    expect(s.cursor).toBe('d')
    extend(s, vis, -1)
    expect([...s.keys]).toEqual(['a', 'b', 'c'])
  })
  it('空列表全选不炸，锚点/游标为 null', () => {
    const s = createSelection()
    all(s, [])
    expect(s.keys.size).toBe(0)
    expect(s.anchor).toBe(null)
    expect(s.cursor).toBe(null)
  })
})

describe('extend（Shift+↑/↓）', () => {
  const vis = ['a', 'b', 'c', 'd']
  it('从游标向下扩展一格并保持 anchor 不动', () => {
    const s = createSelection()
    single(s, 'b')
    extend(s, vis, 1)
    expect([...s.keys]).toEqual(['b', 'c'])
    expect(s.anchor).toBe('b')
    extend(s, vis, 1)
    expect([...s.keys]).toEqual(['b', 'c', 'd'])
  })
  it('向上扩展回缩区间', () => {
    const s = createSelection()
    single(s, 'c')
    extend(s, vis, -1)
    expect([...s.keys]).toEqual(['b', 'c'])
  })
  it('越过两端时夹取，不越界', () => {
    const s = createSelection()
    single(s, 'a')
    extend(s, vis, -1)
    expect([...s.keys]).toEqual(['a'])
    single(s, 'd'); extend(s, vis, 1)
    expect([...s.keys]).toEqual(['d'])
  })
  it('空可见列表是 no-op', () => {
    const s = createSelection()
    extend(s, [], 1)
    expect(s.keys.size).toBe(0)
  })
  it('清空后游标也复位', () => {
    const s = createSelection()
    single(s, 'a'); clear(s)
    expect(s.cursor).toBe(null)
  })
})
