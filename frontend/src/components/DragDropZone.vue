<script setup>
// 桌面文件/文件夹拖拽上传容器（支持 Wails 原生 OnFileDrop 与 HTML5 拖拽）
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { onFileDrop } from '../api'
import UiIcon from './UiIcon.vue'

const emit = defineEmits(['drop-files'])

const isDragging = ref(false)
const zone = ref(null)
let dragCounter = 0
let unbindDrop = null

function onDragEnter(e) {
  e.preventDefault()
  dragCounter++
  if (e.dataTransfer && e.dataTransfer.types && e.dataTransfer.types.includes('Files')) {
    isDragging.value = true
  }
}

function onDragLeave(e) {
  e.preventDefault()
  dragCounter--
  if (dragCounter <= 0) {
    dragCounter = 0
    isDragging.value = false
  }
}

function onDragOver(e) {
  e.preventDefault()
  if (e.dataTransfer?.types?.includes('Files')) {
    e.dataTransfer.dropEffect = 'copy'
  }
}

function onDrop(e) {
  e.preventDefault()
  dragCounter = 0
  isDragging.value = false

  const dt = e.dataTransfer
  if (!dt || !dt.files || !dt.files.length) return

  const paths = []
  for (let i = 0; i < dt.files.length; i++) {
    const f = dt.files[i]
    if (f.path) paths.push(f.path)
  }
  if (paths.length) {
    emit('drop-files', paths)
  }
}

onMounted(() => {
  // Wails v2 原生文件拖入监听
  unbindDrop = onFileDrop((x, y, paths) => {
    const rect = zone.value?.getBoundingClientRect()
    if (!rect || !rect.width || !rect.height || x < rect.left || x >= rect.right || y < rect.top || y >= rect.bottom) return
    isDragging.value = false
    dragCounter = 0
    if (paths && paths.length) {
      emit('drop-files', paths)
    }
  })
})

onBeforeUnmount(() => {
  if (typeof unbindDrop === 'function') unbindDrop()
})
</script>

<template>
  <div
    ref="zone"
    class="drag-drop-zone"
    :class="{ 'drag-active': isDragging }"
    style="--wails-drop-target: drop"
    @dragenter="onDragEnter"
    @dragleave="onDragLeave"
    @dragover="onDragOver"
    @drop="onDrop"
  >
    <slot />
    <transition name="popover-zoom">
      <div v-if="isDragging" class="drag-overlay">
        <div class="drag-box">
          <UiIcon name="upload" :size="36" class="drag-icon" />
          <span class="drag-title">松开鼠标上传文件到当前目录</span>
          <span class="drag-sub">支持直接拖拽单个/多个文件或文件夹</span>
        </div>
      </div>
    </transition>
  </div>
</template>

<style scoped>
.drag-drop-zone {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}
.drag-active {
  outline: 2px dashed var(--color-primary);
  outline-offset: -2px;
  transition: outline-color var(--motion-fast) var(--motion-ease), outline-offset var(--motion-fast) var(--motion-spring);
}
.drag-overlay {
  position: absolute;
  inset: 8px;
  background: var(--bg-surface);
  border: 2px dashed var(--color-primary);
  border-radius: var(--radius-lg);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  pointer-events: none;
  box-shadow: var(--shadow-modal);
}
.drag-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  color: var(--color-primary);
}
.drag-icon {
  animation: pulse 1.4s var(--motion-ease) infinite;
}
.drag-title {
  font-size: 16px;
  font-weight: 700;
  color: var(--text-primary);
}
.drag-sub {
  font-size: 13px;
  color: var(--text-tertiary);
}
@media (prefers-reduced-motion: reduce) { .drag-active { transition: none; } .drag-icon { animation: none; } }
</style>
