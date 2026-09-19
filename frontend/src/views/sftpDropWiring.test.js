// 拖拽接线的结构性不变量（真机事故 2026-09-19 之后补）：
// 事故根因不是逻辑错，而是**形状不匹配** —— 模板把原生 DragEvent 传给期望 { event } 的
// onPaneDrop，解构出 undefined ⇒ 读 dataTransfer 抛 TypeError ⇒ 每个 drop 都弹红条。
// vitest 环境没有 jsdom/@vue/test-utils（见 TransferQueue.render.test.js 的说明），
// 组件事件无法派发；但这一层靠读源码即可钉死，且这些断言在旧代码上必然变红。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8')
const sftp = read('./SftpView.vue')
const app = read('../App.vue')

describe('SFTP 面板 drag/drop 接线不变量', () => {
  it('onPaneDrop：模板实参必须提供签名解构的键（旧写法 onPaneDrop(pane, $event) 必须红）', () => {
    const sig = sftp.match(/function\s+onPaneDrop\s*\(([^)]*)\)/)
    expect(sig, '找不到 onPaneDrop 签名').toBeTruthy()
    const destructured = sig[1].match(/\{([^}]*)\}/)
    expect(destructured, '签名必须按对象解构（与 onMoveDrop 保持同一形状）').toBeTruthy()
    const keys = destructured[1].split(',').map((s) => s.trim()).filter(Boolean)
    expect(keys).toEqual(['event'])
    const calls = [...sftp.matchAll(/onPaneDrop\('(local|remote)',\s*([^)]*)\)/g)]
    expect(calls.map((c) => c[1]).sort()).toEqual(['local', 'remote'])
    for (const [, pane, arg] of calls) {
      expect(arg.trim().startsWith('{'), pane + ' 面板必须传 { event: $event }，实际：' + arg).toBe(true)
      expect(arg).toContain('event:')
      expect(arg).toContain('$event')
    }
  })

  it('drop 读载荷只走 payloadFromDragEvent：视图里不得再有裸 event.dataTransfer.getData', () => {
    expect(sftp).toContain('payloadFromDragEvent(event)')
    expect(sftp).not.toContain('.dataTransfer.getData')
  })

  it('拖拽 MIME 只有 dnd.js 一个来源，视图不得重写字符串', () => {
    expect(sftp).not.toContain("'application/x-sshore'")
    expect(sftp).toContain('DRAG_MIME')
  })

  it('系统拖入单一通道：前端不再有 Go 桥的 files:dropped，监听由 App.vue 注册、显式 useDropTarget=false', () => {
    expect(sftp).not.toContain("EventsOn('files:dropped'")
    expect(app).toContain('dispatchSystemDrop')
    expect(app).toMatch(/OnFileDrop\([\s\S]{0,200}?,\s*false\)/)
    expect(app).toContain('OnFileDropOff()')
  })

  it('系统拖入落点接线：dragover 记悬停高亮，DOM drop 记命中面板，处理器用 pickDropPane', () => {
    const wraps = [...sftp.matchAll(/<div class="pane-wrap"[^>]*>/g)].map((m) => m[0])
    expect(wraps.length).toBe(2)
    for (const w of wraps) {
      expect(w).toContain("@dragover.prevent=\"onPaneDragOver(")
      expect(w).toContain('drop-hover')
    }
    expect(sftp).toMatch(/onPaneDragOver\('local'\)/)
    expect(sftp).toMatch(/onPaneDragOver\('remote'\)/)
    expect(sftp).toContain('pickDropPane({ x: payload.x, y: payload.y }, rects, lastDropPane.value)')
    // 回退记录必须来自 dragover（真机：外拖的 drop 不一定到达本地面板空白区，只有 dragover 一定经过）
    const overBody = sftp.match(/function onPaneDragOver\(pane\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(overBody).toContain('noteDropPane(pane)')
    // drop 侧再记一次兜底，并且必须清掉视觉高亮（真机拖完后面板一直挂虚线框）
    const dropBody = sftp.match(/async function onPaneDrop\([^)]*\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(dropBody).toContain('noteDropPane(targetPane)')
    expect(dropBody).toContain('hoverPane.value = null')
    expect(dropBody.indexOf('hoverPane.value = null')).toBeLessThan(dropBody.indexOf('payloadFromDragEvent'))
    // 消费后必须清记录（否则 3s TTL 内的下一次系统拖入可能沿用旧落点）
    const filesBody = sftp.match(/function onFilesDropped\(payload\)\s*\{([\s\S]*?)\n\}/)[1]
    expect(filesBody).toContain('lastDropPane.value = null')
    expect(sftp).toContain("window.addEventListener('dragend', clearDropHover)")
    expect(sftp).toContain("window.removeEventListener('dragend', clearDropHover)")
  })

  it('系统拖入处理器随 KeepAlive 成对挂摘（不摘会让别的标签拖入投到 SFTP 面板）', () => {
    const grab = (name) => {
      const m = sftp.match(new RegExp(name + '\\(\\(\\)\\s*=>\\s*\\{([\\s\\S]*?)\\}\\)'))
      expect(m, '找不到 ' + name).toBeTruthy()
      return m[1]
    }
    expect(grab('onActivated')).toContain('setSystemDropHandler(onFilesDropped)')
    expect(grab('onDeactivated')).toContain('setSystemDropHandler(null)')
    expect(grab('onUnmounted')).toContain('setSystemDropHandler(null)')
  })
})