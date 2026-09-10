<script setup>
import { ref, computed, nextTick, watch } from 'vue'
import PanView from './PanView.vue'
import UiSelect from '../components/UiSelect.vue'
import { accountName, providerMetaOf, providerIconUrl, migrateFiles, copy, capsOf } from '../api'
const props = defineProps({ account: Object, accounts: Array, providers: Array })
const emit = defineEmits(['toast', 'go'])
const dual = ref(false), rightId = ref(''), focused = ref('left'), left = ref(null), right = ref(null), busy = ref(false)
const rightAccount = computed(() => props.accounts.find(a => a.user_id === rightId.value) || null)
const options = computed(() => props.accounts.map(a => ({ value: a.user_id, label: accountName(a), img: providerIconUrl(providerMetaOf(a, props.providers)) })))
watch(() => props.accounts, list => { if (!list.some(a => a.user_id === rightId.value)) rightId.value = list.find(a => a.user_id !== props.account?.user_id)?.user_id || list[0]?.user_id || '' }, { immediate: true })
async function transfer(direction) {
  if (busy.value) return
  const src = (direction === 'right' ? left : right).value?.snapshot()
  const dst = (direction === 'right' ? right : left).value?.snapshot()
  if (!src?.account || !dst?.account || !src.files.length || dst.mode !== 'list') { emit('toast', '请选择文件，并在另一栏打开目标目录', 'warn'); return }
  busy.value = true
  try {
    if (src.account.user_id === dst.account.user_id && src.account.drive_id === dst.account.drive_id) {
      if (src.dirId === dst.dirId) throw new Error('来源与目标目录相同')
      if (dst.path.some(p => src.files.some(f => f.isDir && f.file_id === p.id))) throw new Error('不能复制到自身子目录')
      if (!capsOf(src.account, props.providers).copy) throw new Error('该网盘不支持盘内复制')
      await copy(src.account.user_id, src.account.drive_id, src.files.map(f => f.file_id), dst.dirId)
      await (direction === 'right' ? right : left).value?.refresh()
    } else {
      await migrateFiles(src.account.user_id, src.account.drive_id, dst.account.user_id, dst.account.drive_id, dst.dirId, src.files.map(f => f.file_id), false)
    }
    emit('toast', '已提交，可在传输页查看进度', 'success')
  } catch(e) { emit('toast', String(e), 'error') }
  finally { busy.value = false }
}
function active() { return dual.value && focused.value === 'right' ? right.value : left.value }
defineExpose({
  navigateHistory: direction => active()?.navigateHistory(direction), refresh: () => active()?.refresh(),
  openMkdirModal: () => active()?.openMkdirModal(), openUploadModal: () => active()?.openUploadModal(),
  clearCache: () => { left.value?.clearCache(); right.value?.clearCache() },
  navigate: async location => { focused.value = 'left'; await nextTick(); await left.value?.navigate(location) },
})
</script>
<template>
  <div class="workspace-view">
    <div class="workspace-controls">
      <span class="account-inline" v-if="account"><img :src="providerIconUrl(providerMetaOf(account, providers))" alt="" />{{ accountName(account) }}</span>
      <button class="tbtn" :class="{active:dual}" @click="dual = !dual; focused = 'left'">{{ dual ? '单栏' : '双栏' }}</button>
      <template v-if="dual"><button class="btn" :disabled="busy" @click="transfer('right')">复制 →</button><button class="btn" :disabled="busy" @click="transfer('left')">← 复制</button><UiSelect v-model="rightId" :options="options" placeholder="右侧账号" /></template>
    </div>
    <div class="workspace-panes" :class="{dual}">
      <div class="workspace-pane" :class="{focused:focused === 'left'}" @pointerdown.capture="focused = 'left'" @focusin="focused = 'left'"><PanView ref="left" :account="account" :accounts="accounts" :providers="providers" :keyboard-active="!dual || focused === 'left'" @toast="(...args) => emit('toast', ...args)" @go="emit('go', $event)" /></div>
      <div v-if="dual" class="workspace-pane" :class="{focused:focused === 'right'}" @pointerdown.capture="focused = 'right'" @focusin="focused = 'right'"><PanView ref="right" :account="rightAccount" :accounts="accounts" :providers="providers" :keyboard-active="focused === 'right'" location-key="right" @toast="(...args) => emit('toast', ...args)" @go="emit('go', $event)" /></div>
    </div>
  </div>
</template>
