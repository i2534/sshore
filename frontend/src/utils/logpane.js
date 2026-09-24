// 日志区高度的唯一来源与夹取。仅会话内记忆，不落设置文件（不碰 Go 配置结构）。
export const LOG_MIN_H = 80
export const LOG_MAX_H = 420
export const LOG_DEFAULT_H = 140

export function clampLogHeight(v, { min = LOG_MIN_H, max = LOG_MAX_H } = {}) {
  const n = Number(v)
  if (!Number.isFinite(n)) return LOG_DEFAULT_H
  return Math.min(max, Math.max(min, Math.round(n)))
}
