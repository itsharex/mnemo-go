<script setup>
import packageInfo from '../../package.json'
// 设置页：左侧导航 + 右侧平面行式布局，极简干净无冗余说明
import { ref, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { GetSettings, SaveSettings, GetDownloadDirectory, OpenDownloadDirectory, ClearCache, PickDirectory, RevealInFolder, GetLogPath, ClearLogs, ExportLogs } from '../api'
import { Environment } from '../../wailsjs/runtime/runtime'
import { getPrefs, setPref } from '../appearance'
import SegTabs from '../components/SegTabs.vue'
import UiIcon from '../components/UiIcon.vue'
import UiSelect from '../components/UiSelect.vue'
import ConfirmModal from '../components/ConfirmModal.vue'
import { cleanBackupPrefs } from '../workspace'
import { ListFavorites, RestoreFavorite, listAccounts, setAccountCustomMeta as saveAccountMeta } from '../api'

const emit = defineEmits(['toast', 'theme', 'update', 'clear-cache'])
const backupBusy = ref(false), pendingBackup = ref(null)
async function exportPrefs() {
  if (backupBusy.value) return
  backupBusy.value = true
  try {
    const preferences = cleanBackupPrefs(getPrefs())
    for (const account of await listAccounts()) {
      if (account.custom_name) preferences.accountAliases[account.user_id] = account.custom_name
      if (account.custom_icon) preferences.accountIcons[account.user_id] = account.custom_icon
    }
    const payload = { format: 'mnemo-preferences', version: 1, preferences: cleanBackupPrefs(preferences), theme: settings.value.theme, favorites: await ListFavorites('', '') }
    const path = await window.go.app.App.ExportPreferences(JSON.stringify(payload, null, 2))
    if (path) emit('toast', '偏好已导出', 'success')
  } catch(e) { emit('toast', String(e), 'error') }
  finally { backupBusy.value = false }
}
async function readBackup() {
  if (backupBusy.value) return
  backupBusy.value = true
  try {
    const raw = await window.go.app.App.ImportPreferences()
    if (!raw) return
    const data = JSON.parse(raw)
    if (data.format !== 'mnemo-preferences' || data.version !== 1 || !data.preferences || typeof data.preferences !== 'object') throw new Error('不支持的备份格式')
    const favorites = Array.isArray(data.favorites) ? data.favorites : []
    if (favorites.length > 10000 || favorites.some(f => !f || ['user_id','drive_id','file_id','name'].some(k => typeof f[k] !== 'string'))) throw new Error('收藏数据无效')
    pendingBackup.value = { preferences: cleanBackupPrefs(data.preferences), theme: ['light','dark','system'].includes(data.theme) ? data.theme : 'system', favorites }
  } catch(e) { emit('toast', String(e), 'error') }
  finally { backupBusy.value = false }
}
async function restorePrefs() {
  const data = pendingBackup.value
  if (!data || backupBusy.value) return
  pendingBackup.value = null; backupBusy.value = true
  try {
    const accounts = await listAccounts()
    for (const acc of accounts) {
      const aliases = data.preferences.accountAliases || {}, icons = data.preferences.accountIcons || {}
      if (Object.hasOwn(aliases, acc.user_id) || Object.hasOwn(icons, acc.user_id)) await saveAccountMeta(acc.user_id, aliases[acc.user_id] ?? acc.custom_name ?? '', icons[acc.user_id] ?? acc.custom_icon ?? '')
    }
    for (const favorite of data.favorites) await RestoreFavorite(favorite)
    for (const [key, value] of Object.entries(data.preferences)) setPref(key, value)
    const current = await GetSettings()
    await SaveSettings({ ...current, theme: data.theme })
    prefs.value = getPrefs(); settings.value.theme = data.theme; emit('theme', data.theme)
    emit('toast', '偏好已恢复，账号凭据不受影响', 'success')
  } catch(e) { emit('toast', '恢复未完成，已恢复的项目会保留，可重试：' + String(e), 'error') }
  finally { backupBusy.value = false }
}

const defaults = {
  theme: 'system',
  defaultTab: 'pan',
  proxy: '',
  downloadDir: '',
  maxConcurrentDownloads: 3,
  maxDownloadSpeed: 0,
  maxUploadSpeed: 0,
  autoUpdate: true,
  confirmUpdate: true,
  playbackResume: true,
  keepTasks: true,
  closeToTray: true,
  logLevel: 'info',
}

const settings = ref({ ...defaults })
const loaded = ref(false)
const loadError = ref('')
const loadingSettings = ref(false)
const saving = ref(false)
const clearingCache = ref(false)
const clearingLogs = ref(false)
const exportingLogs = ref(false)
const logPath = ref('')
const effectiveDownloadDir = ref(''), downloadDirError = ref('')
let directoryReadSeq = 0
async function refreshDownloadDirectory() {
  const seq = ++directoryReadSeq
  try {
    const dir = await GetDownloadDirectory()
    if (seq !== directoryReadSeq) return
    effectiveDownloadDir.value = dir || ''
    downloadDirError.value = ''
  } catch (error) {
    if (seq !== directoryReadSeq) return
    effectiveDownloadDir.value = ''
    downloadDirError.value = String(error?.message || error)
  }
}
let pendingSave = false
const bodyEl = ref(null)
const activeNav = ref('general')

const groups = [
  { id: 'general', label: '基础', icon: 'settings' },
  { id: 'pan', label: '文件', icon: 'cloud' },
  { id: 'transfer', label: '传输', icon: 'download' },
  { id: 'player', label: '播放', icon: 'play' },
  { id: 'network', label: '连接', icon: 'cloud-down' },
  { id: 'cache', label: '缓存', icon: 'database' },
  { id: 'logs', label: '日志', icon: 'doc' },
  { id: 'update', label: '更新', icon: 'refresh' },
]

// 纯前端偏好
const prefs = ref(getPrefs())
function onPref(key, value) {
  prefs.value = { ...prefs.value, [key]: value }
  setPref(key, value)
}

let testAudio = null
function playTestSound() {
  try {
    if (!testAudio) {
      testAudio = new Audio(new URL('../assets/audio/download_finished.mp3', import.meta.url).href)
    }
    testAudio.currentTime = 0
    testAudio.play().then(() => emit('toast', '正在试听提示音', 'success')).catch(() => {})
  } catch { /* 忽略 */ }
}

const speedOptions = [
  { value: 0.5, label: '0.5x' },
  { value: 0.75, label: '0.75x' },
  { value: 1, label: '1.0x' },
  { value: 1.25, label: '1.25x' },
  { value: 1.5, label: '1.5x' },
  { value: 2, label: '2.0x' },
]

const seekStepOptions = [
  { value: 5, label: '5 秒' },
  { value: 10, label: '10 秒' },
  { value: 15, label: '15 秒' },
  { value: 30, label: '30 秒' },
]

const tabOptions = [
  { key: 'pan', label: '网盘' },
  { key: 'transfer', label: '传输' },
  { key: 'sync', label: '同步' },
  { key: 'share', label: '分享' },
]

const sortKeyOptions = [
  { value: 'name', label: '名称' },
  { value: 'time', label: '修改时间' },
  { value: 'size', label: '大小' },
]

async function loadSettings() {
  if (loadingSettings.value) return
  loadingSettings.value = true
  loaded.value = false
  loadError.value = ''
  try {
    const s = (await GetSettings()) || {}
    settings.value = {
      ...defaults,
      ...s,
      // Backend stores bytes/s; the settings UI presents the friendlier KB/s.
      maxDownloadSpeed: Math.round((Number(s.maxDownloadSpeed) || 0) / 1024),
      maxUploadSpeed: Math.round((Number(s.maxUploadSpeed) || 0) / 1024),
    }
		settings.value.logLevel = settings.value.logLevel || 'info'
		logPath.value = await GetLogPath().catch(() => '')
    loaded.value = true
    await refreshDownloadDirectory()
  } catch (e) {
    loadError.value = '设置加载失败：' + String(e?.message || e)
  } finally {
    loadingSettings.value = false
  }
}
onMounted(() => {
  loadSettings()
  bodyEl.value?.addEventListener('scroll', onScroll, { passive: true })
})

onBeforeUnmount(() => {
  bodyEl.value?.removeEventListener('scroll', onScroll)
  if (scrollFrame) cancelAnimationFrame(scrollFrame)
})

async function scrollTo(id) {
  activeNav.value = id
  await nextTick()
  document.getElementById('sg-' + id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

let scrollFrame = 0
function onScroll() {
  if (scrollFrame) return
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = 0
    updateActiveSection()
  })
}
function updateActiveSection() {
  const root = bodyEl.value
  if (!root) return
  let current = groups[0].id
  for (const g of groups) {
    const el = document.getElementById('sg-' + g.id)
    if (el && el.offsetTop <= root.scrollTop + 96) current = g.id
  }
  activeNav.value = current
}

async function save(silent) {
  if (!loaded.value) return
  if (saving.value) { pendingSave = true; return }
  saving.value = true
  try {
    const s = { ...settings.value }
    s.maxConcurrentDownloads = Math.min(8, Math.max(1, Number(s.maxConcurrentDownloads) || 1))
    s.maxDownloadSpeed = Math.max(0, Number(s.maxDownloadSpeed) || 0) * 1024
    s.maxUploadSpeed = Math.max(0, Number(s.maxUploadSpeed) || 0) * 1024
    await SaveSettings(s)
    await refreshDownloadDirectory()
    if (settings.value.downloadDir === s.downloadDir && s.downloadDir.trim() && effectiveDownloadDir.value) {
      settings.value.downloadDir = effectiveDownloadDir.value
    }
    if (!silent) emit('toast', '设置已保存', 'success')
    return true
  } catch (e) {
    emit('toast', '保存失败: ' + String(e), 'error')
    return false
  } finally {
    saving.value = false
    if (pendingSave) { pendingSave = false; save(true) }
  }
}

function toggle(key) {
  settings.value[key] = !settings.value[key]
  save(true)
}

function onThemeChange(theme) {
  settings.value.theme = theme
  emit('theme', theme)
  save(true)
}

function onInputCommit() {
  save(true)
}

async function pickDownloadDir() {
  let dir
  try { dir = await PickDirectory('选择下载文件夹', effectiveDownloadDir.value || '') } catch { return }
  if (!dir) return
  settings.value.downloadDir = dir
  save(true)
}

async function openDownloadDir() {
  try { await OpenDownloadDirectory() }
  catch (error) { emit('toast', '打开下载目录失败：' + String(error?.message || error), 'error') }
}

async function resetDownloadDir() {
  const previous = settings.value.downloadDir
  settings.value.downloadDir = ''
  if (!(await save(true))) settings.value.downloadDir = previous
}

function setDownloadLimitPreset(kb) {
  settings.value.maxDownloadSpeed = kb
  save(true)
}
function setUploadLimitPreset(kb) {
  settings.value.maxUploadSpeed = kb
  save(true)
}

async function clearCache() {
  if (clearingCache.value) return
  clearingCache.value = true
  try {
    await ClearCache()
    emit('clear-cache')
    emit('toast', '缓存已清除', 'success')
  } catch (e) {
    emit('toast', '清除缓存失败: ' + String(e), 'error')
  } finally {
    clearingCache.value = false
  }
}

async function clearLogs() {
  if (clearingLogs.value) return
  clearingLogs.value = true
  try {
    await ClearLogs()
    logPath.value = await GetLogPath()
    emit('toast', '日志已清除', 'success')
  } catch (e) {
    emit('toast', '清除日志失败: ' + String(e), 'error')
  } finally {
    clearingLogs.value = false
  }
}

async function exportLogs() {
  if (exportingLogs.value) return
  exportingLogs.value = true
  try {
    const path = await ExportLogs()
    if (path) emit('toast', '日志已导出', 'success')
  } catch (e) {
    emit('toast', '导出日志失败: ' + String(e), 'error')
  } finally {
    exportingLogs.value = false
  }
}
</script>

<template>
  <div class="settings-layout">
    <ConfirmModal v-if="pendingBackup" title="恢复偏好" message="将覆盖备份中的偏好并合并收藏，是否继续？账号凭据不会更改。" @cancel="pendingBackup = null" @ok="restorePrefs" />
    <aside class="settings-nav">
      <button
        v-for="g in groups"
        :key="g.id"
        type="button"
        class="sn-item"
        :class="{ active: activeNav === g.id }"
        @click="scrollTo(g.id)"
      >
        <UiIcon :name="g.icon" :size="16" />
        <span>{{ g.label }}</span>
      </button>
    </aside>

    <div class="settings-body" ref="bodyEl">
      <div v-if="loadError" role="alert" class="sg-row">
        <span>{{ loadError }}</span>
        <button class="btn" @click="loadSettings">重试</button>
      </div>
      <div v-if="loadingSettings" role="status">正在加载设置…</div>
      <div v-show="loaded" class="settings-column">
        <!-- 1. 基础 -->
        <section class="settings-group" id="sg-general">
          <header class="sg-heading"><h2>基础</h2></header>
          <div class="sg-card">
            <div class="sg-row"><div class="sg-text"><span class="sg-label">偏好备份</span><span class="sg-desc">备份外观、账号名称与图标、排序和收藏，不含登录凭据</span></div><div class="sg-control"><button class="btn" :disabled="backupBusy" @click="exportPrefs">导出</button><button class="btn" :disabled="backupBusy" @click="readBackup">恢复</button></div></div>
            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">外观主题</span></div>
              <div class="sg-control">
                <div class="theme-cards">
                  <button
                    class="theme-card"
                    :class="{ active: settings.theme === 'system' || !settings.theme }"
                    @click="onThemeChange('system')"
                  >
                    <UiIcon name="globe" :size="14" /><span>跟随系统</span>
                  </button>
                  <button
                    class="theme-card"
                    :class="{ active: settings.theme === 'light' }"
                    @click="onThemeChange('light')"
                  >
                    <UiIcon name="sun" :size="14" /><span>浅色</span>
                  </button>
                  <button
                    class="theme-card"
                    :class="{ active: settings.theme === 'dark' }"
                    @click="onThemeChange('dark')"
                  >
                    <UiIcon name="moon" :size="14" /><span>深色</span>
                  </button>
                </div>
              </div>
            </div>

            <div class="sg-row oled-background-row">
              <div class="sg-text">
                <span class="sg-label" id="oled-background-label">OLED 纯黑背景</span>
                <span class="sg-desc">适用于 OLED 屏幕，仅在深色模式下将背景改为纯黑，主题色保持不变</span>
              </div>
              <div class="sg-control oled-background-control">
                <button type="button" class="switch" role="switch" aria-labelledby="oled-background-label"
                  :aria-checked="prefs.oledBackground" :class="{ on: prefs.oledBackground }"
                  @click="onPref('oledBackground', !prefs.oledBackground)"></button>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">默认启动页面</span></div>
              <div class="sg-control">
                <SegTabs
                  :options="tabOptions"
                  :modelValue="settings.defaultTab || 'pan'"
                  @update:modelValue="(v) => { settings.defaultTab = v; save(true) }"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text">
                <span class="sg-label">最小化到托盘</span>
              </div>
              <div class="sg-control">
                <div class="switch" :class="{ on: settings.closeToTray !== false }" @click="toggle('closeToTray')"></div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">下载完成提示音</span></div>
              <div class="sg-control">
                <button v-if="prefs.downloadSound" class="tbtn xs" @click="playTestSound">
                  <UiIcon name="play" :size="12" />试听
                </button>
                <div class="switch" :class="{ on: prefs.downloadSound }" @click="onPref('downloadSound', !prefs.downloadSound)"></div>
              </div>
            </div>
          </div>
        </section>

        <!-- 2. 文件 -->
        <section class="settings-group" id="sg-pan">
          <header class="sg-heading"><h2>文件</h2></header>
          <div class="sg-card">
            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">默认视图</span></div>
              <div class="sg-control">
                <SegTabs
                  :options="[{ key: 'list', label: '列表' }, { key: 'grid', label: '网格' }]"
                  :modelValue="prefs.viewMode"
                  @update:modelValue="(v) => onPref('viewMode', v)"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">默认排序</span></div>
              <div class="sg-control">
                <UiSelect
                  :modelValue="prefs.defaultSortKey || 'name'"
                  :options="sortKeyOptions"
                  style="width:116px"
                  @update:modelValue="(v) => onPref('defaultSortKey', v)"
                />
                <SegTabs
                  :options="[{ key: 'asc', label: '升序' }, { key: 'desc', label: '降序' }]"
                  :modelValue="prefs.defaultSortAsc ? 'asc' : 'desc'"
                  @update:modelValue="(v) => onPref('defaultSortAsc', v === 'asc')"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">目录树悬停预览</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: prefs.hoverPreview }" @click="onPref('hoverPreview', !prefs.hoverPreview)"></div>
              </div>
            </div>
          </div>
        </section>

        <!-- 3. 传输 -->
        <section class="settings-group" id="sg-transfer">
          <header class="sg-heading"><h2>传输</h2></header>
          <div class="sg-card">
            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">下载保存目录</span><span class="sg-desc">留空跟随系统下载位置</span></div>
              <div class="sg-control sg-control-grow">
                <input
                  class="input"
                  v-model="settings.downloadDir"
                  :placeholder="effectiveDownloadDir || '系统默认下载目录'"
                  :title="downloadDirError || effectiveDownloadDir"
                  @blur="onInputCommit"
                  @keydown.enter="onInputCommit"
                />
                <button class="btn sm" @click="pickDownloadDir">选择</button>
                <button class="btn sm" :disabled="!effectiveDownloadDir || saving" @click="openDownloadDir">打开</button>
                <button v-if="settings.downloadDir" class="btn sm" :disabled="saving" @click="resetDownloadDir">恢复默认</button>
              </div>
            </div>
            <div v-if="downloadDirError" class="sg-row" role="alert"><span class="sg-desc">{{ downloadDirError }}</span></div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">最大并发下载数</span></div>
              <div class="sg-control">
                <input
                  class="input input-sm"
                  type="number"
                  min="1"
                  max="8"
                  v-model.number="settings.maxConcurrentDownloads"
                  @blur="onInputCommit"
                  @keydown.enter="onInputCommit"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text">
                <span class="sg-label">下载限速</span>
                <span class="sg-desc">单位 KB/s，0 为不限速</span>
              </div>
              <div class="sg-control">
                <input
                  class="input input-sm"
                  type="number"
                  min="0"
                  v-model.number="settings.maxDownloadSpeed"
                  placeholder="0"
                  @blur="onInputCommit"
                  @keydown.enter="onInputCommit"
                />
                <div class="preset-pills">
                  <button class="pill-btn" :class="{ active: !settings.maxDownloadSpeed }" @click="setDownloadLimitPreset(0)">不限速</button>
                  <button class="pill-btn" :class="{ active: settings.maxDownloadSpeed === 5120 }" @click="setDownloadLimitPreset(5120)">5M</button>
                  <button class="pill-btn" :class="{ active: settings.maxDownloadSpeed === 10240 }" @click="setDownloadLimitPreset(10240)">10M</button>
                  <button class="pill-btn" :class="{ active: settings.maxDownloadSpeed === 20480 }" @click="setDownloadLimitPreset(20480)">20M</button>
                </div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text">
                <span class="sg-label">上传限速</span>
                <span class="sg-desc">单位 KB/s，0 为不限速</span>
              </div>
              <div class="sg-control">
                <input
                  class="input input-sm"
                  type="number"
                  min="0"
                  v-model.number="settings.maxUploadSpeed"
                  placeholder="0"
                  @blur="onInputCommit"
                  @keydown.enter="onInputCommit"
                />
                <div class="preset-pills">
                  <button class="pill-btn" :class="{ active: !settings.maxUploadSpeed }" @click="setUploadLimitPreset(0)">不限速</button>
                  <button class="pill-btn" :class="{ active: settings.maxUploadSpeed === 2048 }" @click="setUploadLimitPreset(2048)">2M</button>
                  <button class="pill-btn" :class="{ active: settings.maxUploadSpeed === 5120 }" @click="setUploadLimitPreset(5120)">5M</button>
                </div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">保留传输记录</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: settings.keepTasks }" @click="toggle('keepTasks')"></div>
              </div>
            </div>
          </div>
        </section>

        <!-- 4. 播放 -->
        <section class="settings-group" id="sg-player">
          <header class="sg-heading"><h2>播放</h2></header>
          <div class="sg-card">
            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">断点续播</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: settings.playbackResume }" @click="toggle('playbackResume')"></div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">默认播放音量</span></div>
              <div class="sg-control">
                <input
                  type="range"
                  class="sg-range"
                  min="0"
                  max="200"
                  :value="prefs.defaultVolume"
                  @input="(e) => onPref('defaultVolume', Number(e.target.value))"
                />
                <span class="sg-value sg-value-num">{{ prefs.defaultVolume }}%</span>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">默认播放倍速</span></div>
              <div class="sg-control">
                <UiSelect
                  :modelValue="prefs.defaultSpeed"
                  :options="speedOptions"
                  style="width:116px"
                  @update:modelValue="(v) => onPref('defaultSpeed', Number(v))"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">快进 / 快退步长</span></div>
              <div class="sg-control">
                <UiSelect
                  :modelValue="prefs.seekStep || 10"
                  :options="seekStepOptions"
                  style="width:116px"
                  @update:modelValue="(v) => onPref('seekStep', Number(v))"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">播放完自动收起</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: prefs.autoCloseOnEnd }" @click="onPref('autoCloseOnEnd', !prefs.autoCloseOnEnd)"></div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">自动挂载同名字幕</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: prefs.autoLoadSubtitles }" @click="onPref('autoLoadSubtitles', !prefs.autoLoadSubtitles)"></div>
              </div>
            </div>
          </div>
        </section>

        <!-- 5. 连接 -->
        <section class="settings-group" id="sg-network">
          <header class="sg-heading"><h2>连接</h2></header>
          <div class="sg-card">
            <div class="sg-row">
              <div class="sg-text">
                <span class="sg-label">代理服务器</span>
                <span class="sg-desc">留空表示直连</span>
              </div>
              <div class="sg-control">
                <input
                  class="input"
                  v-model="settings.proxy"
                  placeholder="http://127.0.0.1:7890"
                  @blur="onInputCommit"
                  @keydown.enter="onInputCommit"
                />
              </div>
            </div>
          </div>
        </section>

        <!-- 6. 缓存 -->
        <section class="settings-group" id="sg-cache">
          <header class="sg-heading"><h2>缓存</h2></header>
          <div class="sg-card">
            <div class="sg-row">
              <div class="sg-text">
                <span class="sg-label">目录与页面缓存</span>
                <span class="sg-desc">保存在安装目录的 data/cache 中，不会删除账号、传输记录或播放进度</span>
              </div>
              <div class="sg-control">
                <button class="btn sm" :disabled="clearingCache" @click="clearCache">
                  <span v-if="clearingCache" class="spin"></span>
                  <UiIcon v-else name="trash" :size="13" />
                  {{ clearingCache ? '清除中…' : '清除缓存' }}
                </button>
              </div>
            </div>
          </div>
        </section>

        <!-- 7. 日志 -->
        <section class="settings-group" id="sg-logs">
          <header class="sg-heading"><h2>日志</h2></header>
          <div class="sg-card">
            <div class="sg-row">
              <div class="sg-text">
                <span class="sg-label">日志等级</span>
                <span class="sg-desc">Info 默认记录关键流程；Debug 会增加网络细节，排查时临时开启</span>
              </div>
              <div class="sg-control">
                <UiSelect
                  :modelValue="settings.logLevel || 'info'"
                  :options="[
                    { value: 'error', label: 'Error' },
                    { value: 'warning', label: 'Warning' },
                    { value: 'info', label: 'Info' },
                    { value: 'debug', label: 'Debug' },
                  ]"
                  style="width:116px"
                  @update:modelValue="(v) => { settings.logLevel = v; save(true) }"
                />
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text" style="min-width:0">
                <span class="sg-label">日志文件</span>
                <span class="sg-desc log-path" :title="logPath">{{ logPath || '未初始化' }}</span>
              </div>
              <div class="sg-control">
                <button class="btn sm" :disabled="exportingLogs" @click="exportLogs">
                  <span v-if="exportingLogs" class="spin"></span>
                  <UiIcon v-else name="download" :size="13" />
                  {{ exportingLogs ? '导出中…' : '导出' }}
                </button>
                <button class="btn sm danger" :disabled="clearingLogs" @click="clearLogs">
                  <span v-if="clearingLogs" class="spin"></span>
                  <UiIcon v-else name="trash" :size="13" />
                  {{ clearingLogs ? '清除中…' : '清除' }}
                </button>
              </div>
            </div>
          </div>
        </section>

        <!-- 8. 更新 -->
        <section class="settings-group" id="sg-update">
          <header class="sg-heading"><h2>更新</h2></header>
          <div class="sg-card">
            <div class="sg-row"><div class="sg-text"><span class="sg-label">当前版本</span><span class="sg-desc">只检查正式版更新</span></div><div class="sg-control">{{ packageInfo.version }}</div></div>
            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">自动检查更新</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: settings.autoUpdate }" @click="toggle('autoUpdate')"></div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">更新前确认</span></div>
              <div class="sg-control">
                <div class="switch" :class="{ on: settings.confirmUpdate }" @click="toggle('confirmUpdate')"></div>
              </div>
            </div>

            <div class="sg-row">
              <div class="sg-text"><span class="sg-label">手动检查更新</span></div>
              <div class="sg-control">
                <button class="btn sm" @click="emit('update')">
                  <UiIcon name="refresh" :size="13" />立即检查
                </button>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.theme-cards {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.theme-card {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--control-border);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
  cursor: pointer;
  transition: all var(--motion-fast) var(--motion-ease);
}
.theme-card:hover {
  border-color: var(--color-primary);
  color: var(--text-primary);
}
.theme-card.active {
  border-color: var(--color-primary);
  background: var(--listselectbg);
  color: var(--color-primary);
  font-weight: 600;
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-primary) 30%, transparent);
}

.preset-pills {
  display: inline-flex;
  gap: 4px;
}
.pill-btn {
  padding: 3px 10px;
  border-radius: var(--radius-full);
  border: 1px solid transparent;
  background: var(--bg-subtle);
  font-size: 11.5px; font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--motion-fast) var(--motion-ease), color var(--motion-fast) var(--motion-ease), box-shadow var(--motion-fast) var(--motion-ease), transform var(--motion-fast) var(--motion-spring);
}
.pill-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.pill-btn:active { transform: scale(.94); }
.pill-btn.active {
  background: color-mix(in srgb, var(--color-primary) 16%, transparent);
  color: var(--color-primary); font-weight: 600;
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-primary) 35%, transparent);
  animation: check-pop 240ms var(--motion-spring);
}
.sg-control-grow { flex: 1; min-width: 0; }
.sg-control-grow .input { flex: 1; min-width: 160px; }
.sg-range { width: 140px; cursor: pointer; accent-color: var(--color-primary); }
.sg-value-num { min-width: 40px; text-align: right; font-variant-numeric: tabular-nums; }
.log-path {
  display: block; max-width: 320px; overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap;
}
</style>
