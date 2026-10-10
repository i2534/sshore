// 刷新入口（工具栏/右键）与菜单矩阵、置灰判据的接线不变量（Task 12 / spec §5.1、§5.3、§11.2）。
// 仓库无 jsdom、无 @vue/test-utils（见 sftpDropWiring.test.js 的说明）：菜单浮层的点击派发不出来，
// 但这一层的缺陷全是"形状/判据"缺陷（刷新按钮带 :disabled 让本地半边刷不了、refresh 走 sel.single
// 把多选收成一项、空选择防御写宽了让整条行菜单 no-op、script 里漏 .value 让置灰静默失效），
// 读源码即可钉死。断言一律"先 match 到标签/函数体，再在切片内断言"，不做文件级 not.toContain（审核 FF4）。
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const src = readFileSync(new URL('./SftpView.vue', import.meta.url), 'utf8')
// ContextMenu 模板块：菜单矩阵的断言都限定在这段切片内。
const tpl = src.slice(src.indexOf('<ContextMenu'), src.indexOf('</ContextMenu>'))
const doActionBody = () => src.match(/async function doAction\(name\)\s*\{([\s\S]*?)\n\}/)[1]

describe('SftpView 刷新入口 / 菜单矩阵 / 置灰判据', () => {
  it('工具栏刷新：调 refreshAll，且该 button 标签内无 disabled（审核 FF4）', () => {
    const m = src.match(/<button[^>]*refreshAll[^>]*>[^<]*<\/button>/)
    expect(m).not.toBe(null) // 找不到按钮就直接失败，别靠后面的断言碰运气
    const btn = m[0]
    expect(btn).not.toContain(':disabled')
    expect(src).not.toContain('@click="loadRemote" :disabled="!connected"') // 旧按钮形态已消失
  })

  it('refreshAll 刷两侧，且未连接时不跳过本地（spec §5.1 / §5.4）', () => {
    const fn = src.match(/async function refreshAll\(\)[\s\S]*?\n\}/)[0]
    expect(fn).toContain('loadLocal()')
    expect(fn).toContain('loadRemote()')
    expect(fn).toMatch(/connected\.value/) // 远程半边只在已连接时刷
    expect(fn).toContain('Promise.all') // 两侧并发，不是串行
  })

  it('右键刷新只刷本面板（一个 refreshPane 两个面板共用）', () => {
    expect(src).toMatch(/async function refreshPane\(pane\)[\s\S]{0,200}loadLocal\(\)/)
    const fn = src.match(/async function refreshPane\(pane\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(fn).toContain('loadLocal()')
    expect(fn).toContain('loadRemote()')
    expect(fn).toMatch(/connected\.value/)
    expect(fn).not.toContain('refreshAll') // 右键只刷本面板，不得退化成刷两侧
  })

  it('doAction 的 refresh 分支在 sel.single 之前（审核 FF6：否则多选被收成一项）', () => {
    const body = doActionBody()
    const iRefresh = body.indexOf("name === 'refresh'")
    const iSingle = body.indexOf('sel.single(')
    expect(iRefresh).toBeGreaterThan(-1) // 防"两处都是 -1 也算小于"的空断言
    expect(iSingle).toBeGreaterThan(-1)
    expect(iRefresh).toBeLessThan(iSingle)
    expect(body).toMatch(/name === 'refresh'\)\s*\{\s*closeMenu\(\);\s*return refreshPane\(pane\)/)
  })

  it('空选择防御只约束依赖选中的动作，且在 sel.single 与 names 之后（审核 F2）', () => {
    const body = doActionBody()
    expect(body).toMatch(/SELECTION_DEPENDENT|\['download', 'upload', 'remove', 'rename'\]/)
    expect(body).not.toMatch(/name !== 'refresh' && name !== 'mkdir' && !names\.length/)
    expect(body.indexOf('sel.single(')).toBeLessThan(body.indexOf('SELECTION_DEPENDENT'))
    expect(body.indexOf('const names = [...s.keys]')).toBeLessThan(body.indexOf('SELECTION_DEPENDENT'))
    // upload-picked 天然无选中（空白菜单唯一动作），不得被受约束集合挡掉
    expect(body).not.toMatch(/SELECTION_DEPENDENT[^)]*'upload-picked'/)
  })

  it('受约束的动作集合不多不少（多了误伤 upload-picked，少了让空选择弹 0 任务框）', () => {
    const decl = src.match(/const SELECTION_DEPENDENT\s*=\s*\[([^\]]*)\]/)
    expect(decl).not.toBe(null)
    const names = decl[1].split(',').map((x) => x.trim().replace(/^'|'$/g, '')).sort()
    expect(names).toEqual(['download', 'remove', 'rename', 'upload'])
  })

  it('置灰接线：唯一判据 remoteDep + 批量下拉也置灰（审核 F3/F19）', () => {
    expect(src).toContain('remoteDep')
    expect(tpl).toMatch(/remoteDep\s*&&\s*!connected/) // §11.2：判据必须出现在菜单模板内
    expect(tpl).not.toMatch(/connected\.value/) // 审核 F3 的对称形态：模板里 ref 自动解包，写 .value 恒错
    const af = src.match(/function actionsFor\(pane\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(af).toMatch(/upload-picked'[^}]*disabled: !connected\.value/)
    expect(af).toMatch(/upload'[^}]*disabled: !connected\.value/)
  })

  it('菜单矩阵：单一判据 + 各分支内容差异（审核 F22）', () => {
    expect(src).toMatch(/const menuHasItem = computed\(/)
    const mh = src.match(/const menuHasItem = computed\(([\s\S]*?)\)\n/)
    expect(mh).not.toBe(null)
    expect(mh[1]).toContain('menu.value.item')
    expect(mh[1]).toContain("!== '..'") // '..' 行按"无选中"处理
    expect(tpl).toMatch(/v-if="!menuHasItem"/)
    expect(tpl).not.toMatch(/menu\.item/) // 模板不得散算选中判据（唯一判据只在 script 里）
    const iDownload = tpl.indexOf("doAction('download')")
    expect(iDownload).toBeGreaterThan(-1)
    const blank = tpl.slice(0, iDownload)
    const row = tpl.slice(iDownload)
    // 刷新入口必须**在空白分支与行分支各自**都在场（复核 Minor：原先只判全文
    // `doAction('refresh')` 总数 >= 2，删掉任一分支的刷新仍然绿）。
    expect(blank).toContain("doAction('refresh')")
    expect(row).toContain("doAction('refresh')")
    expect(blank).toContain("doAction('mkdir')")
    expect(blank).toContain("doAction('upload-picked')")
    expect(row).toContain("doAction('download')")
    expect(row).not.toContain("doAction('mkdir')")
    // 再收紧一级（复核 Minor）：上一版只用 `row` 一个切片同时覆盖「远程行 + 本地行」，
    // 两个刷新按钮在同一个切片里 ⇒ 删掉其中任意一个仍然绿。这里按两个行分支的锚点切成
    // 互不重叠的两段，各自钉住自己的那个刷新按钮。
    const iRemoteRow = tpl.indexOf("menu.pane === 'remote'")
    const iLocalRow = tpl.indexOf(":key=\"'row-local-remote'\"")
    expect(iRemoteRow, '找不到远程行分支（menu.pane === remote）').toBeGreaterThan(-1)
    expect(iLocalRow, '找不到本地行分支（row-local-remote）').toBeGreaterThan(iRemoteRow)
    // 锚点必须真的把模板切开：每段恰好一个刷新按钮，不是空切片或原文溢出碰运气
    const remoteRow = tpl.slice(iRemoteRow, iLocalRow)
    const localRow = tpl.slice(iLocalRow)
    expect(remoteRow).toContain("doAction('refresh')")
    expect(localRow).toContain("doAction('refresh')")
    expect((remoteRow.match(/doAction\('refresh'\)/g) || []).length).toBe(1)
    expect((localRow.match(/doAction\('refresh'\)/g) || []).length).toBe(1)
    expect(remoteRow).toContain("doAction('download')") // 远程行独有
    expect(localRow).toContain("doAction('upload')") // 本地行独有（上传到远程）
  })

  // 菜单动作分派覆盖（复核 Minor / Important 的测试盲区）：模板里每个 `doAction('<name>')`
  // 的动作名都必须在 doAction/legacyAction 里真有分派分支。缺分派时点菜单表现为"菜单关了、
  // 什么都没发生"（doAction 先 closeMenu()，唯一出口在别处）。修复前 upload-picked 必须为红。
  it('菜单每个动作名都在 doAction/legacyAction 里有分派分支（复核 Important：upload-picked 死按钮）', () => {
    const names = [...new Set([...tpl.matchAll(/doAction\('([^']+)'\)/g)].map((m) => m[1]))]
    expect(names.length).toBeGreaterThanOrEqual(6) // 防 matchAll/切片写错 ⇒ 空集恒绿
    expect(names).toContain('upload-picked') // 正是本次复核抓到的死按钮，钉住它被抽出来
    // 可达性前提（复核 Minor）：下面拿"函数体文本包含 name === 'x'"当分派证据，若切片写错
    // 拿到空体/空串，覆盖断言会变成恒真（"没有名字找不到分派"）。先钉住两个体非空、且真的
    // 有分派分支，覆盖结论才可信。
    const body = doActionBody()
    expect(body.trim().length, 'doAction 体为空（切片正则写错？）').toBeGreaterThan(100)
    expect((body.match(/name === '/g) || []).length, 'doAction 里没有分派分支').toBeGreaterThanOrEqual(4)
    expect(body).toContain("name === 'refresh'") // 至少一个已知分支真的可达
    const legacy = src.match(/function legacyAction\(name, pane, it\)\s*\{([\s\S]*?)\n\}/)
    expect(legacy).not.toBe(null)
    expect(legacy[1].trim().length, 'legacyAction 体为空（切片正则写错？）').toBeGreaterThan(50)
    const dispatch = body + legacy[1]
    expect(names.filter((n) => !dispatch.includes(`name === '${n}'`))).toEqual([])
  })

  it('两个面板的 @context 都接到 showMenu（审核 F18）', () => {
    expect((src.match(/@context="showMenu\('/g) || []).length).toBe(2)
    expect(src).toContain(`@context="showMenu('local', $event)"`)
    expect(src).toContain(`@context="showMenu('remote', $event)"`)
  })

  it('空白右键不清空已有选中（spec §5.3 / 验收 B1）', () => {
    const fn = src.match(/function showMenu\(pane, \{ item, event \}\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(fn).toContain('item') // 允许 item: null（空白/面板级右键）
    expect(fn).not.toMatch(/sel\./) // 弹菜单不得动选中集合
  })

  it('SaveSftpUI 唯一出口（spec §6.3.3 / 审核 FF11）：只允许 store、*.test.js 与 wailsjs/**', () => {
    // 说明（复核 Minor）：下面扫描的 root 是 `frontend/src`（`new URL('../', import.meta.url)`
    // 相对本测试文件 `src/views/`），**不含** `frontend/wailsjs`。白名单里的 `wailsjs/**`
    // 只是显式声明"生成绑定不归这条红线管"，实际匹配不到任何被扫文件 —— 不要误以为
    // 该 clause 覆盖了生成绑定（它覆盖不到）。
    const root = fileURLToPath(new URL('../', import.meta.url))
    const files = walk(root)
    expect(files.length).toBeGreaterThan(50) // 防遍历写错 ⇒ 空集恒绿
    const hits = files
      .filter((f) => readFileSync(f, 'utf8').includes('SaveSftpUI'))
      .map((f) => relative(root, f).split(sep).join('/'))
    expect(hits).toContain('stores/sftpui.js') // 证明扫描真的命中了唯一合法出口
    expect(hits.filter((r) => r !== 'stores/sftpui.js' && !r.endsWith('.test.js') && !r.startsWith('wailsjs/'))).toEqual([])
  })
})

function walk(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name)
    if (e.isDirectory()) walk(p, out)
    else if (e.isFile()) out.push(p)
  }
  return out
}
