// 外观应用：主题（明/暗）。强调色固定为品牌紫，不再提供色包切换。
import { WindowSetDarkTheme, WindowSetLightTheme, WindowSetSystemDefaultTheme, EventsEmit } from '../wailsjs/runtime/runtime'
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'

export function isDarkMode(theme) {
  return theme === 'dark' || (theme !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches)
}

// 一次性应用主题；theme 为后端设置值（system/light/dark）
export function applyAppearance(theme) {
  const dark = isDarkMode(theme)
  document.documentElement.classList.toggle('dark', dark)
  applyOledBackground()
  // 清理旧版色包残留
  localStorage.removeItem('mnemo.themePackLight')
  localStorage.removeItem('mnemo.themePackDark')
  // 系统标题栏跟随主题（Wails Windows 主题 API）
  try {
    if (theme === 'dark') WindowSetDarkTheme()
    else if (theme === 'light') WindowSetLightTheme()
    else WindowSetSystemDefaultTheme()
  } catch { /* 非 Windows 或旧版 runtime 时忽略 */ }
  // 通知后端原生悬浮窗明暗主题
  try {
    EventsEmit('app:theme', dark)
  } catch { /* 浏览器预览模式忽略 */ }
  return dark
}

// ---------- 纯前端偏好（后端 Settings 无对应字段，存 localStorage） ----------
const PREFS_KEY = 'mnemo.prefs'
const LAST_DRIVE_KEY = 'mnemo.lastDrive'
let cachedPrefsRaw
let cachedPrefs
const PREFS_DEFAULTS = {
  oledBackground: false, // 深色模式使用纯黑背景
  viewMode: 'list',       // 网盘默认视图 list | grid
  hoverPreview: true,     // 目录树悬停预览
  downloadSound: true,    // 传输完成提示音
  defaultVolume: 100,     // 播放器默认音量 0-200
  defaultSpeed: 1,        // 播放器默认倍速
  seekStep: 10,           // 快进/快退步长（秒）
  autoCloseOnEnd: false,  // 播放到结尾自动收起控制条
  autoLoadSubtitles: true,// 自动加载同名字幕
  defaultSortKey: 'name', // 默认排序键: name | time | size
  defaultSortAsc: true,   // 默认排序方向: true=升序 | false=降序
  sideWidth: 220,         // 网盘侧边栏宽度（px）
  accountAliases: {},     // 账号本地自定义昵称 { [userId]: alias }
  accountIcons: {},       // 账号本地自定义图标 { [userId]: iconFileName }
}

export function getPrefs() {
  if (window.__mnemoPreviewPrefs) return { ...PREFS_DEFAULTS, ...window.__mnemoPreviewPrefs }
  try {
    const raw = localStorage.getItem(PREFS_KEY) || '{}'
    if (raw !== cachedPrefsRaw) {
      cachedPrefs = JSON.parse(raw) || {}
      cachedPrefsRaw = raw
    }
    return { ...PREFS_DEFAULTS, ...cachedPrefs }
  } catch { return { ...PREFS_DEFAULTS } }
}

export function setPref(key, value) {
  const p = getPrefs()
  p[key] = value
  localStorage.setItem(PREFS_KEY, JSON.stringify(p))
  if (key === 'oledBackground') applyOledBackground()
  // localStorage is not reactive. Let mounted views update immediately when a
  // preference is changed from the settings page.
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent('mnemo:prefs-changed', { detail: { key, value } }))
  }
}

function applyOledBackground() {
  document.documentElement.classList.toggle('oled', getPrefs().oledBackground === true)
}

// 侧栏与页面共用账号顺序；拖拽保存后立即通知所有使用者。
export function useOrderedAccounts(source) {
  const order = ref(getPrefs().accountOrder || [])
  function refreshOrder(event) {
    if (event.type === 'storage' || event.detail?.key === 'accountOrder') {
      order.value = getPrefs().accountOrder || []
    }
  }
  onMounted(() => {
    window.addEventListener('mnemo:prefs-changed', refreshOrder)
    window.addEventListener('storage', refreshOrder)
  })
  onBeforeUnmount(() => {
    window.removeEventListener('mnemo:prefs-changed', refreshOrder)
    window.removeEventListener('storage', refreshOrder)
  })
  return computed(() => {
    const ranks = new Map((Array.isArray(order.value) ? [...new Set(order.value)] : []).map((id, index) => [id, index]))
    return [...source()].sort((a, b) => (ranks.get(a.user_id) ?? Infinity) - (ranks.get(b.user_id) ?? Infinity))
  })
}

// ---------- 账号本地自定义昵称与图标（仅在本机显示，不影响远端） ----------
export function getAccountAlias(userId) {
  if (!userId) return ''
  const aliases = getPrefs().accountAliases || {}
  return String(aliases[userId] || '').trim()
}

export function setAccountAlias(userId, alias) {
  if (!userId) return
  const p = getPrefs()
  const aliases = { ...(p.accountAliases || {}) }
  const clean = String(alias || '').trim()
  if (clean) {
    aliases[userId] = clean
  } else {
    delete aliases[userId]
  }
  setPref('accountAliases', aliases)
}

export function getAccountCustomIcon(userId) {
  if (!userId) return ''
  const icons = getPrefs().accountIcons || {}
  return String(icons[userId] || '').trim()
}

export function setAccountCustomIcon(userId, iconKey) {
  if (!userId) return
  const p = getPrefs()
  const icons = { ...(p.accountIcons || {}) }
  const clean = String(iconKey || '').trim()
  if (clean) {
    icons[userId] = clean
  } else {
    delete icons[userId]
  }
  setPref('accountIcons', icons)
}

export function setAccountCustomMeta(userId, alias, iconKey) {
  if (!userId) return
  const p = getPrefs()
  const aliases = { ...(p.accountAliases || {}) }
  const icons = { ...(p.accountIcons || {}) }
  const cleanAlias = String(alias || '').trim()
  const cleanIcon = String(iconKey || '').trim()

  if (cleanAlias) aliases[userId] = cleanAlias
  else delete aliases[userId]

  if (cleanIcon) icons[userId] = cleanIcon
  else delete icons[userId]

  p.accountAliases = aliases
  p.accountIcons = icons
  localStorage.setItem(PREFS_KEY, JSON.stringify(p))
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent('mnemo:prefs-changed', { detail: { key: 'accountCustom', value: { aliases, icons } } }))
  }
}

// ---------- 当前网盘（跨应用重启恢复，不包含任何凭据） ----------
export function getLastDriveSelection() {
  try {
    const value = JSON.parse(localStorage.getItem(LAST_DRIVE_KEY) || 'null')
    if (!value || typeof value !== 'object') return null
    const userId = String(value.userId || '').trim()
    const driveId = String(value.driveId || '').trim()
    return userId ? { userId, driveId } : null
  } catch {
    return null
  }
}

export function setLastDriveSelection(userId, driveId) {
  const normalizedUserId = String(userId || '').trim()
  if (!normalizedUserId) return
  try {
    localStorage.setItem(LAST_DRIVE_KEY, JSON.stringify({
      userId: normalizedUserId,
      driveId: String(driveId || '').trim(),
    }))
  } catch { /* 存储不可用时不影响网盘操作 */ }
}

export function clearLastDriveSelection() {
  try { localStorage.removeItem(LAST_DRIVE_KEY) } catch { /* noop */ }
}
