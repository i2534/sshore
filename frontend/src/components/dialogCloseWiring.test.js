// 5 个模态框的关闭路径不变量（Task 8）：只能通过显式按钮关闭。
// 断言收进 <template> 切片 —— 注释里出现的 `keyup.esc` 字面量会让全文断言假阳性
// （ConflictDialog/SearchOverlay 的注释里原本就有），本仓既有口径也是"不做文件级 not.toContain"。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const MODALS = ['AppDialog.vue', 'ConflictDialog.vue', 'SearchOverlay.vue', 'SettingsDialog.vue', 'SyncConflictsDialog.vue']
const read = (f) => readFileSync(new URL('./' + f, import.meta.url), 'utf8')
const tplOf = (src) => src.slice(src.indexOf('<template>'), src.lastIndexOf('</template>'))

describe('模态框只能通过显式按钮关闭', () => {
  it('模板里不再有遮罩点击关闭', () => {
    for (const f of MODALS) expect(tplOf(read(f)), `${f} 仍有 @click.self`).not.toContain('@click.self')
  })

  it('模板里不再有 Esc 关闭（AppDialog 的 prompt 输入框也在内）', () => {
    for (const f of MODALS) expect(tplOf(read(f)), `${f} 仍有 keyup.esc`).not.toContain('keyup.esc')
  })

  it('过时注释里的 keyup.esc 也清掉（否则"全文不含"会假阳性）', () => {
    for (const f of ['ConflictDialog.vue', 'SearchOverlay.vue']) {
      expect(read(f), `${f} 注释里仍提 keyup.esc`).not.toContain('keyup.esc')
    }
  })

  it('每个模态框的显式关闭按钮仍在', () => {
    const app = tplOf(read('AppDialog.vue'))
    // 确认按钮走 confirm()（script 里 emit('ok')），模板里能看到的显式路径是这两个：
    expect(app).toContain('@click="confirm"')
    expect(app).toContain("emit('cancel'")
    const conflict = tplOf(read('ConflictDialog.vue'))
    expect(conflict).toContain("emit('cancel'")
    expect(conflict).toContain("emit('confirm'")
    expect(tplOf(read('SearchOverlay.vue'))).toContain("emit('close'")
    expect(tplOf(read('SettingsDialog.vue'))).toContain("emit('close'")
    expect(tplOf(read('SyncConflictsDialog.vue'))).toContain("emit('close'")
  })

  it('AppDialog 仍保留 Enter 提交（只去 Esc，不动键盘提交）', () => {
    expect(tplOf(read('AppDialog.vue'))).toContain('@keyup.enter="confirm"')
  })

  it('不误伤 FilePane：路径栏 Esc 与列表空白点击不在本轮范围', () => {
    const fp = read('./FilePane.vue')
    expect(fp).toContain('@click.self="onListBlankClick"')
    expect(fp).toContain('@keyup.esc="resetPath"')
  })
})
