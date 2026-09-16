<script setup>
import { ref, watch, nextTick } from 'vue'
import { POLICY_SKIP, POLICY_OVERWRITE, POLICY_RENAME } from '../utils/batch'

const props = defineProps({
  visible: Boolean,
  target: { type: String, default: '' },
  total: { type: Number, default: 0 },
  conflicts: { type: Array, default: () => [] },
  hiddenSelected: { type: Number, default: 0 },
})
const emit = defineEmits(['confirm', 'cancel'])
const policy = ref(POLICY_SKIP)
const overlay = ref(null)
const MAX_LIST = 8

watch(() => props.visible, async (v) => {
  if (!v) return
  policy.value = POLICY_SKIP
  // 遮罩没有 tabindex 时键盘事件不会落在它上面，@keyup.esc 形同虚设。
  // 打开后把焦点移到遮罩（tabindex="-1" 使其可编程聚焦），Esc 即可关闭；
  // 焦点落在内部单选/按钮上时，keyup 也会冒泡到遮罩，同样生效。
  await nextTick()
  if (overlay.value) overlay.value.focus()
})

function shown() { return props.conflicts.slice(0, MAX_LIST) }
function more() { return Math.max(0, props.conflicts.length - MAX_LIST) }
</script>

<template>
  <div v-if="visible" ref="overlay" tabindex="-1" class="ui-overlay" @click.self="emit('cancel')" @keyup.esc="emit('cancel')">
    <div class="dialog" role="dialog" aria-label="投递确认">
      <div class="dtitle">{{ conflicts.length ? '目标已存在同名项' : '确认投递' }}</div>
      <p class="dmsg">
        目标 {{ target }}：本次 {{ total }} 项<template v-if="conflicts.length">，其中 {{ conflicts.length }} 项已存在</template>
        <template v-if="hiddenSelected">（另含 {{ hiddenSelected }} 项被过滤隐藏，不可见但会一起操作）</template>
      </p>
      <p v-if="conflicts.length" class="dlist">{{ shown().join(' · ') }}<template v-if="more()"> 等 {{ more() }} 项</template></p>
      <template v-if="conflicts.length">
        <label class="opt" :class="{ sel: policy === POLICY_OVERWRITE }">
          <input type="radio" :value="POLICY_OVERWRITE" v-model="policy" /> 覆盖全部
        </label>
        <label class="opt" :class="{ sel: policy === POLICY_SKIP }">
          <input type="radio" :value="POLICY_SKIP" v-model="policy" /> 跳过已存在项
        </label>
        <label class="opt" :class="{ sel: policy === POLICY_RENAME }">
          <input type="radio" :value="POLICY_RENAME" v-model="policy" /> 另存副本
        </label>
      </template>
      <div class="dbtns">
        <button @click="emit('cancel')">取消本次批量</button>
        <button class="primary" @click="emit('confirm', conflicts.length ? policy : POLICY_SKIP)">开始</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 遮罩靠 tabindex="-1" 接收 Esc；焦点环画在整个遮罩上没有意义，隐掉 */
.ui-overlay:focus { outline: none; }
.dialog { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 20px; width: 440px; text-align: left; }
.dtitle { font-weight: 600; color: var(--text); }
.dmsg { color: var(--text-dim); font-size: var(--fs-13); }
.dlist { color: var(--text-faint); font-size: var(--fs-12); word-break: break-all; }
.opt { display: flex; gap: 6px; align-items: center; padding: 6px; border: 1px solid var(--border); border-radius: 5px; margin-top: 6px; color: var(--text); font-size: var(--fs-13); }
.opt.sel { border-color: var(--accent); background: var(--surface); }
.opt input { width: auto; }
.dbtns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
</style>
