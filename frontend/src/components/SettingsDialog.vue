<script setup>
import { ref, watch } from 'vue'
import { GetAppInfo } from '../../wailsjs/go/main/App'
import { useSettingsStore, THEMES, FONT_SCALES, LATIN_FONTS, CJK_FONTS } from '../stores/settings'

const props = defineProps({ visible: Boolean })
const emit = defineEmits(['close'])
const store = useSettingsStore()
const saving = ref(false)
const err = ref('')
const appInfo = ref({ name: '', version: '', repo: '' })

async function loadAppInfo() {
  try { appInfo.value = await GetAppInfo() } catch (e) { appInfo.value = { name: 'sshore', version: '', repo: '' } }
}

watch(() => props.visible, (v) => { if (v) { err.value = ''; loadAppInfo() } })

// 任一设置变更：立即应用（根节点）+ 落盘。忽略初次挂载/关闭时的赋值。
watch(
  () => [store.theme, store.fontScale, store.latinFont, store.cjkFont, store.autoStartOnLaunch],
  async () => {
    if (!props.visible) return
    store.apply()
    saving.value = true
    err.value = ''
    try { await store.save() } catch (e) { err.value = String(e) } finally { saving.value = false }
  }
)
</script>

<template>
  <div v-if="visible" class="ui-overlay" @click.self="emit('close')">
    <div class="dialog" role="dialog" aria-label="设置">
      <div class="dhead">
        <div class="dtitle">设置</div>
        <button class="dclose" aria-label="关闭" @click="emit('close')">×</button>
      </div>

      <section class="group">
        <h3>主题</h3>
        <div class="opts">
          <label v-for="t in THEMES" :key="t.value" class="radio">
            <input type="radio" :value="t.value" v-model="store.theme" />
            <span>{{ t.label }}</span>
          </label>
        </div>
      </section>

      <section class="group">
        <h3>字号</h3>
        <div class="opts">
          <label v-for="f in FONT_SCALES" :key="f.value" class="radio">
            <input type="radio" :value="f.value" v-model.number="store.fontScale" />
            <span>{{ f.label }}</span>
          </label>
        </div>
      </section>

      <section class="group">
        <h3>字体</h3>
        <div class="field">
          <label for="latin-font">英文字体</label>
          <select id="latin-font" v-model="store.latinFont">
            <option v-for="f in LATIN_FONTS" :key="f.value" :value="f.value">{{ f.label }}</option>
          </select>
        </div>
        <div class="field">
          <label for="cjk-font">中文字体</label>
          <select id="cjk-font" v-model="store.cjkFont">
            <option v-for="f in CJK_FONTS" :key="f.value" :value="f.value">{{ f.label }}</option>
          </select>
        </div>
      </section>

      <section class="group">
        <h3>启动</h3>
        <label class="check">
          <input type="checkbox" v-model="store.autoStartOnLaunch" />
          <span>启动后自动连接转发通道</span>
        </label>
      </section>

      <section class="group help">
        <h3>帮助</h3>
        <p class="meta">应用 {{ appInfo.name || 'sshore' }} · 版本 {{ appInfo.version || 'dev' }} · 仓库
          <a v-if="appInfo.repo" :href="appInfo.repo" target="_blank" rel="noopener">{{ appInfo.repo }}</a>
          <span v-else>—</span>
        </p>
        <p class="docs">
          <a href="https://github.com/i2534/sshore/blob/master/README.zh-CN.md" target="_blank" rel="noopener">在线帮助文档 →</a>
        </p>
      </section>

      <p v-if="err" class="err">保存失败: {{ err }}</p>
      <div class="dbtns">
        <button class="primary" :disabled="saving" @click="emit('close')">完成</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 20px; width: 420px; max-height: 90vh; overflow: auto; box-shadow: 0 8px 32px rgba(0,0,0,0.5); }
.dhead { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.dtitle { font-weight: 600; color: var(--text); }
.dclose { background: none; border: none; color: var(--text-dim); font-size: var(--fs-20); line-height: 1; cursor: pointer; padding: 2px 8px; border-radius: 4px; }
.dclose:hover { background: var(--surface-hover); color: var(--text); }
.group { margin-bottom: 16px; padding-bottom: 12px; border-bottom: 1px solid var(--border); }
.group:last-of-type { border-bottom: none; }
.group h3 { margin: 0 0 8px; font-size: var(--fs-13); color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.4px; }
.opts { display: flex; flex-wrap: wrap; gap: 14px; }
.radio, .check { display: inline-flex; align-items: center; gap: 6px; color: var(--text); cursor: pointer; font-size: var(--fs-14); }
.radio input, .check input { width: auto; height: auto; margin: 0; padding: 0; background: none; border: none; cursor: pointer; }
.field { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.field label { color: var(--text-dim); font-size: var(--fs-13); min-width: 60px; }
.field select { flex: 1; }
.meta { color: var(--text-dim); font-size: var(--fs-13); margin: 0 0 8px; }
.meta a { color: var(--accent); text-decoration: none; word-break: break-all; }
.meta a:hover { text-decoration: underline; }
.docs { margin: 0 0 8px; }
.docs a { color: var(--accent); text-decoration: none; font-size: var(--fs-13); }
.docs a:hover { text-decoration: underline; }
.err { color: var(--danger); font-size: var(--fs-13); }
.dbtns { display: flex; justify-content: flex-end; margin-top: 8px; }
.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
.primary:hover { background: var(--accent-hover); }
.primary:disabled { opacity: 0.6; cursor: default; }
</style>
