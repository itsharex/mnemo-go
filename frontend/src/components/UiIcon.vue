<script setup>
// 全局内联 SVG 图标（stroke 风格，lucide 式），按 name 分发。
// 设计规范：UI 禁用 Emoji，一律使用该组件。
import { computed } from 'vue'

const props = defineProps({
  name: { type: String, required: true },
  size: { type: [Number, String], default: 16 },
})

const PATHS = {
  folder: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
  file: 'M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z M14 3v6h6',
  video: 'M4 5h12a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2z M22 8l-4 3 4 3z M18 8v6',
  audio: 'M9 18V6l10-2v11 M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0z M19 15a3 3 0 1 1-6 0 3 3 0 0 1 6 0z',
  image: 'M4 5h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2z M8.5 11a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3z M21 15l-5-5-9 9',
  archive: 'M3 4h18v4H3z M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8 M10 12h4',
  doc: 'M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z M14 3v6h6 M9 13h6 M9 17h6',
  download: 'M12 3v12 M6 9l6 6 6-6 M4 21h16',
  upload: 'M12 21V9 M6 15l6-6 6 6 M4 3h16',
  share: 'M8.5 12a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z M20.5 6a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z M20.5 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z M8.2 10.8l7.6-3.6 M8.2 13.2l7.6 3.6',
  link: 'M10 14a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7 M14 10a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7',
  star: 'M12 3l2.7 5.6 6.1.8-4.5 4.3 1.1 6.1L12 16.9 6.6 19.8l1.1-6.1-4.5-4.3 6.1-.8z',
  trash: 'M4 7h16 M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2 M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13 M10 11v6 M14 11v6',
  restore: 'M3 12a9 9 0 1 0 3-6.7 M3 4v5h5',
  pencil: 'M17 3l4 4L8 20H4v-4z M14 6l4 4',
  move: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z M12 10v6 M9 13l3 3 3-3',
  copy: 'M9 9h11a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V10a1 1 0 0 1 1-1z M5 15H4a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h11a1 1 0 0 1 1 1v1',
  migrate: 'M8 7h13 M17 3l4 4-4 4 M16 17H3 M7 21l-4-4 4-4',
  search: 'M10.5 18a7.5 7.5 0 1 1 0-15 7.5 7.5 0 0 1 0 15z M21 21l-5.2-5.2',
  home: 'M3 11l9-8 9 8 M5 10v10h14V10 M10 20v-6h4v6',
  back: 'M19 12H5 M11 18l-6-6 6-6',
  refresh: 'M21 12a9 9 0 1 1-3-6.7 M21 4v5h-5',
  settings: 'M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7z M19 12a7 7 0 0 0-.1-1.2l2-1.5-2-3.4-2.3.9a7 7 0 0 0-2.1-1.2L14 3h-4l-.5 2.6a7 7 0 0 0-2.1 1.2l-2.3-.9-2 3.4 2 1.5A7 7 0 0 0 5 12a7 7 0 0 0 .1 1.2l-2 1.5 2 3.4 2.3-.9a7 7 0 0 0 2.1 1.2L10 21h4l.5-2.6a7 7 0 0 0 2.1-1.2l2.3.9 2-3.4-2-1.5A7 7 0 0 0 19 12z',
  close: 'M6 6l12 12 M18 6L6 18',
  info: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M12 11v5 M12 8h.01',
  warning: 'M12 3l10 17H2z M12 10v4 M12 17.5h.01',
  cloud: 'M7 18a4.5 4.5 0 0 1-.4-9A6 6 0 0 1 18.3 10 4 4 0 0 1 18 18z',
  'cloud-down': 'M7 15a4.5 4.5 0 0 1-.4-9A6 6 0 0 1 18.3 7 4 4 0 0 1 18 15 M12 11v7 M9 15l3 3 3-3',
  monitor: 'M4 4h16a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z M9 20h6 M12 16v4',
  'chevron-right': 'M9 6l6 6-6 6',
  'chevron-down': 'M6 9l6 6 6-6',
  check: 'M4 12l5 5 11-11',
  grid: 'M4 5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v4a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z M14 5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v4a1 1 0 0 1-1 1h-4a1 1 0 0 1-1-1z M4 15a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v4a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z M14 15a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v4a1 1 0 0 1-1 1h-4a1 1 0 0 1-1-1z',
  list: 'M9.5 6H20 M9.5 12H20 M9.5 18H20 M4 5.2h1.6v1.6H4z M4 11.2h1.6v1.6H4z M4 17.2h1.6v1.6H4z',
  external: 'M14 4h6v6 M20 4l-9 9 M18 13v6a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h6',
  plus: 'M12 5v14 M5 12h14',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M3 12h18 M12 3a13.5 13.5 0 0 1 0 18 M12 3a13.5 13.5 0 0 0 0 18',
  pause: 'M8 5v14 M16 5v14',
  play: 'M7 4l13 8-13 8z',
  'x-circle': 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M9 9l6 6 M15 9l-6 6',
  'up-level': 'M9 14l-5-5 5-5 M4 9h10a6 6 0 0 1 6 6v5',
  'sort-asc': 'M12 19V5 M6 11l6-6 6 6',
  'sort-desc': 'M12 5v14 M6 13l6 6 6-6',
  drive: 'M4 15h16a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-3a1 1 0 0 1 1-1z M6 18h.01 M4.5 15L7 6h10l2.5 9',
  database: 'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3z M4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6 M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6',
  sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10z M12 2v2 M12 20v2 M4.9 4.9l1.4 1.4 M17.7 17.7l1.4 1.4 M2 12h2 M20 12h2 M4.9 19.1l1.4-1.4 M17.7 6.3l1.4-1.4',
  moon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z',
  forward: 'M13 5l7 7-7 7 M5 5l7 7-7 7',
  rewind: 'M11 19l-9-7 9-7z M20 19l-9-7 9-7z',
  'fast-forward': 'M13 19l9-7-9-7z M4 19l9-7-9-7z',
  'skip-back': 'M5 5v14 M19 5l-10 7 10 7z',
  'skip-forward': 'M19 5v14 M5 5l10 7-10 7z',
  repeat: 'M17 2l4 4-4 4 M3 11V8a2 2 0 0 1 2-2h16 M7 22l-4-4 4-4 M21 13v3a2 2 0 0 1-2 2H3',
  'rotate-right': 'M21 4v6h-6 M21 10a9 9 0 1 0-2 8',
  'chevron-left': 'M15 6l-6 6 6 6',
  priority: 'M4 4h16 M12 20V9 M6.5 14.5L12 9l5.5 5.5',
  volume: 'M11 5L6 9H3v6h3l5 4z M15.5 8.5a5 5 0 0 1 0 7 M18.5 5.5a9 9 0 0 1 0 13',
  'volume-x': 'M11 5L6 9H3v6h3l5 4z M16 9l5 5 M21 9l-5 5',
  eye: 'M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12z M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  'eye-off': 'M3 3l18 18 M10.6 10.6a2 2 0 0 0 2.8 2.8 M9.9 5.2A10.8 10.8 0 0 1 12 5c6.5 0 10 7 10 7a18.5 18.5 0 0 1-3.1 3.9 M6.6 6.6C3.7 8.4 2 12 2 12s3.5 7 10 7a10.8 10.8 0 0 0 3.8-.7',
  camera: 'M4 7h3l1.5-2h7L17 7h3a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z',
  captions: 'M4 5h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2z M7 10h3 M7 14h2 M14 10h3 M14 14h3',
  maximize: 'M8 3H3v5 M16 3h5v5 M21 16v5h-5 M3 16v5h5',
  minimize: 'M3 8h5V3 M16 3v5h5 M21 16h-5v5 M8 21v-5H3',
  'window-minimize': 'M5 12h14',
  'window-maximize': 'M5 5h14v14H5z',
  'window-restore': 'M7 7h12v12H7z M5 17H4V5h12v1',
  'more-horizontal': 'M5 12h.01 M12 12h.01 M19 12h.01',
  'picture-in-picture': 'M3 5h18v14H3z M13 13h6v4h-6z',
}

const d = computed(() => PATHS[props.name] || PATHS.file)
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.8"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    style="flex-shrink:0;display:inline-block;vertical-align:-2px"
  >
    <path :d="d" />
  </svg>
</template>
