// FilePane.vue 的**真实组件渲染**测试（SSR，审核 FF10）：排序状态上收成 props 之后，
// 「行序 / 列头箭头 / active 类 / @visible 载荷」必须由生产组件模板真渲染出来断言，
// 而不是读源码猜（读源码的断言留在 filePaneWiring.test.js）。
// 通道先例：frontend/src/components/TransferQueue.render.test.js:1-11（@vue/server-renderer 已在 node_modules）。
// 铁律：helper 是 async 的，每个用例都必须 await —— 漏了 await 拿到的是 Promise，
// 之后的 toContain/indexOf 断言全都失去判别力（这正是 FF10 要防的假绿）。
import { describe, it, expect } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import FilePane from './FilePane.vue'

// 故意让「名称序」与「大小序 / 修改时间序」都不同，这样 props 没接上时行序断言必红。
const ITEMS = [
  { name: 'a.txt', size: 300, modTime: '2024-01-01 10:00' },
  { name: 'b.txt', size: 100, modTime: '2024-01-02 10:00' },
  { name: '.secret', size: 500, modTime: '2024-01-03 10:00' },
  { name: 'c.bin', size: 200, modTime: '2024-01-04 10:00' },
]

const render = async (props = {}) =>
  await renderToString(createSSRApp({ render: () => h(FilePane, { items: ITEMS, ...props }) }))

// 抠出某一列表头那一小段（col-size 与 .cell-size 不同串，不会互相命中）
const col = (html, cls) => (html.match(new RegExp(`<span class="[^"]*${cls}[^"]*"[^>]*>[\\s\\S]*?</span>`)) || [''])[0]
const at = (html, name) => html.indexOf(name)

describe('FilePane.vue 生产渲染（SSR）：排序 props 驱动行序/箭头/active', () => {
  it('sortKey=name + sortDir=1：行按名称升序，▲ 在「名称」列且该列 active', async () => {
    const html = await render({ sortKey: 'name', sortDir: 1 })
    const [a, b, c] = [at(html, 'a.txt'), at(html, 'b.txt'), at(html, 'c.bin')]
    expect(a).toBeGreaterThan(-1)
    expect(a).toBeLessThan(b)
    expect(b).toBeLessThan(c)

    const name = col(html, 'col-name')
    expect(name).toContain('▲')
    expect(name).toContain('active')
    expect(col(html, 'col-size')).not.toContain('active')
    expect(col(html, 'col-time')).not.toContain('active')
  })

  it('sortKey=size + sortDir=-1：行序为 size 降序，▼ 在「大小」列且该列 active', async () => {
    const html = await render({ sortKey: 'size', sortDir: -1 })
    // size：.secret 500 > a.txt 300 > c.bin 200 > b.txt 100
    const [a, b, s, c] = [at(html, 'a.txt'), at(html, 'b.txt'), at(html, '.secret'), at(html, 'c.bin')]
    expect(s).toBeGreaterThan(-1)
    expect(s).toBeLessThan(a)
    expect(a).toBeLessThan(c)
    expect(c).toBeLessThan(b)

    const size = col(html, 'col-size')
    expect(size).toContain('▼')
    expect(size).not.toContain('▲')
    expect(size).toContain('active')
    // 未排序列既不能 active，也不能挂箭头（arrow() 漏改 props 时这里会同时红）
    expect(col(html, 'col-name')).not.toContain('active')
    expect(col(html, 'col-name')).not.toContain('▲')
    expect(col(html, 'col-name')).not.toContain('▼')
  })

  it('sortKey=modTime + sortDir=1：行按修改时间升序，▲ 落在「修改时间」列', async () => {
    const html = await render({ sortKey: 'modTime', sortDir: 1 })
    const [a, b, s, c] = [at(html, 'a.txt'), at(html, 'b.txt'), at(html, '.secret'), at(html, 'c.bin')]
    expect(a).toBeLessThan(b)
    expect(b).toBeLessThan(s)
    expect(s).toBeLessThan(c)

    const time = col(html, 'col-time')
    expect(time).toContain('▲')
    expect(time).toContain('active')
    expect(col(html, 'col-size')).not.toContain('active')
  })

  it('缺省 props（父层尚未接线）容忍：不崩，按默认 name/升序渲染', async () => {
    // T11 之前 SftpView 不传 :sort-key/:sort-dir，此用例钉住「有安全默认值」这条硬要求
    const html = await render({})
    expect(html).toContain('a.txt')
    expect(col(html, 'col-name')).toContain('▲')
    expect(col(html, 'col-name')).toContain('active')
  })

  it('onVisible 载荷就是可见顺序（过滤 + 排序后的行，与真实渲染行序一致）', async () => {
    const seen = []
    const html = await render({
      sortKey: 'size', sortDir: -1, showHidden: false,
      onVisible: (keys) => seen.push(keys),
    })
    const keys = seen.at(-1)
    // showHidden=false 把 dotfile 过滤掉；size 降序 => a.txt(300) > c.bin(200) > b.txt(100)
    expect(keys).toEqual(['a.txt', 'c.bin', 'b.txt'])
    expect(html).not.toContain('.secret')

    // 载荷顺序必须与真渲染出来的行序逐项一致（不是「大致有序」）
    const positions = keys.map((k) => at(html, k))
    expect(positions).toEqual([...positions].sort((x, y) => x - y))
    expect(positions[0]).toBeGreaterThan(-1)
  })

  it('onVisible 载荷随排序 props 变化（证明它不是恒定的行名单）', async () => {
    const byName = []
    await render({ sortKey: 'name', sortDir: 1, showHidden: false, onVisible: (k) => byName.push(k) })
    const bySize = []
    await render({ sortKey: 'size', sortDir: 1, showHidden: false, onVisible: (k) => bySize.push(k) })
    expect(byName.at(-1)).toEqual(['a.txt', 'b.txt', 'c.bin'])
    expect(bySize.at(-1)).toEqual(['b.txt', 'c.bin', 'a.txt'])
  })
})
