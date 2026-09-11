<script setup>
import { computed } from 'vue'
import { conflictActionLabel } from '../utils/sync'

const props = defineProps({
  visible: Boolean,
  conflicts: { type: Array, default: () => [] },
  ruleName: { type: String, default: '' },
})
const emit = defineEmits(['close', 'resolve', 'resolveAll'])

const ACTIONS = ['keep_local', 'take_remote', 'save_as']

// S10：批量按钮会给**每个**冲突各发一次 IPC（N 条 = N 次 ResolveSyncConflict 往返），
// 大队列会把 UI 卡住且毫无提示。超过这个上限就禁用批量，要求分批或逐条处理。
// 50 的取法：一次批量 IPC 串行往返约几十毫秒量级，50 条仍在可接受范围，
// 再大就明显卡顿；宁可让用户分批，也不静默卡死。
const BATCH_LIMIT = 50
const batchDisabled = computed(() => !props.conflicts.length || props.conflicts.length > BATCH_LIMIT)

function fmtSize(n) {
  const v = Number(n)
  return Number.isFinite(v) ? String(v) : '-'
}
</script>

<template>
  <div v-if="visible" class="ui-overlay" @click.self="emit('close')">
    <div class="dialog" role="dialog">
      <div class="dtitle">冲突裁决 · {{ ruleName }}</div>
      <p class="dmsg">
        这些文件在远端与本地都被改过，系统不会自动覆盖。逐条选择如何处理；
        「保留本地」以本地现状为基线、「用远端覆盖」会重新下载、「另存远端版本」把远端那版写成兄弟文件而不动本地。
      </p>

      <div class="bulk">
        <span>批量（每条各发一次 IPC，当前 {{ conflicts.length }} 条 = {{ conflicts.length }} 次）：</span>
        <button v-for="a in ACTIONS" :key="'all-' + a" class="ghost"
          :disabled="batchDisabled" @click="emit('resolveAll', a)">
          全部{{ conflictActionLabel(a) }}
        </button>
        <span v-if="conflicts.length > BATCH_LIMIT" class="over">
          超过 {{ BATCH_LIMIT }} 条已禁用批量，请逐条裁决或分批处理
        </span>
      </div>

      <div class="rows">
        <div v-for="c in conflicts" :key="c.rel_path" class="row">
          <div class="rel" :title="c.rel_path">{{ c.rel_path }}</div>
          <div class="sides">
            <span>远端 {{ fmtSize(c.remote_size) }} · {{ c.remote_mtime || '-' }}</span>
            <span>本地 {{ fmtSize(c.local_size) }} · {{ c.local_mtime || '-' }}</span>
          </div>
          <div class="btns">
            <button v-for="a in ACTIONS" :key="c.rel_path + '-' + a" class="ghost"
              @click="emit('resolve', { rel: c.rel_path, action: a })">
              {{ conflictActionLabel(a) }}
            </button>
          </div>
        </div>
        <p v-if="!conflicts.length" class="empty">当前没有待裁决的冲突</p>
      </div>

      <div class="dbtns">
        <button @click="emit('close')">关闭</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 20px; width: 720px; max-width: 92vw; max-height: 80vh; overflow: auto; box-shadow: 0 8px 32px rgba(0,0,0,0.5); }
.dtitle { font-weight: 600; color: var(--text); margin-bottom: 8px; }
.dmsg { color: var(--text-dim); margin: 0 0 12px; font-size: var(--fs-13); line-height: 1.5; }
.bulk { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-bottom: 10px; color: var(--text-dim); font-size: var(--fs-12); }
.over { color: var(--warning); }
.rows { display: flex; flex-direction: column; gap: 8px; }
.row { border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; }
.rel { color: var(--text); word-break: break-all; margin-bottom: 4px; }
.sides { display: flex; gap: 14px; flex-wrap: wrap; color: var(--text-dim); font-size: var(--fs-12); margin-bottom: 6px; }
.btns { display: flex; gap: 6px; flex-wrap: wrap; }
.ghost { background: transparent; }
.empty { color: var(--text-faint); }
.dbtns { display: flex; justify-content: flex-end; margin-top: 12px; }
</style>
