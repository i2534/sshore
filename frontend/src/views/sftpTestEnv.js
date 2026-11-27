// 测试专用：为 SftpView.vue 的「最小挂载」构建注入环境。
// 真实部分：vue 响应式 API、三个 Pinia store 工厂、全部纯函数 utils（与生产同源）。
// 替身部分：IPC 绑定（调用方 vi.mock 后把命名空间传进来）、EventsOn、生命周期钩子、window/document。
// 生命周期钩子收集到 hooks 里，测试可手动触发 onMounted 回调来验证启动恢复顺序（V2）。
import { ref, reactive, computed } from 'vue'
import { useLogStore } from '../stores/logs'
import { useLocationsStore } from '../stores/locations'
import { useSftpUIStore } from '../stores/sftpui'
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
import { resolvePathInput, REMOTE_HOME } from '../utils/pathbar'
import { resolveRemoteTarget } from '../utils/remotepath'

export function makeSftpEnv({ App, EventsOn, hooks, win, doc, overrides = {} }) {
  const h = hooks || { mounted: [], unmounted: [], activated: [], deactivated: [] }
  return {
    // vue 响应式用真的（不依赖 DOM）
    ref, reactive, computed,
    // 生命周期替身：收集回调，测试手动触发
    onMounted: (fn) => h.mounted.push(fn),
    onUnmounted: (fn) => h.unmounted.push(fn),
    onActivated: (fn) => h.activated.push(fn),
    onDeactivated: (fn) => h.deactivated.push(fn),
    // IPC 绑定（已 vi.mock）与运行时事件
    ...App,
    EventsOn,
    // 真实 store 工厂（绑定已被 mock，active pinia 由测试 beforeEach 设置）
    useLogStore, useLocationsStore, useSftpUIStore,
    // 真实纯函数 utils
    sel, copyTargetNames, copyPayload, localJoin, localParent, joinRel, splitPath,
    actionFor, clampLogHeight, LOG_DEFAULT_H,
    planTasks, classify, applyPolicy, needsConfirm, summarize, copyName, nextTransferSeq, transferID,
    failureText, isInternalTempName, applyProgress, TRANSFER_PROGRESS_EVENT,
    applyOutcome, applyCancelResult, applyBatchCancel, shouldDispatch, hasCancelled,
    payloadFor, payloadFromDragEvent, DRAG_MIME, pickDropPane, canDropInto,
    setSystemDropHandler, resolvePathInput, REMOTE_HOME, resolveRemoteTarget,
    // DOM 替身（showMenu 读 innerWidth/innerHeight；onFilesDropped 读 querySelector）
    window: win,
    document: doc,
    ...overrides,
  }
}