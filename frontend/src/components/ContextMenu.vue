<script setup>
// 通用右键/下拉菜单。items: [{ icon(UiIcon 名称), label, danger, disabled, sep, action }]
import { computed, nextTick, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import UiIcon from './UiIcon.vue'

const props = defineProps({
  x: { type: Number, required: true },
  y: { type: Number, required: true },
  items: { type: Array, required: true },
})
const emit = defineEmits(['close', 'select'])
const menuEl = ref(null)
const activeIndex = ref(0)
const size = ref({ width: 0, height: 0 })
const viewport = ref({ width: window.innerWidth, height: window.innerHeight })
let opener = null
let resizeObserver
let restoreFocus = false
let disposed = false

const actionableItems = computed(() => props.items
  .map((item, sourceIndex) => ({ item, sourceIndex }))
  .filter(({ item }) => !item.sep && !item.header && !item.disabled))

const pos = computed(() => {
  const { width: w, height: h } = size.value
  return {
    left: Math.max(8, Math.min(props.x, viewport.value.width - w - 8)) + 'px',
    top: Math.max(8, Math.min(props.y, viewport.value.height - h - 8)) + 'px',
  }
})

function pick(item) {
  if (item.disabled || item.sep || item.header) return
  restoreFocus = true
  emit('select', item.action !== undefined ? item.action : item.key)
  emit('close')
}

function measure() {
  if (disposed || !menuEl.value) return
  // offset dimensions avoid the opening transition's scale transform.
  const rect = menuEl.value.getBoundingClientRect()
  size.value = { width: menuEl.value.offsetWidth || rect.width, height: menuEl.value.offsetHeight || rect.height }
}

function focusActive() {
  nextTick(() => {
    if (disposed) return
    const sourceIndex = actionableItems.value[activeIndex.value]?.sourceIndex
    const item = sourceIndex === undefined ? menuEl.value : menuEl.value?.querySelector(`[data-menu-index="${sourceIndex}"]`)
    item?.focus({ preventScroll: true })
    item?.scrollIntoView?.({ block: 'nearest' })
  })
}

function onPointerDown(e) {
  if (!menuEl.value?.contains(e.target)) emit('close')
}
function activate(sourceIndex) {
  const index = actionableItems.value.findIndex(entry => entry.sourceIndex === sourceIndex)
  if (index >= 0) activeIndex.value = index
}
function onPointerMove(sourceIndex, event) {
  if (event.currentTarget.disabled) return
  activate(sourceIndex)
  event.currentTarget.focus({ preventScroll: true })
}
function onKey(e) {
  e.stopPropagation()
  if (e.key === 'Tab') {
    emit('close')
    return
  }
  if (e.key === 'Escape') {
    e.preventDefault()
    restoreFocus = true
    emit('close')
    return
  }
  const len = actionableItems.value.length
  if (!len) return
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    activeIndex.value = e.key === 'ArrowDown'
      ? (activeIndex.value + 1) % len
      : (activeIndex.value - 1 + len) % len
    focusActive()
  } else if (e.key === 'Home' || e.key === 'End') {
    e.preventDefault()
    activeIndex.value = e.key === 'Home' ? 0 : len - 1
    focusActive()
  } else if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    const entry = actionableItems.value[activeIndex.value]
    if (entry) pick(entry.item)
  }
}

function onBlur() { emit('close') }
function onScroll(e) {
  if (!(e.target instanceof Node) || !menuEl.value?.contains(e.target)) emit('close')
}
function onResize() {
  viewport.value = { width: window.innerWidth, height: window.innerHeight }
  measure()
}
function onFocusIn(e) {
  if (!menuEl.value?.contains(e.target)) emit('close')
}

watch(() => [props.x, props.y, props.items], () => {
  activeIndex.value = 0
  nextTick(measure)
  focusActive()
}, { deep: true })

onMounted(() => {
  opener = document.activeElement
  window.addEventListener('pointerdown', onPointerDown, true)
  window.addEventListener('blur', onBlur)
  window.addEventListener('scroll', onScroll, true)
  window.addEventListener('resize', onResize)
  window.addEventListener('focusin', onFocusIn)
  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(measure)
    resizeObserver.observe(menuEl.value)
  }
  measure()
  focusActive()
})
onBeforeUnmount(() => {
  disposed = true
  window.removeEventListener('pointerdown', onPointerDown, true)
  window.removeEventListener('blur', onBlur)
  window.removeEventListener('scroll', onScroll, true)
  window.removeEventListener('resize', onResize)
  window.removeEventListener('focusin', onFocusIn)
  resizeObserver?.disconnect()
  if (restoreFocus && menuEl.value?.contains(document.activeElement) && opener?.isConnected) opener.focus({ preventScroll: true })
})
</script>

<template>
  <teleport to="body">
    <transition name="popover-zoom">
      <div ref="menuEl" class="ctx-menu" :style="pos" role="menu" aria-label="操作菜单" tabindex="-1" @pointerdown.stop @contextmenu.prevent.stop @keydown="onKey">
        <template v-for="(item, i) in items" :key="i">
          <div v-if="item.sep" class="ctx-sep"></div>
          <div v-else-if="item.header" class="ctx-header">{{ item.header }}</div>
          <button
            v-else
            type="button"
            class="ctx-item"
            :class="{ danger: item.danger, disabled: item.disabled }"
            :data-menu-index="i"
            role="menuitem"
            :disabled="item.disabled"
            :tabindex="item.disabled ? -1 : (actionableItems.findIndex((entry) => entry.sourceIndex === i) === activeIndex ? 0 : -1)"
            @click="pick(item)"
            @focus="activate(i)"
            @pointermove="onPointerMove(i, $event)"
          >
            <span class="ci"><UiIcon v-if="item.icon" :name="item.icon" :size="15" /></span>
            <span>{{ item.label }}</span>
          </button>
        </template>
      </div>
    </transition>
  </teleport>
</template>
