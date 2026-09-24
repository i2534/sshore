import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('./LogPanel.vue', import.meta.url), 'utf8')

describe('日志面板', () => {
  it('chips 保留 v-if（SFTP 页不传 tunnels，chips 恒空；去掉 v-if 会留一条空带）', () => {
    expect(src).toMatch(/<div v-if="chips\.length" class="chips"/)
    expect(src).not.toMatch(/\.chips\s*\{[^}]*min-height/)
  })
  it('文案中文化：不再出现英文 filter source_id 占位符', () => {
    expect(src).not.toContain('filter source_id')
    expect(src).toContain('按来源筛选（规则名/主机）')
  })
})
