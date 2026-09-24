// 日志分隔条的接线不变量（本仓无 jsdom / 无 @vue/test-utils，组件事件无法派发，
// 跨文件形状靠读源码钉死，仿 views/sftpDropWiring.test.js）。
// 这些断言在实现前必然变红；删掉 Shift 守卫或任一处 onSplitterUp() 也会变红。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const sftp = readFileSync(new URL('./SftpView.vue', import.meta.url), 'utf8')

describe('日志分隔条接线不变量', () => {
  it('分隔条 DOM 契约：data-role + role=separator + tabindex，鼠标与键盘都接上', () => {
    const i = sftp.indexOf('data-role="log-splitter"')
    expect(i, '找不到 log-splitter').toBeGreaterThan(-1)
    const tag = sftp.slice(sftp.lastIndexOf('<', i), sftp.indexOf('>', i))
    expect(tag).toContain('role="separator"')
    expect(tag).toContain('aria-orientation="horizontal"')
    expect(tag).toContain('tabindex="0"')
    expect(tag).toContain('@mousedown="onSplitterDown"')
    expect(tag).toContain('@keydown="onSplitterKey"')
  })

  it('高度只由内联 :style 提供，CSS 不再写死 140px（仅会话内记忆，无持久化）', () => {
    expect(sftp).toContain('const logHeight = ref(LOG_DEFAULT_H)')
    expect(sftp).toContain(":style=\"{ height: logHeight + 'px' }\"")
    expect(sftp).toMatch(/\.logpane\s*\{[^}]*flex:\s*none/)
    expect(sftp).not.toMatch(/\.logpane\s*\{[^}]*height:\s*140px/)
    expect(sftp).not.toContain('localStorage')
  })

  it('onSplitterKey：Shift 直接让路，只有不带 Shift 的 ↑/↓ 才 ±16 并 preventDefault', () => {
    const m = sftp.match(/function onSplitterKey\(ev\)\s*\{([\s\S]*?)\n\}/)
    expect(m, '找不到 onSplitterKey').toBeTruthy()
    const body = m[1]
    // 判别力：删掉 if (ev.shiftKey) return 这条断言必红
    expect(body).toMatch(/if\s*\(ev\.shiftKey\)\s*return/)
    expect(body.indexOf('ev.shiftKey')).toBeLessThan(body.indexOf('ArrowUp'))
    expect(body.indexOf('ev.shiftKey')).toBeLessThan(body.indexOf('ArrowDown'))
    expect(body).toContain('clampLogHeight(logHeight.value + 16)')
    expect(body).toContain('clampLogHeight(logHeight.value - 16)')
    expect((body.match(/preventDefault\(\)/g) || []).length).toBe(2)
  })

  // I1：mouseup 落在窗口外时 window 永远收不到，若只挂 mouseup 摘监听，
  // 监听会残留到下一次 mouseup/切标签，鼠标移回窗口便“不按键跟着光标伸缩”。
  it('I1：只接左键，且窗口失焦时兜底摘掉 window 监听（blur 成对挂摘）', () => {
    const down = sftp.match(/function onSplitterDown\(ev\)\s*\{([\s\S]*?)\n\}/)
    expect(down, '找不到 onSplitterDown').toBeTruthy()
    const downBody = down[1]
    // 判别力：删掉 ev.button !== 0 守卫这条必红（右键拖动会进入 resize）
    expect(downBody).toMatch(/if\s*\(ev\.button\s*!==\s*0\)\s*return/)
    // 判别力：删掉 blur 的 addEventListener 这条必红（窗口外松开后残留监听）
    expect(downBody).toMatch(/addEventListener\('blur',\s*onSplitterUp\)/)

    const up = sftp.match(/function onSplitterUp\(\)\s*\{([\s\S]*?)\n\}/)
    expect(up, '找不到 onSplitterUp').toBeTruthy()
    // 判别力：只 add 不 remove 同样残留，这条必须钉住 remove
    expect(up[1]).toMatch(/removeEventListener\('blur',\s*onSplitterUp\)/)
  })

  it('夹取与默认值来自 utils/logpane，单一来源', () => {
    expect(sftp).toContain("from '../utils/logpane'")
    expect(sftp).toContain('clampLogHeight(')
  })

  it('onSplitterUp 在 onDeactivated 与 onUnmounted 两处都调用（KeepAlive 下切标签不触发卸载）', () => {
    const grab = (name) => {
      const m = sftp.match(new RegExp(name + '\\(\\(\\)\\s*=>\\s*\\{([\\s\\S]*?)\\}\\)'))
      expect(m, '找不到 ' + name).toBeTruthy()
      return m[1]
    }
    expect(grab('onDeactivated')).toContain('onSplitterUp()')
    expect(grab('onUnmounted')).toContain('onSplitterUp()')
  })
})
