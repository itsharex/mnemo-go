<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import Modal from './Modal.vue'
import UiIcon from './UiIcon.vue'
import ConfirmModal from './ConfirmModal.vue'
import { CheckUpdate, GetUpdateStatus, DownloadUpdate, CancelUpdate, ApplyUpdate, RevealInFolder, OpenBrowser, getSettings, onEvent } from '../api'

const props = defineProps({ initialInfo: { type: Object, default: null } })
const emit = defineEmits(['close'])
const state = ref('checking')
const info = ref(props.initialInfo ? { ...props.initialInfo } : null)
const progress = ref({ downloaded: 0, total: 0 })
const updatePath = ref('')
const errorMsg = ref('')
const confirmBeforeInstall = ref(true)
const installConfirmOpen = ref(false)
const canceling = ref(false)
const canInstall = computed(() => info.value?.canInstall === true)
const busy = computed(() => ['checking', 'downloading', 'verifying', 'applying'].includes(state.value))
let alive = true, sequence = 0, revision = -1, offState

function pct() {
  const total = progress.value.total || 0
  return total > 0 ? Math.min(100, Math.round(progress.value.downloaded / total * 100)) : 0
}
function fmtBytes(n) {
  let value = Math.max(0, Number(n) || 0)
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (value >= 1024 && i < units.length - 1) { value /= 1024; i++ }
  return (i ? value.toFixed(1) : String(Math.round(value))) + ' ' + units[i]
}
function receive(status) {
  if (!alive || !status || Number(status.revision) < revision) return false
  revision = Number(status.revision) || 0
  state.value = status.phase || 'idle'
  if (status.info) info.value = status.info
  progress.value = { downloaded: status.downloaded || 0, total: status.total || status.info?.size || 0 }
  updatePath.value = status.path || ''
  errorMsg.value = status.error || ''
  canceling.value = false
  return true
}
async function check() {
  if (['downloading', 'verifying', 'applying'].includes(state.value)) return
  const seq = ++sequence
  state.value = 'checking'; errorMsg.value = ''
  try {
    const result = await CheckUpdate()
    if (!alive || seq !== sequence) return
    info.value = result
    state.value = result?.available ? 'available' : 'idle'
    updatePath.value = ''
  } catch (e) {
    if (!alive || seq !== sequence) return
    errorMsg.value = String(e); state.value = 'error'
  }
}
async function startDownload() {
  if (busy.value) return
  state.value = 'downloading'; errorMsg.value = ''; updatePath.value = ''
  progress.value = { downloaded: 0, total: info.value?.size || 0 }
  try {
    await DownloadUpdate(info.value?.url || '')
    if (alive) receive(await GetUpdateStatus())
  } catch (e) {
    if (alive) { errorMsg.value = String(e); state.value = 'error' }
  }
}
async function cancelDownload() {
  if (canceling.value) return
  canceling.value = true
  try {
    await CancelUpdate()
    if (alive) receive(await GetUpdateStatus())
  } catch (e) {
    if (alive) { errorMsg.value = String(e); canceling.value = false }
  }
}
async function install() {
  if (busy.value || !canInstall.value) return
  if (confirmBeforeInstall.value) { installConfirmOpen.value = true; return }
  await applyUpdate()
}
async function applyUpdate() {
  installConfirmOpen.value = false
  if (busy.value || !updatePath.value) return
  state.value = 'applying'; errorMsg.value = ''
  try { await ApplyUpdate(updatePath.value) }
  catch (e) {
    if (!alive) return
    try { receive(await GetUpdateStatus()) }
    catch { updatePath.value = '' }
    if (alive) { errorMsg.value = String(e); state.value = 'error' }
  }
}
async function openFolder() {
  try { await RevealInFolder(updatePath.value) }
  catch (e) { errorMsg.value = String(e) }
}
async function openRelease() {
  if (!info.value?.releaseUrl) return
  try { await OpenBrowser(info.value.releaseUrl) }
  catch (e) { errorMsg.value = String(e) }
}

onMounted(async () => {
  offState = onEvent('update:state', status => {
    if (receive(status)) ++sequence
  })
  getSettings().then(s => { if (alive) confirmBeforeInstall.value = s?.confirmUpdate !== false }).catch(() => {})
  try {
    const status = await GetUpdateStatus()
    if (!alive) return
    if (!receive(status)) return
    if (['downloading', 'verifying', 'done', 'applying', 'canceled', 'error'].includes(state.value)) return
    if (props.initialInfo?.available) { info.value = props.initialInfo; state.value = 'available'; return }
    await check()
  } catch { if (alive) await check() }
})
onBeforeUnmount(() => { alive = false; ++sequence; offState?.() })
</script>

<template>
  <Modal title="更新" width="500px" @close="emit('close')">
    <div class="upd-body">
      <p v-if="info?.currentVersion" class="upd-meta upd-current">当前版本 {{ info.currentVersion }}</p>
      <div v-if="state === 'checking'" class="upd-state" role="status"><span class="spin"></span><span>正在检查更新…</span></div>
      <div v-else-if="state === 'idle'" class="upd-state">
        <UiIcon name="check" :size="30" /><p class="upd-title">已是最新正式版</p>
        <button class="btn" @click="check">重新检查</button>
      </div>
      <div v-else-if="state === 'available' || state === 'canceled'" class="upd-state">
        <UiIcon name="download" :size="30" />
        <p class="upd-title">{{ state === 'canceled' ? '下载已取消' : '发现新版本' }} <b>{{ info?.version }}</b></p>
        <p v-if="info?.size" class="upd-meta">更新包 {{ fmtBytes(info.size) }}</p>
        <p v-if="!canInstall" class="upd-meta">下载后手动安装，现有应用保持运行。</p>
        <div class="upd-actions"><button class="btn primary" @click="startDownload">{{ state === 'canceled' ? '重新下载' : '下载更新' }}</button><button class="btn" @click="emit('close')">稍后</button></div>
      </div>
      <div v-else-if="state === 'downloading' || state === 'verifying'" class="upd-state" role="status">
        <p class="upd-title">{{ state === 'verifying' ? '正在校验更新包…' : '正在下载更新…' }}</p>
        <div class="upd-progress" role="progressbar" :aria-valuenow="pct()" aria-valuemin="0" aria-valuemax="100"><div class="upd-bar" :style="{ width: pct() + '%' }"></div></div>
        <p class="upd-meta">{{ fmtBytes(progress.downloaded) }} / {{ progress.total > 0 ? fmtBytes(progress.total) : '大小未知' }}<template v-if="progress.total > 0"> · {{ pct() }}%</template></p>
        <p class="upd-meta">关闭此窗口后仍会继续下载，可从设置中重新打开。</p>
        <button class="btn" :disabled="canceling" @click="cancelDownload">{{ canceling ? '取消中…' : '取消下载' }}</button>
      </div>
      <div v-else-if="state === 'done'" class="upd-state">
        <UiIcon name="check" :size="30" /><p class="upd-title">下载完成，校验通过</p>
        <p class="upd-meta">{{ fmtBytes(progress.downloaded) }}</p>
        <div class="upd-actions"><button v-if="canInstall" class="btn primary" @click="install">安装并重启</button><button class="btn" @click="openFolder">打开目录</button></div>
        <p v-if="!canInstall" class="upd-meta">退出当前应用后，使用下载的更新包手动安装。</p>
      </div>
      <div v-else-if="state === 'applying'" class="upd-state" role="status"><span class="spin"></span><span>正在启动安装程序，请完成系统授权…</span></div>
      <div v-else-if="state === 'error'" class="upd-state">
        <UiIcon name="warning" :size="30" /><p class="upd-title">更新未完成</p>
        <div class="upd-actions"><button v-if="canInstall && updatePath" class="btn primary" @click="install">重试安装</button><button v-if="info?.available" class="btn" @click="startDownload">重新下载</button><button class="btn" @click="check">重新检查</button></div>
      </div>
      <p v-if="errorMsg" class="upd-err" role="alert">{{ errorMsg }}</p>
      <details v-if="info?.notes" class="upd-notes" open><summary>更新内容</summary><pre>{{ info.notes }}</pre></details>
      <button v-if="info?.releaseUrl" class="tbtn upd-release" @click="openRelease">查看发布页</button>
    </div>
  </Modal>
  <ConfirmModal v-if="installConfirmOpen" title="安装更新" message="安装会退出并重启 Mnemo，传输将中断。请先保存编辑内容，确认后继续。" ok-text="安装并重启" @ok="applyUpdate" @cancel="installConfirmOpen = false" />
</template>

<style scoped>
.upd-body { padding: 4px 0; }
.upd-state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 16px 0; text-align: center; }
.upd-title { font-size: var(--fs-body); color: var(--text-primary); margin: 0; }
.upd-title b { color: var(--color-primary); }
.upd-meta { font-size: var(--fs-aux); color: var(--text-tertiary); margin: 0; line-height: 1.6; }
.upd-current { text-align: center; }
.upd-err { font-size: var(--fs-aux); color: var(--color-danger); margin: 12px 0; overflow-wrap: anywhere; }
.upd-progress { width: 100%; height: 6px; background: var(--bg-subtle); border-radius: var(--radius-full); overflow: hidden; }
.upd-bar { height: 100%; background: var(--color-primary); transition: width 150ms linear; }
.upd-actions { display: flex; flex-wrap: wrap; gap: 8px; justify-content: center; }
.upd-notes { margin-top: 12px; padding: 12px; border-radius: var(--radius-md); background: var(--bg-subtle); }
.upd-notes summary { cursor: pointer; font-size: var(--fs-aux); }
.upd-notes pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 220px; overflow-y: auto; font: inherit; font-size: var(--fs-aux); line-height: 1.7; }
.upd-release { display: block; margin: 12px auto 0; }
</style>
