// UpdateSection.vue 的**真实组件渲染**测试。
// - SSR（沿用仓库无 jsdom / 无 @vue/test-utils 的约定）：断言模板对 store 状态 +
//   utils/update.js 纯函数的绑定（改模板或改纯函数都会让用例变红）。
// - A（更新源防抖落盘）无法派发 DOM 事件，改为直接单测组件导出的 createSourceCommitter，
//   并用 vi.mock 的 SetSettings 计数断言「连续输入只落盘一次」。
import { describe, it, expect, vi } from "vitest";
import { createSSRApp } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createPinia, setActivePinia } from "pinia";
import { readFileSync } from "node:fs";
import UpdateSection, { createSourceCommitter } from "./UpdateSection.vue";
import { useUpdateStore, emptyInfo } from "../stores/update";
import { useSettingsStore } from "../stores/settings";

// 把 Wails 绑定挡在测试之外：A 的用例会真实执行 settings.save()，用 SetSettings 计数断言。
const backend = vi.hoisted(() => ({ saved: [], settings: {} }));
vi.mock("../../wailsjs/go/main/App", () => ({
  GetSettings: async () => backend.settings,
  SetSettings: async (payload) => { backend.saved.push(payload) },
  SyncWindowBackground: async () => {},
  GetUpdateInfo: async () => ({ seq: 0, state: "idle" }),
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
  EventsOn: () => () => {},
  EventsOff: () => {},
}));

// 先用同一 pinia 实例给 store 播种状态，再把该 pinia 交给 SSR app（组件经 inject 拿到同一实例）。
async function render(seed) {
  const pinia = createPinia();
  setActivePinia(pinia);
  if (seed) seed({ update: useUpdateStore(), settings: useSettingsStore() });
  const app = createSSRApp(UpdateSection);
  app.use(pinia);
  return renderToString(app);
}

const info = (over = {}) => ({ ...emptyInfo, ...over });
const source = (rel) => readFileSync(new URL(rel, import.meta.url), "utf8");

describe("UpdateSection SSR", () => {
  it("渲染标题与检查按钮", async () => {
    const app = createSSRApp(UpdateSection);
    app.use(createPinia());
    const html = await renderToString(app);
    expect(html).toContain("更新");
    expect(html).toContain("检查更新");
  });

  it("pending_log 展示截断到 2000 字符（长日志不能撑爆设置页）", async () => {
    const html = await render(({ update }) => {
      update.info = info({ state: "io-failed", pending_log: "L".repeat(2500) });
    });
    expect(html).toContain("L".repeat(2000));
    expect(html).not.toContain("L".repeat(2001));
    expect(html).toContain("…");
  });

  it("下载中：已知总长用 progressText 显示「已下载 / 总长」与百分比", async () => {
    const html = await render(({ update }) => {
      update.info = info({ state: "downloading", done: 1048576, total: 2097152, progress: 50 });
    });
    expect(html).toContain("1.0 MB / 2.0 MB");
    expect(html).toContain("50%");
  });

  it("下载中：总长未知（progressPercent 为 null）→ indeterminate 进度条 + 「已下载 X」", async () => {
    const html = await render(({ update }) => {
      update.info = info({ state: "downloading", done: 512, total: -1, progress: -1 });
    });
    expect(html).toContain("已下载 512 B");
    expect(html).toContain("indet");
    expect(html).not.toContain("0%");
  });

  it("按钮矩阵来自 actions(info)：available 给下载/跳过，ready 给重启并升级", async () => {
    const available = await render(({ update }) => {
      update.info = info({ state: "available", latest: "v0.7.0" });
    });
    expect(available).toContain("下载更新");
    expect(available).toContain("跳过此版本");
    expect(available).not.toContain("重启并升级");

    const ready = await render(({ update }) => {
      update.info = info({ state: "ready", latest: "v0.7.0" });
    });
    expect(ready).toContain("重启并升级");
    expect(ready).not.toContain("下载更新");
  });

  it("失败/人工升级提示时给「打开发布页」，并展示 error 与 manual-upgrade 文案", async () => {
    const html = await render(({ update }) => {
      update.info = info({ state: "io-failed", error: "disk full", hint: "manual-upgrade" });
    });
    expect(html).toContain("打开发布页");
    expect(html).toContain("disk full");
    expect(html).toContain("手动步骤");
  });

  it("间隔下拉含 12/24/48/168/关闭，并渲染更新源输入框、自动检查开关与恢复默认", async () => {
    const html = await render();
    for (const label of ["12 小时", "24 小时", "48 小时", "168 小时（7 天）", "关闭轮询"]) {
      expect(html).toContain(label);
    }
    expect(html).toContain('id="update-source"');
    expect(html).toContain("恢复默认");
    expect(html).toContain("自动检查更新");
  });

  it("非 https 的自定义更新源给风险提示", async () => {
    const html = await render(({ settings }) => {
      settings.updateSource = "http://mirror.example.com";
    });
    expect(html).toContain("必须使用 https");
  });

  it("已跳过的版本只读展示，不提供编辑入口", async () => {
    const html = await render(({ settings }) => {
      settings.updateSkippedVersion = "0.7.0";
    });
    expect(html).toContain("已跳过");
    expect(html).toContain("0.7.0");
  });

  // —— fix round 1 B：更新说明折叠展示 published_at + notes（spec §12.1） ——
  it("更新说明折叠展示格式化的 published_at 与 notes（spec §12.1）", async () => {
    const html = await render(({ update }) => {
      update.info = info({
        state: "available", latest: "v0.7.0",
        notes: "修复若干问题", published_at: "2026-09-19T00:00:00Z",
      });
    });
    const detail = html.slice(html.indexOf("<details"), html.indexOf("</details>"));
    expect(detail).toContain("发布于 2026-09-19 00:00 UTC");
    expect(detail).toContain("修复若干问题");
  });

  it("published_at 缺失或非法时不渲染日期行（不出现 NaN）", async () => {
    const missing = await render(({ update }) => {
      update.info = info({ state: "available", notes: "说明", published_at: "" });
    });
    expect(missing).toContain("说明");
    expect(missing).not.toContain("发布于");

    const invalid = await render(({ update }) => {
      update.info = info({ state: "available", notes: "说明", published_at: "not-a-date" });
    });
    expect(invalid).not.toContain("发布于");
    expect(invalid).not.toContain("NaN");
  });

  // —— fix round 1 C：https 提示必须排除 loopback http 源（spec §10.2/§12.1） ——
  it("https 提示：非 loopback 的 http / 非法源给提示（正例）", async () => {
    for (const src of ["http://mirror.example.com", "http://127.0.0.2:8080", "http://localhost.evil.com", "mirror.example.com"]) {
      const html = await render(({ settings }) => { settings.updateSource = src; });
      expect(html, src + " 应给 https 提示").toContain("必须使用 https");
    }
  });

  it("https 提示：loopback http 源（127.0.0.1 / localhost / ::1，端口无关）与 https 不给提示（负例）", async () => {
    for (const src of ["http://127.0.0.1:8123", "http://localhost:8080/api", "http://[::1]:9000", "https://mirror.example.com", ""]) {
      const html = await render(({ settings }) => { settings.updateSource = src; });
      expect(html, src + " 不应给 https 提示").not.toContain("必须使用 https");
    }
  });

  // —— fix round 1 A：更新源防抖落盘（独立于其它离散控件的「变更即保存」） ——
  it("更新源不在「变更即保存」watch 列表，输入框走 @input 防抖 + @change flush（fix round 1 A）", () => {
    const dialog = source("./SettingsDialog.vue");
    expect(dialog, "设置对话框不得再直接监听 updateSource").not.toContain("updateSource");
    const section = source("./UpdateSection.vue");
    expect(section).toContain('v-model="settings.updateSource"');
    expect(section).toContain('@input="commitSource"');
    expect(section).toContain('@change="commitSource.flush"');
  });

  it("更新源连续输入只落盘一次（SetSettings 计数），change flush 立即落盘（fix round 1 A）", async () => {
    vi.useFakeTimers();
    try {
      const pinia = createPinia();
      setActivePinia(pinia);
      const settings = useSettingsStore();
      backend.saved.length = 0;
      backend.settings = {};
      const commit = createSourceCommitter(() => settings.save());
      // 模拟 v-model 逐键赋值 + @input 防抖：连续 4 个字符
      for (const v of ["h", "ht", "htt", "http://127.0.0.1:8123/api"]) {
        settings.updateSource = v;
        commit();
      }
      expect(commit.pending()).toBe(true);
      expect(backend.saved, "输入过程中不得落盘").toHaveLength(0);
      // 停手 ~500ms：只落盘一次，且是最终值（不是中间态 "htt"）
      await vi.advanceTimersByTimeAsync(500);
      await Promise.resolve();
      expect(backend.saved).toHaveLength(1);
      expect(backend.saved[0].update_source).toBe("http://127.0.0.1:8123/api");
      expect(commit.pending()).toBe(false);

      // 再次输入后立刻失焦/回车（@change → flush）：不等 500ms 也立即落盘
      settings.updateSource = "https://mirror.example.com";
      commit();
      await commit.flush();
      expect(backend.saved).toHaveLength(2);
      expect(backend.saved[1].update_source).toBe("https://mirror.example.com");
      expect(commit.pending()).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});
