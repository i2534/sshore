// 启动恢复顺序 / 落点决策 / 失效回落 / 取列表函数抽取的接线不变量（Task 11）。
// vitest 环境没有 jsdom/@vue/test-utils（见 sftpDropWiring.test.js 的说明），组件生命周期与
// IPC 无法在测试里真实派发；但这一层的缺陷几乎全是"形状/顺序"缺陷（顺序错了会静默损坏
// last_local_dir、ref 当路径传给后端、级别写成 error 让验收 grep 失真），读源码即可钉死。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./SftpView.vue', import.meta.url), 'utf8')

describe('SFTP 启动恢复 / 落点决策 / 失效回落接线', () => {
  it('onMounted 顺序硬约束：sftpui.load 在 Cwd/loadLocal 之前', () => {
    const body = src.match(/onMounted\(async \(\) => \{([\s\S]*?)\n\}\)/)[1]
    const i = (s) => body.indexOf(s)
    expect(i('await locations.load()')).toBeGreaterThan(-1)
    expect(i('await sftpui.load()')).toBeGreaterThan(i('await locations.load()'))
    expect(i('await Cwd()')).toBeGreaterThan(i('await sftpui.load()'))
    expect(i('await loadLocal()')).toBeGreaterThan(i('await Cwd()'))
  })

  it('本地路径用记忆值优先、失败回落 Cwd', () => {
    const body = src.match(/onMounted\(async \(\) => \{([\s\S]*?)\n\}\)/)[1]
    expect(body.indexOf("lastPathFor('local')")).toBeGreaterThan(-1)
    expect(body.indexOf("lastPathFor('local')")).toBeLessThan(body.indexOf('Cwd()'))
    expect(body).toContain('上次的本地目录') // 回落文案（spec §10 逐字）
    expect(body).toMatch(/warn\(/) // 审核 F7：级别是 warn，不能用 err()（它硬编码 error）
  })

  it('connect/syncConnection 共用 resolveRemoteTarget，且 pendingHome 是显式标志', () => {
    expect(src.match(/resolveRemoteTarget\(/g).length).toBeGreaterThanOrEqual(2)
    expect(src).toContain('pendingHome')
    const home = src.match(/async function goRemoteHome\(\)\s*\{([\s\S]*?)\n\}/)[1] // 只裁函数体，别用跨边界正则
    expect(home).not.toContain("pendingPath.value = ''")
    expect(home).toContain('pendingHome')
  })

  it('落点决策只传 ref 的 .value（审核 F17：传 ref 对象会恒真值，把 ref 当路径交给后端）', () => {
    const calls = [...src.matchAll(/resolveRemoteTarget\(\{([\s\S]*?)\}\)/g)].map((m) => m[1])
    expect(calls.length).toBeGreaterThanOrEqual(2)
    for (const c of calls) {
      expect(c).toContain('pendingPath: pendingPath.value')
      expect(c).toContain('pendingHome: pendingHome.value')
      expect(c).toMatch(/remembered:\s*sftpui\.lastPathFor\('remote', h\)/)
    }
  })

  it('connect 被 sftpui.loaded 兜住，且 load 失败不会冒泡成 unhandled rejection（审核 F8）', () => {
    expect(src).toMatch(/!sftpui\.loaded/)
    expect(src).toMatch(/sftpui\.load\(\)\.catch\(err\)|try\s*\{[^}]*sftpui\.load\(\)/)
  })

  it('loadRemote 的 catch 不外泄 rejection（F1：openRemote/工具栏刷新都不接住它）', () => {
    const fn = src.match(/async function loadRemote\(\)[\s\S]*?\n\}/)[0]
    const tail = fn.slice(fn.lastIndexOf('catch (e) {'))
    expect(tail).toMatch(/remoteOpFailed\(e\)/)
    // 中间态（T13 未落地 remoteOpFailed）与 T13 落地后都必须把 rejection 终结在链内
    expect(tail).toMatch(/\.catch\(err\)/)
  })

  it('排序接线（审核 F9，最后一米）', () => {
    expect((src.match(/@sort="onSort\('/g) || []).length).toBe(2)
    expect(src).toContain(':sort-key="sftpui.sort.local.key"')
    expect(src).toContain(':sort-key="sftpui.sort.remote.key"')
    expect(src).toContain(':sort-dir="sftpui.sort.local.dir"')
    expect(src).toContain(':sort-dir="sftpui.sort.remote.dir"')
  })

  it('loadRemote 成功路径记录远程最后路径（审核 F10）', () => {
    const fn = src.match(/async function loadRemote\(\)[\s\S]*?\n\}/)[0]
    expect(fn).toMatch(/addRemoteRecent\(/)
  })

  it('记忆路径失效回落 home 只在 fromMemory 时发生', () => {
    const fn = src.match(/async function connect\(\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(fn).toContain('fromMemory')
    expect(fn).toMatch(/if \([^)]*fromMemory/) // 回落必须被 fromMemory 守卫（B2：显式指定的路径不许偷偷改道）
    expect(fn).toMatch(/warn\('上次的远程目录/) // 审核 F7：级别是 warn，不是 err()
  })

  it('connect 失败路径清掉两个 pending 标志（F3：残留会让下一次「连接」落错点）', () => {
    const fn = src.match(/async function connect\(\)\s*\{([\s\S]*?)\n\}/)[1]
    const tail = fn.slice(fn.lastIndexOf('catch (e) {'))
    expect(tail).toContain("pendingPath.value = ''")
    expect(tail).toContain('pendingHome.value = false')
  })

  it('syncConnection 回落 home 的第二发不落进静默 catch（F2：零日志 + remotePath 与 items 脱节）', () => {
    const fn = src.match(/async function syncConnection\(\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(fn).toMatch(/catch \(e2\)/) // 第二发有自己的兜错
    expect(fn).toMatch(/err\(e2\)/) // 且必须记原文（§6.5 明写要记日志）
    const iCatch2 = fn.indexOf('catch (e2)')
    const iCommit = fn.indexOf('remotePath.value = home')
    const iReturn = fn.indexOf('return', iCatch2)
    expect(iCommit).toBeGreaterThan(iCatch2) // 失败分支 return 之后才允许提交 home
    expect(iCommit).toBeGreaterThan(iReturn)
  })

  it('warn()/info() helper 的 level 必须是 warn/info（F7 本体：err() 硬编码 error）', () => {
    const warnFn = src.match(/async function warn\(msg\)\s*\{([\s\S]*?)\n\}/)[1]
    const infoFn = src.match(/async function info\(msg\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(warnFn).toContain("level: 'warn'")
    expect(infoFn).toContain("level: 'info'")
  })

  it('取列表函数抽取：loadRemote/loadLocal 包装会抛错的 fetch*', () => {
    expect(src).toContain('async function fetchRemote(')
    expect(src).toContain('async function fetchLocal(')
    expect(src).toMatch(/async function loadRemote\(\)[\s\S]{0,200}fetchRemote\(/)
  })
})
