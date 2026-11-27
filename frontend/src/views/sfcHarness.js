// 测试专用：SFC「最小挂载」harness。
//
// 为什么需要它：仓库没有 jsdom、也没有 @vue/test-utils（见 sftpDropWiring.test.js 说明），
// 既有组件测试只能做编译冒烟或读源码切片断言"形状"。但本轮修复的验收要求断言**真实行为**
// （菜单 y 的字面量、SftpRemove 收到的 host 参数、乱序响应后 conflicts 的归属……），
// 形状断言给不出这些字面量。
//
// 做法：抽出 <script setup> 的真实代码，去掉 import（依赖一律由 env 注入），用 new Function
// 在受控环境里执行。Vue 的 ref/reactive/computed 用**真的**（响应式包不依赖 DOM），
// Pinia store 用**真的**（绑定已被 vi.mock），只有 IPC 绑定与生命周期钩子是替身。
// 于是被测函数（showMenu/connect/cleanItem/openConflicts……）跑的就是生产代码本身，
// 断言落在真实状态与真实绑定调用参数上，而不是"mock 存在性"。模板不渲染（本轮缺陷全在 script）。
import { readFileSync } from 'node:fs'

// sfcUrl: new URL('./Xxx.vue', import.meta.url)
// env:    注入的自由标识符（vue API、绑定、store 工厂、utils、window/document 等）
// expose: 需要从 setup 作用域取回的标识符名列表（函数/ref）
export function runSetup(sfcUrl, env, expose) {
  const src = readFileSync(sfcUrl, 'utf8')
  const m = src.match(/<script setup>([\s\S]*?)<\/script>/)
  if (!m) throw new Error('找不到 <script setup>：' + String(sfcUrl))
  // 去掉 import 语句（含跨行）：这些依赖改由 env 注入，才能 mock/替换。
  // 锚定行首 ^import，惰性匹配到该条 import 自己的 from '...' 行尾，不会吞掉后续代码。
  const code = m[1].replace(/^import[\s\S]*?from\s*['"][^'"]+['"];?[ \t]*$/gm, '')
  const keys = Object.keys(env)
  const fn = new Function(...keys, '"use strict";\n' + code + '\n;return {' + expose.join(',') + '};')
  return fn(...keys.map((k) => env[k]))
}