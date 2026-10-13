// 右键「复制」的作用集合与文本拼接：纯函数，无 DOM / 无 Vue 依赖，
// 因此能在无 jsdom 的环境里被真单测（Review Focus #1–#4 就靠它兜底）。
import { join as localJoin } from './localpath'

// 远程路径一律 POSIX 语义（与 SftpView.vue 的 posixJoin 同义）。
function posixJoin(base, name) {
  if (base === '/' || base === '') return '/' + name
  return base.replace(/\/+$/, '') + '/' + name
}

/**
 * 本次复制要作用的项名列表（按面板显示顺序）。
 *
 * - `item` 为空或 '..' ⇒ 空数组（面板级/空白右键没有可复制对象）；
 * - 右键那一行在选中集内 ⇒ 整个选中集；否则只取该行（只读，绝不改选中集）；
 * - 不在 visibleNames 里的（被过滤器隐藏的）选中项排到可见项之后 —— 顺序只在
 *   可见项之间有用户可感知的意义，隐藏项不能因此被丢掉。
 */
export function copyTargetNames(selection, item, visibleNames) {
  if (!item || item.name === '..') return []
  const keys = selection.keys
  const names = keys.has(item.name) ? [...keys] : [item.name]
  const order = new Map((visibleNames || []).map((n, i) => [n, i]))
  const hiddenRank = Number.MAX_SAFE_INTEGER
  return names.sort((a, b) => (order.get(a) ?? hiddenRank) - (order.get(b) ?? hiddenRank))
}

/**
 * 复制到剪贴板的文本内容（多行以 `\n` 拼接）。
 *
 * kind: 'copy-name' | 'copy-path'；ctx.pane 决定路径口径：
 * 远程走 POSIX 拼接，本地走 localpath.join —— 后者刻意沿用输入路径自身的
 * 分隔符（`C:\x` → `C:\x\a`，`C:/x` → `C:/x/a`），不强制改写。
 */
export function copyPayload(kind, names, ctx) {
  const list = names || []
  if (kind === 'copy-name') return list.join('\n')
  const isLocal = (ctx || {}).pane === 'local'
  const base = isLocal ? ((ctx || {}).localPath || '/') : (ctx || {}).remotePath
  const join = isLocal ? localJoin : posixJoin
  return list.map((n) => join(base, n)).join('\n')
}
