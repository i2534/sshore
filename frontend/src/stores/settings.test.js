import { describe, it, expect, beforeEach } from 'vitest'
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
    expect(s.loaded).toBe(false)
  })
})
