import { describe, it, expect } from 'vitest'
import {
  DEFAULT_EXCLUDES, stateLabel, dotClass, kindLabel, canMirrorDelete, mirrorDeleteHint,
  probeBadge, countsOf, alignText, currentFileName, statsOf, deleteSummary, retryable,
  conflictActionLabel, parseExcludes, formatExcludes, ruleLabel,
  newSyncRuleForm, syncRuleToForm, formToSyncRule, validateSyncRuleForm, shouldRefresh,
  CONFLICT_KEEP_LOCAL, CONFLICT_TAKE_REMOTE, CONFLICT_SAVE_AS, CONFLICT_ACTIONS, BATCH_LIMIT,
  dialogErrorText, batchResolvable, shouldSubscribe,
} from './sync'

// 注意：SyncRuleStat 在 Go 侧没有 json tag，故运行时键是 PascalCase。
const stat = {
  Mode: 'poll', Reason: '远端无 inotifywait', PollIntervalS: 3,
  Pending: 4, Done: 10, Failed: 2, Conflicts: 1,
  AlignScanned: 3, AlignTotal: 120, CurrentFile: 'a/b.txt',
  SourceMissing: true, DeletePending: 2, DeleteFingerprint: 'fp-1',
  DeletePaths: ['x.txt', 'y.txt'], DeleteNeedsConfirm: true,
}

describe('sync 展示辅助', () => {
  it('状态映射为中文，未知回退原值', () => {
    expect(stateLabel('connected')).toBe('已连接')
    expect(stateLabel('reconnecting')).toBe('重连中')
    expect(stateLabel('bogus')).toBe('bogus')
    expect(stateLabel('')).toBe('未知')
  })

  it('状态决定圆点样式', () => {
    expect(dotClass('connected')).toBe('on')
    expect(dotClass('connecting')).toBe('warn')
    expect(dotClass('reconnecting')).toBe('warn')
    expect(dotClass('error')).toBe('err')
    expect(dotClass('stopped')).toBe('off')
  })

  it('kind 标签与 mirror_delete 可用性（决策 15）', () => {
    expect(kindLabel('dir')).toBe('目录')
    expect(kindLabel('file')).toBe('单文件')
    expect(canMirrorDelete('dir')).toBe(true)
    expect(canMirrorDelete('file')).toBe(false)
    expect(mirrorDeleteHint('dir')).toBe('')
    expect(mirrorDeleteHint('file')).toContain('无效')
  })

  it('探测徽章：inotify 正常、poll 降级且带 Reason', () => {
    expect(probeBadge({ Mode: 'inotify' })).toEqual({ text: '实时', level: 'ok', title: 'inotify 实时探测' })
    const b = probeBadge(stat)
    expect(b.text).toBe('轮询 3s')
    expect(b.level).toBe('warn')
    expect(b.title).toBe('远端无 inotifywait')
  })

  it('poll_interval_s 缺省回退 5s', () => {
    expect(probeBadge({ Mode: 'poll' }).text).toBe('轮询 5s')
  })

  it('S8：Mode 未定（空串/缺 stats/未知值）时中性徽章，不谎报轮询', () => {
    expect(probeBadge({ Mode: '' })).toEqual({ text: '探测中', level: '', title: '探测方式尚未确定' })
    expect(probeBadge(undefined)).toEqual({ text: '探测中', level: '', title: '探测方式尚未确定' })
    expect(probeBadge({ Mode: 'weird' }).text).toBe('探测中')
  })

  it('四组计数读取 PascalCase 键', () => {
    const c = countsOf(stat)
    expect(c.map(x => x.value)).toEqual([4, 10, 2, 1])
    expect(countsOf(undefined).map(x => x.value)).toEqual([0, 0, 0, 0])
  })

  it('对齐进度只在进行中显示', () => {
    expect(alignText(stat)).toBe('对齐 3/120')
    expect(alignText({ AlignScanned: 120, AlignTotal: 120 })).toBe('')
    expect(alignText({ AlignTotal: 0 })).toBe('')
    expect(currentFileName(stat)).toBe('a/b.txt')
  })

  it('statsOf 容忍缺失', () => {
    expect(statsOf({ r1: stat }, 'r1')).toBe(stat)
    expect(statsOf({}, 'r1')).toEqual({})
    expect(statsOf(undefined, 'r1')).toEqual({})
  })

  it('待确认删除摘要：挂起 + 指纹 + 闸门可确认三者齐全才显示', () => {
    expect(deleteSummary(stat)).toEqual({ count: 2, fingerprint: 'fp-1', paths: ['x.txt', 'y.txt'] })
    // FIX 2c：只有阈值臂挂起（DeleteNeedsConfirm=true）才可确认，硬拒绝臂必须为 null。
    expect(deleteSummary({ ...stat, DeleteNeedsConfirm: false })).toBe(null)
    expect(deleteSummary({ ...stat, DeleteNeedsConfirm: undefined })).toBe(null)
    expect(deleteSummary({ DeletePending: 0 })).toBe(null)
    expect(deleteSummary(undefined)).toBe(null)
  })

  it('可重试判断看 Failed 计数', () => {
    expect(retryable(stat)).toBe(true)
    expect(retryable({ Failed: 0 })).toBe(false)
    expect(retryable(undefined)).toBe(false)
  })

  it('冲突动作映射中文', () => {
    expect(conflictActionLabel('keep_local')).toBe('保留本地')
    expect(conflictActionLabel('take_remote')).toBe('用远端覆盖')
    expect(conflictActionLabel('save_as')).toBe('另存远端版本')
    expect(conflictActionLabel('weird')).toBe('weird')
  })

  it('S6：显示名回退 name → remote_path → host', () => {
    expect(ruleLabel({ name: 'conf', remote_path: '/r', host: 'h' })).toBe('conf')
    expect(ruleLabel({ name: '', remote_path: '/r', host: 'h' })).toBe('/r')
    expect(ruleLabel({ name: '', remote_path: '', host: 'h' })).toBe('h')
    expect(ruleLabel(undefined)).toBe('')
  })
})

describe('排除项与表单', () => {
  it('排除项文本解析：去空行/去首尾空白/保序去重', () => {
    expect(parseExcludes('  .git/ \n\nnode_modules/\n.git/\n')).toEqual(['.git/', 'node_modules/'])
    expect(parseExcludes('')).toEqual([])
    expect(parseExcludes(null)).toEqual([])
    expect(formatExcludes(['a', 'b'])).toBe('a\nb')
    expect(formatExcludes(null)).toBe('')
  })

  it('M1：表单默认值镜像 store.go:83-85 的 DefaultExcludes（五条全在，逐行预置）', () => {
    const f = newSyncRuleForm()
    expect(f.kind).toBe('dir')
    expect(f.max_depth).toBe(0)
    expect(f.poll_interval_s).toBe(5)
    expect(f.auto_reconnect).toBe(true) // 纯函数兜底；全局默认由 SyncView 创建时注入（S3）
    expect(f.mirror_delete).toBe(false)
    expect(f.enabled).toBe(false)
    // 关键：表单必须预置五条默认排除项。若提交空数组，后端 Normalize 只在
    // Excludes==nil 时兜底，空 slice 获胜，.git/、node_modules/ 会被同步（M1）。
    expect(f.excludesText).toBe('.git/\nnode_modules/\n*.swp\n*~\n.DS_Store')
    expect(parseExcludes(f.excludesText)).toEqual(['.git/', 'node_modules/', '*.swp', '*~', '.DS_Store'])
    expect(DEFAULT_EXCLUDES).toEqual(['.git/', 'node_modules/', '*.swp', '*~', '.DS_Store'])
  })

  it('规则 <-> 表单往返不丢字段', () => {
    const rule = {
      id: 'r1', name: 'conf', host: 'prod-01', user: 'alice', kind: 'dir',
      remote_path: '/srv/conf', local_path: '/tmp/conf', max_depth: 2,
      excludes: ['.git/'], mirror_delete: true, force_poll: true,
      poll_interval_s: 7, auto_reconnect: false, enabled: true,
    }
    const f = syncRuleToForm(rule)
    expect(f.excludesText).toBe('.git/')
    expect(f.auto_reconnect).toBe(false)
    expect(formToSyncRule(f)).toEqual(rule)
  })

  it('缺 auto_reconnect（undefined）回退 true（真正的全局默认由视图注入，不再声称与后端缺键默认一致）', () => {
    expect(syncRuleToForm({ id: 'x' }).auto_reconnect).toBe(true)
  })

  it('单文件规则提交时强制 mirror_delete=false', () => {
    const f = { ...newSyncRuleForm(), kind: 'file', mirror_delete: true }
    expect(formToSyncRule(f).mirror_delete).toBe(false)
  })

  it('前端校验覆盖明显错误（host 正则与控制字符仍由后端强制，不声称全部对齐）', () => {
    const ok = { ...newSyncRuleForm(), host: 'h', remote_path: '/r', local_path: '/l' }
    expect(validateSyncRuleForm(ok)).toBe('')
    expect(validateSyncRuleForm({ ...ok, host: '' })).toBe('请选择主机')
    expect(validateSyncRuleForm({ ...ok, remote_path: '' })).toBe('远端路径不能为空')
    expect(validateSyncRuleForm({ ...ok, remote_path: 'rel/path' })).toContain('绝对路径')
    expect(validateSyncRuleForm({ ...ok, remote_path: '~/x' })).toBe('')
    expect(validateSyncRuleForm({ ...ok, local_path: '  ' })).toBe('本地路径不能为空')
    expect(validateSyncRuleForm({ ...ok, max_depth: 999 })).toContain('max_depth')
    expect(validateSyncRuleForm({ ...ok, max_depth: -1 })).toBe('')
    expect(validateSyncRuleForm({ ...ok, poll_interval_s: 0 })).toContain('poll_interval_s')
  })

  it('实时刷新只认配置的来源类型', () => {
    expect(shouldRefresh('sync', ['sync', 'system'])).toBe(true)
    expect(shouldRefresh('system', ['sync', 'system'])).toBe(true)
    expect(shouldRefresh('sftp', ['sync', 'system'])).toBe(false)
  })

  it('FIX 4：规则缺 excludes（null/undefined）时不得清空默认排除项（M1 防线）', () => {
    // 后端 Normalize 只在 Excludes==nil 时填默认值；UI 提交空数组会让 .git/、node_modules/
    // 被同步，因此缺省时必须回退到与表单预置一致的默认排除项。
    expect(syncRuleToForm({ id: 'x' }).excludesText).toBe(DEFAULT_EXCLUDES.join('\n'))
    expect(syncRuleToForm({ id: 'x', excludes: null }).excludesText).toBe(DEFAULT_EXCLUDES.join('\n'))
    // 显式空数组仍尊重用户选择（与后端 Excludes!=nil 的语义一致），不得被默认值覆盖。
    expect(syncRuleToForm({ id: 'x', excludes: [] }).excludesText).toBe('')
    expect(syncRuleToForm({ id: 'x', excludes: ['.git/'] }).excludesText).toBe('.git/')
  })
})

describe('冲突对话框跨语言契约与交互守卫', () => {
  it('FIX 5：三个引擎动作字符串与 internal/sync/conflict.go 逐字一致', () => {
    expect(CONFLICT_KEEP_LOCAL).toBe('keep_local')
    expect(CONFLICT_TAKE_REMOTE).toBe('take_remote')
    expect(CONFLICT_SAVE_AS).toBe('save_as')
    expect(CONFLICT_ACTIONS).toEqual(['keep_local', 'take_remote', 'save_as'])
    expect(BATCH_LIMIT).toBe(50)
  })

  it('FIX 1a：对话框错误文案归一，空值不渲染', () => {
    expect(dialogErrorText(null)).toBe('')
    expect(dialogErrorText(undefined)).toBe('')
    expect(dialogErrorText('')).toBe('')
    expect(dialogErrorText('  boom  ')).toBe('boom')
    expect(dialogErrorText(new Error('boom'))).toContain('boom')
  })

  it('FIX 1d：批量按钮决策——无冲突/超上限/执行中一律禁用', () => {
    expect(batchResolvable(3, false)).toBe(true)
    expect(batchResolvable(50, false)).toBe(true)
    expect(batchResolvable(0, false)).toBe(false)
    expect(batchResolvable(51, false)).toBe(false)
    // busy 守卫：批量在途时不得再次触发。
    expect(batchResolvable(3, true)).toBe(false)
    expect(batchResolvable(0, true)).toBe(false)
  })

  it('FIX 3：激活竞态守卫——只有激活且未订阅时才注册', () => {
    expect(shouldSubscribe(true, false)).toBe(true)
    expect(shouldSubscribe(true, true)).toBe(false)
    expect(shouldSubscribe(false, false)).toBe(false)
    expect(shouldSubscribe(false, true)).toBe(false)
  })
})
