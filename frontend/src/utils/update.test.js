import { describe, it, expect } from "vitest";
import { actions, stateLabel, statusMessage, progressPercent, formatBytes } from "./update";
import { STATES, progressText } from "./update";

const base = { state: "idle", current: "v0.6.0", latest: "", hint: "", pending_log: "", skipped: false, progress: 0, manual: false };

describe("actions", () => {
  it("available 才允许下载与跳过", () => {
    const a = actions({ ...base, state: "available", latest: "v0.7.0" });
    expect(a.download).toBe(true);
    expect(a.skip).toBe(true);
    expect(a.apply).toBe(false);
    expect(a.cancel).toBe(false);
  });

  it("verify-failed 与 io-failed 都能重试下载（补齐状态机缺边）", () => {
    for (const state of ["verify-failed", "io-failed"]) {
      const a = actions({ ...base, state, latest: "v0.7.0" });
      expect(a.download, state).toBe(true);
    }
  });

  it("verify-failed / io-failed 可丢弃残留（I-2：否则永远无法处理），available 不需要", () => {
    for (const state of ["verify-failed", "io-failed"]) {
      expect(actions({ ...base, state, latest: "v0.7.0" }).discard, state).toBe(true);
    }
    expect(actions({ ...base, state: "available", latest: "v0.7.0" }).discard).toBe(false);
  });

  it("downloading 只给取消", () => {
    const a = actions({ ...base, state: "downloading" });
    expect(a.cancel).toBe(true);
    expect(Object.entries(a).filter(([k, v]) => v && k !== "cancel" && k !== "openPage")).toHaveLength(0);
  });

  it("ready 才出现重启并升级，且可删除已下载", () => {
    const a = actions({ ...base, state: "ready" });
    expect(a.apply).toBe(true);
    expect(a.discard).toBe(true);
    expect(a.download).toBe(false);
  });

  it("disabled / skipped 不允许下载，skipped 允许取消跳过", () => {
    expect(actions({ ...base, state: "disabled" }).download).toBe(false);
    const s = actions({ ...base, state: "skipped", latest: "v0.7.0", skipped: true });
    expect(s.download).toBe(false);
    expect(s.clearSkip).toBe(true);
  });

  it("applying 时全部禁用", () => {
    const a = actions({ ...base, state: "applying" });
    expect(Object.values(a).every((v) => v === false)).toBe(true);
  });
});

describe("labels", () => {
  it("非 release 构建说明无法判定是否最新", () => {
    const info = { ...base, current: "v0.6.0-80-gc2d2a36", state: "available", latest: "v0.7.0" };
    expect(stateLabel(info)).toContain("本地构建");
    expect(statusMessage(info)).toContain("无法判定");
  });

  it("io-failed + hint 给人工升级指引", () => {
    const info = { ...base, state: "io-failed", hint: "manual-upgrade" };
    expect(statusMessage(info)).toContain("手动");
  });

  it("pending_log 非空时提示上次升级未完成", () => {
    const info = { ...base, state: "idle", pending_log: "/opt/sshore/sshore-update.log" };
    expect(statusMessage(info)).toContain("上次升级未完成");
  });
});

describe("progress", () => {
  it("未知总大小返回 null（UI 用 indeterminate）", () => {
    expect(progressPercent({ ...base, state: "downloading", progress: -1 })).toBeNull();
  });
  it("已知进度返回 0-100", () => {
    expect(progressPercent({ ...base, state: "downloading", progress: 42 })).toBe(42);
  });
  it("formatBytes 走二进制单位", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
  });
});

// ── 以下为 Task 13 裁定的补齐：15 个状态逐个穷举断言（brief 表逐字保留在上方）────

const ALL_STATES = [
  "idle", "checking", "up-to-date", "available", "skipped", "rate-limited",
  "check-failed", "no-asset", "no-checksum", "downloading", "verify-failed",
  "io-failed", "ready", "applying", "disabled",
];

const NONE = {
  check: false, download: false, cancel: false, apply: false,
  discard: false, skip: false, clearSkip: false, openPage: false,
};

describe("STATES 穷举", () => {
  it("常量表无重复且恰好覆盖状态机的 15 个状态", () => {
    expect(new Set(STATES).size).toBe(STATES.length);
    expect([...STATES].sort()).toEqual([...ALL_STATES].sort());
  });

  it("每个状态都有非空文案（不抛异常、不落空）", () => {
    for (const state of ALL_STATES) {
      const msg = statusMessage({ ...base, state });
      expect(typeof msg, state).toBe("string");
      expect(msg.length, state).toBeGreaterThan(0);
    }
  });
});

describe("statusMessage 全枚举", () => {
  const cases = [
    ["idle", {}, "尚未检查更新"],
    ["checking", {}, "正在检查更新…"],
    ["up-to-date", {}, "已是最新版本"],
    ["disabled", {}, "已关闭自动检查（可手动检查）"],
    ["skipped", { latest: "v0.7.0" }, "已跳过 v0.7.0"],
    ["rate-limited", {}, "更新源限流，请稍后再试或手动下载"],
    ["check-failed", {}, "检查失败"],
    ["check-failed", { error: "连接超时" }, "检查失败：连接超时"],
    ["no-asset", {}, "发现新版本，但本平台暂无可用包"],
    ["no-checksum", {}, "该版本未提供校验文件，已拒绝自动升级"],
    ["available", { latest: "v0.7.0" }, "发现新版本 v0.7.0"],
    ["downloading", {}, "正在下载并校验…"],
    ["verify-failed", {}, "下载内容校验失败，已丢弃，可重试"],
    ["io-failed", {}, "写入失败，可重试"],
    ["io-failed", { hint: "manual-upgrade" }, "安装目录不可写，请按手动步骤升级"],
    ["ready", { latest: "v0.7.0" }, "v0.7.0 已就绪，点击「重启并升级」生效"],
    ["applying", {}, "正在重启…"],
  ];

  it("逐状态给出与 brief 逐字一致的文案", () => {
    for (const [state, over, expected] of cases) {
      expect(statusMessage({ ...base, state, ...over }), state).toBe(expected);
    }
  });

  it("skipped 缺 latest 时退化为「该版本」", () => {
    expect(statusMessage({ ...base, state: "skipped" })).toBe("已跳过 该版本");
  });

  it("ready 缺 latest 时退化为「新版本」", () => {
    expect(statusMessage({ ...base, state: "ready" })).toBe("新版本 已就绪，点击「重启并升级」生效");
  });

  it("pending_log 覆盖任何状态（上次升级未完成）", () => {
    for (const state of ALL_STATES) {
      const msg = statusMessage({ ...base, state, pending_log: "/opt/sshore/sshore-update.log" });
      expect(msg, state).toContain("上次升级未完成");
    }
  });
});

describe("stateLabel", () => {
  it("release 版本显示「当前版本」", () => {
    expect(stateLabel({ ...base, current: "v0.6.0" })).toBe("当前版本 v0.6.0");
  });

  it("describe 串 / dev / 空 current 都判定为本地构建", () => {
    expect(stateLabel({ ...base, current: "v0.6.0-80-gc2d2a36" })).toBe("本地构建 v0.6.0-80-gc2d2a36（dev）");
    expect(stateLabel({ ...base, current: "dev" })).toBe("本地构建 dev（dev）");
    expect(stateLabel({ ...base, current: "" })).toBe("本地构建 unknown（dev）");
  });
});

describe("按钮矩阵全枚举（spec §12.1）", () => {
  it("idle / checking / up-to-date / rate-limited / check-failed / no-asset / no-checksum → 检查 + 发布页", () => {
    for (const state of ["idle", "checking", "up-to-date", "rate-limited", "check-failed", "no-asset", "no-checksum"]) {
      expect(actions({ ...base, state }), state).toEqual({ ...NONE, check: true, openPage: true });
    }
  });

  it("available → 下载 + 跳过 + 检查 + 发布页", () => {
    expect(actions({ ...base, state: "available", latest: "v0.7.0" }))
      .toEqual({ ...NONE, check: true, download: true, skip: true, openPage: true });
  });

  it("skipped → 取消跳过 + 检查 + 发布页", () => {
    expect(actions({ ...base, state: "skipped", latest: "v0.7.0", skipped: true }))
      .toEqual({ ...NONE, check: true, clearSkip: true, openPage: true });
  });

  it("downloading → 只有取消下载", () => {
    expect(actions({ ...base, state: "downloading" })).toEqual({ ...NONE, cancel: true });
  });

  it("verify-failed / io-failed → 重试下载 + 丢弃残留 + 检查 + 发布页", () => {
    for (const state of ["verify-failed", "io-failed"]) {
      expect(actions({ ...base, state, latest: "v0.7.0" }), state)
        .toEqual({ ...NONE, check: true, download: true, discard: true, openPage: true });
    }
  });

  it("ready → 重启并升级 + 删除已下载 + 发布页", () => {
    expect(actions({ ...base, state: "ready", latest: "v0.7.0" }))
      .toEqual({ ...NONE, apply: true, discard: true, openPage: true });
  });

  it("applying → 全部按钮禁用", () => {
    expect(actions({ ...base, state: "applying" })).toEqual(NONE);
  });
});

describe("disabled（非 release 构建 / auto=false）", () => {
  it("不允许任何更新动作，但保留手动检查与发布页（spec §12.1：检查更新（手动仍可用）· 打开发布页）", () => {
    const a = actions({ ...base, state: "disabled", latest: "v0.7.0" });
    for (const key of ["download", "apply", "cancel", "discard", "skip", "clearSkip"]) {
      expect(a[key], key).toBe(false);
    }
    expect(a.check).toBe(true);
    expect(a.openPage).toBe(true);
  });
});

describe("progressPercent 边界", () => {
  it("钳制到 0-100", () => {
    expect(progressPercent({ ...base, progress: 100 })).toBe(100);
    expect(progressPercent({ ...base, progress: 150 })).toBe(100);
    expect(progressPercent({ ...base, progress: -5 })).toBeNull();
    expect(progressPercent({ ...base, progress: null })).toBeNull();
    expect(progressPercent({ ...base })).toBe(0);
  });

  it("有 done/total 时按字节实时计算", () => {
    expect(progressPercent({ ...base, done: 1024, total: 4096, progress: 0 })).toBe(25);
  });
});

describe("progress 文案（已知总长 vs 未知总长）", () => {
  it("已知总长给出「已下载 / 总字节」", () => {
    const info = { ...base, state: "downloading", done: 1024, total: 4096, progress: 25 };
    expect(progressPercent(info)).toBe(25);
    expect(progressText(info)).toBe("1.0 KB / 4.0 KB");
  });

  it("total<=0 视为未知总长：indeterminate（null）且只报已下载字节", () => {
    const info = { ...base, state: "downloading", done: 2048, total: 0, progress: -1 };
    expect(progressPercent(info)).toBeNull();
    expect(progressText(info)).toBe("已下载 2.0 KB");
  });

  it("缺少 done/total 时退回后端快照的 percent", () => {
    const info = { ...base, state: "downloading", progress: 42 };
    expect(progressPercent(info)).toBe(42);
    expect(progressText(info)).toBe("42%");
  });

  it("没有可用进度时返回空串", () => {
    expect(progressText({ ...base, state: "downloading", progress: -1 })).toBe("");
    expect(progressText(null)).toBe("");
  });
});

describe("formatBytes 边界", () => {
  it("二进制单位逐级换算", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(null)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(1024 * 1024 * 1024)).toBe("1.0 GB");
  });
});
