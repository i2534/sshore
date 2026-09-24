import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8')

describe('控件尺寸的单一来源', () => {
  const css = read('./style.css')
  // 注意：CSS 里这条选择器写成多行，必须用正则匹配，精确子串会切不到
  const heightBlock = () => (css.split(/button,\s*input:not\(\[type="checkbox"\]\):not\(\[type="radio"\]\),\s*select\s*\{/)[1] || '').split('}')[0]
  const baseBlock = () => (css.split(/^button, input, select \{/m)[1] || '').split('}')[0]

  it('--ctl-h 只定义一次，且随 --ui-scale 缩放', () => {
    const hits = css.match(/--ctl-h:\s*[^;]+;/g) || []
    expect(hits).toHaveLength(1)
    expect(hits[0]).toContain('var(--ui-scale)')
  })
  it('高度规则只含盒模型属性（不能含视觉属性，否则压过 scoped 的 .curpath）', () => {
    const b = heightBlock()
    expect(b).toContain('box-sizing: border-box')
    expect(b).toContain('height: var(--ctl-h)')
    expect(b).toContain('line-height')
    expect(b).not.toContain('background')
    expect(b).not.toContain('border-radius')
    expect(b).not.toMatch(/border:\s*1px/)
  })
  it('元素级规则仍是 button, input, select（低特异性），padding 改成 0 10px', () => {
    const b = baseBlock()
    expect(b).toContain('padding: 0 10px')
    expect(b).not.toContain('height: var(--ctl-h)')   // 高度只在上面那条规则里
  })
  it('复选框/单选框的 native 口径保留（不被统一高度波及）', () => {
    expect(css).toMatch(/input\[type="checkbox"\], input\[type="radio"\]\s*\{/)
  })
  it('菜单类/胶囊类/图标类按钮显式 opt-out（清单见计划表，一处都不能少）', () => {
    const pane = read('./components/FilePane.vue')
    expect(pane).toMatch(/\.positem\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.poslabel \.more\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.batch \.chip\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.bmenu button\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.mini\s*\{[^}]*height:\s*auto/)
    expect(pane).toMatch(/\.fx\s*\{[^}]*height:\s*auto/)
    expect(read('./components/LogPanel.vue')).toMatch(/\.chip\s*\{[^}]*height:\s*auto/)
    expect(read('./components/TransferQueue.vue')).toMatch(/\.act\s*\{[^}]*height:\s*auto/)
    expect(read('./components/TransferQueue.vue')).toMatch(/\.copy\s*\{[^}]*height:\s*auto/)
    expect(read('./components/ContextMenu.vue')).toMatch(/:deep\(button\)\s*\{[^}]*height:\s*auto/)
    expect(read('./App.vue')).toMatch(/\.sidebar button\s*\{[^}]*height:\s*auto/)
    expect(read('./App.vue')).toMatch(/\.fatal-close\s*\{[^}]*height:\s*auto/)
    expect(read('./components/SettingsDialog.vue')).toMatch(/\.dclose\s*\{[^}]*height:\s*auto/)
    expect(read('./components/UpdateSection.vue')).toMatch(/\.link\s*\{[^}]*height:\s*auto/)
  })
  it('右键菜单项显式左对齐（全局 button{justify-content:center} 会把图标+文字整行居中）', () => {
    // I3：T3 fix 轮加过 justify-content: flex-start，但此前只钉了 height: auto，
    // 删掉 flex-start 仍全绿——补这条断言后删实现即红。
    expect(read('./components/ContextMenu.vue')).toMatch(/\.menu :deep\(button\)[^}]*justify-content:\s*flex-start/)
  })
  it('列宽魔数不再散落（88px/140px 只作为 --col-size/--col-time 的值出现）', () => {
    const pane = read('./components/FilePane.vue')
    expect(pane.match(/grid-template-columns:[^;]*\b(88px|140px)\b/g) || []).toHaveLength(0)
  })
})
