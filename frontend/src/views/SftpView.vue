<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { ListHosts, SftpList, SftpGet, SftpGetDir, SftpPut, SftpPutRecursive, SftpRemoveRecursive, SftpMove, SftpRemove, SftpMkdir, SftpRename, SftpConnect, SftpDisconnect, SftpConnected, SftpProbe, SftpTransferCancel, ListLocal, DeleteLocal, MkdirLocal, RenameLocal, StatLocal, PickLocalFile, CopyLocal, StatPaths, Cwd, SftpHome, ClipboardFileSupport, ClipboardTempDir, CopyFilesToClipboard } from '../../wailsjs/go/main/App'
import { useLogStore } from '../stores/logs'
import { useLocationsStore } from '../stores/locations'
import { useSftpUIStore } from '../stores/sftpui'
import SearchOverlay from '../components/SearchOverlay.vue'
import FilePane from '../components/FilePane.vue'
import TransferQueue from '../components/TransferQueue.vue'
import LogPanel from '../components/LogPanel.vue'
import ContextMenu from '../components/ContextMenu.vue'
import MenuSubmenu from '../components/MenuSubmenu.vue'
import AppDialog from '../components/AppDialog.vue'
import ConflictDialog from '../components/ConflictDialog.vue'
import * as sel from '../utils/selection'
import { copyTargetNames, copyPayload } from '../utils/copy'
import { join as localJoin, parentOf as localParent, joinRel, splitPath } from '../utils/localpath'
import { actionFor } from '../utils/keys'
import { clampLogHeight, LOG_DEFAULT_H } from '../utils/logpane'
import { planTasks, classify, applyPolicy, needsConfirm, summarize, copyName, nextTransferSeq, transferID } from '../utils/batch'
import { failureText, isInternalTempName, applyProgress, TRANSFER_PROGRESS_EVENT,
  applyOutcome, applyCancelResult, applyBatchCancel, shouldDispatch, hasCancelled } from '../utils/queue'
import { payloadFor, payloadFromDragEvent, DRAG_MIME, pickDropPane, canDropInto } from '../utils/dnd'
import { setSystemDropHandler } from '../utils/systemDrop'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { resolvePathInput, REMOTE_HOME } from '../utils/pathbar'
import { resolveRemoteTarget } from '../utils/remotepath'
// 说明：StatPaths / CopyLocal / SftpMove / ListLocal / RenameLocal 已在既有 import 行里，
// 这里**不重复声明**（重复 import 同名标识符会让 vite build 直接 SyntaxError）。

const logStore = useLogStore()
const locations = useLocationsStore()
// 最后路径/排序的持久化 store（T7）：启动恢复、落点决策、排序写入的唯一真源。
const sftpui = useSftpUIStore()
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
// pendingHome：「主目录」按钮的**显式意图**（§6.4 B3 / D15）。不能再靠"空 pendingPath = home"
// 这个隐含约定 —— 一旦插入"记忆路径"优先级，"主目录"就会被送回上次的目录。
const pendingHome = ref(false)

const transfers = ref([])

// 面板不显示我们自己的临时文件（.sshore-sftppart-*，含 backup 名）。
// 隐藏而不是删除：列表刷新后仍在，选中/删除等动作够不到它们。
const visibleRemoteItems = computed(() => (remoteItems.value || []).filter((it) => !isInternalTempName(it.name)))
const visibleLocalItems = computed(() => (localItems.value || []).filter((it) => !isInternalTempName(it.name)))

// 进度订阅的退订函数：KeepAlive 下 setup 只跑一次，必须成对挂摘
// （系统拖入处理器走 setSystemDropHandler(null)，同样在 onDeactivated/onUnmounted 成对摘）。
let offProgress = null

// 事件按 id 落到队列项，并把 PartPath 存进该项 —— 续传/清理都靠这个锚点。
function onTransferProgress(frame) {
  applyProgress(transfers.value, frame) // transfers 是 ref([])，内部 mutate 即可触发重绘
}

// 单文件/目录项的实际派发：runBatch 与「重试」共用。
// 终态**只由这次调用的结果决定**（见 task-14 渲染契约第 1 条）。
async function dispatchTransfer(rec) {
  rec.status = '处理中'
  rec.reason = ''
  rec.elapsed = 0
  rec.cancelRequested = false
  rec.cancelFailed = false
  rec.pendingCancel = false
  rec.cancelOk = false // 上一轮「真取消」的锁存（修复轮 3）必须清掉，重试是全新一次取消周期
  rec.outcome = null // 上一轮的操作结果原文（I3 顺序 B 的修正依据）必须清掉，否则重试会误判
  rec.pending = false
  rec.hasProgress = false
  rec.done = undefined
  rec.total = undefined
  rec.filesDone = undefined
  rec.filesTotal = undefined
  rec.phase = ''
  // 主机身份快照（审核 F21）：回同步 connected 必须带守卫 —— await 期间用户可能已经切了主机，
  // 把新主机写成"已连接"就是撒谎。
  const h = host.value
  rec.startedAt = Date.now()
  let endedAt = 0 // 终态时刻在 catch 首行锁定：探活/重连最长约 15s，不得算进「耗时」（T13 复核 Minor）
  try {
    if (rec.direction === 'download') {
      await (rec.isDir ? SftpGetDir(rec.id, host.value, '', rec.src, rec.dst, rec.resume, rec.partPath) : SftpGet(rec.id, host.value, '', rec.src, rec.dst, rec.resume, rec.partPath))
    } else {
      await (rec.isDir ? SftpPutRecursive(rec.id, host.value, '', rec.src, rec.dst, rec.resume, rec.partPath) : SftpPut(rec.id, host.value, '', rec.src, rec.dst, rec.resume, rec.partPath))
    }
    // 终态决策在 queue.js 的 applyOutcome（唯一落点，单测同函数）：操作返回 nil = 已提交，
    // 即使 Cancel 返回过 true（提交已完成的窄窗口，Task 10 评审有真协议探测），也必须渲染为
    // 完成（"取消过晚"），绝不删掉已安装的目标文件。
    applyOutcome(rec, { ok: true })
    // §8.5 一致性回同步（审核 FF16）：后端传输按需拨号 ⇒ 传输成功即"链路真的可用"，
    // 回写 connected 让行菜单/工具栏刷新不再被置灰规则跳过。守卫见上面的快照注释。
    if (host.value === h && !connected.value) connected.value = true
  } catch (e) {
    endedAt = Date.now()
    // 抛错时是否算「取消」由 cancelFailed 区分：Cancel 明确返回 false（未能取消）之后，
    // 这次失败是无关失败，绝不栽给取消（评审 I3）。
    applyOutcome(rec, { ok: false, error: e })
    // 取消语义优先（审核 FF15a）：用户主动取消导致的失败不该被归因为"连接已失效"，
    // 否则会白跑一次探活，真断链时还会把"取消"也拖进重连。其余失败归口复核（D14）。
    if (rec.cancelRequested) err(e)
    else await remoteOpFailed(e)
  }
  rec.elapsed = Math.floor(((endedAt || Date.now()) - rec.startedAt) / 1000)
  return rec.status
}

// 取消 = 取消被点项 + 停止派发本批后续项（整批语义由前端编排层落实，spec §6.1）。
// 终态判定绝不在这里内联：在飞项的终态只看传输方法的返回（applyOutcome），
// 未派发项的终态由 applyBatchCancel 立即落定。
async function cancelTransfer(rec) {
  if (!rec || !rec.id) return
  // 取消落点全在 queue.js 的 applyBatchCancel（唯一落点，单测同函数）：
  //  - 被点的是 pending（还没派发）：它自己没有操作结果，立即定「取消」并 batchAborted，
  //    绝不派发它，也绝不对未注册的 id 假称「取消中」（评审 I2）；
  //  - 被点的是在飞项：置 cancelRequested/pendingCancel，终态等它自己的返回。
  // 两种情况下同批后面的 pending 项都立即定「取消」，派发循环据此 break。
  const plan = applyBatchCancel(transfers.value, rec)
  if (!plan.inFlight) return
  // Cancel 的返回值必须用起来（评审 I3）：true 保持「取消中…」；false 立刻转「未能取消」，
  // 并且之后这次传输若以错误结束，也不算取消。
  try {
    applyCancelResult(rec, await SftpTransferCancel(rec.id))
  } catch (e) {
    applyCancelResult(rec, false)
    err(e)
  }
}

// 重试：整项重跑，resume=false。旧 partPath 仍传给后端，让后端先删同名 .part 再重来。
async function retryItem(rec) {
  if (!rec || !rec.id) return
  const keep = rec.partPath
  rec.resume = false
  rec.partPath = keep
  await dispatchTransfer(rec)
  await (rec.direction === 'download' ? loadLocal() : loadRemote())
}

// 续传：从已保存的 .part 锚点续写（仅单文件项提供该按钮）。
async function resumeItem(rec) {
  if (!rec || !rec.id || !rec.partPath || rec.isDir) return
  rec.resume = true
  await dispatchTransfer(rec)
  await (rec.direction === 'download' ? loadLocal() : loadRemote())
}

// 清理：立即删掉该项的 .part（对 7 天崩溃清理窗口的补偿，D14）。失败只提示、不抛。
async function cleanItem(rec) {
  if (!rec || !rec.partPath) return
  try {
    if (rec.direction === 'download') await DeleteLocal(rec.partPath)
    else await SftpRemove(host.value, '', rec.partPath)
    rec.partPath = ''
    logStore.add({ source_id: 'sftp', source_type: 'sftp', level: 'info', ts: new Date().toISOString(), message: '已清理临时文件 ' + rec.src })
  } catch (e) {
    // §8.4 第 12 项（审核 FF3）：SftpRemove 的失败原来被包成字符串「清理临时文件失败：」，
    // 任何"函数里有没有裸 err(e)"的口径都看不见它。按 rec.direction 分派：
    // download = 本地的 DeleteLocal（保持既有文案），upload = 远程 ⇒ 归口复核。
    if (rec.direction === 'download') err('清理临时文件失败：' + String((e && e.message) || e))
    else await remoteOpFailed(e)
  }
}

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
// 级别不能写错（spec §10 / 审核 F7）：err() 把 level 硬编码成 'error'，所以「记住的路径
// 失效回落」这类 warn、探活这类 info 必须走独立 helper，否则验收 grep 的级别会失真。
async function warn(msg) {
  logStore.add({ source_id: 'sftp', source_type: 'sftp', level: 'warn', message: String(msg), ts: new Date().toISOString() })
}
async function info(msg) {
  logStore.add({ source_id: 'sftp', source_type: 'sftp', level: 'info', message: String(msg), ts: new Date().toISOString() })
}

// ===== 连接失效自愈（spec §8.3 / §8.4 / §8.5）=====
// 根因：Connected(host) 是**粘性连接意图**（握手成功即 true，只有显式 Disconnect 才清）⇒
// 主机休眠唤醒后链路已死但后端仍回答 true，UI 永远停在"已连接"，且期间的操作全部失败。
// probing / recovering 必须是**普通 let 变量（非 ref）**（审核 F15）：接线测试按 `probing = false`
// 这类赋值形态匹配，写成 ref 会红，而 ref 恒真的误用会让探活/重连被静默短路。
let probing = false // 只防"探针并发"（管探活那一段）
let recovering = false // 防"反复切标签叠加重连"（管重连那一段）；两个标志各管一段，缺一不可
// 客户端超时兜底（spec §8.3，15s：覆盖后端最坏预算并留余量）
const PROBE_TIMEOUT_MS = 15000

// 探活三态：绝不能用布尔表示"未知"，否则超时/绑定抛错会被当成"链路已失效"、对可能健康的
// 链路做无谓 teardown（违反 D14「探针说失效才算失效」）。
//   'ok'      = 后端探针确认可用
//   'lost'    = 后端探针明确回答 false（链路确实不可用）
//   'unknown' = 超时或探针自身抛错（不改状态、不重连、不记 error）
// 客户端超时不是锦上添花：legacy batch 后端挂在无 ctx 的 cmd.Run() 上时 await SftpProbe 永不返回，
// probing 若不靠 finally 复位，该视图此后**任何**切标签探活都会被静默短路（自愈永久失效且无报错）。
async function probeTimed(h) {
  const started = performance.now()
  let timer = null
  let state = 'unknown'
  try {
    state = await Promise.race([
      Promise.resolve(SftpProbe(h)).then((ok) => (ok ? 'ok' : 'lost')),
      new Promise((resolve) => { timer = setTimeout(() => resolve('unknown'), PROBE_TIMEOUT_MS) }),
    ])
  } catch (e) {
    // 探针自身抛错：工具坏了不等于链路坏了 ⇒ 未知，既不重连也不改状态
    state = 'unknown'
  } finally {
    if (timer !== null) clearTimeout(timer) // 不 clear 就每次探活都留一个挂着的句柄
  }
  // spec §10 / 审核 F12：每次探活一条 info 级耗时日志，§11.3 F1 的耗时分布靠它观测
  await info('探活耗时 ' + Math.round(performance.now() - started) + 'ms')
  return state
}

// 远程操作失败后的复核（spec §8.4 / D14）：只回答"链路是不是真的没了"。
// `!connected.value` 早退（自审修正 7）：那是"连接本身刚失败"的场景（例如自愈过程中重连失败），
// 此时再探活/再重连只会打出第二轮无意义尝试。probing 只管探针并发，管不住
// "探针 A 失败后重连还在跑、探针 B 又失败"的重入 —— 那一段由 recovering 管。
async function verifyRemote() {
  const h = host.value
  if (!h || !connected.value || probing) return
  probing = true
  try {
    // === 'lost' 是硬要求（本计划最重要的 Blocking 修正）：三态是字符串，把探活结果当布尔取反
    // 再判断（在 'ok'/'lost'/'unknown' 下恒为 false）会让自愈永不触发。
    if ((await probeTimed(h)) === 'lost') await recoverConnection(h)
  } finally {
    probing = false
  }
}

// 远程操作失败的统一归口：先记后端原始报错原文（绝不丢 e），再复核链路（可能触发一次自愈）。
// **自身永不 reject**：loadRemote 里是浮动调用（void Promise.resolve().then(...)），
// 这里一旦抛出就是 unhandled rejection —— 视图没有任何调用点能接住它。故两级兜错。
async function remoteOpFailed(e) {
  try {
    await err(e)
  } catch {
    // 日志系统自身故障也不能因此跳过链路复核
  }
  try {
    await verifyRemote()
  } catch (e2) {
    try { await err(e2) } catch { /* 连补记都失败就只能沉默：绝不外泄 rejection */ }
  }
}

// 自愈（spec §8.3 步骤 1-7）：先断开（清粘性意图 + 丢死会话），再走既有握手流程重连**一次**。
// 只重连一次：不循环、不起定时器；recovering 幂等，防反复切标签叠加出多轮重连。
async function recoverConnection(h) {
  if (recovering) return
  recovering = true
  try {
    await warn('远端连接已失效（主机休眠或网络中断），正在重连…')
    // 清粘性意图是必须的（spec §8.2）：后端探针经 AcquireList → markSessionConnected 会把
    // 粘性 Connected 置 true，"探针说失效"与"粘性已置位"会同时成立，不清就会翻回"已连接"。
    // 断开失败静默（§10）：它的失败不影响后面的重新握手。
    try { await SftpDisconnect(h) } catch { /* 清理失败不中断自愈 */ }
    connected.value = false
    remoteItems.value = []
    sel.clear(remoteSelection) // 与 disconnect() 的降级形态一致 ⇒ §5.3 置灰规则自动生效
    // 不写 pendingPath（v1.7 修正 / 审核 F6）：pendingPath 是落点优先级 1，把记忆值塞进去会让
    // resolveRemoteTarget 返回 fromMemory === false ⇒ §6.5「仅来自记忆才回落 home」在自愈路径
    // 永不触发（R3 承诺失效）。什么都不设才对：下面的重连会在优先级 3 自取同一记忆值。
    await connect()
    // 用户已切主机：静默早退，不记「重连失败」（那条日志是噪音且误导，spec §8.3 步骤 6 例外）
    if (host.value !== h) return
    // connect() 不返回值也不 rethrow ⇒ 成功/失败判据只能是 connected.value（审核 FF14）
    if (connected.value) await info('已重新连接 ' + h)
    else await err('重连失败：连接未建立，请手动点「连接」')
  } finally {
    recovering = false
  }
}

// 取列表的**裸函数**：只做「取列表 + 抛错」（spec §6.5）。
// 序号守卫、loading、最近路径三项绝不塞进来（审核 F23）：goRemote/goLocal 的
// 「旧响应丢弃 + 失败整体回滚」靠调用点自己自增序号与回滚；fetch* 若也动这些，
// 一个过期的失败响应就会把目录回滚成旧值。
async function fetchRemote(target) {
  return (await SftpList(host.value, '', target || '/')) || []
}

async function loadRemote() {
  if (!host.value) return
  const seq = ++remoteSeq
  remoteLoading.value = true
  try {
    const items = await fetchRemote(remotePath.value || '/')
    if (seq === remoteSeq) remoteItems.value = items // 已被更新请求取代则丢弃
    // 审核 F10：双击进目录（openRemote）与工具栏刷新都只经这里，不补记则最常用的手势
    // 永远不写 last_remote_dirs（本地侧 loadLocal 本来就记，这正是两侧不对称的来源）。
    // 不增加写盘：locations.addRemoteRecent 有队首短路，记录无变化时不发 IPC。
    // 记账独立兜错（顺序不变：取列表成功后才记，且同受 seq 守卫）：写盘失败是本地 IO
    // 故障、不是链路信号，混进外层 catch 会把"后端写盘失败"误诊成"连接已断"，白跑一次探活/重连。
    if (seq === remoteSeq) {
      try { await locations.addRemoteRecent(host.value, remotePath.value) } catch (e) { err(e) }
    }
  }
  // 审核 F5：本轮语义变更 —— 记原文 + 一次探活复核（可能自愈），不再只是 err(e)。
  // 故意不 await：探活最多 15s，await 会把 loading 与调用方（如 runBatch 收尾）一起拖住。
  // 但"不 await"绝不能变成"外泄 rejection"：调用点（openRemote / 工具栏刷新 / removeSelected…）
  // 都不接住 loadRemote()，这里同步抛出（T13 落地 remoteOpFailed 之前它是未定义标识符）就会
  // 直接变成 unhandled rejection。故把调用放进一条内部已终结的 Promise 链，rejection 一律落进 err。
  catch (e) { void Promise.resolve().then(() => remoteOpFailed(e)).catch(err) }
  finally { if (seq === remoteSeq) remoteLoading.value = false }
}

// 排序状态上收（§7.3）：FilePane 只 emit「下一个方向」，父层只负责持久化 ——
// 绝不在父层再取反（T9 交接：payload.dir 已经是目标方向），否则每次点击都翻转方向。
async function onSort(pane, { key, dir }) {
  try { await sftpui.setSort(pane, key, dir) } catch (e) { err(e) }
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
  // B4 / 审核 F8：load() 未完成时点「连接」拿不到记忆路径（幂等重试即可）。
  // 必须 catch：load() 会 reject，裸 await 会让这里的失败变成未捕获拒绝、
  // 连接按钮"点了没反应"。加载失败仍继续（回落到 home）。
  if (!sftpui.loaded) await sftpui.load().catch(err)
  try {
    await SftpConnect(h)
    if (host.value !== h) return // await 期间已切换主机，丢弃结果
    // SftpHome 失败沿用既有回落（内层静默；外层 catch 覆盖，spec §8.4 排除清单）
    let home = '/'
    try { home = await SftpHome(h) } catch { home = '/' }
    if (host.value !== h) return
    // §6.4 B5：落点决策只有 resolveRemoteTarget 一个实现（connect 与 syncConnection 共用）。
    // 必须传 .value（审核 F17）：传 ref 对象会恒为真值，把 ref 当路径交给后端。
    const target = resolveRemoteTarget({
      pendingPath: pendingPath.value,
      pendingHome: pendingHome.value,
      remembered: sftpui.lastPathFor('remote', h),
      home,
    })
    // 消费后清空（含失败分支）：残留会让**下一次**连接落到上一次的目标（既有 :335 原则）
    pendingPath.value = ''
    pendingHome.value = false
    connected.value = true
    remotePath.value = target.path
    // §6.5 前提前置（B2）：只有"本次落点来自记忆"才需要先列一次以判定要不要回落 home。
    // 用户显式指定的路径（pendingPath/pendingHome）失败仍按既有失败语义处理，绝不偷偷改道。
    if (target.fromMemory && target.path !== home) {
      try {
        await fetchRemote(target.path)
      } catch (e) {
        remotePath.value = home
        await fetchRemote(home) // 回落本身也失败 ⇒ 抛给外层 catch（不二次回落）
        warn('上次的远程目录 ' + target.path + ' 已不可用，已回到 ' + home)
      }
    }
    await loadRemote()
    // 记账（配置写盘）失败不得被外层 catch 判成「连接失败」：只记日志，不改 connected/pending
    // 语义（T11 复核 out-of-scope 观察；与 loadRemote 内的记账同形）。
    try { await locations.addRemoteRecent(h, remotePath.value) } catch (e) { err(e) }
  } catch (e) {
    err(e)
    // 失败路径也要消费掉两个 pending 标志（onNavigate 对 pendingPath 的补偿只覆盖它自己那条
    // 调用链；连接按钮 / 位置下拉 / goRemoteHome 留下的标志无人清）：残留会让**下一次**
    // 「连接」落到上一次没成功的 home 或目录，正是 B1/SF4 那类静默失效。
    pendingPath.value = ''
    pendingHome.value = false
    if (host.value === h) connected.value = false
  }
}

// 乐观提交 + 失败整体回滚：输入框立刻反映用户输入，打不开时路径与目录内容一起复原，
// 并把后端原始错误写进日志面板（用户看得见，不是"点了没反应"）。
// 本地也要自己的请求序号：乐观提交引入了回滚，慢的失败响应若晚于更晚的成功返回，
// 就会把目录回滚成旧值（远程早有 remoteSeq 守这件事，本地原来没有回滚所以不需要）。
let localSeq = 0

async function goLocal(target) {
  const prevPath = localPath.value
  const prevItems = localItems.value
  const seq = ++localSeq
  localPath.value = target
  localLoading.value = true
  try {
    const items = await fetchLocal(target || '/')
    if (seq !== localSeq) return
    localItems.value = items
    sel.clear(localSelection)
    // 同 goRemote：记账失败不得触发本地面板回滚。
    try { await locations.addLocalRecent(target) } catch (e) { err(e) }
  } catch (e) {
    if (seq !== localSeq) return
    localPath.value = prevPath
    localItems.value = prevItems
    err('无法打开 ' + target + '：' + String((e && e.message) || e))
  } finally { if (seq === localSeq) localLoading.value = false }
}

// 远程同形；remoteSeq 的过期丢弃语义与 loadRemote 保持一致（旧主机的响应不得污染当前视图）。
async function goRemote(target) {
  const prevPath = remotePath.value
  const prevItems = remoteItems.value
  const seq = ++remoteSeq
  remotePath.value = target
  remoteLoading.value = true
  try {
    const items = await fetchRemote(target || '/')
    if (seq !== remoteSeq) return
    remoteItems.value = items
    sel.clear(remoteSelection)
    // 同 connect：记账失败不得触发导航回滚或探活（否则「写盘失败」会被记成「导航失败 + 链路失效」）。
    try { await locations.addRemoteRecent(host.value, target) } catch (e) { err(e) }
  } catch (e) {
    if (seq !== remoteSeq) return
    remotePath.value = prevPath
    remoteItems.value = prevItems
    // §8.4 第 4 项：远程导航失败是"链路可能已失效"的信号之一（D14）⇒ 交给统一归口记原文 + 复核。
    await remoteOpFailed(e)
  } finally { if (seq === remoteSeq) remoteLoading.value = false }
}

// 远程「主目录」：未连接时置**显式标志** pendingHome 再 connect()（D15：不再用
// "清空 pendingPath"表达 home，否则记忆路径会把它截胡），已连接则直接解析 SftpHome 后跳转。
// 行为变化（有意）：home 也会记入「最近」。
async function goRemoteHome() {
  if (!host.value) { err('请先选择主机'); return }
  if (!connected.value) { pendingHome.value = true; await connect(); return }
  let home = '/'
  // §8.4 第 5 项：SftpHome 是纯远程调用，失败同样要复核（它是用户点的动作，不是状态查询）。
  try { home = await SftpHome(host.value) } catch (e) { await remoteOpFailed(e); return }
  return goRemote(home)
}

// 面板头路径栏提交（唯一入口是 FilePane 的 @navigate）。
async function onNavigate(pane, raw) {
  const r = resolvePathInput({
    pane, raw,
    hasHost: !!host.value,
    connected: connected.value,
    current: pane === 'local' ? localPath.value : remotePath.value,
  })
  if (r.kind === 'noop') {
    if (r.reason === 'no-host') err('请先选择主机')
    else if (r.reason === 'local-home') err('本地路径不支持 ~ 写法，请填绝对路径')
    return
  }
  if (r.kind === 'home') return goRemoteHome()
  if (r.kind === 'connect') {
    pendingPath.value = r.path
    await connect()
    // connect() 失败时不改 remotePath、也不清 pendingPath（SftpView.vue:254-257 的既有行为）。
    // 残留的 pendingPath 会让**下一次**成功连接落到上次失败的目录，所以这里显式清掉。
    if (pendingPath.value === r.path) pendingPath.value = ''
    return
  }
  if (pane === 'local') return goLocal(r.path)
  return goRemote(r.path)
}

// 面板头「📍 位置」选中一项：本地面板直接跳转；远程面板同机已连接则直接跳转，
// 未连接则记入 pendingPath 后交给既有的 connect()（它内部消费 pendingPath）。
async function pickPosition(pane, path) {
  if (pane === 'local') return goLocal(path)
  if (path === REMOTE_HOME) return goRemoteHome()
  if (host.value && connected.value) return goRemote(path)
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
  // hit.path 是**根相对路径**且后端已 filepath.ToSlash（恒用 '/' 分隔）；
  // 本地拼回绝对路径时要按基目录的风格输出分隔符（Windows 上是 '\\'）。
  const { dir, name } = splitPath(hit.path)
  if (search.pane === 'local') {
    if (dir) localPath.value = joinRel(localPath.value, dir)
    await loadLocal()
    if (name) sel.single(localSelection, name)
  } else {
    // 远程必须继续用 POSIX 助手：sepOf() 只要发现路径里有反斜杠就返回 '\'，
    // 而远端路径里出现反斜杠是合法的（文件名），joinRel 会产出混合分隔符。
    if (dir) remotePath.value = posixJoin(remotePath.value, dir)
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
// 取列表的裸函数（与 fetchRemote 同构）：只取列表并抛错，序号/loading/recent 留在调用方。
async function fetchLocal(target) {
  return (await ListLocal(target || '/')) || []
}

async function loadLocal() {
  const seq = ++localSeq // 与 goLocal 共用序号：任何更新的本地加载都会先作废在途 goLocal，其失败回滚不再发生
  localLoading.value = true
  try {
    const items = await fetchLocal(localPath.value || '/')
    if (seq === localSeq) localItems.value = items // 已被更新请求取代则丢弃
    if (seq === localSeq) await locations.addLocalRecent(localPath.value) // 只在成功时记；条数上限由后端负责
  }
  catch (e) { if (seq === localSeq) err(e) } // 过期请求的错误不得污染更新的导航
  finally { if (seq === localSeq) localLoading.value = false }
}

// 工具栏「刷新」刷**两侧**（spec §5.1）：本地半边总能刷 —— 未连接时也要能刷出本地变化（§5.4），
// 所以工具栏按钮上的 :disabled 必须去掉（本地面板不依赖远程连接）。远程半边只在已连接时刷。
// Promise.all 并发；两侧各自的 localSeq/remoteSeq 继续负责丢弃过期响应（连点不会串）。
async function refreshAll() {
  const jobs = [loadLocal()]
  if (connected.value) jobs.push(loadRemote())
  await Promise.all(jobs)
}

// 右键「刷新」只刷**本面板**（spec §5.1）：两个面板共用这一个函数，面板由 menu.pane 带进来。
// 远程面板未连接时静默跳过（与 refreshAll 的远程半边同语义）。
async function refreshPane(pane) {
  if (pane === 'local') return loadLocal()
  if (!connected.value) return
  return loadRemote()
}

function openRemote(it) {
  if (it.name === '..') { remotePath.value = posixParentOf(remotePath.value); loadRemote(); sel.clear(remoteSelection); return }
  if (!it.isDir) return
  remotePath.value = posixJoin(remotePath.value, it.name)
  loadRemote()
  sel.clear(remoteSelection)
}
function openLocal(it) {
  if (it.name === '..') { localPath.value = localParent(localPath.value); loadLocal(); sel.clear(localSelection); return }
  if (!it.isDir) return
  localPath.value = localJoin(localPath.value, it.name)
  loadLocal()
  sel.clear(localSelection)
}

// Absolute-path helpers (POSIX-style). '/' is the root; going up stops there.
function posixJoin(base, name) {
  if (base === '/' || base === '') return '/' + name
  return base.replace(/\/+$/, '') + '/' + name
}
function posixParentOf(p) {
  if (!p || p === '/') return '/'
  const trimmed = p.replace(/\/+$/, '')
  const idx = trimmed.lastIndexOf('/')
  if (idx <= 0) return '/'
  return trimmed.slice(0, idx)
}

// item === null 表示"空白/面板级"右键（FilePane 的空白背景与 '..' 行都发 null），
// 菜单矩阵由下面的 menuHasItem 单一判据切换。这里**不动选中集合**：
// 空白右键不清空已有选中（spec §5.3 / 验收 B1），刷新也不改选中（§5.1）。
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

// 菜单矩阵的**唯一**选中判据（spec §5.3）：'..' 行按"无选中"处理（走面板级菜单）。
// 模板只用它切"空白/面板级"与"文件行"两个分支，不得在模板里散算 menu.item。
const menuHasItem = computed(() => !!(menu.value.item && menu.value.item.name !== '..'))

// 置灰的**唯一**判据（spec §5.3 / 审核 F3、FF5）：某动作是否需要远程连接 —— 判据是"动作"
// 而不是"面板"，因为本地面板的「上传文件…/上传到远程」落点同样是 remotePath。
// 模板里以 v-for 把返回值接成别名 remoteDep，统一写 `remoteDep && !connected`
// （模板内 ref 自动解包；写成 connected.value 会恒为 undefined ⇒ 置灰静默失效）。
function remoteDepFor(name) {
  return menu.value.pane === 'remote' || name === 'upload' || name === 'upload-picked'
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
  // 全选/清空已移到过滤栏的可见按钮（唯一入口），菜单 tail 里不再重复。
  const tail = [
    { name: 'rename', label: '重命名', disabled: n !== 1 },
    { name: 'remove', label: '删除 (' + n + ')' },
  ]
  if (n === 0 && pane === 'remote') return tail
  const head = pane === 'remote'
    ? [{ name: 'download', label: '下载到本地 (' + n + ')' }]
    // 第三个入口也要与右键菜单同口径（spec §5.3 / 审核 FF5/F3）：本地面板的 upload/upload-picked
    // 落点是 remotePath，未连接时置灰。`connected` 在 script 里是 ref —— 必须写 .value，
    // 写 `!connected` 恒 false（ref 对象恒真），置灰会静默失效。
    : [{ name: 'upload', label: '上传到远程 (' + n + ')', disabled: !connected.value },
       { name: 'upload-picked', label: '上传文件…', disabled: !connected.value }]
  return head.concat(tail)
}

function onPaneAction(pane, name) {
  const s = selectionFor(pane)
  // 过滤栏三按钮走的就是这条既有链路，两支必须保留（只删了菜单项，没删分派）。
  if (name === 'select-all') return sel.all(s, visibleFor(pane))
  if (name === 'invert') return sel.invert(s, visibleFor(pane))
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
    return runBatch({ direction: 'download', names, sourceDir: remotePath.value, targetDir: localPath.value || '/', sourceItems: visibleRemoteItems.value })
  }
  // 上传只可能来自本地面板（远程面板没有上传入口，spec §6.2）
  return runBatch({ direction: 'upload', names, sourceDir: localPath.value || '/', targetDir: remotePath.value, sourceItems: visibleLocalItems.value })
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
async function runBatch({ direction, names, sourceDir, targetDir, sourceItems, systemPaths, gesture = 'batch', hiddenSelected, keepSelection = false, existingNames }) {
  const sourceSelection = direction === 'upload' ? localSelection : remoteSelection
  const targetItems = direction === 'download' ? visibleLocalItems.value : visibleRemoteItems.value
  const tasks = systemPaths
    ? systemPaths.map((i) => ({ name: i.name, src: i.path, dst: (direction === 'download' ? localJoin : posixJoin)(targetDir, i.name), isDir: i.isDir }))
    : planTasks({ direction, names, sourceDir, targetDir, isDirMap: (sourceItems || []).reduce((m, it) => (m[it.name] = it.isDir, m), {}) })
  // hiddenSelected / existingNames 是可注入的覆盖项（复制到剪贴板时用）：
  // 目标目录是本次新建的空目录，既不该拿「用户当前本地目录」判冲突，
  // 也不该为隐藏选中项弹确认框。既有调用点不传 ⇒ 行为完全不变。
  const hidden = hiddenSelected ?? hiddenSelectedCount(sourceSelection, visibleFor(direction === 'upload' ? 'local' : 'remote'))
  const existing = existingNames ?? (targetItems || []).map((it) => it.name)
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
  const seq = nextTransferSeq() // 同一批共享前缀 t<seq>-，批内序号从 0 起
  // 每个队列项一个稳定 id（Task 9）：绑定首参是 id（不是 host），进度事件也按 id 关联。
  // 先把整批项挂进队列（未派发的状态也是「处理中」，取消时才能整批停下），再逐项 await。
  const batch = run.map((t, n) => ({
    id: transferID(seq, n), direction, name: t.name, src: t.src, dst: t.dst,
    size: 0, status: '处理中', startedAt: Date.now(), partPath: '', resume: false, isDir: !!t.isDir, pending: true,
  }))
  for (const rec of batch) transfers.value.push(rec)
  for (const rec of batch) {
    // 这一批已被取消：不再派发（已完成项保留，被点的 pending 行与它之后的项已在
    // applyBatchCancel 里定「取消」）。shouldDispatch 是派发判定唯一落点。
    if (!shouldDispatch(rec)) break
    rec.pending = false
    rec.pendingCancel = false
    await dispatchTransfer(rec)
    results.push(rec)
  }
  await (direction === 'download' ? loadLocal() : loadRemote())
  const s = summarize(results)
  logStore.add({ source_id: 'sftp', source_type: 'sftp', level: s.failed ? 'error' : 'info', ts: new Date().toISOString(),
    message: '批量' + direction + ' ' + results.length + ' 项：成功 ' + s.ok + ' 跳过 ' + s.skipped + ' 失败 ' + s.failed })
  const doneNames = results.filter((r) => r.status === '完成').map((r) => r.name)
  // 复制到剪贴板是只读动作（keepSelection=true）：不得清空用户的选中集（设计 §3.3）。
  if (!keepSelection) sel.remove(sourceSelection, doneNames) // 只移除成功项：失败/留在原地的项仍应保持选中（spec §6.4）
  return { ...s, results, cancelled: hasCancelled(batch) } // cancelled 看整批（含未派发的排队项），results 只看已派发项
}

// 「复制失败清单」：队列只负责 emit，落盘/剪贴板由编排层做（避免出现死按钮）。
async function copyFailures() {
  const failed = transfers.value.filter((t) => t.status === '失败')
  if (!failed.length) return
  const text = failed.map((t) => failureText(t)).join('\n')
  try { await navigator.clipboard.writeText(text) } catch (e) { err(e) }
}

// ===== 右键「复制 ▸」（设计 §3）=====
// 运行时能力：Windows true / Linux false。Linux 上「文件」项**不渲染**（不是置灰）。
const clipboardFileSupport = ref(false)

// 子菜单三项。置灰判据必须写在 script：既有 sftpMenuWiring.test.js 明令 ContextMenu
// 模板切片内不得出现 `connected.value`（模板里 ref 自动解包，写 .value 恒错）。
// 「文件」在远程面板未连接时置灰——它需要先下载，其它两项是纯字符串拼接。
const COPY_ITEMS = computed(() => {
  const items = [
    { key: 'copy-name', label: '文件名' },
    { key: 'copy-path', label: '全路径' },
  ]
  if (clipboardFileSupport.value) items.push({ key: 'copy-file', label: '文件', disabled: menu.value.pane === 'remote' && !connected.value })
  return items
})

// 文本两项：整串文本一次写入剪贴板，多行以 \n 拼接（顺序由 utils/copy 保证）。
async function copyText(pane, names, kind) {
  if (!names.length) return // 空集合不得把用户的剪贴板清成空串
  const text = copyPayload(kind, names, { pane, localPath: localPath.value, remotePath: remotePath.value })
  try {
    await navigator.clipboard.writeText(text)
    info('已复制 ' + names.length + ' 个' + (kind === 'copy-name' ? '文件名' : '全路径') + '到剪贴板')
  } catch (e) { err(e) }
}

// 「文件」：把选中的文件/目录作为文件对象写进系统剪贴板（Windows 可直接粘到资源管理器）。
// 本地面板直接写；远程必须先下载到剪贴板临时目录 —— 剪贴板里存的是**路径引用**，
// 所以临时目录本次不删除，由下次启动的陈旧清理回收（设计 §4.3）。
async function copyFilesAsObjects(pane, names) {
  if (!names.length) return
  try {
    if (pane === 'local') {
      const paths = names.map((n) => localJoin(localPath.value || '/', n))
      await CopyFilesToClipboard(paths)
      info('已复制 ' + paths.length + ' 个文件到剪贴板（可直接粘贴到文件管理器）')
      return
    }

    const tmp = await ClipboardTempDir()
    // existingNames: [] 是必须的：runBatch 默认拿**用户当前本地目录**判冲突，
    // 只要本地有同名文件就会弹冲突框并按默认策略「跳过」，结果是静默什么都不复制。
    // hiddenSelected: 0 免掉隐藏选中项的询问（隐藏项也要一并复制）；
    // keepSelection: true 让这次只读动作不摘掉用户的远程选中集。
    const s = await runBatch({
      direction: 'download',
      names,
      sourceDir: remotePath.value,
      targetDir: tmp,
      sourceItems: visibleRemoteItems.value,
      gesture: 'batch',
      hiddenSelected: 0,
      keepSelection: true,
      existingNames: [],
    })
    if (!s) { await err('复制已取消，未写入剪贴板'); return }
    // 中途取消时已完成的项也不许写：半批数据进剪贴板会让用户以为复制了全部。
    // 判据必须用整批的 s.cancelled —— 被取消的**排队项**不在 s.results 里（见 queue.js）。
    if (s.cancelled) { await err('复制已取消，未写入剪贴板'); return }

    // 临时目录是**本地**路径 ⇒ 必须用 localJoin（posixJoin 会产出反斜杠系统认不出的路径）。
    const paths = s.results.filter((r) => r.status === '完成').map((r) => localJoin(tmp, r.name))
    if (!paths.length) { await err('复制失败：没有成功的项（见传输队列）'); return }
    await CopyFilesToClipboard(paths)
    info('已复制 ' + paths.length + ' 个文件到剪贴板（可直接粘贴到文件管理器）')
    if (s.failed > 0) await err('有 ' + s.failed + ' 项复制失败（见传输队列）')
  } catch (e) {
    err(e)
  }
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
    const full = pane === 'local' ? localJoin(base, n) : posixJoin(base, n)
    const rec = { direction: pane === 'local' ? 'move' : 'download', name: n, src: full, dst: '', size: 0, status: '处理中', startedAt: Date.now() }
    transfers.value.push(rec)
    try {
      if (pane === 'local') await DeleteLocal(full)
      else await SftpRemoveRecursive(host.value, '', full)
      rec.status = '完成'
    } catch (e) {
      rec.status = '失败'
      rec.reason = String((e && e.message) || e)
      // 混合面板（§8.4 第 6 项）：本地分支保留裸 err(e)，远程分支才归口复核
      // —— 本地 IO 失败不是链路信号，混进去会白跑一次探活/重连。
      if (pane === 'local') err(e)
      else await remoteOpFailed(e)
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
      await renameLocal(localJoin(localPath.value, it.name), localJoin(localPath.value, newName))
      await loadLocal()
    } else {
      await SftpRename(host.value, '', posixJoin(remotePath.value, it.name), posixJoin(remotePath.value, newName))
      await loadRemote()
    }
  } catch (e) {
    // 混合面板（§8.4 第 7 项）：本地重命名失败是本地面板的 IO 问题，不归口；
    // 远程 SftpRename 失败才是链路信号。
    if (pane === 'local') err(e)
    else await remoteOpFailed(e)
  }
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
      await MkdirLocal(localJoin(localPath.value, name))
      await loadLocal()
    } else {
      await SftpMkdir(host.value, '', posixJoin(remotePath.value, name))
      await loadRemote()
    }
  } catch (e) {
    // 混合面板（§8.4 第 8 项）：同上，只有远程分支归口。
    if (pane === 'local') err(e)
    else await remoteOpFailed(e)
  }
}

// uploadPicked = 既有 upload() 的"选文件上传"分支：右键上传已由批量入口 runBatch 承接
// （doAction('upload')），面板动作「上传文件…」只负责弹 PickLocalFile 后上传单个文件。
async function uploadPicked() {
  let t = null
  try {
    const local = await PickLocalFile()
    if (!local) return
    const name = local.split(/[\\/]/).pop()
    // 单文件手势同样要有 id（绑定首参是 id）：复用 dispatchTransfer，才能同时拿到
    // 进度订阅、取消返回值的终态判定与失败后的重试/续传/清理锚点。
    t = { id: transferID(nextTransferSeq(), 0), direction: 'upload', name, src: local, dst: posixJoin(remotePath.value, name), size: 0, status: '处理中', startedAt: Date.now(), partPath: '', resume: false, isDir: false }
    try { t.size = await StatLocal(local) } catch (e) { t.size = 0 }
    transfers.value.push(t)
    await dispatchTransfer(t)
    await loadRemote()
  } catch (e) {
    // §8.4 第 9 项：uploadPicked 整条链路都是远程操作（PickLocalFile 失败也由这里兜底），
    // 归口复核保证"面板仍显示已连接但上传一直报错"能被自愈。
    await remoteOpFailed(e)
    if (t) {
      // 外层兜底：不得自己写终态（e-weak 结构性 pin），仍走共享终态落点。
      // t 有 id、正常已经过 dispatchTransfer/applyOutcome；这里只覆盖 dispatchTransfer
      // 之外（如 loadRemote）的异常。shared helper 会把终态/原因交给 finalizeOutcome 统一推导。
      applyOutcome(t, { ok: false, error: e })
      t.elapsed = Math.floor((Date.now() - t.startedAt) / 1000)
    }
  }
}

// 空选择防御只约束**依赖选中**的动作（spec §5.3 / 审核 FF2）：写成"除 refresh/mkdir 外一律
// return"会误伤空白菜单的 upload-picked（它天然没有选中，点了永远没反应）；把防御挪到单项选中
// 之前，又会让"右键一个未选中行"的整条行菜单变 no-op。upload 留在集合里是因为它在**本地面板
// 行菜单**中是"上传选中项"；远程面板的同名项走 uploadPicked，不依赖选中。
const SELECTION_DEPENDENT = ['download', 'upload', 'remove', 'rename']

// 右键菜单：download/upload/remove 走批量入口（右键项不在集合内时先 single()，与 §3 决策 5 一致）。
async function doAction(name) {
  const pane = menu.value.pane
  // refresh 必须最先返回（spec §5.1 / 审核 FF6）：若走下面的单项选中，多选会被悄悄收成一项，
  // 直接违反"刷新保留选中"。这里不读 item、也不改 names。
  if (name === 'refresh') { closeMenu(); return refreshPane(pane) }
  // 复制三项同样必须在这里返回（在 sel.single 之前）：它们自己算作用集合，
  // 且**不得**改变选中集——复制是只读动作（设计 §3.3）。
  if (name === 'copy-name' || name === 'copy-path' || name === 'copy-file') {
    const names = copyTargetNames(selectionFor(pane), menu.value.item, visibleFor(pane))
    closeMenu()
    return name === 'copy-file' ? copyFilesAsObjects(pane, names) : copyText(pane, names, name)
  }
  const s = selectionFor(pane)
  const it = menu.value.item
  if (it && it.name !== '..' && !sel.isSelected(s, it.name)) sel.single(s, it.name)
  const names = [...s.keys]
  if (SELECTION_DEPENDENT.includes(name) && !names.length) return
  // 空白/面板级菜单的「⬆ 上传文件…」（spec §5.3）：upload-picked 天然无选中，不在
  // SELECTION_DEPENDENT 内，所以防御之后必须**补上这条分派** —— 它的既有出口只有
  // onPaneAction（FilePane 的动作表链路），右键菜单走的是 doAction，缺分派就成了死按钮
  // （先 closeMenu() 再"什么都没发生"）。形态与上面的 refresh 分支同构（复核 Important）。
  if (name === 'upload-picked') { closeMenu(); return uploadPicked() }
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
    return runBatch({ direction: 'upload', names, sourceDir: localPath.value || '/', targetDir: remotePath.value, sourceItems: visibleLocalItems.value, gesture })
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
  // Shift+↑/↓：以 anchor 为起点、cursor 为游标扩展区间（Ctrl+A 后可从末尾逐格收窄）。
  else if (action === 'extend-up') { ev.preventDefault(); sel.extend(s, vis, -1) }
  else if (action === 'extend-down') { ev.preventDefault(); sel.extend(s, vis, 1) }
  else if (action === 'escape') { sel.clear(s) }
}

// ===== 拖拽：拖起 =====
function onDragStart(pane, { item, event }) {
  const s = selectionFor(pane)
  if (!sel.isSelected(s, item.name)) sel.single(s, item.name)
  event.dataTransfer.effectAllowed = 'copyMove'
  event.dataTransfer.setData(DRAG_MIME, payloadFor(pane, [...s.keys]))
}

// ===== 路径①②：面板互拖（落到面板空白处 = 投递到对方当前目录）=====
async function onPaneDrop(targetPane, { event }) {
  // 高亮必须随 drop 消失，否则面板会一直挂着虚线框（真机截图暴露过）。
  hoverPane.value = null
  // 落点回退记录：drop 命中的面板也记一次（真机上外部拖入的 drop 并不总会到达面板空白区，
  // 主要来源仍是 dragover，见 noteDropPane）。
  noteDropPane(targetPane)
  // 事件形状由 dnd.test.js + sftpDropWiring.test.js 双向钉住：模板必须传 { event: $event }。
  const payload = payloadFromDragEvent(event)
  if (!payload || payload.pane === targetPane) return
  const guard = canDropInto({ sourcePane: payload.pane, targetPane, item: { isDir: true }, connected: connected.value })
  if (!guard.ok) { err(guard.reason); return }
  const gesture = payload.names.length === 1 ? 'single' : 'batch'
  if (payload.pane === 'remote') {
    await runBatch({ direction: 'download', names: payload.names, sourceDir: remotePath.value, targetDir: localPath.value, sourceItems: visibleRemoteItems.value, gesture })
  } else {
    await runBatch({ direction: 'upload', names: payload.names, sourceDir: localPath.value, targetDir: remotePath.value, sourceItems: visibleLocalItems.value, gesture })
  }
}

// ===== 路径④：面板内移动到子目录行 =====
async function onMoveDrop(pane, { item, event }) {
  const payload = payloadFromDragEvent(event)
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
  //
  // §8.4 第 10 项（审核 FF2）：这段加载原本在 try **之外**（唯一的 try 在下面的移动循环里）
  // ⇒ 死链时 onMoveDrop 直接 reject，没有任何调用点接住它：无日志、无自愈、unhandled rejection。
  // 把"目标子目录列表加载 + 冲突询问"一起纳入 try。
  let run = []
  let skipped = []
  try {
    const targetItems = pane === 'local'
      ? await ListLocal(targetDir)
      : await SftpList(host.value, '', targetDir)
    const existing = (targetItems || []).map((it) => it.name)
    const prefix = base.replace(/\/+$/, '')
    const planned = payload.names.map((n) => ({ name: n, src: prefix + '/' + n, dst: targetDir + '/' + n }))
    const cls = classify(planned, existing)
    run = cls.clean
    skipped = cls.conflicts
    if (cls.conflicts.length) {
      const policy = await askConflict({ conflicts: cls.conflicts, total: planned.length, target: targetDir, hiddenSelected: 0 }, 0)
      if (policy === null) return
      const applied = applyPolicy(cls.conflicts, policy, existing)
      run = cls.clean.concat(applied.run)
      skipped = applied.skipped
    }
  } catch (e) {
    // 混合面板：本地分支保留裸 err(e)，远程分支归口复核（本地 IO 失败不是链路信号）
    if (pane === 'local') err(e)
    else await remoteOpFailed(e)
    return
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
      // §8.4 第 11 项：移动循环的失败同样按面板分派（本地 = 本地面板 IO，远程 = 链路信号）
      if (pane === 'local') err(e)
      else await remoteOpFailed(e)
    }
    rec.elapsed = Math.floor((Date.now() - rec.startedAt) / 1000)
  }
  await (pane === 'local' ? loadLocal() : loadRemote())
  // spec §6.4：失败/跳过的项还留在原处，必须保持选中便于重试——只移除确实完成的项。
  sel.remove(pane === 'local' ? localSelection : remoteSelection, done)
}

// 系统拖入的落点回退记录：{pane, ts}，由 dragover（主要）与 drop 持续刷新。
// 它与坐标（屏幕像素 vs 页面坐标，实测不可靠）互为补充，见 dnd.pickDropPane；
// 生命周期只到「被 onFilesDropped 消费」或超过 TTL，不是视觉状态（视觉是 hoverPane）。
const lastDropPane = ref(null)
// 拖入期间悬停的面板：纯视觉高亮（spec R3 的「拖入期间高亮让用户确认」）。
const hoverPane = ref(null)
// 回退记录必须与视觉高亮分开：drop 一到就要清高亮（否则面板一直挂虚线框），
// 而 Wails 的 wails:file-drop 事件要经 Go 解析真实路径、几毫秒后才到 —— 那时只能靠这份记录。
function noteDropPane(pane) { lastDropPane.value = { pane, ts: Date.now() } }
function onPaneDragOver(pane) {
  if (hoverPane.value !== pane) hoverPane.value = pane
  noteDropPane(pane) // 持续刷新：外拖的 dragover 一定会经过这里
}
// ESC 取消的拖拽不会产生 drop：靠 window 的 dragend 兜底清高亮（dragleave 不用于此，
// 它会在面板子元素之间冒泡，频繁清会造成闪烁）。
function clearDropHover() { hoverPane.value = null }

// ===== 路径③：系统文件管理器拖入 =====
// webview 侧的 dragover/drop 监听由 App.vue 在启动时注册一次（Wails JS 版 OnFileDrop），
// 这里只把「落点判定 + 投递」注册为处理器：KeepAlive 下切走标签即摘除，
// 否则「端口转发/文件同步」标签下拖入文件也会投递到 SFTP 面板。
function onFilesDropped(payload) {
  if (!payload || !payload.paths || !payload.paths.length) return
  const localEl = document.querySelector('[data-pane="local"]')
  const remoteEl = document.querySelector('[data-pane="remote"]')
  if (!localEl || !remoteEl) return
  const rects = { local: localEl.getBoundingClientRect(), remote: remoteEl.getBoundingClientRect() }
  // 坐标单位 / DPI 缩放需实测（spec R3）：坐标命中优先，否则用刚才 DOM drop 命中的面板
  // （TTL 3s，防陈旧）；两者都没有才提示无效，绝不猜一个面板乱投。
  const pane = pickDropPane({ x: payload.x, y: payload.y }, rects, lastDropPane.value)
  lastDropPane.value = null
  hoverPane.value = null
  if (!pane) { err('落点无效：请把文件拖到左侧本地或右侧远程面板上再松手'); return }
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
    await runBatch({ direction: 'upload', names: ok.map((i) => i.name), sourceDir: '', targetDir: remotePath.value, sourceItems: visibleLocalItems.value, systemPaths: ok })
    return
  }
  const base = localPath.value || '/'
  const existing = (visibleLocalItems.value || []).map((it) => it.name)
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
      transfers.value.push({ direction: 'copy', name: i.name, src: i.path, dst: localJoin(base, i.name), size: i.size, status: '跳过', elapsed: 0 })
      continue
    }
    let name = i.name
    if (hasConflict && policy === 'rename') {
      name = copyName(i.name, existing)
      existing.push(name) // 累积已占用的名字，否则两个同名源会算出同一个新名
    }
    const dst = localJoin(base, name)
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
  // presets.toml 坏掉时只发一次 startup 事件可能早于前端订阅而丢失，所以由这里补一次用户可见提示
  if (locations.presetsError) err('预设文件解析失败：' + locations.presetsError)
  // B3/B4 顺序是硬约束，且这里的 load() 失败必须被吞掉（审核 F8）：裸 await 会让下面
  // 恢复本地面板的两次调用全不执行（本地面板直接空着，用户以为坏了）。
  try { await sftpui.load() } catch (e) { err(e) }
  // 本地恢复（§6.4 B3）：记忆值优先，为空或打不开才回落到当前工作目录。
  // 探针必须用**会抛错**的 fetchLocal：loadLocal 内部吞错，判不出"这个目录打不开"（§6.5）。
  const rememberedLocal = sftpui.lastPathFor('local')
  let localTarget = ''
  if (rememberedLocal) {
    try { await fetchLocal(rememberedLocal); localTarget = rememberedLocal } catch (e) { localTarget = '' }
  }
  if (!localTarget) {
    let cwd = '/'
    try { cwd = await Cwd() } catch (e) { err(e); cwd = '/' }
    // 文案逐字取自 spec §10，级别必须是 warn（审核 F7：err() 硬编码 error，会误导验收 grep）
    if (rememberedLocal) warn('上次的本地目录 ' + rememberedLocal + ' 已不可用，已回到 ' + cwd)
    localTarget = cwd
  }
  localPath.value = localTarget
  await loadLocal()
  // 剪贴板能力查询必须放进**这个** onMounted：sftpRestoreWiring.test.js 用正则取第一个
  // onMounted 的两条顺序断言，新增第二个 onMounted 会让它们全红。不 await：不拖慢启动路径。
  ClipboardFileSupport().then((v) => { clipboardFileSupport.value = !!v }).catch(() => { clipboardFileSupport.value = false })
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
  if (!h) return
  if (connected.value) {
    // 已连接：真探活复核（spec §8.3）。**不重置路径、不重载列表** —— 每次切标签跳目录
    // 是不可接受的。probing 复用同一个去重标志：探针在飞时不并发第二次探活。
    if (probing) return
    probing = true
    try {
      // === 'lost' 是硬要求（审核 F1）：三态字符串配 !(await ...) 恒 false ⇒ 自愈永不触发。
      if ((await probeTimed(h)) === 'lost') await recoverConnection(h)
    } catch { /* 探针本身失败不改变现状（未知不是失效） */ }
    finally { probing = false }
    return
  }
  try {
    const ok = await SftpConnected(h)
    if (host.value !== h || connected.value) return // await 期间状态已变
    if (!ok) { connected.value = false; return }
    // §6.4 B5：与 connect() 共用同一落点决策（不再无条件 SftpHome）。
    // 否则「切主机再切回」（后端粘性 Connected 仍为 true，走这里恢复）会落到 home，
    // 与「回到该主机最后路径」直接矛盾。
    let home = '/'
    try { home = await SftpHome(h) } catch { home = '/' }
    if (host.value !== h) return
    const target = resolveRemoteTarget({
      pendingPath: pendingPath.value,
      pendingHome: pendingHome.value,
      remembered: sftpui.lastPathFor('remote', h),
      home,
    })
    pendingPath.value = ''
    pendingHome.value = false
    connected.value = true
    remotePath.value = target.path
    // §6.5：只有"来自记忆"才回落 home（判定与 connect 逐字同构）
    if (target.fromMemory && target.path !== home) {
      try {
        await fetchRemote(target.path)
      } catch (e) {
        // 第二发（回落 home）单独兜错：这里是 syncConnection 的外层静默 catch（状态查询语义）
        // 之外的用户可见失败，落进外层就**零日志**（§6.5 明写要记日志），还会跳过 loadRemote()
        // 而把 remotePath 改成 home、items 仍是上一个主机的旧值。
        try {
          await fetchRemote(home)
        } catch (e2) {
          // 回落本身也失败（§6.5：不做二次回落）：1) 必须记原文（不许落进外层静默 catch）；
          // 2) connected/loading 与 connect 的失败路径同语义（不谎报已连接，也不再跑 loadRemote）；
          // 3) 不提交 home —— 列不出来的目录不该成为 remotePath（保持与 items 同源）。
          err(e2)
          if (host.value === h) connected.value = false
          return
        }
        remotePath.value = home
        warn('上次的远程目录 ' + target.path + ' 已不可用，已回到 ' + home)
      }
    }
    await loadRemote()
  } catch { /* 状态查询失败时保持现状 */ }
}

// 日志区高度：仅会话内记忆（KeepAlive 缓存期间保留，窗口重开回到默认），不落设置文件。
const logHeight = ref(LOG_DEFAULT_H)
let splitterStart = null
function onSplitterDown(ev) {
  if (ev.button !== 0) return // 只接左键：右键/中键拖动不该进入 resize（也不该吞掉 contextmenu）
  splitterStart = { y: ev.clientY, h: logHeight.value }
  window.addEventListener('mousemove', onSplitterMove)
  window.addEventListener('mouseup', onSplitterUp)
  // 窗口外松开鼠标时 mouseup 落不到 window 上：blur 兜底摘监听，否则移回窗口后会
  // 不按键地跟着光标伸缩日志区，直到下一次 mouseup 或切标签（I1）。
  window.addEventListener('blur', onSplitterUp)
  ev.preventDefault()
}
function onSplitterMove(ev) {
  if (!splitterStart) return
  logHeight.value = clampLogHeight(splitterStart.h + (splitterStart.y - ev.clientY))
}
// 幂等：拖拽中切标签/丢 mouseup/窗口失焦时都要能安全重复调用，避免 window 上残留监听。
function onSplitterUp() {
  splitterStart = null
  window.removeEventListener('mousemove', onSplitterMove)
  window.removeEventListener('mouseup', onSplitterUp)
  window.removeEventListener('blur', onSplitterUp)
}
function onSplitterKey(ev) {
  // Shift+↑/↓ 名义上是「文件选择区间扩展」的键位（Task 2），但焦点在分隔条上时
  // keys.js 的 isEditableTarget 会因 [role="separator"] 守卫让 actionFor 返回 null，
  // 全局永远收不到这个动作 —— 所以这里是完全 no-op（不是「让给全局」），
  // 目的是避免 resize 与区间扩展在同一次按键上互相打架（㉕）。
  if (ev.shiftKey) return
  if (ev.key === 'ArrowUp') { logHeight.value = clampLogHeight(logHeight.value + 16); ev.preventDefault() }
  else if (ev.key === 'ArrowDown') { logHeight.value = clampLogHeight(logHeight.value - 16); ev.preventDefault() }
}

// KeepAlive 生命周期：切入时挂菜单监听、重启时钟并同步连接状态；切出时停时钟。
// 必须成对挂摘：SftpView 被 <KeepAlive> 缓存，setup 只跑一次（App.vue:57-61）。
// 在 setup 顶层注册会让「端口转发/文件同步」标签下按 Delete 弹出 SFTP 的删除确认框，
// 因此注册写进既有的 onActivated（那里已经在挂 click 监听）。
onActivated(() => {
  window.addEventListener('click', outsideClick)
  window.addEventListener('keydown', onKeydown)
  window.addEventListener('dragend', clearDropHover)
  setSystemDropHandler(onFilesDropped)
  // 进度订阅同样成对挂摘：切走标签后仍在的订阅会把帧落到已离开的视图上。
  offProgress = EventsOn(TRANSFER_PROGRESS_EVENT, onTransferProgress)
  startClock()
  syncConnection()
})
onDeactivated(() => {
  window.removeEventListener('click', outsideClick)
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('dragend', clearDropHover)
  setSystemDropHandler(null)
  if (offProgress) { offProgress(); offProgress = null }
  stopClock()
  onSplitterUp() // 拖拽中切标签：不摘会残留 window mousemove/mouseup
})
onUnmounted(() => {
  window.removeEventListener('click', outsideClick)
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('dragend', clearDropHover)
  setSystemDropHandler(null)
  if (offProgress) { offProgress(); offProgress = null }
  stopClock()
  onSplitterUp()
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
      <!-- 工具栏刷新刷两侧，本地半边未连接也能刷 ⇒ 这里**刻意不带 :disabled**（spec §5.1/§5.4） -->
      <button @click="refreshAll">🔄 刷新</button>
      <label class="hidden-toggle">
        <input type="checkbox" v-model="showAll" /> 显示隐藏文件
      </label>
    </div>
    <div class="panes">
      <!-- @dragenter.prevent 不是装饰：WebKitGTK 上只有 @dragover.prevent 时，面板级 drop 根本不会派发
           （Linux 真机实测：面板内行拖拽能成、跨面板拖拽完全无反应）；Chromium/WebView2 两者都行。 -->
      <div class="pane-wrap" data-pane="local" :class="{ 'drop-hover': hoverPane === 'local' }" @dragenter.prevent @dragover.prevent="onPaneDragOver('local')" @drop.prevent="onPaneDrop('local', { event: $event })">
        <FilePane title="本地" pane="local" host="" :path="localPath || '/'" :items="visibleLocalItems" :sel-keys="[...localSelection.keys]" :anchor="localSelection.anchor"
          :show-hidden="showAll" :loading="localLoading" :actions="actionsFor('local')" :hidden-selected="hiddenFor('local')"
          :presets="locations.presetsForPane('local', '')" :disks="locations.disksForPane('local')"
          :bookmarks="locations.bookmarksForPane('local', '')" :recents="locations.recentsForPane('local', '')" :bookmarked="bookmarkedFor('local')"
          :sort-key="sftpui.sort.local.key" :sort-dir="sftpui.sort.local.dir"
          @select="onSelect('local', $event)" @open="openLocal" @action="onPaneAction('local', $event)"
          @pick-position="pickPosition('local', $event)" @toggle-bookmark="toggleBookmark('local')" @search="search.pane = 'local'; search.visible = true"
          @clear="sel.clear(localSelection)"
          @navigate="onNavigate('local', $event)"
          @sort="onSort('local', $event)"
          @context="showMenu('local', $event)" @visible="localVisible = $event" @focus="focusedPane = 'local'"
          @dragstart="onDragStart('local', $event)" @dropon="onMoveDrop('local', $event)" />
      </div>
      <div class="pane-wrap" data-pane="remote" :class="{ 'drop-hover': hoverPane === 'remote' }" @dragenter.prevent @dragover.prevent="onPaneDragOver('remote')" @drop.prevent="onPaneDrop('remote', { event: $event })">
        <FilePane title="远程" pane="remote" :host="host" :path="remotePath" :items="visibleRemoteItems" :sel-keys="[...remoteSelection.keys]" :anchor="remoteSelection.anchor"
          :show-hidden="showAll" :loading="remoteLoading" :actions="actionsFor('remote')" :hidden-selected="hiddenFor('remote')"
          :presets="locations.presetsForPane('remote', host)"
          :bookmarks="locations.bookmarksForPane('remote', host)" :recents="locations.recentsForPane('remote', host)" :bookmarked="bookmarkedFor('remote')"
          :sort-key="sftpui.sort.remote.key" :sort-dir="sftpui.sort.remote.dir"
          @select="onSelect('remote', $event)" @open="openRemote" @action="onPaneAction('remote', $event)"
          @pick-position="pickPosition('remote', $event)" @toggle-bookmark="toggleBookmark('remote')" @search="search.pane = 'remote'; search.visible = true"
          @clear="sel.clear(remoteSelection)"
          @navigate="onNavigate('remote', $event)"
          @sort="onSort('remote', $event)"
          @context="showMenu('remote', $event)" @visible="remoteVisible = $event" @focus="focusedPane = 'remote'"
          @dragstart="onDragStart('remote', $event)" @dropon="onMoveDrop('remote', $event)" />
      </div>
    </div>
    <TransferQueue :transfers="transfers" :now="now" @copy-failures="copyFailures"
      @cancel="cancelTransfer" @retry="retryItem" @resume="resumeItem" @clean="cleanItem" />
    <div class="splitter" data-role="log-splitter" role="separator" aria-orientation="horizontal"
      tabindex="0" title="拖动调整日志高度" @mousedown="onSplitterDown" @keydown="onSplitterKey"></div>
    <div class="logpane ui-panel" :style="{ height: logHeight + 'px' }"><LogPanel :source-types="['sftp', 'system']" /></div>

    <!-- 菜单矩阵（spec §5.3）：只以 menuHasItem 一个判据切"空白/面板级"与"文件行"两支；
         每项的置灰统一写 `remoteDep && !connected` —— remoteDep 是 remoteDepFor(name) 经
         v-for 接进来的别名，保证模板里只有一种置灰表达式（模板内 ref 自动解包，不能写 .value）。 -->
    <ContextMenu :visible="menu.visible" :x="menu.x" :y="menu.y" @close="closeMenu">
      <template v-if="!menuHasItem">
        <!-- 空白/面板级（列头与过滤栏背景同此）：🔄 刷新 · 📁 新建文件夹 · ⬆ 上传文件… -->
        <template v-for="remoteDep in [remoteDepFor('refresh')]" :key="'blank-pane'">
          <button :disabled="remoteDep && !connected" @click="doAction('refresh')"><span class="ic">🔄</span>刷新</button>
          <button :disabled="remoteDep && !connected" @click="doAction('mkdir')"><span class="ic">📁</span>新建文件夹</button>
        </template>
        <template v-for="remoteDep in [remoteDepFor('upload-picked')]" :key="'blank-picked'">
          <button :disabled="remoteDep && !connected" @click="doAction('upload-picked')"><span class="ic">⬆</span>上传文件…</button>
        </template>
      </template>
      <template v-else>
        <!-- 远程面板 · 文件行：⬇ 下载… · ⬆ 上传… · ✏ 重命名 · 🗑 删除 · ── · 🔄 刷新（不含 mkdir） -->
        <template v-if="menu.pane === 'remote'">
          <template v-for="remoteDep in [remoteDepFor('download')]" :key="'row-remote'">
            <button :disabled="remoteDep && !connected" @click="doAction('download')"><span class="ic">⬇</span>下载…</button>
            <button :disabled="remoteDep && !connected" @click="doAction('upload')"><span class="ic">⬆</span>上传…</button>
            <MenuSubmenu label="复制" :items="COPY_ITEMS" @pick="doAction($event)" />
            <button :disabled="remoteDep && !connected" @click="doAction('rename')"><span class="ic">✏</span>重命名</button>
            <button :disabled="remoteDep && !connected" @click="doAction('remove')"><span class="ic">🗑</span>删除</button>
            <button class="sep" :disabled="remoteDep && !connected" @click="doAction('refresh')"><span class="ic">🔄</span>刷新</button>
          </template>
        </template>
        <!-- 本地面板 · 文件行：⬆ 上传到远程 · ✏ 重命名 · 🗑 删除 · ── · 🔄 刷新（不含 mkdir） -->
        <template v-else>
          <template v-for="remoteDep in [remoteDepFor('upload')]" :key="'row-local-remote'">
            <button :disabled="remoteDep && !connected" @click="doAction('upload')"><span class="ic">⬆</span>上传到远程</button>
          </template>
          <MenuSubmenu label="复制" :items="COPY_ITEMS" @pick="doAction($event)" />
          <template v-for="remoteDep in [remoteDepFor('rename')]" :key="'row-local'">
            <button :disabled="remoteDep && !connected" @click="doAction('rename')"><span class="ic">✏</span>重命名</button>
            <button :disabled="remoteDep && !connected" @click="doAction('remove')"><span class="ic">🗑</span>删除</button>
            <button class="sep" :disabled="remoteDep && !connected" @click="doAction('refresh')"><span class="ic">🔄</span>刷新</button>
          </template>
        </template>
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
    <!-- @failed：深搜是远程操作，失败必须进连接失效复核的同一个归口（spec §8.4 第 13 项） -->
    <SearchOverlay :visible="search.visible" :scope="search.pane" :host="host" :root="search.pane === 'local' ? localPath : remotePath"
      @close="search.visible = false" @open-hit="openHit" @failed="remoteOpFailed" />
  </div>
</template>

<style scoped>
.sftp { display: flex; flex-direction: column; height: 100%; gap: 8px; }
.toolbar { display: flex; gap: 8px; align-items: center; }
.panes { display: flex; gap: 8px; flex: 1; min-height: 0; }
/* 放置区容器：承接 .panes 的伸缩；data-pane 同时是系统拖入命中测试的锚点 */
.pane-wrap { flex: 1; display: flex; min-width: 0; }
/* 系统拖入悬停提示（spec R3 的「拖入期间高亮，让用户确认落点」）：落点回退靠它可见 */
.pane-wrap.drop-hover { outline: 2px dashed var(--accent); outline-offset: -2px; border-radius: 4px; }
.splitter { height: 7px; flex: none; cursor: row-resize; background: var(--bg-elev); border-top: 1px solid var(--border); border-bottom: 1px solid var(--border); }
.logpane { flex: none; padding: 12px; overflow: auto; }
.hidden-toggle { display: flex; align-items: center; gap: 4px; font-size: var(--fs-12); color: var(--text-dim); }
</style>