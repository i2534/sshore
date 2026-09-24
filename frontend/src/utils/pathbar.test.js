import { describe, it, expect } from 'vitest'
import { resolvePathInput, isImeCommit, REMOTE_HOME } from './pathbar'

const local = (raw, current = 'C:\\base') => resolvePathInput({ pane: 'local', raw, current, hasHost: false, connected: false })
const remote = (raw, opts = {}) => resolvePathInput({ pane: 'remote', raw, current: '/base', hasHost: true, connected: true, ...opts })

describe('resolvePathInput：路径栏输入 → 导航意图', () => {
  it('空串 / 纯空白 / null 一律 noop(empty)，不发请求', () => {
    expect(local('')).toEqual({ kind: 'noop', reason: 'empty' })
    expect(local('   ')).toEqual({ kind: 'noop', reason: 'empty' })
    expect(local(null)).toEqual({ kind: 'noop', reason: 'empty' })
  })

  it('兼容 Windows「复制为路径」的引号包裹', () => {
    expect(local('"C:\\Program Files"')).toEqual({ kind: 'navigate', path: 'C:\\Program Files' })
  })

  it('本地裸盘符归一为盘根（D: → D:\\）', () => {
    expect(local('D:')).toEqual({ kind: 'navigate', path: 'D:\\' })
  })

  it('本地与当前目录相同 → noop(same)', () => {
    expect(local('C:\\base')).toEqual({ kind: 'noop', reason: 'same' })
  })

  it('本地不做 ~ 展开：明确提示而不是把 ~ 交给后端去报错', () => {
    expect(local('~')).toEqual({ kind: 'noop', reason: 'local-home' })
  })

  it('远程 ~ 且已选主机 → home', () => {
    expect(remote(REMOTE_HOME)).toEqual({ kind: 'home' })
  })

  it('远程 ~ 但未选主机 → noop(no-host)', () => {
    expect(remote(REMOTE_HOME, { hasHost: false })).toEqual({ kind: 'noop', reason: 'no-host' })
  })

  it('远程未连接普通路径 → connect(path)，由 connect() 消费', () => {
    expect(remote('/var/log', { connected: false })).toEqual({ kind: 'connect', path: '/var/log' })
  })

  it('远程未连接时输入"当前显示的路径"也要去连，不被 same 短路', () => {
    expect(remote('/base', { connected: false })).toEqual({ kind: 'connect', path: '/base' })
  })

  it('远程已连接 → navigate(path)；与当前相同 → noop(same)', () => {
    expect(remote('/var/log')).toEqual({ kind: 'navigate', path: '/var/log' })
    expect(remote('/base')).toEqual({ kind: 'noop', reason: 'same' })
  })

  it('远程未选主机 → noop(no-host)', () => {
    expect(remote('/x', { hasHost: false, connected: false })).toEqual({ kind: 'noop', reason: 'no-host' })
  })
})

describe('isImeCommit：路径栏回车的 IME 守卫（必须挂在 keydown 上）', () => {
  it('组合态中的回车（isComposing=true）判定为「确认候选」，不得提交跳转', () => {
    expect(isImeCommit({ isComposing: true, keyCode: 13 })).toBe(true)
  })

  it('keyCode 229（部分浏览器组合态的 keydown 哨兵）也判定为 IME 确认', () => {
    expect(isImeCommit({ isComposing: false, keyCode: 229 })).toBe(true)
  })

  it('普通回车（组合已结束、isComposing=false）不得被当成 IME 确认', () => {
    expect(isImeCommit({ isComposing: false, keyCode: 13 })).toBe(false)
  })

  it('事件缺失时安全返回 false（不吞掉任何提交）', () => {
    expect(isImeCommit(null)).toBe(false)
    expect(isImeCommit(undefined)).toBe(false)
  })
})
