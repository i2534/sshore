import { describe, it, expect } from 'vitest'
import { actionFor, isEditableTarget } from './keys'

function ev(key, opts = {}) {
  return { key, ctrlKey: false, metaKey: false, target: opts.target || { tagName: 'LI' } }
}
function input() {
  return { tagName: 'INPUT', isContentEditable: false, closest: () => null }
}

describe('keys 键盘分派', () => {
  it('Delete 映射为 delete', () => {
    expect(actionFor(ev('Delete'))).toBe('delete')
  })
  it('Ctrl/Cmd+A 映射为 select-all', () => {
    const e1 = { key: 'a', ctrlKey: true, target: { tagName: 'UL' } }
    const e2 = { key: 'A', metaKey: true, target: { tagName: 'UL' } }
    expect(actionFor(e1)).toBe('select-all')
    expect(actionFor(e2)).toBe('select-all')
  })
  it('Escape 映射为 escape', () => {
    expect(actionFor(ev('Escape'))).toBe('escape')
  })
  it('焦点在输入框时一律不处理（守卫）', () => {
    expect(actionFor(ev('Delete', { target: input() }))).toBe(null)
    expect(actionFor({ key: 'a', ctrlKey: true, target: input() })).toBe(null)
  })
  it('焦点在浮层/对话框内时不处理', () => {
    const inDialog = { tagName: 'DIV', isContentEditable: false, closest: (s) => (s.includes('.ui-overlay') ? {} : null) }
    expect(isEditableTarget(inDialog)).toBe(true)
    expect(actionFor(ev('Delete', { target: inDialog }))).toBe(null)
  })
  it('其他按键不处理', () => {
    expect(actionFor(ev('x'))).toBe(null)
  })
})
