// 同步规则的纯展示/表单逻辑：SyncView、SyncCard、SyncConflictsDialog 共用。
// 这里也是本特性唯一被 vitest 覆盖的一层 —— 仓库没有组件测试基建，
// 所以凡是能写成纯函数的一律放这里，组件只做渲染与接线。
//
// 重要：sync.SyncRuleStat 在 Go 侧没有 json tag，运行时键是 **PascalCase**
// （Mode/Pending/AlignTotal/...，定义见 internal/sync/ctrl.go:39-56）；而
// config.SyncRule 有 json tag，是 snake_case。两者不要混。
// 注意：frontend/wailsjs/go/models.ts 里**没有** sync.SyncRuleStat
// （wails v2.15.0 不为 map 的 value 结构体生成 TS 类型），不要照它写。

export const SYNC_STATES = {
  stopped: '已停止',
  connecting: '连接中',
  connected: '已连接',
  reconnecting: '重连中',
  error: '错误',
}

// **镜像** internal/config/store.go:83-85 的 DefaultExcludes()，不是同源。
// UI 新建规则时表单预置这五条：若提交空数组，后端 Normalize 只在 Excludes==nil
// 时兜底，空 slice 获胜，.git/、node_modules/ 等会被同步。
// 后端改默认值时这里必须同步改（见 Global Constraints）。
export const DEFAULT_EXCLUDES = ['.git/', 'node_modules/', '*.swp', '*~', '.DS_Store']

export function stateLabel(state) {
  if (!state) return '未知'
  return SYNC_STATES[state] || state
}

export function dotClass(state) {
  if (state === 'connected') return 'on'
  if (state === 'connecting' || state === 'reconnecting') return 'warn'
  if (state === 'error') return 'err'
  return 'off'
}

export function kindLabel(kind) {
  return kind === 'file' ? '单文件' : '目录'
}

// 决策 15：单文件规则的 mirror_delete 无效（源消失一律不删本地）。
export function canMirrorDelete(kind) {
  return kind !== 'file'
}

export function mirrorDeleteHint(kind) {
  if (canMirrorDelete(kind)) return ''
  return '单文件规则下 mirror_delete 无效：源消失一律不删本地'
}

function intOf(v) {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

// 探测徽章：实时（inotify）或轮询 Ns；降级时黄色并把后端 Reason 作为悬浮说明。
// S8：Mode 尚未由后端确定时（启动瞬间的空串、缺 stats、未知值）返回中性「探测中」，
// 绝不能默认按轮询渲染 —— 那会在探测完成前就谎报「轮询 Ns / 未使用 inotify」。
export function probeBadge(stats) {
  const s = stats || {}
  if (s.Mode === 'inotify') {
    return { text: '实时', level: 'ok', title: 'inotify 实时探测' }
  }
  if (s.Mode !== 'poll') {
    return { text: '探测中', level: '', title: '探测方式尚未确定' }
  }
  const secs = intOf(s.PollIntervalS) > 0 ? intOf(s.PollIntervalS) : 5
  return {
    text: '轮询 ' + secs + 's',
    level: 'warn',
    title: s.Reason || '未使用 inotify，已降级为轮询',
  }
}

export function countsOf(stats) {
  const s = stats || {}
  return [
    { key: 'pending', label: '待处理', value: intOf(s.Pending) },
    { key: 'done', label: '已完成', value: intOf(s.Done) },
    { key: 'failed', label: '失败', value: intOf(s.Failed) },
    { key: 'conflicts', label: '冲突', value: intOf(s.Conflicts) },
  ]
}

export function alignText(stats) {
  const s = stats || {}
  const total = intOf(s.AlignTotal)
  const scanned = intOf(s.AlignScanned)
  if (total <= 0 || scanned >= total) return ''
  return '对齐 ' + scanned + '/' + total
}

export function currentFileName(stats) {
  const s = stats || {}
  return s.CurrentFile || ''
}

export function statsOf(all, id) {
  if (!all || !id) return {}
  return all[id] || {}
}

// S6：单条规则的展示名回退顺序统一为 name → remote_path → host。
// SyncCard、SyncView 的确认/日志文案、冲突对话框标题都必须用它，不能各写一套。
export function ruleLabel(rule) {
  const r = rule || {}
  return r.name || r.remote_path || r.host || ''
}

// 待确认删除：只有真挂起时才有指纹，UI 必须把指纹原样回传给确认绑定。
// FIX 2c：还必须要求闸门确实以「数量阈值」臂挂起（DeleteNeedsConfirm=true）。
// 硬拒绝臂（镜像关闭/根消失/扫描不完整/溢出/首轮/远端疑似清空）即使留下指纹，
// 也不得展示可点的「确认删除」——否则 UI 会提供一个后端拒绝的确认入口。
export function deleteSummary(stats) {
  const s = stats || {}
  const count = intOf(s.DeletePending)
  if (count <= 0 || !s.DeleteFingerprint || s.DeleteNeedsConfirm !== true) return null
  return {
    count,
    fingerprint: s.DeleteFingerprint,
    paths: Array.isArray(s.DeletePaths) ? s.DeletePaths : [],
  }
}

export function retryable(stats) {
  return intOf((stats || {}).Failed) > 0
}

// FIX 5：三个动作字符串是**跨语言契约**，必须与 internal/sync/conflict.go:10-12 的
// ConflictKeepLocal/ConflictTakeRemote/ConflictSaveAs 逐字一致。放在被测模块里，
// 组件不再各自硬编码，拼错会在 vitest 而不是运行时暴露。
export const CONFLICT_KEEP_LOCAL = 'keep_local'
export const CONFLICT_TAKE_REMOTE = 'take_remote'
export const CONFLICT_SAVE_AS = 'save_as'
export const CONFLICT_ACTIONS = [CONFLICT_KEEP_LOCAL, CONFLICT_TAKE_REMOTE, CONFLICT_SAVE_AS]
// BATCH_LIMIT 是纯前端约束（后端没有这个上限），从组件搬进被测层。
export const BATCH_LIMIT = 50

// FIX 1a：对话框内的错误文案。空值归一为空串（不渲染），其余转成可读字符串。
export function dialogErrorText(err) {
  if (err == null) return ''
  return String(err).trim()
}

// FIX 1d：批量按钮可用性 —— 有冲突、不超上限、且当前没有批量在途（busy 守卫）。
export function batchResolvable(conflictCount, busy) {
  const n = intOf(conflictCount)
  return !busy && n > 0 && n <= BATCH_LIMIT
}

// FIX 3：激活竞态守卫 —— 只有视图处于激活态且尚未注册订阅时才注册。
export function shouldSubscribe(active, alreadySubscribed) {
  return !!active && !alreadySubscribed
}

export function conflictActionLabel(action) {
  if (action === CONFLICT_KEEP_LOCAL) return '保留本地'
  if (action === CONFLICT_TAKE_REMOTE) return '用远端覆盖'
  if (action === CONFLICT_SAVE_AS) return '另存远端版本'
  return action
}

// 排除项在表单里是多行文本；提交时去空行、去首尾空白、保序去重。
export function parseExcludes(text) {
  const out = []
  const parts = String(text == null ? '' : text).split('\n')
  for (const raw of parts) {
    const s = raw.trim()
    if (s && out.indexOf(s) === -1) out.push(s)
  }
  return out
}

export function formatExcludes(list) {
  if (!Array.isArray(list)) return ''
  return list.join('\n')
}

// excludesText 预置 DEFAULT_EXCLUDES：这是**镜像** internal/config/store.go:83-85，
// 不是「配合 Normalize」——后端只在 Excludes == nil 时才填默认值，UI 提交空数组
// （非 nil）会让默认排除项全部丢失（M1）。两份列表必须同步修改。
//
// auto_reconnect 的纯函数兜底是 true；真正的默认值来自全局 App.AutoReconnectDefault，
// 由 SyncView 在创建时用 settings store 的值覆盖（S3）。这里保持纯函数、无 import。
export function newSyncRuleForm() {
  return {
    id: '',
    name: '',
    host: '',
    user: '',
    kind: 'dir',
    remote_path: '',
    local_path: '',
    max_depth: 0,
    excludesText: DEFAULT_EXCLUDES.join('\n'),
    mirror_delete: false,
    force_poll: false,
    poll_interval_s: 5,
    auto_reconnect: true,
    enabled: false,
  }
}

export function syncRuleToForm(rule) {
  const f = newSyncRuleForm()
  if (!rule) return f
  f.id = rule.id || ''
  f.name = rule.name || ''
  f.host = rule.host || ''
  f.user = rule.user || ''
  f.kind = rule.kind === 'file' ? 'file' : 'dir'
  f.remote_path = rule.remote_path || ''
  f.local_path = rule.local_path || ''
  f.max_depth = Number.isFinite(Number(rule.max_depth)) ? Number(rule.max_depth) : 0
  // FIX 4：excludes 缺失（null/undefined）时必须回退到表单预置的默认排除项。
  // 若照旧写成空串，提交空数组后后端只在 Excludes==nil 时填默认值，空 slice 获胜，
  // .git/、node_modules/ 会被同步（M1 防线）。显式空数组仍尊重用户选择。
  f.excludesText = Array.isArray(rule.excludes) ? formatExcludes(rule.excludes) : f.excludesText
  f.mirror_delete = !!rule.mirror_delete
  f.force_poll = !!rule.force_poll
  f.poll_interval_s = Number(rule.poll_interval_s) > 0 ? Number(rule.poll_interval_s) : 5
  // auto_reconnect 是 *bool：缺失（undefined）按 true（对齐后端 Reconnect() 的 nil
  // 兜底）；真正的全局默认由后端 normalize() 从 App.AutoReconnectDefault 灌入，
  // UI 这条只是防御性回退（S3）。
  f.auto_reconnect = rule.auto_reconnect !== false
  f.enabled = !!rule.enabled
  return f
}

export function formToSyncRule(form) {
  const f = form || {}
  return {
    id: f.id || '',
    name: f.name || '',
    host: f.host || '',
    user: f.user || '',
    kind: f.kind === 'file' ? 'file' : 'dir',
    remote_path: String(f.remote_path == null ? '' : f.remote_path).trim(),
    local_path: String(f.local_path == null ? '' : f.local_path).trim(),
    max_depth: Number(f.max_depth) || 0,
    excludes: parseExcludes(f.excludesText),
    // 单文件规则的 mirror_delete 一律落 false：后端会拒，UI 也不该提交它。
    mirror_delete: canMirrorDelete(f.kind) ? !!f.mirror_delete : false,
    force_poll: !!f.force_poll,
    poll_interval_s: Number(f.poll_interval_s) || 5,
    auto_reconnect: !!f.auto_reconnect,
    enabled: !!f.enabled,
  }
}

// 只做前端能判的部分，**不声称与后端全部硬约束对齐**：host 正则
// （forward.ValidateHost）与控制字符检查故意省略，仍由后端 ValidateSyncRule 强制。
// 这里只为拦截明显无效输入、避免无谓往返（S11）。
export function validateSyncRuleForm(form) {
  const f = form || {}
  if (!f.host) return '请选择主机'
  if (f.kind !== 'dir' && f.kind !== 'file') return 'kind 必须是 dir 或 file'
  const remote = String(f.remote_path == null ? '' : f.remote_path).trim()
  if (!remote) return '远端路径不能为空'
  if (remote[0] !== '/' && remote[0] !== '~') return '远端路径必须是绝对路径或以 ~ 开头'
  if (!String(f.local_path == null ? '' : f.local_path).trim()) return '本地路径不能为空'
  const depth = Number(f.max_depth)
  if (!(depth === -1 || (depth >= 0 && depth <= 64))) return 'max_depth 必须是 -1 或 0..64'
  const pi = Number(f.poll_interval_s)
  if (!(pi >= 1 && pi <= 3600)) return 'poll_interval_s 必须在 1..3600'
  return ''
}

export function shouldRefresh(sourceType, sourceTypes) {
  return (sourceTypes || []).indexOf(sourceType) !== -1
}
