import { describe, it, expect } from 'vitest'
import SyncCard from '../components/SyncCard.vue'
// 后续任务继续 import：Task 5 加 SyncConflictsDialog、Task 6 加 SyncView。

// 编译冒烟，**不是**渲染/行为测试：plugin-vue 在 import 时编译 SFC 模板，
// 因此模板/导入错误会在这里暴露。`npm run build` 只编译 index.html→src/main.js
// 可达的模块，尚未接到 App.vue 的新组件不会被它覆盖（M4）。
describe('sync 组件编译冒烟', () => {
  it('SyncCard 能编译并导出组件', () => {
    expect(SyncCard).toBeDefined()
  })
})
