import { defineStore } from 'pinia'
import { GetSftpUI, SaveSftpUI } from '../../wailsjs/go/main/App'

// 排序键白名单与后端 config.SftpUIState.Normalize 逐字一致（name|size|modTime）。
// 前端兜底的理由不是"后端会写坏值"，而是绑定返回畸形 / 组件误传时**绝不把坏值写回盘**：
// Go 侧 json tag 精确匹配，写进去的非法值会被后端的 Normalize 再次纠正，但那要等下一次读。
const SORT_KEYS = ['name', 'size', 'modTime']

function normalizeSortKey(key) {
  return SORT_KEYS.includes(key) ? key : 'name'
}

// 与后端 normalizeSortDir 同口径：<0 → -1（降序），否则 → 1（升序，0 不是合法方向）。
function normalizeSortDir(dir) {
  return dir < 0 ? -1 : 1
}

// 归一 {key, dir}；对 undefined/畸形输入也返回合法结构（面板读 undefined 会白屏）。
function normalizeSort(sort) {
  const s = sort || {}
  return { key: normalizeSortKey(s.key), dir: normalizeSortDir(s.dir) }
}

export const useSftpUIStore = defineStore('sftpui', {
  state: () => ({
    loaded: false,
    lastLocalDir: '',
    lastRemoteDirs: {}, // host（不含 user）→ 最后路径
    sort: { local: { key: 'name', dir: 1 }, remote: { key: 'name', dir: 1 } },
  }),
  actions: {
    async load() {
      // 后端返回的是整体快照。失败**不吞**：err() 是 SftpView 的组件内函数，store 调不到，
      // 所以让调用方（onMounted）catch 后走既有 err()；这里也不置 loaded，避免"半截状态已加载"。
      const data = await GetSftpUI()
      this.lastLocalDir = (data && data.last_local_dir) || ''
      this.lastRemoteDirs = { ...((data && data.last_remote_dirs) || {}) }
      // 后端已归一；这里只防"绑定返回畸形"（规格 §7.1）
      this.sort = {
        local: normalizeSort({ key: data && data.sort_local_key, dir: data && data.sort_local_dir }),
        remote: normalizeSort({ key: data && data.sort_remote_key, dir: data && data.sort_remote_dir }),
      }
      this.loaded = true
    },
    // 本地面板与主机无关；远程按 host 取（规格 §7.1）。
    lastPathFor(pane, host) {
      if (pane === 'local') return this.lastLocalDir || ''
      return this.lastRemoteDirs[host] || ''
    },
    async setSort(pane, key, dir) {
      const next = normalizeSort({ key, dir })
      const cur = this.sort[pane]
      // 无变化短路要覆盖**整条** setSort：连 saveSnapshot() 里的"读回"都不该发生，
      // 否则用户重复点同一列就会多一次 GetSftpUI。
      if (cur && cur.key === next.key && cur.dir === next.dir) return
      // 先改内存再写盘：写失败由 saveSnapshot 抛出、调用方 err()，内存**不回滚**
      // —— 下次变更会再试（§7.1 FF9），且 UI 与用户刚做的操作保持一致。
      this.sort[pane] = next
      await this.saveSnapshot()
    },
    // 同会话内存镜像：locations store 落盘成功后调用（§7.2）。纯内存、零新增 IPC，
    // 也绝不触碰 loaded/sort 等其它状态 —— 镜像只负责"断开→重连立刻拿到正确路径"。
    noteLocalPath(path) {
      this.lastLocalDir = path || ''
    },
    noteRemotePath(host, path) {
      if (!host) return // 空 host 会污染 last_remote_dirs 的键空间
      this.lastRemoteDirs = { ...this.lastRemoteDirs, [host]: path || '' }
    },
    // 本 store 唯一的 SaveSftpUI 出口（§6.3.3）。**必须先读回**：后端还有
    // AddRemoteRecent / SftpHome 等写路径会更新 cur，若整结构回写就会把较新的路径字段
    // 静默回滚（审核 BF2）。
    async saveSnapshot() {
      const cur = await GetSftpUI()
      const curRemote = (cur && cur.last_remote_dirs) || {}
      const local = normalizeSort(this.sort.local)
      const remote = normalizeSort(this.sort.remote)
      const payload = {
        // 六个键逐字 snake_case（禁止 {...this.$state} 展开）：Go 侧按 json tag 精确解码，
        // camelCase 会被静默忽略 ⇒ 整段清零。
        last_local_dir: (cur && cur.last_local_dir) || '',
        // 路径 map 以读回为基底、本 store 知道的 host 胜出：整体覆盖会抹掉后端其它写路径
        // 加进来的 host，只取本 store 又会回滚它们。
        last_remote_dirs: { ...curRemote, ...this.lastRemoteDirs },
        sort_local_key: local.key,
        sort_local_dir: local.dir,
        sort_remote_key: remote.key,
        sort_remote_dir: remote.dir,
      }
      await SaveSftpUI(payload)
    },
  },
})
