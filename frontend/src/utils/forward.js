// 转发模式元信息：把 local/remote/dynamic 的英文缩写映射成清晰的中文说明，
// 供表单下拉、规则卡片等处共用，避免各处重复维护。
export const FORWARD_MODES = {
  local: {
    code: '-L',
    label: '本地转发',
    desc: '监听本机端口，经 SSH 转发到远端目标（把本地流量送入远程主机）',
  },
  remote: {
    code: '-R',
    label: '远程转发',
    desc: '监听远端端口，经 SSH 回连到本机（把远程流量送回本地）',
  },
  dynamic: {
    code: '-D',
    label: '动态 SOCKS',
    desc: '在本机启动 SOCKS 代理，按需把流量转发到任意远端目标',
  },
}

// 取中文标签，未知模式回退为原值（不空白）。
export function modeLabel(mode) {
  const m = FORWARD_MODES[mode]
  return m ? m.label : mode
}

// 取模式一句话说明，未知模式返回空。
export function modeDesc(mode) {
  const m = FORWARD_MODES[mode]
  return m ? m.desc : ''
}
