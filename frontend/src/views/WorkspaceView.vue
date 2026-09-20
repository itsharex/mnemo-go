<script setup>
import { ref, computed, nextTick, watch, onMounted, onBeforeUnmount } from 'vue'
import PanView from './PanView.vue'
import UiSelect from '../components/UiSelect.vue'
import UiIcon from '../components/UiIcon.vue'
import { accountName, providerMetaOf, providerIconUrl, migrateFiles, move, copy, capsOf, onEvent, pinFileSnapshot } from '../api'
const props = defineProps({ account: Object, accounts: Array, providers: Array, dual: Boolean })
const emit = defineEmits(['toast', 'go'])
const dual = computed(() => props.dual)
const rightId = ref(''), focused = ref('left'), left = ref(null), right = ref(null), busy = ref(false)
const cloudDrag = ref(null), dragOver = ref('')
function endDrag() { cloudDrag.value = null; dragOver.value = '' }
watch(dual, () => { focused.value = 'left'; endDrag() })
const rightAccount = computed(() => props.accounts.find(a => a.user_id === rightId.value) || null)
const options = computed(() => props.accounts.map(a => ({ value: a.user_id, label: accountName(a), img: providerIconUrl(providerMetaOf(a, props.providers)) })))
watch(() => props.accounts, list => { if (!list.some(a => a.user_id === rightId.value)) rightId.value = list.find(a => a.user_id !== props.account?.user_id)?.user_id || list[0]?.user_id || '' }, { immediate: true })
watch(() => [props.account?.user_id, props.account?.drive_id, rightAccount.value?.user_id, rightAccount.value?.drive_id], endDrag)
function pane(side) { return (side === 'left' ? left : right).value }
function startDrag(side, source, event) {
  if (!dual.value || busy.value || !source.files.length || !event.dataTransfer) { event.preventDefault(); return }
  cloudDrag.value = { side, source }
  event.dataTransfer.effectAllowed = 'move'
  event.dataTransfer.setData('application/x-mnemo-cloud-files', 'internal')
}
function overPane(side, event) {
  if (!cloudDrag.value) return
  event.preventDefault()
  const allowed = !busy.value && side !== cloudDrag.value.side && pane(side)?.snapshot()?.mode === 'list'
  dragOver.value = allowed ? side : ''
  if (event.dataTransfer) event.dataTransfer.dropEffect = allowed ? 'move' : 'none'
}
function leavePane(event) {
  if (!event.currentTarget.contains(event.relatedTarget)) dragOver.value = ''
}
function dropOnPane(side, folder) {
  const drag = cloudDrag.value
  endDrag()
  if (!drag || drag.side === side) return
  return transfer(side === 'right' ? 'right' : 'left', drag.source, folder, true)
}
async function refreshAccounts(source, target) {
  const sameAccount = source.account.user_id === target.account.user_id && source.account.drive_id === target.account.drive_id
  await Promise.all([left.value, right.value].filter(Boolean).flatMap(view => [
    view.invalidateDirectories(source.account.user_id, source.account.drive_id, [source.dirId, ...(sameAccount ? [target.dirId] : [])]),
    ...(sameAccount
      ? [] : [view.invalidateDirectories(target.account.user_id, target.account.drive_id, [target.dirId])]),
  ]))
}
async function transfer(direction, dragged = null, folder = null, moving = false) {
  if (busy.value) return
  const src = dragged || (direction === 'right' ? left : right).value?.snapshot()
  const destination = (direction === 'right' ? right : left).value?.snapshot()
  const dst = destination && { ...destination, dirId: folder?.file_id || destination.dirId, path: folder ? [...destination.path, { id: folder.file_id }] : destination.path }
  if (!src?.account || !dst?.account || !src.files.length || dst.mode !== 'list') { emit('toast', '请选择文件，并在另一栏打开目标目录', 'warn'); return }
  busy.value = true
  try {
    if (src.account.user_id === dst.account.user_id && src.account.drive_id === dst.account.drive_id) {
      if (src.dirId === dst.dirId) throw new Error('来源与目标目录相同')
      if (src.files.some(f => f.isDir && (f.file_id === dst.dirId || dst.path.some(p => f.file_id === p.id)))) throw new Error('不能移动或复制到自身及其子目录')
      if (!capsOf(src.account, props.providers)[moving ? 'move' : 'copy']) throw new Error(moving ? '该网盘不支持盘内移动' : '该网盘不支持盘内复制')
      await pinSelection(src)
      const result = await (moving ? move : copy)(src.account.user_id, src.account.drive_id, src.files.map(f => f.file_id), dst.dirId)
      await refreshAccounts(src, dst)
      if (!Array.isArray(result) || result.length !== src.files.length) throw new Error('部分项目未完成，请检查目录后重试')
      emit('toast', moving ? '已移动到目标目录' : '已复制到目标目录', 'success')
    } else {
      if (!capsOf(src.account, props.providers).download) throw new Error('源网盘不支持下载，无法迁移')
      const targetCaps = capsOf(dst.account, props.providers)
      if (!targetCaps.upload && !targetCaps.rapidUploadHashes?.length) throw new Error('目标网盘不支持上传，无法迁移')
      await pinSelection(src)
      await migrateFiles(src.account.user_id, src.account.drive_id, dst.account.user_id, dst.account.drive_id, dst.dirId, src.files.map(f => f.file_id), moving)
      emit('toast', moving ? '已提交移动迁移，成功传输后移除源项目；可在传输页查看进度' : '已提交复制迁移，可在传输页查看进度', 'success')
    }
  } catch(e) { emit('toast', String(e), 'error') }
  finally { busy.value = false }
}
function pinSelection(source) {
  return Promise.all(source.files.map(file => pinFileSnapshot(source.account.user_id, source.account.drive_id, {
    ...file, parent_file_id: file.parent_file_id || source.dirId,
  })))
}
let stopMigration
onMounted(() => {
  stopMigration = onEvent('migrate:progress', job => {
    if (!job || !['completed', 'partial', 'failed', 'canceled'].includes(job.status)) return
    refreshAccounts(
      { account: { user_id: job.srcUser, drive_id: job.srcDrive } },
      { account: { user_id: job.dstUser, drive_id: job.dstDrive }, dirId: job.dstParent },
    ).catch(error => emit('toast', String(error), 'error'))
  })
})
onBeforeUnmount(() => { stopMigration?.(); endDrag() })
function active() { return dual.value && focused.value === 'right' ? right.value : left.value }
defineExpose({
  selectAccount: reload => { focused.value = 'left'; if (reload) return left.value?.refresh() },
  navigateHistory: direction => active()?.navigateHistory(direction), refresh: () => active()?.refresh(),
  openMkdirModal: () => active()?.openMkdirModal(), openUploadModal: () => active()?.openUploadModal(),
  clearCache: () => { left.value?.clearCache(); right.value?.clearCache() },
  navigate: async location => { focused.value = 'left'; await nextTick(); await left.value?.navigate(location) },
})
</script>
<template>
  <div class="workspace-view">
    <div v-if="dual" class="workspace-controls">
      <button class="btn" :disabled="busy" @click="transfer('right')"><UiIcon name="copy" :size="14" />复制到右栏</button>
      <button class="btn" :disabled="busy" @click="transfer('left')"><UiIcon name="copy" :size="14" />复制到左栏</button>
      <UiSelect v-model="rightId" :options="options" placeholder="右侧账号" />
    </div>
    <div class="workspace-panes" :class="{dual}">
      <div class="workspace-pane" :class="{focused:focused === 'left', 'cloud-drop-active': dragOver === 'left'}" @pointerdown.capture="focused = 'left'" @focusin="focused = 'left'" @dragover="overPane('left', $event)" @dragleave="leavePane"><PanView ref="left" :account="account" :accounts="accounts" :providers="providers" :keyboard-active="!dual || focused === 'left'" :cloud-drag-enabled="dual && !busy" :cloud-drag-active="!!cloudDrag" @cloud-drag-start="(source, event) => startDrag('left', source, event)" @cloud-drag-end="endDrag" @cloud-drop="folder => dropOnPane('left', folder)" @toast="(...args) => emit('toast', ...args)" @go="emit('go', $event)" /><div v-if="dragOver === 'left'" class="cloud-drop-hint">释放以移动</div></div>
      <div v-if="dual" class="workspace-pane workspace-pane-enter" :class="{focused:focused === 'right', 'cloud-drop-active': dragOver === 'right'}" @pointerdown.capture="focused = 'right'" @focusin="focused = 'right'" @dragover="overPane('right', $event)" @dragleave="leavePane"><PanView ref="right" :account="rightAccount" :accounts="accounts" :providers="providers" :keyboard-active="focused === 'right'" location-key="right" :cloud-drag-enabled="dual && !busy" :cloud-drag-active="!!cloudDrag" @cloud-drag-start="(source, event) => startDrag('right', source, event)" @cloud-drag-end="endDrag" @cloud-drop="folder => dropOnPane('right', folder)" @toast="(...args) => emit('toast', ...args)" @go="emit('go', $event)" /><div v-if="dragOver === 'right'" class="cloud-drop-hint">释放以移动</div></div>
    </div>
  </div>
</template>

<style scoped>
.workspace-controls { gap: 8px; }
.workspace-controls .btn { display: inline-flex; align-items: center; gap: 6px; }
.workspace-pane { position: relative; min-width: 0; }
.workspace-pane-enter { animation: workspace-pane-enter var(--motion-normal) var(--motion-ease); }
@keyframes workspace-pane-enter { from { opacity: 0; transform: translateX(6px); } }
.cloud-drop-active { outline: 2px dashed var(--color-primary); outline-offset: -4px; }
.cloud-drop-hint { position: absolute; bottom: 16px; left: 50%; translate: -50% 0; padding: 8px 14px; border-radius: var(--radius-full); background: var(--bg-elevated); color: var(--color-primary); box-shadow: var(--shadow-modal); font-size: 12px; text-align: center; pointer-events: none; z-index: 10; }
@media (prefers-reduced-motion: reduce) { .workspace-pane-enter { animation: none; } }
</style>
