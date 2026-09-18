// TransferQueue.vue 的**真实组件渲染**测试（SSR）：评审 I1 的核心是「测试断言的代码路径
// UI 根本不用」。这里直接渲染生产组件，行内备注/进度/进度条/动作都由组件模板 + queue.js
// 纯函数产出，改组件模板或改纯函数都会让这些用例变红。
import { describe, it, expect } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import TransferQueue from './TransferQueue.vue'
import { CANCELLED_LATE_TEXT, CANCELLING_TEXT, CANCEL_FAILED_TEXT, WAITING_FRAME_TEXT,
  applyBatchCancel, applyCancelResult } from '../utils/queue'

const render = (transfers) => renderToString(createSSRApp({ render: () => h(TransferQueue, { transfers, now: 1000 }) }))

const row = (over = {}) => ({ id: 't1-0', direction: 'download', name: 'a.bin', src: '/r/a.bin', dst: '/l/a.bin', size: 10, status: '处理中', startedAt: 1, ...over })

describe('TransferQueue.vue 生产渲染（SSR）', () => {
  it('Cancel 真但操作结果 nil：渲染「取消过晚（已完成）」，绝不显示「已取消」', async () => {
    const html = await render([row({ status: '完成', cancelRequested: true, elapsed: 2 })])
    expect(html).toContain(CANCELLED_LATE_TEXT)
    expect(html).not.toContain('已取消')
    expect(html).toContain('done status')
    expect(html).not.toContain('class="pbar"')
  })

  it('无帧的运行行：渲染可用性占位，不画卡在 0% 的假条', async () => {
    const html = await render([row()])
    expect(html).toContain(WAITING_FRAME_TEXT)
    expect(html).not.toContain('class="pbar"')
    expect(html).not.toContain('0%')
  })

  it('total<0 的降级帧：未知进度 + 不定条，绝不 100%、绝不当失败', async () => {
    const html = await render([row({ done: 5, total: -1, hasProgress: true })])
    expect(html).toContain('未知进度')
    expect(html).toContain('indet')
    expect(html).not.toContain('100%')
    expect(html).not.toContain('err status')
  })

  it('Cancel 返回 false：走 applyBatchCancel + applyCancelResult 的真实取消路径，渲染「未能取消」', async () => {
    // 不注入 fixture 字段：真实调用取消落点，断言组件渲染的是这条路径产出的状态。
    const rec = row({ pending: false })
    applyBatchCancel([rec], rec)              // 点取消：cancelRequested/pendingCancel
    expect(applyCancelResult(rec, false))     // Cancel 返回 false：cancelFailed
      .toBe('failed')
    expect(rec.cancelFailed).toBe(true)
    const html = await render([rec])
    expect(html).toContain(CANCEL_FAILED_TEXT)
    expect(html).not.toContain(CANCELLING_TEXT)
  })

  it('Cancel 返回 true：渲染「取消中…」', async () => {
    const html = await render([row({ cancelRequested: true, pendingCancel: true })])
    expect(html).toContain(CANCELLING_TEXT)
  })

  it('取消请求在飞（pendingCancel）时取消按钮禁用：防止并发第二发 Cancel 返回 false（修复轮 3）', async () => {
    const html = await render([row({ cancelRequested: true, pendingCancel: true })])
    expect(html).toContain(CANCELLING_TEXT)
    const btn = (html.match(/<button[^>]*data-act="cancel"[^>]*>/) || [''])[0]
    expect(btn).toContain('data-act="cancel"')
    expect(btn).toContain('disabled')
  })

  it('整批取消后汇总显示「取消 N」且进行中为 0（M1）', async () => {
    const html = await render([
      row({ id: 't1-0', status: '取消', reason: '已取消', elapsed: 1 }),
      row({ id: 't1-1', status: '取消', reason: '已取消', elapsed: 1 }),
    ])
    expect(html).toContain('取消 2')
    expect(html).toContain('进行中 0')
  })

  it('无 id 的 legacy 失败记录不渲染死重试按钮（I4）', async () => {
    const html = await render([{ direction: 'move', name: 'a.bin', src: '/l/a.bin', dst: '', size: 1, status: '失败', reason: 'boom', elapsed: 1 }])
    expect(html).toContain('boom')
    expect(html).not.toContain('data-act="retry"')
  })

  it('有 id 的处理中项提供取消按钮（含队列中的 pending 行）', async () => {
    const html = await render([row({ pending: true })])
    expect(html).toContain('data-act="cancel"')
  })
})
