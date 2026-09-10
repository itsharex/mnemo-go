<script setup>
import { ref, computed, onBeforeUnmount } from 'vue'
import { search, capsOf, accountName, providerMetaOf, providerIconUrl } from '../api'
import Modal from './Modal.vue'
import UiIcon from './UiIcon.vue'
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
    <form class="workspace-controls" @submit.prevent="run"><input class="input" v-model="keyword" placeholder="搜索所有账号的文件" autofocus /><button class="btn primary">搜索</button></form>
    <label class="workspace-controls"><input type="checkbox" v-model="remote" />同时在线搜索</label>
    <p class="hint">缓存仅包含已浏览的目录，最多显示 1000 项；在线搜索按网盘能力执行，每个账号最多展示 500 项。</p>
    <p v-if="busy" role="status">正在搜索… <button class="tbtn" @click="epoch++; busy = false">停止</button></p>
    <p v-for="error in errors" :key="error" role="alert">{{ error }}</p>
    <div class="workspace-results">
      <button v-for="r in ordered" :key="r.userId + r.file.file_id" class="workspace-result" @click="open(r)">
        <img :src="providerIconUrl(providerMetaOf(account(r.userId), providers))" alt="" />
        <span><strong>{{ r.file.name }}</strong><small>{{ accountName(account(r.userId)) }} · {{ r.parentId || '路径由网盘提供' }} · {{ r.source }} · {{ new Date(r.updatedAt*1000).toLocaleString() }}</small></span><UiIcon name="chevron-right" :size="14" />
      </button>
      <p v-if="!busy && !ordered.length" class="workspace-empty-state">{{ keyword ? '没有匹配的文件' : '输入名称，查找散落在各个网盘的文件' }}</p>
    </div>
  </Modal>
</template>
