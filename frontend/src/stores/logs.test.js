import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useLogStore } from './logs'

describe('log store ring buffer', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('caps entries at cap', () => {
    const s = useLogStore()
    s.cap = 3
    for (let i = 0; i < 5; i++) s.add({ source_id: String(i), level: 'info', message: 'm' })
    expect(s.logs.length).toBe(3)
    expect(s.logs[0].source_id).toBe('2')
  })

  it('filters by source and level', () => {
    const s = useLogStore()
    s.add({ source_id: 'a', level: 'info', message: 'x' })
    s.add({ source_id: 'b', level: 'error', message: 'y' })
    s.filterSource = 'a'
    expect(s.filtered.length).toBe(1)
    s.filterSource = ''
    s.filterLevel = 'error'
    expect(s.filtered.length).toBe(1)
  })

  it('clear empties logs', () => {
    const s = useLogStore()
    s.add({ source_id: 'a', level: 'info', message: 'x' })
    s.clear()
    expect(s.logs.length).toBe(0)
  })
})
