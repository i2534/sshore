// V5：openConflicts 先写 conflictsFor 再 await SyncRuleConflicts(rule.id)，无序号守卫。
// A 的响应晚于 B 返回时，conflicts 被 A 覆盖而标题仍是 B ⇒ 裁决发到错误规则。
// 仓库其它视图已有同款 seq 守卫（SftpView remoteSeq/localSeq）。这里用可控 promise 模拟乱序返回，
// 通过 sfcHarness 最小挂载真实执行 openConflicts，断言最终 conflicts 属于最后打开的规则。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { ref } from 'vue'
import { runSetup } from './sfcHarness'
import { useLogStore } from '../stores/logs'
import { useSettingsStore } from '../stores/settings'
import {
  newSyncRuleForm, syncRuleToForm, formToSyncRule, validateSyncRuleForm,
  statsOf, ruleLabel, canMirrorDelete, mirrorDeleteHint, shouldRefresh,
  dialogErrorText, shouldSubscribe,
} from '../utils/sync'
import * as App from '../../wailsjs/go/main/App'

vi.mock('../../wailsjs/go/main/App', () => ({
  ListSyncRules: vi.fn(async () => []),
  CreateSyncRule: vi.fn(async () => {}),
  UpdateSyncRule: vi.fn(async () => {}),
  DeleteSyncRule: vi.fn(async () => {}),
  SyncRuleStates: vi.fn(async () => ({})),
  SyncRuleStats: vi.fn(async () => ({})),
  SyncRuleConflicts: vi.fn(async () => []),
  ResolveSyncConflict: vi.fn(async () => {}),
  ConfirmSyncRuleDeletes: vi.fn(async () => {}),
  ListHosts: vi.fn(async () => []),
  PickLocalDir: vi.fn(async () => ''),
  GetSettings: vi.fn(async () => ({})),
  SetSettings: vi.fn(async () => {}),
  SyncWindowBackground: vi.fn(async () => {}),
}))

describe('SyncView 冲突对话框乱序响应（V5）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    App.SyncRuleConflicts.mockReset()
  })

  it('A 晚于 B 返回时，过期响应被丢弃，conflicts 仍属于最后打开的 B', async () => {
    let resolveA, resolveB
    App.SyncRuleConflicts.mockImplementation((id) => {
      if (id === 'A') return new Promise((r) => { resolveA = r })
      if (id === 'B') return new Promise((r) => { resolveB = r })
      return Promise.resolve([])
    })
    const hooks = { activated: [], deactivated: [], unmounted: [] }
    const { openConflicts, conflicts, conflictsFor } = runSetup(new URL('./SyncView.vue', import.meta.url), {
      ref,
      onActivated: (fn) => hooks.activated.push(fn),
      onDeactivated: (fn) => hooks.deactivated.push(fn),
      onUnmounted: (fn) => hooks.unmounted.push(fn),
      ...App,
      useLogStore, useSettingsStore,
      newSyncRuleForm, syncRuleToForm, formToSyncRule, validateSyncRuleForm,
      statsOf, ruleLabel, canMirrorDelete, mirrorDeleteHint, shouldRefresh,
      dialogErrorText, shouldSubscribe,
    }, ['openConflicts', 'conflicts', 'conflictsFor'])

    // 先打开 A，再打开 B（标题/conflictsFor 立刻变成 B）
    const pA = openConflicts({ id: 'A', name: '规则A' })
    const pB = openConflicts({ id: 'B', name: '规则B' })
    // B 的响应先回
    resolveB([{ rel_path: 'b.txt' }])
    await pB
    // A 的响应晚回（乱序）：不得覆盖 B
    resolveA([{ rel_path: 'a.txt' }])
    await pA

    expect(conflictsFor.value.id).toBe('B')
    expect(conflicts.value.length).toBe(1)
    expect(conflicts.value[0].rel_path).toBe('b.txt')
  })
})