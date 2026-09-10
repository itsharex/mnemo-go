<script setup>
// 网页播放器只保留浏览器/WebView 可解码路径：原生 MP4/WebM/Ogg，按需加载
// HLS.js、dash.js 和 MPEG-TS 的 MSE 流。所有远程请求都经 Go 侧本地会话代理。
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { playVideo, playVideoQuality, pinFileSnapshot, getPlayCursor, savePlayCursor, getSettings, previewUrl, download, openKindOf } from '../api'
import { getPrefs } from '../appearance'
import { srtToVtt, parseSup, SupRenderer } from '../player/subtitles'
import { WindowMinimise, WindowToggleMaximise, WindowIsMaximised } from '../../wailsjs/runtime/runtime'
import UiIcon from './UiIcon.vue'

const props = defineProps({
  account: { type: Object, required: true },
  file: { type: Object, required: true },
  files: { type: Array, default: () => [] },
  capabilities: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['close', 'toast', 'select-file'])

const videoEl = ref(null)
const containerEl = ref(null)
const progressEl = ref(null)
const loading = ref(true)
const error = ref('')
const playing = ref(false)
const position = ref(0)
const duration = ref(0)
const buffered = ref(0)
const volume = ref(Math.min(200, Math.max(0, getPrefs().defaultVolume ?? 100)))
const muted = ref(false)
const brightness = ref(100)
const speed = ref(getPrefs().defaultSpeed || 1)
const seekStep = getPrefs().seekStep || 10
const qualities = ref([])
const currentQuality = ref('')
const src = ref('')
const streamType = ref('')
const looping = ref(false)
const isFullscreen = ref(false)
// 窗口控制（右上角）：全屏时隐藏
const winMax = ref(false)
function winMinimise() { try { WindowMinimise() } catch { /* browser preview */ } }
function winToggleMax() {
  try {
    WindowToggleMaximise()
    winMax.value = !winMax.value
    WindowIsMaximised().then((v) => { winMax.value = !!v }).catch(() => {})
  } catch { /* browser preview */ }
}
const pipActive = ref(false)
const showControls = ref(true)
const subtitleSources = ref([])
const subtitleTracks = ref([])
const currentSubtitle = ref('')
const subtitleEnabled = ref(false)
const activeMenu = ref('')
const subtitleScale = ref(1)
const subtitlePosition = ref('bottom')
const scrubVisible = ref(false)
const scrubX = ref(0)
const scrubTime = ref(0)
const centerPulse = ref(false)
const osdVisible = ref(false)
const osdIcon = ref('volume')
const osdText = ref('')
const osdPct = ref(0)
const supCanvasEl = ref(null)
const localSubInput = ref(null)
const extraTextSubs = ref([]) // 网盘同名字幕 + 本地文本字幕（srt/vtt）
const supTracks = ref([])     // SUP 图形字幕：{ label, url }
const supActive = ref(false)
const assTracks = ref([])     // ASS/SSA 特效字幕：{ label, url | content }
const assActive = ref(false)
const isBuffering = ref(false) // 播放过程中卡顿缓冲状态
const loadingSpeed = ref('') // 实时缓冲网速
let lastLoadedBytes = 0
let lastSpeedCalcTime = Date.now()

let unmounted = false
let playbackSeq = 0
let sourceSeq = 0
let controlsTimer = null
let saveTimer = null
let pendingResume = 0
let pendingAutoplay = true
let suppressVideoErrors = false
let hlsPlayer = null
let dashPlayer = null
let tsPlayer = null
let tsTimeBase = 0
let tsSeekController = null
let activePreview = null
let tsSeekSequence = 0
let hlsRecoveryAttempts = 0
let dashRecoveryAttempts = 0
let activeSourceURL = ''
let playbackEnded = false
let centerTimer = null
let osdTimer = null
let audioCtx = null
let gainNode = null
let supRenderer = null
let previousFocus = null
let assRenderer = null
let subtitleFetchController = null
let localSubtitleReader = null
const subtitleObjectURLs = new Set()
const subtitleCueTimes = new WeakMap()

const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2]
const speedOptions = SPEEDS.map((s) => ({ value: s, label: s + 'x' }))
const UNSUPPORTED_WEB_CONTAINERS = new Set(['avi', 'flv', 'm2ts', 'mkv', 'mpg', 'mpeg', 'mts', 'rm', 'rmvb', 'ts', 'wmv'])
const episodeFiles = computed(() => (props.files || []).filter((candidate) => !candidate?.isDir && isVideoFile(candidate)))
const episodeIndex = computed(() => episodeFiles.value.findIndex((candidate) => candidate.file_id === props.file?.file_id))
const currentQualityLabel = computed(() => qualities.value.find((quality) => quality.value === currentQuality.value)?.label || currentQuality.value || (streamType.value || '网页播放').toUpperCase())

onMounted(() => {
  previousFocus = document.activeElement
  containerEl.value?.focus()
  try { WindowIsMaximised().then((v) => { winMax.value = !!v }).catch(() => {}) } catch { /* browser preview */ }
  document.addEventListener('keydown', onKeyDown)
  document.addEventListener('fullscreenchange', onFullscreenChange)
  document.addEventListener('pointerdown', onDocumentPointerDown, true)
  window.addEventListener('resize', onWindowResize)
  startPlayback()
})
watch(() => props.file?.file_id, (nextId, previousId) => {
  if (!nextId || nextId === previousId || unmounted) return
  saveCursor(previousId)
  if (saveTimer) {
    clearInterval(saveTimer)
    saveTimer = null
  }
  startPlayback()
})
onBeforeUnmount(() => {
  unmounted = true
  playbackSeq++
  sourceSeq++
  saveCursor()
  cancelSubtitleFetch()
  cancelLocalSubtitleReader()
  revokeSubtitleObjectURLs()
  destroyAdaptivePlayers()
  if (saveTimer) clearInterval(saveTimer)
  if (controlsTimer) clearTimeout(controlsTimer)
  if (centerTimer) clearTimeout(centerTimer)
  if (osdTimer) clearTimeout(osdTimer)
  if (audioCtx) { try { audioCtx.close() } catch {}; audioCtx = null; gainNode = null }
  stopSup()
  destroyAss()
  document.removeEventListener('keydown', onKeyDown)
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  window.removeEventListener('resize', onWindowResize)
  nextTick(() => { if (previousFocus?.isConnected) previousFocus.focus?.() })
})

function isVideoFile(file) {
  return openKindOf(file, props.capabilities) === 'video'
}

async function startPlayback() {
  const seq = ++playbackSeq
  sourceSeq++
  if (saveTimer) {
    clearInterval(saveTimer)
    saveTimer = null
  }
  cancelSubtitleFetch()
  cancelLocalSubtitleReader()
  revokeSubtitleObjectURLs()
  destroyAdaptivePlayers()
  loading.value = true
  error.value = ''
  playing.value = false
  position.value = 0
  buffered.value = 0
  duration.value = 0
  src.value = ''
  activeSourceURL = ''
  playbackEnded = false
  streamType.value = ''
  subtitleSources.value = []
  subtitleTracks.value = []
  extraTextSubs.value = []
  supTracks.value = []
  assTracks.value = []
  stopSup()
  destroyAss()
  pendingResume = 0
  clearVideoSource()
  try {
    await pinFileSnapshot(props.account.user_id, props.account.drive_id, props.file)
    if (unmounted || seq !== playbackSeq) return
    const settings = await getSettings().catch(() => null)
    let resumeAt = 0
    if (!settings || settings.playbackResume !== false) {
      resumeAt = await getPlayCursor(props.account.user_id, props.account.drive_id, props.file.file_id).catch(() => 0)
    }
    if (unmounted || seq !== playbackSeq) return
    const preview = await playVideo(props.account.user_id, props.account.drive_id, props.file.file_id)
    if (unmounted || seq !== playbackSeq) return
    setQualityOptions(preview)
    if (preview && Number.isFinite(preview.duration) && preview.duration > 0) duration.value = preview.duration
    await mountCloudSubtitles()
    if (unmounted || seq !== playbackSeq) return
    loading.value = false
    await nextTick()
    if (unmounted || seq !== playbackSeq) return
    await loadPlaybackSource(preview, resumeAt, true, seq)
  } catch (e) {
    if (!unmounted && seq === playbackSeq) error.value = readablePlaybackError(e)
  } finally {
    if (!unmounted && seq === playbackSeq && loading.value) loading.value = false
  }
}

async function loadPlaybackSource(preview, resumeAt, autoplay, parentSeq) {
  const url = String(preview && preview.url || '').trim()
  if (!url) throw new Error('未获取到可播放的视频地址')
  const v = videoEl.value
  if (!v) throw new Error('播放器尚未初始化')
  const loadSeq = ++sourceSeq
  destroyAdaptivePlayers()
  clearVideoSource()
  src.value = ''
  activeSourceURL = url
  playbackEnded = false
  pendingResume = Math.max(0, Number(resumeAt) || 0)
  pendingAutoplay = Boolean(autoplay)
  streamType.value = normalizeStreamType(preview)
  activePreview = preview
  tsTimeBase = 0
  subtitleSources.value = [...normalizeSubtitles(preview && preview.subtitles), ...extraTextSubs.value]
  subtitleTracks.value = []
  subtitleEnabled.value = false
  currentSubtitle.value = ''
  stopSup()
  destroyAss()
  await nextTick()
  if (unmounted || parentSeq !== playbackSeq || loadSeq !== sourceSeq) return

  if (streamType.value === 'hls') {
    await loadHLS(v, url, loadSeq)
    return
  }
  if (streamType.value === 'dash') {
    await loadDASH(v, url, loadSeq)
    return
  }
  if (['ts', 'mpegts', 'm2ts', 'mts'].includes(streamType.value)) {
    if (resumeAt > 0) {
      await seekTS(resumeAt, autoplay)
      return
    }
    await loadMPEGTS(v, url, loadSeq, preview)
    return
  }
  if (UNSUPPORTED_WEB_CONTAINERS.has(streamType.value)) {
    throw new Error(`网页播放器不支持 ${streamType.value.toUpperCase()} 容器，请下载后使用本地播放器打开`)
  }
  await loadNativeSource(v, url, loadSeq)
}

async function loadNativeSource(v, url, loadSeq) {
  src.value = url
  await nextTick()
  if (unmounted || loadSeq !== sourceSeq || videoEl.value !== v) return
  suppressVideoErrors = false
  v.load()
}

async function loadMPEGTS(v, url, loadSeq, preview) {
  const mod = await import('mpegts.js')
  if (unmounted || loadSeq !== sourceSeq || videoEl.value !== v) return
  const mpegts = mod.default || mod
  if (!mpegts.isSupported()) throw new Error('当前系统 WebView 不支持 MPEG-TS 播放')
  const player = mpegts.createPlayer({
    type: 'mpegts', url, isLive: false,
    duration: Math.max(0, Number(preview?.duration) || 0) * 1000,
  }, {
    enableWorker: true, lazyLoad: true, lazyLoadMaxDuration: 90,
    autoCleanupSourceBuffer: true, autoCleanupMaxBackwardDuration: 60,
    autoCleanupMinBackwardDuration: 30,
  })
  tsPlayer = player
  suppressVideoErrors = false
  player.on(mpegts.Events.ERROR, (type) => {
    if (unmounted || player !== tsPlayer || loadSeq !== sourceSeq) return
    failPlayback(type === mpegts.ErrorTypes.NETWORK_ERROR
      ? '视频流加载失败，请重新获取播放地址或检查代理设置'
      : '视频流解析失败，请尝试切换清晰度')
  })
  player.attachMediaElement(v)
  player.load()
}

async function loadHLS(v, url, loadSeq) {
  if (canPlayNativeHLS(v)) {
    await loadNativeSource(v, url, loadSeq)
    return
  }
  const Hls = await getHlsConstructor()
  if (unmounted || loadSeq !== sourceSeq) return
  if (!Hls || !Hls.isSupported()) throw new Error('当前系统 WebView 不支持 HLS 播放')
  const player = new Hls({
    enableWorker: true,
    lowLatencyMode: false,
    capLevelToPlayerSize: true,
    maxBufferLength: 30,
    maxMaxBufferLength: 60,
    backBufferLength: 30,
  })
  hlsPlayer = player
  hlsRecoveryAttempts = 0
  suppressVideoErrors = false
  player.on(Hls.Events.MEDIA_ATTACHED, () => {
    if (!unmounted && player === hlsPlayer && loadSeq === sourceSeq) player.loadSource(url)
  })
  player.on(Hls.Events.ERROR, (_, data) => handleHLSError(Hls, player, data, loadSeq))
  player.attachMedia(v)
}

async function loadDASH(v, url, loadSeq, retrying = false) {
  if (!window.MediaSource) throw new Error('当前系统 WebView 不支持 DASH 播放')
  const dashjs = await getDashConstructor()
  if (unmounted || loadSeq !== sourceSeq) return
  const player = dashjs.MediaPlayer().create()
  dashPlayer = player
  if (!retrying) dashRecoveryAttempts = 0
  suppressVideoErrors = false
  player.updateSettings({
    streaming: {
      buffer: {
        bufferTimeAtTopQuality: 20,
        bufferTimeAtTopQualityLongForm: 30,
        stableBufferTime: 12,
      },
      fastSwitchEnabled: false,
    },
  })
  player.on(dashjs.MediaPlayer.events.ERROR, (event) => handleDASHError(player, event, loadSeq))
  player.initialize(v, url, false)
}

function handleHLSError(Hls, player, data, loadSeq) {
  if (!data || !data.fatal || player !== hlsPlayer || loadSeq !== sourceSeq) return
  if (data.type === Hls.ErrorTypes.NETWORK_ERROR && hlsRecoveryAttempts < 1) {
    hlsRecoveryAttempts++
    player.startLoad()
    return
  }
  if (data.type === Hls.ErrorTypes.MEDIA_ERROR && hlsRecoveryAttempts < 1) {
    hlsRecoveryAttempts++
    player.recoverMediaError()
    return
  }
  failPlayback('HLS 流加载失败，请检查网络或重新获取播放地址')
}

function handleDASHError(player, event, loadSeq) {
  if (player !== dashPlayer || loadSeq !== sourceSeq) return
  const retryURL = activeSourceURL
  if (dashRecoveryAttempts < 1 && retryURL && videoEl.value) {
    dashRecoveryAttempts++
    try {
      dashPlayer = null
      player.reset()
      void loadDASH(videoEl.value, retryURL, loadSeq, true).catch((e) => failPlayback(readablePlaybackError(e)))
      return
    } catch {}
  }
  const detail = event && event.error && event.error.message ? `：${event.error.message}` : ''
  failPlayback('DASH 流加载失败' + detail)
}

function failPlayback(message) {
  destroyAdaptivePlayers()
  playing.value = false
  error.value = message
}

function destroyAdaptivePlayers() {
  tsSeekController?.abort()
  tsSeekController = null
  const ts = tsPlayer
  tsPlayer = null
  if (ts) {
    try { ts.destroy() } catch {}
  }
  const hls = hlsPlayer
  hlsPlayer = null
  if (hls) {
    try { hls.destroy() } catch {}
  }
  const dash = dashPlayer
  dashPlayer = null
  if (dash) {
    try { dash.reset() } catch {}
  }
}

function clearVideoSource() {
  const v = videoEl.value
  if (!v) return
  suppressVideoErrors = true
  try {
    v.pause()
    v.removeAttribute('src')
    v.load()
  } catch {}
}

let hlsConstructorPromise = null
async function getHlsConstructor() {
  if (!hlsConstructorPromise) hlsConstructorPromise = import('hls.js').then((mod) => mod.default)
  return hlsConstructorPromise
}

let dashConstructorPromise = null
async function getDashConstructor() {
  if (!dashConstructorPromise) dashConstructorPromise = import('dashjs').then((mod) => mod.default || mod)
  return dashConstructorPromise
}

function normalizeStreamType(preview) {
  const declared = String(preview && preview.stream_type || '').trim().toLowerCase()
  if (declared === 'm3u8') return 'hls'
  if (declared === 'mpd') return 'dash'
  if (declared) return declared
  const ext = extensionOf(props.file.name)
  if (ext === 'm3u8') return 'hls'
  if (ext === 'mpd') return 'dash'
  if (['m4v', 'mov', '3gp'].includes(ext)) return 'mp4'
  return ext
}

function canPlayNativeHLS(v) {
  return Boolean(v.canPlayType('application/vnd.apple.mpegurl') || v.canPlayType('application/x-mpegURL'))
}

function extensionOf(name) {
  const value = String(name || '')
  const index = value.lastIndexOf('.')
  return index > 0 ? value.slice(index + 1).toLowerCase() : ''
}

function normalizeSubtitles(value) {
  if (!Array.isArray(value)) return []
  return value
    .map((track, index) => ({
      url: String(track && track.url || '').trim(),
      language: String(track && track.language || 'und').trim() || 'und',
      label: String(track && track.language || `字幕 ${index + 1}`).trim() || `字幕 ${index + 1}`,
    }))
    .filter((track) => track.url)
}

function setQualityOptions(preview) {
  const seen = new Set()
  qualities.value = (preview && Array.isArray(preview.qualities) ? preview.qualities : [])
    .map((q) => ({ value: String(q.value || q.quality || q.label || '').trim(), label: q.label || q.quality || q.value || '原画' }))
    .filter((q) => q.value && !seen.has(q.value) && seen.add(q.value))
  currentQuality.value = String(preview && preview.current_quality || (qualities.value[0] && qualities.value[0].value) || '')
}

function onLoaded() {
  const v = videoEl.value
  if (!v) return
  playbackEnded = false
  updateDuration(v)
  applyVolume()
  v.playbackRate = speed.value
  if (pendingResume > 0 && (!Number.isFinite(v.duration) || pendingResume < v.duration)) v.currentTime = pendingResume
  pendingResume = 0
  const autoplay = pendingAutoplay
  pendingAutoplay = false
  if (autoplay) v.play().catch(() => {})
  if (saveTimer) clearInterval(saveTimer)
  saveTimer = setInterval(saveCursor, 5000)
  onTracksChange()
}

function updateDuration(v) {
  if (tsPlayer) return
  if (Number.isFinite(v.duration) && v.duration > 0) duration.value = v.duration
}

function onDurationChange() {
  const v = videoEl.value
  if (v) updateDuration(v)
}

function onLoadedData() {
  onTracksChange()
}

function onTimeUpdate() {
  const v = videoEl.value
  if (!v) return
  if (playbackEnded && (!Number.isFinite(v.duration) || v.currentTime < v.duration)) playbackEnded = false
  position.value = v.currentTime + tsTimeBase
  updateBuffered(v)
  if (supActive.value) renderSupFrame()
}

function onProgress() {
  const v = videoEl.value
  if (!v) return
  updateBuffered(v)
  // 计算缓冲实时网速
  try {
    if (v.buffered.length > 0) {
      const now = Date.now()
      const dt = (now - lastSpeedCalcTime) / 1000
      if (dt >= 0.5) {
        // 估算缓冲字节速率：按当前 buffer 秒数 × 比特率估算
        const bufEnd = v.buffered.end(v.buffered.length - 1)
        const dur = v.duration || 1
        const fSize = props.file?.size || 0
        if (fSize > 0 && dur > 0) {
          const approxBytes = (bufEnd / dur) * fSize
          const speedBps = Math.max(0, (approxBytes - lastLoadedBytes) / dt)
          lastLoadedBytes = approxBytes
          lastSpeedCalcTime = now
          loadingSpeed.value = formatSpeed(speedBps)
        }
      }
    }
  } catch {}
}

function updateBuffered(v) {
  try {
    if (v.buffered.length > 0) buffered.value = v.buffered.end(v.buffered.length - 1) + tsTimeBase
  } catch {}
}

function onPlay() { playbackEnded = false; playing.value = true; isBuffering.value = false; scheduleHideControls() }
function onPause() { playing.value = false; isBuffering.value = false; showControls.value = true }
function onWaiting() { isBuffering.value = true }
function onPlaying() { isBuffering.value = false }
function onCanPlay() { isBuffering.value = false }
function onCanPlayThrough() { isBuffering.value = false }
function onEnded() {
  playing.value = false
  if (!looping.value) {
    playbackEnded = true
    clearPlayCursor()
    showControls.value = getPrefs().autoCloseOnEnd ? false : true
  }
}
function onError() {
  if (loading.value || suppressVideoErrors || hlsPlayer || dashPlayer || tsPlayer) return
  const code = videoEl.value && videoEl.value.error && videoEl.value.error.code
  error.value = code === 4 ? '当前网页播放器不支持此视频的容器或编解码' : '视频加载失败，请检查网络或重新获取播放地址'
}
function onVolumeChange() {
  const v = videoEl.value
  if (!v) return
  // 增益链接管后 video.volume 恒为 1，音量以 volume ref 为准
  if (!gainNode) volume.value = Math.round(v.volume * 100)
  muted.value = v.muted
}
function onPipEnter() { pipActive.value = true }
function onPipLeave() { pipActive.value = false }

// ---- controls ----
function togglePlay() {
  const v = videoEl.value
  if (!v) return
  if (audioCtx && audioCtx.state === 'suspended') audioCtx.resume().catch(() => {})
  centerPulse.value = true
  if (centerTimer) clearTimeout(centerTimer)
  centerTimer = setTimeout(() => { centerPulse.value = false }, 700)
  if (v.paused) v.play().catch(() => {})
  else v.pause()
}

function seek(delta) {
  const v = videoEl.value
  if (!v) return
  seekTo(position.value + delta)
}

function seekTo(requested) {
  const v = videoEl.value
  if (!v || !Number.isFinite(requested)) return false
  tsSeekController?.abort()
  tsSeekController = null
  const total = Number.isFinite(v.duration) ? v.duration : duration.value
  const target = Math.max(0, total > 0 ? Math.min(total, requested) : requested)
  if (tsPlayer && target > 0) {
    let bufferedTarget = false
    for (let i = 0; i < v.buffered.length; i++) {
      if (target >= v.buffered.start(i) + tsTimeBase && target < v.buffered.end(i) + tsTimeBase) bufferedTarget = true
    }
    if (!bufferedTarget) {
      seekTS(target, !v.paused)
      return true
    }
  }
  playbackEnded = false
  if (tsPlayer && target < tsTimeBase) { seekTS(target, !v.paused); return true }
  v.currentTime = target - tsTimeBase
  position.value = target
  return true
}

async function seekTS(target, autoplay) {
  const requestID = ++tsSeekSequence
  tsSeekController?.abort()
  const controller = new AbortController()
  tsSeekController = controller
  isBuffering.value = true
  position.value = target
  try {
    const requestURL = new URL(activeSourceURL)
    requestURL.searchParams.set('seek', String(target))
    const response = await fetch(requestURL, { signal: controller.signal })
    if (!response.ok) throw new Error('视频定位失败，请重试')
    const result = await response.json()
    if (unmounted || requestID !== tsSeekSequence || controller.signal.aborted) return
    tsSeekController = null
    const loadSeq = ++sourceSeq
    destroyAdaptivePlayers()
    clearVideoSource()
    tsTimeBase = Number(result.start) || 0
    if (assRenderer) assRenderer.timeOffset = tsTimeBase
    onTracksChange()
    pendingResume = Math.max(0, target - tsTimeBase)
    pendingAutoplay = autoplay
    playbackEnded = false
    error.value = ''
    await loadMPEGTS(videoEl.value, new URL(result.url, activeSourceURL).href, loadSeq, activePreview)
  } catch (e) {
    if (unmounted || requestID !== tsSeekSequence || controller.signal.aborted) return
    isBuffering.value = false
    position.value = (videoEl.value?.currentTime || 0) + tsTimeBase
    emit('toast', readablePlaybackError(e), 'error')
  } finally {
    if (tsSeekController === controller) tsSeekController = null
  }
}

function onSeekInput(e) {
  if (!seekTo(Number(e.target.value))) e.target.value = String(position.value)
}

function onVolume(e) {
  volume.value = Number(e.target.value)
  applyVolume()
  showOsd(muted.value || volume.value === 0 ? 'volume-x' : 'volume', `音量 ${muted.value ? 0 : volume.value}%`, (muted.value ? 0 : volume.value) / 2)
}

// 音量 0–200%：100% 以内用原生 volume，超过后接入 WebAudio 增益链
function ensureAudioChain() {
  const v = videoEl.value
  if (gainNode || !v) return
  try {
    const Ctx = window.AudioContext || window.webkitAudioContext
    if (!Ctx) return
    audioCtx = audioCtx || new Ctx()
    if (audioCtx.state === 'suspended') audioCtx.resume().catch(() => {})
    const source = audioCtx.createMediaElementSource(v)
    gainNode = audioCtx.createGain()
    source.connect(gainNode)
    gainNode.connect(audioCtx.destination)
  } catch {
    gainNode = null
  }
}

function applyVolume() {
  const v = videoEl.value
  if (!v) return
  const level = volume.value
  if (level > 100) ensureAudioChain()
  if (gainNode) {
    v.volume = 1
    gainNode.gain.value = level / 100
  } else {
    v.volume = Math.min(100, level) / 100
  }
  if (level > 0) v.muted = false
}

function showOsd(icon, text, pct) {
  osdIcon.value = icon
  osdText.value = text
  osdPct.value = Math.max(0, Math.min(100, pct))
  osdVisible.value = true
  if (osdTimer) clearTimeout(osdTimer)
  osdTimer = setTimeout(() => { osdVisible.value = false }, 900)
}

function toggleMute() {
  const v = videoEl.value
  if (v) v.muted = !v.muted
}

function onSpeed(value) {
  speed.value = Number(value)
  const v = videoEl.value
  if (v) v.playbackRate = speed.value
}

function toggleLoop() {
  looping.value = !looping.value
  const v = videoEl.value
  if (v) v.loop = looping.value
}

function toggleFullscreen() {
  const el = containerEl.value
  const video = videoEl.value
  if (!el) return
  const wailsRuntime = window.runtime
  if (wailsRuntime && typeof wailsRuntime.WindowFullscreen === 'function') {
    try {
      if (isFullscreen.value) {
        wailsRuntime.WindowUnfullscreen?.()
        isFullscreen.value = false
      } else {
        wailsRuntime.WindowFullscreen()
        isFullscreen.value = true
      }
      return
    } catch {}
  }
  if (document.fullscreenElement === el) {
    if (typeof document.exitFullscreen === 'function') document.exitFullscreen().catch?.(() => {})
    return
  }
  if (typeof el.requestFullscreen === 'function') {
    const request = el.requestFullscreen()
    if (request && typeof request.catch === 'function') request.catch(() => emit('toast', '当前系统不支持全屏播放', 'error'))
    return
  }
  // WebKit's desktop/iOS WebView exposes the legacy video-only API.
  if (video && typeof video.webkitEnterFullscreen === 'function') {
    video.webkitEnterFullscreen()
    return
  }
  emit('toast', '当前系统不支持全屏播放', 'error')
}

function onFullscreenChange() {
  isFullscreen.value = document.fullscreenElement === containerEl.value
  // 通知后端：全屏播放时隐藏桌面悬浮球
  try { window.runtime?.EventsEmit?.('app:fullscreen', isFullscreen.value) } catch { /* 无 bridge */ }
}

function onWebkitFullscreenEnter() { isFullscreen.value = true }
function onWebkitFullscreenLeave() { isFullscreen.value = false }

function onWebkitPresentationModeChange() {
  const video = videoEl.value
  if (!video) return
  pipActive.value = video.webkitPresentationMode === 'picture-in-picture'
}

async function togglePip() {
  const v = videoEl.value
  if (!v) return
  try {
    if (document.pictureInPictureElement) await document.exitPictureInPicture()
    else if (document.pictureInPictureEnabled) await v.requestPictureInPicture()
    else if (typeof v.webkitSetPresentationMode === 'function') v.webkitSetPresentationMode('picture-in-picture')
    else throw new Error('当前系统 WebView 不支持画中画')
  } catch (e) {
    emit('toast', '画中画不可用: ' + String(e), 'error')
  }
}

function screenshot() {
  const v = videoEl.value
  if (!v || !v.videoWidth) return
  try {
    const canvas = document.createElement('canvas')
    canvas.width = v.videoWidth
    canvas.height = v.videoHeight
    const context = canvas.getContext('2d')
    if (!context) return
    context.drawImage(v, 0, 0)
    canvas.toBlob((blob) => {
      if (!blob) return
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = (props.file.name || 'screenshot').replace(/\.[^.]+$/, '') + '-' + Math.floor(position.value) + 's.png'
      link.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      emit('toast', '截图已保存')
    }, 'image/png')
  } catch (e) {
    emit('toast', '当前视频无法截图: ' + String(e), 'error')
  }
}

// ---- subtitle ----
function onTracksChange() {
  const v = videoEl.value
  if (!v) return
  const tracks = []
  let selected = -1
  for (let index = 0; index < v.textTracks.length; index++) {
    const track = v.textTracks[index]
    if (track.cues) for (const cue of track.cues) {
      if (!subtitleCueTimes.has(cue)) subtitleCueTimes.set(cue, { start: cue.startTime, end: cue.endTime })
      const original = subtitleCueTimes.get(cue)
      cue.startTime = original.start - tsTimeBase
      cue.endTime = original.end - tsTimeBase
    }
    tracks.push({ index, label: track.label || `字幕 ${index + 1}`, kind: track.kind })
    if (track.mode === 'showing') selected = index
  }
  subtitleTracks.value = tracks
  subtitleEnabled.value = selected >= 0
  currentSubtitle.value = selected >= 0 ? String(selected) : ''
  setSubtitlePosition(subtitlePosition.value)
  // 自动挂载同名字幕偏好：有可用文本轨且未选择时默认开启第一条
  if (selected < 0 && tracks.length > 0 && !supActive.value && getPrefs().autoLoadSubtitles !== false) selectSubtitle(0)
}

function selectSubtitle(index) {
  const v = videoEl.value
  if (!v) return
  for (let trackIndex = 0; trackIndex < v.textTracks.length; trackIndex++) {
    v.textTracks[trackIndex].mode = trackIndex === index ? 'showing' : 'disabled'
  }
  subtitleEnabled.value = index >= 0
  currentSubtitle.value = index >= 0 ? String(index) : ''
}

function toggleSubtitle() {
  if (supActive.value) { stopSup(); currentSubtitle.value = ''; return }
  if (assActive.value) { destroyAss(); currentSubtitle.value = ''; return }
  if (subtitleEnabled.value) selectSubtitle(-1)
  else if (subtitleTracks.value.length > 0) selectSubtitle(0)
}

function onSubtitleSelected(value) {
  stopSup()
  destroyAss()
  selectSubtitle(value === 'off' ? -1 : Number(value))
}

// ---- 网盘同名字幕 / 本地字幕 / SUP 图形字幕 ----
function isSubtitleSibling(file, base) {
  const name = String(file?.name || '').toLowerCase()
  if (!name.startsWith(base + '.')) return false
  return ['srt', 'vtt', 'ass', 'ssa', 'sup'].includes(extensionOf(name))
}

async function mountCloudSubtitles() {
  const seq = playbackSeq
  const base = String(props.file.name || '').replace(/\.[^.]+$/, '').toLowerCase()
  if (!base) return
  const siblings = (props.files || []).filter((f) => !f.isDir && f.file_id !== props.file.file_id && isSubtitleSibling(f, base))
  for (const sibling of siblings) {
    const ext = extensionOf(sibling.name)
    try {
      const url = await previewUrl(props.account.user_id, props.account.drive_id, sibling.file_id)
      if (unmounted || seq !== playbackSeq) return
      if (ext === 'sup') {
        supTracks.value = [...supTracks.value, { label: sibling.name, url }]
      } else if (ext === 'ass' || ext === 'ssa') {
        assTracks.value = [...assTracks.value, { label: sibling.name, url }]
      } else {
        // 代理按扩展名自动完成 srt→vtt 转换
        extraTextSubs.value = [...extraTextSubs.value, { url, label: sibling.name }]
      }
    } catch { /* 单个字幕获取失败不影响其余 */ }
  }
}

function textBlobUrl(text) {
  return registerSubtitleObjectURL(URL.createObjectURL(new Blob([text], { type: 'text/vtt' })))
}

function registerSubtitleObjectURL(url) {
  if (url) subtitleObjectURLs.add(url)
  return url
}

function revokeSubtitleObjectURLs() {
  for (const url of subtitleObjectURLs) {
    try { URL.revokeObjectURL(url) } catch {}
  }
  subtitleObjectURLs.clear()
}

function cancelSubtitleFetch() {
  const controller = subtitleFetchController
  subtitleFetchController = null
  if (controller) {
    try { controller.abort() } catch {}
  }
}

function cancelLocalSubtitleReader() {
  const reader = localSubtitleReader
  localSubtitleReader = null
  if (reader && reader.readyState === 1) {
    try { reader.abort() } catch {}
  }
}

async function selectSup(index) {
  const track = supTracks.value[index]
  if (!track) return
  cancelSubtitleFetch()
  const controller = new AbortController()
  subtitleFetchController = controller
  selectSubtitle(-1)
  destroyAss()
  try {
    const response = await fetch(track.url, { signal: controller.signal })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    const buffer = await response.arrayBuffer()
    if (unmounted || controller.signal.aborted) return
    const sets = parseSup(buffer)
    if (!sets.length) throw new Error('no display sets')
    if (!supRenderer) supRenderer = new SupRenderer(supCanvasEl.value)
    supRenderer.load(sets)
    supActive.value = true
    currentSubtitle.value = 'sup:' + index
    renderSupFrame()
  } catch (error) {
    if (unmounted || error?.name === 'AbortError') return
    emit('toast', 'SUP 字幕解析失败', 'error')
  } finally {
    if (subtitleFetchController === controller) subtitleFetchController = null
  }
}

function stopSup() {
  supActive.value = false
  if (supRenderer) supRenderer.stop()
}

// ---- ASS/SSA 特效字幕（libass WASM 渲染，保留定位/颜色/卡拉 OK 等全部特效） ----
let jassubPromise = null
async function getJassub() {
  if (!jassubPromise) {
    jassubPromise = Promise.all([
      import('jassub'),
      import('jassub/dist/wasm/jassub-worker.js?url'),
      import('jassub/dist/wasm/jassub-worker.wasm?url'),
      import('jassub/dist/default.woff2?url'),
    ]).then(([mod, worker, wasm, font]) => ({
      JASSUB: mod.default,
      workerUrl: worker.default,
      wasmUrl: wasm.default,
      fontUrl: font.default,
    }))
  }
  return jassubPromise
}

async function selectAss(index) {
  const track = assTracks.value[index]
  if (!track) return
  cancelSubtitleFetch()
  const controller = new AbortController()
  subtitleFetchController = controller
  selectSubtitle(-1)
  stopSup()
  try {
    let content = track.content
    if (!content) {
      const response = await fetch(track.url, { signal: controller.signal })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      content = await response.text()
    }
    if (unmounted || controller.signal.aborted) return
    const { JASSUB, workerUrl, wasmUrl, fontUrl } = await getJassub()
    if (unmounted || controller.signal.aborted) return
    destroyAss()
    assRenderer = new JASSUB({ video: videoEl.value, subContent: content, workerUrl, wasmUrl, fonts: [fontUrl], timeOffset: tsTimeBase })
    assActive.value = true
    currentSubtitle.value = 'ass:' + index
  } catch (error) {
    if (unmounted || error?.name === 'AbortError') return
    emit('toast', 'ASS 字幕加载失败', 'error')
  } finally {
    if (subtitleFetchController === controller) subtitleFetchController = null
  }
}

function destroyAss() {
  assActive.value = false
  if (assRenderer) {
    try { assRenderer.destroy() } catch {}
    assRenderer = null
  }
}

function onWindowResize() {
  if (supActive.value) renderSupFrame()
}

function renderSupFrame() {
  const v = videoEl.value
  const canvas = supCanvasEl.value
  if (!v || !canvas || !supRenderer) return
  supRenderer.renderAt(v.currentTime + tsTimeBase, computeSubtitleBox(v, canvas))
}

// SUP 按 16:9 规格制作：渲染框锁定为视频内容矩形内的最大 16:9 区域，其余保持透明
function computeSubtitleBox(v, canvas) {
  const cw = Math.round(v.clientWidth)
  const ch = Math.round(v.clientHeight)
  if (!cw || !ch) return null
  if (canvas.width !== cw || canvas.height !== ch) { canvas.width = cw; canvas.height = ch }
  const vw = v.videoWidth || 16
  const vh = v.videoHeight || 9
  const scale = Math.min(cw / vw, ch / vh)
  const dw = vw * scale
  const dh = vh * scale
  const dx = (cw - dw) / 2
  const dy = (ch - dh) / 2
  let bw = dw
  let bh = dw * 9 / 16
  if (bh > dh) { bh = dh; bw = dh * 16 / 9 }
  return { x: dx + (dw - bw) / 2, y: dy + (dh - bh) / 2, w: bw, h: bh }
}

function onLocalSubtitlePicked(event) {
  const picked = event.target.files && event.target.files[0]
  event.target.value = ''
  if (!picked) return
  cancelLocalSubtitleReader()
  const ext = extensionOf(picked.name)
  const label = picked.name + '（本地）'
  if (ext === 'sup') {
    const url = registerSubtitleObjectURL(URL.createObjectURL(picked))
    supTracks.value = [...supTracks.value, { label, url }]
    selectSup(supTracks.value.length - 1)
    return
  }
  if (ext === 'ass' || ext === 'ssa') {
    const reader = new FileReader()
    localSubtitleReader = reader
    reader.onload = () => {
      if (unmounted || localSubtitleReader !== reader) return
      localSubtitleReader = null
      assTracks.value = [...assTracks.value, { label, content: String(reader.result || '') }]
      selectAss(assTracks.value.length - 1)
    }
    reader.onerror = () => { if (localSubtitleReader === reader) localSubtitleReader = null }
    reader.onabort = () => { if (localSubtitleReader === reader) localSubtitleReader = null }
    reader.readAsText(picked)
    return
  }
  const reader = new FileReader()
  localSubtitleReader = reader
  reader.onload = () => {
    if (unmounted || localSubtitleReader !== reader) return
    localSubtitleReader = null
    const text = String(reader.result || '')
    const vtt = srtToVtt(text)
    const url = textBlobUrl(vtt)
    const entry = { url, language: 'und', label }
    extraTextSubs.value = [...extraTextSubs.value, entry]
    subtitleSources.value = [...subtitleSources.value, entry]
    nextTick(() => {
      const v = videoEl.value
      if (v && v.textTracks.length > 0) selectSubtitle(v.textTracks.length - 1)
    })
  }
  reader.onerror = () => { if (localSubtitleReader === reader) localSubtitleReader = null }
  reader.onabort = () => { if (localSubtitleReader === reader) localSubtitleReader = null }
  reader.readAsText(picked)
}

function setSubtitleScale(value) {
  subtitleScale.value = Math.max(0.8, Math.min(1.5, Number(value) || 1))
}

function setSubtitlePosition(value) {
  subtitlePosition.value = value === 'top' ? 'top' : 'bottom'
  const video = videoEl.value
  if (!video) return
  for (let index = 0; index < video.textTracks.length; index++) {
    const cues = video.textTracks[index].cues
    if (!cues) continue
    for (let cueIndex = 0; cueIndex < cues.length; cueIndex++) {
      try { cues[cueIndex].line = subtitlePosition.value === 'top' ? 10 : 90 } catch {}
    }
  }
}

function selectEpisode(file) {
  if (!file || file.file_id === props.file?.file_id) {
    activeMenu.value = ''
    return
  }
  activeMenu.value = ''
  emit('select-file', file)
}

// 底部控制条上一集/下一集快捷切换
function switchEpisode(step) {
  const list = episodeFiles.value
  if (list.length <= 1) return
  const next = episodeIndex.value + step
  if (next < 0 || next >= list.length) return
  selectEpisode(list[next])
}

// 播放失败时的下载兜底（不支持容器 / 资源失效均可直接取回）
async function downloadCurrent() {
  try {
    await download(props.account.user_id, props.account.drive_id, props.file)
    emit('toast', '已加入下载队列', 'success')
  } catch (e) {
    emit('toast', String(e), 'error')
  }
}

// ---- popover menus ----
function toggleMenu(name) {
  activeMenu.value = activeMenu.value === name ? '' : name
  if (activeMenu.value) showControls.value = true
}

function closeMenu() {
  activeMenu.value = ''
}

// ---- save cursor ----
function saveCursor(fileId = props.file?.file_id) {
  if (playbackEnded) {
    clearPlayCursor(fileId)
    return
  }
  const v = videoEl.value
  if (!v || !v.currentTime || v.currentTime < 1) return
  if (!fileId) return
  savePlayCursor(props.account.user_id, props.account.drive_id, fileId, v.currentTime + tsTimeBase).catch(() => {})
}

function clearPlayCursor(fileId = props.file?.file_id) {
  if (!fileId) return
  savePlayCursor(props.account.user_id, props.account.drive_id, fileId, 0).catch(() => {})
}

let qualitySeq = 0
async function switchQuality(quality) {
  if (!quality) return
  const requestSeq = ++qualitySeq
  if (quality === currentQuality.value) return
  const v = videoEl.value
  const wasPlaying = Boolean(v && !v.paused)
  const currentTime = v ? v.currentTime + tsTimeBase : 0
  const seq = playbackSeq
  try {
    const preview = await playVideoQuality(props.account.user_id, props.account.drive_id, props.file.file_id, quality)
    if (unmounted || seq !== playbackSeq || requestSeq !== qualitySeq) return
    setQualityOptions(preview)
    error.value = ''
    await loadPlaybackSource(preview, currentTime, wasPlaying, seq)
  } catch (e) {
    if (unmounted || seq !== playbackSeq || requestSeq !== qualitySeq) return
    emit('toast', readablePlaybackError(e), 'error')
  }
}

// ---- keyboard ----
function onKeyDown(e) {
  if (e.code === 'Escape') {
    e.preventDefault()
    if (activeMenu.value) activeMenu.value = ''
    else if (isFullscreen.value) toggleFullscreen()
    else emit('close')
    return
  }
  if (e.key === 'Tab') {
    const controls = [...(containerEl.value?.querySelectorAll('button:not(:disabled), input:not([type="file"]):not(:disabled)') || [])].filter(el => el.offsetParent !== null)
    const index = controls.indexOf(document.activeElement)
    if (controls.length && (index < 0 || (e.shiftKey ? index === 0 : index === controls.length - 1))) {
      e.preventDefault()
      controls[e.shiftKey ? controls.length - 1 : 0].focus()
    }
    return
  }
  if (e.ctrlKey || e.metaKey || e.altKey || e.target?.isContentEditable) return
  if (e.target && /^(INPUT|TEXTAREA|SELECT|BUTTON)$/.test(e.target.tagName)) return
  switch (e.code) {
    case 'Space': e.preventDefault(); togglePlay(); break
    case 'ArrowLeft': e.preventDefault(); seek(-seekStep); break
    case 'ArrowRight': e.preventDefault(); seek(seekStep); break
    case 'ArrowUp': e.preventDefault(); adjustVolume(5); break
    case 'ArrowDown': e.preventDefault(); adjustVolume(-5); break
    case 'KeyF': toggleFullscreen(); break
    case 'KeyM': toggleMute(); break
    case 'KeyP': togglePip(); break
    case 'KeyL': toggleLoop(); break
    case 'KeyS': screenshot(); break
    case 'KeyC': toggleSubtitle(); break
  }
  showControls.value = true
  scheduleHideControls()
}

function adjustVolume(delta) {
  if (!videoEl.value) return
  volume.value = Math.max(0, Math.min(200, volume.value + delta))
  applyVolume()
  showOsd(muted.value || volume.value === 0 ? 'volume-x' : 'volume', `音量 ${muted.value ? 0 : volume.value}%`, (muted.value ? 0 : volume.value) / 2)
}

function adjustBrightness(delta) {
  brightness.value = Math.max(20, Math.min(200, brightness.value + delta))
  showOsd('sun', `亮度 ${brightness.value}%`, brightness.value / 2)
}

// ---- auto-hide controls ----
function scheduleHideControls() {
  if (controlsTimer) clearTimeout(controlsTimer)
  if (!playing.value) return
  controlsTimer = setTimeout(() => {
    if (!activeMenu.value && !containerEl.value?.querySelector('.pp-bottom:hover, .pp-bottom:focus-within, .pp-topbar:hover, .pp-topbar:focus-within')) showControls.value = false
  }, 2600)
}

function onMouseMove() {
  showControls.value = true
  scheduleHideControls()
}

function onMouseLeave() {
  if (playing.value && !activeMenu.value && !containerEl.value?.querySelector(':focus-within')) showControls.value = false
}

function onProgressPointerMove(event) {
  const element = progressEl.value
  if (!element || !duration.value) return
  const rect = element.getBoundingClientRect()
  const ratio = Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width))
  scrubX.value = ratio * 100
  scrubTime.value = ratio * duration.value
  scrubVisible.value = true
}

function onProgressPointerLeave() { scrubVisible.value = false }

function onWheel(event) {
  if (event.target?.closest?.('.pp-pop, .pp-bottom, .pp-topbar')) return
  if (Math.abs(event.deltaY) < 1) return
  const el = containerEl.value
  const half = el ? el.getBoundingClientRect().width / 2 : window.innerWidth / 2
  const step = event.deltaY < 0 ? 5 : -5
  if (event.clientX < half) adjustBrightness(step)
  else adjustVolume(step)
}

function onDocumentPointerDown(event) {
  if (activeMenu.value && !event.target.closest('.pp-menu-root')) {
    activeMenu.value = ''
  }
}

function readablePlaybackError(errorValue) {
  const message = String(errorValue && errorValue.message || errorValue || '')
  return message || '视频加载失败，请重试'
}

function fmtTime(seconds) {
  if (!seconds || !Number.isFinite(seconds)) return '00:00'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return String(m).padStart(2, '0') + ':' + String(s).padStart(2, '0')
}

const pct = computed(() => duration.value > 0 ? Math.min(100, (position.value / duration.value) * 100) : 0)
const bufPct = computed(() => duration.value > 0 ? Math.min(100, (buffered.value / duration.value) * 100) : 0)
</script>

<template>
  <teleport to="body">
    <div
      ref="containerEl"
      class="player-panel"
      :class="{ 'cursor-hidden': !showControls && playing, fullscreen: isFullscreen }"
      role="dialog"
      aria-modal="true"
      :aria-label="'视频预览：' + file.name"
      tabindex="-1"
      :style="{ '--subtitle-scale': subtitleScale }"
      @mousemove="onMouseMove"
      @mouseleave="onMouseLeave"
      @wheel="onWheel"
    >
      <div class="pp-stage">
        <video
          ref="videoEl"
          v-show="!loading && !error"
          :src="src"
          class="pp-video"
          :style="{ filter: 'brightness(' + brightness / 100 + ')' }"
          @loadedmetadata="onLoaded"
          @loadeddata="onLoadedData"
          @durationchange="onDurationChange"
          @timeupdate="onTimeUpdate"
          @progress="onProgress"
          @play="onPlay"
          @pause="onPause"
          @waiting="onWaiting"
          @playing="onPlaying"
          @canplay="onCanPlay"
          @canplaythrough="onCanPlayThrough"
          @ended="onEnded"
          @error="onError"
          @volumechange="onVolumeChange"
          @enterpictureinpicture="onPipEnter"
          @leavepictureinpicture="onPipLeave"
          @webkitpresentationmodechanged="onWebkitPresentationModeChange"
          @webkitbeginfullscreen="onWebkitFullscreenEnter"
          @webkitendfullscreen="onWebkitFullscreenLeave"
          @click="togglePlay"
          @dblclick="toggleFullscreen"
          preload="metadata"
          crossorigin="anonymous"
          playsinline
        >
          <track
            v-for="track in subtitleSources"
            :key="track.url"
            kind="subtitles"
            :src="track.url"
            :srclang="track.language"
            :label="track.label"
            @load="onTracksChange"
            @error="onTracksChange"
          />
        </video>
        <canvas v-show="supActive" ref="supCanvasEl" class="pp-sup-canvas"></canvas>
        <!-- 初始加载或卡顿缓冲状态（带光晕脉冲与实时速度） -->
        <div v-if="loading || isBuffering" class="pp-state pp-buffering-state">
          <div class="pp-loader-box">
            <span class="pp-spinner"></span>
            <span class="pp-loader-text">{{ loading ? '正在载入视频…' : '正在缓冲…' }}</span>
            <span v-if="loadingSpeed" class="pp-loader-speed">{{ loadingSpeed }}</span>
          </div>
        </div>
        <div v-else-if="error" class="pp-state pp-error">
          <UiIcon name="warning" :size="28" />
          <span class="pp-error-text">{{ error }}</span>
          <div class="pp-error-actions">
            <button class="pp-retry" type="button" @click="startPlayback"><UiIcon name="refresh" :size="14" />重新加载</button>
            <button class="pp-retry pp-retry-solid" type="button" @click="downloadCurrent"><UiIcon name="download" :size="14" />下载文件</button>
          </div>
        </div>
        <transition name="pp-osd">
          <div v-if="osdVisible" class="pp-osd">
            <UiIcon :name="osdIcon" :size="17" />
            <span class="pp-osd-text">{{ osdText }}</span>
            <div class="pp-osd-bar"><i :style="{ width: osdPct + '%' }"></i></div>
          </div>
        </transition>
        <transition name="pp-center-fade">
          <button
            v-if="!loading && !error && (!playing || centerPulse)"
            type="button"
            class="pp-center"
            :title="playing ? '暂停 (空格)' : '播放 (空格)'"
            @click="togglePlay"
          ><UiIcon :name="playing ? 'pause' : 'play'" :size="32" /></button>
        </transition>
      </div>

      <header class="pp-topbar" :class="{ hidden: isFullscreen && !showControls && playing }" @focusin="onMouseMove" @focusout="scheduleHideControls">
        <div class="pp-media-mark"><UiIcon name="video" :size="20" /></div>
        <div class="pp-file-meta pp-window-drag">
          <div class="pp-title" :title="file.name">{{ file.name }}</div>
          <div class="pp-sub">{{ currentQualityLabel }}<span v-if="episodeFiles.length > 1"> · 第 {{ episodeIndex + 1 }} 集 / 共 {{ episodeFiles.length }} 集</span></div>
        </div>
        <div class="pp-top-actions">
          <div v-if="!isFullscreen" class="pp-window-controls" aria-label="窗口控制">
            <button type="button" class="pp-btn pp-win-btn" title="最小化" aria-label="最小化窗口" @click="winMinimise"><UiIcon name="window-minimize" :size="14" /></button>
            <button type="button" class="pp-btn pp-win-btn" :title="winMax ? '还原窗口' : '最大化窗口'" :aria-label="winMax ? '还原窗口' : '最大化窗口'" @click="winToggleMax"><UiIcon :name="winMax ? 'window-restore' : 'window-maximize'" :size="14" /></button>
            <button type="button" class="pp-btn pp-win-btn pp-win-close" title="关闭 (Esc)" aria-label="关闭播放器" @click="emit('close')"><UiIcon name="close" :size="14" /></button>
          </div>
          <div v-else class="pp-window-controls" aria-label="窗口控制">
            <button type="button" class="pp-btn pp-win-btn pp-win-close" title="关闭 (Esc)" aria-label="关闭播放器" @click="emit('close')"><UiIcon name="close" :size="14" /></button>
          </div>
        </div>
      </header>

      <section v-if="!loading && !error" class="pp-bottom" :class="{ hidden: isFullscreen && !showControls && playing }" @focusin="onMouseMove" @focusout="scheduleHideControls" aria-label="播放控制">
        <div
          ref="progressEl"
          class="pp-progress"
          :style="{ '--played': pct + '%', '--buffered': bufPct + '%' }"
          @pointermove="onProgressPointerMove"
          @pointerleave="onProgressPointerLeave"
        >
          <div class="pp-progress-buffer"></div>
          <div class="pp-progress-fill"><span class="pp-progress-thumb"></span></div>
          <div v-if="scrubVisible" class="pp-scrub" :style="{ left: scrubX + '%' }">
            <span>{{ fmtTime(scrubTime) }}</span>
          </div>
          <input type="range" class="pp-progress-input" min="0" :max="duration || 0" step="0.1" :value="position" aria-label="播放进度" @input="onSeekInput" />
        </div>

        <div class="pp-controls">
          <div class="pp-group">
            <button type="button" class="pp-btn pp-skip pp-episode-nav" :disabled="episodeFiles.length <= 1 || episodeIndex <= 0" :title="episodeFiles.length <= 1 || episodeIndex <= 0 ? '没有上一集' : '上一集'" @click="switchEpisode(-1)"><UiIcon name="skip-back" :size="20" /></button>
            <button type="button" class="pp-btn pp-play-main" :title="playing ? '暂停 (空格)' : '播放 (空格)'" @click="togglePlay"><UiIcon :name="playing ? 'pause' : 'play'" :size="24" /></button>
            <button type="button" class="pp-btn pp-skip pp-episode-nav" :disabled="episodeFiles.length <= 1 || episodeIndex < 0 || episodeIndex >= episodeFiles.length - 1" :title="episodeFiles.length <= 1 || episodeIndex < 0 || episodeIndex >= episodeFiles.length - 1 ? '没有下一集' : '下一集'" @click="switchEpisode(1)"><UiIcon name="skip-forward" :size="20" /></button>
            <div class="pp-vol">
              <button type="button" class="pp-btn" :title="muted ? '取消静音 (M)' : '静音 (M)'" @click="toggleMute"><UiIcon :name="muted || volume === 0 ? 'volume-x' : 'volume'" :size="20" /></button>
              <div class="pp-vol-slider">
                <input type="range" min="0" max="200" :value="muted ? 0 : volume" :style="{ '--vol-fill': (muted ? 0 : volume) / 2 + '%' }" aria-label="音量" @input="onVolume" />
                <span class="pp-vol-value">{{ muted ? 0 : volume }}%</span>
              </div>
            </div>
            <span class="pp-time">{{ fmtTime(position) }}<i>/</i>{{ fmtTime(duration) }}</span>
          </div>

          <div class="pp-group pp-right">
            <div class="pp-menu-root">
              <button type="button" class="pp-btn pp-text-btn" :class="{ active: activeMenu === 'speed' }" title="播放速度" @click.stop="toggleMenu('speed')">{{ speed }}x</button>
              <div v-if="activeMenu === 'speed'" class="pp-pop">
                <div class="pp-pop-title">播放速度</div>
                <button v-for="item in speedOptions" :key="item.value" type="button" class="pp-pop-item" :class="{ on: item.value === speed }" @click="onSpeed(item.value); closeMenu()">
                  <span class="pp-pop-check"><UiIcon v-if="item.value === speed" name="check" :size="14" /></span>{{ item.label }}
                </button>
              </div>
            </div>

            <div class="pp-menu-root">
              <button type="button" class="pp-btn" :class="{ active: activeMenu === 'subtitle' || subtitleEnabled || supActive }" title="字幕 (C)" @click.stop="toggleMenu('subtitle')"><UiIcon name="captions" :size="20" /></button>
              <div v-if="activeMenu === 'subtitle'" class="pp-pop">
                <div class="pp-pop-title">字幕</div>
                <button type="button" class="pp-pop-item" :class="{ on: !subtitleEnabled && !supActive }" @click="onSubtitleSelected('off'); closeMenu()">
                  <span class="pp-pop-check"><UiIcon v-if="!subtitleEnabled && !supActive" name="check" :size="14" /></span>关闭
                </button>
                <button v-for="track in subtitleTracks" :key="track.index" type="button" class="pp-pop-item" :class="{ on: subtitleEnabled && currentSubtitle === String(track.index) }" @click="onSubtitleSelected(String(track.index)); closeMenu()">
                  <span class="pp-pop-check"><UiIcon v-if="subtitleEnabled && currentSubtitle === String(track.index)" name="check" :size="14" /></span><span class="pp-pop-item-label">{{ track.label }}</span>
                </button>
                <button v-for="(track, index) in supTracks" :key="'sup-' + index" type="button" class="pp-pop-item" :class="{ on: supActive && currentSubtitle === 'sup:' + index }" @click="selectSup(index); closeMenu()">
                  <span class="pp-pop-check"><UiIcon v-if="supActive && currentSubtitle === 'sup:' + index" name="check" :size="14" /></span><span class="pp-pop-item-label">{{ track.label }}</span><span class="pp-pop-tag">SUP</span>
                </button>
                <button v-for="(track, index) in assTracks" :key="'ass-' + index" type="button" class="pp-pop-item" :class="{ on: assActive && currentSubtitle === 'ass:' + index }" @click="selectAss(index); closeMenu()">
                  <span class="pp-pop-check"><UiIcon v-if="assActive && currentSubtitle === 'ass:' + index" name="check" :size="14" /></span><span class="pp-pop-item-label">{{ track.label }}</span><span class="pp-pop-tag">ASS</span>
                </button>
                <div v-if="!subtitleTracks.length && !supTracks.length && !assTracks.length" class="pp-pop-empty">无可用字幕</div>
                <div class="pp-pop-divider"></div>
                <button type="button" class="pp-pop-item" @click="localSubInput && localSubInput.click(); closeMenu()">
                  <span class="pp-pop-check"></span><UiIcon name="upload" :size="14" />加载本地字幕…
                </button>
                <template v-if="subtitleTracks.length">
                  <div class="pp-pop-divider"></div>
                  <div class="pp-pop-tools">
                    <span class="pp-pop-tools-label">大小</span>
                    <button type="button" class="pp-tool" @click="setSubtitleScale(subtitleScale - .1)">−</button>
                    <span class="pp-tool-value">{{ Math.round(subtitleScale * 100) }}%</span>
                    <button type="button" class="pp-tool" @click="setSubtitleScale(subtitleScale + .1)">＋</button>
                    <span class="pp-pop-tools-label">位置</span>
                    <button type="button" class="pp-tool pp-tool-wide" @click="setSubtitlePosition(subtitlePosition === 'top' ? 'bottom' : 'top')">{{ subtitlePosition === 'top' ? '顶部' : '底部' }}</button>
                  </div>
                </template>
              </div>
            </div>

            <div class="pp-menu-root">
              <button type="button" class="pp-btn" :class="{ active: activeMenu === 'episodes' }" title="播放列表" @click.stop="toggleMenu('episodes')"><UiIcon name="list" :size="20" /></button>
              <div v-if="activeMenu === 'episodes'" class="pp-pop pp-pop-list">
                <div class="pp-pop-title">播放列表 <span class="pp-pop-count">{{ episodeIndex + 1 }}/{{ episodeFiles.length }}</span></div>
                <button v-for="(episode, index) in episodeFiles" :key="episode.file_id" type="button" class="pp-pop-item pp-episode" :class="{ on: episode.file_id === file.file_id }" :title="episode.name" @click="selectEpisode(episode)">
                  <span class="pp-episode-no">{{ index + 1 }}</span>
                  <span class="pp-episode-name">{{ episode.name }}</span>
                  <UiIcon v-if="episode.file_id === file.file_id" name="play" :size="11" />
                </button>
              </div>
            </div>

            <div v-if="qualities.length" class="pp-menu-root">
              <button type="button" class="pp-btn pp-text-btn" :class="{ active: activeMenu === 'quality' }" title="清晰度" @click.stop="toggleMenu('quality')">{{ currentQualityLabel }}</button>
              <div v-if="activeMenu === 'quality'" class="pp-pop">
                <div class="pp-pop-title">清晰度</div>
                <button v-for="quality in qualities" :key="quality.value" type="button" class="pp-pop-item" :class="{ on: quality.value === currentQuality }" @click="switchQuality(quality.value); closeMenu()">
                  <span class="pp-pop-check"><UiIcon v-if="quality.value === currentQuality" name="check" :size="14" /></span>{{ quality.label }}
                </button>
              </div>
            </div>

            <div class="pp-menu-root">
              <button type="button" class="pp-btn" :class="{ active: activeMenu === 'more' }" title="更多播放选项" :aria-expanded="activeMenu === 'more'" @click.stop="toggleMenu('more')"><UiIcon name="more-horizontal" :size="20" /></button>
              <div v-if="activeMenu === 'more'" class="pp-pop">
                <div class="pp-pop-title">播放选项</div>
                <button type="button" class="pp-pop-item" @click="screenshot(); closeMenu()"><UiIcon name="camera" :size="18" />保存截图<span class="pp-shortcut">S</span></button>
                <button type="button" class="pp-pop-item" :class="{ on: pipActive }" @click="togglePip(); closeMenu()"><UiIcon name="picture-in-picture" :size="18" />画中画<span class="pp-shortcut">P</span></button>
                <button type="button" class="pp-pop-item" :class="{ on: looping }" @click="toggleLoop()"><UiIcon name="repeat" :size="18" />循环播放<span class="pp-shortcut">{{ looping ? '开启' : '关闭' }}</span></button>
                <div class="pp-pop-divider"></div>
                <button type="button" class="pp-pop-item" @click="seek(-seekStep); closeMenu()"><UiIcon name="rewind" :size="18" />快退 {{ seekStep }} 秒<span class="pp-shortcut">←</span></button>
                <button type="button" class="pp-pop-item" @click="seek(seekStep); closeMenu()"><UiIcon name="fast-forward" :size="18" />快进 {{ seekStep }} 秒<span class="pp-shortcut">→</span></button>
              </div>
            </div>
            <button type="button" class="pp-btn" :title="isFullscreen ? '退出全屏 (F)' : '全屏 (F)'" @click="toggleFullscreen"><UiIcon :name="isFullscreen ? 'minimize' : 'maximize'" :size="20" /></button>
          </div>
        </div>
      </section>
      <input ref="localSubInput" type="file" accept=".srt,.vtt,.ass,.ssa,.sup" class="pp-hidden-input" @change="onLocalSubtitlePicked" />
    </div>
  </teleport>
</template>

<style scoped>
/* 与主界面共用表面、文字与强调色；视频画布保持中性黑。 */
.player-panel {
  --pp-white: var(--text-primary);
  --pp-dim: var(--text-secondary);
  --pp-glass: var(--bg-elevated);
  --pp-hover: var(--bg-hover);
  --pp-control-active: var(--color-primary);
  --subtitle-scale: 1;
  position: fixed; z-index: 520; inset: 0;
  overflow: hidden; isolation: isolate;
  color: var(--text-primary); background: var(--bg-surface);
  font-family: inherit; user-select: none;
}
.pp-stage { position: absolute; inset: 48px 0 92px; display: flex; align-items: center; justify-content: center; background: #08090c; overflow: hidden; }
.pp-video { display: block; width: 100%; height: 100%; object-fit: contain; }
.pp-video::cue { color: #fff; background: rgba(0,0,0,.65); font-size: calc(1em * var(--subtitle-scale)); }
.pp-sup-canvas { position: absolute; z-index: 1; inset: 0; width: 100%; height: 100%; pointer-events: none; }
.pp-hidden-input { display: none; }
.player-panel.cursor-hidden .pp-stage { cursor: none; }
.pp-topbar { position: absolute; z-index: 3; top: 0; left: 0; right: 0; height: 48px; display: flex; align-items: center; gap: 12px; padding: 0 0 0 18px; background: var(--bg-surface); border-bottom: 1px solid var(--border-light); --wails-draggable: drag; }
.pp-media-mark { display: flex; align-items: center; color: var(--color-primary); }
.pp-file-meta { flex: 1; min-width: 0; display: flex; align-items: baseline; gap: 16px; }
.pp-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 600; line-height: 1.5; }
.pp-sub { flex-shrink: 0; color: var(--pp-dim); font-size: 11px; }
.pp-top-actions { display: flex; align-self: stretch; flex-shrink: 0; }
.pp-window-controls { display: flex; align-items: stretch; --wails-draggable: no-drag; }
.pp-btn { display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; width: 36px; height: 36px; padding: 0; border: 0; border-radius: var(--radius-sm); color: var(--text-secondary); background: transparent; font: inherit; cursor: pointer; transition: background 140ms ease, color 140ms ease; --wails-draggable: no-drag; }
.pp-btn:hover:not(:disabled) { background: var(--bg-hover); color: var(--text-primary); }
.pp-btn:disabled { opacity: .3; cursor: default; }
.pp-btn.active { background: var(--listselectbg); color: var(--color-primary); }
.pp-btn:focus-visible, .pp-tool:focus-visible, .pp-pop-item:focus-visible { outline: 2px solid var(--border-focus); outline-offset: -2px; }
.pp-btn.pp-win-btn { width: 46px; height: 100%; border-radius: 0; }
.pp-btn.pp-win-close:hover { background: #c43d4b; color: #fff; }
.pp-text-btn { width: auto; min-width: 42px; padding: 0 10px; font-size: 12px; font-weight: 600; white-space: nowrap; }
.pp-bottom { position: absolute; z-index: 3; bottom: 0; left: 0; right: 0; height: 92px; display: flex; flex-direction: column; justify-content: center; gap: 6px; padding: 8px 24px 12px; background: var(--bg-surface); border-top: 1px solid var(--border-light); --wails-draggable: no-drag; }
.pp-progress { position: relative; height: 18px; flex-shrink: 0; cursor: pointer; }
.pp-progress::before, .pp-progress-buffer, .pp-progress-fill { position: absolute; top: 50%; left: 0; right: 0; height: 3px; transform: translateY(-50%); border-radius: 8px; }
.pp-progress::before { content: ''; background: var(--bg-subtle); }
.pp-progress-buffer { right: auto; width: var(--buffered); background: var(--control-border); }
.pp-progress-fill { right: auto; width: var(--played); background: var(--color-primary); }
.pp-progress-thumb { position: absolute; top: 50%; right: -5px; width: 10px; height: 10px; transform: translateY(-50%); border-radius: 50%; background: var(--color-primary); opacity: 0; transition: opacity 140ms ease; }
.pp-progress:hover .pp-progress-thumb, .pp-progress:focus-within .pp-progress-thumb { opacity: 1; }
.pp-progress:focus-within { outline: 2px solid var(--border-focus); outline-offset: 2px; border-radius: 4px; }
.pp-progress-input { position: absolute; z-index: 2; inset: 0; width: 100%; height: 100%; margin: 0; opacity: 0; cursor: pointer; }
.pp-scrub { position: absolute; bottom: 24px; padding: 6px 10px; transform: translateX(-50%); color: var(--text-primary); background: var(--bg-elevated); border: 1px solid var(--border-light); border-radius: var(--radius-sm); box-shadow: var(--shadow-sm); font-size: 12px; font-variant-numeric: tabular-nums; pointer-events: none; }
.pp-controls, .pp-group { display: flex; align-items: center; min-width: 0; }
.pp-controls { justify-content: space-between; gap: 16px; }
.pp-group { gap: 6px; }
.pp-right { justify-content: flex-end; gap: 4px; }
.pp-play-main { width: 38px; height: 38px; border-radius: 50%; background: var(--color-primary); color: #fff; }
.pp-play-main:hover:not(:disabled) { background: var(--color-primary); color: #fff; filter: brightness(1.08); }
.pp-play-main :deep(svg) { width: 20px; height: 20px; }
.pp-time { margin-left: 8px; color: var(--text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.pp-time i { margin: 0 7px; color: var(--text-tertiary); font-style: normal; }
.pp-vol { display: flex; align-items: center; margin-left: 8px; }
.pp-vol-slider { display: flex; align-items: center; width: 0; overflow: hidden; transition: width 160ms ease; }
.pp-vol:hover .pp-vol-slider, .pp-vol:focus-within .pp-vol-slider { width: 112px; }
.pp-vol-slider input { width: 64px; height: 3px; margin: 0 4px; accent-color: var(--color-primary); cursor: pointer; }
.pp-vol-value { width: 36px; color: var(--text-secondary); font-size: 11px; font-variant-numeric: tabular-nums; }
.pp-state { position: absolute; z-index: 2; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 16px; padding: 32px; color: #d8dce5; font-size: 13px; text-align: center; pointer-events: none; }
.pp-loader-box { display: flex; flex-direction: column; align-items: center; gap: 14px; padding: 24px; }
.pp-loader-text { font-size: 13px; }
.pp-loader-speed { color: #aeb5c4; font-size: 12px; font-variant-numeric: tabular-nums; }
.pp-spinner { width: 28px; height: 28px; border: 2px solid #ffffff24; border-top-color: #fff; border-radius: 50%; animation: pp-spin .8s linear infinite; }
@keyframes pp-spin { to { transform: rotate(360deg); } }
.pp-error-text { max-width: 520px; line-height: 1.7; overflow-wrap: anywhere; }
.pp-error-actions { display: flex; gap: 10px; pointer-events: auto; }
.pp-retry { display: inline-flex; align-items: center; gap: 8px; padding: 8px 14px; border: 1px solid #ffffff38; border-radius: 8px; color: #fff; background: #ffffff0c; cursor: pointer; font: inherit; }
.pp-retry:hover { background: #ffffff20; }
.pp-retry-solid { background: var(--color-primary); border-color: transparent; }
.pp-center { position: absolute; z-index: 2; top: 50%; left: 50%; transform: translate(-50%,-50%); display: flex; align-items: center; justify-content: center; width: 64px; height: 64px; padding: 0; border: 1px solid #ffffff40; border-radius: 50%; color: #fff; background: #12141bcc; cursor: pointer; transition: background 160ms ease; }
.pp-center:hover { background: #252934e8; }
.pp-center :deep(svg) { width: 26px; height: 26px; }
.pp-center-fade-enter-active, .pp-center-fade-leave-active { transition: opacity 160ms ease; }
.pp-center-fade-enter-from, .pp-center-fade-leave-to { opacity: 0; }
.pp-osd { position: absolute; z-index: 4; top: 24px; left: 50%; transform: translateX(-50%); display: flex; align-items: center; gap: 10px; padding: 10px 14px; border: 1px solid #ffffff24; border-radius: 10px; background: #171b24ed; color: #fff; font-size: 12px; pointer-events: none; }
.pp-osd-bar { width: 64px; height: 3px; background: #ffffff30; border-radius: 4px; overflow: hidden; }
.pp-osd-bar i { display: block; height: 100%; background: #fff; }
.pp-osd-enter-active, .pp-osd-leave-active { transition: opacity 160ms ease; }
.pp-osd-enter-from, .pp-osd-leave-to { opacity: 0; }
.pp-menu-root { position: relative; }
.pp-pop { position: absolute; z-index: 20; right: 0; bottom: calc(100% + 14px); min-width: 208px; max-width: min(320px, calc(100vw - 32px)); max-height: min(52vh, 380px); overflow-y: auto; padding: 6px; border: 1px solid var(--border-light); border-radius: var(--radius-md); background: var(--bg-elevated); color: var(--text-primary); box-shadow: var(--shadow-lg); scrollbar-width: thin; animation: pp-pop-in 140ms ease; }
@keyframes pp-pop-in { from { opacity: 0; transform: translateY(4px); } }
.pp-pop-title { display: flex; align-items: center; justify-content: space-between; padding: 8px 10px; font-size: 11px; color: var(--text-tertiary); }
.pp-pop-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 36px; padding: 8px 10px; border: 0; border-radius: var(--radius-sm); background: transparent; color: var(--text-secondary); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.pp-pop-item:hover { background: var(--bg-hover); color: var(--text-primary); }
.pp-pop-item.on { background: var(--listselectbg); color: var(--color-primary); }
.pp-pop-check { display: inline-flex; flex-shrink: 0; width: 16px; }
.pp-pop-item-label, .pp-episode-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pp-pop-tag { font-size: 10px; padding: 2px 4px; background: var(--bg-subtle); border-radius: 4px; }
.pp-pop-divider { height: 1px; margin: 5px 8px; background: var(--border-light); }
.pp-pop-empty { padding: 10px; font-size: 12px; color: var(--text-tertiary); }
.pp-pop-list { width: 300px; }
.pp-episode-no { min-width: 22px; color: var(--text-tertiary); font-size: 11px; font-variant-numeric: tabular-nums; }
.pp-pop-item.on .pp-episode-no { color: inherit; }
.pp-shortcut { margin-left: auto; color: var(--text-tertiary); font-size: 11px; }
.pp-pop-tools { display: flex; align-items: center; gap: 6px; padding: 8px; white-space: nowrap; }
.pp-pop-tools-label, .pp-tool-value { color: var(--text-secondary); font-size: 11px; }
.pp-tool { min-width: 26px; height: 28px; padding: 0 6px; border: 1px solid var(--border-light); border-radius: 6px; background: var(--bg-surface); color: var(--text-primary); font: inherit; font-size: 12px; cursor: pointer; }
.pp-tool:hover { background: var(--bg-hover); }
/* 全屏时保留画布空间，工具栏在鼠标活动或键盘聚焦时显示。 */
.player-panel.fullscreen { --bg-surface: #12151bef; --bg-elevated: #20242e; --bg-hover: #ffffff12; --bg-subtle: #ffffff16; --text-primary: #f3f4f7; --text-secondary: #c5c9d3; --text-tertiary: #9ba2b1; --border-light: #ffffff18; --control-border: #ffffff38; --listselectbg: #ffffff18; }
.fullscreen .pp-stage { inset: 0; }
.fullscreen .pp-topbar, .fullscreen .pp-bottom { transition: opacity 200ms ease; }
.fullscreen .hidden { opacity: 0; pointer-events: none; }
.fullscreen .hidden:focus-within { opacity: 1; pointer-events: auto; }
@media (max-width: 900px) { .pp-sub { display: none; } .pp-vol:hover .pp-vol-slider, .pp-vol:focus-within .pp-vol-slider { width: 76px; } .pp-vol-value { display: none; } }
@media (max-width: 680px) { .pp-bottom { padding-right: 12px; padding-left: 12px; } .pp-group { gap: 2px; } .pp-controls { gap: 4px; } .pp-vol { margin-left: 0; } .pp-time { font-size: 11px; margin-left: 2px; } .pp-text-btn { padding: 0 6px; min-width: 34px; } .pp-episode-nav { display: none; } }
@media (max-width: 480px) { .pp-bottom { height: 130px; } .pp-stage { bottom: 130px; } .pp-controls { flex-wrap: wrap; justify-content: center; gap: 4px; } .pp-right { justify-content: center; width: 100%; } .pp-title { font-size: 12px; } .pp-media-mark { display: none; } .pp-pop { position: fixed; left: 12px; right: 12px; bottom: 128px; max-width: none; width: auto; } }
</style>
