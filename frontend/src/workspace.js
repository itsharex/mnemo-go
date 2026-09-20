import { reactive } from 'vue'

function providerFromUserId(userId) {
  const value = String(userId || '')
  const colon = value.indexOf(':')
  if (colon > 0 && (value.startsWith('webdav:') || value.startsWith('s3:'))) return value.slice(0, colon)
  const underscore = value.indexOf('_')
  return underscore > 0 ? value.slice(0, underscore) : ''
}

// Keep the persistent cache identity in one place so normal directory loads
// and startup root prewarming always address the same on-disk snapshot.
export function directoryCacheKey(userId, driveId, mode = 'list', dirId = '', keyword = '') {
  return [providerFromUserId(userId), userId, driveId, mode, dirId || '', keyword || '']
    .map(value => encodeURIComponent(String(value ?? '')))
    .join('|')
}

export function parseShareText(text) {
  const match = String(text || '').match(/https?:\/\/[^\s<>"'，。；）)]+/i)
  if (!match) return { url: '', password: '', provider: '' }
  try {
    const url = new URL(match[0])
    const host = url.hostname.toLowerCase()
    const domains = { pikpak: ['mypikpak.com', 'pikpak.me'], aliopen: ['alipan.com', 'aliyundrive.com'], pan123: ['123pan.com', '123pan.cn', '123684.com', '123865.com', '123912.com'] }
    const provider = Object.keys(domains).find(key => domains[key].some(domain => host === domain || host.endsWith('.' + domain))) || ''
    const code = String(text).match(/(?:提取码|访问码|密码|code)\s*[:：=]?\s*([a-z0-9]{4,8})/i)
    return { url: url.href, password: code?.[1] || url.searchParams.get('pwd') || url.searchParams.get('password') || '', provider }
  } catch { return { url: '', password: '', provider: '' } }
}

export const accountHealth = reactive({})

function rawErrorText(error) {
  return String(error?.message || error || '').replace(/^Error:\s*/i, '').trim()
}

// Convert provider/HTTP implementation details into short, actionable copy.
// The raw error remains available to the logger, but never reaches the UI.
export function driveErrorNotice(error, action = '网盘操作') {
  const text = rawErrorText(error)
  const normalized = text.toLowerCase()
  if (/401|unauthorized|invalid_grant|invalid[_ -]?token|token.*expir|登录.*失效|凭据.*失效|重新登录/.test(normalized)) {
    const message = /登录|连接|验证/.test(action)
      ? '登录信息无效，请检查账号、密码或授权信息'
      : '网盘登录已失效，请重新登录'
    return { category: 'auth', type: 'warn', message }
  }
  if (/cancelled|canceled|context canceled|操作已取消|用户取消/.test(normalized)) {
    return { category: 'canceled', type: 'info', message: '操作已取消', silent: true }
  }
  if (/captcha_(?:retry|expired)|sms.?code|verify.?code|验证码.*(?:错|失效|过期)/.test(normalized)) {
    return { category: 'verification', type: 'warn', message: '验证码不正确或已过期，请重新获取后再试' }
  }
  if (/429|too many|rate.?limit|retry.?after|risk|风控|频繁|限流/.test(normalized)) {
    return { category: 'limited', type: 'warn', message: '请求过于频繁，请稍后再试' }
  }
  if (/quota|insufficient.?storage|no space|空间不足|容量不足|存储空间/.test(normalized)) {
    return { category: 'quota', type: 'warn', message: '网盘空间不足，请清理空间后重试' }
  }
  if (/403|forbidden|access.?denied|permission|权限不足|无权限|拒绝访问/.test(normalized)) {
    return { category: 'permission', type: 'warn', message: '没有权限完成此操作，请检查账号或文件权限' }
  }
  if (/404|not found|不存在|未找到/.test(normalized)) {
    return { category: 'notFound', type: 'warn', message: '目标文件或目录已不存在，请刷新后重试' }
  }
  if (/409|conflict|already exists|已存在|名称冲突/.test(normalized)) {
    return { category: 'conflict', type: 'warn', message: '目标位置存在同名项目，请更换名称或冲突处理方式' }
  }
  if (/not supported|unsupported|不支持|未实现/.test(normalized)) {
    return { category: 'unsupported', type: 'warn', message: '当前网盘不支持此操作' }
  }
  if (/timeout|timed out|network|fetch|dial tcp|connection|connect|eof|网络|超时|连接失败|无法连接/.test(normalized)) {
    return { category: 'network', type: 'error', message: `${action}失败，请检查网络或代理设置后重试` }
  }
  if (/http 5\d\d|status 5\d\d|bad gateway|service unavailable|gateway timeout|服务.*异常/.test(normalized)) {
    return { category: 'service', type: 'error', message: '网盘服务暂时不可用，请稍后重试' }
  }
  if (/incorrect|invalid.?password|bad.?credentials|login.?fail|密码.*错|账号.*错|授权信息.*错/.test(normalized)) {
    return { category: 'credentials', type: 'warn', message: '账号、密码或授权信息不正确，请检查后重试' }
  }
  return { category: 'unknown', type: 'error', message: `${action}失败，请稍后重试` }
}

export function dispatchDriveNotice(notice) {
  if (!notice || notice.silent || typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent('mnemo:drive-notice', { detail: notice }))
}

export function recordAccountHealth(userId, error = null) {
  if (!userId) return
  const notice = error ? driveErrorNotice(error, '检查账号') : null
  const message = notice?.message || ''
  const status = !error ? 'ok'
    : notice.category === 'auth' ? 'auth'
    : notice.category === 'quota' ? 'quota'
    : notice.category === 'limited' ? 'limited'
    : notice.category === 'network' ? 'network' : 'error'
  accountHealth[userId] = { status, message, checkedAt: Date.now() }
}
export const healthLabels = { ok: '正常', auth: '登录失效', quota: '空间不足', limited: '服务受限', network: '连接失败', error: '检查失败' }

export function cleanBackupPrefs(prefs) {
  const clean = {}
  for (const key of ['oledBackground', 'hoverPreview', 'downloadSound', 'autoCloseOnEnd', 'autoLoadSubtitles', 'defaultSortAsc']) if (typeof prefs?.[key] === 'boolean') clean[key] = prefs[key]
  for (const [key, [min, max]] of Object.entries({ defaultVolume: [0, 200], defaultSpeed: [0.25, 4], seekStep: [1, 300], sideWidth: [160, 500] })) if (Number.isFinite(prefs?.[key])) clean[key] = Math.max(min, Math.min(max, prefs[key]))
  if (['list', 'grid'].includes(prefs?.viewMode)) clean.viewMode = prefs.viewMode
  if (['name', 'time', 'size'].includes(prefs?.defaultSortKey)) clean.defaultSortKey = prefs.defaultSortKey
  if (Array.isArray(prefs?.accountOrder)) clean.accountOrder = [...new Set(prefs.accountOrder.filter(id => typeof id === 'string' && id.length < 512))]
  for (const key of ['accountAliases', 'accountIcons']) {
    clean[key] = Object.create(null)
    for (const [id, value] of Object.entries(prefs?.[key] || {})) {
      if (['__proto__', 'constructor', 'prototype'].includes(id) || typeof value !== 'string') continue
      if (key === 'accountAliases') clean[key][id] = value.slice(0, 40)
      else if (value.length <= 2_000_000 && (/^[\w.-]+\.svg$/.test(value) || /^data:image\/(png|jpeg|webp|svg\+xml);base64,/.test(value))) clean[key][id] = value
    }
  }
  return clean
}
