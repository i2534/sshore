// SftpView / SearchOverlay 的连接失效自愈接线不变量（Task 13 / spec §8.3、§8.4、§8.5、§10、
// §11.3 D1/D2/D3）。
//
// 仓库没有 jsdom、没有 @vue/test-utils（见 sftpDropWiring.test.js 的说明）：探针超时、重连顺序、
// 探针自身抛错这些行为无法在测试里真实派发。但这一层的缺陷全是"形状/顺序"缺陷，且每一条都对应
// 一个已确认的真缺陷：
//   · 三态写成布尔（`if (!(await probeTimed(h)))`）在字符串三态下恒 false ⇒ 自愈永不触发
//     —— 这是本计划最重要的 Blocking 修正；
//   · 把记忆值塞进优先级 1 的 pendingPath ⇒ resolveRemoteTarget 返回 fromMemory=false ⇒ §6.5
//     的"仅来自记忆才回落 home"在自愈路径永不触发；
//   · loadRemote 里是浮动调用，remoteOpFailed 若 reject 就是 unhandled rejection；
//   · 13 个落点漏一个，就有一类失败不归口（面板仍显示"已连接"）。
// 切片一律用**花括号配对**（审核 F14：注释/缩进都是未规定的形状，不能当切片锚点）。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./SftpView.vue', import.meta.url), 'utf8')
const overlay = readFileSync(new URL('../components/SearchOverlay.vue', import.meta.url), 'utf8')

// 花括号配对切片：s[i] 必须是与前面签名配对的 '{'。返回块内内容（不含外层花括号）。
// 用配对而不是 `/[\s\S]*?\n\}/`：后者把"第一个顶格 }"当结尾，一旦函数内出现顶格注释/
// 模板字符串就会被切歪，且失败信息指向错误的原因。
function sliceBlock(s, i) {
  let depth = 0
  for (let k = i; k < s.length; k++) {
    if (s[k] === '{') depth++
    else if (s[k] === '}') { depth--; if (depth === 0) return s.slice(i + 1, k) }
  }
  return ''
}
function fnBody(name, source = src) {
  const re = new RegExp('(?:async\\s+)?function\\s+' + name + '\\s*\\([^)]*\\)\\s*\\{')
  const m = re.exec(source)
  expect(m, '找不到函数 ' + name).not.toBe(null)
  return sliceBlock(source, m.index + m[0].length - 1)
}

describe('SFTP 连接失效自愈：探活三态 / 幂等重连 / 13 个落点归口', () => {
  it('probeTimed：Promise.race + 15s 客户端超时 + clearTimeout，三态齐全', () => {
    const pf = fnBody('probeTimed')
    expect(pf).toMatch(/Promise\.race/)
    expect(pf).toMatch(/SftpProbe\(/)
    expect(pf).toMatch(/15000|PROBE_TIMEOUT_MS/) // 15s 兜底（spec §8.3：legacy batch 可能永不返回）
    expect(pf).toMatch(/clearTimeout/) // 计时器必须被清掉，否则每次探活都留一个挂着的句柄
    expect(pf).toContain("'ok'")
    expect(pf).toContain("'lost'")
    expect(pf).toContain("'unknown'")
  })

  it('probeTimed：探针自身抛错 ⇒ unknown，绝不当成 lost（否则对可能健康的链路 teardown）', () => {
    const pf = fnBody('probeTimed')
    expect(pf).toMatch(/catch[\s\S]*?'unknown'/)
    expect(pf).not.toMatch(/catch[\s\S]{0,100}'lost'/)
  })

  it('probeTimed：performance.now 计时 + 每次探活一条 info「探活耗时 …ms」（审核 F12 / F1 可观测）', () => {
    const pf = fnBody('probeTimed')
    expect(pf).toMatch(/performance\.now\(\)/)
    expect(pf).toContain('探活耗时')
    expect(src).toContain('探活耗时')
    // 级别必须是 info：探活耗时不是错误（§10），用 err() 会污染日志级别与 D1 的 grep
    const infoFn = fnBody('info')
    expect(infoFn).toContain("level: 'info'")
    expect(pf).toMatch(/info\(/)
  })

  it('verifyRemote：connected===false 早退；核心一行是 === lost 才重连（Blocking）；finally 复位', () => {
    const vf = fnBody('verifyRemote')
    // 早退防"连接本身刚失败"时再打一轮无意义探活+重连（自审修正 7）
    expect(vf).toMatch(/!connected\.value/)
    expect(vf).toMatch(/if \(!h \|\| !connected\.value \|\| probing\) return/)
    // 硬要求：三态是字符串，写成 !(await probeTimed(...)) 会恒 false ⇒ 自愈永不触发
    expect(vf).toMatch(/if \(\(await probeTimed\((?:h|host\.value)\)\) === 'lost'\) await recoverConnection\((?:h|host\.value)\)/)
    expect(vf).not.toMatch(/!\s*\(await probeTimed/)
    expect(vf).toMatch(/finally\s*\{[^}]*probing = false/)
  })

  it('recoverConnection：入口幂等 → SftpDisconnect → connect()，finally 复位', () => {
    const fn = fnBody('recoverConnection')
    const iGuard = fn.indexOf('if (recovering) return')
    const iSet = fn.indexOf('recovering = true')
    expect(iGuard).toBeGreaterThan(-1)
    expect(iSet).toBeGreaterThan(iGuard)
    expect(fn).toMatch(/recovering = true[\s\S]*?SftpDisconnect\([\s\S]*?await connect\(\)/)
    expect(fn).toMatch(/finally\s*\{[^}]*recovering = false/)
  })

  it('recoverConnection：不写 pendingPath（审核 F6）—— 否则 §6.5 记忆失效回落永不触发', () => {
    const fn = fnBody('recoverConnection')
    expect(fn).not.toMatch(/pendingPath\.value\s*=\s*sftpui\.lastPathFor/)
    expect(fn).not.toMatch(/pendingPath\.value\s*=\s*remembered/)
  })

  it('recoverConnection：只重连一次（无 while / 无定时器）—— 切片内断言，不是全文件', () => {
    const fn = fnBody('recoverConnection')
    // 切片而不是文件级 not.toContain：文件里 startClock 的 setInterval 是合法的，
    // 文件级断言会恒红（或为绕开它而删掉正当代码）。
    expect(fn).not.toMatch(/while\s*\(|setInterval/)
  })

  it('recoverConnection：成功/失败判据是 connected.value，切主机时静默早退不记「重连失败」', () => {
    const fn = fnBody('recoverConnection')
    expect(fn).toMatch(/if \(host\.value !== h\) return/)
    expect(fn).toMatch(/connected\.value/)
    const iSwitch = fn.indexOf('if (host.value !== h) return')
    const iFail = fn.indexOf('重连失败：')
    expect(iFail).toBeGreaterThan(iSwitch) // 早退在失败日志之前 ⇒ 切主机不产生误导日志
  })

  it('三条自愈文案逐字（§10）', () => {
    expect(src).toContain('远端连接已失效（主机休眠或网络中断），正在重连…')
    expect(src).toContain('已重新连接')
    expect(src).toContain('重连失败：')
    expect(src).toContain('请手动点「连接」')
  })

  it('remoteOpFailed：先记原始报错原文（不丢 e）再复核；永不 reject（浮动调用的唯一保护）', () => {
    expect(src).toMatch(/async function remoteOpFailed\(e\)/)
    const fn = fnBody('remoteOpFailed')
    const iErr = fn.indexOf('err(e)')
    const iVerify = fn.indexOf('verifyRemote()')
    expect(iErr).toBeGreaterThan(-1)
    expect(iVerify).toBeGreaterThan(iErr) // err(e) 必须在前面：原文优先，不能被复核吞掉
    expect(fn).toMatch(/catch/)
    expect(fn).not.toMatch(/\bthrow\b/) // 绝不外泄 rejection（loadRemote 里是浮动调用）
    // 且必须有"两级兜错"：err(e) 失败也不许跳过复核，复核失败也不许外泄
    const catches = fn.match(/catch/g) || []
    expect(catches.length).toBeGreaterThanOrEqual(2)
  })

  it('probing / recovering 是普通 let（非 ref，审核 F15）', () => {
    expect(src).toMatch(/let probing = false/)
    expect(src).toMatch(/let recovering = false/)
    expect(src).not.toMatch(/const probing = ref\(/)
    expect(src).not.toMatch(/const recovering = ref\(/)
  })

  it('13 个落点归口（逐个函数体切片 + catch 后必须接 remoteOpFailed/verifyRemote）', () => {
    // spec §8.4 清单第 1/2/4/5/6/7/8/9/10/11/12 项所在的 10 个函数
    const remote = ['dispatchTransfer', 'loadRemote', 'goRemote', 'goRemoteHome', 'uploadPicked',
      'removeSelected', 'renameItem', 'mkdirIn', 'onMoveDrop', 'cleanItem']
    for (const n of remote) {
      const body = fnBody(n)
      expect(body.length, n + ' 切片为空').toBeGreaterThan(0)
      // 注意：简报里的 `/catch[\s\S]*?(remoteOpFailed\(|verifyRemote\(/` 少了一个右括号，
      // 直接抄过来会让整个测试文件语法错误（Invalid regular expression: Unterminated group）。
      expect(body, n).toMatch(/catch[\s\S]*?(remoteOpFailed\(|verifyRemote\()/)
    }
    // 第 10、11 项都在 onMoveDrop：目标子目录加载 + 移动循环，一个函数里两条 catch 归口
    const mv = fnBody('onMoveDrop')
    expect((mv.match(/remoteOpFailed\(/g) || []).length).toBeGreaterThanOrEqual(2)
  })

  it('混合面板的 4 个点保留本地分支的裸 err(e)（不能断言"函数里没有 err(e)"）', () => {
    for (const n of ['removeSelected', 'renameItem', 'mkdirIn', 'onMoveDrop']) {
      const body = fnBody(n)
      expect(body, n).toMatch(/pane === 'local'[\s\S]{0,60}err\(e\)|err\(e\)[\s\S]{0,60}pane === 'local'/)
    }
  })

  it('cleanItem 按 rec.direction 分派（download=本地、else=远程，审核 FF3）', () => {
    const ci = fnBody('cleanItem')
    expect(ci).toContain('rec.direction')
    expect(ci).toMatch(/rec\.direction[\s\S]{0,200}remoteOpFailed\(/)
  })

  it('onMoveDrop：targetItems 加载必须在 try 内（审核 FF2：原本在 try 之外 ⇒ 死链时 unhandled rejection）', () => {
    const mv = fnBody('onMoveDrop')
    const iTry = mv.indexOf('try {')
    const iList = mv.indexOf('SftpList(')
    expect(iTry).toBeGreaterThan(-1)
    expect(iList).toBeGreaterThan(-1)
    expect(iTry).toBeLessThan(iList)
  })

  it('connect 保持 err(e)、不接 remoteOpFailed（审核 FF15b：恒早退，死接线不计入覆盖）', () => {
    const fn = fnBody('connect')
    expect(fn).toMatch(/catch \(e\) \{[\s\S]*?err\(e\)/)
    expect(fn).not.toMatch(/remoteOpFailed\(|verifyRemote\(/)
    // 全文窗口断言：connect 之后 400 字符内不得出现 remoteOpFailed（防止"顺手接上"）
    expect(src).not.toMatch(/async function connect\(\)[\s\S]{0,400}remoteOpFailed/)
  })

  it('第 13 个落点：SearchOverlay emits 增加 failed，catch 里 emit("failed", e) + 模板接 @failed', () => {
    expect(overlay).toMatch(/defineEmits\(\[[^\]]*'failed'[^\]]*\]\)/)
    const run = overlay.slice(overlay.indexOf('async function run()'), overlay.indexOf('function cancel()'))
    expect(run.length).toBeGreaterThan(0)
    expect(run).toMatch(/emit\('failed',\s*e\)/)
    expect(run).toMatch(/catch \(e\) \{[\s\S]*?emit\('failed'/)
    expect(src).toContain('@failed="remoteOpFailed"')
  })

  it('syncConnection 已连接分支：真探活 + === lost 判定，且不重置路径/不重载列表（F1/F14）', () => {
    const fn = fnBody('syncConnection')
    const i = fn.indexOf('if (connected.value) {')
    expect(i).toBeGreaterThan(-1)
    const branch = sliceBlock(fn, i + 'if (connected.value) '.length)
    expect(branch).toContain('probeTimed(')
    expect(branch).toMatch(/===\s*'lost'/)
    expect(branch).not.toMatch(/!\s*\(await probeTimed/)
    // 每次切标签跳目录是不可接受的：已连接分支不得重置路径或重载列表
    expect(branch).not.toMatch(/SftpHome|loadRemote\(|remotePath\.value =/)
  })

  it('dispatchTransfer：成功路径回同步 connected=true，且带 host 守卫（审核 F21）', () => {
    const fn = fnBody('dispatchTransfer')
    expect(fn).toMatch(/const h = host\.value/)
    expect(fn).toMatch(/host\.value === h && !connected\.value\) connected\.value = true/)
    // 快照必须在 await 之前，守卫在成功路径上
    expect(fn.indexOf('const h = host.value')).toBeLessThan(fn.indexOf('await (rec.isDir'))
    // 只回写 true，绝不在这里回写 false（失败路径的状态由 verifyRemote 决定）
    expect(fn).not.toMatch(/connected\.value = false/)
  })

  it('dispatchTransfer：取消中的失败保留裸 err(e)、跳过探活（审核 FF15a）', () => {
    const fn = fnBody('dispatchTransfer')
    expect(fn).toMatch(/cancelRequested[\s\S]{0,160}remoteOpFailed\(/)
    expect(fn).toMatch(/if \(rec\.cancelRequested\) err\(e\)/)
  })
})
