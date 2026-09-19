import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { progressPercent, progressText } from "../utils/update";
import { useUpdateStore } from "./update";

// 把 Wails 绑定与事件运行时挡在测试之外（无 jsdom / 无 @vue/test-utils 的仓库约定）。
// vi.hoisted 让假后端先于 vi.mock 工厂就绪（同 stores/settings.test.js）。
const backend = vi.hoisted(() => ({
  info: null,      // GetUpdateInfo/CheckUpdate 的返回快照
  infoFn: null,    // 需要控制 resolve 时机时替换 info 的取数函数
  fail: null,      // { 绑定名: Error } 令对应绑定 reject
  calls: [],       // 记录绑定调用 [name, ...args]
  listeners: new Map(), // 事件名 -> handler[]
}));

vi.mock("../../wailsjs/go/main/App", () => {
  const rec = (name, snapshot) => async (...args) => {
    backend.calls.push([name, ...args]);
    const fail = backend.fail && backend.fail[name];
    if (fail) throw fail;
    if (!snapshot) return undefined;
    return backend.infoFn ? backend.infoFn(...args) : backend.info;
  };
  return {
    GetUpdateInfo: rec("GetUpdateInfo", true),
    CheckUpdate: rec("CheckUpdate", true),
    StartUpdateDownload: rec("StartUpdateDownload", false),
    CancelUpdateDownload: rec("CancelUpdateDownload", false),
    ApplyUpdateAndRestart: rec("ApplyUpdateAndRestart", false),
    DiscardUpdateDownload: rec("DiscardUpdateDownload", false),
    SkipUpdateVersion: rec("SkipUpdateVersion", false),
    ClearSkippedUpdate: rec("ClearSkippedUpdate", false),
    OpenReleasePage: rec("OpenReleasePage", false),
  };
});

vi.mock("../../wailsjs/runtime/runtime", () => ({
  EventsOn: (name, cb) => {
    const list = backend.listeners.get(name) || [];
    list.push(cb);
    backend.listeners.set(name, list);
    return () => {
      const cur = backend.listeners.get(name) || [];
      backend.listeners.set(name, cur.filter((fn) => fn !== cb));
    };
  },
  EventsOff: (name) => { backend.listeners.delete(name); },
}));

const info = (over = {}) => ({ seq: 1, state: "idle", current: "v0.6.0", latest: "", progress: 0, skipped: false, hint: "", pending_log: "", ...over });

describe("update store", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("按 seq 丢弃迟到的旧载荷", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 5, state: "available", latest: "v0.7.0" }));
    s.applyState(info({ seq: 3, state: "idle" }));
    expect(s.info.state).toBe("available");
  });

  it("progress 只更新进度字段", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "downloading" }));
    s.applyProgress({ done: 10, total: 100, percent: 10 });
    expect(s.info.progress).toBe(10);
    expect(s.info.state).toBe("downloading");
  });

  it("available 时角标可见，acknowledge 后消失；换版本后重新可见", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "available", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
    s.acknowledge();
    expect(s.badgeVisible).toBe(false);
    s.applyState(info({ seq: 2, state: "available", latest: "v0.8.0" }));
    expect(s.badgeVisible).toBe(true);
  });

  it("available 已确认后进入 ready 会重新亮（spec §12.2）", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "available", latest: "v0.7.0" }));
    s.acknowledge();
    expect(s.badgeVisible).toBe(false);
    s.applyState(info({ seq: 2, state: "ready", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
  });

  it("skipped / disabled 不亮角标", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "skipped", latest: "v0.7.0", skipped: true }));
    expect(s.badgeVisible).toBe(false);
    s.applyState(info({ seq: 2, state: "disabled" }));
    expect(s.badgeVisible).toBe(false);
  });

  it("ready 状态也亮角标", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "ready", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
  });
});

describe("update store：seq 严格单调", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("相同 seq 的重复事件不覆盖已归约的状态", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 5, state: "available", latest: "v0.7.0" }));
    s.applyState(info({ seq: 5, state: "idle" }));
    expect(s.info.state).toBe("available");
    expect(s.info.latest).toBe("v0.7.0");
  });

  it("seq=0 的首个快照仍可归约（初始 lastSeq 必须小于 0）", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 0, state: "disabled" }));
    expect(s.info.state).toBe("disabled");
  });

  it("applyProgress 不推进 seq，慢到的旧快照仍被丢弃", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 4, state: "downloading" }));
    s.applyProgress({ done: 1, total: 2, percent: 50 });
    s.applyState(info({ seq: 2, state: "idle" }));
    expect(s.info.state).toBe("downloading");
  });
});

describe("update store：progress 归约保留 done/total", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("已知总长：done/total 写入 info，progressText 走「已下载 / 总字节」", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "downloading" }));
    s.applyProgress({ done: 1048576, total: 2097152, percent: 50 });
    expect(s.info.done).toBe(1048576);
    expect(s.info.total).toBe(2097152);
    expect(progressPercent(s.info)).toBe(50);
    expect(progressText(s.info)).toBe("1.0 MB / 2.0 MB");
  });

  it("未知总长又给了 percent：不得留下 total<=0，否则已知百分比退化为 indeterminate（Minor 1）", () => {
    const s = useUpdateStore();
    s.applyProgress({ done: 512, total: 0, percent: 25 });
    expect(s.info.total == null).toBe(true);
    expect(progressPercent(s.info)).toBe(25);
    expect(progressText(s.info)).toBe("25%");
  });

  it("未知总长且 percent<0：indeterminate，但仍保留 done 供「已下载 X」", () => {
    const s = useUpdateStore();
    s.applyProgress({ done: 512, total: -1, percent: -1 });
    expect(progressPercent(s.info)).toBeNull();
    expect(s.info.done).toBe(512);
    expect(progressText(s.info)).toBe("已下载 512 B");
  });

  it("后续 state 快照是完整快照：清掉上一轮下载的瞬时 done/total", () => {
    const s = useUpdateStore();
    s.applyProgress({ done: 10, total: 100, percent: 10 });
    s.applyState(info({ seq: 2, state: "ready" }));
    expect(s.info.done).toBeUndefined();
    expect(s.info.total == null).toBe(true);
  });
});

describe("update store：事件订阅幂等", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    backend.listeners.clear();
    backend.calls.length = 0;
    backend.fail = null;
    backend.infoFn = null;
    backend.info = info({ seq: 1 });
  });

  it("重复 hydrate / ensureListening / ensureSubscribed 不累积监听器", async () => {
    const s = useUpdateStore();
    await s.hydrate();
    await s.hydrate();
    s.ensureListening();
    s.ensureSubscribed();
    expect(backend.listeners.get("update:state")).toHaveLength(1);
    expect(backend.listeners.get("update:progress")).toHaveLength(1);
  });

  it("事件到达即归约到 info（状态 + 进度）", async () => {
    const s = useUpdateStore();
    await s.hydrate();
    backend.listeners.get("update:state")[0](info({ seq: 7, state: "downloading", latest: "v0.7.0" }));
    backend.listeners.get("update:progress")[0]({ done: 50, total: 100, percent: 50 });
    expect(s.info.state).toBe("downloading");
    expect(s.info.done).toBe(50);
    expect(s.info.total).toBe(100);
    expect(progressPercent(s.info)).toBe(50);
  });

  it("先订阅后取快照：迟到的旧快照不覆盖已到的事件（spec §12.2 竞态）", async () => {
    const s = useUpdateStore();
    let release;
    backend.infoFn = () => new Promise((resolve) => { release = () => resolve(info({ seq: 3, state: "idle" })); });
    const pending = s.hydrate();
    // hydrate 已同步注册监听：此刻事件先到（seq 9 > 快照 seq 3）
    backend.listeners.get("update:state")[0](info({ seq: 9, state: "available", latest: "v0.9.0" }));
    release();
    await pending;
    expect(s.info.state).toBe("available");
    expect(s.info.seq).toBe(9);
  });

  it("stopListening 清理监听器，重订阅后仍只有一份", async () => {
    const s = useUpdateStore();
    await s.hydrate();
    expect(backend.listeners.get("update:state")).toHaveLength(1);
    s.stopListening();
    expect(backend.listeners.get("update:state") || []).toHaveLength(0);
    await s.hydrate();
    expect(backend.listeners.get("update:state")).toHaveLength(1);
    expect(backend.listeners.get("update:progress")).toHaveLength(1);
  });
});

describe("update store：9 个动作封装", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    backend.listeners.clear();
    backend.calls.length = 0;
    backend.fail = null;
    backend.infoFn = null;
    backend.info = info({ seq: 1, state: "idle" });
  });

  it("每个动作调用对应绑定，刷新类动作随后取一次快照", async () => {
    const s = useUpdateStore();
    await s.check();
    await s.download();
    await s.cancel();
    await s.apply();
    await s.discard();
    await s.skip("v0.7.0");
    await s.clearSkip();
    await s.openPage();
    await s.getUpdateInfo();
    expect(backend.calls).toEqual([
      ["CheckUpdate", true],
      ["StartUpdateDownload"],
      ["CancelUpdateDownload"],
      ["ApplyUpdateAndRestart"],
      ["DiscardUpdateDownload"], ["GetUpdateInfo"],
      ["SkipUpdateVersion", "v0.7.0"], ["GetUpdateInfo"],
      ["ClearSkippedUpdate"], ["GetUpdateInfo"],
      ["OpenReleasePage"],
      ["GetUpdateInfo"],
    ]);
  });

  const failing = [
    ["check", "CheckUpdate"],
    ["download", "StartUpdateDownload"],
    ["cancel", "CancelUpdateDownload"],
    ["apply", "ApplyUpdateAndRestart"],
    ["discard", "DiscardUpdateDownload"],
    ["skip", "SkipUpdateVersion"],
    ["clearSkip", "ClearSkippedUpdate"],
    ["openPage", "OpenReleasePage"],
    ["getUpdateInfo", "GetUpdateInfo"],
  ];
  for (const [action, binding] of failing) {
    it(`${action} 失败时落 error 字段且 promise rejected（${binding}）`, async () => {
      backend.fail = { [binding]: new Error("boom-" + binding) };
      const s = useUpdateStore();
      await expect(s[action]("v0.9.0")).rejects.toThrow("boom-" + binding);
      expect(s.error).toContain("boom-" + binding);
    });
  }

  it("hydrate 失败落 error 字段且不抛（应用启动不应崩）", async () => {
    backend.fail = { GetUpdateInfo: new Error("boom-info") };
    const s = useUpdateStore();
    await expect(s.hydrate()).resolves.toBeUndefined();
    expect(s.error).toContain("boom-info");
  });

  it("成功动作清空上一次的 error", async () => {
    backend.fail = { StartUpdateDownload: new Error("boom") };
    const s = useUpdateStore();
    await expect(s.download()).rejects.toThrow("boom");
    expect(s.error).toContain("boom");
    backend.fail = null;
    await s.download();
    expect(s.error).toBe("");
  });
});
