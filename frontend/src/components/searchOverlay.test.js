// SearchOverlay：SSR 初始渲染 + 「打开不自动搜索」的结构不变量。
// 仓库无 jsdom / 无 @vue/test-utils：可渲染的断言走 renderToString，跨文件形状靠读源码。
import { describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import SearchOverlay from './SearchOverlay.vue'
import { EMPTY_PATTERN_HINT } from '../utils/searchbox'

vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }))
vi.mock('../../wailsjs/go/main/App', () => ({
  SftpSearch: async () => ({}), LocalSearch: async () => ({}), SearchCancel: () => {},
}))

const src = readFileSync(new URL('./SearchOverlay.vue', import.meta.url), 'utf8')

describe('SearchOverlay：打开时不得自动全量搜索', () => {
  it('SSR 打开态渲染「先输入」提示，且没有任何命中行', async () => {
    const html = await renderToString(createSSRApp({
      render: () => h(SearchOverlay, { visible: true, root: '/', scope: 'remote', host: 'h' }),
    }))
    expect(html).toContain(EMPTY_PATTERN_HINT)
    expect(html).not.toContain('<li')
  })

  it('打开分支不再调用 run()', () => {
    const start = src.indexOf('watch(() => props.visible')
    // 结束点必须带 '() => {'：文件顶部的 import 行也含 'onUnmounted('，不带后缀会切出空串让断言假绿
    const end = src.indexOf('onUnmounted(() => {', start)
    expect(start).toBeGreaterThan(-1)
    expect(end).toBeGreaterThan(start)
    const watchBlock = src.slice(start, end)
    expect(watchBlock).not.toContain('run()')
    expect(watchBlock).toContain('EMPTY_PATTERN_HINT')
  })

  it('run() 用 shouldSearch 守卫空模式', () => {
    const body = src.slice(src.indexOf('async function run()'), src.indexOf('function cancel()'))
    expect(body).toContain('shouldSearch(')
  })

  it('结果区限高，浮层不再吃掉整屏', () => {
    expect(src).toMatch(/\.hits\s*\{[^}]*max-height/)
    expect(src).toContain('data-role="hits"')
  })
})
