<script setup>
// 高级预览弹窗：
// 1. 图片画廊（缩放/拖拽平移/90度旋转/翻页/胶卷条）
// 2. 文本与代码专业预览/编辑器（大屏自适应/最大化全屏/滚动同步/防折行排布/Markdown精美排版/Ctrl+S云端回传/状态栏）
// 3. 音频播放；PDF 等非白名单格式由文件页提示下载，不会进入此弹窗
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { PreviewURL, PinFileSnapshot, openKindOf, formatBytes, formatTime, saveCloudText, copyText, iconOf, getPlayCursor, savePlayCursor } from '../api'
import { getPrefs } from '../appearance'
import { WindowMinimise, WindowToggleMaximise, WindowIsMaximised } from '../../wailsjs/runtime/runtime'
import Modal from './Modal.vue'
import ConfirmModal from './ConfirmModal.vue'
import UiIcon from './UiIcon.vue'

const props = defineProps({
  account: { type: Object, required: true },
  file: { type: Object, required: true },
  fileList: { type: Array, default: () => [] },
})
const emit = defineEmits(['close', 'toast', 'saved'])

const activeFile = ref(props.file)
const kind = computed(() => openKindOf(activeFile.value))
const isImmersive = computed(() => kind.value === 'image' || kind.value === 'audio')

const url = ref('')
const text = ref('')
const editContent = ref('')
const saving = ref(false)
const loading = ref(true)
const error = ref('')
const winMax = ref(false) // 应用窗口最大化状态
function winMinimise() { try { WindowMinimise() } catch { /* browser preview */ } }
function winToggleMax() {
  try {
    WindowToggleMaximise()
    winMax.value = !winMax.value
    WindowIsMaximised().then((v) => { winMax.value = !!v }).catch(() => {})
  } catch { /* browser preview */ }
}

// ---------- 图片画廊状态 ----------
const imageList = computed(() => {
  const list = props.fileList && props.fileList.length ? props.fileList : [props.file]
  return list.filter((f) => !f.isDir && openKindOf(f) === 'image')
})

const currentImageIdx = computed(() => {
  const id = activeFile.value.file_id
  const idx = imageList.value.findIndex((f) => f.file_id === id)
  return idx >= 0 ? idx : 0
})

// ---------- 图片查看器（沉浸式重写） ----------
// 图层转场模型：切图时旧层冻结变换退出、新层解码完成后才淡入，切换无白屏闪烁
const stageEl = ref(null)
const layers = ref([]) // [{ key, url, file, w, h, leaving, frozen }]
const natural = ref({ w: 0, h: 0 })
const fitScale = ref(1) // 适配视口的基准缩放（不超过原始尺寸）
const zoom = ref(1) // 1 = 适配窗口
const rotation = ref(0)
const pos = ref({ x: 0, y: 0 })
const isDragging = ref(false)
const uiHidden = ref(false) // 光标闲置后隐藏控件
const showFilm = ref(false)
const imgSwitching = ref(false)
let dragStart = { x: 0, y: 0, posX: 0, posY: 0 }
let activeDragCleanup = null
let idleTimer = null
let imgSeq = 0
let stageRO = null

const liveTransform = computed(
  () => `translate(${pos.value.x}px, ${pos.value.y}px) scale(${(fitScale.value * zoom.value).toFixed(4)}) rotate(${rotation.value}deg)`
)

function computeFit() {
  const el = stageEl.value, n = natural.value
  if (!el || !n.w || !n.h) { fitScale.value = 1; return }
  fitScale.value = Math.min(1, (el.clientWidth - 24) / n.w, Math.max(1, el.clientHeight - 100) / n.h)
}

function resetImageTransform() {
  zoom.value = 1
  rotation.value = 0
  pos.value = { x: 0, y: 0 }
}

function stagePoint(e) {
  const r = stageEl.value.getBoundingClientRect()
  return { x: e.clientX - r.left - r.width / 2, y: e.clientY - r.top - (r.height - 76) / 2 }
}

// 缩放（origin 存在且未旋转时锚定光标位置）
function zoomTo(next, origin) {
  const old = zoom.value
  const nz = Math.min(10, Math.max(0.1, Number(next.toFixed(3))))
  if (nz === old) return
  if (origin && rotation.value % 360 === 0) {
    const k = nz / old
    pos.value = { x: origin.x - (origin.x - pos.value.x) * k, y: origin.y - (origin.y - pos.value.y) * k }
  }
  zoom.value = nz
}
function zoomByFactor(f) { zoomTo(zoom.value * f) }
function rotateBy(deg) { rotation.value = (rotation.value + deg + 360) % 360 }

function onWheel(e) {
  if (kind.value !== 'image') return
  e.preventDefault()
  pokeUI()
  zoomTo(zoom.value * (e.deltaY < 0 ? 1.18 : 1 / 1.18), stagePoint(e))
}

// 双击：适配 ↔ 2x（锚定点击点）
function onDblClick(e) {
  if (kind.value !== 'image') return
  if (e.target.closest('.pv-ctl, .pv-edge, .pv-filmstrip')) return
  if (Math.abs(zoom.value - 1) > 0.01 || pos.value.x || pos.value.y) resetImageTransform()
  else zoomTo(2, stagePoint(e))
}

function onPointerDown(e) {
  if (kind.value !== 'image' || e.button !== 0) return
  if (e.target.closest('.pv-ctl, .pv-edge, .pv-filmstrip')) return
  isDragging.value = true
  dragStart = { x: e.clientX, y: e.clientY, posX: pos.value.x, posY: pos.value.y }
  const onMove = (ev) => {
    if (!isDragging.value) return
    pos.value = { x: dragStart.posX + (ev.clientX - dragStart.x), y: dragStart.posY + (ev.clientY - dragStart.y) }
  }
  const onUp = () => {
    isDragging.value = false
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
    activeDragCleanup = null
  }
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
  activeDragCleanup = onUp
}

// 控件自动隐藏（类播放器）：光标活动 2.4s 后隐藏
function pokeUI() {
  uiHidden.value = false
  clearTimeout(idleTimer)
  idleTimer = setTimeout(() => { if (!isDragging.value && !stageEl.value?.querySelector('.pv-ctl:hover, .pv-ctl:focus-within, .pv-filmstrip:hover, .pv-filmstrip:focus-within')) uiHidden.value = true }, 2400)
}

function switchImage(step) {
  const total = imageList.value.length
  if (total <= 1) return
  if (slideshow.value && step !== 0) stopSlideshow()
  pokeUI()
  const nextIdx = (currentImageIdx.value + step + total) % total
  activeFile.value = imageList.value[nextIdx]
}

function selectImage(img) {
  if (activeFile.value.file_id === img.file_id) return
  if (slideshow.value) stopSlideshow()
  activeFile.value = img
}

// 1:1 实际像素（zoom 相对适配基准，1:1 即 fitScale*zoom = 1）
function zoomToOne() {
  const el = stageEl.value
  if (!el) return
  if (Math.abs(fitScale.value * zoom.value - 1) < 0.01) { resetImageTransform(); return }
  resetImageTransform()
  zoomTo(1 / (fitScale.value || 1))
}
const isOneToOne = computed(() => Math.abs(fitScale.value * zoom.value - 1) < 0.01)

function loadImageBitmap(url) {
  return new Promise((resolve, reject) => {
    const im = new Image()
    im.onload = () => resolve(im)
    im.onerror = () => reject(new Error('图片解码失败'))
    im.src = url
  })
}

async function loadImage() {
  const seq = ++imgSeq
  const file = activeFile.value
  imgSwitching.value = true
  error.value = ''
  try {
    await PinFileSnapshot(props.account.user_id, props.account.drive_id, file)
    const previewUrl = await PreviewURL(props.account.user_id, props.account.drive_id, file.file_id)
    const im = await loadImageBitmap(previewUrl) // 等像素就绪再换层，避免白屏
    if (seq !== imgSeq) return
    const frozen = liveTransform.value
    layers.value.forEach((l) => { l.leaving = true; l.frozen = frozen })
    natural.value = { w: im.naturalWidth, h: im.naturalHeight }
    computeFit()
    resetImageTransform()
    layers.value.push({ key: `${file.file_id}-${seq}`, url: previewUrl, file, w: im.naturalWidth, h: im.naturalHeight })
    setTimeout(() => { layers.value = layers.value.filter((l) => !l.leaving) }, 300)
    preloadNeighbors()
  } catch (e) {
    if (seq !== imgSeq) return
    // 首次加载失败才全屏报错；切换失败保留当前图
    if (!layers.value.length) error.value = String(e && e.message ? e.message : e)
  } finally {
    if (seq === imgSeq) { imgSwitching.value = false; loading.value = false }
  }
}

// 预载相邻两张，切换基本零等待
async function preloadNeighbors() {
  const list = imageList.value, idx = currentImageIdx.value
  for (const step of [1, -1]) {
    const f = list[(idx + step + list.length) % list.length]
    if (!f || f.file_id === activeFile.value.file_id) continue
    try {
      await PinFileSnapshot(props.account.user_id, props.account.drive_id, f)
      const u = await PreviewURL(props.account.user_id, props.account.drive_id, f.file_id)
      const im = new Image()
      im.src = u
    } catch { /* 预载失败静默 */ }
  }
}

// ---------- 音频播放器（沉浸式重写） ----------
const audioList = computed(() => {
  const list = props.fileList && props.fileList.length ? props.fileList : [props.file]
  return list.filter((f) => !f.isDir && openKindOf(f) === 'audio')
})
const audioIdx = computed(() => audioList.value.findIndex((f) => f.file_id === activeFile.value.file_id))
const audioEl = ref(null)
const audioPlaying = ref(false)
const audioPos = ref(0)
const audioDur = ref(0)
const audioBuffered = ref(0)
const audioVolume = ref(Math.min(200, Math.max(0, getPrefs().defaultVolume ?? 100)))
const audioMuted = ref(false)
const audioSpeed = ref(getPrefs().defaultSpeed || 1)
const audioLoop = ref(false)
const audioMenu = ref('') // 'speed' | 'playlist' | ''
const audioError = ref('')
const AUDIO_SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2]
let audioCtx = null
let audioGain = null
let audioSaveTimer = null
let pendingAudioResume = 0

const audioCover = computed(() => String(activeFile.value?.thumbnail || '').trim())
const audioExt = computed(() => {
  const n = String(activeFile.value?.name || '')
  const i = n.lastIndexOf('.')
  return i > 0 ? n.slice(i + 1).toUpperCase() : 'AUDIO'
})
const audioPct = computed(() => (audioDur.value ? (audioPos.value / audioDur.value) * 100 : 0))
const audioBufPct = computed(() => (audioDur.value ? Math.min(100, (audioBuffered.value / audioDur.value) * 100) : 0))

function fmtClock(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = String(s % 60).padStart(2, '0')
  return (h > 0 ? h + ':' + String(m).padStart(2, '0') : String(m)) + ':' + sec
}

// 音量 0–200%：100% 以内走原生 volume，超过后接 WebAudio 增益链（对齐 PlayerPanel）
// 注意：切曲时 <audio> 元素会被重建，MediaElementSource 必须针对新元素重建，
// 否则增益链仍挂在旧元素上，>100% 音量会静默失效。
let audioSourceEl = null
function ensureAudioGain() {
  const el = audioEl.value
  if (!el) return
  try {
    const Ctx = window.AudioContext || window.webkitAudioContext
    if (!Ctx) return
    audioCtx = audioCtx || new Ctx()
    if (audioCtx.state === 'suspended') audioCtx.resume().catch(() => {})
    if (!audioGain) {
      audioGain = audioCtx.createGain()
      audioGain.connect(audioCtx.destination)
    }
    if (audioSourceEl !== el) {
      const source = audioCtx.createMediaElementSource(el)
      source.connect(audioGain)
      audioSourceEl = el
    }
  } catch {
    audioGain = null
    audioSourceEl = null
  }
}

function applyAudioVolume() {
  const el = audioEl.value
  if (!el) return
  const level = audioVolume.value
  if (level > 100) ensureAudioGain()
  if (audioGain) {
    el.volume = 1
    audioGain.gain.value = level / 100
  } else {
    el.volume = Math.min(100, level) / 100
  }
  if (level > 0) el.muted = false
}

function onAudioVolume(e) { audioVolume.value = Number(e.target.value); applyAudioVolume() }

function toggleAudioMute() {
  const el = audioEl.value
  if (el) el.muted = !el.muted
}

function onAudioVolChange() {
  const el = audioEl.value
  if (el) audioMuted.value = !!el.muted
}

function onAudioSpeed(v) {
  audioSpeed.value = Number(v)
  const el = audioEl.value
  if (el) el.playbackRate = audioSpeed.value
  audioMenu.value = ''
}

function toggleAudioPlay() {
  const el = audioEl.value
  if (!el) return
  if (el.paused) el.play().catch(() => {})
  else el.pause()
}

function audioSeekBy(delta) {
  const el = audioEl.value
  if (!el || !audioDur.value) return
  el.currentTime = Math.min(Math.max(0, audioDur.value - 0.2), Math.max(0, el.currentTime + delta))
  audioPos.value = el.currentTime
}

function onAudioSeekInput(e) {
  const el = audioEl.value
  if (!el) return
  el.currentTime = Number(e.target.value)
  audioPos.value = el.currentTime
}

function onAudioTime() {
  const el = audioEl.value
  if (!el) return
  audioPos.value = el.currentTime
  try {
    if (el.buffered.length) audioBuffered.value = el.buffered.end(el.buffered.length - 1)
  } catch { /* ignore */ }
}

function onAudioMeta() {
  const el = audioEl.value
  if (!el) return
  audioDur.value = el.duration || 0
  el.playbackRate = audioSpeed.value
  applyAudioVolume()
  if (pendingAudioResume > 0 && audioDur.value > 5 && pendingAudioResume < audioDur.value - 3) {
    el.currentTime = pendingAudioResume
  }
  pendingAudioResume = 0
  updateAudioMediaSession()
}

function onAudioError() {
  const el = audioEl.value
  if (!el || !el.src || el.src === window.location.href) return
  audioError.value = '音频加载失败，请重试或下载后播放'
}

function switchAudio(step) {
  const total = audioList.value.length
  if (total <= 1) return
  const nextIdx = ((audioIdx.value + step) % total + total) % total
  saveAudioCursor(activeFile.value?.file_id)
  activeFile.value = audioList.value[nextIdx]
}

function selectAudioFile(f) {
  audioMenu.value = ''
  if (!f || f.file_id === activeFile.value.file_id) return
  saveAudioCursor(activeFile.value?.file_id)
  activeFile.value = f
}

function onAudioEnded() {
  audioPlaying.value = false
  if (audioLoop.value) {
    const el = audioEl.value
    if (el) { el.currentTime = 0; el.play().catch(() => {}) }
    return
  }
  if (audioList.value.length > 1) switchAudio(1)
}

function saveAudioCursor(fileId = activeFile.value?.file_id) {
  const el = audioEl.value
  if (!fileId || !el) return
  const pos = el.currentTime || 0
  if (pos > 3 && audioDur.value > 0 && pos < audioDur.value - 3) {
    savePlayCursor(props.account.user_id, props.account.drive_id, fileId, pos).catch(() => {})
  } else if (pos <= 3) {
    savePlayCursor(props.account.user_id, props.account.drive_id, fileId, 0).catch(() => {})
  }
}

function onAudioPlay() {
  audioPlaying.value = true
  audioError.value = ''
  setupAudioMediaSession()
  updateAudioMediaSession()
  if (!audioSaveTimer) audioSaveTimer = setInterval(() => saveAudioCursor(), 5000)
}

function onAudioPause() {
  audioPlaying.value = false
  saveAudioCursor()
  clearInterval(audioSaveTimer)
  audioSaveTimer = null
}

// ---- MediaSession：系统媒体键 / 锁屏信息 ----
function updateAudioMediaSession() {
  if (!('mediaSession' in navigator)) return
  try {
    navigator.mediaSession.metadata = new MediaMetadata({
      title: activeFile.value?.name || '',
      artist: 'Mnemo' + (props.account?.name ? ' · ' + props.account.name : ''),
      album: audioExt.value,
      artwork: audioCover.value ? [{ src: audioCover.value, sizes: '256x256' }] : [],
    })
  } catch { /* ignore */ }
}

function setupAudioMediaSession() {
  if (!('mediaSession' in navigator)) return
  try {
    const ms = navigator.mediaSession
    ms.setActionHandler('play', () => audioEl.value?.play().catch(() => {}))
    ms.setActionHandler('pause', () => audioEl.value?.pause())
    ms.setActionHandler('previoustrack', () => switchAudio(-1))
    ms.setActionHandler('nexttrack', () => switchAudio(1))
    ms.setActionHandler('seekbackward', () => audioSeekBy(-10))
    ms.setActionHandler('seekforward', () => audioSeekBy(10))
  } catch { /* ignore */ }
}

function clearAudioMediaSession() {
  if (!('mediaSession' in navigator)) return
  try {
    navigator.mediaSession.metadata = null
    for (const action of ['play', 'pause', 'previoustrack', 'nexttrack', 'seekbackward', 'seekforward']) {
      navigator.mediaSession.setActionHandler(action, null)
    }
  } catch { /* ignore */ }
}

// ---------- 文本与代码专业预览/编辑状态 ----------
const isMarkdownFile = computed(() => {
  const name = (activeFile.value.name || '').toLowerCase()
  return name.endsWith('.md') || name.endsWith('.markdown')
})

// 文本模式：'preview' (代码/文本预览) | 'markdown' (文档渲染) | 'edit' (在线编辑)
const textMode = ref('preview')
const fontSize = ref(13.5)
const wordWrap = ref(false) // 默认不自动换行，保持宽敞横向滚动，避免长行频繁被折断
const showLineNumbers = ref(true)
const encoding = ref('UTF-8')
const copiedFull = ref(false)
const showSearch = ref(false)
const searchKw = ref('')
const searchInputEl = ref(null)

// 光标位置
const cursorPos = ref({ line: 1, col: 1 })
const gutterEl = ref(null)
const viewEl = ref(null)
const editorEl = ref(null)
const confirmLeaveDialog = ref(false)

const currentText = computed(() => (textMode.value === 'edit' ? editContent.value : text.value))
const textLines = computed(() => currentText.value.split('\n'))
const isModified = computed(() => textMode.value === 'edit' && editContent.value !== text.value)

// 语言标签识别
const langMeta = computed(() => {
  const name = (activeFile.value.name || '').toLowerCase()
  const ext = name.includes('.') ? name.split('.').pop() : ''
  const map = {
    js: 'JavaScript', mjs: 'JavaScript', cjs: 'JavaScript',
    ts: 'TypeScript', tsx: 'TypeScript React', jsx: 'React JSX',
    vue: 'Vue Component',
    json: 'JSON', json5: 'JSON5', jsonc: 'JSON with Comments',
    html: 'HTML', htm: 'HTML',
    css: 'CSS', scss: 'SCSS', sass: 'SASS', less: 'LESS',
    go: 'Go',
    py: 'Python', pyw: 'Python',
    rs: 'Rust',
    java: 'Java', kt: 'Kotlin',
    c: 'C', cpp: 'C++', cc: 'C++', h: 'C/C++ Header', hpp: 'C++ Header',
    cs: 'C#',
    php: 'PHP',
    rb: 'Ruby',
    sh: 'Shell Script', bash: 'Bash Script', zsh: 'Zsh Script', ps1: 'PowerShell', bat: 'Batch', cmd: 'Batch',
    sql: 'SQL Database',
    yaml: 'YAML', yml: 'YAML',
    xml: 'XML', svg: 'SVG Image/XML',
    toml: 'TOML', ini: 'INI Config', conf: 'Config', env: 'Environment',
    md: 'Markdown', markdown: 'Markdown',
    txt: 'Plain Text', log: 'Log File',
  }
  return map[ext] || (ext ? ext.toUpperCase() : 'Plain Text')
})

// 滚动同步（行号与内容区 100% 像素级对齐）
function onContentScroll(e) {
  if (gutterEl.value) {
    gutterEl.value.scrollTop = e.target.scrollTop
  }
}

// 统计字符数/字数
const charCount = computed(() => currentText.value.length)
const lineEnding = computed(() => (currentText.value.includes('\r\n') ? 'CRLF' : 'LF'))

// 搜索匹配拆分
function searchParts(line) {
  const kw = searchKw.value.trim()
  if (!kw) return null
  const str = String(line || '')
  const i = str.toLowerCase().indexOf(kw.toLowerCase())
  if (i < 0) return null
  return [
    { text: str.slice(0, i), hit: false },
    { text: str.slice(i, i + kw.length), hit: true },
    { text: str.slice(i + kw.length), hit: false },
  ]
}

const matchCount = computed(() => {
  const kw = searchKw.value.trim()
  if (!kw) return 0
  let count = 0
  const kwLower = kw.toLowerCase()
  for (const line of textLines.value) {
    let p = 0
    const lower = line.toLowerCase()
    while ((p = lower.indexOf(kwLower, p)) !== -1) {
      count++
      p += kwLower.length
    }
  }
  return count
})

function toggleSearch() {
  showSearch.value = !showSearch.value
  if (showSearch.value) {
    nextTick(() => searchInputEl.value?.focus())
  } else {
    searchKw.value = ''
  }
}

// 复制全部文本
async function copyAllText() {
  const ok = await copyText(currentText.value)
  if (ok) {
    copiedFull.value = true
    emit('toast', '已复制全部文本内容', 'success')
    setTimeout(() => { copiedFull.value = false }, 1800)
  } else {
    emit('toast', '复制失败', 'error')
  }
}

// 编辑器光标与按键事件
function updateCursorPos(e) {
  const el = e.target
  if (!el || typeof el.selectionStart !== 'number') return
  const val = el.value.slice(0, el.selectionStart)
  const lines = val.split('\n')
  cursorPos.value = {
    line: lines.length,
    col: lines[lines.length - 1].length + 1,
  }
}

function onEditorKeyDown(e) {
  if (e.key === 'Tab') {
    e.preventDefault()
    const el = e.target
    const start = el.selectionStart
    const end = el.selectionEnd
    const val = editContent.value
    editContent.value = val.substring(0, start) + '  ' + val.substring(end)
    nextTick(() => {
      el.selectionStart = el.selectionEnd = start + 2
      updateCursorPos(e)
    })
  } else if ((e.ctrlKey || e.metaKey) && (e.key === 's' || e.code === 'KeyS')) {
    e.preventDefault()
    doSaveText()
  } else if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.code === 'KeyF')) {
    e.preventDefault()
    toggleSearch()
  } else {
    nextTick(() => updateCursorPos(e))
  }
}

// 保存修改回传云端
async function doSaveText() {
  if (saving.value || !isModified.value) return
  saving.value = true
  try {
    const parentId = activeFile.value.parent_file_id || 'root'
    await saveCloudText(
      props.account.user_id,
      props.account.drive_id,
      parentId,
      activeFile.value.name,
      editContent.value
    )
    text.value = editContent.value
    emit('toast', '保存成功，已上传到网盘', 'success')
    emit('saved')
  } catch (e) {
    emit('toast', '保存失败: ' + String(e), 'error')
  } finally {
    saving.value = false
  }
}

// 关闭前未保存检查
function handleCloseRequest() {
  if (isModified.value) {
    confirmLeaveDialog.value = true
  } else {
    emit('close')
  }
}

// ---------- 轻量安全 Markdown 解析器 ----------
function escapeHtml(str) {
  return String(str || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function sanitizeUrl(rawUrl) {
  const trimmed = String(rawUrl || '').trim()
  if (/^(https?:\/\/|mailto:|\/|\.\/|#)/i.test(trimmed)) {
    return escapeHtml(trimmed)
  }
  return '#'
}

function renderMarkdown(src) {
  if (!src) return ''

  // 代码块提取 (```lang ... ```)
  const codeBlocks = []
  let md = src.replace(/```([a-zA-Z0-9_-]*)\n([\s\S]*?)```/g, (_, lang, code) => {
    const idx = codeBlocks.length
    const safeLang = escapeHtml(lang || 'code')
    const safeCode = escapeHtml(code.trim())
    codeBlocks.push(
      `<div class="md-code-block"><div class="md-code-header"><span class="md-code-lang">${safeLang}</span></div><pre><code>${safeCode}</code></pre></div>`
    )
    return `<!--CODEBLOCK_${idx}-->`
  })

  // 转义 HTML 字符
  md = escapeHtml(md)

  // 行内代码
  md = md.replace(/`([^`]+)`/g, '<code class="md-inline-code">$1</code>')

  // 标题
  md = md.replace(/^###### (.*$)/gim, '<h6 class="md-h6">$1</h6>')
  md = md.replace(/^##### (.*$)/gim, '<h5 class="md-h5">$1</h5>')
  md = md.replace(/^#### (.*$)/gim, '<h4 class="md-h4">$1</h4>')
  md = md.replace(/^### (.*$)/gim, '<h3 class="md-h3">$1</h3>')
  md = md.replace(/^## (.*$)/gim, '<h2 class="md-h2">$1</h2>')
  md = md.replace(/^# (.*$)/gim, '<h1 class="md-h1">$1</h1>')

  // 分割线
  md = md.replace(/^---$/gim, '<hr class="md-hr" />')

  // 引用块 (转义后为 &gt;)
  md = md.replace(/^&gt;\s?(.*$)/gim, '<blockquote class="md-quote">$1</blockquote>')

  // 格式
  md = md.replace(/\*\*\*(.*?)\*\*\*/g, '<strong><em>$1</em></strong>')
  md = md.replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
  md = md.replace(/\*(.*?)\*/g, '<em>$1</em>')
  md = md.replace(/~~(.*?)~~/g, '<del>$1</del>')

  // 任务列表
  md = md.replace(/^- \[x\] (.*$)/gim, '<li class="md-task-item"><span class="md-task-box checked">✓</span> <span>$1</span></li>')
  md = md.replace(/^- \[ \] (.*$)/gim, '<li class="md-task-item"><span class="md-task-box"></span> <span>$1</span></li>')

  // 列表
  md = md.replace(/^[-\*] (.*$)/gim, '<li class="md-list-item">$1</li>')

  // 链接（严格白名单与 URL 属性清洗）
  md = md.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, label, link) => {
    const href = sanitizeUrl(link)
    return `<a href="${href}" target="_blank" rel="noopener noreferrer" class="md-link">${label}</a>`
  })

  // 段落
  md = md.replace(/\n\n/g, '<div class="md-gap"></div>')
  md = md.replace(/\n/g, '<br />')

  // 还原代码块
  codeBlocks.forEach((block, idx) => {
    md = md.replace(`<!--CODEBLOCK_${idx}-->`, block)
  })

  return md
}

const renderedMarkdown = computed(() => renderMarkdown(text.value))

// ---------- 加载核心逻辑 ----------
let loadSeq = 0
let textController = null
async function loadPreview() {
  const seq = ++loadSeq
  textController?.abort()
  textController = null
  if (kind.value === 'image') return loadImage()
  ++imgSeq
  // 切曲/换图时保留已渲染舞台，避免整屏闪烁；仅首次或空态才展示全屏 loading
  if (!url.value) loading.value = true
  error.value = ''
  url.value = ''
  try {
    if (!['image', 'text', 'audio'].includes(kind.value)) {
      throw new Error(kind.value === 'pdf' ? 'PDF 暂不支持在线预览，请下载后查看' : '此文件格式不支持在线预览，请下载后查看')
    }
    await PinFileSnapshot(
      props.account.user_id,
      props.account.drive_id,
      activeFile.value
    )
    const previewUrl = await PreviewURL(
      props.account.user_id,
      props.account.drive_id,
      activeFile.value.file_id
    )
    if (seq !== loadSeq) return
    if (kind.value === 'audio') {
      pendingAudioResume = await getPlayCursor(props.account.user_id, props.account.drive_id, activeFile.value.file_id).catch(() => 0)
      if (seq !== loadSeq) return
      audioPos.value = 0
      audioDur.value = 0
      audioBuffered.value = 0
    }
    url.value = previewUrl
    if (kind.value === 'text') {
      textController = new AbortController()
      const resp = await fetch(previewUrl, { signal: textController.signal })
      if (!resp.ok) throw new Error(`HTTP ${resp.status} 加载失败`)
      const buf = await resp.arrayBuffer()
      if (seq !== loadSeq) return
      if (buf.byteLength > 4 * 1024 * 1024) throw new Error('文本文件超过 4MB，不支持在线预览，请下载后查看')
      const decoded = decodeText(buf)
      text.value = decoded.text
      encoding.value = decoded.encoding
      editContent.value = text.value
      textMode.value = isMarkdownFile.value ? 'markdown' : 'preview'
    }
  } catch (e) {
    if (seq !== loadSeq) return
    error.value = String(e && e.message ? e.message : e)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

watch(() => props.file, (f) => { if (f) activeFile.value = f })
watch(() => activeFile.value.file_id, loadPreview)

function onKey(e) {
  if (isImmersive.value && (e.ctrlKey || e.metaKey || e.altKey || e.target?.closest?.('input, textarea, select, button, [contenteditable="true"]'))) return
  if (textMode.value === 'edit') return
  if (kind.value === 'image') {
    if (e.key === 'ArrowLeft') switchImage(-1)
    else if (e.key === 'ArrowRight') switchImage(1)
    else if (e.key === '+' || e.key === '=') zoomByFactor(1.25)
    else if (e.key === '-' || e.key === '_') zoomByFactor(1 / 1.25)
    else if (e.key === '0') resetImageTransform()
    else if (e.key === 'r' || e.key === 'R') rotateBy(90)
  } else if (kind.value === 'audio') {
    if (e.code === 'Space') { e.preventDefault(); toggleAudioPlay() }
    else if (e.key === 'ArrowLeft') audioSeekBy(-10)
    else if (e.key === 'ArrowRight') audioSeekBy(10)
    else if (e.key === 'ArrowUp') { e.preventDefault(); audioVolume.value = Math.min(200, audioVolume.value + 5); applyAudioVolume() }
    else if (e.key === 'ArrowDown') { e.preventDefault(); audioVolume.value = Math.max(0, audioVolume.value - 5); applyAudioVolume() }
    else if (e.key === 'm' || e.key === 'M') toggleAudioMute()
    else if (e.key === 'l' || e.key === 'L') audioLoop.value = !audioLoop.value
  } else if (kind.value === 'text') {
    if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.code === 'KeyF')) {
      e.preventDefault()
      toggleSearch()
    }
  }
}

onMounted(() => {
  loadPreview()
  pokeUI()
  try { WindowIsMaximised().then((v) => { winMax.value = !!v }).catch(() => {}) } catch { /* browser preview */ }
  window.addEventListener('keydown', onKey)
  if (typeof ResizeObserver !== 'undefined') stageRO = new ResizeObserver(() => computeFit())
})
watch(stageEl, (el) => {
  stageRO?.disconnect()
  if (el && stageRO) { stageRO.observe(el); nextTick(computeFit) }
})
onBeforeUnmount(() => {
  ++loadSeq
  ++imgSeq
  textController?.abort()
  window.removeEventListener('keydown', onKey)
  clearTimeout(idleTimer)
  stageRO?.disconnect()
  if (activeDragCleanup) activeDragCleanup()
  // 音频播放器清理：保存进度、停表、释放 WebAudio、注销 MediaSession
  if (kind.value === 'audio') {
    saveAudioCursor()
    clearInterval(audioSaveTimer)
    audioSaveTimer = null
    clearAudioMediaSession()
    try { audioCtx?.close() } catch { /* ignore */ }
    audioCtx = null
    audioGain = null
  }
  stopSlideshow()
})

// ---------- 图片幻灯片放映 ----------
const slideshow = ref(false)
let slideshowTimer = null
function toggleSlideshow() {
  if (slideshow.value) { stopSlideshow(); return }
  if (imageList.value.length <= 1) return
  slideshow.value = true
  // 定时器直接推进，不走 switchImage（用户手动导航才会停止放映）
  slideshowTimer = setInterval(() => {
    if (document.hidden) return
    const total = imageList.value.length
    if (total <= 1) { stopSlideshow(); return }
    pokeUI()
    activeFile.value = imageList.value[(currentImageIdx.value + 1) % total]
  }, 3000)
}
function stopSlideshow() {
  slideshow.value = false
  clearInterval(slideshowTimer)
  slideshowTimer = null
}

// 文本编码探测
function decodeText(buf) {
  const u8 = new Uint8Array(buf)
  if (u8.length >= 3 && u8[0] === 0xef && u8[1] === 0xbb && u8[2] === 0xbf) {
    return { text: new TextDecoder('utf-8').decode(u8.subarray(3)), encoding: 'UTF-8 (BOM)' }
  }
  const utf8 = new TextDecoder('utf-8', { fatal: false }).decode(u8)
  let replacements = 0
  for (let i = 0; i < utf8.length; i++) {
    if (utf8.charCodeAt(i) === 0xfffd) replacements++
  }
  if (replacements > utf8.length / 100) {
    try {
      return { text: new TextDecoder('gbk').decode(u8), encoding: 'GBK' }
    } catch {
      // 回退
    }
  }
  return { text: utf8, encoding: 'UTF-8' }
}
</script>

<template>
  <Modal
    :dialog-class="'preview-modal' + (isImmersive ? ' immersive' : '')"
    :hide-head="isImmersive"
    width=""
    @close="handleCloseRequest"
    body-class="preview-body"
  >
    <!-- 自定义高级弹窗头部（图片/音频为沉浸式浮层，不使用实体头栏） -->
    <template #head>
      <div v-if="kind !== 'image' && kind !== 'audio'" class="pv-head-custom">
        <div class="pv-head-icon" :class="'ft-' + iconOf(activeFile)">
          <UiIcon :name="iconOf(activeFile)" :size="20" />
        </div>
        <div class="pv-head-meta">
          <div class="pv-head-title" :title="activeFile.name">
            <span>{{ activeFile.name }}</span>
            <span v-if="isModified" class="pv-head-unsaved" title="有未保存的修改">*</span>
          </div>
          <div class="pv-head-sub">
            <span>{{ formatBytes(activeFile.size) }}</span>
            <span class="pv-head-sep">·</span>
            <span>修改时间: {{ formatTime(activeFile.time) }}</span>
            <template v-if="kind === 'text'">
              <span class="pv-head-sep">·</span>
              <span class="pv-head-pill">{{ langMeta }}</span>
              <span class="pv-head-pill">{{ encoding }}</span>
            </template>
          </div>
        </div>
      </div>
    </template>

    <!-- 顶部悬浮工具条（仅文本） -->
    <div v-if="kind === 'text'" class="pv-toolbar">
      <!-- 文本与代码专业工具 -->
      <template>
        <!-- 模式切换分段按钮 -->
        <div class="pv-mode-seg">
          <button
            v-if="isMarkdownFile"
            class="pv-seg-btn"
            :class="{ active: textMode === 'markdown' }"
            @click="textMode = 'markdown'"
          >
            <UiIcon name="info" :size="12" /> Markdown 渲染
          </button>
          <button
            class="pv-seg-btn"
            :class="{ active: textMode === 'preview' }"
            @click="textMode = 'preview'"
          >
            <UiIcon name="play" :size="12" /> 代码预览
          </button>
          <button
            class="pv-seg-btn"
            :class="{ active: textMode === 'edit' }"
            @click="textMode = 'edit'"
          >
            <UiIcon name="pencil" :size="12" /> 在线编辑
          </button>
        </div>

        <span class="pv-sep"></span>

        <!-- 复制全文 -->
        <button class="tbtn xs" :title="'复制全部文本内容'" @click="copyAllText">
          <UiIcon v-if="copiedFull" name="check" :size="12" class="icon-check-pop" />
          <UiIcon v-else name="copy" :size="12" />
          <span>{{ copiedFull ? '已复制' : '复制全文' }}</span>
        </button>

        <!-- 自动换行 -->
        <button
          v-if="textMode !== 'markdown'"
          class="tbtn xs"
          :class="{ active: wordWrap }"
          :title="wordWrap ? '当前：自动折行（点击切换为单行连续横向滚动）' : '当前：单行连续横向滚动（点击切换为自动折行）'"
          @click="wordWrap = !wordWrap"
        >
          <UiIcon name="list" :size="12" />
          <span>自动换行: {{ wordWrap ? '已开启' : '已关闭' }}</span>
        </button>

        <!-- 字号调节 -->
        <div v-if="textMode !== 'markdown'" class="pv-font-size-group">
          <button class="tbtn xs icon" title="减小字号" :disabled="fontSize <= 11" @click="fontSize = Math.max(11, fontSize - 1)">A-</button>
          <span class="pv-fs-val">{{ fontSize }}px</span>
          <button class="tbtn xs icon" title="加大字号" :disabled="fontSize >= 18" @click="fontSize = Math.min(18, fontSize + 1)">A+</button>
        </div>

        <!-- 查找搜索按钮 -->
        <button
          v-if="textMode !== 'markdown'"
          class="tbtn xs"
          :class="{ active: showSearch }"
          title="文本查找 (Ctrl+F)"
          @click="toggleSearch"
        >
          <UiIcon name="search" :size="12" /> 查找
        </button>

        <!-- 保存按钮 (编辑模式下) -->
        <div v-if="textMode === 'edit'" class="pv-edit-actions">
          <span v-if="isModified" class="pv-modified-hint"><span class="pulse-dot"></span>已修改</span>
          <button class="btn primary sm" :disabled="!isModified || saving" title="保存修改并上传 (Ctrl+S)" @click="doSaveText">
            <span v-if="saving" class="spin spin-on-primary"></span>
            <UiIcon v-else name="check" :size="13" />
            <span>{{ saving ? '保存上传中…' : '保存修改 (Ctrl+S)' }}</span>
          </button>
        </div>
      </template>
    </div>

    <!-- 文本查找浮层 -->
    <div v-if="kind === 'text' && showSearch && textMode !== 'markdown'" class="pv-search-bar">
      <UiIcon name="search" :size="14" style="color:var(--color-primary)" />
      <input
        ref="searchInputEl"
        v-model="searchKw"
        class="pv-search-input"
        placeholder="在文本中查找… (Esc 关闭)"
        @keydown.esc.stop="toggleSearch"
      />
      <span class="pv-search-count">{{ matchCount ? `${matchCount} 个匹配` : (searchKw ? '无匹配' : '') }}</span>
      <button class="btn-circle sm" title="关闭查找" @click="toggleSearch"><UiIcon name="close" :size="12" /></button>
    </div>

    <header v-if="isImmersive" class="pv-topbar">
      <div class="pv-topbar-meta">
        <UiIcon :name="kind === 'image' ? 'image' : 'audio'" :size="20" />
        <span class="pv-topbar-name" :title="activeFile.name">{{ activeFile.name }}</span>
        <span class="pv-topbar-sub">{{ formatBytes(activeFile.size) }}<template v-if="kind === 'image' && natural.w"> · {{ natural.w }} × {{ natural.h }}</template></span>
      </div>
      <div class="pv-topbar-actions" aria-label="窗口控制">
        <button type="button" class="pv-ctl-btn pv-window-btn" title="最小化" aria-label="最小化窗口" @click="winMinimise"><UiIcon name="window-minimize" :size="14" /></button>
        <button type="button" class="pv-ctl-btn pv-window-btn" :title="winMax ? '还原窗口' : '最大化窗口'" :aria-label="winMax ? '还原窗口' : '最大化窗口'" @click="winToggleMax"><UiIcon :name="winMax ? 'window-restore' : 'window-maximize'" :size="14" /></button>
        <button type="button" class="pv-ctl-btn pv-window-btn pv-window-close" title="关闭 (Esc)" aria-label="关闭预览" @click="handleCloseRequest"><UiIcon name="close" :size="14" /></button>
      </div>
    </header>

    <!-- 主展示区 -->
    <div v-if="loading" class="empty pv-center"><span class="spin"></span><span>加载内容中…</span></div>
    <div v-else-if="error" class="empty pv-center">
      <span class="empty-icon"><UiIcon name="warning" :size="32" /></span>
      <span>{{ error }}</span>
      <button class="btn sm" style="margin-top:8px" @click="loadPreview">重试加载</button>
    </div>

    <template v-else>
      <!-- 1. 图片画廊（沉浸式舞台） -->
      <div
        v-if="kind === 'image'"
        ref="stageEl"
        class="pv-stage"
        :class="{ 'ui-hidden': uiHidden, grabbing: isDragging }"
        @wheel="onWheel"
        @pointerdown="onPointerDown"
        @pointermove="pokeUI"
        @dblclick="onDblClick"
      >
        <div
          v-for="layer in layers"
          :key="layer.key"
          class="pv-img-layer"
          :class="{ leaving: layer.leaving }"
        >
          <img
            :src="layer.url"
            :alt="layer.file.name"
            draggable="false"
            :style="{ transform: layer.frozen || liveTransform, width: layer.w + 'px', height: layer.h + 'px' }"
          />
        </div>

        <div v-if="imgSwitching && layers.length" class="pv-stage-busy"><span class="spin"></span></div>

        <template v-if="imageList.length > 1">
          <button class="pv-edge left" title="上一张 (←)" @click.stop="switchImage(-1)"><UiIcon name="chevron-left" :size="18" /></button>
          <button class="pv-edge right" title="下一张 (→)" @click.stop="switchImage(1)"><UiIcon name="chevron-right" :size="18" /></button>
        </template>

        <!-- 胶卷缩略图条（控制条切换） -->
        <div v-if="showFilm && imageList.length > 1" class="pv-filmstrip" @pointerdown.stop @dblclick.stop>
          <button
            type="button"
            v-for="img in imageList"
            :key="img.file_id"
            class="pv-film-thumb"
            :class="{ active: img.file_id === activeFile.file_id }"
            :title="img.name"
            @click="selectImage(img)"
          >
            <img v-if="img.thumbnail" :src="img.thumbnail" alt="" draggable="false" loading="lazy" />
            <UiIcon v-else name="image" :size="16" />
          </button>
        </div>

        <!-- 浮动控制条 -->
        <div class="pv-ctl" @pointerdown.stop @dblclick.stop>
          <button class="pv-ctl-btn" :disabled="imageList.length <= 1" title="上一张 (←)" @click="switchImage(-1)"><UiIcon name="chevron-left" :size="18" /></button>
          <span class="pv-ctl-counter">{{ currentImageIdx + 1 }} / {{ imageList.length }}</span>
          <button class="pv-ctl-btn" :disabled="imageList.length <= 1" title="下一张 (→)" @click="switchImage(1)"><UiIcon name="chevron-right" :size="18" /></button>
          <span class="pv-ctl-sep"></span>
          <button class="pv-ctl-btn pv-ctl-text" title="缩小 (-)" @click="zoomByFactor(1 / 1.25)">−</button>
          <button type="button" class="pv-ctl-zoom" title="点击复原（适配窗口）" @click="resetImageTransform">{{ Math.round(fitScale * zoom * 100) }}%</button>
          <button class="pv-ctl-btn" title="放大 (+)" @click="zoomByFactor(1.25)"><UiIcon name="plus" :size="13" /></button>
          <button class="pv-ctl-btn" title="适配窗口 (0)" @click="resetImageTransform"><UiIcon name="maximize" :size="18" /></button>
          <button class="pv-ctl-btn pv-ctl-text pv-ctl-ratio" :class="{ active: isOneToOne }" title="实际大小 (双击百分比)" @click="zoomToOne">1:1</button>
          <button class="pv-ctl-btn" title="顺时针旋转 90° (R)" @click="rotateBy(90)"><UiIcon name="rotate-right" :size="18" /></button>
          <span class="pv-ctl-sep"></span>
          <button v-if="imageList.length > 1" class="pv-ctl-btn" :class="{ active: slideshow }" :title="slideshow ? '暂停幻灯片' : '幻灯片放映（3 秒/张）'" @click="toggleSlideshow"><UiIcon :name="slideshow ? 'pause' : 'play'" :size="14" /></button>
          <button class="pv-ctl-btn" :class="{ active: showFilm }" :disabled="imageList.length <= 1" title="缩略图" @click="showFilm = !showFilm"><UiIcon name="grid" :size="14" /></button>
        </div>
      </div>

      <!-- 2. 音频播放（沉浸式播放器） -->
      <div v-else-if="kind === 'audio'" class="pv-audio-stage" @pointerdown="audioMenu && (audioMenu = '')">

        <!-- 中部：唱片 + 曲目信息 -->
        <div class="pv-audio-center">
          <div class="pv-disc" :class="{ empty: !audioCover }">
            <img v-if="audioCover" :src="audioCover" alt="" draggable="false" />
            <UiIcon v-else name="audio" :size="46" />
          </div>
          <div class="pv-audio-details">
            <span class="pv-audio-eyebrow">{{ audioPlaying ? '正在播放' : '音频预览' }}</span>
            <div class="pv-audio-title" :title="activeFile.name">{{ activeFile.name }}</div>
            <div class="pv-audio-sub">
              <span>{{ audioExt }}</span><i>·</i><span>{{ formatBytes(activeFile.size) }}</span>
              <template v-if="audioList.length > 1"><i>·</i><span>{{ audioIdx + 1 }} / {{ audioList.length }}</span></template>
            </div>
          </div>
        </div>

        <!-- 底部播放工具栏 -->
        <div class="pv-audio-dock" @pointerdown.stop @dblclick.stop>
          <div class="pv-audio-progress">
            <div class="pv-audio-track">
              <div class="pv-audio-buffer" :style="{ width: audioBufPct + '%' }"></div>
              <div class="pv-audio-fill" :style="{ width: audioPct + '%' }"><span class="pv-audio-thumb"></span></div>
            </div>
            <input type="range" min="0" :max="audioDur || 0" step="0.1" :value="audioPos" aria-label="播放进度" @input="onAudioSeekInput" />
          </div>
          <div class="pv-audio-times">
            <span>{{ fmtClock(audioPos) }}</span>
            <span>{{ fmtClock(audioDur) }}</span>
          </div>
          <div class="pv-audio-controls">
            <div class="pv-audio-group">
              <button v-if="audioList.length > 1" class="pv-abtn" :class="{ active: audioMenu === 'playlist' }" title="播放列表" @click.stop="audioMenu = audioMenu === 'playlist' ? '' : 'playlist'"><UiIcon name="list" :size="17" /></button>
              <button v-if="audioList.length > 1" class="pv-abtn" title="上一曲" @click="switchAudio(-1)"><UiIcon name="skip-back" :size="20" /></button>
              <button class="pv-abtn pv-abtn-main" :title="audioPlaying ? '暂停 (空格)' : '播放 (空格)'" @click="toggleAudioPlay"><UiIcon :name="audioPlaying ? 'pause' : 'play'" :size="22" /></button>
              <button v-if="audioList.length > 1" class="pv-abtn" title="下一曲" @click="switchAudio(1)"><UiIcon name="skip-forward" :size="20" /></button>
              <button class="pv-abtn" :class="{ active: audioLoop }" title="单曲循环 (L)" @click="audioLoop = !audioLoop"><UiIcon name="repeat" :size="18" /></button>
            </div>
            <div class="pv-audio-group pv-audio-right">
              <button class="pv-abtn pv-abtn-text" :class="{ active: audioMenu === 'speed' }" title="播放速度" @click.stop="audioMenu = audioMenu === 'speed' ? '' : 'speed'">{{ audioSpeed }}x</button>
              <div class="pv-audio-vol">
                <button class="pv-abtn" :title="audioMuted ? '取消静音 (M)' : '静音 (M)'" @click="toggleAudioMute"><UiIcon :name="audioMuted || audioVolume === 0 ? 'volume-x' : 'volume'" :size="17" /></button>
                <input type="range" class="pv-audio-vol-range" min="0" max="200" :value="audioVolume" title="音量（最高 200%）" @input="onAudioVolume" />
                <span class="pv-audio-vol-val">{{ audioMuted ? 0 : audioVolume }}%</span>
              </div>
            </div>
          </div>

          <!-- 弹层：倍速 -->
          <div v-if="audioMenu === 'speed'" class="pv-audio-pop" @pointerdown.stop>
            <div class="pv-audio-pop-title">播放速度</div>
            <button v-for="s in AUDIO_SPEEDS" :key="s" class="pv-audio-pop-item" :class="{ on: s === audioSpeed }" @click.stop="onAudioSpeed(s)">
              <span class="pv-audio-pop-check"><UiIcon v-if="s === audioSpeed" name="check" :size="13" /></span>{{ s }}x
            </button>
          </div>
          <!-- 弹层：播放列表 -->
          <div v-if="audioMenu === 'playlist'" class="pv-audio-pop pv-audio-pop-list" @pointerdown.stop>
            <div class="pv-audio-pop-title">播放列表 <span>{{ audioIdx + 1 }}/{{ audioList.length }}</span></div>
            <button v-for="(t, i) in audioList" :key="t.file_id" class="pv-audio-pop-item pv-audio-track-item" :class="{ on: t.file_id === activeFile.file_id }" :title="t.name" @click.stop="selectAudioFile(t)">
              <span class="pv-audio-track-no">{{ i + 1 }}</span>
              <span class="pv-audio-track-name">{{ t.name }}</span>
              <span v-if="t.file_id === activeFile.file_id" class="pv-audio-eq" :class="{ paused: !audioPlaying }"><i></i><i></i><i></i></span>
            </button>
          </div>
        </div>

        <!-- 错误态 -->
        <div v-if="audioError" class="pv-audio-err">
          <UiIcon name="warning" :size="18" />
          <span>{{ audioError }}</span>
          <button class="btn sm" @click="loadPreview">重试</button>
        </div>

        <audio
          v-if="url"
          ref="audioEl"
          :src="url"
          class="pv-audio-el"
          crossorigin="anonymous"
          preload="metadata"
          autoplay
          @play="onAudioPlay"
          @pause="onAudioPause"
          @ended="onAudioEnded"
          @timeupdate="onAudioTime"
          @progress="onAudioTime"
          @loadedmetadata="onAudioMeta"
          @volumechange="onAudioVolChange"
          @error="onAudioError"
        ></audio>
      </div>

      <!-- 4. 文本/代码/Markdown 专业展示区 -->
      <div v-else class="pv-text-container">
        <!-- Markdown 渲染视图 -->
        <div
          v-if="textMode === 'markdown'"
          class="pv-markdown-view"
          v-html="renderedMarkdown"
        ></div>

        <!-- 代码预览与编辑器双轨容器 -->
        <div v-else class="pv-code-box">
          <!-- 智能同步行号槽 -->
          <div v-if="showLineNumbers" ref="gutterEl" class="pv-gutter" :style="{ fontSize: fontSize + 'px' }">
            <div v-for="n in textLines.length" :key="n" class="pv-gutter-line">{{ n }}</div>
          </div>

          <!-- 在线编辑 textarea -->
          <textarea
            v-if="textMode === 'edit'"
            ref="editorEl"
            v-model="editContent"
            class="pv-editor-textarea"
            :class="{ wrap: wordWrap }"
            :style="{ fontSize: fontSize + 'px' }"
            spellcheck="false"
            @scroll="onContentScroll"
            @click="updateCursorPos"
            @keyup="updateCursorPos"
            @keydown="onEditorKeyDown"
          ></textarea>

          <!-- 查看模式 pre 渲染行 -->
          <div
            v-else
            ref="viewEl"
            class="pv-code-view"
            :class="{ wrap: wordWrap }"
            :style="{ fontSize: fontSize + 'px' }"
            @scroll="onContentScroll"
          >
            <div
              v-for="(line, idx) in textLines"
              :key="idx"
              class="pv-code-line"
            >
              <template v-if="searchKw && searchParts(line)">
                <template v-for="(part, pIdx) in searchParts(line)" :key="pIdx">
                  <mark v-if="part.hit" class="pv-search-hit">{{ part.text }}</mark>
                  <template v-else>{{ part.text }}</template>
                </template>
              </template>
              <template v-else>{{ line || ' ' }}</template>
            </div>
          </div>
        </div>

        <!-- 文本视图底部状态栏 (类似 VS Code / Sublime 的专业质感) -->
        <footer class="pv-statusbar">
          <div class="pv-sb-section">
            <span class="pv-sb-item"><UiIcon name="list" :size="12" />{{ textLines.length }} 行</span>
            <span class="pv-sb-item">{{ charCount }} 字符</span>
            <span class="pv-sb-item">{{ formatBytes(activeFile.size) }}</span>
            <span v-if="textMode === 'edit'" class="pv-sb-item pv-sb-pos">Ln {{ cursorPos.line }}, Col {{ cursorPos.col }}</span>
          </div>
          <div class="pv-sb-spacer"></div>
          <div class="pv-sb-section">
            <span class="pv-sb-item">{{ encoding }}</span>
            <span class="pv-sb-item">{{ lineEnding }}</span>
            <span class="pv-sb-item">{{ wordWrap ? '自动换行' : '不换行' }}</span>
            <span class="pv-sb-item pv-sb-lang">{{ langMeta }}</span>
          </div>
        </footer>
      </div>
    </template>

    <!-- 未保存修改确认弹窗 -->
    <ConfirmModal
      v-if="confirmLeaveDialog"
      title="存在未保存的修改"
      message="文本内容已发生变更，未保存离开将丢失修改。确定要关闭吗？"
      ok-text="放弃修改并关闭"
      :danger="true"
      @ok="confirmLeaveDialog = false; emit('close')"
      @cancel="confirmLeaveDialog = false"
    />
  </Modal>
</template>

<style scoped>
/* 媒体预览与应用共享主题；窗口控制不受内容加载和隐藏计时影响。 */
:global(.modal.preview-modal.immersive) {
  --pv-media-surface: var(--bg-surface);
  --pv-media-surface-hover: var(--bg-hover);
  --pv-media-fg: var(--text-primary);
  --pv-media-muted: var(--text-secondary);
  --pv-media-border: var(--border-light);
  --pv-media-shadow: var(--shadow-md);
  --pv-media-dock: var(--bg-elevated);
  --pv-media-dock-border: var(--border-light);
  --pv-media-active: var(--color-primary);
  --pv-media-active-fg: #fff;
  --pv-audio-base: var(--bg-base);
  --pv-audio-fg: var(--text-primary);
  --pv-audio-muted: var(--text-secondary);
  position: fixed; inset: 0; width: 100%; height: 100%; max-width: none; max-height: none;
  margin: 0; border: 0; border-radius: 0; overflow: hidden; background: var(--bg-base);
}
:global(.modal.preview-modal.immersive .preview-body) { border-radius: 0; background: var(--bg-base); }
/* 弹窗头部自适应高级排布 */
.pv-head-custom {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1;
}
.pv-head-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border-radius: var(--radius-sm);
  background: var(--bg-subtle);
  flex-shrink: 0;
}
.pv-head-meta {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}
.pv-head-title {
  font-size: 15px;
  font-weight: 700;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  display: flex;
  align-items: center;
  gap: 4px;
}
.pv-head-unsaved {
  color: var(--color-warning);
  font-weight: 800;
  font-size: 16px;
}
.pv-head-sub {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-tertiary);
  white-space: nowrap;
}
.pv-head-sep { opacity: 0.6; }
.pv-head-pill {
  padding: 0 6px;
  border-radius: 4px;
  background: var(--bg-subtle);
  color: var(--text-secondary);
  font-size: 11px;
}

.pv-toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 46px;
  padding: 7px 14px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-light);
  flex-shrink: 0;
  flex-wrap: nowrap;
  overflow-x: auto;
  scrollbar-width: thin;
}
.pv-toolbar > * { flex: 0 0 auto; }
.pv-sep { width: 1px; height: 18px; background: var(--border-light); margin: 0 4px; }
.pv-center { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; }
.pv-edit-actions { display: flex; align-items: center; gap: 8px; margin-left: auto; }

/* 模式分段选择器 */
.pv-mode-seg {
  display: inline-flex;
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
  padding: 2.5px;
  gap: 3px;
}
.pv-seg-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 11px;
  font-size: 12.5px;
  color: var(--text-secondary);
  border: none;
  background: transparent;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: all var(--motion-fast) var(--motion-ease);
}
.pv-seg-btn:hover { color: var(--text-primary); }
.pv-seg-btn.active {
  background: var(--bg-surface);
  color: var(--color-primary);
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}

.pv-font-size-group {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
  padding: 1px 4px;
}
.pv-fs-val { font-size: 11.5px; font-variant-numeric: tabular-nums; color: var(--text-secondary); padding: 0 3px; }

.pv-modified-hint {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--color-warning);
  font-weight: 500;
}
.pulse-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-warning);
  animation: pulse 1.4s ease infinite;
}

/* 文本查找栏 */
.pv-search-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 18px;
  background: var(--bg-elevated);
  border-bottom: 1px solid var(--border-light);
  animation: modal-fade-enter 160ms var(--motion-ease);
}
.pv-search-input {
  flex: 1;
  max-width: 360px;
  height: 28px;
  border: 1px solid var(--control-border);
  border-radius: var(--radius-sm);
  padding: 0 10px;
  font-size: 13px;
  background: var(--control-bg);
  color: var(--text-primary);
  outline: none;
}
.pv-search-input:focus { border-color: var(--border-focus); box-shadow: var(--ring-focus); }
.pv-search-count { font-size: 12.5px; color: var(--text-tertiary); }
.pv-search-hit {
  background: color-mix(in srgb, var(--color-warning) 50%, transparent);
  color: inherit;
  border-radius: 2px;
  padding: 0 1px;
}

/* 文本与代码主区容器 */
.pv-text-container {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 0;
  padding: 12px 14px 14px;
  background: var(--bg-base);
}

.pv-code-box {
  flex: 1;
  min-height: 0;
  display: flex;
  background: var(--bg-surface);
  font-family: 'JetBrains Mono', 'Fira Code', 'Cascadia Code', 'SF Mono', Consolas, monospace;
  line-height: 24px;
  overflow: hidden;
  position: relative;
  border: 1px solid var(--border-light);
  border-bottom: 0;
  border-radius: var(--radius-sm) var(--radius-sm) 0 0;
}

/* 行号栏 */
.pv-gutter {
  width: 56px;
  flex-shrink: 0;
  background: var(--bg-subtle);
  border-right: 1px solid var(--border-light);
  padding: 10px 0;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  padding-right: 12px;
  color: var(--text-tertiary);
  user-select: none;
  overflow: hidden;
  font-variant-numeric: tabular-nums;
}
.pv-gutter-line {
  height: 24px;
  line-height: 24px;
}

/* 查看与编辑内容区 */
.pv-code-view, .pv-editor-textarea {
  flex: 1;
  min-width: 0;
  height: 100%;
  padding: 10px 16px;
  margin: 0;
  border: none;
  background: transparent;
  color: var(--text-primary);
  font-family: inherit;
  line-height: 24px;
  outline: none;
  resize: none;
  overflow: auto;
  scrollbar-gutter: stable;
  user-select: text;
  white-space: pre;
  tab-size: 2;
}
.pv-code-view.wrap, .pv-editor-textarea.wrap {
  white-space: pre-wrap;
  word-break: break-all;
}
.pv-code-line {
  height: 24px;
  line-height: 24px;
}
.pv-code-view.wrap .pv-code-line {
  height: auto;
  min-height: 24px;
}

/* Markdown 文档渲染视图 */
.pv-markdown-view {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 24px 42px 48px;
  background: var(--bg-surface);
  color: var(--text-primary);
  font-size: 15px;
  line-height: 1.8;
  user-select: text;
  max-width: 920px;
  margin: 0 auto;
  width: 100%;
  border: 1px solid var(--border-light);
  border-radius: var(--radius-sm);
}
:deep(.md-h1) { font-size: 26px; font-weight: 750; margin: 18px 0 12px; border-bottom: 1px solid var(--border-light); padding-bottom: 8px; color: var(--text-primary); }
:deep(.md-h2) { font-size: 21px; font-weight: 700; margin: 20px 0 10px; border-bottom: 1px solid var(--border-lighter); padding-bottom: 6px; color: var(--text-primary); }
:deep(.md-h3) { font-size: 18px; font-weight: 650; margin: 16px 0 8px; color: var(--text-primary); }
:deep(.md-h4) { font-size: 16px; font-weight: 650; margin: 14px 0 6px; color: var(--text-primary); }
:deep(.md-h5), :deep(.md-h6) { font-size: 14.5px; font-weight: 600; margin: 12px 0 4px; color: var(--text-secondary); }
:deep(.md-inline-code) {
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 13.5px;
  font-family: 'JetBrains Mono', 'Cascadia Code', monospace;
  background: var(--bg-subtle);
  color: var(--color-primary);
  border: 1px solid var(--border-lighter);
}
:deep(.md-code-block) {
  margin: 14px 0;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-light);
  overflow: hidden;
  background: var(--bg-base);
}
:deep(.md-code-header) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 5px 14px;
  background: var(--bg-subtle);
  border-bottom: 1px solid var(--border-lighter);
  font-size: 12px;
  color: var(--text-tertiary);
  font-weight: 600;
  text-transform: uppercase;
}
:deep(.md-code-block pre) {
  padding: 14px 16px;
  margin: 0;
  overflow-x: auto;
  font-family: 'JetBrains Mono', 'Cascadia Code', monospace;
  font-size: 13.5px;
  line-height: 1.65;
}
:deep(.md-quote) {
  border-left: 3.5px solid var(--color-primary);
  margin: 12px 0;
  padding: 6px 16px;
  color: var(--text-secondary);
  background: color-mix(in srgb, var(--color-primary) 6%, transparent);
  border-radius: 0 var(--radius-xs) var(--radius-xs) 0;
}
:deep(.md-link) { color: var(--color-primary); text-decoration: none; font-weight: 500; }
:deep(.md-link:hover) { text-decoration: underline; }
:deep(.md-hr) { border: none; border-top: 1px solid var(--border-light); margin: 24px 0; }
:deep(.md-list-item) { margin-left: 22px; list-style: disc; margin-bottom: 5px; }
:deep(.md-task-item) { list-style: none; display: flex; align-items: center; gap: 8px; margin-bottom: 5px; }
:deep(.md-task-box) { width: 15px; height: 15px; border: 1.5px solid var(--control-border); border-radius: 3px; display: inline-flex; align-items: center; justify-content: center; font-size: 11px; }
:deep(.md-task-box.checked) { background: var(--color-primary); border-color: var(--color-primary); color: #fff; }
:deep(.md-gap) { height: 12px; }

/* 底部状态栏 (Status bar) */
.pv-statusbar {
  display: flex;
  align-items: center;
  height: 28px;
  padding: 0 14px;
  background: var(--bg-subtle);
  border: 1px solid var(--border-light);
  border-top: 0;
  border-radius: 0 0 var(--radius-sm) var(--radius-sm);
  font-size: 12px;
  color: var(--text-tertiary);
  user-select: none;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
.pv-sb-section { display: flex; align-items: center; gap: 14px; }
.pv-sb-item { display: inline-flex; align-items: center; gap: 4px; }
.pv-sb-pos { color: var(--text-secondary); font-weight: 500; }
.pv-sb-spacer { flex: 1; }
.pv-sb-lang { color: var(--color-primary); font-weight: 600; }

@media (max-width: 720px) {
  .pv-head-sub { overflow: hidden; text-overflow: ellipsis; }
  .pv-toolbar { padding: 6px 10px; }
  .pv-search-bar { padding: 7px 10px; }
  .pv-text-container { padding: 8px; }
  .pv-code-view, .pv-editor-textarea { padding: 10px 12px; }
  .pv-markdown-view { padding: 20px 22px 36px; }
  .pv-statusbar { padding: 0 10px; }
  .pv-sb-section { gap: 8px; }
}

/* 图片画布与变换层 */
.pv-stage {
  flex: 1; min-height: 0; position: relative; overflow: hidden;
  background: #050507;
  user-select: none; touch-action: none; cursor: grab;
}
.pv-stage.grabbing { cursor: grabbing; }
.pv-img-layer {
  position: absolute; inset: 0; z-index: 1;
  display: flex; align-items: center; justify-content: center;
  pointer-events: none;
}
.pv-img-layer:not(.leaving) { animation: pv-layer-in 260ms var(--motion-ease) both; }
.pv-img-layer.leaving { animation: pv-layer-out 240ms ease both; }
@keyframes pv-layer-in { from { opacity: 0; transform: scale(1.015); } }
@keyframes pv-layer-out { to { opacity: 0; transform: scale(.99); } }
.pv-img-layer img {
  display: block; max-width: none; max-height: none;
  transform-origin: center center;
  transition: transform 200ms var(--motion-ease);
  box-shadow: 0 10px 44px rgba(0, 0, 0, .55);
  border-radius: 2px;
}
.pv-stage.grabbing .pv-img-layer img { transition: none; }
.pv-stage-busy { position: absolute; z-index: 10; top: 12px; right: 14px; color: rgba(255,255,255,.7); }

/* 媒体标题栏 */
.pv-topbar {
  position: absolute; top: 0; left: 0; right: 0; z-index: 8;
  display: flex; align-items: flex-start; justify-content: space-between; gap: 12px;
  padding: 10px 12px 34px;
  background: linear-gradient(rgba(0, 0, 0, .55), transparent);
  transition: opacity .25s ease, transform .25s ease;
  --wails-draggable: drag;
}
.pv-topbar-meta { display: flex; align-items: center; gap: 8px; min-width: 0; color: rgba(255, 255, 255, .92); padding-top: 2px; }
.pv-topbar-name {
  font-size: 13.5px; font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  text-shadow: 0 1px 4px rgba(0, 0, 0, .7);
}
.pv-topbar-sub { font-size: 11.5px; color: rgba(255, 255, 255, .68); flex: none; }
.pv-topbar-actions {
  display: flex; gap: 0; flex: none; overflow: hidden;
  border: 1px solid var(--pv-media-border); border-radius: 8px;
  background: var(--pv-media-surface); box-shadow: var(--pv-media-shadow);
  --wails-draggable: no-drag;
}

/* 左右翻页箭头 */
.pv-edge {
  position: absolute; z-index: 6; top: 50%; transform: translateY(-50%);
  width: 40px; height: 64px;
  display: flex; align-items: center; justify-content: center;
  color: var(--pv-media-fg);
  background: var(--pv-media-surface);
  backdrop-filter: blur(12px); -webkit-backdrop-filter: blur(12px);
  border: 1px solid var(--pv-media-border); border-radius: 10px;
  box-shadow: var(--pv-media-shadow);
  cursor: pointer; opacity: .9;
  transition: opacity .2s ease, background .18s ease, transform .18s var(--motion-spring);
}
.pv-edge.left { left: 14px; }
.pv-edge.right { right: 14px; }
.pv-edge:hover { opacity: 1; background: var(--pv-media-surface-hover); }
.pv-edge:active { transform: translateY(-50%) scale(.93); }

/* 底部控制条：渐变遮罩条（无实体药丸），控件直接浮在渐变上 */
.pv-ctl {
  position: absolute; z-index: 9; left: 50%; bottom: 14px;
  display: flex; align-items: center; justify-content: center; gap: 5px;
  width: max-content; max-width: calc(100% - 28px); overflow-x: auto;
  padding: 6px 8px;
  border: 1px solid var(--pv-media-dock-border); border-radius: 13px;
  color: var(--pv-media-fg); background: var(--pv-media-dock);
  backdrop-filter: blur(20px) saturate(1.15); -webkit-backdrop-filter: blur(20px) saturate(1.15);
  box-shadow: var(--pv-media-shadow);
  transform: translateX(-50%);
  transition: opacity .25s ease, transform .25s ease;
  scrollbar-width: none;
  --wails-draggable: no-drag;
}
.pv-ctl::-webkit-scrollbar { display: none; }
.pv-ctl-btn {
  width: 34px; height: 34px; flex: none;
  display: flex; align-items: center; justify-content: center;
  margin: 0; padding: 0;
  border: 1px solid transparent; border-radius: 9px;
  color: var(--pv-media-fg); background: transparent; cursor: pointer;
  transition: background .16s ease, border-color .16s ease, transform .18s var(--motion-spring), color .16s;
}
.pv-ctl-btn:hover:not(:disabled) { background: var(--pv-media-surface-hover); border-color: var(--pv-media-border); }
.pv-ctl-btn:active:not(:disabled) { transform: scale(.88); }
.pv-ctl-btn:disabled { opacity: .38; cursor: not-allowed; }
.pv-ctl-btn:focus-visible, .pv-edge:focus-visible, .pv-ctl-zoom:focus-visible { outline: 2px solid var(--pv-media-active); outline-offset: 2px; }
.pv-ctl-btn.active { color: var(--pv-media-active-fg); background: var(--pv-media-active); border-color: transparent; }
.pv-window-btn { width: 40px; height: 34px; border: 0; border-left: 1px solid var(--pv-media-border); border-radius: 0; }
.pv-window-btn:first-child { border-left: 0; }
.pv-window-btn:hover:not(:disabled) { color: var(--pv-media-fg); background: var(--pv-media-surface-hover); border-color: var(--pv-media-border); }
.pv-window-close:hover:not(:disabled) { color: #fff; background: #c43d4b; border-color: #c43d4b; }
.pv-ctl-text { font-size: 17px; line-height: 1; padding-bottom: 2px; }
.pv-ctl-ratio { font-size: 11px; font-weight: 700; letter-spacing: .04em; width: auto; min-width: 34px; padding: 0 7px; }
.pv-ctl-counter, .pv-ctl-zoom { font-size: 12px; color: var(--pv-media-fg); font-variant-numeric: tabular-nums; white-space: nowrap; }
.pv-ctl-zoom { min-width: 44px; min-height: 28px; margin: 0; padding: 3px 5px; border: 1px solid transparent; border-radius: 7px; background: transparent; cursor: pointer; text-align: center; }
.pv-ctl-zoom:hover { background: var(--pv-media-surface-hover); border-color: var(--pv-media-border); }
.pv-ctl-sep { width: 1px; height: 18px; background: var(--pv-media-dock-border); flex: none; }

/* 胶卷缩略图条（默认隐藏，控制条切换） */
.pv-filmstrip {
  position: absolute; z-index: 8; left: 50%; bottom: 66px; transform: translateX(-50%);
  display: flex; gap: 6px; max-width: min(78%, 560px); overflow-x: auto;
  padding: 7px; border-radius: 12px;
  background: var(--pv-media-dock);
  backdrop-filter: blur(20px); -webkit-backdrop-filter: blur(20px);
  border: 1px solid var(--pv-media-dock-border);
  box-shadow: var(--pv-media-shadow);
  scrollbar-width: none;
  transition: opacity .25s ease;
}
.pv-filmstrip::-webkit-scrollbar { display: none; }
.pv-film-thumb {
  width: 56px; height: 40px; flex: none;
  border-radius: 7px; overflow: hidden; cursor: pointer;
  border: 1.5px solid transparent;
  display: flex; align-items: center; justify-content: center;
  background: var(--pv-media-surface); color: var(--pv-media-muted);
  transition: transform .18s var(--motion-spring), border-color .15s ease;
}
.pv-film-thumb:hover { transform: translateY(-2px); }
.pv-film-thumb.active { border-color: var(--pv-media-active); }
.pv-film-thumb img { width: 100%; height: 100%; object-fit: cover; }

/* 控件自动隐藏 */
.pv-stage.ui-hidden { cursor: none; }
.pv-stage.ui-hidden .pv-ctl,
.pv-stage.ui-hidden .pv-edge,
.pv-stage.ui-hidden .pv-topbar,
.pv-stage.ui-hidden .pv-filmstrip { opacity: 0; pointer-events: none; }
.pv-stage.ui-hidden .pv-ctl { transform: translateX(-50%) translateY(8px); }
.pv-stage.ui-hidden .pv-topbar { transform: translateY(-8px); }

/* 音频预览 */
.pv-audio-stage {
  flex: 1; min-height: 0; position: relative; overflow: hidden;
  background: var(--pv-audio-base);
  color: var(--pv-audio-fg); user-select: none;
}

/* 曲目封面与信息 */
.pv-audio-center {
  position: absolute; z-index: 2; inset: 0 0 164px;
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 12px; padding: 24px; pointer-events: none;
}
.pv-disc {
  width: 156px; height: 156px; border-radius: 26px;
  display: flex; align-items: center; justify-content: center;
  overflow: hidden; position: relative; flex: none;
  background: var(--pv-media-surface);
  border: 1px solid var(--pv-media-border);
  box-shadow: 0 20px 48px rgba(15, 23, 42, .28);
}
.pv-disc img { width: 100%; height: 100%; object-fit: cover; }
.pv-disc.empty {
  background:
    linear-gradient(135deg, color-mix(in srgb, var(--pv-media-active) 88%, #1e1740), color-mix(in srgb, var(--pv-media-active) 42%, #15203b));
  color: var(--pv-media-active-fg);
}
.pv-audio-title {
  max-width: min(640px, 82%); color: var(--pv-audio-fg); font-size: 18px; font-weight: 720; letter-spacing: .01em;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.pv-audio-sub {
  display: flex; align-items: center; gap: 7px;
  font-size: 12.5px; color: var(--pv-audio-muted); font-variant-numeric: tabular-nums;
}
.pv-audio-sub i { font-style: normal; opacity: .45; }

/* 底部实体控制条 */
.pv-audio-dock {
  position: absolute; z-index: 9; left: 50%; bottom: 16px;
  width: min(760px, calc(100% - 32px));
  padding: 12px 14px 11px;
  border-radius: 16px;
  color: var(--pv-media-fg); background: var(--pv-media-dock);
  transform: translateX(-50%);
  backdrop-filter: blur(22px) saturate(1.15); -webkit-backdrop-filter: blur(22px) saturate(1.15);
  border: 1px solid var(--pv-media-dock-border);
  box-shadow: var(--pv-media-shadow);
  --wails-draggable: no-drag;
}
.pv-audio-progress { position: relative; height: 16px; display: flex; align-items: center; cursor: pointer; }
.pv-audio-track {
  position: relative; width: 100%; height: 4px; border-radius: 999px;
  background: color-mix(in srgb, var(--pv-media-muted) 28%, transparent); overflow: visible;
}
.pv-audio-buffer { position: absolute; inset: 0 auto 0 0; border-radius: 999px; background: color-mix(in srgb, var(--pv-media-muted) 48%, transparent); }
.pv-audio-fill {
  position: absolute; inset: 0 auto 0 0; border-radius: 999px;
  background: var(--pv-media-active);
}
.pv-audio-thumb {
  position: absolute; right: -6px; top: 50%; translate: 0 -50%;
  width: 12px; height: 12px; border-radius: 50%; background: var(--pv-media-active-fg);
  box-shadow: 0 1px 6px rgba(0, 0, 0, .5);
  opacity: 0; transition: opacity .15s ease, scale .15s var(--motion-spring);
}
.pv-audio-progress:hover .pv-audio-thumb, .pv-audio-progress:focus-within .pv-audio-thumb { opacity: 1; }
.pv-audio-progress:hover .pv-audio-track { height: 5px; }
.pv-audio-progress input[type='range'] {
  position: absolute; inset: 0; width: 100%; margin: 0; opacity: 0; cursor: pointer;
}
.pv-audio-times {
  display: flex; justify-content: space-between; margin-top: 5px;
  font-size: 11.5px; color: var(--pv-media-muted); font-variant-numeric: tabular-nums;
}
.pv-audio-controls {
  display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 9px;
}
.pv-audio-group { display: flex; align-items: center; gap: 4px; min-width: 0; }
.pv-audio-right { justify-content: flex-end; gap: 10px; }
.pv-abtn {
  width: 36px; height: 36px; flex: none;
  display: inline-flex; align-items: center; justify-content: center;
  margin: 0; padding: 0;
  border: 1px solid var(--pv-media-border); border-radius: 10px; cursor: pointer;
  color: var(--pv-media-fg); background: var(--pv-media-surface);
  transition: background .16s ease, border-color .16s ease, transform .18s var(--motion-spring), color .16s;
}
.pv-abtn:hover { background: var(--pv-media-surface-hover); }
.pv-abtn:active { transform: scale(.9); }
.pv-abtn:focus-visible { outline: 2px solid var(--pv-media-active); outline-offset: 2px; }
.pv-abtn.active { color: var(--pv-media-active-fg); background: var(--pv-media-active); border-color: var(--pv-media-active); }
.pv-abtn-main {
  width: 46px; height: 46px; border-radius: 50%;
  border-color: var(--pv-media-active);
  color: var(--pv-media-active-fg); background: var(--pv-media-active);
  box-shadow: 0 8px 22px color-mix(in srgb, var(--pv-media-active) 38%, transparent);
}
.pv-abtn-main:hover { color: var(--pv-media-active-fg); background: var(--pv-media-active); transform: scale(1.06); }
.pv-abtn-text { width: auto; min-width: 40px; padding: 0 9px; font-size: 12.5px; font-weight: 650; font-variant-numeric: tabular-nums; }
.pv-audio-vol { display: flex; align-items: center; gap: 7px; }
.pv-audio-vol-range {
  width: 86px; accent-color: var(--pv-media-active); height: 3px; cursor: pointer;
}
.pv-audio-vol-val { font-size: 11.5px; color: var(--pv-media-muted); min-width: 36px; font-variant-numeric: tabular-nums; }

/* 弹层：倍速 / 播放列表 */
.pv-audio-pop {
  position: absolute; right: 14px; bottom: calc(100% + 10px); z-index: 6;
  min-width: 148px; max-height: 300px; overflow-y: auto;
  padding: 6px; border-radius: 14px;
  color: var(--pv-media-fg); background: var(--pv-media-dock);
  backdrop-filter: blur(20px); -webkit-backdrop-filter: blur(20px);
  border: 1px solid var(--pv-media-dock-border);
  box-shadow: var(--pv-media-shadow);
  animation: pv-pop-in 160ms var(--motion-spring) both;
}
@keyframes pv-pop-in { from { opacity: 0; transform: translateY(8px) scale(.97); } }
.pv-audio-pop-list { left: 14px; right: auto; width: min(340px, 80%); }
.pv-audio-pop-title {
  display: flex; align-items: center; justify-content: space-between;
  padding: 7px 10px 5px; font-size: 12px; font-weight: 700; color: var(--pv-media-fg);
}
.pv-audio-pop-title span { color: var(--pv-media-muted); font-weight: 500; font-variant-numeric: tabular-nums; }
.pv-audio-pop-item {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 7px 10px; border: 1px solid transparent; border-radius: 9px; cursor: pointer;
  background: transparent; color: var(--pv-media-fg); font-size: 12.5px; text-align: left;
  transition: background .14s ease;
}
.pv-audio-pop-item:hover { background: var(--pv-media-surface-hover); border-color: var(--pv-media-border); }
.pv-audio-pop-item.on { color: var(--pv-media-active-fg); background: var(--pv-media-active); border-color: var(--pv-media-active); }
.pv-audio-pop-check { width: 15px; flex: none; display: inline-flex; }
.pv-audio-track-no { width: 20px; flex: none; text-align: right; color: var(--pv-media-muted); font-variant-numeric: tabular-nums; font-size: 11.5px; }
.pv-audio-track-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pv-audio-eq { display: inline-flex; align-items: flex-end; gap: 2px; height: 12px; flex: none; }
.pv-audio-eq i { width: 3px; border-radius: 2px; background: currentColor; animation: pv-eq 900ms ease-in-out infinite; }
.pv-audio-eq i:nth-child(1) { height: 60%; animation-delay: -200ms; }
.pv-audio-eq i:nth-child(2) { height: 100%; animation-delay: -500ms; }
.pv-audio-eq i:nth-child(3) { height: 45%; animation-delay: -800ms; }
.pv-audio-eq.paused i { animation-play-state: paused; opacity: .5; }
@keyframes pv-eq { 0%, 100% { transform: scaleY(.4); } 50% { transform: scaleY(1); } }

.pv-audio-el { display: none; }
.pv-audio-err {
  position: absolute; top: 50%; left: 50%; transform: translate(-50%, -50%); z-index: 10;
  display: flex; flex-direction: column; align-items: center; gap: 10px;
  padding: 20px 26px; border-radius: 14px;
  background: var(--pv-media-dock); border: 1px solid var(--pv-media-dock-border);
  box-shadow: var(--pv-media-shadow); color: #c43d4b; font-size: 13px;
}

@media (max-width: 680px) {
  .pv-audio-center { bottom: 182px; }
  .pv-audio-dock { width: calc(100% - 20px); bottom: 10px; padding: 10px; }
  .pv-audio-controls { gap: 6px; }
  .pv-audio-group { gap: 2px; }
  .pv-audio-vol-range { width: 54px; }
  .pv-audio-vol-val { display: none; }
  .pv-audio-title { max-width: calc(100% - 32px); font-size: 16px; }
  .pv-disc { width: 132px; height: 132px; border-radius: 22px; }
}

/* 图片与音频共用48px标题栏，右上角窗口按钮无外侧留白。 */
.pv-topbar { position: relative; flex: 0 0 48px; align-items: center; gap: 12px; padding: 0 0 0 18px; background: var(--bg-surface); border-bottom: 1px solid var(--border-light); }
.pv-topbar-meta { flex: 1; min-width: 0; gap: 12px; padding: 0; color: var(--text-primary); }
.pv-topbar-meta > :deep(svg) { color: var(--color-primary); }
.pv-topbar-name { font-size: 13px; font-weight: 600; text-shadow: none; }
.pv-topbar-sub { color: var(--text-secondary); font-size: 11px; }
.pv-topbar-actions { align-self: stretch; border: 0; border-radius: 0; box-shadow: none; background: transparent; }
.pv-window-btn { width: 46px; height: 100%; border: 0; border-radius: 0; }
.pv-window-btn:active:not(:disabled) { transform: none; }
.pv-stage { background: var(--bg-base); }
.pv-img-layer { bottom: 76px; }
.pv-img-layer img { box-shadow: var(--shadow-sm); }
.pv-edge { width: 36px; height: 48px; border-radius: var(--radius-md); box-shadow: var(--shadow-sm); }
.pv-stage-busy { top: 16px; right: 18px; color: var(--color-primary); }
.pv-ctl { bottom: 20px; gap: 4px; padding: 6px 10px; border-radius: var(--radius-md); box-shadow: var(--shadow-md); }
.pv-ctl-btn { width: 36px; height: 36px; border-radius: var(--radius-sm); }
.pv-ctl-btn.pv-window-btn { width: 46px; height: 100%; border-radius: 0; }
.pv-ctl-btn:hover:not(:disabled) { border-color: transparent; }
.pv-ctl-btn.active { color: var(--color-primary); background: var(--listselectbg); }
.pv-ctl-btn :deep(svg) { width: 18px; height: 18px; }
.pv-window-btn :deep(svg) { width: 14px; height: 14px; }
.pv-filmstrip { bottom: 82px; padding: 8px; gap: 8px; max-width: calc(100% - 120px); }
.pv-film-thumb { width: 64px; height: 48px; }
.pv-stage.ui-hidden .pv-ctl:focus-within, .pv-stage.ui-hidden .pv-filmstrip:focus-within { opacity: 1; pointer-events: auto; }
.pv-stage.ui-hidden .pv-ctl:focus-within { transform: translateX(-50%); }
.pv-audio-center { inset: 0 0 150px; flex-direction: row; gap: 40px; padding: 40px max(32px, 10vw); }
.pv-disc { width: clamp(160px, 28vw, 300px); height: auto; aspect-ratio: 1; border-radius: var(--radius-xl); background: var(--bg-surface); border: 1px solid var(--border-light); box-shadow: var(--shadow-lg); animation: none; }
.pv-disc.empty { background: var(--bg-subtle); color: var(--color-primary); }
.pv-disc.empty :deep(svg) { width: 64px; height: 64px; stroke-width: 1.25; }
.pv-audio-details { flex: 1; min-width: 0; max-width: 460px; display: flex; flex-direction: column; align-items: flex-start; gap: 16px; }
.pv-audio-eyebrow { color: var(--color-primary); font-size: 12px; font-weight: 600; }
.pv-audio-title { max-width: 100%; font-size: clamp(20px, 2.5vw, 32px); font-weight: 600; line-height: 1.45; letter-spacing: -.02em; white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; }
.pv-audio-sub { color: var(--text-secondary); font-size: 12px; flex-wrap: wrap; }
.pv-audio-dock { width: min(800px, calc(100% - 64px)); bottom: 32px; padding: 16px 24px; border-radius: var(--radius-lg); box-shadow: var(--shadow-sm); backdrop-filter: none; }
.pv-abtn { border: 0; background: transparent; border-radius: var(--radius-sm); }
.pv-abtn.active { background: var(--listselectbg); color: var(--color-primary); }
.pv-abtn-main { width: 44px; height: 44px; border-radius: 50%; box-shadow: none; background: var(--color-primary); color: #fff; }
.pv-audio-controls { margin-top: 8px; }
.pv-audio-group { gap: 8px; }
.pv-audio-pop { max-height: min(45vh, 320px); border-radius: var(--radius-md); backdrop-filter: none; }
.pv-audio-pop-item.on { background: var(--listselectbg); color: var(--color-primary); border-color: transparent; }
.pv-audio-pop-item:hover { border-color: transparent; }
.pv-audio-thumb { background: var(--color-primary); box-shadow: none; }
.pv-audio-err { max-width: calc(100% - 48px); color: var(--color-error); }
@media (max-width: 700px) {
  .pv-topbar-sub { display: none; }
  .pv-audio-center { flex-direction: column; gap: 24px; padding: 24px; bottom: 164px; }
  .pv-audio-details { flex: 0; align-items: center; text-align: center; gap: 10px; max-width: 90%; }
  .pv-audio-sub { justify-content: center; }
  .pv-disc { width: clamp(120px, 24vh, 220px); }
  .pv-audio-title { font-size: 20px; -webkit-line-clamp: 2; }
  .pv-audio-dock { width: calc(100% - 32px); bottom: 16px; padding: 12px 16px; }
  .pv-audio-group { gap: 2px; }
  .pv-audio-vol-range { width: 54px; }
}
@media (max-height: 520px) {
  .pv-audio-center { flex-direction: row; bottom: 132px; padding: 20px 32px; gap: 24px; }
  .pv-disc { width: min(22vh, 140px); }
  .pv-audio-details { align-items: flex-start; text-align: left; flex: 1; }
  .pv-audio-title { font-size: 18px; }
  .pv-audio-dock { bottom: 12px; padding: 8px 16px; }
}
</style>
