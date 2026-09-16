import { defineStore } from 'pinia'
import {
  ListLocations, ListPresets, AddBookmark, RemoveBookmark, AddLocalRecent, AddRemoteRecent,
} from '../../wailsjs/go/main/App'

export const useLocationsStore = defineStore('locations', {
  state: () => ({
    bookmarks: [], localRecents: [], remoteRecents: [],
    // 固定预设（local/remote 来自 presets.toml，localDisks 由后端实时枚举），与用户位置数据分开存。
    presets: { local: [], localDisks: [], remote: [] },
    // 后端 ListPresets 返回的 err：presets.toml 读/解析失败时非空。
    // 由 SftpView 在 load() 之后写进日志面板（只发一次 Init 事件会在前端订阅前丢失）。
    presetsError: '',
    loaded: false,
  }),
  actions: {
    async load() {
      // 两次 IPC 并行；预设失败不该连累书签/最近（绑定异常时各自兜底）。
      const [data, presetData] = await Promise.all([
        ListLocations(),
        ListPresets().catch(() => null),
      ])
      this.bookmarks = (data && data.bookmarks) || []
      this.localRecents = (data && data.localRecents) || []
      this.remoteRecents = (data && data.remoteRecents) || []
      // 后端保证三组非 nil；这里仍兜底 null，避免绑定异常时整块 UI 白屏。
      this.presets = {
        local: (presetData && presetData.local) || [],
        localDisks: (presetData && presetData.localDisks) || [],
        remote: (presetData && presetData.remote) || [],
      }
      this.presetsError = (presetData && presetData.err) || ''
      this.loaded = true
    },
    async addBookmark(b) {
      await AddBookmark(b)
      this.bookmarks = this.bookmarks.concat([b])
    },
    async removeBookmark(scope, host, path) {
      await RemoveBookmark(scope, host, path)
      this.bookmarks = this.bookmarks.filter((b) => !(b.scope === scope && b.host === host && b.path === path))
    },
    async addLocalRecent(path) {
      // 短路：队首已是同一路径 ⇒ 直接返回，不做 IPC、也不改列表。
      // 否则每次目录导航/刷新/批量重载都会写一次配置（后端 AddLocalRecent 每次 saveConfig）。
      if (this.localRecents[0]?.path === path) return
      await AddLocalRecent(path)
      this.localRecents = [{ path, ts: new Date().toISOString() }]
        .concat(this.localRecents.filter((r) => r.path !== path)).slice(0, 20)
    },
    async addRemoteRecent(host, path) {
      // 同上：同一 (host, path) 已经在队首时不再写盘。
      if (this.remoteRecents[0]?.host === host && this.remoteRecents[0]?.path === path) return
      await AddRemoteRecent(host, path)
      this.remoteRecents = [{ host, path, ts: new Date().toISOString() }]
        .concat(this.remoteRecents.filter((r) => !(r.host === host && r.path === path))).slice(0, 20)
    },
    // 本地面板与主机无关；远程面板只看当前主机（spec §9）。
    recentsForPane(pane, host) {
      if (pane === 'local') return this.localRecents
      return this.remoteRecents.filter((r) => r.host === host)
    },
    presetsForPane(pane, host) {
      if (pane === 'local') return this.presets.local
      // 远程预设可带 host：留空 = 所有主机（与 bookmarksForPane 同规则）。
      // host 为空（还没选主机）时不筛，避免面板空白。
      return this.presets.remote.filter((p) => !p.host || !host || p.host === host)
    },
    // 磁盘是本地概念：远程面板恒为空组。
    disksForPane(pane) {
      return pane === 'local' ? this.presets.localDisks : []
    },
    // 书签同样按面板/主机过滤：否则远程面板会列出别的主机的书签。
    bookmarksForPane(pane, host) {
      return this.bookmarks.filter((b) => (pane === 'local'
        ? b.scope === 'local'
        : (b.scope === 'remote' && (!host || b.host === host))))
    },
  },
})
