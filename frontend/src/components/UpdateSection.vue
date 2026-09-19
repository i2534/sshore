<script setup>
import { computed, onMounted } from "vue";
import { useUpdateStore } from "../stores/update";
import { useSettingsStore } from "../stores/settings";
import { stateLabel, statusMessage, actions, progressPercent, progressText } from "../utils/update";

const update = useUpdateStore();
const settings = useSettingsStore();

// 元素与按钮矩阵全部由 utils/update.js 的纯函数决定，模板只做绑定（spec §12.1）。
const acts = computed(() => actions(update.info));
const label = computed(() => stateLabel(update.info));
const message = computed(() => statusMessage(update.info));
// 进度文案用 progressText：「已下载 / 总长」或「已下载 X」；未知总长时 progressPercent 为 null → indeterminate。
const percent = computed(() => progressPercent(update.info));
const progressLabel = computed(() => progressText(update.info));
const customSource = computed(() => Boolean(settings.updateSource));

// 更新说明与升级日志可能上千行：统一截断到 2000 字符（spec §12.1）。
const EXCERPT_LIMIT = 2000;
function excerpt(text) {
  const s = text || "";
  return s.length > EXCERPT_LIMIT ? s.slice(0, EXCERPT_LIMIT) + "…" : s;
}
const notesExcerpt = computed(() => excerpt(update.info.notes));
const logExcerpt = computed(() => excerpt(update.info.pending_log));

// 失败详情：动作失败落 store.error，或后端快照带 error / pending_log / 人工升级提示。
const errorText = computed(() => update.error || update.info.error || "");
const failure = computed(() => Boolean(errorText.value || update.info.pending_log || update.info.hint === "manual-upgrade"));
// 失败时始终给「打开发布页」（可选动作矩阵之外的人工出路）。
const showOpenPage = computed(() => acts.value.openPage || failure.value);

onMounted(() => {
  // hydrate 只把失败落进 error 字段、不 rethrow（Task 14 评审 M2）；仍显式兜底，
  // 保证任何异常都不会变成未处理的 rejection。
  update.hydrate().catch(() => {});
});

// 动作失败已由 store 落进 error 字段并在失败详情区展示，这里不再向上抛（避免未处理 rejection）。
async function run(fn) {
  try {
    await fn();
  } catch (e) {
    /* 详情见 store.error */
  }
}

// 跳过/取消跳过后必须重读设置：后端直接改了 TOML，前端快照会过期（spec §6 第 2 条）。
async function skip() {
  await run(() => update.skip(update.info.latest));
  await settings.load();
}
async function clearSkip() {
  await run(() => update.clearSkip());
  await settings.load();
}
async function applyNow() {
  if (customSource.value && !globalThis.confirm("更新源为自定义地址，确认继续？")) return;
  await run(() => update.apply());
}
</script>

<template>
  <section class="group update">
    <h3>更新</h3>
    <p class="meta">{{ label }}</p>
    <p class="status">{{ message }}</p>

    <p v-if="errorText" class="warn">错误：{{ errorText }}</p>
    <p v-if="update.info.pending_log" class="warn">上次升级未完成，日志：{{ logExcerpt }}</p>
    <p v-if="update.info.hint === 'manual-upgrade'" class="warn">
      安装目录不可写，请按手动步骤升级（可打开发布页手动下载）。
    </p>

    <div v-if="update.info.state === 'downloading'" class="bar" :class="{ indet: percent === null }">
      <div class="fill" :style="percent === null ? {} : { width: percent + '%' }" />
      <span class="pct">{{ progressLabel }}</span>
    </div>

    <details v-if="notesExcerpt"><summary>更新说明</summary><pre>{{ notesExcerpt }}</pre></details>

    <div class="btns">
      <button v-if="acts.check" :disabled="update.info.state === 'checking'" @click="run(() => update.check())">立即检查更新</button>
      <button v-if="acts.download" class="primary" @click="run(() => update.download())">下载更新</button>
      <button v-if="acts.cancel" @click="run(() => update.cancel())">取消下载</button>
      <button v-if="acts.apply" class="primary" @click="applyNow()">重启并升级</button>
      <button v-if="acts.discard" @click="run(() => update.discard())">删除已下载的更新</button>
      <button v-if="acts.skip" @click="skip()">跳过此版本</button>
      <button v-if="acts.clearSkip" @click="clearSkip()">取消跳过</button>
      <button v-if="showOpenPage" @click="run(() => update.openPage())">打开发布页</button>
    </div>

    <div class="field">
      <label class="check"><input v-model="settings.updateCheckAuto" type="checkbox" /> 自动检查更新</label>
    </div>
    <div class="field">
      <label for="update-interval">检查间隔</label>
      <select id="update-interval" v-model.number="settings.updateCheckIntervalHours">
        <option :value="12">12 小时</option>
        <option :value="24">24 小时</option>
        <option :value="48">48 小时</option>
        <option :value="168">168 小时（7 天）</option>
        <option :value="0">关闭轮询</option>
      </select>
    </div>
    <div class="field">
      <label for="update-source">更新源</label>
      <input id="update-source" v-model="settings.updateSource" placeholder="默认 GitHub（可填镜像 API 地址）" />
      <button class="link" @click="settings.resetUpdateSettings()">恢复默认</button>
    </div>
    <p v-if="settings.updateSkippedVersion" class="meta">已跳过版本：{{ settings.updateSkippedVersion }}（只读）</p>
    <p v-if="customSource && !settings.updateSource.startsWith('https://')" class="warn">非本机地址必须使用 https。</p>
  </section>
</template>

<style scoped>
.update .status { color: var(--text); font-size: var(--fs-13); margin: 0 0 8px; }
.update .warn { color: var(--text-dim); font-size: var(--fs-12); margin: 0 0 8px; word-break: break-all; max-height: 160px; overflow: auto; }
.update .bar { position: relative; height: 18px; background: var(--surface-hover); border-radius: 4px; overflow: hidden; margin: 0 0 8px; }
.update .bar .fill { height: 100%; background: var(--accent); transition: width 200ms linear; }
.update .bar.indet .fill { width: 30%; animation: update-indet 1.2s ease-in-out infinite; }
@keyframes update-indet { 0% { margin-left: -30%; } 100% { margin-left: 100%; } }
.update .bar .pct { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; font-size: var(--fs-12); color: var(--text); }
.update details { margin: 0 0 8px; }
.update details pre { white-space: pre-wrap; word-break: break-word; max-height: 220px; overflow: auto; font-size: var(--fs-12); }
.update .btns { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 10px; }
.update .field { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.update .field label { color: var(--text-dim); font-size: var(--fs-13); min-width: 60px; }
.update .field input[type="text"], .update .field input:not([type]) { flex: 1; }
.update .link { background: none; border: none; color: var(--accent); cursor: pointer; font-size: var(--fs-13); padding: 0; }
</style>
