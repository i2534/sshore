// ContextMenu.vue 的样式形状不变量（仓库无 jsdom、无 @vue/test-utils，:disabled 的
// 视觉/不可点行为派发不出来，只能读源码钉死 CSS 规则，仿 components/filePaneWiring.test.js）。
// T10 只加禁用态样式；置灰项本身由 T12（SftpView 菜单矩阵）接线。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./ContextMenu.vue', import.meta.url), 'utf8')
const css = (src.match(/<style scoped>([\s\S]*?)<\/style>/) || ['', ''])[1]

describe('ContextMenu 菜单项禁用态（spec §5.4）', () => {
  it('样式块存在且是 scoped', () => {
    expect(css).not.toBe('')
    expect(src).toMatch(/<style scoped>/)
  })

  it('button:disabled = 置灰 + 不可点（缺 cursor 会让人以为还能点）', () => {
    expect(css).toMatch(/button:disabled\)\s*\{/)
    expect(css).toMatch(/button:disabled\)\s*\{[^}]*color:\s*var\(--text-faint\)/)
    expect(css).toMatch(/button:disabled\)\s*\{[^}]*cursor:\s*default/)
  })

  it('button:disabled:hover 不复用 hover 底色（否则禁用项看起来仍可点）', () => {
    // 既有 `button:hover` 是 (0,2,2) 量级；禁用态 hover 若不显式覆盖，
    // 鼠标划过灰项仍会高亮 —— 与「置灰即不可点」的语义直接冲突。
    expect(css).toMatch(/button:disabled:hover\)\s*\{/)
    expect(css).toMatch(/button:disabled:hover\)\s*\{[^}]*background:\s*none/)
  })

  it('禁用态写在 :deep() 内（scoped 下不加 :deep 命不中插槽传进来的 button）', () => {
    expect(css).toMatch(/\.menu\s+:deep\(button:disabled\)/)
    expect(css).toMatch(/\.menu\s+:deep\(button:disabled:hover\)/)
  })
})
