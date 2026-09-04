<script setup>
import { ref, watch } from 'vue'

const props = defineProps({
  visible: Boolean,
  mode: { type: String, default: 'confirm' }, // confirm | prompt
  title: { type: String, default: '' },
  message: { type: String, default: '' },
  placeholder: { type: String, default: '' },
  initial: { type: String, default: '' },
})
const emit = defineEmits(['ok', 'cancel'])

const value = ref('')
watch(() => props.visible, (v) => { if (v) value.value = props.initial || '' })

function confirm() {
  if (props.mode === 'prompt' && !value.value.trim()) return
  emit('ok', value.value)
}
</script>

<template>
  <div v-if="visible" class="ui-overlay" @click.self="emit('cancel')">
    <div class="dialog" role="dialog">
      <div class="dtitle">{{ title }}</div>
      <p class="dmsg">{{ message }}</p>
      <input
        v-if="mode === 'prompt'"
        v-model="value"
        class="dinput"
        :placeholder="placeholder"
        @keyup.enter="confirm"
        @keyup.esc="emit('cancel')"
      />
      <div class="dbtns">
        <button class="primary" @click="confirm">{{ mode === 'prompt' ? '确定' : '确认' }}</button>
        <button @click="emit('cancel')">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 20px; width: 360px; box-shadow: 0 8px 32px rgba(0,0,0,0.5); }
.dtitle { font-weight: 600; color: var(--text); margin-bottom: 8px; }
.dmsg { color: var(--text-dim); margin: 0 0 12px; font-size: var(--fs-14); }
.dinput { width: 100%; box-sizing: border-box; padding: 8px 10px; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; color: var(--text); margin-bottom: 12px; }
.dinput:focus { border-color: var(--accent); outline: none; }
.dbtns { display: flex; gap: 8px; justify-content: flex-end; }
.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
.primary:hover { background: var(--accent-hover); }
</style>
