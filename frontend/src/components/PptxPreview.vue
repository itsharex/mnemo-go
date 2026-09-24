<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({ bytes: Object, file: Object })
const stage = ref(null)
const host = ref(null)
const loading = ref(true)
const error = ref('')
const slideCount = ref(0)
const currentSlide = ref(0)
const zoom = ref(100)
const controller = new AbortController()
let viewer = null
let disposed = false
let stageObserver = null

function resizeViewer() {
  if (!stage.value || !host.value) return
  const ratio = viewer?.slideWidth && viewer.slideHeight ? viewer.slideWidth / viewer.slideHeight : 16 / 9
  const width = Math.max(280, Math.min(stage.value.clientWidth - 48, (stage.value.clientHeight - 48) * ratio))
  host.value.style.width = `${width}px`
}

async function openPresentation() {
  try {
    const { PptxViewer, RECOMMENDED_ZIP_LIMITS } = await import('@aiden0z/pptx-renderer')
    if (disposed || !host.value) return
    resizeViewer()
    viewer = new PptxViewer(host.value, {
      fitMode: 'contain',
      zoomPercent: zoom.value,
      zipLimits: RECOMMENDED_ZIP_LIMITS,
      lazyMedia: true,
      lazySlides: true,
      pdfjs: false,
      onSlideChange: index => { currentSlide.value = index },
    })
    await viewer.open(props.bytes, { renderMode: 'slide', signal: controller.signal, lazyMedia: true, lazySlides: true })
    if (disposed) return
    slideCount.value = viewer.slideCount
    currentSlide.value = viewer.currentSlideIndex
    if (!slideCount.value) throw new Error('文件中没有可显示的幻灯片')
    resizeViewer()
  } catch (cause) {
    if (!disposed) error.value = String(cause?.message || cause)
  } finally {
    if (!disposed) loading.value = false
  }
}

async function goToSlide(index) {
  if (!viewer || index < 0 || index >= slideCount.value) return
  await viewer.goToSlide(index)
  currentSlide.value = viewer.currentSlideIndex
}

async function changeZoom(step) {
  if (!viewer) return
  zoom.value = Math.max(50, Math.min(200, zoom.value + step))
  await viewer.setZoom(zoom.value)
}

onMounted(() => {
  if (stage.value && typeof ResizeObserver !== 'undefined') {
    stageObserver = new ResizeObserver(resizeViewer)
    stageObserver.observe(stage.value)
  } else window.addEventListener('resize', resizeViewer)
  void openPresentation()
})
onBeforeUnmount(() => { disposed = true; controller.abort(); stageObserver?.disconnect(); window.removeEventListener('resize', resizeViewer); viewer?.destroy() })
</script>

<template>
  <div class="pptx-viewer">
    <div class="pptx-toolbar"><strong>演示文稿预览</strong>
      <div v-if="slideCount" class="pptx-controls">
        <button class="btn sm" :disabled="currentSlide === 0" @click="goToSlide(currentSlide - 1)">上一张</button><span>{{ currentSlide + 1 }} / {{ slideCount }}</span><button class="btn sm" :disabled="currentSlide + 1 >= slideCount" @click="goToSlide(currentSlide + 1)">下一张</button>
        <span class="pptx-divider"></span><button class="btn sm" @click="changeZoom(-10)">−</button><span>{{ zoom }}%</span><button class="btn sm" @click="changeZoom(10)">＋</button>
      </div>
    </div>
    <div v-if="loading" class="pptx-state"><span class="spin"></span>正在解析演示文稿…</div>
    <div v-else-if="error" class="pptx-state" role="alert">{{ error }}</div>
    <div ref="stage" class="pptx-stage"><div ref="host" class="pptx-host"></div></div>
  </div>
</template>

<style scoped>
.pptx-viewer { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.pptx-toolbar { min-height: 48px; padding: 7px 16px; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--border-light); background: var(--bg-surface); }
.pptx-toolbar strong { font-size: 12px; color: var(--text-secondary); white-space: nowrap; }
.pptx-controls { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.pptx-divider { width: 1px; height: 20px; background: var(--border-light); }
.pptx-state { position: absolute; z-index: 2; inset: 48px 0 0; display: flex; align-items: center; justify-content: center; gap: 10px; color: var(--text-secondary); background: var(--bg-base); font-size: 13px; }
.pptx-stage { flex: 1; min-height: 0; overflow: auto; padding: 24px; display: flex; justify-content: center; background: var(--bg-base); }
.pptx-host { flex: 0 0 auto; width: min(100%, 900px); }
@media (max-width: 600px) { .pptx-toolbar, .pptx-controls { flex-wrap: wrap; } }
</style>
