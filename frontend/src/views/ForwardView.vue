<script setup>
import LogPanel from '../components/LogPanel.vue'
</script>

<template>
  <div class="fwd">
    <div class="panel">
      <h3>转发规则</h3>
    </div>
    <div class="panel"><LogPanel /></div>
  </div>
</template>

<style scoped>
.fwd { display: flex; height: 100%; gap: 12px; }
.panel { flex: 1; border: 1px solid #ddd; padding: 8px; overflow: auto; }
</style>
