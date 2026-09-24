// api.js — thin wrapper over the generated Wails bindings + runtime events.
import * as App from '../wailsjs/go/app/App'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { debug, info, warn, error, errorText, configKeys } from './logger'
import { getAccountAlias, getAccountCustomIcon } from './appearance'
import { directoryCacheKey, dispatchDriveNotice, driveErrorNotice } from './workspace'

// re-export the raw binding surface (used by views directly)
export * from '../wailsjs/go/app/App'

export function onEvent(name, cb) {
  try { return EventsOn(name, cb) } catch { return () => {} } // 浏览器预览无 Wails bridge
}

const fileDropListeners = new Set()
let stopNativeDrop
export function onFileDrop(cb) {
  fileDropListeners.add(cb)
  if (fileDropListeners.size === 1 && typeof window !== 'undefined' && window.runtime?.OnFileDrop) {
    stopNativeDrop = window.runtime.OnFileDrop((...args) => {
      for (const listener of fileDropListeners) listener(...args)
    })
  }
  return () => {
    fileDropListeners.delete(cb)
    if (!fileDropListeners.size) {
      if (typeof stopNativeDrop === 'function') stopNativeDrop()
      else window.runtime?.OnFileDropOff?.()
      stopNativeDrop = null
    }
  }
}

function driveCall(action, operation) {
  return Promise.resolve(operation).catch((cause) => {
    const notice = driveErrorNotice(cause, action)
    warn('drive', 'provider operation failed', { action, category: notice.category, error: errorText(cause) })
    dispatchDriveNotice(notice)
    const friendly = new Error(notice.message)
    friendly.name = 'DriveOperationError'
    friendly.category = notice.category
    friendly.cause = cause
    throw friendly
  })
}

export { EventsOn }

// ---------- helpers ----------

export function listProviders() {
  const started = performance.now()
  debug('rpc', 'ListProviders started')
  return App.ListProviders().then((result) => {
    info('rpc', 'ListProviders completed', { count: (result || []).length, duration_ms: Math.round(performance.now() - started) })
    return result
  }).catch((err) => {
    error('rpc', 'ListProviders failed', { error: errorText(err), duration_ms: Math.round(performance.now() - started) })
    throw err
  })
}
export function listAccounts() {
  const started = performance.now()
  debug('rpc', 'ListAccounts started')
  return App.ListAccounts().then((result) => {
    info('rpc', 'ListAccounts completed', { count: (result || []).length, duration_ms: Math.round(performance.now() - started) })
    return result
  }).catch((err) => {
    error('rpc', 'ListAccounts failed', { error: errorText(err), duration_ms: Math.round(performance.now() - started) })
    throw err
  })
}
export function login(provider, config) {
  const started = performance.now()
  info('login', 'provider login RPC started', { provider, config_keys: configKeys(config), has_captcha_token: !!String(config?.captcha_token || '').trim() })
  return App.ProviderLogin(provider, config).then((result) => {
    info('login', 'provider login RPC completed', { provider, duration_ms: Math.round(performance.now() - started) })
    return result
  }).catch((err) => {
    const detail = errorText(err)
    warn('login', 'provider login RPC failed', { provider, error: detail, duration_ms: Math.round(performance.now() - started) })
    // These markers are continuation states consumed by LoginModal, not
    // terminal failures. Keep them intact so captcha/SMS flows can proceed.
    if (/captcha_required(?:\r?\n|_189)|pan139_sms_required/i.test(detail)) throw err
    if (/captcha_(?:retry|expired)_189|429|too[ _-]*(?:many|frequent)|rate[ _-]*limit|risk[ _-]*control|access[ _-]*prohibited/i.test(detail)) {
      dispatchDriveNotice(driveErrorNotice(err, '登录网盘'))
      throw err
    }
    return driveCall('登录网盘', Promise.reject(err))
  })
}
export function SendPan139SMS(username) {
  const fn = App.SendPan139SMS || (typeof window !== 'undefined' && window.go?.app?.App?.SendPan139SMS)
  if (typeof fn !== 'function') return Promise.reject(new Error('139 短信验证暂不可用'))
  return driveCall('发送短信验证码', fn(username))
}
export function SendPan189SMS(username, validateCode = '') {
  const fn = App.SendPan189SMS || (typeof window !== 'undefined' && window.go?.app?.App?.SendPan189SMS)
  if (typeof fn !== 'function') return Promise.reject(new Error('天翼短信登录暂不可用，请重启新版应用'))
  return driveCall('发送短信验证码', fn(username, validateCode))
}
export function SendGuangyaSms(phone) { return driveCall('发送短信验证码', App.SendGuangyaSms(phone)) }
export function saveMounted(provider, conn) { return driveCall('连接网盘', App.SaveMountedAccount(provider, conn)) }
export function validateMountedWrite(provider, conn) { return driveCall('验证写入权限', App.ValidateMountedWrite(provider, conn)) }
export function removeAccount(userId) { return App.RemoveAccount(userId) }
export function renameMountedAccount(userId, name) { return App.RenameMountedAccount(userId, name) }
export function setAccountCustomMeta(userId, customName, customIcon) {
  const fn = App.SetAccountCustomMeta || (typeof window !== 'undefined' && window.go?.app?.App?.SetAccountCustomMeta)
  if (typeof fn === 'function') {
    return fn(userId, customName, customIcon)
  }
  return Promise.resolve(null)
}

export function listDir(userId, driveId, dirId) { return driveCall('加载目录', App.ListDir(userId, driveId, dirId)) }
// 启动恢复、后台预取等非用户发起的目录读取不能抢占界面错误提示；调用方仍会
// 接收到原始失败，以便在当前视图内保留加载状态或安排重试。
export function listDirSilently(userId, driveId, dirId) { return App.ListDir(userId, driveId, dirId) }
export function search(userId, driveId, kw) { return driveCall('搜索文件', App.SearchFiles(userId, driveId, kw)) }
export function listTrash(userId, driveId) { return driveCall('加载回收站', App.ListTrash(userId, driveId)) }
export function mkdir(userId, driveId, parentId, name) { return driveCall('创建文件夹', App.Mkdir(userId, driveId, parentId, name)) }
export function rename(userId, driveId, fileId, name) { return driveCall('重命名', App.RenameFile(userId, driveId, fileId, name)) }
export function RenameBatch(userId, driveId, refs, names) { return driveCall('批量重命名', App.RenameBatch(userId, driveId, refs, names)) }
export function trash(userId, driveId, ids) { return driveCall('移入回收站', App.TrashFiles(userId, driveId, ids)) }
export function remove(userId, driveId, ids) { return driveCall('删除文件', App.DeleteFiles(userId, driveId, ids)) }
export function restore(userId, driveId, ids) { return driveCall('还原文件', App.RestoreFiles(userId, driveId, ids)) }
export function move(userId, driveId, ids, toParent) { return driveCall('移动文件', App.MoveFiles(userId, driveId, ids, toParent)) }
export function copy(userId, driveId, ids, toParent) { return driveCall('复制文件', App.CopyFiles(userId, driveId, ids, toParent)) }
export function favorite(userId, driveId, fav, ids) { return driveCall(fav ? '添加收藏' : '取消收藏', App.FavoriteFiles(userId, driveId, fav, ids)) }
export function ListFavorites(userId, driveId) { return driveCall('加载收藏', App.ListFavorites(userId, driveId)) }
export function AddFavorite(userId, driveId, favoriteItem) { return driveCall('添加收藏', App.AddFavorite(userId, driveId, favoriteItem)) }
export function RemoveFavorite(userId, driveId, fileId) { return driveCall('取消收藏', App.RemoveFavorite(userId, driveId, fileId)) }
export function download(userId, driveId, file) { return driveCall('添加下载任务', App.DownloadFile(userId, driveId, file)) }
export function pinFileSnapshot(userId, driveId, file) { return driveCall('读取文件信息', App.PinFileSnapshot(userId, driveId, file)) }
export function PinFileSnapshot(userId, driveId, file) { return pinFileSnapshot(userId, driveId, file) }
export function downloadUrl(name, url, headers) { return App.DownloadURL(name, url, headers) }
export function createShare(userId, driveId, params) { return driveCall('创建分享', App.CreateShare(userId, driveId, params)) }
export function cancelShare(entry) { return driveCall('取消分享', App.CancelShare(entry)) }
export function uploadFiles(userId, driveId, parentId, conflictPolicy, paths) { return driveCall('添加上传任务', App.UploadFiles(userId, driveId, parentId, conflictPolicy, paths)) }
export function validateUploadFiles(userId, driveId, paths) { return driveCall('检查上传文件', App.ValidateUploadFiles(userId, driveId, paths)) }
export function saveCloudText(userId, driveId, parentId, fileName, content) { return driveCall('保存云端文件', App.SaveCloudTextFile(userId, driveId, parentId, fileName, content)) }
export function migrateFiles(srcUser, srcDrive, dstUser, dstDrive, dstParent, fileIDs, move) {
  return driveCall('创建跨盘迁移任务', App.MigrateFiles(srcUser, srcDrive, dstUser, dstDrive, dstParent, fileIDs, move))
}
export function PreviewMigration(srcUser, srcDrive, dstUser, dstDrive, dstParent, fileIDs) {
  return driveCall('检查迁移任务', App.PreviewMigration(srcUser, srcDrive, dstUser, dstDrive, dstParent, fileIDs))
}
export function VerifyMigration(id) { return driveCall('校验迁移结果', App.VerifyMigration(id)) }
export function listMigrateJobs() { return driveCall('加载迁移任务', App.ListMigrateJobs()) }
export function cancelMigrate(id) { return driveCall('取消迁移任务', App.CancelMigrate(id)) }
export function resumeMigrate(id) { return driveCall('继续迁移任务', App.ResumeMigrate(id)) }
export function deleteMigrateJob(id) { return driveCall('删除迁移记录', App.DeleteMigrateJob(id)) }
export function clearMigrateJobs() { return driveCall('清空迁移记录', App.ClearMigrateJobs()) }

// ---------- transfer (download) ----------
export function listDownloads() { return App.ListDownloads() }
export function pauseDownload(id) { return App.PauseDownload(id) }
export function resumeDownload(id) { return App.ResumeDownload(id) }
export function cancelDownload(id) { return App.CancelDownload(id) }
export function removeDownload(id) { return App.RemoveDownload(id) }
export function prioritizeDownload(id) { return App.PrioritizeDownload(id) }
export function clearDownloads() { return App.ClearDownloads() }

// ---------- transfer (upload) ----------
export function listUploads() { return App.ListUploads() }
export function cancelUpload(id) { return App.CancelUpload(id) }
export function resumeUpload(id) { return App.ResumeUpload(id) }
export function clearUploads() { return App.ClearUploads() }

// ---------- offline (PikPak cloud) ----------
export function offlineDownload(userId, driveId, url, fileName) { return driveCall('创建离线下载', App.OfflineDownload(userId, driveId, url, fileName)) }
export function OfflineDownload(userId, driveId, url, fileName) { return offlineDownload(userId, driveId, url, fileName) }
export function listOfflineTasks(userId) { return driveCall('加载离线任务', App.ListOfflineTasks(userId)) }
export function ListOfflineTasks(userId) { return listOfflineTasks(userId) }
export function refreshOfflineTasks(userId, driveId) { return driveCall('刷新离线任务', App.RefreshOfflineTasks(userId, driveId)) }
export function deleteOfflineTask(userId, driveId, taskId, deleteFiles) { return driveCall('删除离线任务', App.DeleteOfflineTask(userId, driveId, taskId, deleteFiles)) }
export function DeleteOfflineTask(userId, driveId, taskId, deleteFiles) { return deleteOfflineTask(userId, driveId, taskId, deleteFiles) }

// ---------- share import ----------
export function importShare(userId, driveId, shareUrl, password) { return driveCall('读取分享链接', App.ImportShare(userId, driveId, shareUrl, password)) }
export function saveImportedShare(userId, driveId, session, fileIDs, toParentId) { return driveCall('保存分享文件', App.SaveImportedShare(userId, driveId, session, fileIDs, toParentId)) }
export function listShareHistory(userId) { return driveCall('加载分享记录', App.ListShareHistory(userId)) }
export function ListShareHistory(userId) { return listShareHistory(userId) }

// ---------- sync ----------
export function PreviewSync(id) { return driveCall('检查同步计划', App.PreviewSync(id)) }
export function RunSync(id) { return driveCall('启动同步任务', App.RunSync(id)) }
export function RunSyncPlan(id, mode, conflictPolicy) { return driveCall('启动同步任务', App.RunSyncPlan(id, mode, conflictPolicy)) }
export function CancelSync(id) { return driveCall('取消同步任务', App.CancelSync(id)) }

export function RestoreFavorite(item) { return driveCall('恢复收藏文件', App.RestoreFavorite(item)) }

// ---------- settings ----------
export function getSettings() { return App.GetSettings() }
export function saveSettings(s) { return App.SaveSettings(s) }
export function getLogPath() { return App.GetLogPath() }
export function clearLogs() { return App.ClearLogs() }
export function exportLogs() { return App.ExportLogs() }

// Cache RPCs share one queue so a clear cannot race a pending directory write.
let cacheRpcTail = Promise.resolve()
function enqueueCacheRpc(fn) {
  const next = cacheRpcTail.catch(() => {}).then(fn)
  cacheRpcTail = next.catch(() => {})
  return next
}
export function GetDirectoryCache(key) { return enqueueCacheRpc(() => App.GetDirectoryCache(key)) }
export function SaveDirectoryCache(key, files) { return enqueueCacheRpc(() => App.SaveDirectoryCache(key, files)) }
export function DeleteDirectoryCache(key) { return enqueueCacheRpc(() => App.DeleteDirectoryCache(key)) }
export function ClearCache() { return enqueueCacheRpc(() => App.ClearCache()) }

const ROOT_PREWARM_CONCURRENCY = 2
const rootPrewarmTasks = new Map()

function validDirectorySnapshot(files) {
  if (!Array.isArray(files)) return false
  const ids = new Set()
  return files.every((file) => {
    const id = file?.file_id
    if (typeof id !== 'string' || !id.trim() || ids.has(id)) return false
    ids.add(id)
    return true
  })
}

async function fetchRootFirstPage(account, rootId) {
  try {
    const page = await App.ListDirPage(account.user_id, account.drive_id || '', rootId, '')
    return page?.items
  } catch (cause) {
    // Older/non-paginated providers can only expose ListDir. Keep the fallback
    // narrow so a network/auth failure never causes a duplicate cloud request.
    if (!/listpaged.*not supported|capability not implemented/i.test(errorText(cause))) throw cause
    return App.ListDir(account.user_id, account.drive_id || '', rootId)
  }
}

function prewarmRootAccount(account, providers) {
  const meta = providerMetaOf(account, providers)
  const rootId = meta.rootKey || 'root'
  const key = directoryCacheKey(account.user_id, account.drive_id || '', 'list', rootId, '')
  if (rootPrewarmTasks.has(key)) return rootPrewarmTasks.get(key)
  const task = (async () => {
    const cached = await GetDirectoryCache(key)
    if (Array.isArray(cached)) return 'cached'
    const files = await fetchRootFirstPage(account, rootId)
    if (!validDirectorySnapshot(files)) throw new Error('root directory prewarm returned an invalid snapshot')
    // PanView may have completed a full foreground load while this first-page
    // request was in flight. Re-check so prewarming never downgrades a newer,
    // complete snapshot to the first page.
    const latest = await GetDirectoryCache(key)
    if (Array.isArray(latest)) return 'cached'
    await SaveDirectoryCache(key, files)
    return 'warmed'
  })().finally(() => rootPrewarmTasks.delete(key))
  rootPrewarmTasks.set(key, task)
  return task
}

// Warm every signed-in account quietly. Existing persistent snapshots skip
// the network entirely; cache misses request only the provider's first page.
// Ordinary failures stay in logs, while provider-confirmed auth expiry is
// still handled by the backend's account:expired flow.
export async function prewarmRootDirectories(accounts, providers) {
  const queue = (accounts || []).filter(account => account?.user_id && !account.disabled)
  const summary = { cached: 0, warmed: 0, failed: 0 }
  let cursor = 0
  async function worker() {
    while (cursor < queue.length) {
      const account = queue[cursor++]
      try {
        const status = await prewarmRootAccount(account, providers)
        summary[status]++
      } catch (cause) {
        summary.failed++
        warn('cache', 'root directory prewarm failed', { provider: providerOf(account.user_id), error: errorText(cause) })
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(ROOT_PREWARM_CONCURRENCY, queue.length) }, worker))
  if (summary.warmed) info('cache', 'root directory prewarm completed', summary)
  return summary
}

// ---------- account ----------
export function refreshAccount(userId) { return driveCall('刷新账号', App.RefreshAccount(userId)) }
// 容量状态会在启动和账号切换时后台同步；这不是用户显式操作，失败只应
// 记录到账号健康状态，不能触发全局错误弹窗。
export function refreshAccountSilently(userId) { return App.RefreshAccount(userId) }
export function refreshAccountNow(userId) { return driveCall('检查账号', App.RefreshAccountNow(userId)) }

// ---------- preview / player ----------
export function previewUrl(userId, driveId, fileId) { return driveCall('打开预览', App.PreviewURL(userId, driveId, fileId)) }
export function PreviewURL(userId, driveId, fileId) { return previewUrl(userId, driveId, fileId) }
export function cachedPreviewImageURL(userId, driveId, file) { return App.CachedPreviewImageURL(userId, driveId, file) }
export function localPreviewUrl(path) { return App.LocalPreviewURL(path) }
export function mediaProxy() { return App.MediaProxy() }
export function playVideo(userId, driveId, fileId) { return driveCall('播放视频', App.PlayVideo(userId, driveId, fileId)) }
export function playVideoQuality(userId, driveId, fileId, quality) { return driveCall('切换清晰度', App.PlayVideoQuality(userId, driveId, fileId, quality)) }
export function getPlayCursor(userId, driveId, fileId) { return App.GetPlayCursor(userId, driveId, fileId) }
export function savePlayCursor(userId, driveId, fileId, sec) { return App.SavePlayCursor(userId, driveId, fileId, sec) }

/** 从 user_id 解析 provider：普通盘 `pikpak_xxx`，挂载存储 `webdav:xxx` / `s3:xxx`。 */
export function providerOf(userId) {
  const uid = String(userId || '')
  const ci = uid.indexOf(':')
  if (ci > 0 && (uid.startsWith('webdav:') || uid.startsWith('s3:'))) return uid.slice(0, ci)
  const ui = uid.indexOf('_')
  return ui > 0 ? uid.slice(0, ui) : ''
}

function accountText(value) {
  return String(value ?? '').trim()
}

function accountProvider(acc) {
  const t = acc && acc.token ? acc.token : {}
  return accountText(t.tokenfrom) || providerOf(acc && acc.user_id)
}

function accountID(acc, provider) {
  const t = acc && acc.token ? acc.token : {}
  const providerID = accountText(t.provider_account_id)
  if (providerID) return providerID

  const userID = accountText(acc && acc.user_id)
  for (const prefix of [`${provider}_`, `${provider}:`]) {
    if (provider && userID.startsWith(prefix)) return userID.slice(prefix.length)
  }
  return userID
}

function displayAccountText(provider, value) {
  let text = accountText(value)
  if (!text) return ''

  switch (provider) {
    case 'guangya': {
      text = text.replace(/^光鸭(?:云盘)?(?:\s+|\s*[-:：]\s*|\s*(?=\+86))/u, '')
      const compact = text.replace(/[\s-]/g, '')
      if (/^\+?861\d{10}$/u.test(compact)) return compact.replace(/^\+?86/u, '')
      return text
    }
    case 'pan139':
      return text.replace(/^139(?:\s*云盘)?(?:\s+|\s*[-:：]\s*)/u, '')
    case 'aliopen':
      return text.replace(/^阿里云盘(?:\s+|\s*[-:：]\s*)/u, '')
    case 'lanzou':
      return text.replace(/^蓝奏(?:云)?(?:\s+|\s*[-:：]\s*)/u, '')
    case 'ilanzou':
      return text.replace(/^优享版蓝奏云(?:\s+|\s*[-:：]\s*)/u, '')
    case 'pan189':
      return text.replace(/\s*·\s*家庭云$/u, '')
    case 'yike':
      return text.replace(/^一刻相册(?:\s+|\s*[-:：]\s*)/u, '')
    default:
      return text
  }
}

export function accountName(acc) {
  if (!acc) return ''
  // 优先使用用户为该账号设置的自定义昵称（账号属性或本地偏好）
  if (acc.custom_name) return acc.custom_name
  const alias = getAccountAlias(acc.user_id)
  if (alias) return alias

  const t = acc.token || {}
  const provider = accountProvider(acc)
  const candidates = [
    t.nick_name,
    t.user_name,
    t.name,
    t.email,
    t.mail,
    accountID(acc, provider)
  ]
  for (const candidate of candidates) {
    const name = displayAccountText(provider, candidate)
    if (name) return name
  }
  return ''
}

export function accountDetail(acc, providers) {
  if (!acc) return ''
  const pid = providerOf(acc.user_id)
  const p = (providers || []).find((x) => x.ID === pid)
  const label = p ? p.Meta.label : pid
  const name = accountName(acc)
  return name ? `${label} · ${name}` : label
}

export function detectWebdavPresetIcon(account) {
  if (!account) return ''
  const conn = account.token?.conn || account.conn || {}
  const endpoint = String(conn.endpoint || '').toLowerCase()
  const name = String(conn.name || account.token?.user_name || account.user_id || '').toLowerCase()

  if (endpoint.includes('jianguoyun.com') || name.includes('坚果云') || name.includes('jianguoyun')) {
    return 'drive-icons/jianguoyun.svg'
  }
  if (endpoint.includes('teracloud.jp') || endpoint.includes('infini-cloud.net') || name.includes('infinicloud') || name.includes('infini-cloud') || name.includes('teracloud')) {
    return 'drive-icons/infinitycloud.svg'
  }
  if (endpoint.includes('nextcloud') || name.includes('nextcloud')) {
    return 'drive-icons/nextcloud.svg'
  }
  if (endpoint.includes('owncloud') || name.includes('owncloud')) {
    return 'drive-icons/owncloud.svg'
  }
  if (endpoint.includes('seafile') || endpoint.includes('seafdav') || name.includes('seafile')) {
    return 'drive-icons/seafile.svg'
  }
  if (endpoint.includes('openlist') || endpoint.includes('alist') || name.includes('openlist') || name.includes('alist')) {
    return 'drive-icons/openlist.svg'
  }
  if (endpoint.includes(':5006') || name.includes('synology') || name.includes('群晖')) {
    return 'drive-icons/synology.svg'
  }
  if (endpoint.includes('koofr.net') || name.includes('koofr')) {
    return 'drive-icons/koofr.svg'
  }
  if (endpoint.includes('yandex.com') || endpoint.includes('yandex.ru') || name.includes('yandex')) {
    return 'drive-icons/yandex.svg'
  }
  if (endpoint.includes('ewebdav.pcloud.com') || name.includes('pcloud (eu)') || name.includes('pcloud（eu')) {
    return 'drive-icons/pcloud-eu.svg'
  }
  if (endpoint.includes('webdav.pcloud.com') || name.includes('pcloud')) {
    return 'drive-icons/pcloud-us.svg'
  }
  return ''
}

export function providerIconUrl(metaOrIcon) {
  const icon = typeof metaOrIcon === 'string' ? metaOrIcon : (metaOrIcon && metaOrIcon.icon) || ''
  if (!icon) return ''
  // 支持自定义上传的 Data URL / Blob / 网络地址
  if (icon.startsWith('data:') || icon.startsWith('blob:') || icon.startsWith('http://') || icon.startsWith('https://')) {
    return icon
  }
  const file = icon.replace(/^drive-icons\//, '')
  if (!file) return ''
  return new URL(`./assets/drive-icons/${file}`, import.meta.url).href
}

/** 账号对应的 provider 能力集（Capabilities，字段小写）。 */
export function capsOf(account, providers) {
  if (!account) return {}
  const pid = providerOf(account.user_id)
  const p = (providers || []).find((x) => x.ID === pid)
  return (p && p.Capabilities) || (p && p.capabilities) || {}
}

export function providerMetaOf(account, providers) {
  if (!account) return {}
  const pid = providerOf(account.user_id)
  const p = (providers || []).find((x) => x.ID === pid)
  const meta = { ...((p && p.Meta) || { key: pid, label: pid, icon: `drive-icons/${pid}.svg` }) }

  // 1. 用户手动为该账号指定的自定义图标优先（后端字段或本地存储）
  const customIcon = account.custom_icon || getAccountCustomIcon(account.user_id)
  if (customIcon) {
    if (customIcon.startsWith('data:') || customIcon.startsWith('blob:') || customIcon.startsWith('http://') || customIcon.startsWith('https://')) {
      meta.icon = customIcon
    } else {
      meta.icon = customIcon.startsWith('drive-icons/') ? customIcon : `drive-icons/${customIcon}`
    }
    return meta
  }

  // 2. WebDAV 预设智能图标匹配
  if (pid === 'webdav') {
    const presetIcon = detectWebdavPresetIcon(account)
    if (presetIcon) meta.icon = presetIcon
  }
  return meta
}

export function formatBytes(n) {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return i === 0 ? `${n} B` : `${v.toFixed(1)} ${units[i]}`
}

export function formatSpeed(bps) {
  return formatBytes(bps) + '/s'
}

export function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 旧版 filetime 双行显示：{ date: '2024-01-02', clock: '15:04' } */
export function formatTimeParts(ts) {
  if (!ts) return { date: '', clock: '' }
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return { date: `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`, clock: `${p(d.getHours())}:${p(d.getMinutes())}` }
}

export function extOf(name) {
  const i = String(name || '').lastIndexOf('.')
  return i > 0 ? name.slice(i + 1).toLowerCase() : ''
}

/** 文件类型图标名（供 UiIcon 组件使用）。规范：UI 禁用 Emoji。 */
export function iconOf(file) {
  if (file.isDir) return 'folder'
  const cat = file.category || ''
  const ext = extOf(file.name)
  if (cat === 'video' || VIDEO_EXTS.has(ext)) return 'video'
  if (cat === 'audio' || AUDIO_EXTS.has(ext)) return 'audio'
  if (cat === 'image' || IMAGE_EXTS.has(ext)) return 'image'
  if (cat === 'archive') return 'archive'
  if (cat === 'doc' || cat === 'text') return 'doc'
  return 'file'
}

const PREVIEW_TEXT_EXTS = new Set([
  'txt', 'md', 'markdown', 'json', 'json5', 'jsonc', 'js', 'mjs', 'cjs', 'ts', 'tsx', 'jsx', 'vue',
  'go', 'py', 'pyw', 'rs', 'java', 'kt', 'c', 'cpp', 'cc', 'cxx', 'h', 'hpp', 'cs', 'php', 'rb', 'lua', 'swift',
  'css', 'scss', 'sass', 'less', 'html', 'htm', 'xml', 'svg', 'yaml', 'yml', 'toml', 'ini', 'conf', 'env', 'properties',
  'log', 'sh', 'bash', 'zsh', 'bat', 'cmd', 'ps1', 'sql', 'diff', 'patch', 'srt', 'vtt', 'ass', 'ssa', 'gitignore', 'dockerfile'
])

const VIDEO_EXTS = new Set([
  'mp4', 'mkv', 'avi', 'mov', 'wmv', 'flv', 'webm', 'm4v', 'ts', 'm3u8', 'rmvb', 'rm', '3gp', 'mpg', 'mpeg', 'm2ts',
])
const AUDIO_EXTS = new Set([
  'mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'wma', 'ape', 'opus', 'amr', 'mid', 'midi',
])
const IMAGE_EXTS = new Set([
  'jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp', 'svg', 'ico', 'tif', 'tiff', 'heic', 'avif',
])

// 这里的集合是“浏览器/WebView 已验证可处理”的白名单，而不是文件图标
// 分类集合。服务端可能把 MKV、HEIC 等文件标成 video/image，但把它们送进
// 预览会得到一个看似加载、实际无法播放的窗口；未列入白名单的文件统一走
// 下载提示。GIF 保留在图片白名单中，浏览器会原生播放其动画。
const VIDEO_PREVIEW_EXTS = new Set(['mp4', 'm4v', 'webm', 'ogv', 'm3u8', 'mpd'])
const AUDIO_PREVIEW_EXTS = new Set(['mp3', 'm4a', 'wav', 'ogg', 'opus', 'flac'])
const IMAGE_PREVIEW_EXTS = new Set(['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'])
const VIDEO_PREVIEW_MIMES = new Set(['video/mp4', 'video/webm', 'video/ogg'])
const AUDIO_PREVIEW_MIMES = new Set(['audio/mpeg', 'audio/mp4', 'audio/wav', 'audio/x-wav', 'audio/ogg', 'audio/opus', 'audio/flac'])
const IMAGE_PREVIEW_MIMES = new Set(['image/jpeg', 'image/png', 'image/gif', 'image/bmp', 'image/webp'])

function mimeOf(file) {
  return String(file?.mime_type || file?.mimeType || '').split(';', 1)[0].trim().toLowerCase()
}

function inPreviewWhitelist(file, extSet, mimeSet, category) {
  const ext = extOf(file?.name)
  if (extSet.has(ext)) return true
  const mime = mimeOf(file)
  return !ext && (mimeSet.has(mime) || String(file?.category || '').toLowerCase() === category && mimeSet.has(mime))
}

/**
 * 文件打开方式判定：仅白名单格式进入在线预览/播放；其余格式返回
 * `download`，避免把浏览器不支持的容器误送到播放器后才失败。
 */
export function openKindOf(file, capabilities = {}) {
  if (file.isDir) return 'dir'
  const cat = String(file.category || '').toLowerCase()
  const ext = extOf(file.name)
  const videoMime = mimeOf(file).startsWith('video/')
  if (capabilities.cloudVideoPreview && (videoMime || VIDEO_EXTS.has(ext) && (ext !== 'ts' || cat === 'video'))) return 'video'
  if (VIDEO_PREVIEW_EXTS.has(ext) || inPreviewWhitelist(file, VIDEO_PREVIEW_EXTS, VIDEO_PREVIEW_MIMES, 'video')) return 'video'
  if (AUDIO_PREVIEW_EXTS.has(ext) || inPreviewWhitelist(file, AUDIO_PREVIEW_EXTS, AUDIO_PREVIEW_MIMES, 'audio')) return 'audio'
  if (IMAGE_PREVIEW_EXTS.has(ext) || inPreviewWhitelist(file, IMAGE_PREVIEW_EXTS, IMAGE_PREVIEW_MIMES, 'image')) return 'image'
  if (ext === 'pdf' || mimeOf(file) === 'application/pdf') return 'pdf'
  if (ext === 'docx') return 'docx'
  if (ext === 'xlsx') return 'xlsx'
  if (ext === 'pptx') return 'pptx'
  if (cat === 'text' || PREVIEW_TEXT_EXTS.has(ext)) return 'text'
  return 'download'
}

/** 复制文本到剪贴板（WebView2 兼容降级）。 */
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    let ok = false
    try { ok = document.execCommand('copy') } catch { ok = false }
    document.body.removeChild(ta)
    return ok
  }
}

// 打开外部浏览器（后端包装，避免直接依赖 runtime）
export function OpenBrowser(url) { return App.OpenBrowser(url) }
export function OpenPikPakCaptcha(url) { return App.OpenPikPakCaptcha(url) }
export function ClosePikPakCaptcha() { return App.ClosePikPakCaptcha() }
