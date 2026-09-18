import { describe, it, expect, beforeEach, beforeAll, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import {
  useSettingsStore,
  THEMES,
  FONT_SCALES,
  LATIN_FONTS,
  CJK_FONTS,
  latinStack,
  cjkStack,
} from './settings'

// 把 Wails 绑定挡在测试之外：store 的 load()/save() 会直接调用
// GetSettings/SetSettings/SyncWindowBackground，真跑到它们会去摸 window.go。
// vi.hoisted 让假后端在 vi.mock 工厂之前就绪（仓库此前没有 mock 绑定的先例）。
const backend = vi.hoisted(() => ({
  settings: {},
  saved: [],
}))
vi.mock('../../wailsjs/go/main/App', () => ({
  GetSettings: async () => backend.settings,
  SetSettings: async (payload) => { backend.saved.push(payload) },
  SyncWindowBackground: async () => {},
}))

// load() 经 apply()/ensureSystemListener() 触碰 DOM；node 环境补最小 stub。
beforeAll(() => {
  globalThis.window = { matchMedia: () => null }
  globalThis.document = { documentElement: { setAttribute() {}, style: { setProperty() {} } } }
})

describe('settings option tables', () => {
  it('每个选项都有 value/label/stack 三元组', () => {
    for (const list of [LATIN_FONTS, CJK_FONTS]) {
      for (const f of list) {
        expect(f).toHaveProperty('value')
        expect(f).toHaveProperty('label')
        expect(f).toHaveProperty('stack')
        expect(f.stack.length).toBeGreaterThan(0)
      }
    }
    expect(THEMES.map((t) => t.value)).toEqual(['dark', 'light', 'system'])
    expect(FONT_SCALES.map((f) => f.value)).toEqual([0.9, 1, 1.15])
  })

  it('拉丁字体栈映射：系统默认与指定字体', () => {
    expect(latinStack('')).toContain('Segoe UI')
    expect(latinStack('inter')).toBe("'Inter'")
    expect(latinStack('nunito')).toBe("'Nunito'")
    expect(latinStack('unknown')).toContain('Segoe UI') // 未知键回退默认
  })

  it('中文字体栈映射：系统默认与指定字体', () => {
    expect(cjkStack('')).toContain('PingFang SC')
    expect(cjkStack('yahei')).toBe("'Microsoft YaHei'")
    expect(cjkStack('pingfang')).toBe("'PingFang SC'")
    expect(cjkStack('noto')).toContain('Noto Sans SC')
    expect(cjkStack('unknown')).toContain('PingFang SC')
  })
})

describe('settings store defaults', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('出厂默认：system 主题、标准字号、字体系统默认、启动自动连接开启', () => {
    const s = useSettingsStore()
    expect(s.theme).toBe('system')
    expect(s.fontScale).toBe(1)
    expect(s.latinFont).toBe('')
    expect(s.cjkFont).toBe('')
    expect(s.autoStartOnLaunch).toBe(true)
    expect(s.autoReconnectDefault).toBe(true)
    expect(s.loaded).toBe(false)
  })
})

describe('settings store auto_reconnect_default 往返', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    backend.settings = {}
    backend.saved.length = 0
  })

  it('save() 必须回传 auto_reconnect_default（漏掉该字段即失败）', async () => {
    const s = useSettingsStore()
    s.autoReconnectDefault = true
    await s.save()
    expect(backend.saved).toHaveLength(1)
    expect(backend.saved[0]).toHaveProperty('auto_reconnect_default')
    expect(backend.saved[0].auto_reconnect_default).toBe(true)
  })

  it('load() 把后端的 true 读进 autoReconnectDefault', async () => {
    backend.settings = { auto_reconnect_default: true }
    const s = useSettingsStore()
    await s.load()
    expect(s.autoReconnectDefault).toBe(true)
  })

  it('load() 不得把后端的显式 false 强转为 true', async () => {
    backend.settings = { auto_reconnect_default: false }
    const s = useSettingsStore()
    await s.load()
    expect(s.autoReconnectDefault).toBe(false)
  })

  it('load() 缺字段（undefined）时按 true 兜底，与 !== false 语义一致', async () => {
    backend.settings = {}
    const s = useSettingsStore()
    await s.load()
    expect(s.autoReconnectDefault).toBe(true)
  })

  it('改值后 save() 原样保持：false→false、true→true', async () => {
    const s = useSettingsStore()
    s.autoReconnectDefault = false
    await s.save()
    expect(backend.saved[backend.saved.length - 1].auto_reconnect_default).toBe(false)
    s.autoReconnectDefault = true
    await s.save()
    expect(backend.saved[backend.saved.length - 1].auto_reconnect_default).toBe(true)
  })
})

describe('settings store sftp_transport 往返', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    backend.settings = {}
    backend.saved.length = 0
  })

  it('save 必须带上 sftp_transport，否则 SetSettings 整结构覆盖会把它清空', async () => {
    const store = useSettingsStore()
    store.sftpTransport = 'gosftp'
    await store.save()
    expect(backend.saved.at(-1).sftp_transport).toBe('gosftp')
  })

  it('load 读回 sftp_transport', async () => {
    backend.settings = { sftp_transport: 'batch' }
    const store = useSettingsStore()
    await store.load()
    expect(store.sftpTransport).toBe('batch')
  })
})
