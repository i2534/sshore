// MenuSubmenu.vue 的接线不变量（仓库无 jsdom、无 @vue/test-utils：hover/定时器/尺寸
// 测量派发不出来，但这一层的缺陷全是"形状"缺陷，读源码即可钉死，仿 filePaneWiring.test.js）。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./MenuSubmenu.vue', import.meta.url), 'utf8')
const tpl = src.slice(src.indexOf('<template>'), src.indexOf('</template>'))

describe('MenuSubmenu 结构与交互接线', () => {
  it('两个延迟常量逐字可读（悬停 120ms 展开、离开 160ms 收起）', () => {
    expect(src).toMatch(/const OPEN_DELAY = 120\b/)
    expect(src).toMatch(/const CLOSE_DELAY = 160\b/)
  })

  it('鼠标进出挂在根元素上（覆盖父项 + 子面板，否则移向子面板途中会提前收起）', () => {
    // 锚定第一个 div 的**开标签**（tpl.indexOf('>') 会命中 <template> 自身的 '>'）。
    const divStart = tpl.indexOf('<div')
    const root = tpl.slice(divStart, tpl.indexOf('>', divStart))
    expect(root).toContain('@mouseenter')
    expect(root).toContain('@mouseleave')
    expect(root).toContain('menusub')
  })

  it('父项点击只做开合，不派发 pick', () => {
    const fn = src.match(/function toggle\(\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(fn).toContain('!open.value')
    expect(fn).not.toContain("emit('pick'")
    expect(tpl).toMatch(/class="menusub-head"[\s\S]{0,80}@click="toggle"/)
  })

  it('先渲染再测量：nextTick 出现在 getBoundingClientRect 之前', () => {
    expect(src).toContain('nextTick')
    expect(src.indexOf('nextTick')).toBeLessThan(src.indexOf('getBoundingClientRect'))
  })

  it('面板 ref 为空时有兜底，不抛 TypeError', () => {
    expect(src).toContain('if (!panel.value)')
  })

  it('定位走纯函数，视口取 window.innerWidth / innerHeight', () => {
    expect(src).toContain("from '../utils/menu'")
    expect(src).toContain('submenuPlacement(')
    expect(src).toContain('window.innerWidth')
    expect(src).toContain('window.innerHeight')
  })

  it('子项按 items 渲染：disabled 透传、点击派发 key、随后收起', () => {
    expect(tpl).toContain('v-for="it in items"')
    expect(tpl).toContain(':disabled="it.disabled"')
    expect(tpl).toContain('@click="pick(it)"')
    const fn = src.match(/function pick\(it\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(fn).toContain("emit('pick', it.key)")
    expect(fn).toContain('open.value = false')
    expect(fn).toContain('it.disabled') // 禁用项直接返回，不派发
  })

  it('卸载时清掉两个定时器', () => {
    const fn = src.match(/function clearTimers\(\)\s*\{([\s\S]*?)\n\}/)[1]
    expect((fn.match(/clearTimeout\(/g) || []).length).toBe(2)
    expect(src).toMatch(/onUnmounted\(/)
  })
})
