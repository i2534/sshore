// 深搜浮层的输入判定与文案：纯函数 + 常量，便于单测（浮层本身只在 SSR 下渲染）。
// 为什么必须有这道守卫：后端的空 pattern 语义是「匹配全部」
// （internal/sftp/search.go 的注释、internal/namepattern.Match('')），
// 打开浮层就查询 = 无条件全量扫描远端目录 + 最多 500 条命中——既烧远端 IO，又让结果区变成一屏路径墙。
export const EMPTY_PATTERN_HINT = '输入名称或通配符（支持 * ?）后回车搜索'

export function shouldSearch(pattern) {
  return String(pattern == null ? '' : pattern).trim().length > 0
}
