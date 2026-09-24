<script setup>
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import pdfWorkerURL from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

const props = defineProps({ url: String, file: Object })
const stage = ref(null)
const canvas = ref(null)
const loading = ref(true)
const error = ref('')
const pages = ref(0)
const pageNumber = ref(1)
const zoom = ref(1)
const fitWidth = ref(true)
const passwordRequired = ref(false)
const password = ref('')
let passwordUpdater = null
let loadingTask = null
let document = null
let renderTask = null
let renderSequence = 0
let disposed = false
let stageObserver = null
function fitOnResize() { if (fitWidth.value && pages.value) void renderPage() }

async function renderPage() {
  if (!document || !canvas.value || !stage.value) return
  const sequence = ++renderSequence
  renderTask?.cancel()
  try {
    const page = await document.getPage(pageNumber.value)
    if (disposed || sequence !== renderSequence) return
    const base = page.getViewport({ scale: 1 })
    const scale = fitWidth.value ? Math.min(3, Math.max(.25, (stage.value.clientWidth - 48) / base.width)) : zoom.value
    const viewport = page.getViewport({ scale })
    const pixelRatio = Math.max(.1, Math.min(window.devicePixelRatio || 1, 2, Math.sqrt(32_000_000 / (viewport.width * viewport.height))))
    const target = canvas.value
    target.width = Math.max(1, Math.floor(viewport.width * pixelRatio))
    target.height = Math.max(1, Math.floor(viewport.height * pixelRatio))
    target.style.width = `${Math.round(viewport.width)}px`
    target.style.height = `${Math.round(viewport.height)}px`
    renderTask = page.render({ canvas: target, viewport, transform: pixelRatio === 1 ? undefined : [pixelRatio, 0, 0, pixelRatio, 0, 0] })
    await renderTask.promise
    if (sequence === renderSequence) stage.value.scrollTop = 0
  } catch (cause) {
    if (!disposed && cause?.name !== 'RenderingCancelledException') error.value = String(cause?.message || cause)
  }
}

async function openPDF() {
  loading.value = true
  error.value = ''
  try {
    const { getDocument, GlobalWorkerOptions, PasswordResponses } = await import('pdfjs-dist')
    if (disposed) return
    GlobalWorkerOptions.workerSrc = pdfWorkerURL
    loadingTask = getDocument({ url: props.url, rangeChunkSize: 256 * 1024, disableAutoFetch: true, disableStream: true, useSystemFonts: true })
    loadingTask.onPassword = (update, reason) => {
      passwordUpdater = update
      passwordRequired.value = true
      password.value = ''
      error.value = reason === PasswordResponses.INCORRECT_PASSWORD ? '密码不正确，请重试' : ''
      loading.value = false
    }
    document = await loadingTask.promise
    if (disposed) return
    pages.value = document.numPages
    if (!pages.value) throw new Error('PDF 没有可显示的页面')
    passwordRequired.value = false
    loading.value = false
    await nextTick()
    await renderPage()
  } catch (cause) {
    if (!disposed && cause?.name !== 'AbortException') error.value = String(cause?.message || cause)
    loading.value = false
  }
}

function submitPassword() {
  if (!password.value || !passwordUpdater) return
  passwordRequired.value = false
  loading.value = true
  error.value = ''
  passwordUpdater(password.value)
}

function changePage(step) {
  const next = Math.min(pages.value, Math.max(1, pageNumber.value + step))
  if (next === pageNumber.value) return
  pageNumber.value = next
  void renderPage()
}

function changeZoom(step) {
  fitWidth.value = false
  zoom.value = Math.max(.25, Math.min(3, zoom.value + step))
  void renderPage()
}

onMounted(() => {
  if (stage.value && typeof ResizeObserver !== 'undefined') {
    stageObserver = new ResizeObserver(fitOnResize)
    stageObserver.observe(stage.value)
  } else window.addEventListener('resize', fitOnResize)
  void openPDF()
})
onBeforeUnmount(() => {
  disposed = true
  stageObserver?.disconnect()
  window.removeEventListener('resize', fitOnResize)
  ++renderSequence
  renderTask?.cancel()
  void loadingTask?.destroy()
})
</script>

<template>
  <div class="document-viewer">
    <div class="document-toolbar">
      <strong>PDF 预览</strong>
      <div v-if="pages" class="document-controls">
        <button class="btn sm" :disabled="pageNumber <= 1" @click="changePage(-1)">上一页</button>
        <span>{{ pageNumber }} / {{ pages }}</span>
        <button class="btn sm" :disabled="pageNumber >= pages" @click="changePage(1)">下一页</button>
        <span class="document-divider"></span>
        <button class="btn sm" @click="changeZoom(-.25)">−</button>
        <button class="btn sm" @click="fitWidth = true; renderPage()">适合宽度</button>
        <button class="btn sm" @click="changeZoom(.25)">＋</button>
      </div>
    </div>
    <div v-if="passwordRequired" class="document-state">
      <form @submit.prevent="submitPassword"><label>请输入 PDF 密码</label><input v-model="password" class="input" type="password" autofocus /><button class="btn primary sm" type="submit">解锁</button></form>
      <span v-if="error">{{ error }}</span>
    </div>
    <div v-else-if="loading" class="document-state"><span class="spin"></span>正在解析 PDF…</div>
    <div v-else-if="error" class="document-state" role="alert">{{ error }}</div>
    <div v-show="!loading && !error && !passwordRequired" ref="stage" class="pdf-stage"><canvas ref="canvas"></canvas></div>
  </div>
</template>

<style scoped>
.document-viewer { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.document-toolbar { min-height: 48px; padding: 7px 16px; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--border-light); background: var(--bg-surface); }
.document-toolbar strong { font-size: 12px; color: var(--text-secondary); white-space: nowrap; }
.document-controls { display: flex; align-items: center; justify-content: flex-end; gap: 6px; flex-wrap: wrap; font-size: 12px; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.document-divider { width: 1px; height: 20px; margin: 0 4px; background: var(--border-light); }
.document-state { flex: 1; display: flex; align-items: center; justify-content: center; flex-direction: column; gap: 10px; color: var(--text-secondary); font-size: 13px; }
.document-state form { display: flex; align-items: center; gap: 8px; }
.pdf-stage { flex: 1; min-height: 0; overflow: auto; padding: 24px; text-align: center; background: var(--bg-base); }
.pdf-stage canvas { display: block; margin: 0 auto; background: white; box-shadow: var(--shadow-md); }
@media (max-width: 600px) { .document-toolbar { flex-wrap: wrap; } .document-controls { justify-content: flex-start; } }
</style>
