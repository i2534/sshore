<script setup>
// Small context-menu overlay. Positioned absolutely; closes on outside click.
defineProps({ visible: Boolean, x: Number, y: Number })
defineEmits(['action', 'close'])
</script>

<template>
  <div v-if="visible" class="menu" :style="{ left: x + 'px', top: y + 'px' }" @contextmenu.prevent @click.stop>
    <slot />
  </div>
</template>

<style scoped>
.menu { position: fixed; z-index: 1000; min-width: 160px; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 6px; padding: 4px; box-shadow: 0 4px 16px rgba(0,0,0,0.4); }
/* 必须显式声明 justify-content：全局 `button, select { justify-content: center }`（style.css:111，(0,0,1)）
   会作用于菜单项；本规则 (0,2,1) 只覆盖了 display，未覆盖 justify-content，
   不补 flex-start 会让「图标 + 文字」整行居中、图标列错位。 */
.menu :deep(button) { display: flex; justify-content: flex-start; width: 100%; height: auto; align-items: center; gap: 8px; text-align: left; background: none; border: none; padding: 6px 10px; color: var(--text); cursor: pointer; }
.menu :deep(button:hover) { background: var(--surface-hover); }
/* 禁用态（T10 / spec §5.4）：置灰 + 不可点。T12 的菜单矩阵用 `remoteDep && !connected`
   把远依赖项置灰；没有这两条规则时 disabled 按钮看起来仍与可点项一模一样。
   `cursor: default` 与 `:hover { background: none }` 都必须显式写：既有 `button:hover`
   会把鼠标划过的禁用项照样点亮，与「灰＝不可点」的语义直接冲突。 */
.menu :deep(button:disabled) { color: var(--text-faint); cursor: default; }
.menu :deep(button:disabled:hover) { background: none; }
.menu :deep(button.sep) { border-top: 1px solid var(--border); margin-top: 2px; }
.menu :deep(.ic) { display: inline-block; width: 20px; text-align: center; flex-shrink: 0; }
</style>
