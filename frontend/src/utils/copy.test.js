// 复制纯函数的真单测：覆盖 Review Focus #1（显示顺序）、#2（隐藏项）、
// #3（右键非选中行 + 不改选中集）、#4（本地/远程路径口径）。
// 这些是本任务唯一能真验证行为的层——SftpView 的接线只能读源码钉形状。
import { describe, it, expect } from 'vitest'
import { copyTargetNames, copyPayload } from './copy'

function sel(...keys) { return { keys: new Set(keys) } }

describe('copyTargetNames：作用集合与顺序', () => {
  it('单选取右键那一行', () => {
    expect(copyTargetNames(sel('a.txt'), { name: 'a.txt' }, ['a.txt', 'b.txt'])).toEqual(['a.txt'])
  })

  it('多选按面板显示顺序排序（不是 Set 的插入序）', () => {
    const got = copyTargetNames(sel('c.txt', 'a.txt', 'b.txt'), { name: 'a.txt' }, ['a.txt', 'b.txt', 'c.txt'])
    expect(got).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })

  it('右键的行不在选中集内时只取该行', () => {
    expect(copyTargetNames(sel('x.txt', 'y.txt'), { name: 'z.txt' }, ['x.txt', 'y.txt', 'z.txt'])).toEqual(['z.txt'])
  })

  it('不改变选中集（复制是只读动作）', () => {
    const s = sel('x.txt', 'y.txt')
    copyTargetNames(s, { name: 'z.txt' }, ['x.txt', 'y.txt', 'z.txt'])
    expect([...s.keys]).toEqual(['x.txt', 'y.txt'])
  })

  it('空白右键与 ".." 行返回空数组', () => {
    expect(copyTargetNames(sel('a.txt'), null, ['a.txt'])).toEqual([])
    expect(copyTargetNames(sel('a.txt'), { name: '..' }, ['a.txt'])).toEqual([])
  })

  it('被过滤器隐藏的选中项排在可见项之后（但必须包含）', () => {
    expect(copyTargetNames(sel('hidden.txt', 'a.txt'), { name: 'a.txt' }, ['a.txt'])).toEqual(['a.txt', 'hidden.txt'])
  })
})

describe('copyPayload：文本拼接与路径口径', () => {
  it('文件名逐行拼接', () => {
    expect(copyPayload('copy-name', ['a.txt', 'b.txt'], {})).toBe('a.txt\nb.txt')
  })

  it('空集合得到空串（调用方据此守卫，不清空剪贴板）', () => {
    expect(copyPayload('copy-name', [], {})).toBe('')
    expect(copyPayload('copy-path', [], { pane: 'remote', remotePath: '/home/u' })).toBe('')
  })

  it('远程全路径用 POSIX 拼接', () => {
    expect(copyPayload('copy-path', ['a.txt'], { pane: 'remote', remotePath: '/home/u' })).toBe('/home/u/a.txt')
  })

  it('远程根目录下不产生双斜杠', () => {
    expect(copyPayload('copy-path', ['a.txt'], { pane: 'remote', remotePath: '/' })).toBe('/a.txt')
  })

  it('本地全路径沿用已有反斜杠', () => {
    expect(copyPayload('copy-path', ['a.txt'], { pane: 'local', localPath: 'C:\\Users\\me' })).toBe('C:\\Users\\me\\a.txt')
  })

  it('本地全路径沿用已有的正斜杠（不强制改写——localpath.join 的既定语义）', () => {
    expect(copyPayload('copy-path', ['a.txt'], { pane: 'local', localPath: 'C:/Users/me' })).toBe('C:/Users/me/a.txt')
  })
})
