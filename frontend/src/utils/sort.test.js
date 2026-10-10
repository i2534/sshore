import { describe, it, expect } from 'vitest'
import { nextSort } from './sort'

// 规格 §7.3：FilePane.sortBy(key) 的语义必须与旧内部 ref 实现（:129-136）逐字一致：
// 同列取反、换列归升序。状态上收到 SftpView 后，这一段决策只此一处实现。
describe('sort · nextSort', () => {
  it('同列取反（1 → -1，-1 → 1）', () => {
    expect(nextSort({ key: 'name', dir: 1 }, 'name')).toEqual({ key: 'name', dir: -1 })
    expect(nextSort({ key: 'name', dir: -1 }, 'name')).toEqual({ key: 'name', dir: 1 })
  })

  it('换列归升序（dir 恒为 1，继承来的方向不保留）', () => {
    expect(nextSort({ key: 'name', dir: -1 }, 'size')).toEqual({ key: 'size', dir: 1 })
    expect(nextSort({ key: 'name', dir: 1 }, 'size')).toEqual({ key: 'size', dir: 1 })
  })

  it('dir 只产出 1 / -1（下游据此渲染 ▲/▼，不接受 0、undefined）', () => {
    expect(['name', 'size', 'mtime'].map((k) => nextSort({ key: 'name', dir: 1 }, k).dir)).toEqual([-1, 1, 1])
    expect(nextSort({ key: 'name', dir: -1 }, 'name').dir).toBe(1)
  })

  it('返回新对象，不改入参', () => {
    const current = { key: 'name', dir: 1 }
    const next = nextSort(current, 'name')
    expect(next).not.toBe(current)
    expect(current).toEqual({ key: 'name', dir: 1 })
    const other = { key: 'name', dir: -1 }
    expect(nextSort(other, 'size')).not.toBe(other)
    expect(other).toEqual({ key: 'name', dir: -1 })
  })

  it('current 缺省时按换列处理（props 初始值兜底，不抛异常）', () => {
    expect(nextSort(undefined, 'name')).toEqual({ key: 'name', dir: 1 })
    expect(nextSort({}, 'name')).toEqual({ key: 'name', dir: 1 })
  })
})
