// SSR 真渲染（先例 TransferQueue.render.test.js）：钉住首屏只渲染父项、子面板默认收起。
import { describe, it, expect } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import MenuSubmenu from './MenuSubmenu.vue'

const ITEMS = [
  { key: 'copy-name', label: '文件名' },
  { key: 'copy-path', label: '全路径' },
  { key: 'copy-file', label: '文件' },
]

describe('MenuSubmenu 首屏渲染', () => {
  it('渲染父项文案与箭头，子项在收起状态下不出现在 DOM 里', async () => {
    const app = createSSRApp(MenuSubmenu, { label: '复制', items: ITEMS })
    const html = await renderToString(app)

    expect(html).toContain('复制')
    expect(html).toContain('▸')
    expect(html).not.toContain('文件名')
    expect(html).not.toContain('全路径')
  })
})
