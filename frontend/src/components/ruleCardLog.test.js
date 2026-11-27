// V3：RuleCard 启停失败日志的 source_id 写死 'rule'，而「日志」按钮把 filterSource 设为
// tunnel.id，LogPanel 精确匹配 ⇒ 恰好这些错误被过滤掉、永远看不见。SyncCard 已修过同款（S7）。
// 仓库无 jsdom/@vue/test-utils：用 sfcHarness 最小挂载真实执行 script setup，
// defineProps/defineEmits 以替身注入，断言落在真实日志条目与真实过滤结果上。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { ref, computed } from 'vue'
import { runSetup } from '../views/sfcHarness'
import { useLogStore } from '../stores/logs'
import { modeLabel } from '../utils/forward'
import { StartTunnel, StopTunnel } from '../../wailsjs/go/main/App'

vi.mock('../../wailsjs/go/main/App', () => ({
  StartTunnel: vi.fn(async () => {}),
  StopTunnel: vi.fn(async () => {}),
}))

describe('RuleCard 启停失败日志 source_id（V3）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    StartTunnel.mockClear()
    StopTunnel.mockClear()
  })

  it('启动失败后日志 source_id === 规则 id，且按规则过滤时这条错误可见', async () => {
    StartTunnel.mockRejectedValueOnce(new Error('boom'))
    const tunnel = { id: 'tun-42', name: 'web', host: 'h', enabled: false, mode: 'local', listen_bind: '127.0.0.1', listen_port: 8080 }
    const emit = vi.fn()
    const { toggle, toggleLog } = runSetup(new URL('./RuleCard.vue', import.meta.url), {
      ref, computed,
      defineProps: () => ({ tunnel, state: 'stopped' }),
      defineEmits: () => emit,
      StartTunnel, StopTunnel, useLogStore, modeLabel,
    }, ['toggle', 'toggleLog'])

    const logStore = useLogStore()
    await toggle()

    const entry = logStore.logs.at(-1)
    expect(entry.level).toBe('error')
    // 核心字面量断言：必须是该规则 id，而不是恒定的 'rule'
    expect(entry.source_id).toBe('tun-42')

    // 「日志」按钮把 filterSource 设为规则 id；LogPanel 精确匹配 ⇒ 这条错误必须留在过滤结果里
    toggleLog()
    expect(logStore.filterSource).toBe('tun-42')
    expect(logStore.filtered.length).toBe(1)
    expect(logStore.filtered[0].source_id).toBe('tun-42')
  })
})