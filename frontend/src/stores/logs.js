import { defineStore } from 'pinia'

export const useLogStore = defineStore('logs', {
  state: () => ({
    logs: [],
    cap: 1000,
    filterSource: '',
    filterLevel: '',
  }),
  getters: {
    filtered(state) {
      let out = state.logs
      if (state.filterSource) out = out.filter(l => l.source_id === state.filterSource)
      if (state.filterLevel) out = out.filter(l => l.level === state.filterLevel)
      return out
    },
  },
  actions: {
    add(evt) {
      this.logs.push(evt)
      if (this.logs.length > this.cap) this.logs.splice(0, this.logs.length - this.cap)
    },
    clear() { this.logs = [] },
  },
})
