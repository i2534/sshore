import { describe, it, expect } from 'vitest'
import { planTasks, classify, copyName, applyPolicy, needsConfirm, summarize, nextTransferSeq, transferID, POLICY_SKIP, POLICY_RENAME }
  from './batch'

const tasks = planTasks({ direction: 'upload', names: ['a.txt', 'b/c'], sourceDir: '/l', targetDir: '/r' })
  .map((t) => ({ ...t, isDir: t.name === 'b/c' }))

describe('batch 冲突策略', () => {
  it('planTasks 拼出 src/dst', () => {
    const t = planTasks({ direction: 'download', names: ['a.txt'], sourceDir: '/r', targetDir: '/l' })[0]
    expect(t.src).toBe('/r/a.txt')
    expect(t.dst).toBe('/l/a.txt')
  })

  it('planTasks 用 isDirMap 标记目录项（决定执行时走递归传输）', () => {
    const t = planTasks({ direction: 'upload', names: ['logs'], sourceDir: '/l', targetDir: '/r', isDirMap: { logs: true } })[0]
    expect(t.isDir).toBe(true)
  })

  it('classify 用原始名字集合判重（含隐藏文件）', () => {
    const { clean, conflicts } = classify(tasks, ['a.txt'])
    expect(conflicts.map((t) => t.name)).toEqual(['a.txt'])
    expect(clean.map((t) => t.name)).toEqual(['b/c'])
  })

  it('copyName 保留扩展名，目录不加后缀', () => {
    expect(copyName('a.txt', ['a.txt'])).toBe('a (2).txt')
    expect(copyName('a.txt', ['a.txt', 'a (2).txt'])).toBe('a (3).txt')
    expect(copyName('logs', ['logs'])).toBe('logs (2)')
  })

  it('跳过策略：冲突项进 skipped，不产生 rename', () => {
    const { conflicts } = classify(tasks, ['a.txt'])
    const r = applyPolicy(conflicts, POLICY_SKIP, ['a.txt'])
    expect(r.run).toEqual([])
    expect(r.skipped.map((t) => t.name)).toEqual(['a.txt'])
  })

  it('另存副本：目标改名，跳过项为空', () => {
    const { conflicts } = classify(tasks, ['a.txt'])
    const r = applyPolicy(conflicts, POLICY_RENAME, ['a.txt'])
    expect(r.skipped).toEqual([])
    expect(r.run[0].dst).toBe('/r/a (2).txt')
  })

  it('弹框条件：有冲突或被过滤隐藏项才弹', () => {
    expect(needsConfirm({ conflictCount: 0, hiddenSelected: 0 })).toBe(false)
    expect(needsConfirm({ conflictCount: 1, hiddenSelected: 0 })).toBe(true)
    expect(needsConfirm({ conflictCount: 0, hiddenSelected: 2 })).toBe(true)
  })

  // Task 9：绑定 SftpGet/SftpPut 的首参是传输 id（不再是 host）。错的 id 不会在
  // npm run build 阶段报错，只会在运行期把 host 当 id，取消/续传全部失联 —— 所以
  // id 的格式与唯一性必须在这里钉住。
  it('transferID 一批共享前缀、批内序号递增（绑定首参，绝不能错位成 host）', () => {
    const seq = nextTransferSeq()
    expect(transferID(seq, 0)).toBe('t' + seq + '-0')
    expect(transferID(seq, 1)).toBe('t' + seq + '-1')
    // 每一批都是新前缀：跨批的 id 绝不重复（取消/事件关联靠它）
    expect(nextTransferSeq()).toBe(seq + 1)
  })

  it('汇总失败清单可读', () => {
    const s = summarize([
      { name: 'a', status: '完成' },
      { name: 'b', status: '跳过' },
      { name: 'c', status: '失败', src: '/l/c', dst: '/r/c', reason: 'Permission denied' },
    ])
    expect(s.ok).toBe(1); expect(s.skipped).toBe(1); expect(s.failed).toBe(1)
    expect(s.failures[0]).toContain('Permission denied')
  })
})
