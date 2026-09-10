import { reactive } from 'vue'

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
export function recordAccountHealth(userId, error = null) {
  if (!userId) return
  const message = error ? String(error) : ''
  const status = !error ? 'ok'
    : /401|unauthorized|token.*expir|登录.*失效|重新登录/i.test(message) ? 'auth'
    : /quota|空间不足|容量不足|insufficient.*storage/i.test(message) ? 'quota'
    : /429|risk|captcha|风控|频繁|限流/i.test(message) ? 'limited'
    : /timeout|network|connect|网络|超时|fetch/i.test(message) ? 'network' : 'error'
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
