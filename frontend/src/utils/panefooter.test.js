import { describe, it, expect } from 'vitest'
import { selectionLabel, filterClearVisible, EMPTY_FILTER_HINT } from './panefooter'

describe('selectionLabel', () => {
  it('未选中时只给总数（不再常驻「已选 0 项 ▾」入口）', () => {
    expect(selectionLabel({ selected: 0, total: 84 })).toBe('84 项')
  })
  it('选中时给出 已选 n / N 项', () => {
    expect(selectionLabel({ selected: 3, total: 84 })).toBe('已选 3 / 84 项')
  })
  it('含被过滤隐藏的已选项时如实标注', () => {
    expect(selectionLabel({ selected: 3, total: 84, hidden: 2 })).toBe('已选 3 / 84 项（含 2 项被过滤）')
  })
  it('无 hidden 时不带括号', () => {
    expect(selectionLabel({ selected: 3, total: 84, hidden: 0 })).toBe('已选 3 / 84 项')
  })
})

describe('filterClearVisible', () => {
  it('过滤框为空时不显示 ✕', () => {
    expect(filterClearVisible('')).toBe(false)
    expect(filterClearVisible(undefined)).toBe(false)
  })
  it('有内容时显示 ✕', () => {
    expect(filterClearVisible('a')).toBe(true)
  })
})

describe('EMPTY_FILTER_HINT', () => {
  it('文案明确是「按名称」（代码只匹配 it.name）', () => {
    expect(EMPTY_FILTER_HINT).toBe('按名称过滤…')
  })
})
