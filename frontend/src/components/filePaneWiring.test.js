// FilePane / SftpView 的形状不变量（仓库无 jsdom / 无 @vue/test-utils，
// 组件事件无法派发，跨文件形状靠读源码钉死，仿 views/sftpDropWiring.test.js）。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const filePane = readFileSync(new URL('./FilePane.vue', import.meta.url), 'utf8')
const sftp = readFileSync(new URL('../views/SftpView.vue', import.meta.url), 'utf8')

describe('路径栏（可编辑）', () => {
  it('FilePane 用可编辑 input 承载路径，并上抛 navigate 原文', () => {
    expect(filePane).toContain('data-role="path"')
    expect(filePane).toMatch(/<input[^>]*data-role="path"/)
    expect(filePane).not.toContain('{{ path }}</span>')
    expect(filePane).toContain('@keyup.enter="commitPath"')
    expect(filePane).toContain("@keyup.esc=\"resetPath\"")
    expect(filePane).toContain("emit('navigate'")
    expect(filePane).toMatch(/defineEmits\(\[[^\]]*'navigate'/) // 新增事件必须在 emits 里声明（否则只有 dev 警告，测试不红）
  })

  it('SftpView 两个面板都接 navigate，并按面板分派', () => {
    expect(sftp).toContain("onNavigate('local'")
    expect(sftp).toContain("onNavigate('remote'")
    expect(sftp).toMatch(/async function onNavigate\(pane, raw\)/)
  })

  it('REMOTE_HOME 只有 pathbar.js 一个来源', () => {
    expect(sftp).not.toMatch(/const REMOTE_HOME = '~'/)
    expect(sftp).toContain("from '../utils/pathbar'")
  })

  it('本地加载与 goLocal 共用同一个 localSeq（否则旧的失败回滚会覆盖更新的导航）', () => {
    expect((sftp.match(/\+\+localSeq/g) || []).length).toBe(2) // goLocal + loadLocal
  })
})

describe('「..」行与普通目录行统一', () => {
  it('up 行是「单击高亮 + 双击打开」，不再单击直接 open', () => {
    const i = filePane.indexOf('class="up"')
    expect(i, '找不到 up 行').toBeGreaterThan(-1)
    // 只截 up 行自身：上界 200 保证不会切进下一行的 @dblclick="emit('open', it)"
    const up = filePane.slice(i, i + 200)
    expect(up).toContain('@dblclick="openUp"')
    expect(up).toContain('@click="onUpClick"')
    expect(up).not.toContain("emit('open'")
  })

  it('「..」不进 selKeys：单击只清空选中并置本地高亮', () => {
    expect(filePane).toMatch(/function onUpClick\(\)\s*\{[^}]*emit\('clear'\)/)
    expect(filePane).toContain('const upActive = ref(false)')
    // sel.* 全在父层调用；父层绝不能把 '..' 放进选择集，否则 runBatchFor 会把它当成传输项
    expect(sftp).not.toMatch(/sel\.(single|toggle|rangeTo)\([^)]*'\.\.'/)
  })
})

