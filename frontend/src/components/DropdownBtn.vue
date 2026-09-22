<script setup>
// 工具条下拉按钮：点击在按钮下方弹出菜单（复用 ContextMenu）。
// items: [{ icon, label, action, danger, disabled, sep }]
import { ref } from 'vue'
import ContextMenu from './ContextMenu.vue'
import UiIcon from './UiIcon.vue'

const props = defineProps({
  label: { type: String, required: true },
  icon: { type: String, default: '' },
  items: { type: Array, required: true },
  disabled: { type: Boolean, default: false },
  title: { type: String, default: '' },
})
const emit = defineEmits(['select'])

const btnEl = ref(null)
const open = ref(false)
const pos = ref({ x: 0, y: 0 })

function toggle() {
  if (props.disabled) return
  if (open.value) { open.value = false; return }
  const r = btnEl.value.getBoundingClientRect()
  pos.value = { x: r.left, y: r.bottom + 4 }
  open.value = true
}

function onSelect(action) {
  open.value = false
  emit('select', action)
}
</script>

<template>
  <button
    ref="btnEl"
    type="button"
    class="tbtn dropbtn"
    :class="{ open }"
    :disabled="disabled"
    :title="title || label"
    @click="toggle"
  >
    <UiIcon v-if="icon" :name="icon" :size="15" />
    <span>{{ label }}</span>
    <UiIcon name="chevron-down" :size="11" class="dropbtn-caret" />
  </button>
  <ContextMenu v-if="open" :x="pos.x" :y="pos.y" :items="items" @close="open = false" @select="onSelect" />
</template>

<style scoped>
.dropbtn.open { background: var(--bg-hover); color: var(--text-primary); }
.dropbtn-caret { opacity: 0.7; margin-left: 1px; transition: transform var(--motion-fast) var(--motion-ease); }
.dropbtn.open .dropbtn-caret { transform: rotate(180deg); }
@media (prefers-reduced-motion: reduce) { .dropbtn-caret { transition-duration: 1ms; } }
</style>
