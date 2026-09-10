<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { login, saveMounted, validateMountedWrite, SendGuangyaSms, SendPan139SMS, SendPan189SMS, providerIconUrl, OpenBrowser, onEvent, ClosePikPakCaptcha, ShowPikPakCaptcha } from '../api'
import UiIcon from './UiIcon.vue'
import UiSelect from './UiSelect.vue'
import { debug, info, warn, error, errorText as formatErrorText, configKeys } from '../logger'

const props = defineProps({ providers: { type: Array, default: () => [] } })
const emit = defineEmits(['close', 'toast'])

const providerId = ref(localStorage.getItem('login_provider') || 'pikpak')
function defaultLoginForm(id) {
  if (id === 'lanzou') return { upload_tier: 'v0' }
  if (id === 'pan189') return { cloud_type: 'personal', login_mode: 'password' }
  if (id === 'pan139') return { login_mode: 'password' }
  return {}
}
const form = ref(defaultLoginForm(providerId.value))
const initialMountedName = providerId.value === 'webdav' ? 'WebDAV' : (providerId.value === 's3' ? 'S3' : '')
const mountedForm = ref({ name: initialMountedName, endpoint: '', username: '', password: '', authType: 'auto', bucket: '', region: '', rootPath: '', basePath: '', sessionToken: '', forcePathStyle: true, verifyWrite: false, allowPrivateNetwork: false })
const webdavPreset = ref('custom')
const genericWebdavIcon = new URL('../assets/drive-icons/webdav.svg', import.meta.url).href
const webdavPresets = [
  { id: 'custom', name: '', label: '自定义 WebDAV', endpoint: '', rootPath: '/', icon: genericWebdavIcon },
  { id: 'jianguoyun', name: '坚果云', label: '坚果云', endpoint: 'https://dav.jianguoyun.com/dav/', rootPath: '/', icon: new URL('../assets/drive-icons/jianguoyun.svg', import.meta.url).href },
  { id: 'infinitycloud', name: 'InfiniCLOUD', label: 'InfiniCLOUD', endpoint: 'https://miya.teracloud.jp/dav/', rootPath: '/', icon: new URL('../assets/drive-icons/infinitycloud.svg', import.meta.url).href },
  { id: 'nextcloud', name: 'Nextcloud', label: 'Nextcloud', endpoint: 'https://your-nextcloud.example.com/remote.php/dav/files/your-username/', rootPath: '/', icon: new URL('../assets/drive-icons/nextcloud.svg', import.meta.url).href },
  { id: 'owncloud', name: 'ownCloud', label: 'ownCloud', endpoint: 'https://your-owncloud.example.com/remote.php/dav/files/your-username/', rootPath: '/', icon: new URL('../assets/drive-icons/owncloud.svg', import.meta.url).href },
  { id: 'seafile', name: 'Seafile', label: 'Seafile', endpoint: 'https://your-seafile.example.com/seafdav/', rootPath: '/', icon: new URL('../assets/drive-icons/seafile.svg', import.meta.url).href },
  { id: 'openlist', name: 'OpenList', label: 'OpenList / AList', endpoint: 'https://your-openlist.example.com/dav/', rootPath: '/', icon: new URL('../assets/drive-icons/openlist.svg', import.meta.url).href },
  { id: 'synology', name: '群晖 WebDAV', label: '群晖 Synology', endpoint: 'https://your-nas.example.com:5006/', rootPath: '/', icon: new URL('../assets/drive-icons/synology.svg', import.meta.url).href },
  { id: 'koofr', name: 'Koofr', label: 'Koofr', endpoint: 'https://app.koofr.net/dav/Koofr/', rootPath: '/', icon: new URL('../assets/drive-icons/koofr.svg', import.meta.url).href },
  { id: 'yandex', name: 'Yandex Disk', label: 'Yandex Disk', endpoint: 'https://webdav.yandex.com/', rootPath: '/', icon: new URL('../assets/drive-icons/yandex.svg', import.meta.url).href },
  { id: 'pcloud-eu', name: 'pCloud（EU）', label: 'pCloud（EU 数据区）', endpoint: 'https://ewebdav.pcloud.com/', rootPath: '/', icon: new URL('../assets/drive-icons/pcloud-eu.svg', import.meta.url).href },
  { id: 'pcloud-us', name: 'pCloud（US）', label: 'pCloud（US 数据区）', endpoint: 'https://webdav.pcloud.com/', rootPath: '/', icon: new URL('../assets/drive-icons/pcloud-us.svg', import.meta.url).href },
]
const busy = ref(false)
const smsBusy = ref(false)
const smsCountdown = ref(0)
let smsTimer = null
const pikpakCooldownSeconds = ref(0)
let pikpakCooldownTimer = null
function startSmsCountdown() {
  smsCountdown.value = 60
  if (smsTimer) clearInterval(smsTimer)
  smsTimer = setInterval(() => {
    smsCountdown.value--
    if (smsCountdown.value <= 0) {
      clearInterval(smsTimer)
      smsTimer = null
    }
  }, 1000)
}
function clearPikPakCooldown() {
  if (pikpakCooldownTimer) clearInterval(pikpakCooldownTimer)
  pikpakCooldownTimer = null
  pikpakCooldownSeconds.value = 0
}
function startPikPakCooldown(seconds) {
  clearPikPakCooldown()
  pikpakCooldownSeconds.value = Math.max(30, Math.ceil(Number(seconds) || 0))
  pikpakCooldownTimer = setInterval(() => {
    pikpakCooldownSeconds.value--
    if (pikpakCooldownSeconds.value <= 0) clearPikPakCooldown()
  }, 1000)
}
function handlePikPakRateLimit(error) {
  const text = String(error || '')
  if (!/(?:频繁|too[ _-]*(?:many|frequent)|rate[ _-]*limit|request[ _-]*frequency|access[ _-]*prohibited|risk[ _-]*control|429)/i.test(text)) return false
	const match = text.match(/(?:等待|wait(?:ing)?(?:\s+for)?|retry\s+after)\s*(\d+)\s*(?:秒|second)?/i)
  const riskBlocked = /access[ _-]*prohibited|risk[ _-]*control/i.test(text)
  startPikPakCooldown(match ? Number(match[1]) : (riskBlocked ? 60 : 30))
  errorText.value = `PikPak 暂时限制了登录请求，请等待 ${pikpakCooldownSeconds.value} 秒后再试`
  return true
}
const errorText = ref('')
const pan139SMSRequired = ref(false)
const passwordVisibility = ref({})
function passwordVisible(key) { return !!passwordVisibility.value[key] }
function togglePassword(key) {
  passwordVisibility.value = { ...passwordVisibility.value, [key]: !passwordVisible(key) }
}

// 一刻相册（yike）登录入口暂时隐藏（待平台重新开发完成），后端注册保留
const availableProviders = computed(() => props.providers.filter((p) => p.ID !== 'yike'))
const provider = computed(() => availableProviders.value.find((p) => p.ID === providerId.value) || availableProviders.value[0] || null)
const webdavPresetOptions = computed(() => webdavPresets.map((item) => ({ value: item.id, label: item.label, img: item.icon })))
const webdavAuthOptions = [
  { value: 'auto', label: '自动（推荐）' },
  { value: 'basic', label: 'Basic' },
  { value: 'digest', label: 'Digest' },
  { value: 'bearer', label: 'Bearer Token' },
]
const fields = computed(() => (provider.value && provider.value.Login && provider.value.Login.fields) || [])
const isMounted = computed(() => providerId.value === 'webdav' || providerId.value === 's3')
const isOAuthField = (field) => field.type === 'oauth'
const isCookieField = (field) => /cookie|cookies|bduss/i.test(`${field.key} ${field.label}`)
const isAccountField = (field) => /^(?:username|password|phone|email|sms_code)$/i.test(field.key)
const hasAccountLogin = computed(() => fields.value.some(isAccountField))
const hasPhoneLogin = computed(() => fields.value.some((field) => field.key === 'phone' || /手机/.test(field.label)))
const hasEmailLogin = computed(() => fields.value.some((field) => field.key === 'email' || /邮箱/.test(field.label)))
const isOAuth = computed(() => !isMounted.value && !hasAccountLogin.value && fields.value.length > 0 && fields.value.every(isOAuthField))
// 按盘隐藏的可选高级字段（保持界面精简，需要时可从此处移除恢复）
const HIDDEN_LOGIN_FIELDS = {
  aliopen: ['client_id', 'client_secret'],
  guangya: ['refresh_token'],
  pan139: ['authorization'],
}

const visibleFields = computed(() => {
  const hidden = HIDDEN_LOGIN_FIELDS[providerId.value] || []
  const nonOAuthFields = fields.value.filter((field) => !isOAuthField(field) && !hidden.includes(field.key))
  // 有其它登录方式（账密/OAuth）时隐藏 Cookie 字段
  const hasAltLogin = hasAccountLogin.value || fields.value.some(isOAuthField)
  return hasAltLogin ? nonOAuthFields.filter((field) => !isCookieField(field)) : nonOAuthFields
})
const isTokenField = (field) => /token|authorization|secret/i.test(`${field.key} ${field.label}`)
const hasCookieLogin = computed(() => !hasAccountLogin.value && visibleFields.value.some(isCookieField))
const hasTokenLogin = computed(() => !hasAccountLogin.value && visibleFields.value.some(isTokenField))
const isLongText = (key) => /cookie|token|bduss|secret|authorization/i.test(key)
const isPan139DirectLogin = computed(() => providerId.value === 'pan139' && String(form.value.authorization || '').trim() !== '')
const hasRefreshToken = computed(() => String(form.value.refresh_token || '').trim() !== '')
const loginSubtitle = computed(() => {
  if (isMounted.value) return '连接设置'
  if (isOAuth.value) return '浏览器授权'
  if (hasPhoneLogin.value && hasEmailLogin.value) return '手机号/邮箱登录'
  if (hasPhoneLogin.value) return '手机号登录'
  if (hasEmailLogin.value) return '邮箱登录'
  if (hasAccountLogin.value) return '账号密码登录'
  if (hasCookieLogin.value) return 'Cookie 登录'
  if (hasTokenLogin.value) return '令牌登录'
  return '登录设置'
})
function isFieldRequired(field) {
  if (['pan139', 'pan189'].includes(providerId.value) && form.value.login_mode === 'sms' && field.key === 'password') return false
  if (isPan139DirectLogin.value && (field.key === 'username' || field.key === 'password')) return false
  return field.required
}
function isPan139FieldVisible(field) {
  if (!['pan139', 'pan189'].includes(providerId.value)) return true
  const sms = form.value.login_mode === 'sms'
  if (field.key === 'password') return !sms
  if (field.key === 'sms_code') return sms
  if (providerId.value === 'pan189' && field.key === 'validate_code') return !!pan189Captcha.value
  return true
}
function fieldInputType(field) {
  return field.type === 'password' ? 'password' : 'text'
}
function fieldInputMode(field) {
  if (/phone|sms_code/i.test(field.key)) return 'numeric'
  if (/email/i.test(field.key)) return 'email'
  return undefined
}

function applyWebDAVPreset(id) {
  const preset = webdavPresets.find((item) => item.id === id) || webdavPresets[0]
  webdavPreset.value = preset.id
  if (preset.id === 'custom') return
  mountedForm.value.name = preset.name
  mountedForm.value.endpoint = preset.endpoint
  mountedForm.value.rootPath = preset.rootPath
}

// Captcha state is initialized before the provider watcher. A stale saved
// provider can be corrected synchronously as soon as the provider list arrives.
const captchaUrl = ref('')
const captchaSessionId = ref('')
const captchaFrameReady = ref(false)
const captchaNativeWindow = ref(false)
const captchaOpening = ref(false)
const captchaSubmitting = ref(false)
const pan189Captcha = ref('')
let offPikPakCaptchaCompleted = null
let loginModalDisposed = false
let captchaCompletionBusy = false
let pendingCaptchaCompletion = null
let captchaClosePromise = Promise.resolve()

function closePikPakCaptchaSession() {
  const close = captchaClosePromise
    .catch(() => {})
    .then(() => ClosePikPakCaptcha())
    .catch(() => {})
  captchaClosePromise = close
  return close
}

watch(providerId, (v, previous) => {
  info('login', 'login provider selected', { provider: v, previous_provider: previous || '' })
  localStorage.setItem('login_provider', v)
  form.value = defaultLoginForm(v)
	passwordVisibility.value = {}
  webdavPreset.value = 'custom'
  mountedForm.value = { name: v === 'webdav' ? 'WebDAV' : (v === 's3' ? 'S3' : ''), endpoint: '', username: '', password: '', authType: 'auto', bucket: '', region: '', rootPath: '', basePath: '', sessionToken: '', forcePathStyle: true, verifyWrite: false, allowPrivateNetwork: false }
  errorText.value = ''
  resetCaptcha(previous === 'pikpak')
  if (v !== 'pikpak') clearPikPakCooldown()
})

watch(() => form.value.login_mode, () => {
  pan139SMSRequired.value = false
  pan189Captcha.value = ''
  delete form.value.validate_code
  delete form.value.sms_code
  if (smsTimer) clearInterval(smsTimer)
  smsTimer = null
  smsCountdown.value = 0
})

// A hidden or removed provider must not remain selected through an old
// localStorage value while the visible panel falls back to another item.
watch(availableProviders, (list) => {
  if (!list.length) return
  if (!list.some((p) => p.ID === providerId.value)) providerId.value = list[0].ID
}, { immediate: true })

function onKey(e) { if (e.key === 'Escape') emit('close') }
onMounted(() => {
	debug('login', 'login modal mounted', { provider: providerId.value })
  loginModalDisposed = false
  window.addEventListener('keydown', onKey)
  offPikPakCaptchaCompleted = onEvent('pikpak:captcha:completed', (payload) => {
	info('captcha', 'PikPak captcha completion event received', { session_id: String(payload?.session_id || ''), has_token: !!String(payload?.captcha_token || '').trim() })
    void completePikPakCaptcha(payload)
  })
})
onBeforeUnmount(() => {
	debug('login', 'login modal unmounted')
  loginModalDisposed = true
  window.removeEventListener('keydown', onKey)
  if (offPikPakCaptchaCompleted) offPikPakCaptchaCompleted()
  offPikPakCaptchaCompleted = null
  void closePikPakCaptchaSession()
  if (smsTimer) clearInterval(smsTimer)
  clearPikPakCooldown()
})

// 逐网盘附加帮助（后端 Login.fields 之外的引导）
const PROVIDER_HELP = {
  aliopen: { label: '如何获取 refresh_token？', url: 'https://alist.nn.ci/tool/aliyundrive/request' },
}
const providerHelp = computed(() => PROVIDER_HELP[providerId.value] || null)
function openHelp() { if (providerHelp.value) OpenBrowser(providerHelp.value.url).catch(() => {}) }

// PikPak 的验证页会回调应用创建的一次性 localhost 地址。不要依赖 iframe
// postMessage：挑战页并不保证会把最终 token 发给嵌入方。
async function completePikPakCaptcha(payload) {
  const sessionID = String(payload?.session_id || '').trim()
  if (
    captchaCompletionBusy ||
    providerId.value !== 'pikpak' ||
    !captchaUrl.value ||
    !captchaSessionId.value ||
    sessionID !== captchaSessionId.value
  ) return
	if (busy.value) {
		pendingCaptchaCompletion = payload
		return
	}
	info('captcha', 'PikPak captcha completion accepted', { session_id: sessionID, has_token: !!String(payload?.captcha_token || '').trim() })

  captchaCompletionBusy = true
  const token = String(payload?.captcha_token || '').trim()
  captchaSessionId.value = ''
  captchaFrameReady.value = false
  captchaNativeWindow.value = false
  captchaUrl.value = ''
  if (token) {
    form.value.captcha_token = token
    form.value.captcha_verified = 'true'
    delete form.value.captcha_requires_confirmation
  } else {
    // 部分回调不会携带最终 token；后端会用初始 token 做一次受限确认。
    form.value.captcha_verified = 'true'
    form.value.captcha_requires_confirmation = 'true'
  }
  errorText.value = ''

  try {
    try {
      await closePikPakCaptchaSession()
    } catch {
      // 回调服务会自行关闭；关闭失败不应阻断已经完成的登录续办。
    }
    if (!loginModalDisposed && providerId.value === 'pikpak') await submit()
  } finally {
    captchaCompletionBusy = false
  }
}

function parseCaptcha(err) {
  const text = String(err)
  const m = text.match(/captcha_required\r?\nurl=(\S+)\r?\ntoken=(\S*)/)
  if (!m) return false
  const session = text.match(/(?:^|\r?\n)session=([A-Za-z0-9_-]+)/)
  captchaUrl.value = m[1]
  captchaSessionId.value = session ? session[1] : ''
  form.value.captcha_token = m[2]
  delete form.value.captcha_verified
  delete form.value.captcha_requires_confirmation
  captchaFrameReady.value = false
  captchaNativeWindow.value = false
  return true
}

async function openCaptchaWindow() {
  const sessionID = captchaSessionId.value
  if (!sessionID || !captchaUrl.value || captchaOpening.value) return
  captchaOpening.value = true
  try {
    const opened = await ShowPikPakCaptcha(sessionID, captchaUrl.value)
    if (sessionID !== captchaSessionId.value || loginModalDisposed) return
    captchaNativeWindow.value = opened
    captchaFrameReady.value = opened
    errorText.value = opened ? '请在独立窗口中完成安全验证，完成后将自动登录' : '请在下方完成安全验证'
  } catch (e) {
    if (sessionID === captchaSessionId.value) {
      captchaNativeWindow.value = true
      errorText.value = String(e)
    }
  } finally {
    captchaOpening.value = false
  }
}
async function reloadCaptcha() {
  if (!captchaUrl.value || busy.value) return
  captchaSessionId.value = ''
  captchaFrameReady.value = false
  captchaUrl.value = ''
  delete form.value.captcha_token
  delete form.value.captcha_verified
  delete form.value.captcha_requires_confirmation
  try {
	info('captcha', 'PikPak captcha reload requested')
    await closePikPakCaptchaSession()
  } catch {
    // A stale callback must not prevent a deliberate retry.
  }
  await submit()
}

// 天翼云 189 图形验证码
function parse189Captcha(err) {
  const m = String(err).match(/captcha_required_189\nimage=(\S+)/)
  if (!m) return false
  pan189Captcha.value = m[1]
  form.value.validate_code = ''
  return true
}
function parse189CaptchaExpired(err) {
  if (!/captcha_expired_189/i.test(String(err))) return false
  pan189Captcha.value = ''
  form.value.validate_code = ''
  errorText.value = '验证码已过期，请重新登录'
  return true
}
function parse189CaptchaRetry(err) {
  if (!/captcha_retry_189/i.test(String(err))) return false
  pan189Captcha.value = ''
  form.value.validate_code = ''
  errorText.value = '验证码不正确，请重新登录'
  return true
}

function resetCaptcha(closeSession = false) {
  pendingCaptchaCompletion = null
  captchaNativeWindow.value = false
  captchaSessionId.value = ''
  captchaUrl.value = ''
  captchaFrameReady.value = false
  captchaSubmitting.value = false
  pan189Captcha.value = ''
  pan139SMSRequired.value = false
  delete form.value.captcha_token
  delete form.value.captcha_verified
  delete form.value.validate_code
  delete form.value.sms_code
  if (closeSession) void closePikPakCaptchaSession()
}

async function sendSms() {
  if (!String(form.value.phone || '').trim()) { errorText.value = '请先填写手机号'; return }
  smsBusy.value = true
	info('login', 'SMS verification request started', { provider: providerId.value })
  errorText.value = ''
  try {
    const r = await SendGuangyaSms(form.value.phone)
    form.value.verification_id = r.verification_id
    form.value.device_id = r.device_id
    form.value.captcha_token = r.captcha_token || ''
    emit('toast', '验证码已发送', 'success')
    startSmsCountdown()
  } catch (e) {
		warn('login', 'SMS verification request failed', { error: formatErrorText(e) })
    errorText.value = String(e)
  } finally {
    smsBusy.value = false
  }
}

async function sendPan139Sms() {
  if (smsBusy.value) return
  if (!String(form.value.username || '').trim()) { errorText.value = '请先填写账号'; return }
  const username = String(form.value.username).trim()
  smsBusy.value = true
  errorText.value = ''
  try {
    await SendPan139SMS(username)
    if (loginModalDisposed || providerId.value !== 'pan139' || form.value.login_mode !== 'sms' || String(form.value.username || '').trim() !== username) return
    emit('toast', '验证码已发送', 'success')
    startSmsCountdown()
  } catch (e) {
    warn('login', '139 SMS verification request failed', { error: formatErrorText(e) })
    errorText.value = String(e)
  } finally {
    smsBusy.value = false
  }
}

async function sendPan189Sms() {
  if (smsBusy.value) return
  const username = String(form.value.username || '').trim()
  if (!username) { errorText.value = '请先填写手机号'; return }
  const attemptProvider = providerId.value
  smsBusy.value = true
  errorText.value = ''
  try {
    const image = await SendPan189SMS(username, String(form.value.validate_code || '').trim())
    if (loginModalDisposed || providerId.value !== attemptProvider || form.value.login_mode !== 'sms' || String(form.value.username || '').trim() !== username) return
    if (image) {
      pan189Captcha.value = image
      form.value.validate_code = ''
      errorText.value = '请填写图形验证码，再点击获取短信验证码'
    } else {
      startSmsCountdown()
      errorText.value = '短信验证码已发送，请填写后登录'
    }
  } catch (e) {
    if (providerId.value === attemptProvider) errorText.value = formatErrorText(e)
  } finally {
    smsBusy.value = false
  }
}

function parsePan139SMSRequired(err) {
  if (!/pan139_sms_required/i.test(String(err))) return false
  pan139SMSRequired.value = true
  form.value.login_mode = 'sms'
  form.value.sms_code = ''
  errorText.value = '请获取并填写短信验证码'
  return true
}

function validate() {
  if (isMounted.value) {
    const m = mountedForm.value
    if (providerId.value === 'webdav' && !m.endpoint.trim()) return '请填写 WebDAV 地址'
    if (providerId.value !== 'webdav' || m.authType !== 'bearer') {
      if (!m.username.trim()) return providerId.value === 's3' ? '请填写 Access Key ID' : '请填写用户名'
    }
    if (!m.password) return providerId.value === 's3' ? '请填写 Secret Access Key' : '请填写密码'
    if (providerId.value === 's3' && !m.bucket.trim()) return '请填写 Bucket'
    return ''
  }
  if (isOAuth.value) return '' // OAuth 无表单校验
  const value = (key) => String(form.value[key] || '').trim()
  if (providerId.value === 'lanzou' && (!value('username') || !value('password'))) {
    return '请填写蓝奏云账号和密码'
  }
  if (providerId.value === 'guangya') {
    if (value('refresh_token')) return ''
    if (!value('phone')) return '请填写手机号'
    if (!value('sms_code')) return '请填写短信验证码'
    if (!value('verification_id')) return '请先获取短信验证码'
    return ''
  }
  if (providerId.value === 'pan189' && value('login_mode') === 'sms') {
    if (!value('username')) return '请填写手机号'
    if (!value('sms_code')) return '请填写短信验证码'
    return ''
  }
  if (providerId.value === 'pan189' && pan189Captcha.value && !value('validate_code')) {
    return '请填写图形验证码'
  }
  if (providerId.value === 'pan139') {
    if (!value('username')) return '请填写手机号/账号'
    if (form.value.login_mode === 'sms') {
      if (!value('sms_code')) return '请填写短信验证码'
    } else if (!value('password')) {
      return '请填写密码'
    }
    return ''
  }
  for (const f of visibleFields.value) {
    if (isFieldRequired(f) && !value(f.key)) return `请填写${f.label}`
  }
  // 全可选字段的 provider 至少填一项
  const inputFields = visibleFields.value.filter((f) => f.type !== 'select')
  const allOpt = inputFields.length > 0 && inputFields.every((f) => !f.required)
  if (allOpt && !inputFields.some((f) => value(f.key))) return '至少填写一个字段'
  return ''
}

async function submit() {
  if (busy.value) return
  const attemptProvider = providerId.value
  const attemptMounted = isMounted.value
  const attemptOAuth = isOAuth.value
  busy.value = true
  captchaSubmitting.value = true
  errorText.value = ''
  try {
	  debug('login', 'login form submit started', { provider: attemptProvider, config_keys: configKeys(attemptMounted ? mountedForm.value : form.value), has_captcha: !!captchaUrl.value })
  // The callback normally resumes login automatically. Keep a manual
  // fallback for dev WebView/browser environments where the event bridge can
  // be delayed or unavailable after the challenge redirects.
    if (attemptProvider === 'pikpak' && captchaUrl.value) {
    captchaSessionId.value = ''
    captchaFrameReady.value = false
    captchaUrl.value = ''
    delete form.value.captcha_verified
    delete form.value.captcha_requires_confirmation
    await closePikPakCaptchaSession()
  }
    if (attemptProvider === 'pikpak') {
    await captchaClosePromise
      if (loginModalDisposed || providerId.value !== attemptProvider || captchaUrl.value) return
  }
    if (attemptProvider === 'pikpak' && pikpakCooldownSeconds.value > 0) {
    errorText.value = `PikPak 暂时限制了登录请求，请等待 ${pikpakCooldownSeconds.value} 秒后再试`
    return
  }
  const err = validate()
  if (err) {
		warn('login', 'login form validation failed', { provider: attemptProvider, reason: err })
		errorText.value = err
		return
	}
    if (attemptMounted) {
      const mountedConfig = { ...mountedForm.value }
      if (attemptProvider !== 'webdav') delete mountedConfig.authType
      const verifyWrite = attemptProvider === 's3' && mountedConfig.verifyWrite === true
      delete mountedConfig.verifyWrite
      if (verifyWrite) await validateMountedWrite(attemptProvider, mountedConfig)
      await saveMounted(attemptProvider, mountedConfig)
    } else {
      await login(attemptProvider, { ...form.value })
    }
    if (loginModalDisposed || providerId.value !== attemptProvider) return
    const successMessage = attemptOAuth
      ? '授权成功'
      : (attemptMounted && attemptProvider === 's3'
        ? (mountedForm.value.verifyWrite ? 'S3 已添加（浏览和写入权限已验证）' : 'S3 已添加（浏览权限已验证，写入权限将在首次上传时验证）')
        : '登录成功')
    emit('toast', successMessage, 'success')
		info('login', 'login form submit completed', { provider: attemptProvider })
    emit('close')
  } catch (e) {
    try {
      if (attemptProvider === 'pikpak' && handlePikPakRateLimit(e)) {
        // Keep the challenge state intact while the provider cooldown runs.
      } else if (attemptProvider === 'pikpak' && parseCaptcha(e)) {
        await openCaptchaWindow()
      } else if (attemptProvider === 'pan189' && parse189Captcha(e)) {
        errorText.value = '请输入图片中的验证码'
      } else if (attemptProvider === 'pan189' && parse189CaptchaRetry(e)) {
        // 清掉旧验证码，下一次提交会重新获取登录参数和图片。
      } else if (attemptProvider === 'pan189' && parse189CaptchaExpired(e)) {
        // 清掉失效图片，下一次提交会重新获取登录参数和验证码。
      } else if (attemptProvider === 'pan139' && parsePan139SMSRequired(e)) {
        // 账密登录已建立临时会话，切换到短信二次验证，不重复提交密码。
      } else {
        errorText.value = String(e)
      }
		warn('login', 'login form submit failed', { provider: attemptProvider, error: formatErrorText(e) })
    } catch (handlerError) {
      // Error rendering must never leave the submit button stuck in busy state.
			errorText.value = String(handlerError)
			error('login', 'login error handler failed', { provider: attemptProvider, error: formatErrorText(handlerError) })
    }
  } finally {
    busy.value = false
    captchaSubmitting.value = false
    if (pendingCaptchaCompletion) {
      const payload = pendingCaptchaCompletion
      pendingCaptchaCompletion = null
      void completePikPakCaptcha(payload)
    }
  }
}
</script>

<template>
  <teleport to="body">
    <transition name="modal-fade">
      <div class="modal-mask" @click.self="emit('close')">
        <div class="modal login-modal">
          <div class="modal-head login-modal-head">
            <h3>添加网盘账号</h3>
            <button class="icon-btn login-close" title="关闭 (Esc)" @click="emit('close')"><UiIcon name="close" :size="14" /></button>
          </div>
          <div class="login-body">
            <div class="login-side" role="tablist" aria-label="网盘服务">
              <button
                v-for="p in availableProviders"
                :key="p.ID"
                type="button"
                role="tab"
                class="lp-item"
                :class="{ active: p.ID === providerId }"
                :aria-selected="p.ID === providerId"
                :title="p.Meta.label"
				:disabled="busy"
                @click="providerId = p.ID"
              >
                <img :src="providerIconUrl(p.Meta)" alt="" />
                <span>{{ p.Meta.label }}</span>
              </button>
            </div>

            <form v-if="provider" class="login-form" @submit.prevent="submit">
              <div class="lf-head">
                <img :src="providerIconUrl(provider.Meta)" alt="" />
                <div>
                  <div class="lf-title">{{ provider.Meta.label }}</div>
                  <div class="lf-sub">{{ loginSubtitle }}</div>
                </div>
              </div>

              <div class="login-form-content" :key="providerId">
                <!-- 挂载存储（WebDAV / S3） -->
                <template v-if="isMounted">
                  <div class="login-section">
                    <div class="field login-field"><label>连接名称</label><input class="input" v-model="mountedForm.name" placeholder="我的 WebDAV / S3" /></div>
                    <div v-if="providerId === 'webdav'" class="field login-field">
                      <label>服务预设</label>
                      <UiSelect v-model="webdavPreset" :options="webdavPresetOptions" block @change="applyWebDAVPreset" />
                    </div>
                    <div class="field login-field"><label>{{ providerId === 's3' ? 'Endpoint (可选)' : 'WebDAV 地址' }}</label><input class="input" v-model="mountedForm.endpoint" :placeholder="providerId === 's3' ? 's3.us-east-1.amazonaws.com (可选，默认 AWS)' : 'https://dav.example.com'" /></div>
                    <div v-if="providerId === 'webdav'" class="field login-field">
                      <label>认证方式</label>
                      <UiSelect v-model="mountedForm.authType" :options="webdavAuthOptions" block />
                    </div>
                    <div v-if="providerId !== 'webdav' || mountedForm.authType !== 'bearer'" class="field login-field"><label>{{ providerId === 's3' ? 'Access Key ID' : '用户名' }}</label><input class="input" v-model="mountedForm.username" :placeholder="providerId === 's3' ? '请输入 Access Key ID' : '请输入用户名'" /></div>
                    <div class="field login-field"><label>{{ providerId === 's3' ? 'Secret Access Key' : (mountedForm.authType === 'bearer' ? 'Bearer Token' : '密码') }}</label><div class="password-input-wrap"><input class="input" :type="passwordVisible('mounted.password') ? 'text' : 'password'" v-model="mountedForm.password" :placeholder="providerId === 's3' ? '请输入 Secret Access Key' : '请输入密码'" /><button class="password-toggle" type="button" :title="passwordVisible('mounted.password') ? '隐藏密码' : '显示密码'" :aria-label="passwordVisible('mounted.password') ? '隐藏密码' : '显示密码'" @click="togglePassword('mounted.password')"><UiIcon :name="passwordVisible('mounted.password') ? 'eye-off' : 'eye'" :size="15" /></button></div></div>
                    <template v-if="providerId === 's3'">
                      <div class="field login-field"><label>Bucket</label><input class="input" v-model="mountedForm.bucket" placeholder="存储桶名称" /></div>
                      <div class="field login-field"><label>Region (可选)</label><input class="input" v-model="mountedForm.region" placeholder="us-east-1" /></div>
                      <div class="field login-field"><label>Session Token (可选)</label><div class="password-input-wrap"><input class="input" :type="passwordVisible('mounted.sessionToken') ? 'text' : 'password'" v-model="mountedForm.sessionToken" placeholder="可选临时令牌" /><button class="password-toggle" type="button" :title="passwordVisible('mounted.sessionToken') ? '隐藏令牌' : '显示令牌'" :aria-label="passwordVisible('mounted.sessionToken') ? '隐藏令牌' : '显示令牌'" @click="togglePassword('mounted.sessionToken')"><UiIcon :name="passwordVisible('mounted.sessionToken') ? 'eye-off' : 'eye'" :size="15" /></button></div></div>
                    </template>
                    <!-- 开关组：全部收成一行 chip，避免每个开关独占一行 -->
                    <div class="field login-field">
                      <div class="switch-chips">
                        <label class="switch-chip">
                          <button class="switch" :class="{ on: mountedForm.allowPrivateNetwork }" type="button" role="switch" :aria-checked="mountedForm.allowPrivateNetwork" @click="mountedForm.allowPrivateNetwork = !mountedForm.allowPrivateNetwork"></button>
                          <span>内网媒体预览</span>
                        </label>
                        <label v-if="providerId === 's3'" class="switch-chip">
                          <button class="switch" :class="{ on: mountedForm.forcePathStyle }" type="button" role="switch" :aria-checked="mountedForm.forcePathStyle" @click="mountedForm.forcePathStyle = !mountedForm.forcePathStyle"></button>
                          <span>路径风格</span>
                        </label>
                        <label v-if="providerId === 's3'" class="switch-chip">
                          <button class="switch" :class="{ on: mountedForm.verifyWrite }" type="button" role="switch" :aria-checked="mountedForm.verifyWrite" @click="mountedForm.verifyWrite = !mountedForm.verifyWrite"></button>
                          <span>写入权限验证</span>
                        </label>
                      </div>
                    </div>
                    <div v-if="providerId === 'webdav'" class="field login-field"><label>根目录 (可选)</label><input class="input" v-model="mountedForm.rootPath" placeholder="/" /></div>
                    <div v-else class="field login-field"><label>挂载路径 (可选)</label><input class="input" v-model="mountedForm.basePath" placeholder="/" /></div>
                  </div>
                </template>

                <!-- OAuth 授权 -->
                <div v-else-if="isOAuth" class="login-state-card oauth-box">
                  <div class="login-state-icon"><UiIcon name="external" :size="20" /></div>
                  <div>
                    <strong>在浏览器中完成授权</strong>
                    <p>点击下方按钮打开授权页面，完成后会自动返回并登录。</p>
                  </div>
                </div>
                <!-- 常规表单 -->
                <template v-else>
                  <div v-if="visibleFields.length" class="login-section">
                    <div v-for="f in visibleFields" v-show="isPan139FieldVisible(f)" :key="f.key" class="field login-field">
                      <label>{{ f.label }}</label>
                      <textarea v-if="isLongText(f.key)" class="textarea" v-model="form[f.key]" :placeholder="f.placeholder || ''" rows="3"></textarea>
                      <UiSelect v-else-if="f.type === 'select'" v-model="form[f.key]" :options="f.options || []" block />
                      <div v-else-if="f.type === 'password'" class="password-input-wrap"><input class="input" :type="passwordVisible(f.key) ? 'text' : 'password'" :inputmode="fieldInputMode(f)" v-model="form[f.key]" :placeholder="f.placeholder || ''" /><button class="password-toggle" type="button" :title="passwordVisible(f.key) ? '隐藏密码' : '显示密码'" :aria-label="passwordVisible(f.key) ? '隐藏密码' : '显示密码'" @click="togglePassword(f.key)"><UiIcon :name="passwordVisible(f.key) ? 'eye-off' : 'eye'" :size="15" /></button></div>
                      <input v-else class="input" :type="fieldInputType(f)" :inputmode="fieldInputMode(f)" v-model="form[f.key]" :placeholder="f.placeholder || ''" />
                      <div v-if="providerId === 'pan189' && f.key === 'validate_code' && pan189Captcha" class="captcha-image-row">
                        <img :src="pan189Captcha" alt="图形验证码" />
                        <span>请输入图片中的字符</span>
                      </div>
                      <div v-if="providerId === 'guangya' && f.key === 'sms_code' && !hasRefreshToken" class="field-action-row">
                        <button class="btn sm" :disabled="smsBusy || smsCountdown > 0" type="button" @click="sendSms">{{ smsBusy ? '发送中…' : (smsCountdown > 0 ? smsCountdown + ' 秒后重发' : '获取验证码') }}</button>
                      </div>
                      <div v-if="providerId === 'pan139' && f.key === 'sms_code'" class="field-action-row">
                        <button class="btn sm" :disabled="smsBusy || smsCountdown > 0" type="button" @click="sendPan139Sms">{{ smsBusy ? '发送中…' : (smsCountdown > 0 ? smsCountdown + ' 秒后重发' : '获取验证码') }}</button>
                      </div>
                      <div v-if="providerId === 'pan189' && f.key === 'sms_code'" class="field-action-row">
                        <button class="btn sm" :disabled="smsBusy || smsCountdown > 0" type="button" @click="sendPan189Sms">{{ smsBusy ? '发送中…' : (smsCountdown > 0 ? smsCountdown + ' 秒后重发' : '获取验证码') }}</button>
                      </div>
                    </div>
                  </div>
                  <div v-else class="login-empty-state">该网盘无需填写表单，直接点击登录。</div>
                </template>

                <!-- 网盘专属帮助链接 -->
                <button v-if="providerHelp" type="button" class="login-help" @click="openHelp">
                  <UiIcon name="external" :size="12" /><span>{{ providerHelp.label }}</span>
                </button>

                <!-- PikPak 滑块安全验证 -->
                <div v-if="captchaUrl" class="login-state-card captcha-box">
                  <div class="captcha-head">
                    <div>
                      <strong>{{ captchaNativeWindow ? '请在独立窗口中完成安全验证' : '请完成安全验证' }}</strong>
                      <p>{{ captchaFrameReady ? '验证完成后将自动继续登录。' : '正在加载验证页面…' }}</p>
                    </div>
                    <button class="btn sm" type="button" :disabled="captchaSubmitting || pikpakCooldownSeconds > 0" @click="reloadCaptcha">重新加载</button>
                    <button v-if="captchaNativeWindow" class="btn sm" type="button" :disabled="captchaOpening || busy" @click="openCaptchaWindow">打开验证窗口</button>
                  </div>
                  <iframe
                    v-if="!captchaNativeWindow && !captchaOpening"
                    class="captcha-frame"
                    :src="captchaUrl"
                    title="PikPak 安全验证"
                    referrerpolicy="strict-origin-when-cross-origin"
                    allow="clipboard-read; clipboard-write"
                    @load="captchaFrameReady = true"
                  ></iframe>
                </div>

                <!-- 统一错误提示（带 Shake 抖动动画） -->
                <div v-if="errorText" class="form-error shake">
                  <UiIcon name="warning" :size="14" /><span>{{ errorText }}</span>
                </div>
              </div>

              <div class="modal-actions login-actions">
                <button class="btn" type="button" @click="emit('close')">取消</button>
                <button class="btn primary" type="submit" :disabled="busy || (providerId === 'pikpak' && pikpakCooldownSeconds > 0)">
                  <span v-if="busy" class="spin spin-on-primary"></span>
                  {{ busy ? '处理中…' : (providerId === 'pikpak' && captchaUrl ? '继续登录' : (isOAuth ? '打开授权页面' : (isMounted ? '保存连接' : '登录'))) }}
                </button>
              </div>
            </form>
          </div>
        </div>
      </div>
    </transition>
  </teleport>
</template>

<style scoped>
.login-close { width: 28px; height: 28px; }
.login-side .lp-item {
  width: 100%; border: 0; background: transparent; font: inherit; text-align: left;
  display: flex; align-items: center; gap: 8px;
  padding: 6px 9px; border-radius: var(--radius-sm);
  cursor: pointer; font-size: 13px; color: var(--text-secondary);
  transition: background var(--motion-fast) ease, color var(--motion-fast) ease, transform var(--motion-spring);
}
.login-side .lp-item:hover { background: var(--bg-hover); color: var(--text-primary); }
.login-side .lp-item.active {
  background: var(--listselectbg);
  color: var(--color-primary);
  font-weight: 600;
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-primary) 18%, transparent);
}
.login-side .lp-item.active img { transform: scale(1.06); }
.login-side .lp-item img {
  width: 18px; height: 18px; object-fit: contain; flex-shrink: 0;
  transition: transform var(--motion-spring);
}
.login-side .lp-item:focus-visible,
.switch:focus-visible {
  outline: none; box-shadow: var(--ring-focus);
}
.login-form { display: flex; flex-direction: column; }
.login-form-content { display: grid; gap: 14px; }
.login-section { display: grid; gap: 13px; }
.login-field { margin: 0 !important; }
.login-field > label {
  display: block;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 5px;
}
.password-input-wrap { position: relative; display: flex; align-items: center; }
.password-input-wrap > .input { padding-right: 38px; }
.password-input-wrap input::-ms-reveal,
.password-input-wrap input::-ms-clear { display: none; width: 0; height: 0; }
.password-toggle {
  position: absolute; right: 5px; display: inline-flex; align-items: center; justify-content: center;
  width: 26px; height: 26px; padding: 0; border: 0; border-radius: var(--radius-xs);
  color: var(--text-tertiary); background: transparent; cursor: pointer;
  transition: color var(--motion-fast) ease, background var(--motion-fast) ease, transform var(--motion-spring);
}
.password-toggle:hover { color: var(--text-primary); background: var(--bg-hover); }
.password-toggle:active { transform: scale(0.92); }
.password-toggle:focus-visible { outline: none; box-shadow: var(--ring-focus); }
.switch-row { display: flex; align-items: center; gap: 9px; min-height: 24px; }
.switch { border: 0; padding: 0; }
.switch-row .hint { margin: 0; line-height: 1.45; }

/* 登录页切换网盘：内容区淡入 + 下滑入位 */
.login-form-content { animation: login-pane-in 240ms var(--motion-glide); }
@keyframes login-pane-in {
  from { opacity: 0; transform: translateY(10px); }
}

/* 开关组 chip：多个开关收进一行 */
.switch-chips { display: flex; flex-wrap: wrap; gap: 8px 18px; padding: 2px 0; }
.switch-chip { display: inline-flex; align-items: center; gap: 7px; font-size: 13px; color: var(--text-secondary); cursor: pointer; user-select: none; }
.switch-chip:hover { color: var(--text-primary); }
.login-state-card {
  display: flex; align-items: flex-start; gap: 12px;
  padding: 16px; border: 1px solid var(--border-light);
  border-radius: var(--radius-sm); background: var(--bg-subtle);
  color: var(--text-secondary); font-size: 13px; line-height: 1.55;
}
.login-state-card strong { display: block; color: var(--text-primary); font-size: 13.5px; }
.login-state-card p { margin: 3px 0 0; }
.login-state-icon {
  display: inline-flex; align-items: center; justify-content: center;
  width: 34px; height: 34px; flex: 0 0 34px;
  border-radius: var(--radius-sm); background: var(--listselectbg); color: var(--color-primary);
}
.oauth-box { min-height: 82px; align-items: center; }
.field-action-row { display: flex; margin-top: 8px; }
.captcha-image-row {
  display: flex; align-items: center; gap: 10px; margin-top: 8px;
  color: var(--text-tertiary); font-size: 12px;
}
.captcha-image-row img {
  height: 36px; min-width: 96px; object-fit: contain;
  border: 1px solid var(--border-light); border-radius: var(--radius-xs); background: #fff;
}
.login-empty-state {
  padding: 14px; border: 1px dashed var(--border-light); border-radius: var(--radius-sm);
  color: var(--text-tertiary); font-size: 13px; line-height: 1.5;
}
.login-help {
  display: inline-flex; align-items: center; gap: 5px; justify-self: start;
  border: none; background: none; padding: 0;
  color: var(--color-primary); font-size: 13px; cursor: pointer;
}
.login-help:hover { text-decoration: underline; }
.captcha-box {
  display: block; padding: 12px;
  background: color-mix(in srgb, var(--color-warning) 8%, transparent);
  border-color: color-mix(in srgb, var(--color-warning) 28%, var(--border-light));
  font-size: 13px; color: var(--text-secondary); line-height: 1.6;
}
.captcha-head { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; }
.captcha-head strong { display:block; color:var(--text-primary); font-size:13px; }
.captcha-head p { margin:3px 0 0; }
.captcha-frame {
  display:block; width:100%; height:320px; margin-top:10px;
  border:1px solid var(--border-light); border-radius:var(--radius-sm);
  background:#fff;
}
.login-actions {
  flex-shrink: 0; margin-top: 16px; padding: 14px 0 0;
  border-top: 1px solid var(--border-lighter);
}
@media (max-width: 640px) {
  .login-side .lp-item { width: auto; text-align: center; }
  .login-state-card { padding: 14px; }
  .captcha-frame { height:280px; }
  .captcha-head { align-items:stretch; flex-direction:column; }
  .captcha-head .btn { align-self:flex-start; }
}
</style>
