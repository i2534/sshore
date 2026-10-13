// 「复制 → 文件」（本地直写 / 远程先下载后入剪贴板）的接线不变量（Task 7）。
// 仓库无 jsdom：跨进程绑定与传输队列都派发不出来，这一层只能读源码钉形状；
// 但所有"必须写对否则静默失败"的点（空目标免冲突框、取消不写、只写成功项、
// 临时目录用本地路径拼接）都在断言里。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./SftpView.vue', import.meta.url), 'utf8')

// 花括号配对切片：非贪婪的 `\{([\s\S]*?)\n\}` 会在函数体内第一个行首 `}` 处提前截断
// （copyFilesAsObjects 里 `if (pane === 'local') { … }` 就会命中），导致断言看到半个函数体。
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

const fnBody = () => sliceBlock(src, 'async function copyFilesAsObjects(pane, names)')
const doActionBody = () => sliceBlock(src, 'async function doAction(name)')

describe('SftpView 复制「文件」接线', () => {
  it('本地面板：直接拼绝对路径写剪贴板', () => {
    const body = fnBody()
    expect(body).toContain('localJoin(')
    expect(body).toContain('CopyFilesToClipboard(')
    expect(body).toContain('（可直接粘贴到文件管理器）')
  })

  it('远程面板：先建临时目录，再入队下载到它', () => {
    const body = fnBody()
    expect(body).toContain('ClipboardTempDir(')
    expect(body).toContain("direction: 'download'")
    expect(body).toContain('targetDir: tmp')
    expect(body.indexOf('ClipboardTempDir('), '必须先建目录再派发下载').toBeLessThan(body.indexOf('runBatch('))
  })

  it('免冲突框：传显式空目标（runBatch 的 existing 取自用户本地目录，会误判冲突）', () => {
    const body = fnBody()
    expect(body).toContain('existingNames: []')
    expect(body).toContain('hiddenSelected: 0')
  })

  it('不改变用户选中集：keepSelection: true', () => {
    expect(fnBody()).toContain('keepSelection: true')
  })

  it('整个函数有 try/catch，绑定抛错不会变成无提示的 unhandled rejection', () => {
    const body = fnBody()
    expect(body).toContain('try {')
    expect(body).toContain('catch')
    expect(body).toMatch(/catch\s*\(e\)\s*\{\s*err\(e\)/)
  })

  it('队列返回 null（冲突框被取消）时有兜底', () => {
    const body = fnBody()
    expect(body).toContain('if (!s)')
    expect(body).toContain("await err('复制已取消，未写入剪贴板')")
  })

  it('中途取消 ⇒ 整批不写剪贴板（判据是整批的 s.cancelled，不是只含已派发项的 results）', () => {
    const body = fnBody()
    expect(body, '必须用整批的 s.cancelled 判定').toContain('s.cancelled')
    expect(body, '不得用 results.some(…「取消」)：被取消的排队项不在 results 里').not.toContain('results.some')
    const iGuard = body.indexOf('s.cancelled')
    const iWrite = body.lastIndexOf('CopyFilesToClipboard(')
    expect(iWrite).toBeGreaterThan(-1)
    expect(iGuard, '取消守卫必须出现在写剪贴板之前').toBeLessThan(iWrite)
  })

  it('只把「完成」的项写进剪贴板，并且用本地路径拼接临时目录下的落点', () => {
    const remote = fnBody().slice(fnBody().indexOf('ClipboardTempDir('))
    const iFilter = remote.indexOf("status === '完成'")
    const iWrite = remote.indexOf('CopyFilesToClipboard(')
    expect(iFilter, '必须先按「完成」过滤').toBeGreaterThan(-1)
    expect(iWrite).toBeGreaterThan(-1)
    expect(iFilter, '过滤必须发生在写剪贴板之前').toBeLessThan(iWrite)
    expect(remote, '临时目录是本地路径 ⇒ 必须用 localJoin').toContain('localJoin(tmp')
    expect(remote, '不得用 posixJoin 拼临时目录').not.toContain('posixJoin(tmp')
  })

  it('没有成功项时写 error 并直接返回', () => {
    const body = fnBody()
    expect(body).toContain('paths.length')
    expect(body).toContain('复制失败：没有成功的项')
  })

  it('部分失败仍写剪贴板，但额外记一条 error', () => {
    const body = fnBody()
    expect(body).toContain('s.failed > 0')
    expect(body).toMatch(/s\.failed > 0\)\s*await err\(/)
  })

  it('doAction 里有 copy-file 分派，且在 sel.single 之前', () => {
    const body = doActionBody()
    const i = body.indexOf("'copy-file'")
    expect(i).toBeGreaterThan(-1)
    expect(i).toBeLessThan(body.indexOf('sel.single('))
    expect(body).toContain('copyFilesAsObjects(')
  })

  it('runBatch 三处改动到位：新参数有默认值、返回值带 results', () => {
    expect(src).toMatch(/async function runBatch\(\{[\s\S]{0,220}hiddenSelected[\s\S]{0,80}keepSelection = false[\s\S]{0,40}existingNames/)
    expect(src).toContain('hiddenSelected ?? hiddenSelectedCount(')
    expect(src).toContain('existingNames ?? (targetItems || []).map(')
    expect(src).toContain('if (!keepSelection) sel.remove(sourceSelection, doneNames)')
    expect(src).toContain('return { ...s, results, cancelled: hasCancelled(batch) }')
    expect(src).toContain("hasCancelled } from '../utils/queue'")
  })
})
