import { defineStore } from "pinia";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import {
  GetUpdateInfo, CheckUpdate, StartUpdateDownload, CancelUpdateDownload,
  ApplyUpdateAndRestart, DiscardUpdateDownload, SkipUpdateVersion, ClearSkippedUpdate, OpenReleasePage,
} from "../../wailsjs/go/main/App";

// 后端契约（internal/update.UpdateInfo）的完整空快照：applyState 收到的是**完整**快照，
// 用 emptyInfo 展开而不是与旧值合并，避免上一轮下载的瞬时 done/total 残留到新状态。
export const emptyInfo = {
  seq: 0, state: "idle", current: "", latest: "", notes: "", published_at: "", source: "",
  progress: 0, ready_path: "", skipped: false, manual: false, hint: "", error: "", pending_log: "",
};

export const UPDATE_STATE_EVENT = "update:state";
export const UPDATE_PROGRESS_EVENT = "update:progress";

// 角标（spec §12.2）：只有 available/ready 亮；acknowledge 记「已确认版本号」，换版本重新亮。
const BADGE_STATES = ["available", "ready"];

function messageOf(e) {
  return e && e.message ? String(e.message) : String(e);
}

export const useUpdateStore = defineStore("update", {
  state: () => ({
    info: { ...emptyInfo },
    // seq 单调（后端 Seq 递增）：lastSeq 初值 < 0，保证 seq=0 的首个快照也能归约。
    lastSeq: -1,
    ackedVersion: "",
    // 确认时的状态：spec §12.2 要求「状态由 available 走到 ready 时重新亮」，
    // 只比较版本号无法区分这两种状态（同一版本从 available→ready）。
    ackedState: "",
    listening: false,
    offState: null,
    offProgress: null,
    error: "",
  }),
  getters: {
    // brief 用的字段名是 err；两者都暴露，读法不敏感。
    err: (s) => s.error,
    badgeVisible: (s) => BADGE_STATES.includes(s.info.state)
      && Boolean(s.info.latest)
      && !(s.info.latest === s.ackedVersion && s.info.state === s.ackedState),
    progress: (s) => s.info.progress,
  },
  actions: {
    ensureListening() {
      if (this.listening) return;
      this.listening = true;
      // 先订阅，再取快照：否则「快照晚于事件」会用旧值覆盖新状态；seq 是第二道保险。
      this.offState = EventsOn(UPDATE_STATE_EVENT, (payload) => this.applyState(payload));
      this.offProgress = EventsOn(UPDATE_PROGRESS_EVENT, (payload) => this.applyProgress(payload));
    },
    // 兼容两种命名（brief/plan 用 ensureListening，裁定与调用方也可能写 ensureSubscribed）。
    ensureSubscribed() {
      this.ensureListening();
    },
    // 退订：只用 EventsOn 返回的退订函数（仓库 App.vue 的既有约定）。
    // 不再用 EventsOff 兜底（Task 14 评审 M3 判定安全：每个订阅都保存了自己的退订函数）。
    stopListening() {
      for (const off of [this.offState, this.offProgress]) {
        if (typeof off !== "function") continue;
        try { off(); } catch (e) { /* 已失效的退订函数不应影响清理 */ }
      }
      this.offState = null;
      this.offProgress = null;
      this.listening = false;
    },
    // 自动/挂载路径：失败落 error 字段供设置页与调用方读取，不抛（应用启动不应因取快照失败而崩）。
    async hydrate() {
      this.ensureListening();
      try {
        this.applyState(await GetUpdateInfo());
        this.error = "";
      } catch (e) {
        this.error = messageOf(e);
      }
    },
    // 完整快照归约：只接受 seq 严格更大的载荷（丢弃迟到/重复事件）。返回是否被采纳。
    applyState(payload) {
      if (!payload) return false;
      const seq = Number(payload.seq);
      if (!Number.isFinite(seq)) return false;
      if (seq <= this.lastSeq) return false;
      this.lastSeq = seq;
      this.info = { ...emptyInfo, ...payload };
      return true;
    },
    // 进度归约：保留 done/total（progressText 的「已下载 / 总字节」依赖它们）。
    // total<=0 表示未知总长：若同时有可用 percent，就不能留下 total<=0，
    // 否则 progressPercent 会直接返回 null，把已知百分比退化为 indeterminate（Task 13 评审 Minor 1）。
    // 未知总长且无 percent（后端用 -1）时保留 done 与 total，UI 走「已下载 X」+ indeterminate。
    applyProgress(payload) {
      if (!payload) return;
      const done = Number(payload.done);
      const total = Number(payload.total);
      const percent = Number(payload.percent);
      const hasPercent = Number.isFinite(percent) && percent >= 0;
      const knownTotal = Number.isFinite(total) && total > 0;
      const next = { ...this.info };
      if (Number.isFinite(done) && done >= 0) next.done = done;
      next.total = knownTotal ? total : (hasPercent ? null : total);
      if (Number.isFinite(percent)) next.progress = percent;
      this.info = next;
    },
    acknowledge() {
      this.ackedVersion = this.info.latest || "";
      this.ackedState = this.info.state || "";
    },
    // 用户动作：错误落 error 字段后原样 rethrow —— 调用方必须能感知失败（不静默）。
    async _call(fn) {
      this.error = "";
      try {
        return await fn();
      } catch (e) {
        this.error = messageOf(e);
        throw e;
      }
    },
    async check() {
      return this._call(async () => { this.applyState(await CheckUpdate(true)); });
    },
    async download() {
      return this._call(async () => { await StartUpdateDownload(); });
    },
    async cancel() {
      return this._call(async () => { await CancelUpdateDownload(); });
    },
    async apply() {
      return this._call(async () => { await ApplyUpdateAndRestart(); });
    },
    async discard() {
      return this._call(async () => { await DiscardUpdateDownload(); this.applyState(await GetUpdateInfo()); });
    },
    async skip(version) {
      return this._call(async () => { await SkipUpdateVersion(version); this.applyState(await GetUpdateInfo()); });
    },
    async clearSkip() {
      return this._call(async () => { await ClearSkippedUpdate(); this.applyState(await GetUpdateInfo()); });
    },
    async openPage() {
      return this._call(async () => { await OpenReleasePage(); });
    },
    async getUpdateInfo() {
      return this._call(async () => {
        const snap = await GetUpdateInfo();
        this.applyState(snap);
        return snap;
      });
    },
  },
});
