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

// Task 15：4 个更新设置字段 + 「跳过双写」修复。save() 只回传 7 个旧字段会让
// 后端 SetSettings（整结构覆盖）把 4 个更新字段清零（spec §6 第 2/3 条）。
describe('settings store 更新字段（Task 15）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    backend.settings = {}
    backend.saved.length = 0
  })

  it('load() 缺字段时按默认：auto=true / 12h / 空跳过 / 空源', async () => {
    const s = useSettingsStore()
    await s.load()
    expect(s.updateCheckAuto).toBe(true)
    expect(s.updateCheckIntervalHours).toBe(12)
    expect(s.updateSkippedVersion).toBe('')
    expect(s.updateSource).toBe('')
  })

  it('load() 读入显式值，update_check_auto 的 false 与 interval=0（关闭轮询）不被强转', async () => {
    backend.settings = {
      update_check_auto: false,
      update_check_interval_hours: 0,
      update_skipped_version: 'v0.7.0',
      update_source: 'https://mirror.example.com',
    }
    const s = useSettingsStore()
    await s.load()
    expect(s.updateCheckAuto).toBe(false)
    expect(s.updateCheckIntervalHours).toBe(0)
    expect(s.updateSkippedVersion).toBe('v0.7.0')
    expect(s.updateSource).toBe('https://mirror.example.com')
  })

  it('save() 必须回传全部 4 个更新字段（漏掉任一即被后端清零）', async () => {
    const s = useSettingsStore()
    s.updateCheckAuto = false
    s.updateCheckIntervalHours = 48
    s.updateSkippedVersion = '0.9.0'
    s.updateSource = 'https://mirror.example.com'
    await s.save()
    expect(backend.saved.at(-1)).toMatchObject({
      update_check_auto: false,
      update_check_interval_hours: 48,
      update_skipped_version: '0.9.0',
      update_source: 'https://mirror.example.com',
    })
  })

  it("跳过版本不会被后续的主题保存清掉（评审发现的真实缺陷）", async () => {
    backend.settings = {
      theme: "system", font_scale: 1,
      update_check_auto: true, update_check_interval_hours: 12,
      update_skipped_version: "0.7.0", update_source: "",
    };
    const s = useSettingsStore();
    await s.load();          // 前端拿到含 0.7.0 的快照
    s.theme = "dark";
    await s.save();          // 全量回写
    expect(backend.saved.at(-1).update_skipped_version).toBe("0.7.0");
  });

  it("跳过版本会在动作后被重读进前端快照", async () => {
    backend.settings = { update_skipped_version: "" };
    const s = useSettingsStore();
    await s.load();
    backend.settings = { update_skipped_version: "0.8.0" };  // 后端被 SkipeVersion 直接改了
    await s.load();
    expect(s.updateSkippedVersion).toBe("0.8.0");
  });

  it('resetUpdateSettings() 把 4 个字段复位为默认（auto=true / 12h / 空跳过 / 空源）', () => {
    const s = useSettingsStore()
    s.updateCheckAuto = false
    s.updateCheckIntervalHours = 168
    s.updateSkippedVersion = '0.9.0'
    s.updateSource = 'https://mirror.example.com'
    s.resetUpdateSettings()
    expect(s.updateCheckAuto).toBe(true)
    expect(s.updateCheckIntervalHours).toBe(12)
    expect(s.updateSkippedVersion).toBe('')
    expect(s.updateSource).toBe('')
  })
})
