import { describe, it, expect } from 'vitest'
import { resolveRemoteTarget } from './remotepath'

// 规格 §6.4 / §6.5（B5、FF8）：connect() 与 syncConnection() 的未连接分支共用这一个落点决策，
// 优先级 1 pendingPath（非空） > 2 pendingHome > 3 记忆路径 > 4 home（SftpHome 兜底）。
// fromMemory 供 §6.5 的失效回落判定使用：只有确实采用了记忆路径才允许回落到 home。
describe('remotepath · resolveRemoteTarget', () => {
  it('pendingPath 优先于一切', () => {
    expect(resolveRemoteTarget({ pendingPath: '/p', pendingHome: true, remembered: '/r', home: '/h' })).toEqual({
      path: '/p',
      fromMemory: false,
    })
  })

  it('pendingHome 优先于记忆路径（D15 回归守卫：已连到 /var/log 时点「主目录」必须到 home）', () => {
    // 旧实现对「主目录」的隐含约定是 pendingPath = '' ⇒ 一旦插入记忆路径这一级，
    // 该约定失效会把用户送回上次的目录 —— 这条用例就是防止退回那个约定。
    expect(resolveRemoteTarget({ pendingHome: true, remembered: '/var/log', home: '/home/lan' })).toEqual({
      path: '/home/lan',
      fromMemory: false,
    })
  })

  it('记忆路径优先于 home，并被标记 fromMemory', () => {
    expect(resolveRemoteTarget({ remembered: '/r', home: '/h' })).toEqual({ path: '/r', fromMemory: true })
  })

  it('无记忆则回落 home', () => {
    expect(resolveRemoteTarget({ remembered: '', home: '/h' })).toEqual({ path: '/h', fromMemory: false })
    expect(resolveRemoteTarget({ home: '/h' })).toEqual({ path: '/h', fromMemory: false })
  })

  it('pendingPath 空串视为未指定（工具栏「连接」主流程恒为 ""，F16）', () => {
    expect(resolveRemoteTarget({ pendingPath: '', remembered: '/r', home: '/h' })).toEqual({
      path: '/r',
      fromMemory: true,
    })
    expect(resolveRemoteTarget({ pendingPath: '', pendingHome: true, remembered: '/r', home: '/h' })).toEqual({
      path: '/h',
      fromMemory: false,
    })
    expect(resolveRemoteTarget({ pendingPath: '', home: '/h' })).toEqual({ path: '/h', fromMemory: false })
  })

  it('fromMemory 只在采用记忆值时真', () => {
    const cases = [
      { input: { pendingPath: '/p', remembered: '/r', home: '/h' }, fromMemory: false },
      { input: { pendingHome: true, remembered: '/r', home: '/h' }, fromMemory: false },
      { input: { remembered: '/r', home: '/h' }, fromMemory: true },
      { input: { remembered: '', home: '/h' }, fromMemory: false },
      { input: { home: '/h' }, fromMemory: false },
    ]
    expect(cases.map((c) => resolveRemoteTarget(c.input).fromMemory)).toEqual(cases.map((c) => c.fromMemory))
  })

  it('不 trim、不替调用方兜 "/"：含空格的路径原样返回（Windows 的 D:\\工作\\虚拟 共享）', () => {
    const win = 'D:\\工作\\虚拟 共享'
    expect(resolveRemoteTarget({ pendingPath: win, home: '/h' })).toEqual({ path: win, fromMemory: false })
    // 前后空格也不动：' /x ' 是非空显式路径，不是空串
    expect(resolveRemoteTarget({ pendingPath: ' /x ', remembered: '/r', home: '/h' })).toEqual({
      path: ' /x ',
      fromMemory: false,
    })
    expect(resolveRemoteTarget({ remembered: ' /r ', home: '/h' })).toEqual({ path: ' /r ', fromMemory: true })
  })

  it('home 为 undefined 不崩，原样返回（调用方自己有 SftpHome 的 "/" 回落）', () => {
    expect(resolveRemoteTarget()).toEqual({ path: undefined, fromMemory: false })
    expect(resolveRemoteTarget({})).toEqual({ path: undefined, fromMemory: false })
    expect(resolveRemoteTarget({ pendingHome: true })).toEqual({ path: undefined, fromMemory: false })
    expect(resolveRemoteTarget({ remembered: '/r' })).toEqual({ path: '/r', fromMemory: true })
    expect(resolveRemoteTarget({ pendingPath: '/p' })).toEqual({ path: '/p', fromMemory: false })
  })

  it('不改入参', () => {
    const input = { pendingPath: '', pendingHome: false, remembered: '/r', home: '/h' }
    resolveRemoteTarget(input)
    expect(input).toEqual({ pendingPath: '', pendingHome: false, remembered: '/r', home: '/h' })
  })
})
