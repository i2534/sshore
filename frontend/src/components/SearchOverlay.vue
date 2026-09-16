<script setup>
import { ref, watch, nextTick, onUnmounted } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { SftpSearch, LocalSearch, SearchCancel } from '../../wailsjs/go/main/App'

const props = defineProps({
  visible: Boolean,
  scope: { type: String, default: 'remote' },
  host: { type: String, default: '' },
  root: { type: String, default: '/' },
})
const emit = defineEmits(['close', 'open-hit'])

const pattern = ref('')
const running = ref(false)
const hits = ref([])
const scanned = ref(0)
const unreadable = ref(0)
const truncated = ref(false)
const cancelled = ref(false)
const errorText = ref('')
let seq = 0
let offProgress = null

function title() {
  return props.scope === 'remote'
    ? '🔍 在 ' + props.host + ' : ' + props.root + ' 下递归搜索'
    : '🔍 在 ' + props.root + ' 下递归搜索'
}

async function run() {
  const id = 's' + (++seq)
  running.value = true
  cancelled.value = false
  errorText.value = ''
  hits.value = []
  scanned.value = 0
  const req = { id, root: props.root, pattern: pattern.value, maxDepth: 5, limit: 500 }
  try {
    const out = props.scope === 'remote'
      ? await SftpSearch({ ...req, host: props.host })
      : await LocalSearch(req)
    if (id !== 's' + seq) return
    hits.value = (out && out.hits) || []
    scanned.value = (out && out.scanned) || 0
    unreadable.value = (out && out.unreadable) || 0
    truncated.value = !!(out && out.truncated)
    cancelled.value = !!(out && out.cancelled)
  } catch (e) {
    if (id === 's' + seq) errorText.value = String(e)
  } finally {
    if (id === 's' + seq) running.value = false // 过期响应不得清掉新搜索的运行态
  }
}

function cancel() {
  SearchCancel('s' + seq)
  running.value = false
  cancelled.value = true
}

watch(() => props.visible, (v) => {
  if (v) {
    // 聚焦输入框：@keyup.esc 挂在遮罩上，只有焦点在子树里才会触发（spec §8.2 要求 Esc 可关）。
    nextTick(() => { const el = document.querySelector('.search-overlay input'); if (el) el.focus() })
    pattern.value = ''
    hits.value = []
    offProgress = EventsOn('sftp:search-progress', (p) => { if (p && p.id === 's' + seq) scanned.value = p.scanned })
    run()
  } else {
    // 关闭即取消：否则远端 BFS 会继续空跑（spec §8.2）。
    SearchCancel('s' + seq)
    running.value = false
    if (offProgress) { offProgress(); offProgress = null }
  }
})

onUnmounted(() => {
  SearchCancel('s' + seq)
  if (offProgress) { offProgress(); offProgress = null }
})
</script>

<template>
  <div v-if="visible" class="ui-overlay search-overlay" @click.self="emit('close')" @keyup.esc="emit('close')">
    <div class="panel">
      <div class="head">
        <span class="ftitle">{{ title() }}</span>
        <span v-if="running" class="dim">搜索中… 已扫描 {{ scanned }} 个目录</span>
        <button v-if="running" @click="cancel">取消</button>
        <button @click="emit('close')">关闭</button>
      </div>
      <div class="bar">
        <input v-model="pattern" placeholder="名称匹配（支持 * ?）" @keyup.enter="run" />
        <button @click="run">搜索</button>
      </div>
      <p v-if="errorText" class="err">{{ errorText }}</p>
      <p v-else class="dim">
        命中 {{ hits.length }} 项<template v-if="truncated"> · 触发上限（最多 500 条）</template>
        <template v-if="unreadable"> · {{ unreadable }} 个目录不可读</template>
        <template v-if="cancelled"> · 已取消（结果为已扫描部分）</template>
      </p>
      <ul class="hits">
        <li v-for="h in hits" :key="h.path" @dblclick="emit('open-hit', h)">
          <span class="p">{{ h.isDir ? '📁' : '📄' }} {{ h.path }}</span>
          <span class="s">{{ h.size || '—' }}</span>
          <span class="t">{{ h.modTime || '—' }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.panel { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 14px; width: 640px; max-height: 80vh; display: flex; flex-direction: column; text-align: left; }
.head { display: flex; gap: 8px; align-items: center; }
.ftitle { color: var(--text); font-weight: 600; flex: 1; }
.bar { display: flex; gap: 8px; margin: 10px 0; }
.bar input { flex: 1; }
.dim { color: var(--text-faint); font-size: var(--fs-12); }
.err { color: var(--danger); font-size: var(--fs-12); }
.hits { list-style: none; margin: 0; padding: 0; overflow: auto; font-family: monospace; font-size: var(--fs-12); }
.hits li { display: flex; gap: 10px; padding: 3px 4px; color: var(--text); cursor: pointer; }
.hits li:hover { background: var(--surface-hover); }
.hits .p { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hits .s { width: 90px; text-align: right; color: var(--text-dim); }
.hits .t { width: 130px; text-align: right; color: var(--text-faint); }
</style>
