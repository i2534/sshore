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
.menu :deep(button) { display: flex; width: 100%; align-items: center; gap: 8px; text-align: left; background: none; border: none; padding: 6px 10px; color: var(--text); cursor: pointer; }
.menu :deep(button:hover) { background: var(--surface-hover); }
.menu :deep(button.sep) { border-top: 1px solid var(--border); margin-top: 2px; }
.menu :deep(.ic) { display: inline-block; min-width: 16px; text-align: center; flex-shrink: 0; }
</style>
