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

  // M1：选择清空后 selKeys.length 归零，`v-if="selKeys.length"` 会把 .batch 整个隐藏，
  // 但 menuOpen 仍是 true；用户重新选中时菜单「未点先弹」。必须在归零时复位 menuOpen。
  it('选择归零时批量菜单 menuOpen 一并复位，重新选中不会未点先弹', () => {
    const m = filePane.match(/watch\(\(\) => props\.selKeys\.length,\s*\(n\)\s*=>\s*\{[^}]*menuOpen\.value = false[^}]*\}\)/)
    expect(m, '找不到把 menuOpen 置 false 的 selKeys.length watch').not.toBeNull()
    // 判别力：删掉这条 watch 即红；且它必须只在归零（!n）时生效
    expect(m[0]).toMatch(/if\s*\(!n\)/)
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

  it('弹层宽度收窄到 220px，且锚点算法与内联宽度用同一个常量', () => {
    expect(filePane).toMatch(/POS_MENU_WIDTH\s*=\s*220/)
    expect(filePane).toContain('r.right - POS_MENU_WIDTH')
  })

  it('弹层子项显式 border-box（防御性组件不变量：横向溢出实由 Task 3 全局 box-sizing 消除）', () => {
    expect(filePane).toMatch(/\.positem\s*\{[^}]*box-sizing:\s*border-box/)
    expect(filePane).toMatch(/\.posfilter\s*\{[^}]*box-sizing:\s*border-box/)
  })

  it('点面板内非弹层区域收起弹层（焦点会落到 .pane 自身，focusout 判不出"离开"）', () => {
    expect(filePane).toContain('@mousedown="onPaneMouseDown"')
    expect(filePane).toMatch(/function onPaneMouseDown\(e\)\s*\{[^}]*closest\('\.pos-wrap'\)/)
  })

  // W1（Windows/WebView2）：旧写法 v-if="g.total > g.rows.length" 让「展开全部」在展开后
  // 从 DOM 消失；Chromium 在焦点节点被移除时给子树发 focusout(relatedTarget=null)，
  // onPaneFocusOut 便把整个弹层收起（Linux/WebKit 时序不同，故此前漏检）。
  // 不变量：控件是否渲染只看 g.expandable（与 expanded 无关），且展开后控件仍在、文案变「收起」。
  it('展开控件以 g.expandable 为唯一渲染条件，节点不随展开消失', () => {
    const more = filePane.match(/<button[^>]*class="more"[^>]*>[^<]*<\/button>/)
    expect(more, '找不到 .more 展开/收起按钮').not.toBeNull()
    expect(more[0]).toContain('v-if="g.expandable"')
    // 回归守卫：这两条一旦回来，就说明又用「行数」当渲染条件了
    expect(filePane).not.toContain('g.total > g.rows.length')
    expect(filePane).not.toMatch(/class="more"[^>]*v-if="[^"]*rows\.length/)
  })

  it('展开控件是 toggle：展开态可点「收起」，且不再只有 expandGroup', () => {
    expect(filePane).toContain('toggleGroup(g.key)')
    expect(filePane).toMatch(/function toggleGroup\(key\)\s*\{/)
    expect(filePane).toContain("'收起'")
    expect(filePane).toContain('展开全部 ')
    expect(filePane).not.toContain('expandGroup(g.key)')
  })
})
describe('面板四段结构与列共轨', () => {
  // 抠出列头行那一小段：不要用 split('list-holder') 切，列头行改到 .list-holder 内部后那样会切错
  const hdrowOf = () => (filePane.match(/<div class="hdrow" data-role="columns">[\s\S]*?<\/div>/) || [''])[0]

  it('列头与列表共用同一套 grid 轨道（宽度只来自两个变量，没有字面量列宽）', () => {
    expect(filePane).toMatch(/--col-size:\s*88px/)
    expect(filePane).toMatch(/--col-time:\s*140px/)
    const grids = filePane.match(/grid-template-columns:\s*minmax\(0,1fr\) var\(--col-size\) var\(--col-time\)/g) || []
    expect(grids).toHaveLength(2) // .hdrow 与 .list li 各一条（grid 轨道必须逐选择器声明）
    expect(filePane.match(/grid-template-columns:[^;]*\b(88px|140px)\b/g) || []).toHaveLength(0)
  })
  it('表头与列表在同一个滚动容器里，表头 sticky（否则出现纵向滚动条时两行又会差一个滚动条宽）', () => {
    const holder = filePane.split('class="list-holder"')[1] || ''
    expect(holder).toContain('data-role="columns"')
    expect(holder).toContain('class="list"')
    expect(filePane).toMatch(/\.hdrow\s*\{[^}]*position:\s*sticky/)
    expect(filePane).toMatch(/\.list-holder\s*\{[^}]*overflow:\s*auto/)
    expect(filePane).toMatch(/\.list\s*\{[^}]*flex:\s*none/)
  })
  it('列头行只放三个列头、不再含输入框、且 nowrap', () => {
    const hd = hdrowOf()
    expect(hd).toContain('col-name')
    expect(hd).toContain('col-size')
    expect(hd).toContain('col-time')
    expect(hd).not.toContain('<input')
    expect(filePane).toMatch(/\.hdrow\s*\{[^}]*white-space:\s*nowrap/)
  })
  it('.list li 就地改成 grid（不要另写合并选择器，否则与旧 flex 规则同特异性、谁在后面谁赢）', () => {
    expect(filePane).toMatch(/\.list li\s*\{[^}]*display:\s*grid/)
    expect(filePane).not.toMatch(/\.hdrow, \.list li/)
  })
  it('底部过滤栏承载过滤框 + 计数；批量入口只在有选中时出现', () => {
    const bar = filePane.split('data-role="filterbar"')[1] || ''
    expect(bar).toContain('data-role="filter"')
    expect(bar).toContain('data-role="selinfo"')
    expect(bar).toContain('{{ selLabel }}')
    expect(filePane).toContain('selectionLabel(')      // 在 <script> 里，别限定在 bar 内断言
    expect(filePane).toMatch(/v-if="selKeys\.length"/)
    expect(filePane).not.toContain('selKeys.length || actions.length')
  })
  it('导航行只留 标题/路径/位置/收藏/深搜（计数与批量已下移）', () => {
    const nav = filePane.split('class="navrow"')[1].split('data-role="columns"')[0]
    expect(nav).toContain('data-role="path"')
    expect(nav).toContain('data-role="position"')
    expect(nav).toContain("emit('search')")
    expect(nav).not.toContain('class="count"')
    expect(nav).not.toContain('class="batch"')
  })
  it('过滤栏的批量菜单向上弹出（面板 overflow:hidden 会裁掉向下的菜单）', () => {
    expect(filePane).toMatch(/\.fbar \.bmenu\s*\{[^}]*bottom:\s*100%/)
    expect(filePane).toMatch(/\.fbar \.bmenu\s*\{[^}]*top:\s*auto/)
  })
})

describe('排序状态上收（props + emit sort）', () => {
  it('排序读 props：无本地 ref、无 *.value 残留、emits 含 sort', () => {
    expect(filePane).not.toContain('const sortKey = ref(')
    expect(filePane).not.toContain('const sortDir = ref(')
    // 审核 FF1（Blocking）：arrow() 与 visible() 同作用域，删 ref 只改其一 ⇒ 渲染期 ReferenceError、
    // 整块面板白屏。这条断言专门抓漏改的 *.value 残留（渲染测试只能覆盖到 visible/arrow 都被调用到的路径）。
    expect(filePane).not.toMatch(/sortKey\.value|sortDir\.value/)
    expect(filePane).toMatch(/defineEmits\(\[[^\]]*'sort'/)
    expect(filePane).toContain("from '../utils/sort'")
  })

  it('props 声明带安全默认值，模板 active 直接读 props 名', () => {
    // 默认值是硬要求：T11 接线前 SftpView 不传这两个 prop，缺省必须能正常渲染而不是 undefined 排序
    expect(filePane).toMatch(/sortKey:\s*\{\s*type:\s*String,\s*default:\s*'name'\s*\}/)
    expect(filePane).toMatch(/sortDir:\s*\{\s*type:\s*Number,\s*default:\s*1\s*\}/)
    expect(filePane).toContain(":class=\"{ active: sortKey === 'name' }\"")
    expect(filePane).toContain(":class=\"{ active: sortKey === 'size' }\"")
    expect(filePane).toContain(":class=\"{ active: sortKey === 'modTime' }\"")
  })

  it('点列头 = 用 T6 的 nextSort 算好「下一个方向」再 emit（不在组件里复制取反实现）', () => {
    expect(filePane).toMatch(/function sortBy\(key\)\s*\{\s*emit\('sort',\s*nextSort\(\{\s*key:\s*props\.sortKey,\s*dir:\s*props\.sortDir\s*\},\s*key\)\)\s*\}/)
    expect(filePane).not.toMatch(/sortDir\s*\*=\s*-1/) // 旧实现残留即红
  })

  it('arrow() 与 visible() 的读源都是 props（两处必须同改）', () => {
    expect(filePane).toMatch(/function arrow\(key\)\s*\{\s*return\s+props\.sortKey === key \? \(props\.sortDir === 1 \? '▲' : '▼'\) : ''\s*\}/)
    expect(filePane).toMatch(/const visible = computed\(\(\)\s*=>\s*\{[\s\S]*?props\.sortKey[\s\S]*?props\.sortDir/)
  })
})

// T10：右键触发面分级（spec §5.2 / §11.2）。空白/列头/过滤栏 = 面板级（item: null），
// 文件行 = 行自身处理器。仓库无 jsdom ⇒ contextmenu 事件写不出来，只能读源码钉形状。
describe('右键触发面分级（空白 vs 行）', () => {
  const handler = () => {
    const m = filePane.match(/function onPaneContext\(e\)\s*\{([\s\S]*?)\n\}/)
    expect(m, '找不到 onPaneContext 空白右键处理器').not.toBeNull()
    return m[1]
  }

  it('空白右键：对可编辑控件与任何 li 放行、对 .pos-wrap 先 closePos 且不弹菜单', () => {
    const fn = handler()
    expect(fn).toContain('input, textarea, [contenteditable]:not([contenteditable="false"])')
    expect(fn).toMatch(/closest\('\.pos-wrap'\)[\s\S]*closePos\(\)/)
    expect(fn).toMatch(/closest\('li'\)[\s\S]*return/)
    expect(fn).toContain("emit('context', { item: null, event: e })")
    expect(filePane).toMatch(/<div class="pane[^>]*@contextmenu="onPaneContext"/)
    expect(filePane).not.toMatch(/@contextmenu\.prevent="onPaneContext"/)   // 必须不带 .prevent
  })

  it('.. 行新增 contextmenu 处理器并按无选中上抛', () => {
    const up = filePane.slice(filePane.indexOf('class="up"'), filePane.indexOf('class="up"') + 260)
    expect(up).toContain('@contextmenu.prevent="emit(\'context\', { item: null, event: $event })"')
  })

  it('分支顺序：可编辑放行 → .pos-wrap → 任何 li → preventDefault+emit', () => {
    // 顺序错了就是行为错：把 li 分支提到可编辑之前，路径栏/过滤框会被劫持；
    // 把 emit 提到 li 之前，行右键会双发射。
    const fn = handler()
    const iEditable = fn.indexOf('input, textarea')
    const iPos = fn.indexOf(".pos-wrap")
    const iLi = fn.indexOf("closest('li')")
    const iEmit = fn.indexOf("emit('context'")
    expect(iEditable).toBeGreaterThan(-1)
    expect(iPos).toBeGreaterThan(-1)
    expect(iLi).toBeGreaterThan(-1)
    expect(iEmit).toBeGreaterThan(-1)
    expect(iEditable).toBeLessThan(iPos)
    expect(iPos).toBeLessThan(iLi)
    expect(iLi).toBeLessThan(iEmit)
  })

  it('面板级右键先 preventDefault 再 emit item:null', () => {
    // 顺序反了会在自定义菜单之上再弹一次原生菜单
    expect(handler()).toMatch(/e\.preventDefault\(\)\s*\n\s*emit\('context', \{ item: null, event: e \}\)/)
  })

  it('注释写明三条刻意选择：不带 .prevent / li 必须 yield 的原因 / loading 落点降级', () => {
    // 注释是回归护栏（spec §5.2 要求写进实现注释）：删掉注释即红，逼实现者重新论证而不是顺手加 .prevent。
    const block = filePane.slice(filePane.indexOf('T10 右键触发面'), filePane.indexOf('function onPaneContext'))
    expect(block, '找不到 T10 右键触发面说明注释').not.toBe('')
    expect(block).toContain('stopPropagation')  // 「.prevent 不 stopPropagation ⇒ .. 行双发射」
    expect(block).toContain('pointer-events')   // loading 期间 .list.busy 的落点降级
    expect(block).toContain('原生')             // 不带 .prevent 是为了保住原生复制粘贴
  })
})

describe('过滤栏的多选入口', () => {
  it('三个按钮都在过滤栏里，且都 emit(action, ...)', () => {
    const bar = filePane.split('data-role="filterbar"')[1]
    expect(bar).toContain('data-role="select-all"')
    expect(bar).toContain('data-role="invert"')
    expect(bar).toContain('data-role="clear"')
    expect(bar).toMatch(/emit\('action', 'invert'\)/)
  })
  it('SftpView 分派 invert 与 extend-up/extend-down', () => {
    // sftp 是本文件顶部既有的 readFileSync(new URL('../views/SftpView.vue', import.meta.url)) 常量
    expect(sftp).toContain("name === 'invert'")
    expect(sftp).toContain("action === 'extend-up'")
    expect(sftp).toContain("action === 'extend-down'")
  })
})



