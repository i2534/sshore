import { describe, it, expect } from 'vitest'
import { createSelection, single, toggle, rangeTo, all, clear, remove, isSelected, selectedNames }
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
