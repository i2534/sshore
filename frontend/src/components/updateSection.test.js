// UpdateSection.vue 的**真实组件渲染**测试（SSR）：沿用仓库无 jsdom / 无 @vue/test-utils 的约定。
// 这里不需要 mock Wails 绑定：SSR 不执行 onMounted，动作也不会被调用，断言的是模板对
// store 状态 + utils/update.js 纯函数的绑定（改模板或改纯函数都会让用例变红）。
import { describe, it, expect } from "vitest";
import { createSSRApp } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createPinia, setActivePinia } from "pinia";
import UpdateSection from "./UpdateSection.vue";
import { useUpdateStore, emptyInfo } from "../stores/update";
import { useSettingsStore } from "../stores/settings";

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
});
