import { describe, it, expect } from 'vitest'
import SyncCard from '../components/SyncCard.vue'
import SyncConflictsDialog from '../components/SyncConflictsDialog.vue'
import SyncView from './SyncView.vue'

describe('sync 组件编译冒烟', () => {
  it('SyncCard 能编译并导出组件', () => {
    expect(SyncCard).toBeDefined()
  })
  it('SyncConflictsDialog 能编译并导出组件', () => {
    expect(SyncConflictsDialog).toBeDefined()
  })
  it('SyncView 能编译并导出组件', () => {
    expect(SyncView).toBeDefined()
  })
})
