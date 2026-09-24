<script setup>
import { ref, onMounted, computed, watch, nextTick, onBeforeUnmount, defineAsyncComponent } from 'vue'
import { listAccounts, listProviders, prewarmRootDirectories, removeAccount, renameMountedAccount, onEvent, GetSettings, SaveSettings, providerOf, providerMetaOf, accountName, providerIconUrl, setAccountCustomMeta as setAccountCustomMetaBackend } from './api'
import { applyAppearance, getLastDriveSelection, setLastDriveSelection, clearLastDriveSelection, getAccountAlias, getAccountCustomIcon, setAccountCustomMeta, useOrderedAccounts } from './appearance'
import PanView from './views/PanView.vue'
import WorkspaceView from './views/WorkspaceView.vue'
import { accountHealth, healthLabels, recordAccountHealth } from './workspace'
import { refreshAccountNow } from './api'
import AccountRail from './components/AccountRail.vue'
import AccountAvatar from './components/AccountAvatar.vue'
import UiIcon from './components/UiIcon.vue'
import appIcon from './assets/logo/icon.svg'
import Modal from './components/Modal.vue'
import ConfirmModal from './components/ConfirmModal.vue'
import UpdateModal from './components/UpdateModal.vue'
import ImageCropModal from './components/ImageCropModal.vue'
import { CheckUpdate } from './api'
import { debug, error, errorText, info, installGlobalErrorLogging } from './logger'
import { WindowMinimise, WindowToggleMaximise, Quit } from '../wailsjs/runtime/runtime'
import { createNavigationHistory, installMouseNavigation } from './navigation'

const tab = ref('pan')
const dualPane = ref(false)
const pageHistory = createNavigationHistory()
pageHistory.reset('pan')
let restoringPage = false
watch(tab, value => {
  if (!restoringPage) pageHistory.record(value)
}, { flush: 'sync' })
function navigateMouse(direction) {
  if (tab.value === 'pan' && panView.value?.navigateHistory?.(direction)) return
  const destination = pageHistory.move(direction)
  if (!destination) return
  restoringPage = true
  switchTab(destination)
  restoringPage = false
}
function mouseNavigationBlocked() {
  return !!document.querySelector('[role="dialog"], [aria-modal="true"], .modal-mask, .ctx-menu, .player-panel') || document.body.classList.contains('rail-drag-active')
}
const tabOrder = ['pan', 'transfer', 'sync', 'share', 'settings']
const TransferView = defineAsyncComponent(() => import('./views/TransferView.vue'))
const ShareView = defineAsyncComponent(() => import('./views/ShareView.vue'))
const SyncView = defineAsyncComponent(() => import('./views/SyncView.vue'))
const SettingsView = defineAsyncComponent(() => import('./views/SettingsView.vue'))
const LoginModal = defineAsyncComponent(() => import('./components/LoginModal.vue'))
const GlobalSearch = defineAsyncComponent(() => import('./components/GlobalSearch.vue'))
const QuickOpen = defineAsyncComponent(() => import('./components/QuickOpen.vue'))
const prevTabIdx = ref(0)
const pageTrans = ref('page-slide-left')
const pageComponents = { pan: WorkspaceView, transfer: TransferView, sync: SyncView, share: ShareView, settings: SettingsView }
const pageComponent = computed(() => pageComponents[tab.value] || PanView)
const pageProps = computed(() => {
  if (tab.value === 'pan') return { account: current.value, accounts: orderedAccounts.value, providers: providers.value, dual: dualPane.value }
  if (tab.value === 'sync') return { account: current.value, accounts: orderedAccounts.value, providers: providers.value }
  if (tab.value === 'transfer' || tab.value === 'share') return { accounts: orderedAccounts.value, providers: providers.value }
  return {}
})
const pageListeners = computed(() => {
  const listeners = { toast, navigate: navigateTo }
  if (tab.value === 'pan') listeners.go = onPanGo
  if (tab.value === 'settings') {
    listeners.theme = applyTheme
    listeners.update = () => { pendingUpdateInfo.value = null; showUpdate.value = true }
    listeners.clearCache = clearPanCache
  }
  return listeners
})
function switchTab(key) {
	if (key === tab.value) return
	info('navigation', 'page switch requested', { from: tab.value, to: key })
  const ni = tabOrder.indexOf(key)
  pageTrans.value = ni >= tabOrder.indexOf(tab.value) ? 'page-slide-left' : 'page-slide-right'
  prevTabIdx.value = tabOrder.indexOf(tab.value)
  tab.value = key
}
const accounts = ref([])
const orderedAccounts = useOrderedAccounts(() => accounts.value)
const providers = ref([])
const current = ref(null)
watch([accounts, providers], ([nextAccounts, nextProviders]) => {
  if (!nextAccounts.length || !nextProviders.length) return
  void prewarmRootDirectories(nextAccounts, nextProviders)
}, { flush: 'post' })
const showLogin = ref(false)
const loginProvider = ref('')
const showQuickOpen = ref(false)
const showSearch = ref(false)
const checkingAccount = ref(false)
async function checkAccount() {
  if (!infoAcc.value || checkingAccount.value) return
  const acc = infoAcc.value
  checkingAccount.value = true
  try { await refreshAccountNow(acc.user_id); recordAccountHealth(acc.user_id); refresh() }
  catch (error) { recordAccountHealth(acc.user_id, error) }
  finally { checkingAccount.value = false }
}
async function navigateTo(location) {
  const acc = accounts.value.find(a => a.user_id === location.userId)
  if (!acc) { toast('账号已移除，请重新添加', 'warn'); return }
  select(acc); switchTab('pan')
  await nextTick()
  // The page transition can defer mounting until its previous page leaves.
  for (let attempt = 0; attempt < 20 && !panView.value; attempt++) await new Promise(resolve => setTimeout(resolve, 30))
  await panView.value?.navigate(location)
}
const showUpdate = ref(false)
const pendingUpdateInfo = ref(null)
const infoAcc = ref(null)
const renameAcc = ref(null)
const renameName = ref('')
const renameIcon = ref('')
const renameBusy = ref(false)
const showPresetIcons = ref(false)
const cropImageSrc = ref('')
const cropIsSvg = ref(false)
const fileInputRef = ref(null)

const PRESET_ICONS = [
  { id: 'pikpak.svg', label: 'PikPak' },
  { id: 'aliopen.svg', label: '阿里云盘' },
  { id: 'onedrive.svg', label: 'OneDrive' },
  { id: 'dropbox.svg', label: 'Dropbox' },
  { id: 'pan123.svg', label: '123 云盘' },
  { id: 'pan189.svg', label: '天翼云盘' },
  { id: 'pan139.svg', label: '移动云盘' },
  { id: 'lanzou.svg', label: '蓝奏云' },
  { id: 'ilanzou.svg', label: '优享蓝奏' },
  { id: 'guangya.svg', label: '光鸭云' },
  { id: 'yike.svg', label: '一刻相册' },
  { id: 'jianguoyun.svg', label: '坚果云' },
  { id: 'infinitycloud.svg', label: 'InfiniCLOUD' },
  { id: 'nextcloud.svg', label: 'Nextcloud' },
  { id: 'owncloud.svg', label: 'ownCloud' },
  { id: 'seafile.svg', label: 'Seafile' },
  { id: 'openlist.svg', label: 'OpenList/AList' },
  { id: 'synology.svg', label: '群晖' },
  { id: 'koofr.svg', label: 'Koofr' },
  { id: 'yandex.svg', label: 'Yandex' },
  { id: 'pcloud-eu.svg', label: 'pCloud (EU)' },
  { id: 'pcloud-us.svg', label: 'pCloud (US)' },
  { id: 's3.svg', label: 'S3' },
  { id: 'webdav.svg', label: 'WebDAV' },
]
const windowMaximized = ref(false)
const curTheme = ref('system')
const isDark = ref(false)
applyAppearance('system') // 防启动闪白，随后以设置为准
isDark.value = document.documentElement.classList.contains('dark')

function closeUpdateModal() {
  showUpdate.value = false
  pendingUpdateInfo.value = null
}

function quickToggleTheme() {
  applyTheme(isDark.value ? 'light' : 'dark')
  saveThemePref()
}
function windowMinimise() {
  try { WindowMinimise() } catch { /* browser preview */ }
}
function windowToggleMaximise() {
  try {
    WindowToggleMaximise()
    windowMaximized.value = !windowMaximized.value
  } catch { /* browser preview */ }
}
function windowQuit() {
  // Wails routes Quit through OnBeforeClose. The backend is the single source
  // of truth for deciding whether to hide to tray or terminate the process.
  try {
    Quit()
  } catch (err) {
    error('window', 'window close request failed', { error: errorText(err) })
    window.close?.()
  }
}
async function saveThemePref() {
  try {
    const s = (await GetSettings()) || {}
    s.theme = curTheme.value
    await SaveSettings(s)
  } catch { /* 静默 */ }
}
const toasts = ref([])
const confirmDialog = ref(null)
function askConfirm(message, onOk, opts) {
  confirmDialog.value = { message, onOk, okText: opts?.okText || '确定', cancelText: opts?.cancelText || '取消', danger: opts?.danger || false, title: opts?.title || '确认操作' }
}
function closeConfirm() { confirmDialog.value = null }
function handleConfirmOk() {
  if (!confirmDialog.value) return
  const cb = confirmDialog.value.onOk
  closeConfirm()
  if (typeof cb === 'function') cb()
}
const tabs = [
  { key: 'pan', label: '网盘', icon: 'cloud' },
  { key: 'transfer', label: '传输', icon: 'migrate' },
  { key: 'sync', label: '同步', icon: 'refresh' },
  { key: 'share', label: '分享', icon: 'share' },
]

const panView = ref(null)

// 顶栏滑动色块：跟随当前 tab 的位置与宽度
const tabStrip = ref(null)
const gliderStyle = ref({ opacity: 0 })
function updateGlider() {
  const strip = tabStrip.value
  if (!strip) return
  const el = strip.querySelector('.top-tab.active')
  if (!el) { gliderStyle.value = { opacity: 0 }; return }
  gliderStyle.value = {
    opacity: 1,
    transform: `translateX(${el.offsetLeft}px)`,
    width: el.offsetWidth + 'px',
  }
}
watch(tab, () => nextTick(updateGlider))

let refreshEpoch = 0
function refresh() {
  const my = ++refreshEpoch
  listAccounts().then((list) => {
    if (my !== refreshEpoch) return
    accounts.value = list || []
    const available = orderedAccounts.value
    if (current.value) {
      const found = available.find((a) => a.user_id === current.value.user_id)
      current.value = found || available[0] || null
    } else if (available.length) {
      const saved = getLastDriveSelection()
      const preferred = saved
        ? (available.find((a) => a.user_id === saved.userId && (!saved.driveId || a.drive_id === saved.driveId)) ||
          available.find((a) => a.user_id === saved.userId))
        : null
      current.value = preferred || available[0]
    }
    if (current.value) {
      setLastDriveSelection(current.value.user_id, current.value.drive_id)
    }
  }).catch(() => {})
}

async function select(acc) {
  if (!acc) return
  const sameAccount = current.value?.user_id === acc.user_id && current.value?.drive_id === acc.drive_id
  current.value = acc
  setLastDriveSelection(acc.user_id, acc.drive_id)
  await nextTick()
  if (current.value?.user_id !== acc.user_id || current.value?.drive_id !== acc.drive_id) return
  panView.value?.selectAccount?.(sameAccount)
}

function onPanGo(target) {
  if (target === 'login') showLogin.value = true
  else switchTab(target)
}

function handleAccountExpired(event) {
  const userId = String(event?.userId || '')
  const provider = String(event?.provider || '')
  const meta = providers.value.find(item => item.ID === provider)
  const providerName = meta?.Meta?.label || provider || '网盘'
  const account = String(event?.accountName || '').trim()
  if (userId) {
    delete accountHealth[userId]
    if (current.value?.user_id === userId) current.value = null
    if (infoAcc.value?.user_id === userId) infoAcc.value = null
    const saved = getLastDriveSelection()
    if (saved?.userId === userId) clearLastDriveSelection()
  }
  refresh()
  const subject = account ? `「${account}」` : '该账号'
  toast(`${providerName}${subject}的登录凭据已过期，账号已从本机移除；云端文件不会被删除，请在需要时重新登录。`, 'warn')
}

function closeLogin() {
  showLogin.value = false
  loginProvider.value = ''
}

function clearPanCache() {
  panView.value?.clearCache?.()
  // ClearCache intentionally removes every account snapshot. Rebuild missing
  // root first pages immediately so switching accounts stays instant.
  void prewarmRootDirectories(accounts.value, providers.value)
}

function providerLabel(acc) {
  const p = providers.value.find((x) => x.ID === providerOf(acc.user_id))
  return p ? p.Meta.label : providerOf(acc.user_id)
}

function remove(acc) {
  askConfirm(`移除账号「${accountName(acc)}」？只删除账号凭据，下载任务、收藏和同步配置等本地记录会保留。`, async () => {
    try {
      await removeAccount(acc.user_id)
      if (current.value && current.value.user_id === acc.user_id) current.value = null
      const saved = getLastDriveSelection()
      if (saved && saved.userId === acc.user_id) clearLastDriveSelection()
      refresh()
      toast('账号已移除', 'success')
    } catch (e) { toast(String(e), 'error') }
  }, { danger: true, title: '移除账号' })
}

function openRename(acc) {
  if (!acc) return
  renameAcc.value = acc
  renameName.value = acc.custom_name || getAccountAlias(acc.user_id) || ''
  renameIcon.value = acc.custom_icon || getAccountCustomIcon(acc.user_id) || ''
  showPresetIcons.value = true
}

function onIconFileSelected(e) {
  const file = e.target.files?.[0]
  if (!file) return
  const isSvg = file.type === 'image/svg+xml' || file.name.toLowerCase().endsWith('.svg')
  const reader = new FileReader()
  reader.onload = () => {
    cropIsSvg.value = isSvg
    cropImageSrc.value = String(reader.result || '')
    if (fileInputRef.value) fileInputRef.value.value = ''
  }
  reader.readAsDataURL(file)
}

function onCropConfirm(croppedDataUrl) {
  renameIcon.value = croppedDataUrl
  cropImageSrc.value = ''
}

async function saveRename() {
  if (!renameAcc.value || renameBusy.value) return
  renameBusy.value = true
  try {
    const uid = renameAcc.value.user_id
    const alias = renameName.value.trim()
    const icon = renameIcon.value
    // 后端保存成功后再更新界面，避免失败时误报成功。
    await setAccountCustomMetaBackend(uid, alias, icon)
    setAccountCustomMeta(uid, alias, icon)
    // 强制触发一次账号列表浅拷贝以便全局响应式刷新
    accounts.value = accounts.value.map((a) => (a.user_id === uid ? { ...a, custom_name: alias, custom_icon: icon } : a))
    if (current.value?.user_id === uid) current.value = { ...current.value, custom_name: alias, custom_icon: icon }
    renameAcc.value = null
    toast('账号设置已更新', 'success')
  } catch (e) {
    toast(String(e), 'error')
  } finally {
    renameBusy.value = false
  }
}

function toast(msg, type = '') {
  const message = String(msg ?? '')
  const id = Date.now() + Math.random()
  const normalizedType = ['success', 'error', 'warn', 'info'].includes(type) ? type : 'info'
  const duplicate = toasts.value.find((item) => Date.now() - item.createdAt < 5000 && (
    item.msg === message || message.includes(item.msg) || item.msg.includes(message)
  ))
  if (duplicate) return duplicate.id
  const labels = { success: '已完成', error: '操作失败', warn: '需要注意', info: '提示' }
  const item = { id, msg: message, type: normalizedType, label: labels[normalizedType], createdAt: Date.now() }
  toasts.value.push(item)
  const lifetime = normalizedType === 'error' ? 6500 : 3600
  setTimeout(() => dismissToast(id), lifetime)
  return id
}

function dismissToast(id) {
  toasts.value = toasts.value.filter((t) => t.id !== id)
}

function applyTheme(theme) {
  curTheme.value = theme || 'system'
  applyAppearance(curTheme.value)
  isDark.value = document.documentElement.classList.contains('dark')
}

function onKey(e) {
  if ((e.ctrlKey || e.metaKey) && (e.key === 'p' || e.code === 'KeyP')) {
    e.preventDefault()
    showQuickOpen.value = !showQuickOpen.value
    return
  }
  if (!e.altKey) return
  const map = { Digit1: 'pan', Digit2: 'transfer', Digit3: 'sync', Digit4: 'share', Digit5: 'settings' }
  if (map[e.code]) { switchTab(map[e.code]); e.preventDefault() }
}

function preventNativeContextMenu(e) {
  // Keep the native menu for text editing, but never expose the browser menu in the app shell.
  const target = e.target
  if (target && target.closest && target.closest('input, textarea, select, [contenteditable="true"]')) return
  e.preventDefault()
}

function onQuickAction(action) {
  if (action === 'toggle-theme') quickToggleTheme()
  else if (action === 'refresh') {
    switchTab('pan')
    panView.value?.refresh()
  } else if (action === 'mkdir') {
    switchTab('pan')
    panView.value?.openMkdirModal()
  } else if (action === 'upload') {
    switchTab('pan')
    panView.value?.openUploadModal()
  }
}

onMounted(async () => {
	const removeMouseNavigation = installMouseNavigation(window, navigateMouse, mouseNavigationBlocked)
	info('app', 'frontend mounted')
	window.addEventListener('contextmenu', preventNativeContextMenu, true)
	const onDriveNotice = (event) => {
	  const notice = event?.detail
	  if (notice?.message) toast(notice.message, notice.type || 'error')
	}
	window.addEventListener('mnemo:drive-notice', onDriveNotice)
	const removeGlobalErrorLogging = installGlobalErrorLogging()
	listProviders().then((p) => { providers.value = p || [] }).catch(() => {})
	let autoUpdateEnabled = true
	try {
	  const s = await GetSettings()
	  // 颜色模式默认跟随系统；顶栏可手动切换（不出现在设置页）
	  if (s) {
		applyTheme(s.theme || 'system')
		if (pageComponents[s.defaultTab]) tab.value = s.defaultTab
		autoUpdateEnabled = s.autoUpdate !== false
	  }
	} catch { /* 默认跟随系统 */ }
	refresh()
	// 启动后延迟检查更新（静默，仅发现有新版时弹窗）
	if (autoUpdateEnabled) {
	  setTimeout(() => {
		CheckUpdate().then((r) => {
		  if (r && r.available) {
			pendingUpdateInfo.value = r
			showUpdate.value = true
		  }
		}).catch(() => {})
	  }, 3000)
	}
  window.addEventListener('keydown', onKey)
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  const onScheme = () => applyAppearance(curTheme.value)
  mq.addEventListener('change', onScheme)
  const offFns = [
    onEvent('account:changed', refresh),
    onEvent('account:expired', handleAccountExpired),
    onEvent('app:ready', refresh),
    onEvent('share:history-error', (ev) => {
      error('share', 'share history persistence failed', { error: errorText(ev?.error) })
      toast('分享已创建，但本地历史记录保存失败，请稍后重试', 'warn')
    }),
    // 原生传输悬浮窗点击/菜单「显示主窗口」时跳到传输页
    onEvent('nav:tab', (key) => { if (typeof key === 'string' && tabOrder.includes(key)) switchTab(key) }),
  ]
  nextTick(updateGlider)
  window.addEventListener('resize', updateGlider)
	cleanupFns = () => {
		removeMouseNavigation()
		removeGlobalErrorLogging()
    window.removeEventListener('resize', updateGlider)
    window.removeEventListener('keydown', onKey)
    window.removeEventListener('contextmenu', preventNativeContextMenu, true)
		window.removeEventListener('mnemo:drive-notice', onDriveNotice)
    mq.removeEventListener('change', onScheme)
    offFns.forEach((fn) => { try { fn && fn() } catch { /* noop */ } })
	}
	debug('app', 'frontend initialization tasks scheduled')
})
let cleanupFns = null
onBeforeUnmount(() => cleanupFns && cleanupFns())
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="app-brand">
        <img class="app-brand-icon" :src="appIcon" alt="" aria-hidden="true" draggable="false" />
        <span>Mnemo</span>
      </div>
      <div ref="tabStrip" class="top-tabs">
        <span class="top-tab-glider" :style="gliderStyle"></span>
        <button
          v-for="t in tabs"
          :key="t.key"
          class="top-tab"
          :title="t.label"
          :aria-label="t.label"
          :class="{ active: tab === t.key }"
          @click="switchTab(t.key)"
        ><UiIcon :name="t.icon" :size="18" /></button>
      </div>
      <div class="spacer"></div>
      <button class="icon-btn" title="快捷命令面板 (Ctrl+P)" @click="showQuickOpen = true"><UiIcon name="search" :size="16" /></button>
      <button class="icon-btn" title="全盘搜索" aria-label="全盘搜索" @click="showSearch = true"><UiIcon name="globe" :size="17" /></button>
      <button v-if="tab === 'pan'" class="icon-btn" :class="{ active: dualPane }" :title="dualPane ? '切换单栏' : '切换双栏'" :aria-label="dualPane ? '切换单栏' : '切换双栏'" :aria-pressed="dualPane" @click="dualPane = !dualPane"><UiIcon :name="dualPane ? 'columns' : 'single-pane'" :size="17" /></button>
      <button class="icon-btn" :title="isDark ? '切换到浅色' : '切换到深色'" @click="quickToggleTheme"><UiIcon :name="isDark ? 'sun' : 'moon'" :size="17" /></button>
      <button class="icon-btn" :class="{ active: tab === 'settings' }" title="设置 (Alt+5)" @click="switchTab('settings')"><UiIcon name="settings" :size="17" /></button>
      <AccountAvatar v-if="current" class="topbar-account" :account="current" :providers="providers" />
      <div class="window-actions" aria-label="窗口控制">
        <button class="window-btn" type="button" title="最小化" @click="windowMinimise"><UiIcon name="window-minimize" :size="13" /></button>
        <button class="window-btn" type="button" :title="windowMaximized ? '还原窗口' : '最大化'" @click="windowToggleMaximise"><UiIcon :name="windowMaximized ? 'window-restore' : 'window-maximize'" :size="13" /></button>
        <button class="window-btn window-close" type="button" title="关闭窗口" @click="windowQuit"><UiIcon name="close" :size="14" /></button>
      </div>
    </header>

    <div class="app-body">
      <AccountRail
        v-if="tab === 'pan'"
        :accounts="orderedAccounts"
        :providers="providers"
        :current="current"
        @select="select"
        @add="showLogin = true"
        @remove="remove"
        @info="infoAcc = $event"
        @rename="openRename"
      />
      <main class="page-host">
        <transition :name="pageTrans">
          <KeepAlive>
            <component
              :is="pageComponent"
              :key="tab"
              class="page-view"
              :ref="tab === 'pan' ? 'panView' : undefined"
              v-bind="pageProps"
              v-on="pageListeners"
            />
          </KeepAlive>
        </transition>
      </main>
    </div>

    <LoginModal v-if="showLogin" :providers="providers" :initial-provider="loginProvider" @close="closeLogin" @toast="toast" />
    <GlobalSearch v-if="showSearch" :accounts="orderedAccounts" :providers="providers" @close="showSearch = false" @navigate="navigateTo" />

    <QuickOpen
      :show="showQuickOpen"
      :accounts="orderedAccounts"
      :providers="providers"
      :current-account="current"
      @close="showQuickOpen = false"
      @select-tab="switchTab"
      @select-account="select"
      @action="onQuickAction"
    />

    <Modal v-if="infoAcc" title="账号信息" width="420px" @close="infoAcc = null">
      <div class="kv-row"><span class="kv-label">状态</span><span>{{ healthLabels[accountHealth[infoAcc.user_id]?.status] || '未检查' }}</span></div>
      <p v-if="accountHealth[infoAcc.user_id]?.checkedAt" class="hint">最近检查：{{ new Date(accountHealth[infoAcc.user_id].checkedAt).toLocaleString() }}</p>
      <p v-if="accountHealth[infoAcc.user_id]?.message" role="alert">{{ accountHealth[infoAcc.user_id].message }}</p>
      <div class="workspace-controls"><button class="btn" :disabled="checkingAccount" @click="checkAccount">{{ checkingAccount ? '检查中…' : '检查' }}</button><button v-if="accountHealth[infoAcc.user_id]?.status === 'auth'" class="btn" @click="infoAcc = null; showLogin = true">重新登录</button><button v-if="accountHealth[infoAcc.user_id]?.status === 'network'" class="btn" @click="infoAcc = null; switchTab('settings')">网络设置</button></div>
      <div class="kv-row"><span class="kv-label">账号</span><span style="user-select:text">{{ accountName(infoAcc) }}</span></div>
      <div class="kv-row"><span class="kv-label">网盘</span><span>{{ providerLabel(infoAcc) }}</span></div>
      <div class="kv-row" v-if="infoAcc.usage && infoAcc.usage.size">
        <span class="kv-label">容量</span><span>{{ infoAcc.usage.usedStr }} / {{ infoAcc.usage.sizeStr }}</span>
      </div>
      <div class="kv-row" v-if="infoAcc.token && infoAcc.token.vipname">
        <span class="kv-label">会员</span><span class="badge primary">{{ infoAcc.token.vipname }}</span>
      </div>
      <div class="kv-row"><span class="kv-label">账号 ID</span><span style="user-select:text;font-size: 12px;color:var(--text-tertiary)">{{ infoAcc.user_id }}</span></div>
      <template #actions>
        <button class="btn primary" @click="infoAcc = null">关闭</button>
      </template>
    </Modal>

    <!-- 账号外观编辑二级弹窗 -->
    <Modal v-if="renameAcc" title="自定义" width="480px" @close="!renameBusy && (renameAcc = null)">
      <div class="custom-acc-form">
        <div class="field">
          <label>显示名称</label>
          <input
            v-model="renameName"
            class="input"
            maxlength="40"
            placeholder="留空则使用默认账号名称"
            autofocus
            @keyup.enter="saveRename"
          />
        </div>

        <div class="field">
          <label>网盘图标</label>
          <div class="acc-icon-selector">
            <!-- 当前选中的预览图 -->
            <div class="acc-icon-preview">
              <img v-if="renameIcon" :src="providerIconUrl(renameIcon)" alt="" />
              <img v-else :src="providerIconUrl(providerMetaOf(renameAcc, providers))" alt="" />
            </div>

            <!-- 图标操作按钮组 -->
            <div class="acc-icon-actions">
              <input
                ref="fileInputRef"
                type="file"
                accept="image/*,.svg"
                style="display: none"
                @change="onIconFileSelected"
              />
              <button class="btn sm" type="button" @click="fileInputRef?.click()">
                <UiIcon name="camera" :size="13" />
                <span>上传图标</span>
              </button>
              <button class="btn sm" type="button" @click="showPresetIcons = !showPresetIcons">
                <UiIcon name="grid" :size="13" />
                <span>{{ showPresetIcons ? '收起内置预设' : '内置预设' }}</span>
              </button>
              <button
                v-if="renameIcon"
                class="btn sm text"
                type="button"
                title="恢复为该网盘的默认图标"
                @click="renameIcon = ''"
              >
                恢复默认
              </button>
            </div>
          </div>

          <!-- 内置预设图标网格展开 -->
          <div v-if="showPresetIcons" class="preset-icons-grid">
            <button
              v-for="p in PRESET_ICONS"
              :key="p.id"
              type="button"
              class="preset-icon-chip"
              :class="{ active: renameIcon === p.id || (!renameIcon && providerMetaOf(renameAcc, providers).icon === 'drive-icons/' + p.id) }"
              :title="p.label"
              @click="renameIcon = p.id; showPresetIcons = false"
            >
              <img :src="providerIconUrl(p.id)" :alt="p.label" />
              <span>{{ p.label }}</span>
            </button>
          </div>
        </div>
      </div>

      <template #actions>
        <button class="btn" type="button" :disabled="renameBusy" @click="renameAcc = null">取消</button>
        <button class="btn primary" type="button" :disabled="renameBusy" @click="saveRename">
          <span v-if="renameBusy" class="spin spin-on-primary"></span>
          {{ renameBusy ? '保存中…' : '保存' }}
        </button>
      </template>
    </Modal>

    <!-- 图片/SVG 裁剪弹窗 -->
    <ImageCropModal
      v-if="cropImageSrc"
      :src="cropImageSrc"
      :is-svg="cropIsSvg"
      @confirm="onCropConfirm"
      @cancel="cropImageSrc = ''"
    />

    <!-- 全局正下方药丸长条通知 (Toast Pills) -->
    <transition-group name="toast-list" tag="div" class="toast-wrap" role="status" aria-live="polite">
      <div v-for="t in toasts" :key="t.id" class="toast" :class="t.type" role="alert">
        <span class="t-icon" aria-hidden="true">
          <UiIcon
            :name="t.type === 'success' ? 'check' : (t.type === 'error' ? 'close' : (t.type === 'warn' ? 'warning' : 'info'))"
            :size="13"
          />
        </span>
        <span class="t-message">{{ t.msg }}</span>
        <button class="toast-close" type="button" title="关闭通知" aria-label="关闭通知" @click="dismissToast(t.id)">
          <UiIcon name="close" :size="11" />
        </button>
      </div>
    </transition-group>

    <ConfirmModal v-if="confirmDialog" :title="confirmDialog.title" :message="confirmDialog.message" :okText="confirmDialog.okText" :cancel-text="confirmDialog.cancelText" :danger="confirmDialog.danger" @ok="handleConfirmOk" @cancel="closeConfirm" />
    <UpdateModal v-if="showUpdate" :initial-info="pendingUpdateInfo" @close="closeUpdateModal" />
  </div>
</template>
