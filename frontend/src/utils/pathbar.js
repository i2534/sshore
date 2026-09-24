// 路径栏（可编辑）输入的纯解析：不访问文件系统、不触碰 DOM，便于单测。
// 语义：
//   本地 —— 引号/空白归一、裸盘符补分隔符（复用 localpath.normalize）；不做 ~ 展开。
//   远程 —— '~' 是「主目录」sentinel（与 Go 侧 preset.RemoteHomeToken 字面量配对）；
//           未连接时返回 connect，由 SftpView 走既有的 pendingPath + connect() 分支。
import { normalize } from './localpath'

// 远程「主目录」预设的 sentinel：必须与 Go 侧 preset.RemoteHomeToken 字面量一致。
export const REMOTE_HOME = '~'

export function resolvePathInput({ pane, raw, hasHost, connected, current }) {
  let p = String(raw == null ? '' : raw).trim()
  // 兼容 Windows 资源管理器「复制为路径」的 "C:\Program Files" 形态
  if (p.length > 1 && p.startsWith('"') && p.endsWith('"')) p = p.slice(1, -1).trim()
  if (!p) return { kind: 'noop', reason: 'empty' }

  if (pane === 'local') {
    if (p === REMOTE_HOME) return { kind: 'noop', reason: 'local-home' }
    const path = normalize(p)
    if (path === current) return { kind: 'noop', reason: 'same' }
    return { kind: 'navigate', path }
  }

  if (!hasHost) return { kind: 'noop', reason: 'no-host' }
  if (p === REMOTE_HOME) return { kind: 'home' }
  // 未连接时先"去连"再比 same：否则在远程面板输入当前显示的路径（未连接时一般是 '/'）
  // 会被 same 短路，用户看到的是"回车没反应"。
  if (!connected) return { kind: 'connect', path: p }
  if (p === current) return { kind: 'noop', reason: 'same' }
  return { kind: 'navigate', path: p }
}

// 路径栏回车提交前的 IME 守卫：返回 true 表示这是「输入法确认候选」的 Enter，不得提交跳转。
// 为什么必须挂在 keydown 而不是 keyup：按规范 isComposing 只在 compositionstart→compositionend
// 之间为真，而 WebKit/Firefox/Safari 会在 compositionend 之后仍发 keyup（此时 isComposing=false，
// stum.de「Handling IME events in JavaScript」），本项目 Linux/WebKitGTK 目标上 keyup 版守卫会漏判。
// keydown 期间组合态仍是 true；keyCode === 229 是部分浏览器组合态 keydown 的哨兵，作为兜底。
export function isImeCommit(ev) {
  if (!ev) return false
  return ev.isComposing === true || ev.keyCode === 229
}
