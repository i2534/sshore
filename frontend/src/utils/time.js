// 日志时间显示格式:yyyy-MM-dd HH:mm:ss(本地时区)。
// 输入为 RFC3339(Go 端 time.Now().Format)或 ISO(前端 new Date().toISOString())。
export function fmtLogTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(ts)
  const p = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
