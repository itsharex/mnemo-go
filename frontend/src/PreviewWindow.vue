<script setup>
import { ref, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { motion, MotionConfig } from 'motion-v'
import PlayerPanel from './components/PlayerPanel.vue'
import PreviewModal from './components/PreviewModal.vue'
import { EventsOn, WindowHide } from '../wailsjs/runtime/runtime'
import { compactReveal, reducedMotion } from './motion/presets'

const props = defineProps({ seed: { type: Object, required: true } })
const file = ref(props.seed.file)
const opened = ref(true)
const toast = ref('')
const preview = ref(null)
const backgroundAudio = ref(false)
let stopClose
onMounted(() => { stopClose = EventsOn('preview:close-request', () => {
  if (props.seed.kind === 'video') close()
  else preview.value?.requestClose()
}) })
onBeforeUnmount(() => stopClose?.())
let toastTimer
function notify(message) {
  toast.value = String(message)
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = '' }, 4000)
}
async function close(force = false) {
  if (!force && backgroundAudio.value && props.seed.kind === 'audio') {
    WindowHide()
    return
  }
  opened.value = false
  clearTimeout(toastTimer)
  await nextTick()
  await Promise.race([window.__mnemoPreviewDrain?.(), new Promise(resolve => setTimeout(resolve, 3000))])
  window.go.app.PreviewHost.Close()
}
</script>

<template>
  <PlayerPanel v-if="opened && seed.kind === 'video'" :account="seed.account" :file="file" :files="seed.files" :capabilities="seed.capabilities" @select-file="file = $event" @close="close" @toast="notify" />
  <PreviewModal v-else-if="opened" ref="preview" :account="seed.account" :file="file" :file-list="seed.files" :background-audio="backgroundAudio" @background-audio="backgroundAudio = $event" @stop-close="close(true)" @close="close()" @toast="notify" />
  <MotionConfig :reduced-motion="reducedMotion">
    <motion.div
      v-if="toast"
      class="preview-window-toast"
      role="status"
      :initial="compactReveal.initial"
      :animate="compactReveal.animate"
      :transition="compactReveal.transition"
    >{{ toast }}</motion.div>
  </MotionConfig>
</template>

<style>
html.dark.preview-window {
  color-scheme: dark;
  --bg-base: #000; --bg-surface: #101010; --bg-elevated: #1b1b1b;
  --bg-hover: #ffffff14; --bg-subtle: #ffffff12;
  --text-primary: #fff; --text-secondary: #d0d0d0; --text-tertiary: #969696;
  --color-primary: #fff; --border-light: #ffffff1a; --border-focus: #fff;
  --listselectbg: #ffffff18; --control-border: #ffffff38;
}
.preview-window body, .preview-window #app { margin: 0; width: 100vw; height: 100vh; overflow: hidden; background: #000; }
.preview-window .modal.preview-modal { position: fixed; inset: 0; width: 100vw !important; height: 100vh !important; max-width: none !important; max-height: none !important; border: 0; border-radius: 0; }
.preview-window .preview-modal .modal-head { padding-right: 0; padding-top: 0; }
.preview-window .preview-modal svg { color: #fff !important; stroke: #fff; fill: none; stroke-width: 1.6; }
.preview-window .preview-modal .pv-abtn-main { background: #ffffff16; }
.preview-window .preview-modal .modal-head { height: 56px; min-height: 56px; padding: 0 0 0 16px; border-bottom: 1px solid #ffffff14; --wails-draggable: drag; }
.preview-window .preview-modal .modal-window-controls { height: 56px; align-self: stretch; }
.preview-window .preview-modal .modal-window-btn { height: 56px; border-radius: 0; }
.preview-window .preview-modal .pv-head-icon { display: none; }
.preview-window .preview-modal .pv-head-title { font-size: 13px; font-weight: 500; }
.preview-window .preview-modal .pv-head-sub { font-size: 10px; }
.preview-window .preview-modal .pv-head-pill { background: #ffffff10; color: #bbb; }
.preview-window .preview-modal .pv-toolbar { box-shadow: none; background: #101010; border-bottom: 1px solid #ffffff14; }
.preview-window .preview-modal .btn.primary, .preview-window .preview-modal .pv-save-btn { color: #fff; background: #ffffff16; }
.preview-window .preview-modal .pv-audio-cover { box-shadow: none; background: #161616; border: 1px solid #ffffff16; }
.preview-window .preview-modal .pv-audio-title { font-weight: 500; }
.preview-window .preview-modal .pv-image-stage { background: #000; }
.preview-window .preview-modal .pv-ctl-btn.active, .preview-window .preview-modal .pv-abtn.active { background: #ffffff20; }
.preview-window-toast {
  position: fixed; z-index: 9999; bottom: 120px; left: 50%;
  max-width: min(80vw, 520px); padding: 9px 13px;
  border: 1px solid #ffffff28; border-radius: 10px;
  background: color-mix(in srgb, #202020 88%, transparent); color: #fff;
  box-shadow: 0 12px 32px #0008; font-size: 13px; line-height: 1.5;
  pointer-events: none; text-align: center;
}
html.dark.preview-window.oled {
  --bg-surface: #000; --bg-elevated: #000; --bg-subtle: #000; --control-bg: #000;
}
.preview-window.oled .preview-modal .pv-toolbar,
.preview-window.oled .preview-modal .pv-audio-cover,
.preview-window.oled .preview-window-toast { background: #000; }
@media (prefers-reduced-motion: reduce) {
  .preview-window-toast { transition: none; }
}
</style>
