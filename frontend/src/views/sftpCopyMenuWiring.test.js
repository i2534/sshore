// SftpView 的「复制 ▸」接线不变量（Task 6）：菜单矩阵位置、三项文案、Linux 分支、
// 置灰判据的所在位置、能力查询挂在既有的那一个 onMounted 里、复制分支在 sel.single 之前。
// 仓库无 jsdom、无 @vue/test-utils（见 sftpDropWiring.test.js 的说明），这一层只能读源码钉形状；
// 作用集合与路径口径的正确性由 utils/copy.test.js 真单测兜底。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./SftpView.vue', import.meta.url), 'utf8')
const tpl = src.slice(src.indexOf('<ContextMenu'), src.indexOf('</ContextMenu>'))
const doActionBody = () => src.match(/async function doAction\(name\)\s*\{([\s\S]*?)\n\}/)[1]

// 从 anchor 处按花括号配对切一块（报告里的 COPY_ITEMS 是 computed(() => { ... })）。
function sliceBlock(text, anchor) {
  const start = text.indexOf(anchor)
  if (start < 0) return ''
  let depth = 0
  for (let i = text.indexOf('{', start); i < text.length; i++) {
    if (text[i] === '{') depth++
    else if (text[i] === '}') { depth--; if (depth === 0) return text.slice(start, i + 1) }
  }
  return ''
}

describe('SftpView 复制子菜单接线', () => {
  it('菜单矩阵两支都插入复制子菜单', () => {
    expect(tpl).toContain('<MenuSubmenu')
    expect(tpl).toContain('label="复制"')
    expect(tpl).toContain('@pick="doAction(')
  })

  it('「复制 ▸」在两支行菜单里都位于「重命名」之前', () => {
    const iRemoteRow = tpl.indexOf("menu.pane === 'remote'")
    const iLocalRow = tpl.indexOf(":key=\"'row-local-remote'\"")
    expect(iRemoteRow, '找不到远程行分支').toBeGreaterThan(-1)
    expect(iLocalRow, '找不到本地行分支').toBeGreaterThan(iRemoteRow)

    for (const [name, block] of [['远程行', tpl.slice(iRemoteRow, iLocalRow)], ['本地行', tpl.slice(iLocalRow)]]) {
      const iCopy = block.indexOf('<MenuSubmenu')
      const iRename = block.indexOf("doAction('rename')")
      expect(iCopy, `${name}没有复制子菜单`).toBeGreaterThan(-1)
      expect(iRename, `${name}找不到重命名`).toBeGreaterThan(-1)
      expect(iCopy, `${name}的复制项必须排在重命名之前`).toBeLessThan(iRename)
    }
  })

  it('空白/面板级菜单不出现复制', () => {
    const blank = tpl.slice(tpl.indexOf('v-if="!menuHasItem"'), tpl.indexOf('<template v-else>'))
    expect(blank.length).toBeGreaterThan(0)
    expect(blank).not.toContain('<MenuSubmenu')
  })

  it('COPY_ITEMS 三项文案与 key 逐字正确', () => {
    const block = sliceBlock(src, 'const COPY_ITEMS = computed')
    expect(block.length, 'COPY_ITEMS 切片为空（锚点或写法变了？）').toBeGreaterThan(50)
    for (const s of ["'copy-name'", "'copy-path'", "'copy-file'", "'文件名'", "'全路径'", "'文件'"]) {
      expect(block, `COPY_ITEMS 缺 ${s}`).toContain(s)
    }
  })

  it('Linux 分支不渲染「文件」：copy-file 只在能力为真时才 push', () => {
    const block = sliceBlock(src, 'const COPY_ITEMS = computed')
    expect(block).toContain('if (clipboardFileSupport.value)')
    expect(block.indexOf('clipboardFileSupport.value')).toBeLessThan(block.indexOf("'copy-file'"))
  })

  it('「文件」的置灰判据写在 script（模板切片内不得出现 connected.value）', () => {
    const block = sliceBlock(src, 'const COPY_ITEMS = computed')
    expect(block).toContain("menu.value.pane === 'remote' && !connected.value")
    expect(tpl).not.toMatch(/connected\.value/) // 既有 sftpMenuWiring 的红线
  })

  it('能力查询挂在既有的那一个 onMounted 内（新增第二个会打红 sftpRestoreWiring）', () => {
    const heads = [...src.matchAll(/onMounted\(async \(\) => \{/g)]
    expect(heads.length, 'onMounted(async () => { 必须仍然只有一个').toBe(1)
    const body = src.match(/onMounted\(async \(\) => \{([\s\S]*?)\n\}\)/)[1]
    expect(body).toContain('ClipboardFileSupport()')
    expect(body).toContain('clipboardFileSupport.value = false')
  })

  it('复制分支位于 sel.single 之前，且两条文本动作都有分派', () => {
    const body = doActionBody()
    const iCopy = body.indexOf("'copy-name'")
    expect(iCopy).toBeGreaterThan(-1)
    expect(body).toContain("'copy-path'")
    expect(body.indexOf('sel.single(')).toBeGreaterThan(-1)
    expect(iCopy, '复制分支必须在 sel.single 之前（否则多选被收成一项）').toBeLessThan(body.indexOf('sel.single('))
  })

  it('作用集合走 utils/copy 纯函数，不在 SftpView 里内联实现', () => {
    expect(src).toContain("from '../utils/copy'")
    expect(doActionBody()).toContain('copyTargetNames(')
    expect(src).toContain('copyPayload(')
  })
})
