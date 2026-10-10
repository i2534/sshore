// 远程落点决策的纯函数（规格 §6.4 B5 / §6.5）：connect() 与 syncConnection() 的未连接分支共用，
// 防止"两条路走两个落点"。只做优先级判断，不做 trim、不替调用方兜 '/'（调用方自己有 SftpHome 兜底），
// 因为路径可以合法包含空格（如 Windows 的 D:\工作\虚拟 共享）。
//
// 优先级：1 pendingPath（非空） > 2 pendingHome > 3 记忆路径 > 4 home
// fromMemory 只在采用记忆路径时为 true，供 §6.5 的"记忆路径失效则回落 home"判定使用。

export function resolveRemoteTarget({ pendingPath, pendingHome, remembered, home } = {}) {
  // 空串 = 未指定：工具栏「连接」主流程恒传 pendingPath: ''（F16）。
  if (pendingPath) return { path: pendingPath, fromMemory: false }
  // D15：「主目录」是显式标志，不能再依赖"空 pendingPath = home"的隐含约定。
  if (pendingHome) return { path: home, fromMemory: false }
  if (remembered) return { path: remembered, fromMemory: true }
  return { path: home, fromMemory: false }
}
