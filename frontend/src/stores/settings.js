import { defineStore } from 'pinia'
import { GetSettings, SetSettings, SyncWindowBackground } from '../../wailsjs/go/main/App'

// —— 设置项选项（纯数据，便于单测） --------------------------------------------

export const THEMES = [
  { value: 'dark', label: '深色' },
  { value: 'light', label: '浅色' },
  { value: 'system', label: '跟随系统' },
]

export const FONT_SCALES = [
  { value: 0.9, label: '小' },
  { value: 1, label: '标准' },
  { value: 1.15, label: '大' },
]

// 英文字体：语义键 → 具体字体栈。空 value 表示系统默认。
export const LATIN_FONTS = [
  { value: '', label: '系统默认', stack: "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Helvetica Neue', Arial" },
  { value: 'nunito', label: 'Nunito', stack: "'Nunito'" },
  { value: 'inter', label: 'Inter', stack: "'Inter'" },
  { value: 'roboto', label: 'Roboto', stack: "'Roboto'" },
  { value: 'segoe', label: 'Segoe UI', stack: "'Segoe UI'" },
  { value: 'helvetica', label: 'Helvetica Neue', stack: "'Helvetica Neue'" },
  { value: 'arial', label: 'Arial', stack: 'Arial' },
]

// 中文字体：语义键 → 具体字体栈。空 value 表示系统默认。
export const CJK_FONTS = [
  { value: '', label: '系统默认', stack: "'PingFang SC', 'Microsoft YaHei', 'Noto Sans SC', 'Microsoft JhengHei', 'WenQuanYi Micro Hei'" },
  { value: 'yahei', label: '微软雅黑', stack: "'Microsoft YaHei'" },
  { value: 'pingfang', label: '苹方', stack: "'PingFang SC'" },
  { value: 'noto', label: '思源黑体', stack: "'Noto Sans SC', 'Source Han Sans SC'" },
  { value: 'songti', label: '宋体', stack: "'SimSun', 'Songti SC'" },
  { value: 'kaiti', label: '楷体', stack: "'KaiTi', 'Kaiti SC'" },
]

function stackFor(list, value) {
  const it = list.find((f) => f.value === value)
  return it ? it.stack : list[0].stack
}

// 供应用/测试复用：把语义键映射为 CSS font-family 栈
export function latinStack(value) { return stackFor(LATIN_FONTS, value) }
export function cjkStack(value) { return stackFor(CJK_FONTS, value) }

// —— system 主题检测（模块级缓存，避免把 MediaQueryList 塞进响应式 store） ----
let mediaQuery = null

function getMedia() {
  if (!window.matchMedia) return null
  if (!mediaQuery) mediaQuery = window.matchMedia('(prefers-color-scheme: light)')
  return mediaQuery
}

function resolveTheme(theme) {
  if (theme !== 'system') return theme === 'light' ? 'light' : 'dark'
  const m = getMedia()
  return m ? (m.matches ? 'light' : 'dark') : 'dark'
}

// 同步原生窗口背景色（浅色/深色）：尽力而为，失败静默（老 WebView 无此接口）。
function syncWindowBg(theme) {
  try { SyncWindowBackground(theme).catch(() => {}) } catch (e) { /* 静默 */ }
}

// —— Pinia store -------------------------------------------------------------

export const useSettingsStore = defineStore('settings', {
  state: () => ({
    theme: 'system',
    fontScale: 1,
    latinFont: '',
    cjkFont: '',
    autoStartOnLaunch: true,
    loaded: false,
  }),
  actions: {
    // 从后端读取设置并应用到根节点；主题为 system 时监听系统深浅色切换。
    async load() {
      const s = await GetSettings()
      this.theme = s.theme || 'system'
      this.fontScale = Number(s.font_scale) > 0 ? Number(s.font_scale) : 1
      this.latinFont = s.latin_font || ''
      this.cjkFont = s.cjk_font || ''
      this.autoStartOnLaunch = s.auto_start_on_launch !== false
      this.loaded = true
      this.apply()
      this.ensureSystemListener()
    },
    async save() {
      await SetSettings({
        theme: this.theme,
        font_scale: this.fontScale,
        latin_font: this.latinFont,
        cjk_font: this.cjkFont,
        auto_start_on_launch: this.autoStartOnLaunch,
      })
    },
    // 把当前状态写到根节点（data-theme / --ui-scale / --font-latin / --font-cjk）
    apply() {
      document.documentElement.setAttribute('data-theme', resolveTheme(this.theme))
      document.documentElement.style.setProperty('--ui-scale', String(this.fontScale))
      document.documentElement.style.setProperty('--font-latin', latinStack(this.latinFont))
      document.documentElement.style.setProperty('--font-cjk', cjkStack(this.cjkFont))
      syncWindowBg(resolveTheme(this.theme))
    },
    // 仅在 theme 为 system 时关注系统深浅色变化（监听器幂等注册一次）
    ensureSystemListener() {
      if (mediaQuery) return
      const m = getMedia()
      if (!m) return
      mediaQuery = m
      m.addEventListener('change', () => {
        if (this.theme === 'system') this.apply()
      })
    },
  },
})
