<script setup>
import { ref, computed, watch, onMounted, onActivated, onDeactivated, onBeforeUnmount } from 'vue'
import {
  ListDownloads, ListUploads,
  PauseDownload, ResumeDownload, CancelDownload, ClearDownloads,
  RemoveDownload, PrioritizeDownload, OpenFile,
  CancelUpload, ClearUploads, DownloadURL, ResumeUpload,
  ListOfflineTasks, OfflineDownload, DeleteOfflineTask,
  ListMigrateJobs, CancelMigrate, ResumeMigrate, DeleteMigrateJob, ClearMigrateJobs,
  EventsOn, RevealInFolder,
  accountName, providerIconUrl, providerMetaOf, capsOf,
  formatBytes, formatSpeed, formatTime, iconOf, copyText
} from '../api'
import { getPrefs } from '../appearance'
import Modal from '../components/Modal.vue'
import ConfirmModal from '../components/ConfirmModal.vue'
import ContextMenu from '../components/ContextMenu.vue'
import UiIcon from '../components/UiIcon.vue'
import UiSelect from '../components/UiSelect.vue'

const props = defineProps({
  accounts: { type: Array, default: () => [] },
  providers: { type: Array, default: () => [] }
})
const emit = defineEmits(['toast', 'navigate'])
const detailID = ref('')
const verifying = ref(false), verification = ref({})
async function verifyMigration() {
  if (!detailID.value || verifying.value) return
  const id = detailID.value; verifying.value = true
  try {
    const result = await window.go.app.App.VerifyMigration(id)
    if (detailID.value === id) verification.value = Object.fromEntries((result || []).map(item => [item.id,item]))
  } catch(e) { emit('toast',String(e),'error') }
  finally { verifying.value = false }
}
watch(detailID, () => { verification.value = {} })
const detailJob = computed(() => migrateJobs.value.find(j => j.id === detailID.value))
const verificationLabels = { unverified: '未校验', size: '传输大小已核对', 'provider-hash': '服务商已接受哈希' }

// ---------- 菜单 / 账号筛选 ----------
const menu = ref('downloading')
const filterUser = ref('')

// 支持云离线的账号
const hasOffline = computed(() => offlineAccounts.value.length > 0)
const offlineAccounts = computed(() => props.accounts.filter((a) => capsOf(a, props.providers).offlineDownload))

const menus = computed(() => {
  const list = [
    { key: 'downloading', icon: 'download', label: '正在下载', cnt: activeDownloads.value.length },
    { key: 'downloaded', icon: 'check', label: '已下载', cnt: doneDownloads.value.length },
    { key: 'uploading', icon: 'upload', label: '正在上传', cnt: activeUploads.value.length },
    { key: 'uploaded', icon: 'check', label: '已上传', cnt: doneUploads.value.length }
  ]
  if (hasOffline.value) list.push({ key: 'offline', icon: 'cloud-down', label: '云离线', cnt: offlineTasks.value.length })
  list.push({ key: 'migrate', icon: 'migrate', label: '迁移', cnt: migrateJobs.value.length })
  return list
})

watch(hasOffline, (v) => { if (!v && menu.value === 'offline') menu.value = 'downloading' })
watch(menu, () => { selectedIds.value = new Set(); ctx.value.show = false })
watch(filterUser, () => { selectedIds.value = new Set() })

function accIcon(acc) { return providerIconUrl(providerMetaOf(acc, props.providers)) }
function accLabel(acc) { return accountName(acc) }

// ---------- 下载 / 上传数据 ----------
const downloads = ref([])
const uploads = ref([])
const loading = ref(true)
const refreshError = ref('')
let refreshSeq = 0

// 下载完成提示音
let finishAudio = null
function playFinishSound() {
  try {
    if (!getPrefs().downloadSound) return
    if (!finishAudio) {
      finishAudio = new Audio(new URL('../assets/audio/download_finished.mp3', import.meta.url).href)
    }
    finishAudio.currentTime = 0
    finishAudio.play().catch(() => {})
  } catch { /* 忽略音频播放限制 */ }
}

async function refresh() {
  if (refreshInFlight) {
    refreshQueued = true
    return
  }
  refreshInFlight = true
  const seq = ++refreshSeq
  try {
    const [d, u] = await Promise.all([ListDownloads(), ListUploads()])
    if (seq !== refreshSeq) return
    downloads.value = d || []
    uploads.value = u || []
    refreshError.value = ''
  } catch (e) {
    if (seq === refreshSeq) refreshError.value = String(e && e.message ? e.message : e)
  } finally {
    if (seq === refreshSeq) loading.value = false
    refreshInFlight = false
    if (refreshQueued) {
      refreshQueued = false
      scheduleRefresh(0)
    }
  }
}

let refreshInFlight = false
let refreshQueued = false

function onTransferEvent(ev) {
  if (!ev || !ev.task) { scheduleRefresh(); return }
  const t = ev.task
  if (ev.kind === 'download') {
    const list = downloads.value
    const idx = list.findIndex((x) => x.id === t.id)
    if (t.status === 'removed') {
      if (idx >= 0) list.splice(idx, 1)
      if (selectedIds.value.has(t.id)) {
        const s = new Set(selectedIds.value)
        s.delete(t.id)
        selectedIds.value = s
      }
      return
    }
    if (idx >= 0) {
      const prev = list[idx]
      if (prev.status !== 'completed' && t.status === 'completed') {
        playFinishSound()
        // 完成后清理该项已选状态
        if (selectedIds.value.has(t.id)) {
          const s = new Set(selectedIds.value)
          s.delete(t.id)
          selectedIds.value = s
        }
      }
      Object.assign(list[idx], t)
    } else {
      downloads.value.unshift(t)
    }
  } else if (ev.kind === 'upload') {
    const list = uploads.value
    const idx = list.findIndex((x) => x.UploadID === t.id)
    if (idx < 0) {
      // The enqueue event can arrive before the first list refresh. Fetch once
      // for the new task, then keep subsequent progress event-driven.
      scheduleRefresh(150)
      return
    }
    const job = list[idx]
    const previous = upStatus(job)
    const next = {
      ...(job.Upload || {}),
      DownSize: Number.isFinite(t.downloaded) ? t.downloaded : (job.Upload?.DownSize || 0),
      DownSpeed: Number.isFinite(t.speed) ? t.speed : (job.Upload?.DownSpeed || 0),
      DownSpeedStr: t.speed ? formatSpeed(t.speed) : '',
      DownProcess: Number.isFinite(t.progress) ? t.progress : (job.Upload?.DownProcess || 0),
      DownState: t.status === 'completed' ? 'completed' : (t.status === 'failed' ? 'failed' : (t.status === 'paused' ? 'stopped' : t.status)),
      IsCompleted: t.status === 'completed',
      IsFailed: t.status === 'failed',
      IsStop: t.status === 'paused' || t.status === 'canceled',
      IsDowning: t.status === 'uploading' || t.status === 'queued',
    }
    // Keep the rich upload metadata returned by ListUploads while replacing
    // only the progress fields carried by the lightweight event snapshot.
    job.Upload = next
    if (previous !== upStatus(job) && t.status === 'completed') scheduleRefresh(0)
  }
}

let refreshTimer = null
function scheduleRefresh(delay = 400) {
  clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    refreshTimer = null
    refresh()
  }, delay)
}

// 快速筛选（任务名/链接，120ms 防抖）
const taskFilterRaw = ref('')
const taskFilter = ref('')
let taskFilterTimer = null
watch(taskFilterRaw, (v) => {
  clearTimeout(taskFilterTimer)
  taskFilterTimer = setTimeout(() => { taskFilter.value = v }, 120)
})
const byKw = (list, nameFn) => {
  const kw = taskFilter.value.trim().toLowerCase()
  if (!kw) return list
  return list.filter((t) => (nameFn(t) || '').toLowerCase().includes(kw) || String(t.url || '').toLowerCase().includes(kw))
}
const byAccount = (list) => filterUser.value ? list.filter((t) => t.user_id === filterUser.value) : list
const byUploadAccount = (list) => filterUser.value ? list.filter((t) => t.UserID === filterUser.value || (t.Info && t.Info.user_id === filterUser.value)) : list
const activeDownloads = computed(() => byKw(byAccount(downloads.value.filter((t) => t.status !== 'completed')), (t) => t.name))
const doneDownloads = computed(() => byKw(byAccount(downloads.value.filter((t) => t.status === 'completed')), (t) => t.name))
const activeUploads = computed(() => byKw(byUploadAccount(uploads.value.filter((t) => t.Upload && !t.Upload.IsCompleted)), upName))
const doneUploads = computed(() => byKw(byUploadAccount(uploads.value.filter((t) => t.Upload && t.Upload.IsCompleted)), upName))

// 全选（正在下载列表）
const allActiveSelected = computed(() => activeDownloads.value.length > 0 && activeDownloads.value.every((t) => selectedIds.value.has(t.id)))
function toggleSelectAllActive() {
  selectedIds.value = allActiveSelected.value ? new Set() : new Set(activeDownloads.value.map((t) => t.id))
}

const downStatusText = (s) => ({ queued: '排队中', downloading: '下载中', paused: '已暂停', completed: '已完成', failed: '失败', canceled: '已取消' }[s] || s)
const downStatusBadge = (s) => ({ downloading: 'primary', queued: 'primary', completed: 'success', failed: 'error', canceled: 'warn', paused: 'warn' }[s] || '')

function upStatus(t) {
  const u = t.Upload || {}
  if (u.IsCompleted) return '已完成'
  if (u.IsFailed) return '失败'
  if (u.IsStop) return '已停止'
  if (u.IsDowning) return '上传中'
  return '排队中'
}
const upStatusBadge = (t) => ({ 已完成: 'success', 失败: 'error', 已停止: 'warn' }[upStatus(t)] || 'primary')
const upName = (t) => (t.Info && t.Info.name) || ((t.Info && t.Info.localFilePath) || '').split(/[\\/]/).pop() || t.UploadID
const upSize = (t) => (t.Info && (t.Info.sizeStr || formatBytes(t.Info.size))) || ''
const upSpeed = (t) => (t.Upload && (t.Upload.DownSpeedStr || formatSpeed(t.Upload.DownSpeed || 0))) || ''
const upErr = (t) => (t.Upload && t.Upload.failedMessage) || ''

// ---------- 选择 / 批量 ----------
const selectedIds = ref(new Set())
function toggleSelect(id) {
  const s = new Set(selectedIds.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selectedIds.value = s
}
function onItemClick(e, id) {
  if (e.ctrlKey || e.metaKey) toggleSelect(id)
  else selectedIds.value = new Set([id])
}
const selectedTasks = computed(() => activeDownloads.value.filter((t) => selectedIds.value.has(t.id)))

// ---------- 核心操作（全部配备即时乐观响应与回滚） ----------

// 暂停
async function pauseTask(t) {
  const orig = t.status
  t.status = 'paused'
  try {
    await PauseDownload(t.id)
    await refresh()
    emit('toast', '已暂停', 'warn')
  } catch (e) {
    t.status = orig
    await refresh()
    emit('toast', '操作失败: ' + String(e), 'error')
  }
}

// 继续/重试
async function resumeTask(t) {
  const orig = t.status
  t.status = 'queued'
  try {
    await ResumeDownload(t.id)
    await refresh()
    emit('toast', '已恢复下载', 'success')
  } catch (e) {
    t.status = orig
    await refresh()
    emit('toast', '操作失败: ' + String(e), 'error')
  }
}

// 取消下载
async function cancelTask(t) {
  const orig = t.status
  t.status = 'canceled'
  try {
    await CancelDownload(t.id)
    await refresh()
    emit('toast', '已取消下载', 'warn')
  } catch (e) {
    t.status = orig
    await refresh()
    emit('toast', '操作失败: ' + String(e), 'error')
  }
}

// 优先下载
function prioritizeTask(t) {
  askConfirm('优先下载将暂停其他所有下载任务，是否继续？', async () => {
    const origMap = new Map(activeDownloads.value.map((x) => [x.id, x.status]))
    activeDownloads.value.forEach((x) => { if (x.id !== t.id && x.status === 'downloading') x.status = 'paused' })
    t.status = 'downloading'
    try {
      await PrioritizeDownload(t.id)
      await refresh()
      emit('toast', '已优先下载该任务（其他已暂停）', 'success')
    } catch (e) {
      activeDownloads.value.forEach((x) => { if (origMap.has(x.id)) x.status = origMap.get(x.id) })
      await refresh()
      emit('toast', '操作失败: ' + String(e), 'error')
    }
  }, { okText: '优先下载', title: '优先下载' })
}

// 删除单条下载记录 (立即乐观移除)
async function removeTask(t) {
  const id = t.id
  const origList = [...downloads.value]
  downloads.value = downloads.value.filter((x) => x.id !== id)
  const s = new Set(selectedIds.value)
  s.delete(id)
  selectedIds.value = s
  try {
    await RemoveDownload(id)
    await refresh()
    emit('toast', '已删除记录', 'success')
  } catch (e) {
    downloads.value = origList
    await refresh()
    emit('toast', '删除失败: ' + String(e), 'error')
  }
}

// 打开文件 / 定位目录
const openFile = (t) => { if (t.localPath) OpenFile(t.localPath) }
const reveal = (t) => { if (t.localPath) RevealInFolder(t.localPath) }

// 全部开始 / 全部暂停
function startAll() {
  const targets = activeDownloads.value.filter((t) => t.status === 'paused' || t.status === 'failed')
  const orig = new Map(targets.map((t) => [t.id, t.status]))
  targets.forEach((t) => { t.status = 'queued' })
  let failed = 0
  Promise.all(targets.map((t) => ResumeDownload(t.id).catch(() => { failed++; return t })))
    .then(async (res) => {
      res.forEach((t) => { if (t) t.status = orig.get(t.id) })
      await refresh()
      if (failed) emit('toast', `${failed} 个任务恢复失败`, 'error')
      else if (targets.length) emit('toast', `已开始全部任务`, 'success')
    })
}

function pauseAll() {
  const targets = activeDownloads.value.filter((t) => t.status === 'downloading' || t.status === 'queued')
  const orig = new Map(targets.map((t) => [t.id, t.status]))
  targets.forEach((t) => { t.status = 'paused' })
  let failed = 0
  Promise.all(targets.map((t) => PauseDownload(t.id).catch(() => { failed++; return t })))
    .then(async (res) => {
      res.forEach((t) => { if (t) t.status = orig.get(t.id) })
      await refresh()
      if (failed) emit('toast', `${failed} 个任务暂停失败`, 'error')
      else if (targets.length) emit('toast', `已暂停全部任务`, 'warn')
    })
}

// 批量操作 (继续/暂停/取消/删除)
function batchResume() {
  const tasks = [...selectedTasks.value]
  const orig = new Map(tasks.map((t) => [t.id, t.status]))
  tasks.forEach((t) => { t.status = 'queued' })
  let failed = 0
  Promise.all(tasks.map((t) => ResumeDownload(t.id).catch(() => { failed++; return t })))
    .then(async (res) => {
      res.forEach((t) => { if (t) t.status = orig.get(t.id) })
      await refresh()
      if (failed) emit('toast', `${failed} 个任务恢复失败`, 'error')
      else emit('toast', `已恢复 ${tasks.length} 个任务`, 'success')
    })
}

function batchPause() {
  const tasks = [...selectedTasks.value]
  const orig = new Map(tasks.map((t) => [t.id, t.status]))
  tasks.forEach((t) => { t.status = 'paused' })
  let failed = 0
  Promise.all(tasks.map((t) => PauseDownload(t.id).catch(() => { failed++; return t })))
    .then(async (res) => {
      res.forEach((t) => { if (t) t.status = orig.get(t.id) })
      await refresh()
      if (failed) emit('toast', `${failed} 个任务暂停失败`, 'error')
      else emit('toast', `已暂停 ${tasks.length} 个任务`, 'warn')
    })
}

function batchCancel() {
  const tasks = [...selectedTasks.value]
  const orig = new Map(tasks.map((t) => [t.id, t.status]))
  tasks.forEach((t) => { t.status = 'canceled' })
  let failed = 0
  Promise.all(tasks.map((t) => CancelDownload(t.id).catch(() => { failed++; return t })))
    .then(async (res) => {
      res.forEach((t) => { if (t) t.status = orig.get(t.id) })
      await refresh()
      if (failed) emit('toast', `${failed} 个任务取消失败`, 'error')
      else emit('toast', `已取消 ${tasks.length} 个任务`, 'warn')
    })
}

function batchRemove() {
  const tasks = [...selectedTasks.value]
  const ids = new Set(tasks.map((t) => t.id))
  const origList = [...downloads.value]
  downloads.value = downloads.value.filter((t) => !ids.has(t.id))
  selectedIds.value = new Set()
  let failed = 0
  Promise.all(Array.from(ids).map((id) => RemoveDownload(id).catch(() => { failed++ })))
    .then(async () => {
      if (failed) {
        downloads.value = origList
        await refresh()
        emit('toast', `${failed} 项记录删除失败`, 'error')
      } else {
        await refresh()
        emit('toast', `已删除 ${ids.size} 项记录`, 'success')
      }
    })
}

// 清除所有已完成下载
async function clearAllDoneDownloads() {
  downloads.value = downloads.value.filter((t) => t.status !== 'completed')
  try {
    await ClearDownloads()
    await refresh()
    emit('toast', '已清除所有已完成记录', 'success')
  } catch (e) {
    await refresh()
    emit('toast', '清除失败: ' + String(e), 'error')
  }
}

// 清除所有已上传记录
async function clearAllDoneUploads() {
  uploads.value = uploads.value.filter((t) => !t.Upload || !t.Upload.IsCompleted)
  try {
    await ClearUploads()
    await refresh()
    emit('toast', '已清除所有已上传记录', 'success')
  } catch (e) {
    await refresh()
    emit('toast', '清除失败: ' + String(e), 'error')
  }
}

// 取消/重试上传
async function cancelUploadTask(t) {
  const id = t.UploadID
  const origList = [...uploads.value]
  uploads.value = uploads.value.filter((x) => x.UploadID !== id)
  try {
    await CancelUpload(id)
    await refresh()
    emit('toast', '已取消上传', 'warn')
  } catch (e) {
    uploads.value = origList
    await refresh()
    emit('toast', '取消失败: ' + String(e), 'error')
  }
}

async function resumeUploadTask(t) {
  try {
    await ResumeUpload(t.UploadID)
    emit('toast', '已重新排队上传', 'success')
    await refresh()
  } catch (e) {
    await refresh()
    emit('toast', String(e), 'error')
  }
}

const copiedLinkMap = ref({})
async function copyLink(t) {
  if (!t.url) return
  const ok = await copyText(t.url)
  if (ok && t.id) {
    copiedLinkMap.value[t.id] = true
    setTimeout(() => {
      const m = { ...copiedLinkMap.value }
      delete m[t.id]
      copiedLinkMap.value = m
    }, 1600)
  }
  emit('toast', ok ? '链接已复制' : '复制失败', ok ? 'success' : 'error')
}

// ---------- 右键菜单 ----------
const ctx = ref({ show: false, x: 0, y: 0, task: null })
const confirmDialog = ref(null)
function askConfirm(message, onOk, opts) {
  confirmDialog.value = { message, onOk, okText: opts?.okText || '确定', danger: opts?.danger || false, title: opts?.title || '确认操作' }
}
function closeConfirm() { confirmDialog.value = null }
function handleConfirmOk() {
  if (!confirmDialog.value) return
  const cb = confirmDialog.value.onOk
  closeConfirm()
  cb && cb()
}

function onCtx(e, t) {
  ctx.value = { show: true, x: e.clientX, y: e.clientY, task: t }
}
const ctxItems = computed(() => {
  const t = ctx.value.task
  if (!t) return []
  const targets = selectedIds.value.has(t.id) && selectedTasks.value.length > 1 ? selectedTasks.value : [t]
  const items = []
  if (t.status === 'completed') {
    if (t.localPath) items.push({ icon: 'play', label: '打开文件', action: 'open' })
    if (t.localPath) items.push({ icon: 'folder', label: '打开所在目录', action: 'reveal' })
    items.push({ icon: 'link', label: '复制链接', disabled: !t.url, action: 'copy' })
    items.push({ sep: true })
    items.push({ icon: 'trash', label: '删除记录', danger: true, action: 'remove' })
    return items
  }
  if (targets.some((x) => x.status === 'paused' || x.status === 'failed'))
    items.push({ icon: 'play', label: targets.length > 1 ? `继续 (${targets.length})` : '继续', action: 'resume' })
  if (targets.some((x) => x.status === 'downloading' || x.status === 'queued'))
    items.push({ icon: 'pause', label: '暂停', action: 'pause' })
  if (targets.some((x) => x.status !== 'completed'))
    items.push({ icon: 'priority', label: '优先下载', action: 'prioritize' })
  items.push({ icon: 'x-circle', label: '取消', danger: true, action: 'cancel' })
  items.push({ sep: true })
  if (t.localPath) items.push({ icon: 'folder', label: '打开所在目录', action: 'reveal' })
  items.push({ icon: 'link', label: '复制链接', disabled: !t.url, action: 'copy' })
  items.push({ icon: 'info', label: '任务详情', action: 'detail' })
  return items
})

// ---------- 任务详情 ----------
const detailTask = ref(null)
const statusText = (s) => ({ queued: '排队中', downloading: '下载中', paused: '已暂停', completed: '已完成', failed: '失败', canceled: '已取消', uploading: '上传中' }[s] || s)

function onCtxSelect(action) {
  const t = ctx.value.task
  if (!t) return
  const targets = selectedIds.value.has(t.id) && selectedTasks.value.length > 1 ? selectedTasks.value : [t]
  if (action === 'resume') targets.forEach((x) => resumeTask(x))
  else if (action === 'pause') targets.forEach((x) => pauseTask(x))
  else if (action === 'prioritize') prioritizeTask(t)
  else if (action === 'cancel') targets.forEach((x) => cancelTask(x))
  else if (action === 'remove') removeTask(t)
  else if (action === 'open') openFile(t)
  else if (action === 'reveal') reveal(t)
  else if (action === 'copy') copyLink(t)
  else if (action === 'detail') detailTask.value = t
}

// ---------- 新建下载 ----------
const dlModal = ref(false)
const dlLinks = ref('')
const dlName = ref('')
const dlAdvanced = ref(false)
const dlHeaders = ref({ ua: '', referer: '', cookie: '' })
function openDlModal() {
  dlLinks.value = ''; dlName.value = ''; dlAdvanced.value = false
  dlHeaders.value = { ua: '', referer: '', cookie: '' }
  dlModal.value = true
}
const dlUrlList = computed(() => dlLinks.value.split('\n').map((s) => s.trim()).filter((s) => /^https?:\/\//i.test(s)))
async function submitDownload() {
  const urls = dlUrlList.value
  if (!urls.length) { emit('toast', '请输入有效的 http/https 链接', 'error'); return }
  dlModal.value = false
  const headers = {}
  if (dlHeaders.value.ua.trim()) headers['User-Agent'] = dlHeaders.value.ua.trim()
  if (dlHeaders.value.referer.trim()) headers['Referer'] = dlHeaders.value.referer.trim()
  if (dlHeaders.value.cookie.trim()) headers['Cookie'] = dlHeaders.value.cookie.trim()
  for (const url of urls) {
    const name = urls.length === 1 ? dlName.value.trim() : ''
    try { await DownloadURL(name, url, headers) } catch (e) { emit('toast', String(e && e.message ? e.message : e), 'error') }
  }
  emit('toast', `已添加 ${urls.length} 个下载任务`, 'success')
  refresh()
}

// ---------- 云离线 ----------
const offlineUser = ref('')
const offlineTasks = ref([])
const offlineLoading = ref(false)
const offlineError = ref('')
let offlineRefreshSeq = 0
const offModal = ref(false)
const offLinks = ref('')
let offlineTimer = null

watch(offlineAccounts, (list) => {
  if (!offlineUser.value && list.length) offlineUser.value = list[0].user_id
}, { immediate: true })

watch(() => props.accounts, (list) => {
  if (filterUser.value && !list.some((a) => a.user_id === filterUser.value)) filterUser.value = ''
  if (offlineUser.value && !list.some((a) => a.user_id === offlineUser.value)) offlineUser.value = ''
}, { deep: true })

const offlineDriveId = computed(() => {
  const acc = offlineAccounts.value.find((a) => a.user_id === offlineUser.value)
  return acc ? acc.drive_id : ''
})

async function refreshOffline() {
  const seq = ++offlineRefreshSeq
  const userID = offlineUser.value
  if (!userID) { offlineTasks.value = []; offlineError.value = ''; return }
  offlineLoading.value = true
  try {
    const list = (await ListOfflineTasks(userID)) || []
    if (seq !== offlineRefreshSeq || userID !== offlineUser.value) return
    offlineTasks.value = list
    offlineError.value = ''
  } catch (e) {
    if (seq === offlineRefreshSeq && userID === offlineUser.value) offlineError.value = String(e && e.message ? e.message : e)
  } finally {
    if (seq === offlineRefreshSeq) offlineLoading.value = false
  }
}
watch(offlineUser, refreshOffline)
watch(menu, (m) => { if (m === 'offline') refreshOffline() })

const offUrlList = computed(() => offLinks.value.split('\n').map((s) => s.trim()).filter((s) => /^https?:\/\//i.test(s) || /^magnet:/i.test(s) || /^ed2k:/i.test(s)))
async function submitOffline() {
  const urls = offUrlList.value
  if (!urls.length) { emit('toast', '请输入有效的链接', 'error'); return }
  offModal.value = false
  let ok = 0
  for (const url of urls) {
    try { await OfflineDownload(offlineUser.value, offlineDriveId.value, url, ''); ok++ }
    catch (e) { emit('toast', String(e && e.message ? e.message : e), 'error') }
  }
  if (ok) emit('toast', `已提交 ${ok} 个离线任务`, 'success')
  offLinks.value = ''
  refreshOffline()
}
const offProgress = (t) => Math.max(0, Math.min(100, Math.round(t.progress || 0)))

async function delOfflineTask(t) {
  askConfirm(`删除离线任务「${t.file_name || t.url || t.task_id}」？`, async () => {
    // 乐观剔除
    offlineTasks.value = offlineTasks.value.filter((x) => x.task_id !== t.task_id && x.id !== t.id)
    try {
      await DeleteOfflineTask(offlineUser.value, offlineDriveId.value, t.task_id || t.id, true)
      await refreshOffline()
      emit('toast', '已删除', 'success')
    } catch (e) { await refreshOffline(); emit('toast', String(e), 'error') }
  }, { danger: true, title: '删除离线任务' })
}

// ---------- 迁移 ----------
const migrateJobs = ref([])
const migrateLoading = ref(false)
let migrateRefreshSeq = 0
function onMigrate(job) {
  if (!job || !job.id) return
  const i = migrateJobs.value.findIndex((j) => j.id === job.id)
  if (i >= 0) migrateJobs.value.splice(i, 1, job)
  else migrateJobs.value.unshift(job)
}
async function refreshMigrateJobs() {
  const seq = ++migrateRefreshSeq
  migrateLoading.value = true
  try {
    const list = await ListMigrateJobs()
    if (seq === migrateRefreshSeq) migrateJobs.value = Array.isArray(list) ? list : []
  } catch (e) {
    if (seq === migrateRefreshSeq && !migrateJobs.value.length) emit('toast', '迁移任务加载失败: ' + String(e), 'error')
  } finally {
    if (seq === migrateRefreshSeq) migrateLoading.value = false
  }
}
async function cancelMigrateJob(job) {
  if (!job || !job.id || !['pending', 'running'].includes(job.status)) return
  try {
    await CancelMigrate(job.id)
    job.message = '正在取消…'
    await refreshMigrateJobs()
    setTimeout(refreshMigrateJobs, 150)
    emit('toast', '已请求取消迁移', 'warn')
  } catch (e) {
    emit('toast', '取消迁移失败: ' + String(e), 'error')
  }
}
async function resumeMigrateJob(job) {
  if (!job || !job.id || !['partial', 'failed', 'canceled'].includes(job.status)) return
  try {
    const updated = await ResumeMigrate(job.id)
    if (updated) onMigrate(updated)
    emit('toast', '已恢复迁移，将跳过已完成的资源', 'success')
  } catch (e) {
    await refreshMigrateJobs()
    emit('toast', '恢复迁移失败: ' + String(e), 'error')
  }
}
async function removeMigrateJob(job) {
  if (!job || !job.id || ['pending', 'running'].includes(job.status)) return
  const original = migrateJobs.value
  migrateJobs.value = original.filter((x) => x.id !== job.id)
  try {
    await DeleteMigrateJob(job.id)
    await refreshMigrateJobs()
    emit('toast', '已删除迁移记录', 'success')
  } catch (e) {
    migrateJobs.value = original
    await refreshMigrateJobs()
    emit('toast', '删除迁移记录失败: ' + String(e), 'error')
  }
}
async function clearMigrateHistory() {
  const original = migrateJobs.value
  migrateJobs.value = original.filter((x) => ['pending', 'running'].includes(x.status))
  try {
    await ClearMigrateJobs()
    await refreshMigrateJobs()
    emit('toast', '已清除迁移历史', 'success')
  } catch (e) {
    migrateJobs.value = original
    await refreshMigrateJobs()
    emit('toast', '清除迁移历史失败: ' + String(e), 'error')
  }
}
const migName = (uid) => {
  const acc = props.accounts.find((a) => a.user_id === uid)
  return acc ? accountName(acc) : uid
}
const migIcon = (uid) => providerIconUrl(providerMetaOf(props.accounts.find(a => a.user_id === uid) || { user_id: uid }, props.providers))
const migBadge = (s) => ({ completed: 'success', failed: 'error', running: 'primary' }[s] || 'warn')
const migStatusText = (s) => ({ pending: '等待中', running: '迁移中', completed: '已完成', partial: '部分完成', failed: '失败', canceled: '已取消' }[s] || s)
const migCompletedTopLevel = (j) => (j.fileIDs || []).filter((id) => (j.completedFileIDs || []).includes(id)).length
const migRemaining = (j) => Math.max(0, (j.fileIDs || []).length - migCompletedTopLevel(j))
const migProgress = (j) => j.totalBytes > 0
  ? Math.min(100, Math.round(((j.processedBytes || 0) / j.totalBytes) * 100))
  : (j.total ? Math.min(100, Math.round(((j.processed || 0) / j.total) * 100)) : 0)
const migProgressText = (j) => j.totalBytes > 0
  ? `${formatBytes(j.processedBytes || 0)} / ${formatBytes(j.totalBytes || 0)}`
  : `${j.processed || 0} / ${j.total || 0} 个文件`

// ---------- 生命周期 ----------
let pollTimer = null
let offFns = []
let viewActive = false

function bindEvents() {
  if (offFns.length) return
  const off1 = EventsOn('transfer:event', onTransferEvent)
  const off2 = EventsOn('migrate:progress', onMigrate)
  if (typeof off1 === 'function') offFns.push(off1)
  if (typeof off2 === 'function') offFns.push(off2)
}

function unbindEvents() {
  offFns.forEach((fn) => { try { fn() } catch { /* 忽略 */ } })
  offFns = []
}

function stopPolling() {
  clearTimeout(pollTimer)
  clearTimeout(offlineTimer)
  pollTimer = null
  offlineTimer = null
}

function startPolling() {
  stopPolling()
  const pollTransfers = async () => {
    if (!viewActive) return
    if (!document.hidden && (activeDownloads.value.length || activeUploads.value.length || menu.value === 'migrate')) {
      await refresh()
      if (menu.value === 'migrate') await refreshMigrateJobs()
    }
    pollTimer = setTimeout(pollTransfers, activeDownloads.value.length || activeUploads.value.length || menu.value === 'migrate' ? 5000 : 15000)
  }
  const pollOffline = async () => {
    if (!viewActive) return
    if (!document.hidden && menu.value === 'offline') await refreshOffline()
    offlineTimer = setTimeout(pollOffline, menu.value === 'offline' ? 8000 : 20000)
  }
  pollTimer = setTimeout(pollTransfers, 5000)
  offlineTimer = setTimeout(pollOffline, 8000)
}

function activateView() {
  if (viewActive) return
  viewActive = true
  bindEvents()
  refresh()
  refreshMigrateJobs()
  startPolling()
}

function deactivateView() {
  if (!viewActive) return
  viewActive = false
  stopPolling()
  clearTimeout(refreshTimer)
  refreshTimer = null
  refreshQueued = false
  refreshSeq++
  unbindEvents()
}

onMounted(activateView)
onActivated(activateView)
onDeactivated(deactivateView)
onBeforeUnmount(() => {
  deactivateView()
  clearTimeout(taskFilterTimer)
})
</script>

<template>
  <div class="page">
    <Modal v-if="detailJob" title="迁移详情" width="760px" @close="detailID = ''">
      <p class="account-inline"><img :src="migIcon(detailJob.srcUser)" alt="" />{{ migName(detailJob.srcUser) }} → <img :src="migIcon(detailJob.dstUser)" alt="" />{{ migName(detailJob.dstUser) }}</p>
      <p>目标目录：{{ detailJob.dstParent }} · {{ migStatusText(detailJob.status) }}</p>
      <div class="workspace-results"><div v-for="item in Object.values(detailJob.items || {})" :key="item.id" class="workspace-result"><span><strong>{{ item.name }}</strong><small>{{ migStatusText(item.status) }} · {{ verificationLabels[item.verification] || '未校验' }}</small><small v-if="item.error">{{ item.error }}</small></span></div><p v-if="!Object.keys(detailJob.items || {}).length">旧任务没有逐项记录，恢复任务后会记录详情。</p></div>
      <p class="hint">大小核对只验证传输字节数；服务商接受哈希不等于重新读取目标文件校验。</p>
      <button class="btn" :disabled="verifying || ['running','pending'].includes(detailJob.status)" @click="verifyMigration">{{ verifying ? '校验中…' : '校验目标' }}</button>
      <p v-for="item in Object.values(verification)" :key="item.id" :style="item.status === 'mismatch' ? {color:'var(--color-error)'} : {}">{{ detailJob.items?.[item.id]?.name || item.id }}：{{ item.detail }}</p>
      <template #actions><button class="btn" @click="emit('navigate', {userId:detailJob.dstUser,dirId:detailJob.dstParent,name:'迁移目标'})">打开目标</button><button v-if="['failed','partial','canceled'].includes(detailJob.status)" class="btn primary" @click="resumeMigrateJob(detailJob)">重试未完成</button></template>
    </Modal>
    <div class="down-layout">
      <!-- 左侧边栏 -->
      <aside class="down-side">
        <div
          v-for="m in menus" :key="m.key"
          class="down-menu-item" :class="{ active: menu === m.key }"
          @click="menu = m.key"
        >
          <span style="width:15px;display:inline-flex"><UiIcon :name="m.icon" :size="15" /></span><span>{{ m.label }}</span>
          <span v-if="m.cnt" class="cnt">{{ m.cnt }}</span>
        </div>

        <div class="side-label">账号筛选</div>
        <div class="down-filter" :class="{ active: !filterUser }" @click="filterUser = ''">
          <span style="width:15px;display:inline-flex"><UiIcon name="globe" :size="15" /></span>
          <span class="df-name">全部账号</span>
        </div>
        <div
          v-for="acc in accounts" :key="acc.user_id"
          class="down-filter" :class="{ active: filterUser === acc.user_id }"
          @click="filterUser = acc.user_id"
        >
          <img v-if="accIcon(acc)" :src="accIcon(acc)" alt="" />
          <span v-else style="width:15px;display:inline-flex"><UiIcon name="drive" :size="15" /></span>
          <span class="df-name">{{ accLabel(acc) }}</span>
        </div>
      </aside>

      <!-- 右侧内容区 -->
      <section class="down-content">
        <div v-if="refreshError" class="transfer-error" role="alert">
          <UiIcon name="alert" :size="15" />
          <span>传输列表刷新失败：{{ refreshError }}</span>
          <button type="button" class="transfer-error-retry" @click="refresh">重试</button>
        </div>
        <!-- 正在下载 -->
        <template v-if="menu === 'downloading'">
          <div class="toppanbtns">
            <div class="toppanbtn">
              <button class="tbtn primary" @click="openDlModal"><UiIcon name="plus" :size="14" />新建下载</button>
            </div>
            <div class="toppanbtn" v-if="!selectedIds.size">
              <button class="tbtn" :disabled="!activeDownloads.length" @click="startAll"><UiIcon name="play" :size="14" />开始全部</button>
              <button class="tbtn" :disabled="!activeDownloads.length" @click="pauseAll"><UiIcon name="pause" :size="14" />暂停全部</button>
              <button class="tbtn danger" @click="askConfirm('清除所有已完成的下载记录？', clearAllDoneDownloads, { danger: true, title: '清除已完成' })"><UiIcon name="trash" :size="14" />清除已完成</button>
            </div>
            <div class="toppanbtn" v-else>
              <button class="tbtn" @click="batchResume"><UiIcon name="play" :size="14" />继续 ({{ selectedTasks.length }})</button>
              <button class="tbtn" @click="batchPause"><UiIcon name="pause" :size="14" />暂停 ({{ selectedTasks.length }})</button>
              <button class="tbtn danger" @click="batchCancel"><UiIcon name="x-circle" :size="14" />取消 ({{ selectedTasks.length }})</button>
              <button class="tbtn danger" @click="batchRemove"><UiIcon name="trash" :size="14" />删除 ({{ selectedTasks.length }})</button>
              <button class="tbtn" @click="selectedIds = new Set()">清除选择</button>
            </div>
            <div class="toolbar-spacer"></div>
            <div class="toppanbtn">
              <span class="search-quick-wrap">
                <span class="sq-icon"><UiIcon name="search" :size="13" /></span>
                <input class="search-quick" v-model="taskFilterRaw" placeholder="快速筛选" />
                <button v-if="taskFilterRaw" class="sq-clear" title="清空筛选" @click="taskFilterRaw = ''"><UiIcon name="close" :size="11" /></button>
              </span>
            </div>
          </div>

          <div class="toppanarea">
            <div class="sel-all-wrap">
              <button class="btn-circle" :class="{ on: allActiveSelected }" title="全选" @click="toggleSelectAllActive">
                <UiIcon :name="allActiveSelected ? 'minus' : 'check'" :size="15" />
              </button>
            </div>
            <div class="selectInfo">{{ selectedIds.size ? `已选 ${selectedIds.size} 项 / ` : '' }}共 {{ activeDownloads.length }} 项</div>
            <div class="toolbar-spacer"></div>
            <div class="cell" style="width:88px">大小</div>
            <div class="cell" style="width:212px">进度</div>
            <div class="cell" style="width:86px">速度</div>
            <div class="cell" style="width:80px">操作</div>
          </div>

          <div class="file-list">
            <template v-if="loading && !activeDownloads.length">
              <div v-for="i in 5" :key="i" class="skeleton-row">
                <div class="skeleton skeleton-icon"></div>
                <div class="skeleton skeleton-name" :style="{ width: (45 + (i * 9) % 30) + '%' }"></div>
                <div class="skeleton skeleton-size"></div>
                <div class="skeleton skeleton-date"></div>
              </div>
            </template>
            <template v-else>
              <transition-group name="task-list">
                <div
                  v-for="t in activeDownloads" :key="t.id"
                  class="taskrow" :class="{ selected: selectedIds.has(t.id) }"
                  @click="onItemClick($event, t.id)"
                  @contextmenu.prevent="onCtx($event, t)"
                >
                  <div class="rangselect">
                    <button class="btn-circle" :class="{ on: selectedIds.has(t.id) }" tabindex="-1" @click.stop="toggleSelect(t.id)">
                      <UiIcon :name="selectedIds.has(t.id) ? 'check' : 'plus'" :size="13" />
                    </button>
                  </div>
                  <div class="fileicon" :class="'ft-' + iconOf({ name: t.name })"><UiIcon :name="iconOf({ name: t.name })" :size="20" /></div>
                  <div class="filename">
                    <div :title="t.localPath || t.url || ''">{{ t.name || t.url || t.id }}</div>
                    <div class="fsub">{{ t.localPath || t.url || '' }}</div>
                  </div>
                  <div class="filesize">{{ formatBytes(t.size) }}</div>
                  <div class="downprogress">
                    <div class="transfering-state">
                      <p class="text-state"><span class="badge" :class="downStatusBadge(t.status)">{{ downStatusText(t.status) }}</span><span>{{ t.progress || 0 }}%</span></p>
                      <div class="progress-total">
                        <div :class="t.status === 'downloading' || t.status === 'queued' ? 'progress-current active' : t.status === 'completed' ? 'progress-current succeed' : t.status === 'failed' ? 'progress-current error' : 'progress-current'" :style="{ width: (t.progress || 0) + '%' }"></div>
                      </div>
                      <p v-if="t.status === 'failed' && t.error" class="text-error" :title="t.error">{{ t.error }}</p>
                    </div>
                  </div>
                  <div class="downspeed">{{ t.status === 'downloading' ? formatSpeed(t.speed || 0) : '' }}</div>
                  <div class="tactions" @click.stop>
                    <button v-if="t.status !== 'completed'" class="btn-circle" title="优先下载（暂停其他任务）" @click="prioritizeTask(t)"><UiIcon name="priority" :size="14" /></button>
                    <button v-if="t.status === 'downloading' || t.status === 'queued'" class="btn-circle" title="暂停" @click="pauseTask(t)"><UiIcon name="pause" :size="14" /></button>
                    <button v-else-if="t.status === 'paused' || t.status === 'failed'" class="btn-circle" :title="t.status === 'failed' ? '重试' : '继续'" @click="resumeTask(t)"><UiIcon name="play" :size="14" /></button>
                    <button v-if="t.localPath" class="btn-circle" title="打开所在目录" @click="reveal(t)"><UiIcon name="folder" :size="14" /></button>
                    <button class="btn-circle" title="取消" style="color:var(--color-error)" @click="cancelTask(t)"><UiIcon name="x-circle" :size="14" /></button>
                    <button class="btn-circle" title="删除记录" style="color:var(--color-error)" @click="removeTask(t)"><UiIcon name="trash" :size="14" /></button>
                  </div>
                </div>
              </transition-group>
              <div v-if="!activeDownloads.length" class="workspace-empty-state">
                <UiIcon name="download" :size="36" style="opacity:.4" />
                <span class="wes-title">暂无正在下载的任务</span>
              </div>
            </template>
          </div>
        </template>

        <!-- 已下载 -->
        <template v-else-if="menu === 'downloaded'">
          <div class="toppanbtns">
            <div class="toppanbtn">
              <button class="tbtn danger" :disabled="!doneDownloads.length" @click="askConfirm('清除所有已下载记录？', clearAllDoneDownloads, { danger: true, title: '清除全部' })"><UiIcon name="trash" :size="14" />清除全部已完成</button>
            </div>
            <div class="toolbar-spacer"></div>
            <div class="toppanbtn">
              <span class="search-quick-wrap">
                <span class="sq-icon"><UiIcon name="search" :size="13" /></span>
                <input class="search-quick" v-model="taskFilterRaw" placeholder="快速筛选" />
                <button v-if="taskFilterRaw" class="sq-clear" title="清空筛选" @click="taskFilterRaw = ''"><UiIcon name="close" :size="11" /></button>
              </span>
            </div>
          </div>
          <div class="file-list">
            <transition-group name="task-list">
              <div v-for="t in doneDownloads" :key="t.id" class="taskrow" @contextmenu.prevent="onCtx($event, t)">
                <div class="rangselect"></div>
                <div class="fileicon" :class="'ft-' + iconOf({ name: t.name })"><UiIcon :name="iconOf({ name: t.name })" :size="20" /></div>
                <div class="filename">
                  <div :title="t.localPath || ''">{{ t.name || t.url || t.id }}</div>
                  <div class="fsub">{{ t.localPath || '' }}<template v-if="t.updated"> · {{ formatTime(t.updated) }}</template></div>
                </div>
                <div class="filesize">{{ formatBytes(t.size) }}</div>
                <div class="downprogress">
                  <div class="transfering-state">
                    <p class="text-state"><span>已完成</span><span>100%</span></p>
                    <div class="progress-total"><div class="progress-current succeed" style="width:100%"></div></div>
                  </div>
                </div>
                <div class="downspeed"></div>
                <div class="tactions">
                  <button v-if="t.localPath" class="btn-circle" title="打开文件" @click="openFile(t)"><UiIcon name="play" :size="14" /></button>
                  <button v-if="t.localPath" class="btn-circle" title="打开所在目录" @click="reveal(t)"><UiIcon name="folder" :size="14" /></button>
                  <button v-if="t.url" class="btn-circle" title="复制链接" @click="copyLink(t)">
                    <UiIcon v-if="copiedLinkMap[t.id]" name="check" :size="14" class="icon-check-pop" />
                    <UiIcon v-else name="link" :size="14" />
                  </button>
                  <button class="btn-circle" title="删除记录" style="color:var(--color-error)" @click="removeTask(t)"><UiIcon name="trash" :size="14" /></button>
                </div>
              </div>
            </transition-group>
            <div v-if="!doneDownloads.length" class="workspace-empty-state">
              <UiIcon name="check" :size="36" style="opacity:.4" />
              <span class="wes-title">暂无已下载记录</span>
            </div>
          </div>
        </template>

        <!-- 正在上传 -->
        <template v-else-if="menu === 'uploading'">
          <div class="toppanbtns">
            <div class="toolbar-spacer"></div>
            <div class="toppanbtn">
              <span class="search-quick-wrap">
                <span class="sq-icon"><UiIcon name="search" :size="13" /></span>
                <input class="search-quick" v-model="taskFilterRaw" placeholder="快速筛选" />
                <button v-if="taskFilterRaw" class="sq-clear" title="清空筛选" @click="taskFilterRaw = ''"><UiIcon name="close" :size="11" /></button>
              </span>
            </div>
          </div>
          <div class="file-list">
            <transition-group name="task-list">
              <div v-for="t in activeUploads" :key="t.UploadID" class="taskrow">
                <div class="rangselect"></div>
                <div class="fileicon" :class="'ft-' + iconOf({ name: upName(t) })"><UiIcon :name="iconOf({ name: upName(t) })" :size="20" /></div>
                <div class="filename">
                  <div>{{ upName(t) }}</div>
                  <div class="fsub">{{ (t.Info && t.Info.localFilePath) || '' }}</div>
                </div>
                <div class="filesize">{{ upSize(t) }}</div>
                <div class="downprogress">
                  <div class="transfering-state">
                    <p class="text-state"><span class="badge" :class="upStatusBadge(t)">{{ upStatus(t) }}</span><span>{{ t.Upload && t.Upload.DownProgress ? t.Upload.DownProgress : 0 }}%</span></p>
                    <div class="progress-total">
                      <div class="progress-current active" :style="{ width: (t.Upload && t.Upload.DownProgress ? t.Upload.DownProgress : 0) + '%' }"></div>
                    </div>
                    <p v-if="upErr(t)" class="text-error" :title="upErr(t)">{{ upErr(t) }}</p>
                  </div>
                </div>
                <div class="downspeed">{{ upSpeed(t) }}</div>
                <div class="tactions">
                  <button v-if="t.Upload && (t.Upload.IsFailed || t.Upload.IsStop)" class="btn-circle" title="重试上传" @click="resumeUploadTask(t)"><UiIcon name="refresh" :size="14" /></button>
                  <button class="btn-circle" title="取消上传" style="color:var(--color-error)" @click="cancelUploadTask(t)"><UiIcon name="x-circle" :size="14" /></button>
                </div>
              </div>
            </transition-group>
            <div v-if="!activeUploads.length" class="workspace-empty-state">
              <UiIcon name="upload" :size="36" style="opacity:.4" />
              <span class="wes-title">暂无正在上传的任务</span>
            </div>
          </div>
        </template>

        <!-- 已上传 -->
        <template v-else-if="menu === 'uploaded'">
          <div class="toppanbtns">
            <div class="toppanbtn">
              <button class="tbtn danger" :disabled="!doneUploads.length" @click="askConfirm('清除所有已上传记录？', clearAllDoneUploads, { danger: true, title: '清除全部' })"><UiIcon name="trash" :size="14" />清除全部已上传</button>
            </div>
            <div class="toolbar-spacer"></div>
            <div class="toppanbtn">
              <span class="search-quick-wrap">
                <span class="sq-icon"><UiIcon name="search" :size="13" /></span>
                <input class="search-quick" v-model="taskFilterRaw" placeholder="快速筛选" />
                <button v-if="taskFilterRaw" class="sq-clear" title="清空筛选" @click="taskFilterRaw = ''"><UiIcon name="close" :size="11" /></button>
              </span>
            </div>
          </div>
          <div class="file-list">
            <transition-group name="task-list">
              <div v-for="t in doneUploads" :key="t.UploadID" class="taskrow">
                <div class="rangselect"></div>
                <div class="fileicon" :class="'ft-' + iconOf({ name: upName(t) })"><UiIcon :name="iconOf({ name: upName(t) })" :size="20" /></div>
                <div class="filename">
                  <div>{{ upName(t) }}</div>
                  <div class="fsub">{{ t.Info && t.Info.path ? t.Info.path : '' }}</div>
                </div>
                <div class="filesize">{{ upSize(t) }}</div>
                <div class="downprogress">
                  <div class="transfering-state">
                    <p class="text-state"><span>已完成</span><span>100%</span></p>
                    <div class="progress-total"><div class="progress-current succeed" style="width:100%"></div></div>
                  </div>
                </div>
                <div class="downspeed"></div>
                <div class="tactions">
                  <button class="btn-circle" title="删除记录" style="color:var(--color-error)" @click="cancelUploadTask(t)"><UiIcon name="trash" :size="14" /></button>
                </div>
              </div>
            </transition-group>
            <div v-if="!doneUploads.length" class="workspace-empty-state">
              <UiIcon name="check" :size="36" style="opacity:.4" />
              <span class="wes-title">暂无已上传记录</span>
            </div>
          </div>
        </template>

        <!-- 云离线 -->
        <template v-else-if="menu === 'offline'">
          <div class="toppanbtns">
            <div class="toppanbtn">
              <UiSelect
                v-model="offlineUser"
                style="width:200px"
                :options="offlineAccounts.map((acc) => ({ value: acc.user_id, label: accLabel(acc) }))"
              />
              <button class="tbtn primary" :disabled="!offlineUser" @click="offLinks = ''; offModal = true"><UiIcon name="plus" :size="14" />新建离线任务</button>
            </div>
            <div class="toolbar-spacer"></div>
            <div class="toppanbtn">
              <button class="tbtn" :disabled="offlineLoading" @click="refreshOffline">
                <span v-if="offlineLoading" class="spin"></span><template v-else><UiIcon name="refresh" :size="14" />刷新</template>
              </button>
            </div>
          </div>
          <div v-if="offlineError" class="transfer-error" role="alert">
            <UiIcon name="alert" :size="15" />
            <span>云离线任务刷新失败：{{ offlineError }}</span>
            <button type="button" class="transfer-error-retry" @click="refreshOffline">重试</button>
          </div>
          <div class="file-list">
            <transition-group name="task-list">
              <div v-for="t in offlineTasks" :key="t.id || t.task_id" class="taskrow">
                <div class="rangselect"></div>
                <div class="fileicon" :class="'ft-' + iconOf({ name: t.file_name })"><UiIcon :name="iconOf({ name: t.file_name })" :size="20" /></div>
                <div class="filename">
                  <div>{{ t.file_name || t.url }}</div>
                  <div class="fsub">{{ t.url }}<template v-if="t.created"> · {{ formatTime(t.created) }}</template></div>
                </div>
                <div class="filesize"></div>
                <div class="downprogress">
                  <div class="transfering-state">
                    <p class="text-state"><span>{{ t.status || '进行中' }}</span><span>{{ offProgress(t) }}%</span></p>
                    <div class="progress-total">
                      <div :class="offProgress(t) >= 100 ? 'progress-current succeed' : 'progress-current active'" :style="{ width: offProgress(t) + '%' }"></div>
                    </div>
                  </div>
                </div>
                <div class="downspeed"></div>
                <div class="tactions">
                  <button class="btn-circle" title="删除离线任务" style="color:var(--color-error)" @click="delOfflineTask(t)"><UiIcon name="trash" :size="14" /></button>
                </div>
              </div>
            </transition-group>
            <div v-if="!offlineTasks.length && !offlineLoading" class="workspace-empty-state">
              <UiIcon name="cloud-down" :size="36" style="opacity:.4" />
              <span class="wes-title">暂无离线任务</span>
            </div>
            <div v-if="offlineLoading && !offlineTasks.length" class="empty"><span class="spin"></span><span>加载中…</span></div>
          </div>
        </template>

        <!-- 迁移 -->
        <template v-else-if="menu === 'migrate'">
          <div class="toppanbtns">
            <div class="toppanbtn">
              <button class="tbtn danger" :disabled="!migrateJobs.some((j) => ['completed', 'partial', 'failed', 'canceled'].includes(j.status))" @click="askConfirm('清除所有已结束的迁移记录？', clearMigrateHistory, { danger: true, title: '清除迁移历史' })"><UiIcon name="trash" :size="14" />清除历史</button>
            </div>
            <div class="toolbar-spacer"></div>
            <div v-if="migrateLoading" class="toppanbtn"><span class="spin"></span></div>
          </div>
          <div class="file-list">
            <transition-group name="task-list">
              <div v-for="j in migrateJobs" :key="j.id" class="taskrow">
                <div class="rangselect"></div>
                <div class="fileicon"><UiIcon name="migrate" :size="20" style="color:var(--color-primary)" /></div>
                <div class="filename">
                  <button class="tbtn account-inline" @click="detailID = j.id"><img :src="migIcon(j.srcUser)" alt="" />{{ migName(j.srcUser) }}<span>→</span><img :src="migIcon(j.dstUser)" alt="" />{{ migName(j.dstUser) }} · 详情</button>
                  <div class="fsub">{{ (j.fileIDs || []).length }} 个文件<template v-if="j.failed"> · 失败 {{ j.failed }}</template><template v-if="['partial', 'failed', 'canceled'].includes(j.status) && migRemaining(j)"> · 可恢复 {{ migRemaining(j) }} 个</template></div>
                </div>
                <div class="filesize"></div>
                <div class="downprogress">
                  <div class="transfering-state">
                    <p class="text-state"><span class="badge" :class="migBadge(j.status)">{{ migStatusText(j.status) }}</span><span>{{ migProgress(j) }}%</span></p>
                    <div class="progress-total">
                      <div :class="j.status === 'completed' ? 'progress-current succeed' : j.status === 'failed' ? 'progress-current error' : j.status === 'partial' ? 'progress-current warning' : 'progress-current active'" :style="{ width: migProgress(j) + '%' }"></div>
                    </div>
                    <p v-if="j.message" class="text-error" :title="j.message">{{ j.message }}</p>
                  </div>
                </div>
                <div class="downspeed">{{ migProgressText(j) }}</div>
                <div class="tactions">
                  <button v-if="j.status === 'pending' || j.status === 'running'" class="btn-circle" title="取消迁移" style="color:var(--color-error)" @click="cancelMigrateJob(j)"><UiIcon name="x-circle" :size="14" /></button>
                  <template v-else>
                    <button v-if="['partial', 'failed', 'canceled'].includes(j.status) && migRemaining(j)" class="btn-circle" title="恢复未完成资源" @click="resumeMigrateJob(j)"><UiIcon name="refresh" :size="14" /></button>
                    <button class="btn-circle" title="清除记录" style="color:var(--color-error)" @click="removeMigrateJob(j)"><UiIcon name="trash" :size="14" /></button>
                  </template>
                </div>
              </div>
            </transition-group>
            <div v-if="!migrateJobs.length" class="workspace-empty-state">
              <UiIcon name="migrate" :size="36" style="opacity:.4" />
              <span class="wes-title">暂无迁移任务</span>
              <span class="wes-desc">在网盘页选中文件后，右键选择「迁移到其他网盘」</span>
            </div>
          </div>
        </template>
      </section>
    </div>
    <!-- 新建下载弹窗 -->
    <Modal v-if="dlModal" title="新建下载" width="480px" @close="dlModal = false">
      <div class="field">
        <label>下载链接（每行一个 http/https）</label>
        <textarea v-model="dlLinks" class="textarea" rows="5" placeholder="https://example.com/file.zip"></textarea>
      </div>
      <div class="field">
        <label>文件名（可选，仅单个链接时生效）</label>
        <input v-model="dlName" class="input" placeholder="留空则自动命名" :disabled="dlUrlList.length !== 1" />
      </div>
      <div class="field">
        <label style="cursor:pointer;user-select:none" @click="dlAdvanced = !dlAdvanced">
          <UiIcon :name="dlAdvanced ? 'chevron-down' : 'chevron-right'" :size="12" /> 高级选项
        </label>
        <template v-if="dlAdvanced">
          <input v-model="dlHeaders.ua" class="input" style="width:100%;margin-bottom:8px" placeholder="User-Agent（可选）" />
          <input v-model="dlHeaders.referer" class="input" style="width:100%;margin-bottom:8px" placeholder="Referer（可选）" />
          <textarea v-model="dlHeaders.cookie" class="textarea" rows="2" placeholder="Cookie（可选）" style="width:100%"></textarea>
        </template>
      </div>
      <template #actions>
        <button class="btn" @click="dlModal = false">取消</button>
        <button class="btn primary" :disabled="!dlUrlList.length" @click="submitDownload">开始下载{{ dlUrlList.length > 1 ? ` (${dlUrlList.length})` : '' }}</button>
      </template>
    </Modal>

    <!-- 新建离线任务弹窗 -->
    <Modal v-if="offModal" title="新建离线任务" width="480px" @close="offModal = false">
      <div class="field">
        <label>链接（每行一个，支持 http/https/magnet/ed2k）</label>
        <textarea v-model="offLinks" class="textarea" rows="6" placeholder="magnet:?xt=urn:btih:…"></textarea>
      </div>
      <template #actions>
        <button class="btn" @click="offModal = false">取消</button>
        <button class="btn primary" :disabled="!offUrlList.length" @click="submitOffline">提交{{ offUrlList.length > 1 ? ` (${offUrlList.length})` : '' }}</button>
      </template>
    </Modal>

    <!-- 右键菜单 -->
    <ContextMenu
      v-if="ctx.show"
      :x="ctx.x" :y="ctx.y" :items="ctxItems"
      @close="ctx.show = false"
      @select="onCtxSelect"
    />

    <!-- 任务详情弹窗 -->
    <Modal v-if="detailTask" title="任务详情" width="440px" @close="detailTask = null">
      <div class="td-row"><span class="td-label">文件名</span><span class="td-val">{{ detailTask.name }}</span></div>
      <div class="td-row"><span class="td-label">任务 ID</span><span class="td-val">{{ detailTask.id }}</span></div>
      <div class="td-row"><span class="td-label">保存路径</span><span class="td-val">{{ detailTask.localPath || '-' }}</span></div>
      <div class="td-row"><span class="td-label">大小</span><span class="td-val">{{ formatBytes(detailTask.downloaded) }} / {{ formatBytes(detailTask.size) }}（{{ detailTask.progress || 0 }}%）</span></div>
      <div class="td-row"><span class="td-label">状态</span><span class="td-val">{{ statusText(detailTask.status) }}</span></div>
      <div class="td-row" v-if="detailTask.speed"><span class="td-label">速度</span><span class="td-val">{{ formatSpeed(detailTask.speed) }}</span></div>
      <div class="td-row" v-if="detailTask.url"><span class="td-label">来源链接</span><span class="td-val td-url">{{ detailTask.url }}</span></div>
      <div class="td-row" v-if="detailTask.error"><span class="td-label">失败原因</span><span class="td-val" style="color:var(--color-error)">{{ detailTask.error }}</span></div>
      <div class="td-row"><span class="td-label">创建时间</span><span class="td-val">{{ formatTime(detailTask.created) }}</span></div>
      <template #actions>
        <button class="btn primary" @click="detailTask = null">关闭</button>
      </template>
    </Modal>

    <ConfirmModal v-if="confirmDialog" :title="confirmDialog.title" :message="confirmDialog.message" :okText="confirmDialog.okText" :danger="confirmDialog.danger" @ok="handleConfirmOk" @cancel="closeConfirm" />
  </div>
</template>

<style scoped>
.transfer-error {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 10px;
  padding: 8px 10px;
  border: 1px solid color-mix(in srgb, var(--color-error) 35%, transparent);
  border-radius: 8px;
  color: var(--color-error);
  background: color-mix(in srgb, var(--color-error) 8%, transparent);
  font-size: 12px;
}
.transfer-error span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.transfer-error-retry {
  flex-shrink: 0;
  margin-left: auto;
  padding: 2px 8px;
  border: 1px solid currentColor;
  border-radius: 5px;
  color: inherit;
  background: transparent;
  font: inherit;
  cursor: pointer;
}
.transfer-error-retry:hover { background: color-mix(in srgb, currentColor 10%, transparent); }
.field { margin-bottom: 14px; }
.td-row { display: flex; align-items: baseline; gap: 12px; padding: 6px 0; font-size: 13.5px; border-bottom: 1px solid var(--border-lighter); }
.td-row:last-child { border-bottom: none; }
.td-label { width: 72px; flex-shrink: 0; color: var(--text-tertiary); font-size: 12.5px; font-weight: 600; }
.td-val { flex: 1; min-width: 0; color: var(--text-primary); word-break: break-all; user-select: text; }
.td-url { font-size: 12px; color: var(--text-link); }
</style>
