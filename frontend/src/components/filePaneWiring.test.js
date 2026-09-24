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
})
