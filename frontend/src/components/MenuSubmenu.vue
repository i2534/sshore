<script setup>
// 右键菜单的「复制 ▸」子菜单：悬停或点击父项展开，点子项派发 pick 后收起。
// 定位算法在 utils/menu.js（纯函数、可真单测）；本组件只负责测量与接线。
import { ref, nextTick, onUnmounted } from 'vue'
import { submenuPlacement } from '../utils/menu'

defineProps({
  label: { type: String, required: true },
  items: { type: Array, default: () => [] }, // [{ key, label, disabled? }]
})
const emit = defineEmits(['pick'])

const OPEN_DELAY = 120
const CLOSE_DELAY = 160

const open = ref(false)
const head = ref(null)
const panel = ref(null)
const style = ref({})

let openTimer = null
let closeTimer = null

function clearTimers() {
  if (openTimer !== null) { clearTimeout(openTimer); openTimer = null }
  if (closeTimer !== null) { clearTimeout(closeTimer); closeTimer = null }
}

async function openNow() {
  clearTimers()
  open.value = true
  // v-if="open" 让面板在置 true 的同一 tick 内还不存在 —— 必须先等渲染再测量，
  // 否则 panel.value 为 null、getBoundingClientRect 直接抛 TypeError。这是打开
  // 子菜单的唯一路径，抛错等于功能不可用（设计 §3.2）。
  await nextTick()
  place()
}

function place() {
  if (!panel.value) return // ref 空兜底：测量失败就保持默认坐标，绝不抛错
  const anchor = head.value.getBoundingClientRect()
  const size = panel.value.getBoundingClientRect()
  const got = submenuPlacement(anchor, size, { width: window.innerWidth, height: window.innerHeight })
  style.value = { left: got.left + 'px', top: got.top + 'px', maxHeight: got.maxHeight + 'px' }
}

function scheduleOpen() {
  clearTimers()
  openTimer = setTimeout(() => { openTimer = null; openNow() }, OPEN_DELAY)
}

function scheduleClose() {
  clearTimers()
  closeTimer = setTimeout(() => { closeTimer = null; open.value = false }, CLOSE_DELAY)
}

function toggle() {
  if (!open.value) return openNow()
  clearTimers()
  open.value = false
}

function pick(it) {
  if (it.disabled) return
  open.value = false
  emit('pick', it.key)
}

onUnmounted(() => { clearTimers() })
</script>

<template>
  <!-- 鼠标进出挂在根元素上：覆盖父项与子面板两块，否则鼠标从父项移向右侧
       子面板的途中就会触发父项的 mouseleave 而提前收起，子项永远点不到。 -->
  <div class="menusub" @mouseenter="scheduleOpen" @mouseleave="scheduleClose">
    <button ref="head" class="menusub-head" @click="toggle">
      <span class="ic">📋</span>{{ label }}<span class="arrow">▸</span>
    </button>
    <div v-if="open" ref="panel" class="menusub-panel" :style="style">
      <button v-for="it in items" :key="it.key" :disabled="it.disabled" @click="pick(it)">{{ it.label }}</button>
    </div>
  </div>
</template>

<style scoped>
.menusub { position: relative; }
/* 父项与子项的按钮形态沿用 ContextMenu.vue 的菜单项规则 —— 必须显式写
   justify-content: flex-start：全局 `button { justify-content: center }` 会把
   「图标 + 文字」整行居中、图标列错位。 */
.menusub-head { display: flex; justify-content: flex-start; width: 100%; height: auto; align-items: center; gap: 8px; text-align: left; background: none; border: none; padding: 6px 10px; color: var(--text); cursor: pointer; }
.menusub-head:hover { background: var(--surface-hover); }
.arrow { margin-left: auto; }
/* z-index 比 ContextMenu(1000) 高一档：子面板要压在菜单之上。 */
.menusub-panel { position: fixed; z-index: 1001; min-width: 140px; overflow: auto; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 6px; padding: 4px; box-shadow: 0 4px 16px rgba(0,0,0,0.4); }
.menusub-panel button { display: flex; justify-content: flex-start; width: 100%; height: auto; align-items: center; gap: 8px; text-align: left; background: none; border: none; padding: 6px 10px; color: var(--text); cursor: pointer; }
.menusub-panel button:hover { background: var(--surface-hover); }
.menusub-panel button:disabled { color: var(--text-faint); cursor: default; }
.menusub-panel button:disabled:hover { background: none; }
</style>
