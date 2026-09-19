// 更新区的展示逻辑全部是纯函数：组件只做绑定，测试不碰 DOM（沿用仓库无 jsdom 的风格）。
export const STATES = ["idle", "checking", "up-to-date", "available", "skipped", "rate-limited",
  "check-failed", "no-asset", "no-checksum", "downloading", "verify-failed", "io-failed",
  "ready", "applying", "disabled"];

const clampPercent = (n) => Math.max(0, Math.min(100, n));

const isLocalBuild = (info) => /-[0-9]+-g[0-9a-f]+$/.test(info.current || "") || !info.current || info.current === "dev";

export function stateLabel(info) {
  if (isLocalBuild(info)) return `本地构建 ${info.current || "unknown"}（dev）`;
  return `当前版本 ${info.current || "unknown"}`;
}

export function statusMessage(info) {
  const st = info.state;
  if (info.pending_log) return "上次升级未完成，可重试升级或查看日志";
  switch (st) {
    case "checking": return "正在检查更新…";
    case "up-to-date": return "已是最新版本";
    case "disabled": return "已关闭自动检查（可手动检查）";
    case "skipped": return `已跳过 ${info.latest || "该版本"}`;
    case "rate-limited": return "更新源限流，请稍后再试或手动下载";
    case "check-failed": return info.error ? `检查失败：${info.error}` : "检查失败";
    case "no-asset": return "发现新版本，但本平台暂无可用包";
    case "no-checksum": return "该版本未提供校验文件，已拒绝自动升级";
    case "available": return isLocalBuild(info)
      ? `发现 ${info.latest}（本地构建无法判定是否最新）`
      : `发现新版本 ${info.latest}`;
    case "downloading": return "正在下载并校验…";
    case "verify-failed": return "下载内容校验失败，已丢弃，可重试";
    case "io-failed": return info.hint === "manual-upgrade" ? "安装目录不可写，请按手动步骤升级" : "写入失败，可重试";
    case "ready": return `${info.latest || "新版本"} 已就绪，点击「重启并升级」生效`;
    case "applying": return "正在重启…";
    default: return "尚未检查更新";
  }
}

export function actions(info) {
  const st = info.state;
  const none = { check: false, download: false, cancel: false, apply: false, discard: false, skip: false, clearSkip: false, openPage: false };
  if (st === "applying") return none;
  const a = { ...none, openPage: true };
  if (st === "downloading") {
    a.cancel = true;
    a.openPage = false;
    return a;
  }
  if (st === "ready") {
    a.apply = true;
    a.discard = true;
    return a;
  }
  if (["available", "verify-failed", "io-failed"].includes(st)) {
    a.download = true;
    a.check = true;
    if (st === "available") a.skip = true;
    return a;
  }
  if (st === "skipped") {
    a.clearSkip = true;
    a.check = true;
    return a;
  }
  a.check = true;
  return a;
}

// 总长未知（total<=0，后端用 progress=-1 表示）返回 null，UI 走 indeterminate；
// 有 done/total 时按字节实时算，否则退回后端快照的 progress 百分比。
export function progressPercent(info) {
  if (!info) return null;
  const { done, total, progress } = info;
  if (total != null) {
    if (!(total > 0)) return null;
    if (done != null && done >= 0) return clampPercent(Math.round((done * 100) / total));
  }
  if (progress == null || progress < 0) return null;
  return clampPercent(progress);
}

// 进度文案：已知总长 → 「已下载 / 总字节」；未知总长 → 只报已下载字节（配 indeterminate）。
export function progressText(info) {
  if (!info) return "";
  const { done, total, progress } = info;
  if (total != null && !(total > 0)) {
    return done != null && done > 0 ? `已下载 ${formatBytes(done)}` : "";
  }
  if (total > 0 && done != null && done >= 0) return `${formatBytes(done)} / ${formatBytes(total)}`;
  if (progress != null && progress >= 0) return `${clampPercent(progress)}%`;
  return "";
}

export function formatBytes(n) {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return i === 0 ? `${v} B` : `${v.toFixed(1)} ${units[i]}`;
}
