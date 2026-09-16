import { defineStore } from 'pinia'
import {
  ListLocations, AddBookmark, RemoveBookmark, AddLocalRecent, AddRemoteRecent,
} from '../../wailsjs/go/main/App'

export const useLocationsStore = defineStore('locations', {
  state: () => ({ bookmarks: [], localRecents: [], remoteRecents: [], loaded: false }),
  actions: {
    async load() {
      const data = await ListLocations()
      this.bookmarks = (data && data.bookmarks) || []
      this.localRecents = (data && data.localRecents) || []
      this.remoteRecents = (data && data.remoteRecents) || []
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
    // 书签同样按面板/主机过滤：否则远程面板会列出别的主机的书签。
    bookmarksForPane(pane, host) {
      return this.bookmarks.filter((b) => (pane === 'local'
        ? b.scope === 'local'
        : (b.scope === 'remote' && (!host || b.host === host))))
    },
  },
})
