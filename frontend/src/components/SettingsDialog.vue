<script setup>
import { ref, watch } from 'vue'
import { GetAppInfo } from '../../wailsjs/go/main/App'
import { useSettingsStore, THEMES, FONT_SCALES, LATIN_FONTS, CJK_FONTS } from '../stores/settings'
import UpdateSection from './UpdateSection.vue'

const props = defineProps({ visible: Boolean })
const emit = defineEmits(['close'])
const store = useSettingsStore()
const saving = ref(false)
const err = ref('')
const appInfo = ref({ name: '', version: '', repo: '' })

async function loadAppInfo() {
  try { appInfo.value = await GetAppInfo() } catch (e) { appInfo.value = { name: 'SSHore', version: '', repo: '' } }
}

watch(() => props.visible, (v) => { if (v) { err.value = ''; loadAppInfo() } })

// 任一设置变更：立即应用（根节点）+ 落盘。忽略初次挂载/关闭时的赋值。
// 注意：更新源是自由文本，**不得**放进这里 —— 逐键落盘会产生并发 save（无序号）丢输入；
// 它由 UpdateSection 单独做 ~500ms 防抖，失焦/回车立即 flush（fix round 1 A）。
watch(
  () => [store.theme, store.fontScale, store.latinFont, store.cjkFont, store.autoStartOnLaunch, store.sftpTransport,
    store.updateCheckAuto, store.updateCheckIntervalHours],
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

      <div class="dbody">
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

        <section class="group">
          <h3>传输</h3>
          <div class="field">
            <label for="sftp-transport">SFTP 传输后端</label>
            <select id="sftp-transport" v-model="store.sftpTransport">
              <option value="">自动（跟随内置默认）</option>
              <option value="gosftp">新实现 gosftp（当前默认）</option>
              <option value="batch">旧实现 batch（兼容回退）</option>
            </select>
          </div>
        </section>

        <section class="group help">
          <h3>帮助</h3>
          <p class="meta">应用 {{ appInfo.name || 'SSHore' }} · 版本 {{ appInfo.version || 'dev' }} · 仓库
            <a v-if="appInfo.repo" :href="appInfo.repo" target="_blank" rel="noopener">{{ appInfo.repo }}</a>
            <span v-else>—</span>
          </p>
          <p class="docs">
            <a href="https://github.com/i2534/sshore/blob/master/README.md" target="_blank" rel="noopener">在线帮助文档 →</a>
          </p>
        </section>

        <UpdateSection />

        <p v-if="err" class="err">保存失败: {{ err }}</p>
      </div>

      <div class="dbtns">
        <button class="primary" :disabled="saving" @click="emit('close')">完成</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 根因（2026-09-20 真机截图定位）：style.css 的 html/#app 是全局 text-align: center，
   对话框必须显式改回 left —— 否则标题、主题/字号单选组、启动复选、帮助段全部居中，而
   .field 行是 flex 左对齐，两套对齐规则混在一起正是「设置页排版杂乱」的来源。
   ConflictDialog / SearchOverlay 的 .dialog/.panel 也是这么抵消的。 */
.dialog {
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: 8px;
  width: 460px;
  max-height: 88vh;
  text-align: left;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 8px 32px rgba(0,0,0,0.5);
}
/* 头/脚固定、只有主体滚动：设置项多（含更新区）时「完成」不必滚到底才能点到。 */
.dhead { display: flex; align-items: center; justify-content: space-between; padding: 14px 20px 10px; border-bottom: 1px solid var(--border); flex: 0 0 auto; }
.dtitle { font-weight: 600; color: var(--text); }
.dclose { background: none; border: none; color: var(--text-dim); font-size: var(--fs-20); line-height: 1; cursor: pointer; padding: 2px 8px; border-radius: 4px; height: auto; }
.dclose:hover { background: var(--surface-hover); color: var(--text); }
.dbody { flex: 1 1 auto; overflow-y: auto; padding: 14px 20px; }
.dbtns { display: flex; justify-content: flex-end; padding: 10px 20px 14px; border-top: 1px solid var(--border); flex: 0 0 auto; }
.group { margin-bottom: 14px; padding-bottom: 12px; border-bottom: 1px solid var(--border); }
.group:last-of-type { margin-bottom: 0; padding-bottom: 0; border-bottom: none; }
.group h3 { margin: 0 0 10px; font-size: var(--fs-13); color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.4px; }
.opts { display: flex; flex-wrap: wrap; gap: 14px; }
.radio, .check { display: inline-flex; align-items: center; gap: 6px; color: var(--text); cursor: pointer; font-size: var(--fs-14); }
.radio input, .check input { width: auto; height: auto; margin: 0; padding: 0; background: none; border: none; cursor: pointer; }
/* 统一的「固定标签列 + 自适应控件」行：英文字体/中文字体/SFTP 传输后端与更新区共用同一
   左边界。旧实现用 min-width: 60px，长标签（SFTP 传输后端）会把控件推右，列就错开了。 */
.field { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.field:last-child { margin-bottom: 0; }
.field label { flex: 0 0 84px; color: var(--text-dim); font-size: var(--fs-13); }
.field select, .field input { flex: 1 1 auto; min-width: 0; }
.meta { color: var(--text-dim); font-size: var(--fs-13); margin: 0 0 8px; }
.meta a { color: var(--accent); text-decoration: none; word-break: break-all; }
.meta a:hover { text-decoration: underline; }
.docs { margin: 0; }
.docs a { color: var(--accent); text-decoration: none; font-size: var(--fs-13); }
.docs a:hover { text-decoration: underline; }
.err { color: var(--danger); font-size: var(--fs-13); margin: 8px 0 0; }
.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
.primary:hover { background: var(--accent-hover); }
.primary:disabled { opacity: 0.6; cursor: default; }
</style>
