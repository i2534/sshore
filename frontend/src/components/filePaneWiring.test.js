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
    // I2：IME 守卫必须挂在 keydown 上（WebKit 在 compositionend 之后才发 keyup，
    // 那时 isComposing 已为 false，keyup 版守卫在 Linux/WebKitGTK 目标上等于没有）。
    expect(filePane).toContain('@keydown.enter="commitPath"')
    expect(filePane).not.toContain('@keyup.enter="commitPath"')
    expect(filePane).toContain('isImeCommit(')
    expect(filePane).toContain("from '../utils/pathbar'")
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

  // I1：单击「..」后 selKeys.length 恒为 0，watch(() => selKeys.length) 的 n→0 守卫不会触发；
  // 所以「点列表空白」与「按 Esc」这两条清空路径必须自己复位 upActive，否则「..」行会留下假高亮。
  it('清空选中（点列表空白 / Esc）同时复位「..」高亮，不留 stale 高亮', () => {
    const blank = filePane.match(/function onListBlankClick\(\)\s*\{([^}]*)\}/)
    expect(blank, '找不到 onListBlankClick').not.toBeNull()
    expect(blank[1]).toContain('upActive.value = false')
    expect(blank[1]).toContain("emit('clear')")
    expect(filePane).toContain('@click.self="onListBlankClick"')

    const kd = filePane.match(/function onPaneKeydown\(e\)\s*\{([^}]*)\}/)
    expect(kd, '找不到 onPaneKeydown').not.toBeNull()
    expect(kd[1]).toContain("e.key === 'Escape'")
    expect(kd[1]).toContain('upActive.value = false')
  })
})

describe('位置弹层', () => {
  it('不再用原生 select，改用分区弹层 + 纯函数分组', () => {
    expect(filePane).not.toMatch(/<select class="pos"/)
    expect(filePane).toContain("from '../utils/positions'")
    expect(filePane).toContain('positionGroups(')
    expect(filePane).toContain('data-role="position"')
  })

  it('对外契约不变：仍是 emit(pick-position, path)，且旧 onPick 已删', () => {
    expect(filePane).toContain("emit('pick-position'")
    expect(filePane).not.toContain('onPick($event)')
  })

  it('弹层显式左对齐（全局 html{text-align:center} 会把它带歪）', () => {
    expect(filePane).toMatch(/\.posmenu\s*\{[^}]*text-align:\s*left/)
  })

  // 钉住"弹层不能被 .pane 的 overflow:hidden 裁掉"：absolute 版在默认窗口下就被裁，
  // 回归时只表现为"展开全部后看不到最后几行"，没有这几条断言会静默通过。
  it('弹层用 position:fixed + 现算锚点（absolute 会被 .pane{overflow:hidden} 裁掉）', () => {
    expect(filePane).toMatch(/\.posmenu\s*\{[^}]*position:\s*fixed/)
    expect(filePane).toMatch(/\.posmenu\s*\{[^}]*box-sizing:\s*border-box/)
    expect(filePane).toContain('getBoundingClientRect()')
    expect(filePane).toContain('r.right - POS_MENU_WIDTH') // 锚点与宽度必须同源
    expect(filePane).toContain("width: POS_MENU_WIDTH + 'px'")
  })

  it('点面板内非弹层区域收起弹层（焦点会落到 .pane 自身，focusout 判不出"离开"）', () => {
    expect(filePane).toContain('@mousedown="onPaneMouseDown"')
    expect(filePane).toMatch(/function onPaneMouseDown\(e\)\s*\{[^}]*closest\('\.pos-wrap'\)/)
  })
})


