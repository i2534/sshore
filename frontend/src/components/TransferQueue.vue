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
.queue { border-top: 1px solid #ddd; max-height: 120px; overflow: auto; font-family: monospace; font-size: 12px; }
.t { padding: 2px 8px; }
</style>
