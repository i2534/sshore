<script setup>
import { ref, reactive, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { ListHosts, SftpList, SftpGet, SftpGetDir, SftpPut, SftpPutRecursive, SftpRemoveRecursive, SftpMove, SftpRemove, SftpMkdir, SftpRename, SftpConnect, SftpDisconnect, SftpConnected, ListLocal, DeleteLocal, MkdirLocal, RenameLocal, StatLocal, PickLocalFile, CopyLocal, StatPaths, Cwd, SftpHome } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import { useLocationsStore } from '../stores/locations'
import SearchOverlay from '../components/SearchOverlay.vue'
import FilePane from '../components/FilePane.vue'
import TransferQueue from '../components/TransferQueue.vue'
import LogPanel from '../components/LogPanel.vue'
import ContextMenu from '../components/ContextMenu.vue'
import AppDialog from '../components/AppDialog.vue'
import ConflictDialog from '../components/ConflictDialog.vue'
import * as sel from '../utils/selection'
import { actionFor } from '../utils/keys'
import { planTasks, classify, applyPolicy, needsConfirm, summarize, copyName } from '../utils/batch'
import { failureText } from '../utils/queue'
import { payloadFor, parsePayload, hitPane, canDropInto } from '../utils/dnd'
import { EventsOn } from '../../wailsjs/runtime/runtime'
// 说明：StatPaths / CopyLocal / SftpMove / ListLocal / RenameLocal 已在既有 import 行里，
// 这里**不重复声明**（重复 import 同名标识符会让 vite build 直接 SyntaxError）。

const logStore = useLogStore()
const locations = useLocationsStore()
// 浮层 watch 没有 immediate ⇒ visible 初值必须是 false，只由用户动作置 true。
const search = reactive({ visible: false, pane: 'remote' })
const hosts = ref([])
const host = ref('')

// remote pane — start at filesystem root so '..' can navigate the whole tree
const remotePath = ref('/')
const remoteItems = ref([])
const remoteSelection = reactive(sel.createSelection())
const remoteVisible = ref([])
const remoteLoading = ref(false)

// local pane — start at cwd so '..' can navigate up to '/'
const localPath = ref('')
const localItems = ref([])
const localSelection = reactive(sel.createSelection())
const localVisible = ref([])
const localLoading = ref(false)

// 键盘分派与批量动作归属的面板（由 FilePane 的 @focus 维护）
const focusedPane = ref('remote')
// 冲突对话框状态：resolve 是 runBatch 里 await 的 promise resolver
const conflict = reactive({ visible: false, resolve: null, plan: null, hiddenSelected: 0 })

// shared "show hidden files" toggle (both panes)
const showAll = ref(false)

// explicit connect/disconnect state for the remote host
const connected = ref(false)

// pendingPath：位置下拉点到"未连接的主机"时先记下目标目录，由 connect() 消费
// （同机已连接则直接跳转，不走这里）。
const pendingPath = ref('')

const transfers = ref([])

// clock ticks every second so the transfer queue's elapsed times repaint.
const now = ref(Date.now())
let clockTimer = null

// context menu state
const menu = ref({ visible: false, x: 0, y: 0, pane: 'remote', item: null })

// modal dialog state (replaces native window.confirm/prompt)
const dialog = ref({ visible: false, mode: 'confirm', title: '', message: '', placeholder: '', initial: '' })
let dialogResolve = null

function openConfirm(title, message) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, mode: 'confirm', title, message, placeholder: '', initial: '' }
    dialogResolve = resolve
  })
}
function openPrompt(title, message, initial) {
  return new Promise((resolve) => {
    dialog.value = { visible: true, mode: 'prompt', title, message, placeholder: '', initial: initial || '' }
    dialogResolve = resolve
  })
}
function onDialogOk(value) {
  dialog.value.visible = false
  if (dialogResolve) { dialogResolve(value || true); dialogResolve = null }
}
function onDialogCancel() {
  dialog.value.visible = false
  if (dialogResolve) { dialogResolve(null); dialogResolve = null }
}

// remote pane request sequence：任何新请求或切换主机都自增，
// 过期响应直接丢弃，防止旧主机/旧目录的结果污染当前视图（M3）。
let remoteSeq = 0

function closeMenu() { menu.value.visible = false }
function outsideClick() { closeMenu() }

async function loadHosts() {
  try { hosts.value = (await ListHosts()) || [] } catch (e) { err(e) }
  if (hosts.value.length && !host.value) host.value = hosts.value[0]
}
async function err(e) {
  logStore.add({ source_id: 'sftp', source_type: 'sftp', level: 'error', message: String(e), ts: new Date().toISOString() })
}

async function loadRemote() {
  if (!host.value) return
  const seq = ++remoteSeq
  remoteLoading.value = true
  try {
    const items = (await SftpList(host.value, '', remotePath.value || '/')) || []
    if (seq === remoteSeq) remoteItems.value = items // 已被更新请求取代则丢弃
  }
  catch (e) { err(e) }
  finally { if (seq === remoteSeq) remoteLoading.value = false }
}

// Host dropdown change: reset connection state, do NOT auto-connect.
function onHostChange() {
  remoteSeq++ // 使在途的远程请求全部过期
  connected.value = false
  remoteItems.value = []
  sel.clear(remoteSelection)
}

async function connect() {
  const h = host.value
  if (!h) return
  try {
    await SftpConnect(h)
    let dest = '/'
    try { dest = await SftpHome(h) } catch { dest = '/' }
    if (host.value !== h) return // await 期间已切换主机，丢弃结果
    // 「最近位置」预设了目标目录时优先进它，否则落到 home
    remotePath.value = pendingPath.value && pendingPath.value !== '/' ? pendingPath.value : dest
    pendingPath.value = ''
    connected.value = true
    await loadRemote()
    await locations.addRemoteRecent(h, remotePath.value)
  } catch (e) {
    err(e)
    if (host.value === h) connected.value = false
  }
}

// 面板头「📍 位置」选中一项：本地面板直接跳转；远程面板同机已连接则直接跳转，
// 未连接则记入 pendingPath 后交给既有的 connect()（它内部消费 pendingPath）。
async function pickPosition(pane, path) {
  if (pane === 'local') { localPath.value = path; await loadLocal(); await locations.addLocalRecent(path); return }
  if (host.value && connected.value) { remotePath.value = path; await loadRemote(); await locations.addRemoteRecent(host.value, path); return }
  pendingPath.value = path
  await connect()
}

// 当前目录是否已收藏：本地面板看书签 scope=local，远程面板还要匹配当前主机。
function bookmarkedFor(pane) {
  const p = pane === 'local' ? localPath.value : remotePath.value
  return locations.bookmarks.some((b) =>
    b.path === p && (pane === 'local' ? b.scope === 'local' : (b.scope === 'remote' && b.host === host.value)))
}

async function toggleBookmark(pane) {
  const p = pane === 'local' ? localPath.value : remotePath.value
  if (!p) return
  const existing = locations.bookmarks.find((b) =>
    b.path === p && (pane === 'local' ? b.scope === 'local' : (b.scope === 'remote' && b.host === host.value)))
  if (existing) await locations.removeBookmark(existing.scope, existing.host, existing.path)
  else await locations.addBookmark({ name: p, scope: pane, host: pane === 'remote' ? host.value : '', path: p })
}

// 双击深搜结果 → 跳到所在目录并选中该项
async function openHit(hit) {
  const dir = hit.path.includes('/') ? hit.path.slice(0, hit.path.lastIndexOf('/')) : ''
  const name = hit.path.includes('/') ? hit.path.slice(hit.path.lastIndexOf('/') + 1) : hit.path
  if (search.pane === 'local') {
    const next = dir ? (localPath.value.replace(/\/+$/, '') + '/' + dir) : localPath.value
    localPath.value = next
    await loadLocal()
    if (name) sel.single(localSelection, name)
  } else {
    const next = dir ? (remotePath.value.replace(/\/+$/, '') + '/' + dir) : remotePath.value
    remotePath.value = next
    await loadRemote()
    if (name) sel.single(remoteSelection, name)
  }
  search.visible = false
}

async function disconnect() {
  if (!host.value) return
  try { await SftpDisconnect(host.value) } catch (e) { err(e) }
  connected.value = false
  remoteItems.value = []
  sel.clear(remoteSelection)
}
async function loadLocal() {
  localLoading.value = true
  try {
    localItems.value = (await ListLocal(localPath.value || '/')) || []
    await locations.addLocalRecent(localPath.value) // 只在成功时记；条数上限由后端负责
  }
  catch (e) { err(e) }
  finally { localLoading.value = false }
}

function openRemote(it) {
  if (it.name === '..') { remotePath.value = parentOf(remotePath.value); loadRemote(); sel.clear(remoteSelection); return }
  if (!it.isDir) return
  remotePath.value = join(remotePath.value, it.name)
  loadRemote()
  sel.clear(remoteSelection)
}
function openLocal(it) {
  if (it.name === '..') { localPath.value = parentOf(localPath.value); loadLocal(); sel.clear(localSelection); return }
  if (!it.isDir) return
  localPath.value = join(localPath.value, it.name)
  loadLocal()
  sel.clear(localSelection)
}

// Absolute-path helpers (POSIX-style). '/' is the root; going up stops there.
function join(base, name) {
  if (base === '/' || base === '') return '/' + name
  return base.replace(/\/+$/, '') + '/' + name
}
function parentOf(p) {
  if (!p || p === '/') return '/'
  const trimmed = p.replace(/\/+$/, '')
  const idx = trimmed.lastIndexOf('/')
  if (idx <= 0) return '/'
  return trimmed.slice(0, idx)
}

function showMenu(pane, { item, event }) {
  const rect = window.innerWidth
  menu.value = {
    visible: true,
    x: Math.min(event.clientX, rect - 180),
    y: event.clientY,
    pane,
    item,
  }
}

function itemsFor(pane) { return pane === 'remote' ? remoteItems.value : localItems.value }
function visibleFor(pane) { return pane === 'remote' ? remoteVisible.value : localVisible.value }
function selectionFor(pane) { return pane === 'remote' ? remoteSelection : localSelection }

// 用可见集合（@visible 上抛的 showAll ∩ filter 结果）而不是原始 items：
// 原始 items 里包含被过滤隐藏的 dotfile，拿它当可见集会把计数恒算成 0（spec 决策 15 / §8.1）。
function hiddenSelectedCount(selection, visibleKeys) {
  const vis = new Set(visibleKeys || [])
  let n = 0
  for (const k of selection.keys) if (!vis.has(k)) n++
  return n
}
function hiddenFor(pane) { return hiddenSelectedCount(selectionFor(pane), visibleFor(pane)) }

// 面板头「已选 N 项 ▾」的动作表：语义按面板决定，面板本身不懂（spec §6.2）。
function actionsFor(pane) {
  const s = selectionFor(pane)
  const n = s.keys.size
  const tail = [
    { name: 'rename', label: '重命名', disabled: n !== 1 },
    { name: 'remove', label: '删除 (' + n + ')' },
    { name: 'select-all', label: '全选' },
    { name: 'clear', label: '清空' },
  ]
  if (n === 0 && pane === 'remote') return tail
  const head = pane === 'remote'
    ? [{ name: 'download', label: '下载到本地 (' + n + ')' }]
    : [{ name: 'upload', label: '上传到远程 (' + n + ')' }, { name: 'upload-picked', label: '上传文件…' }]
  return head.concat(tail)
}

function onPaneAction(pane, name) {
  const s = selectionFor(pane)
  if (name === 'select-all') return sel.all(s, visibleFor(pane))
  if (name === 'clear') return sel.clear(s)
  if (name === 'remove') return removeSelected(pane)
  if (name === 'upload-picked') return uploadPicked()
  if (name === 'rename') return renameItem(pane, itemsFor(pane).find((it) => s.keys.has(it.name)))
  if (name === 'download' || name === 'upload') return runBatchFor(pane, name)
}

function runBatchFor(pane, name) {
  const s = selectionFor(pane)
  const names = [...s.keys]
  if (!names.length) return null
  if (name === 'download') {
    return runBatch({ direction: 'download', names, sourceDir: remotePath.value, targetDir: localPath.value || '/', sourceItems: remoteItems.value })
  }
  // 上传只可能来自本地面板（远程面板没有上传入口，spec §6.2）
  return runBatch({ direction: 'upload', names, sourceDir: localPath.value || '/', targetDir: remotePath.value, sourceItems: localItems.value })
}

function onSelect(pane, { item, event }) {
  const s = selectionFor(pane)
  const vis = visibleFor(pane)
  focusedPane.value = pane
  if (event && (event.ctrlKey || event.metaKey)) sel.toggle(s, item.name)
  else if (event && event.shiftKey) sel.rangeTo(s, vis, item.name)
  else sel.single(s, item.name)
}

function askConflict(plan, hiddenSelected) {
  return new Promise((resolve) => {
    conflict.visible = true
    conflict.resolve = resolve
    conflict.plan = plan
    conflict.hiddenSelected = hiddenSelected
  })
}
function onConflictConfirm(policy) { conflict.visible = false; if (conflict.resolve) conflict.resolve(policy) }
function onConflictCancel() { conflict.visible = false; if (conflict.resolve) conflict.resolve(null) }

// direction: 'download' | 'upload'
// gesture: 'single' | 'batch'（默认 batch）。判别依据是**手势来源**而不是项数（spec 决策 7）：
//   右键单项下载/上传、跨面板单项拖拽 = single → 不弹冲突框，对已存在目标有意直接覆盖；
//   面板头下拉、多选批量、系统拖入 = batch → 保留 needsConfirm/askConflict 流程。
async function runBatch({ direction, names, sourceDir, targetDir, sourceItems, systemPaths, gesture = 'batch' }) {
  const sourceSelection = direction === 'upload' ? localSelection : remoteSelection
  const targetItems = direction === 'download' ? localItems.value : remoteItems.value
  const tasks = systemPaths
    ? systemPaths.map((i) => ({ name: i.name, src: i.path, dst: targetDir.replace(/\/+$/, '') + '/' + i.name, isDir: i.isDir }))
    : planTasks({ direction, names, sourceDir, targetDir, isDirMap: (sourceItems || []).reduce((m, it) => (m[it.name] = it.isDir, m), {}) })
  const hidden = hiddenSelectedCount(sourceSelection, visibleFor(direction === 'upload' ? 'local' : 'remote'))
  const existing = (targetItems || []).map((it) => it.name)
  const { clean, conflicts } = classify(tasks, existing)
  let run = clean
  let skipped = []
  if (gesture === 'single') {
    // 单文件手势免确认：冲突项直接进 run（覆盖语义），也不追问隐藏选中项。
    run = clean.concat(conflicts)
  } else if (needsConfirm({ conflictCount: conflicts.length, hiddenSelected: hidden })) {
    const policy = await askConflict({ conflicts, total: tasks.length, target: targetDir, hiddenSelected: hidden }, hidden)
    if (policy === null) return null
    const applied = applyPolicy(conflicts, policy, existing)
    skipped = applied.skipped
    run = clean.concat(applied.run)
  }
  const results = skipped.map((t) => ({ ...t, status: '跳过' }))
  for (const t of skipped) transfers.value.push({ direction, name: t.name, src: t.src, dst: t.dst, size: 0, status: '跳过', elapsed: 0 })
  for (const t of run) {
    const rec = { direction, name: t.name, src: t.src, dst: t.dst, size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (direction === 'download') await (t.isDir ? SftpGetDir(host.value, '', t.src, t.dst) : SftpGet(host.value, '', t.src, t.dst))
      else await (t.isDir ? SftpPutRecursive(host.value, '', t.src, t.dst) : SftpPut(host.value, '', t.src, t.dst))
      rec.status = '完成'
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      err(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
    results.push(rec)
  }
  await (direction === 'download' ? loadLocal() : loadRemote())
  const s = summarize(results)
  logStore.add({ source_id: 'sftp', source_type: 'sftp', level: s.failed ? 'error' : 'info', ts: new Date().toISOString(),
    message: '批量' + direction + ' ' + results.length + ' 项：成功 ' + s.ok + ' 跳过 ' + s.skipped + ' 失败 ' + s.failed })
  const doneNames = results.filter((r) => r.status === '完成').map((r) => r.name)
  sel.remove(sourceSelection, doneNames) // 只移除成功项：失败/留在原地的项仍应保持选中（spec §6.4）
  return s
}

// 「复制失败清单」：队列只负责 emit，落盘/剪贴板由编排层做（避免出现死按钮）。
async function copyFailures() {
  const failed = transfers.value.filter((t) => t.status === '失败')
  if (!failed.length) return
  const text = failed.map((t) => failureText(t)).join('\n')
  try { await navigator.clipboard.writeText(text) } catch (e) { err(e) }
}

// 删除（唯一不可撤销的批量动作，单独走确认）
async function removeSelected(pane) {
  const s = selectionFor(pane)
  const names = [...s.keys]
  if (!names.length) return
  const items = itemsFor(pane)
  // hiddenSelectedCount 的第二个参数必须是**可见名字**数组（与 FilePane 上抛的 @visible 同源），
  // 不是 item 对象数组：传 items 会让 vis.has(name) 恒假，把"被过滤隐藏的项数"恒算成选中总数，
  // 删除确认框就会恒显示「（其中 N 项被过滤隐藏）」，误导用户。
  const hidden = hiddenSelectedCount(s, visibleFor(pane))
  const hasDir = (items || []).some((it) => s.keys.has(it.name) && it.isDir)
  const msg = '删除' + (pane === 'local' ? '本地' : '远程') + ' ' + names.length + ' 项？' +
    (hasDir ? '（含目录，将递归删除）' : '') + (hidden ? '（其中 ' + hidden + ' 项被过滤隐藏）' : '')
  const ok = await openConfirm('确认删除', msg)
  if (!ok) return
  const base = pane === 'local' ? (localPath.value || '/') : remotePath.value
  for (const n of names) {
    const full = base.replace(/\/+$/, '') + '/' + n
    const rec = { direction: pane === 'local' ? 'move' : 'download', name: n, src: full, dst: '', size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (pane === 'local') await DeleteLocal(full)
      else await SftpRemoveRecursive(host.value, '', full)
      rec.status = '完成'
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      err(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await (pane === 'local' ? loadLocal() : loadRemote())
  // 只把成功删掉的项移出选中集合：失败项还留在原地，保持选中便于重试（spec §6.4）。
  const removed = names.filter((n) => !transfers.value.some((t) => t.name === n && t.status === '失败'))
  sel.remove(s, removed)
}

// rename / mkdir 仍是单项手势，保持原有实现（由 doAction 与面板动作表调用）。
async function renameItem(pane, it) {
  if (!it || it.name === '..') { closeMenu(); return }
  const newName = await openPrompt('重命名', '新名称：', it.name)
  if (!newName || newName === it.name) { closeMenu(); return }
  try {
    if (pane === 'local') {
      await renameLocal(join(localPath.value, it.name), join(localPath.value, newName))
      await loadLocal()
    } else {
      await SftpRename(host.value, '', join(remotePath.value, it.name), join(remotePath.value, newName))
      await loadRemote()
    }
  } catch (e) { err(e) }
  closeMenu()
}

// renameLocal renames a local file/dir via the Go binding.
async function renameLocal(oldPath, newPath) {
  await RenameLocal(oldPath, newPath)
}

async function mkdirIn(pane) {
  const name = await openPrompt('新建文件夹', '新建文件夹名称：', '')
  if (!name) return
  try {
    if (pane === 'local') {
      await MkdirLocal(join(localPath.value, name))
      await loadLocal()
    } else {
      await SftpMkdir(host.value, '', join(remotePath.value, name))
      await loadRemote()
    }
  } catch (e) { err(e) }
}

// uploadPicked = 既有 upload() 的"选文件上传"分支：右键上传已由批量入口 runBatch 承接
// （doAction('upload')），面板动作「上传文件…」只负责弹 PickLocalFile 后上传单个文件。
async function uploadPicked() {
  let t = null
  try {
    const local = await PickLocalFile()
    if (!local) return
    const name = local.split(/[\\/]/).pop()
    t = { direction: 'upload', name, src: local, dst: join(remotePath.value, name), size: 0, status: '处理中', startedAt: Date.now() }
    try { t.size = await StatLocal(local) } catch (e) { t.size = 0 }
    transfers.value.push(t)
    await SftpPut(host.value, '', local, join(remotePath.value, name))
    t.status = '完成'
    t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
    await loadRemote()
  } catch (e) {
    err(e)
    if (t) {
      t.status = '失败'
      t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
    }
  }
}

// 右键菜单：download/upload/remove 走批量入口（右键项不在集合内时先 single()，与 §3 决策 5 一致）。
async function doAction(name) {
  const pane = menu.value.pane
  const s = selectionFor(pane)
  const it = menu.value.item
  if (it && it.name !== '..' && !sel.isSelected(s, it.name)) sel.single(s, it.name)
  const names = [...s.keys]
  const sourceDir = pane === 'remote' ? remotePath.value : (localPath.value || '/')
  const targetDir = pane === 'remote' ? (localPath.value || '/') : remotePath.value
  const sourceItems = itemsFor(pane)
  closeMenu()
  const gesture = names.length === 1 ? 'single' : 'batch'
  if (name === 'download') return runBatch({ direction: 'download', names, sourceDir, targetDir, sourceItems, gesture })
  // 上传按面板分派（spec §6.2：远程面板的动作表里没有"上传"项）：
  // 远程面板右键的「上传…」语义 = 选本地文件上传，沿用既有 PickLocalFile 行为；
  // 只有本地面板的「上传到远程」才是"上传选中项"。
  // 绝不能用远程 names + 本地 sourceDir 去跑 runBatch：那会把远端路径当本地源执行 SftpPut。
  if (name === 'upload') {
    if (pane === 'remote') return uploadPicked()
    return runBatch({ direction: 'upload', names, sourceDir: localPath.value || '/', targetDir: remotePath.value, sourceItems: localItems.value, gesture })
  }
  if (name === 'remove') return removeSelected(pane)
  return legacyAction(name, pane, it)
}

// rename / mkdir 仍是单项手势，保持原有实现：
function legacyAction(name, pane, it) {
  if (name === 'rename') return renameItem(pane, it)
  if (name === 'mkdir') return mkdirIn(pane)
}

function onKeydown(ev) {
  const action = actionFor(ev)
  if (!action) return
  const s = selectionFor(focusedPane.value)
  const vis = visibleFor(focusedPane.value)
  if (action === 'delete') { ev.preventDefault(); removeSelected(focusedPane.value) }
  else if (action === 'select-all') { ev.preventDefault(); sel.all(s, vis) }
  else if (action === 'escape') { sel.clear(s) }
}

// ===== 拖拽：拖起 =====
function onDragStart(pane, { item, event }) {
  const s = selectionFor(pane)
  if (!sel.isSelected(s, item.name)) sel.single(s, item.name)
  event.dataTransfer.effectAllowed = 'copyMove'
  event.dataTransfer.setData('application/x-sshore', payloadFor(pane, [...s.keys]))
}

// ===== 路径①②：面板互拖（落到面板空白处 = 投递到对方当前目录）=====
async function onPaneDrop(targetPane, { event }) {
  const payload = parsePayload(event.dataTransfer.getData('application/x-sshore'))
  if (!payload || payload.pane === targetPane) return
  const guard = canDropInto({ sourcePane: payload.pane, targetPane, item: { isDir: true }, connected: connected.value })
  if (!guard.ok) { err(guard.reason); return }
  const gesture = payload.names.length === 1 ? 'single' : 'batch'
  if (payload.pane === 'remote') {
    await runBatch({ direction: 'download', names: payload.names, sourceDir: remotePath.value, targetDir: localPath.value, sourceItems: remoteItems.value, gesture })
  } else {
    await runBatch({ direction: 'upload', names: payload.names, sourceDir: localPath.value, targetDir: remotePath.value, sourceItems: localItems.value, gesture })
  }
}

// ===== 路径④：面板内移动到子目录行 =====
async function onMoveDrop(pane, { item, event }) {
  const payload = parsePayload(event.dataTransfer.getData('application/x-sshore'))
  if (!payload || payload.pane !== pane || !payload.names.length) return
  if (!item.isDir) { err('只能放到目录上'); return }
  const base = pane === 'local' ? (localPath.value || '/') : remotePath.value
  const targetDir = base.replace(/\/+$/, '') + '/' + item.name
  // 非法落点（自嵌套 / 拖到目录自己那一行）必须在发请求前拒绝
  for (const n of payload.names) {
    const guard = canDropInto({ sourcePane: pane, targetPane: pane, item: { name: n, isDir: true }, sourceDir: base, targetDir: targetDir + '/' + n, connected: connected.value })
    if (!guard.ok) { err(guard.reason); return }
  }
  // 目标子目录内容未加载 → 先按需加载一次再判重（spec 决策 25）；Windows 上
  // os.Rename 目标存在会直接失败，所以必须走同一套冲突策略而不是硬干。
  const targetItems = pane === 'local'
    ? await ListLocal(targetDir)
    : await SftpList(host.value, '', targetDir)
  const existing = (targetItems || []).map((it) => it.name)
  const prefix = base.replace(/\/+$/, '')
  const planned = payload.names.map((n) => ({ name: n, src: prefix + '/' + n, dst: targetDir + '/' + n }))
  const { clean, conflicts } = classify(planned, existing)
  let run = clean
  let skipped = conflicts
  if (conflicts.length) {
    const policy = await askConflict({ conflicts, total: planned.length, target: targetDir, hiddenSelected: 0 }, 0)
    if (policy === null) return
    const applied = applyPolicy(conflicts, policy, existing)
    run = clean.concat(applied.run)
    skipped = applied.skipped
  }
  for (const t of skipped) {
    transfers.value.push({ direction: 'move', name: t.name, src: t.src, dst: t.dst, size: 0, status: '跳过', elapsed: 0 })
  }
  const done = []
  for (const t of run) {
    const rec = { direction: 'move', name: t.name, src: t.src, dst: t.dst, size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (pane === 'local') await RenameLocal(t.src, t.dst)
      else await SftpMove(host.value, '', t.src, t.dst)
      rec.status = '完成'
      done.push(t.name)
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      err(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await (pane === 'local' ? loadLocal() : loadRemote())
  // spec §6.4：失败/跳过的项还留在原处，必须保持选中便于重试——只移除确实完成的项。
  sel.remove(pane === 'local' ? localSelection : remoteSelection, done)
}

// ===== 路径③：系统文件管理器拖入 =====
let offDrop = null

function onFilesDropped(payload) {
  if (!payload || !payload.paths || !payload.paths.length) return
  const localEl = document.querySelector('[data-pane="local"]')
  const remoteEl = document.querySelector('[data-pane="remote"]')
  if (!localEl || !remoteEl) return
  const rects = { local: localEl.getBoundingClientRect(), remote: remoteEl.getBoundingClientRect() }
  // 坐标单位 / DPI 缩放需实测（spec R3）；命中失败只会提示"落点无效"，不会误操作。
  const pane = hitPane({ x: payload.x, y: payload.y }, rects)
  if (!pane) { err('落点无效：请拖到左侧本地或右侧远程面板'); return }
  handleSystemDrop(pane, payload.paths)
}

async function handleSystemDrop(pane, paths) {
  const infos = await StatPaths(paths)
  // PathInfo.Err 带 omitempty：成功项的 JSON 里**没有** err 键，因此只能用 if (i.err) 判失败。
  for (const i of infos) if (i.err) err(i.path + '：' + i.err)
  const ok = infos.filter((i) => !i.err)
  if (!ok.length) return
  if (pane === 'remote') {
    if (!connected.value) { err('远程未连接，无法上传'); return }
    await runBatch({ direction: 'upload', names: ok.map((i) => i.name), sourceDir: '', targetDir: remotePath.value, sourceItems: localItems.value, systemPaths: ok })
    return
  }
  const base = (localPath.value || '/').replace(/\/+$/, '')
  const existing = (localItems.value || []).map((it) => it.name)
  const conflicts = ok.filter((i) => existing.includes(i.name))
  let policy = 'skip'
  if (needsConfirm({ conflictCount: conflicts.length, hiddenSelected: 0 })) {
    const chosen = await askConflict({ conflicts: conflicts.map((i) => ({ name: i.name })), total: ok.length, target: base, hiddenSelected: 0 }, 0)
    if (chosen === null) return
    policy = chosen
  }
  for (const i of ok) {
    const hasConflict = existing.includes(i.name)
    if (hasConflict && policy === 'skip') {
      transfers.value.push({ direction: 'copy', name: i.name, src: i.path, dst: base + '/' + i.name, size: i.size, status: '跳过', elapsed: 0 })
      continue
    }
    let name = i.name
    if (hasConflict && policy === 'rename') {
      name = copyName(i.name, existing)
      existing.push(name) // 累积已占用的名字，否则两个同名源会算出同一个新名
    }
    const dst = base + '/' + name
    const rec = { direction: 'copy', name, src: i.path, dst, size: i.size, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try { await CopyLocal(i.path, dst); rec.status = '完成' } catch (e) { rec.status = '失败'; rec.reason = String((e && e.message) || e); err(e) }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await loadLocal()
}

onMounted(async () => {
  await loadHosts()
  await locations.load() // 书签与双侧最近位置（T6 store），供两个面板头的位置下拉使用
  // local starts at current working dir
  try { localPath.value = await Cwd() } catch (e) { localPath.value = '/' }
  await loadLocal()
})

function startClock() {
  if (!clockTimer) clockTimer = setInterval(() => { now.value = Date.now() }, 1000)
}
function stopClock() {
  if (clockTimer) { clearInterval(clockTimer); clockTimer = null }
}

// syncConnection 用后端 SftpConnected 校正本地状态（M6）：KeepAlive 下切标签
// 不再销毁视图，但首次挂载/后端 ControlPersist 过期时 UI 可能与真实连接不符。
async function syncConnection() {
  const h = host.value
  if (!h || connected.value) return
  try {
    const ok = await SftpConnected(h)
    if (host.value !== h || connected.value) return // await 期间状态已变
    if (!ok) { connected.value = false; return }
    connected.value = true
    try { remotePath.value = await SftpHome(h) } catch { remotePath.value = '/' }
    await loadRemote()
  } catch { /* 状态查询失败时保持现状 */ }
}

// KeepAlive 生命周期：切入时挂菜单监听、重启时钟并同步连接状态；切出时停时钟。
// 必须成对挂摘：SftpView 被 <KeepAlive> 缓存，setup 只跑一次（App.vue:57-61）。
// 在 setup 顶层注册会让「端口转发/文件同步」标签下按 Delete 弹出 SFTP 的删除确认框，
// 因此注册写进既有的 onActivated（那里已经在挂 click 监听）。
onActivated(() => {
  window.addEventListener('click', outsideClick)
  window.addEventListener('keydown', onKeydown)
  // 系统拖入订阅同样成对挂摘：KeepAlive 下 setup 只跑一次，切走标签必须退订，
  // 否则「端口转发/文件同步」标签下拖入文件也会投递到 SFTP 面板。
  offDrop = EventsOn('files:dropped', onFilesDropped)
  startClock()
  syncConnection()
})
onDeactivated(() => {
  window.removeEventListener('click', outsideClick)
  window.removeEventListener('keydown', onKeydown)
  if (offDrop) { offDrop(); offDrop = null }
  stopClock()
})
onUnmounted(() => {
  window.removeEventListener('click', outsideClick)
  window.removeEventListener('keydown', onKeydown)
  if (offDrop) { offDrop(); offDrop = null }
  stopClock()
})
</script>

<template>
  <div class="sftp">
    <div class="toolbar">
      <select v-model="host" @change="onHostChange">
        <option v-for="h in hosts" :key="h" :value="h">{{ h }}</option>
      </select>
      <button v-if="!connected" @click="connect" :disabled="!host">🔌 连接</button>
      <button v-else @click="disconnect">⏏ 断开</button>
      <button @click="loadRemote" :disabled="!connected">刷新</button>
      <label class="hidden-toggle">
        <input type="checkbox" v-model="showAll" /> 显示隐藏文件
      </label>
    </div>
    <div class="panes">
      <div class="pane-wrap" data-pane="local" @dragover.prevent @drop.prevent="onPaneDrop('local', $event)">
        <FilePane title="本地" pane="local" host="" :path="localPath || '/'" :items="localItems" :sel-keys="[...localSelection.keys]" :anchor="localSelection.anchor"
          :show-hidden="showAll" :loading="localLoading" :actions="actionsFor('local')" :hidden-selected="hiddenFor('local')"
          :bookmarks="locations.bookmarksForPane('local', '')" :recents="locations.recentsForPane('local', '')" :bookmarked="bookmarkedFor('local')"
          @select="onSelect('local', $event)" @open="openLocal" @action="onPaneAction('local', $event)"
          @pick-position="pickPosition('local', $event)" @toggle-bookmark="toggleBookmark('local')" @search="search.pane = 'local'; search.visible = true"
          @clear="sel.clear(localSelection)"
          @context="showMenu('local', $event)" @visible="localVisible = $event" @focus="focusedPane = 'local'"
          @dragstart="onDragStart('local', $event)" @dropon="onMoveDrop('local', $event)" />
      </div>
      <div class="pane-wrap" data-pane="remote" @dragover.prevent @drop.prevent="onPaneDrop('remote', $event)">
        <FilePane title="远程" pane="remote" :host="host" :path="remotePath" :items="remoteItems" :sel-keys="[...remoteSelection.keys]" :anchor="remoteSelection.anchor"
          :show-hidden="showAll" :loading="remoteLoading" :actions="actionsFor('remote')" :hidden-selected="hiddenFor('remote')"
          :bookmarks="locations.bookmarksForPane('remote', host)" :recents="locations.recentsForPane('remote', host)" :bookmarked="bookmarkedFor('remote')"
          @select="onSelect('remote', $event)" @open="openRemote" @action="onPaneAction('remote', $event)"
          @pick-position="pickPosition('remote', $event)" @toggle-bookmark="toggleBookmark('remote')" @search="search.pane = 'remote'; search.visible = true"
          @clear="sel.clear(remoteSelection)"
          @context="showMenu('remote', $event)" @visible="remoteVisible = $event" @focus="focusedPane = 'remote'"
          @dragstart="onDragStart('remote', $event)" @dropon="onMoveDrop('remote', $event)" />
      </div>
    </div>
    <TransferQueue :transfers="transfers" :now="now" @copy-failures="copyFailures" />
    <div class="logpane ui-panel"><LogPanel :source-types="['sftp', 'system']" /></div>

    <ContextMenu :visible="menu.visible" :x="menu.x" :y="menu.y" @close="closeMenu">
      <template v-if="menu.pane === 'remote'">
        <button @click="doAction('download')"><span class="ic">⬇</span>下载…</button>
        <button @click="doAction('upload')"><span class="ic">⬆</span>上传…</button>
        <button @click="doAction('rename')"><span class="ic">✏</span>重命名</button>
        <button class="sep" @click="doAction('remove')"><span class="ic">🗑</span>删除</button>
        <button @click="doAction('mkdir')"><span class="ic">📁</span>新建文件夹</button>
      </template>
      <template v-else>
        <button @click="doAction('upload')"><span class="ic">⬆</span>上传到远程</button>
        <button @click="doAction('rename')"><span class="ic">✏</span>重命名</button>
        <button class="sep" @click="doAction('remove')"><span class="ic">🗑</span>删除</button>
        <button @click="doAction('mkdir')"><span class="ic">📁</span>新建文件夹</button>
      </template>
    </ContextMenu>

    <AppDialog
      :visible="dialog.visible"
      :mode="dialog.mode"
      :title="dialog.title"
      :message="dialog.message"
      :initial="dialog.initial"
      @ok="onDialogOk"
      @cancel="onDialogCancel"
    />

    <ConflictDialog :visible="conflict.visible" :target="conflict.plan && conflict.plan.target"
      :total="conflict.plan && conflict.plan.total" :conflicts="conflict.plan ? conflict.plan.conflicts.map((c) => c.name) : []"
      :hidden-selected="conflict.hiddenSelected" @confirm="onConflictConfirm" @cancel="onConflictCancel" />

    <!-- 只挂一个实例，用 :scope 切换面板（组件内用全局 querySelector 聚焦，两个实例会打架） -->
    <SearchOverlay :visible="search.visible" :scope="search.pane" :host="host" :root="search.pane === 'local' ? localPath : remotePath"
      @close="search.visible = false" @open-hit="openHit" />
  </div>
</template>

<style scoped>
.sftp { display: flex; flex-direction: column; height: 100%; gap: 8px; }
.toolbar { display: flex; gap: 8px; align-items: center; }
.panes { display: flex; gap: 8px; flex: 1; min-height: 0; }
/* 放置区容器：承接 .panes 的伸缩；data-pane 同时是系统拖入命中测试的锚点 */
.pane-wrap { flex: 1; display: flex; min-width: 0; }
.logpane { height: 140px; flex-shrink: 0; padding: 12px; overflow: auto; }
.hidden-toggle { display: flex; align-items: center; gap: 4px; font-size: var(--fs-12); color: var(--text-dim); }
</style>
