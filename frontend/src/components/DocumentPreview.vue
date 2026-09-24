<script setup>
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { PinFileSnapshot, PreviewURL } from '../api'
import { DOCUMENT_LIMITS, readDocumentBytes } from '../documentPreview'

const props = defineProps({ account: Object, file: Object, kind: String })
const renderers = {
  pdf: defineAsyncComponent(() => import('./PdfPreview.vue')),
  docx: defineAsyncComponent(() => import('./DocxPreview.vue')),
  xlsx: defineAsyncComponent(() => import('./SpreadsheetPreview.vue')),
  pptx: defineAsyncComponent(() => import('./PptxPreview.vue')),
}
const renderer = computed(() => renderers[props.kind])
const loading = ref(true)
const error = ref('')
const url = ref('')
const bytes = shallowRef(null)
const controller = new AbortController()
let disposed = false

async function load() {
  loading.value = true
  error.value = ''
  try {
    const limit = DOCUMENT_LIMITS[props.kind]
    if (!limit) throw new Error('不支持此文档格式')
    if (props.file.size > limit) throw new Error('文件超过在线预览大小上限，请下载后查看')
    await PinFileSnapshot(props.account.user_id, props.account.drive_id, props.file)
    if (disposed) return
    const source = await PreviewURL(props.account.user_id, props.account.drive_id, props.file.file_id)
    if (disposed) return
    if (props.kind === 'pdf') url.value = source
    else bytes.value = await readDocumentBytes(source, limit, controller.signal)
  } catch (cause) {
    if (!disposed) error.value = String(cause?.message || cause)
  } finally {
    if (!disposed) loading.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => { disposed = true; controller.abort() })
</script>

<template>
  <div class="document-preview">
    <div v-if="loading" class="document-state" role="status"><span class="spin"></span><span>正在打开文档…</span></div>
    <div v-else-if="error" class="document-state" role="alert"><span>{{ error }}</span><button class="btn sm" @click="load">重试</button></div>
    <component :is="renderer" v-else :url="url" :bytes="bytes" :file="file" />
  </div>
</template>

<style scoped>
.document-preview { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; background: var(--bg-base); }
.document-state { flex: 1; display: flex; align-items: center; justify-content: center; gap: 12px; color: var(--text-secondary); font-size: 13px; }
</style>
