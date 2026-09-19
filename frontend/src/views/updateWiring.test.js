// Task 16 结构性不变量 + 角标派生行为。
// 仓库约定：无 jsdom / 无 @vue/test-utils，组件只能 SSR；「启动接线」「角标单一来源」这类
// 跨文件形状靠读源码钉死（仿 sftpDropWiring.test.js），而 store 的角标派生行为则 mock
// Wails 绑定后直接单测（同 stores/update.test.js）。
import { describe, it, expect, beforeEach, vi } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { createPinia, setActivePinia } from "pinia";
import { useUpdateStore, emptyInfo } from "../stores/update";

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(resolve(here, p), "utf8");

// 把 Wails 绑定/事件运行时挡在测试之外：本文件只需要 store 的纯派生行为。
const backend = vi.hoisted(() => ({ listeners: new Map() }));
vi.mock("../../wailsjs/go/main/App", () => ({
  GetUpdateInfo: async () => null,
  CheckUpdate: async () => ({}),
  StartUpdateDownload: async () => {},
  CancelUpdateDownload: async () => {},
  ApplyUpdateAndRestart: async () => {},
  DiscardUpdateDownload: async () => {},
  SkipUpdateVersion: async () => {},
  ClearSkippedUpdate: async () => {},
  OpenReleasePage: async () => {},
}));
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

// 取 onMounted/onUnmounted 这类「回调实参」的函数体（按花括号配对，避免正则截断嵌套箭头函数）。
function callBody(src, marker) {
  const at = src.indexOf(marker);
  if (at < 0) return "";
  const brace = src.indexOf("{", at);
  if (brace < 0) return "";
  let depth = 0;
  for (let i = brace; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}") {
      depth--;
      if (depth === 0) return src.slice(brace, i + 1);
    }
  }
  return "";
}

describe("更新功能的结构性不变量", () => {
  it("角标只有一个来源（store 派生值），不额外拉取", () => {
    const app = read("../App.vue");
    expect(app).toContain("updateStore.badgeVisible");
    expect(app).not.toContain("CheckUpdate(");
  });

  it("打开设置时确认角标（acknowledge）", () => {
    const app = read("../App.vue");
    expect(app).toContain("updateStore.acknowledge()");
  });

  it("自动检查的默认值来自后端，不在前端硬编码开关默认", () => {
    const settings = read("../stores/settings.js");
    expect(settings).toContain("s.update_check_auto !== false");
  });

  it("跳过与取消跳过之后必须重读设置（否则前端快照会把它清掉）", () => {
    const sec = read("../components/UpdateSection.vue");
    expect(sec).toMatch(/skip\(\)[\s\S]*settings\.load\(\)/);
    expect(sec).toMatch(/clearSkip\(\)[\s\S]*settings\.load\(\)/);
  });

  it("重启并升级只在 ready 时出现（由 utils 的按钮矩阵决定）", () => {
    const sec = read("../components/UpdateSection.vue");
    expect(sec).toContain("acts.apply");
  });
});

// —— 裁定补充：启动接线（裁定 1/2） ——
describe("启动接线不变量（裁定 1/2）", () => {
  const app = read("../App.vue");

  it("App.vue 挂载时 hydrate（带 .catch）+ ensureListening，卸载时 stopListening", () => {
    const mounted = callBody(app, "onMounted(");
    const unmounted = callBody(app, "onUnmounted(");
    expect(mounted, "找不到 App.vue 的 onMounted 回调体").not.toBe("");
    expect(mounted).toContain("updateStore.ensureListening()");
    expect(mounted).toContain("updateStore.hydrate()");
    expect(app, "hydrate 必须 .catch 兜底，不能留下未处理 rejection").toMatch(/updateStore\.hydrate\(\)\s*\.catch\(/);
    expect(unmounted, "找不到 App.vue 的 onUnmounted 回调体").not.toBe("");
    expect(unmounted).toContain("updateStore.stopListening()");
    expect(app).toContain("const updateStore = useUpdateStore()");
    expect(app).toMatch(/import\s*\{[^}]*useUpdateStore[^}]*\}\s*from\s*["']\.\/stores\/update["']/);
  });

  it("启动 hydrate 不依赖 SettingsDialog/UpdateSection 的 onMounted（v-if 子树，打开设置才执行）", () => {
    // App.vue 不得引入位于 SettingsDialog v-if 子树里的 UpdateSection 来间接 hydrate
    // （按 import/标签判定，避免把解释性注释也算进来）
    expect(app).not.toMatch(/import\s+UpdateSection\b/);
    expect(app).not.toMatch(/<UpdateSection\b/);
    // 反证前提：真正会 hydrate 的是 UpdateSection（所以不能依赖它在启动时跑）
    const sec = read("../components/UpdateSection.vue");
    expect(sec).toMatch(/onMounted\([\s\S]*?update\.hydrate\(\)/);
    // SettingsDialog 自身不 hydrate，只是条件挂载 UpdateSection
    const dialog = read("../components/SettingsDialog.vue");
    expect(dialog).not.toMatch(/\.(hydrate|ensureListening)\(/);
  });

  it("退订走 store 保存的退订函数：App.vue 调 stopListening，不直接 EventsOff 事件名（Task 14 M3）", () => {
    expect(app).toContain("updateStore.stopListening()");
    // 直接 EventsOff("update:state","update:progress") 会清掉这两个事件名的全部监听器
    // （按「调用」判定：注释里出现该名字不算）
    expect(app).not.toMatch(/EventsOff\s*\(/);
    expect(app).not.toContain("update:state");
    expect(app).not.toContain("update:progress");
  });
});

// —— 裁定补充：角标文案单一来源（裁定 3/5） ——
describe("角标与状态文案共用同一来源（裁定 3/5）", () => {
  it("App.vue 文案取自 utils/update.js，不另造状态字符串", () => {
    const app = read("../App.vue");
    const sec = read("../components/UpdateSection.vue");
    expect(app).toMatch(/from\s*["']\.\/utils\/update["']/);
    expect(app).toMatch(/\b(stateLabel|statusMessage)\(/);
    expect(sec).toMatch(/from\s*["']\.\.\/utils\/update["']/);
    expect(sec).toContain("statusMessage(");
    for (const st of ["available", "ready", "skipped", "disabled", "downloading"]) {
      expect(app, st + " 不应出现在 App.vue（角标不得另造状态→文案映射）").not.toContain('"' + st + '"');
    }
  });
});

// —— 裁定补充：角标只对 available/ready 显示（裁定 3） ——
const info = (over = {}) => ({ ...emptyInfo, ...over });

describe("角标派生：只对 available/ready 显示（裁定 3）", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("available/ready 且有 latest → 亮；其余状态一律不亮", () => {
    const s = useUpdateStore();
    let seq = 1;
    const hidden = ["idle", "checking", "up-to-date", "skipped", "rate-limited", "check-failed",
      "no-asset", "no-checksum", "downloading", "verify-failed", "io-failed", "applying", "disabled"];
    for (const st of hidden) {
      s.applyState(info({ seq: seq++, state: st, latest: "v0.7.0" }));
      expect(s.badgeVisible, st + " 不应亮角标").toBe(false);
    }
    s.applyState(info({ seq: seq++, state: "available", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
    s.applyState(info({ seq: seq++, state: "ready", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
  });
});

// —— 裁定补充：显式钉住 Task 14 的 M1「latest 非空」严格性（裁定 4） ——
describe("角标派生：M1「latest 非空」严格性（裁定 4，显式钉住）", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("state=available 但 latest 为空 → 不亮（比 spec §12.2 更严；有意保留该防御）", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "available", latest: "" }));
    expect(s.info.state).toBe("available");
    expect(s.badgeVisible).toBe(false);
  });

  it("同一状态但 latest 非空 → 亮（对照：证明上一条不是状态判定失败）", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "available", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
  });

  it("ready 且 latest 为空 → 同样不亮（严格性对两个状态一致）", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "ready", latest: "" }));
    expect(s.badgeVisible).toBe(false);
  });
});
