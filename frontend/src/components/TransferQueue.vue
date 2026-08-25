<script setup>
defineProps({ transfers: { type: Array, default: () => [] } })
</script>

<template>
  <div class="queue">
    <div v-for="(t, i) in transfers" :key="i" class="t">
      {{ t.name }} — {{ t.status }}
    </div>
  </div>
</template>

<style scoped>
.queue { border-top: 1px solid var(--border); max-height: 120px; overflow: auto; font-family: monospace; font-size: 12px; color: var(--text-dim); }
.t { padding: 2px 8px; }
</style>
