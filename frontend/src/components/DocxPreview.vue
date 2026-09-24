<script setup>
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({ bytes: Object, file: Object })
const content = ref(null)
const stage = ref(null)
const loading = ref(true)
const error = ref('')
const pages = ref(0)
const zoom = ref(100)
const fitToWidth = ref(true)
let disposed = false
let stageObserver = null
function fitOnResize() { if (fitToWidth.value && !loading.value) void fitWidth() }

async function renderDocument() {
  try {
    const { renderAsync } = await import('docx-preview')
    if (disposed || !content.value) return
    await renderAsync(props.bytes, content.value, content.value, {
      className: 'docx',
      inWrapper: true,
      breakPages: true,
      renderHeaders: true,
      renderFooters: true,
      renderFootnotes: true,
      renderEndnotes: true,
      renderAltChunks: false,
      useBase64URL: false,
    })
    if (disposed) return
    pages.value = content.value.querySelectorAll('section.docx').length
    loading.value = false
    await fitWidth()
  } catch (cause) {
    if (!disposed) { error.value = String(cause?.message || cause); loading.value = false }
  }
}

function changeZoom(step) { fitToWidth.value = false; zoom.value = Math.max(50, Math.min(200, zoom.value + step)) }

async function fitWidth() {
  fitToWidth.value = true
  zoom.value = 100
  await nextTick()
  const page = content.value?.querySelector('section.docx')
  if (page && stage.value) zoom.value = Math.max(25, Math.min(200, Math.round((stage.value.clientWidth - 48) / page.offsetWidth * 100)))
}

onMounted(() => {
  if (stage.value && typeof ResizeObserver !== 'undefined') {
    stageObserver = new ResizeObserver(fitOnResize)
    stageObserver.observe(stage.value)
  } else window.addEventListener('resize', fitOnResize)
  void renderDocument()
})
onBeforeUnmount(() => { disposed = true; stageObserver?.disconnect(); window.removeEventListener('resize', fitOnResize); content.value?.replaceChildren() })
</script>

<template>
  <div class="document-viewer">
    <div class="document-toolbar"><strong>Word 预览 <span v-if="pages">· {{ pages }} 页</span></strong>
      <div class="document-controls"><button class="btn sm" @click="changeZoom(-10)">−</button><span>{{ zoom }}%</span><button class="btn sm" @click="changeZoom(10)">＋</button><button class="btn sm" @click="fitWidth">适合宽度</button></div>
    </div>
    <div v-if="loading" class="document-state"><span class="spin"></span>正在排版文档…</div>
    <div v-else-if="error" class="document-state" role="alert">{{ error }}</div>
    <div v-show="!error" ref="stage" class="docx-stage"><div ref="content" class="docx-content" :style="{ zoom: zoom / 100 }"></div></div>
  </div>
</template>

<style scoped>
.document-viewer { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.document-toolbar { min-height: 48px; padding: 7px 16px; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--border-light); background: var(--bg-surface); }
.document-toolbar strong { font-size: 12px; color: var(--text-secondary); }
.document-controls { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-secondary); }
.document-state { position: absolute; inset: 48px 0 0; z-index: 2; display: flex; align-items: center; justify-content: center; gap: 10px; background: var(--bg-base); color: var(--text-secondary); font-size: 13px; }
.docx-stage { flex: 1; min-height: 0; overflow: auto; background: var(--bg-base); }
.docx-content { width: max-content; min-width: 100%; }
.docx-content :deep(.docx-wrapper) { background: transparent; padding: 24px 0; }
.docx-content :deep(section.docx) { margin: 0 auto 24px; box-shadow: var(--shadow-md); }
@media (max-width: 600px) { .document-toolbar { flex-wrap: wrap; } .document-controls { flex-wrap: wrap; } }
</style>
