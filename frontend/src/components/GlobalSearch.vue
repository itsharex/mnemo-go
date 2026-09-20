<script setup>
import { ref, computed, onBeforeUnmount } from 'vue'
import { motion, MotionConfig } from 'motion-v'
import { search, capsOf, accountName, providerMetaOf, providerIconUrl } from '../api'
import Modal from './Modal.vue'
import UiIcon from './UiIcon.vue'
import { compactReveal, reducedMotion } from '../motion/presets'
const props = defineProps({ accounts: Array, providers: Array })
const emit = defineEmits(['close', 'navigate'])
const keyword = ref(''), results = ref([]), errors = ref([]), busy = ref(false), remote = ref(false)
let epoch = 0
onBeforeUnmount(() => ++epoch)
const ordered = computed(() => {
  const rank = new Map(props.accounts.map((a, i) => [a.user_id, i]))
  return results.value.filter(r => rank.has(r.userId)).sort((a,b) => rank.get(a.userId) - rank.get(b.userId))
})
function account(id) { return props.accounts.find(a => a.user_id === id) }
async function run() {
  const query = keyword.value.trim()
  const seq = ++epoch
  results.value = []; errors.value = []
  if (!query) { busy.value = false; return }
  busy.value = true
  try {
    const cached = await window.go.app.App.SearchCachedFiles(query)
    if (seq !== epoch) return
    results.value = (cached || []).map(r => ({ ...r, source: '缓存' }))
    if (remote.value) {
      const queue = props.accounts.filter(a => capsOf(a, props.providers).search)
      const worker = async () => {
        while (queue.length && seq === epoch) {
          const acc = queue.shift()
          try {
            const files = await search(acc.user_id, acc.drive_id, query)
            if (seq !== epoch) return
            const seen = new Set((files || []).map(f => f.file_id))
            results.value = results.value.filter(r => r.userId !== acc.user_id || !seen.has(r.file.file_id))
            results.value.push(...(files || []).slice(0, 500).map(file => ({ userId: acc.user_id, driveId: acc.drive_id, parentId: file.parent_file_id, file, source: '在线', updatedAt: Date.now()/1000 })))
          } catch { if (seq === epoch) errors.value.push(`${accountName(acc)}：在线搜索失败`) }
        }
      }
      await Promise.all([worker(), worker()])
    }
  } catch { if (seq === epoch) errors.value.push('缓存搜索失败，请重试') }
  finally { if (seq === epoch) busy.value = false }
}
function open(result) {
  emit('navigate', { userId: result.userId, dirId: result.file.isDir ? result.file.file_id : result.parentId, name: result.file.isDir ? result.file.name : '所在目录', file: result.file })
  emit('close')
}
</script>
<template>
  <Modal title="搜索" width="760px" @close="emit('close')">
    <MotionConfig :reduced-motion="reducedMotion">
      <motion.div class="global-search" :initial="compactReveal.initial" :animate="compactReveal.animate" :transition="compactReveal.transition">
        <form class="global-search-form" @submit.prevent="run">
          <UiIcon name="search" :size="17" class="global-search-icon" />
          <input class="input" v-model="keyword" placeholder="搜索所有账号的文件" autofocus />
          <button class="btn primary" :disabled="busy">{{ busy ? '搜索中' : '搜索' }}</button>
        </form>
        <div class="global-search-options">
          <label class="global-search-toggle"><input type="checkbox" v-model="remote" /><span>同时在线搜索</span></label>
          <span>缓存结果优先显示</span>
        </div>
        <div v-if="busy" class="global-search-status" role="status"><span class="spin"></span><span>正在搜索…</span><button class="tbtn" type="button" @click="epoch++; busy = false">停止</button></div>
        <div v-for="error in errors" :key="error" class="global-search-error" role="alert"><UiIcon name="warning" :size="14" /><span>{{ error }}</span></div>
        <div class="workspace-results global-search-results">
          <button v-for="r in ordered" :key="r.userId + r.file.file_id" class="workspace-result" @click="open(r)">
            <img :src="providerIconUrl(providerMetaOf(account(r.userId), providers))" alt="" />
            <span><strong>{{ r.file.name }}</strong><small>{{ accountName(account(r.userId)) }} · {{ r.parentId || '路径由网盘提供' }} · {{ r.source }} · {{ new Date(r.updatedAt*1000).toLocaleString() }}</small></span><UiIcon name="chevron-right" :size="14" />
          </button>
          <div v-if="!busy && !ordered.length" class="workspace-empty-state global-search-empty">
            <UiIcon name="search" :size="22" />
            <span>{{ keyword ? '没有匹配的文件' : '输入名称，查找散落在各个网盘的文件' }}</span>
          </div>
        </div>
      </motion.div>
    </MotionConfig>
  </Modal>
</template>

<style scoped>
.global-search { display: grid; gap: 12px; min-height: 0; }
.global-search-form { display: flex; align-items: center; gap: 9px; padding: 6px 7px 6px 12px; border: 1px solid var(--control-border); border-radius: var(--radius-md); background: var(--control-bg); }
.global-search-form:focus-within { border-color: var(--border-focus); box-shadow: var(--ring-focus); }
.global-search-form .input { min-width: 0; flex: 1; height: 30px; border: 0; box-shadow: none; background: transparent; }
.global-search-form .input:focus { box-shadow: none; }
.global-search-icon { flex: 0 0 auto; color: var(--color-primary); }
.global-search-options { display: flex; align-items: center; justify-content: space-between; gap: 10px; color: var(--text-tertiary); font-size: 12px; }
.global-search-toggle { display: inline-flex; align-items: center; gap: 7px; color: var(--text-secondary); cursor: pointer; }
.global-search-toggle input { accent-color: var(--color-primary); }
.global-search-status, .global-search-error { display: flex; align-items: center; gap: 8px; min-height: 32px; padding: 7px 9px; border-radius: var(--radius-sm); background: var(--bg-subtle); color: var(--text-secondary); font-size: 12.5px; }
.global-search-status .tbtn { margin-left: auto; }
.global-search-error { color: var(--color-error); background: color-mix(in srgb, var(--color-error) 8%, transparent); }
.global-search-results { max-height: min(420px, 46vh); overflow-y: auto; border-top: 1px solid var(--border-lighter); }
.global-search-results .workspace-result { transition: background var(--motion-fast) var(--motion-ease), transform var(--motion-fast) var(--motion-spring); }
.global-search-results .workspace-result:hover { transform: translateX(2px); }
.global-search-empty { display: grid; justify-items: center; gap: 9px; min-height: 160px; color: var(--text-tertiary); }
.global-search-empty svg { color: var(--color-primary); opacity: .7; }
@media (max-width: 560px) { .global-search-options > span { display: none; } .global-search-form .btn { min-width: 64px; } }
</style>
