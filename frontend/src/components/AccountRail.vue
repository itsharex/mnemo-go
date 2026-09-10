<script setup>
// 账号快切栏（复刻旧版 AccountRail）：默认 60px 窄图标栏，悬停展开为 220px 显示名称与用量。
import { computed, ref, onBeforeUnmount, watch } from 'vue'
import { providerOf, accountName, providerIconUrl, providerMetaOf } from '../api'
import { getPrefs, setPref } from '../appearance'
import ContextMenu from './ContextMenu.vue'
import UiIcon from './UiIcon.vue'

const props = defineProps({
  accounts: { type: Array, default: () => [] },
  providers: { type: Array, default: () => [] },
  current: { type: Object, default: null },
})
const emit = defineEmits(['select', 'add', 'remove', 'info', 'rename'])

const expanded = ref(false)
const railEl = ref(null)
const menu = ref(null)
let hovering = false
let cancelDrag = null
let clickTimer = null
let enterTimer = null
let leaveTimer = null
onBeforeUnmount(() => {
  cancelDrag?.()
  clearTimeout(enterTimer)
  clearTimeout(leaveTimer)
  clearTimeout(clickTimer)
})

// ---------- 手动拖拽排序（顺序存 localStorage prefs.accountOrder） ----------
const dragIdx = ref(-1)
const liveList = ref(null) // 拖拽中的实时顺序
const bumpMap = ref({})    // 被挤动项的碰撞果冻：user_id -> { dir, delay }
let suppressClick = false

const orderedAccounts = computed(() => {
  if (liveList.value) return liveList.value
  const order = Array.isArray(getPrefs().accountOrder) ? getPrefs().accountOrder : []
  if (!order.length) return props.accounts
  const known = [...new Set(order)]
    .map((id) => props.accounts.find((a) => a.user_id === id))
    .filter(Boolean)
  const unknown = props.accounts.filter((a) => !order.includes(a.user_id))
  return [...known, ...unknown]
})

function onItemPointerDown(e, acc) {
  if (e.button !== 0) return
  cancelDrag?.()
  clearTimeout(clickTimer)
  suppressClick = false
  menu.value = null
  const startX = e.clientX
  const startY = e.clientY
  const listEl = e.currentTarget.closest('.rail-list')
  const itemEl = e.currentTarget
  let dragging = false
  let ghost = null
  let rafId = 0
  let startRaf = 0
  let bumpRaf = 0
  let dropTimer = null
  let heights = null   // Map<user_id, height>，拖动起点捕获的各项静止高度
  let gapPx = 0
  let top0 = 0         // 首项静止 top
  let bottomLimit = 0  // 末项静止 bottom（拖动垂直范围上限）
  let ghostTop0 = 0    // 幽灵初始 top（X 始终锁定，只允许上下）
  let ghostY = 0       // 弹簧当前位置（top）
  let dropping = false
  let px = e.clientX
  let py = e.clientY

  // 持续 rAF 循环：幽灵以弹簧插值跟随指针（灵动 lag），并做槽位换位；
  // 指针停下后弹簧仍收敛到目标，不依赖 pointermove 频率
  const frame = () => {
    rafId = 0
    if (!ghost || dropping) return
    const desired = Math.min(Math.max(ghostTop0 + (py - startY), top0), bottomLimit - ghost.offsetHeight)
    ghostY += (desired - ghostY) * 0.32
    ghost.style.transform = `translate3d(0, ${ghostY - ghostTop0}px, 0) scale(1.04)`
    if (heights && liveList.value) {
      const target = slotAt(ghostY + ghost.offsetHeight / 2)
      const cur = dragIdx.value
      if (target !== cur && cur >= 0) {
        const before = liveList.value
        const beforeIdx = new Map(before.map((a, i) => [a.user_id, i]))
        const list = [...before]
        const [moved] = list.splice(cur, 1)
        list.splice(target, 0, moved)
        liveList.value = list
        dragIdx.value = target
        // 碰撞果冻：被挤动的项朝移动方向“被顶一下”（压缩-回弹），
        // 延迟与 FLIP 波纹一致；清空再赋值以保证同向连续挤动时动画能重新播放
        const bumps = {}
        list.forEach((a, ni) => {
          if (a.user_id === moved.user_id) return
          const oi = beforeIdx.get(a.user_id)
          if (oi !== ni) {
            bumps[a.user_id] = { dir: ni > oi ? 'down' : 'up', delay: Math.min(Math.abs(ni - target) * 35, 140) }
          }
        })
        bumpMap.value = {}
        cancelAnimationFrame(bumpRaf)
        bumpRaf = requestAnimationFrame(() => { bumpMap.value = bumps })
      }
    }
    // 弹簧未收敛或拖动中：继续下一帧
    if (dragging && !dropping) rafId = requestAnimationFrame(frame)
  }

  // 槽位 = 指针落入哪一项的区间；边界由捕获的各项高度按当前顺序累加，
  // 支持各项高度不一致（展开态有无用量条），且与动画中的 DOM 无关
  const slotAt = (y) => {
    const list = liveList.value
    let top = top0
    for (let i = 0; i < list.length; i++) {
      const h = heights.get(list[i].user_id) || 0
      if (y < top + h + gapPx) return i
      top += h + gapPx
    }
    return list.length - 1
  }

  // 拖动起点：冻结一切展开/收起过渡并等一帧让布局静止，再捕获几何与克隆
  const startDrag = () => {
    if (!dragging) return
    const items = [...listEl.querySelectorAll('.rail-item')]
    if (!items.length) return
    heights = new Map()
    items.forEach((el, i) => heights.set(liveList.value[i].user_id, el.getBoundingClientRect().height))
    gapPx = parseFloat(getComputedStyle(listEl).rowGap) || 0
    top0 = items[0].getBoundingClientRect().top
    bottomLimit = items[items.length - 1].getBoundingClientRect().bottom
    const r = itemEl.getBoundingClientRect()
    bumpMap.value = {}
    ghostTop0 = r.top
    ghostY = r.top
    ghost = itemEl.cloneNode(true)
    ghost.tabIndex = -1
    ghost.setAttribute('aria-hidden', 'true')
    ghost.className = itemEl.className.replace('dragging', '').trim() + ' rail-ghost'
    ghost.style.width = r.width + 'px'
    ghost.style.left = r.left + 'px'
    ghost.style.top = r.top + 'px'
    listEl.closest('.account-rail').appendChild(ghost)
    if (!rafId) rafId = requestAnimationFrame(frame)
  }

  // 松手落位：幽灵以带回弹的过渡弹入目标槽位，落点即真实项位置，随后无缝替换
  const dropGhost = () => {
    if (!ghost) return
    dropping = true
    let targetTop = top0
    const list = liveList.value || []
    for (let i = 0; i < dragIdx.value && i < list.length; i++) {
      targetTop += (heights?.get(list[i].user_id) || 0) + gapPx
    }
    ghost.classList.add('dropping')
    ghost.style.transform = `translate3d(0, ${targetTop - ghostTop0}px, 0) scale(1)`
    dropTimer = setTimeout(() => {
      if (ghost) { ghost.remove(); ghost = null }
      listEl.closest('.account-rail')?.classList.remove('rail-frozen')
      liveList.value = null
      dragIdx.value = -1
      heights = null
      cancelDrag = null
      if (!hovering) onRailLeave()
    }, 260)
  }

  const onMove = (ev) => {
    if (ev.pointerId !== undefined && e.pointerId !== undefined && ev.pointerId !== e.pointerId) return
    px = ev.clientX
    py = ev.clientY
    if (!dragging) {
      if (Math.abs(py - startY) < 6 || Math.abs(py - startY) < Math.abs(px - startX)) return
      dragging = true
      dragActive = true
      suppressClick = true
      clearTimeout(enterTimer)
      clearTimeout(leaveTimer)
      liveList.value = [...orderedAccounts.value]
      dragIdx.value = liveList.value.findIndex((a) => a.user_id === acc.user_id)
      // 冻结栏宽/项高过渡（进行中的展开动画立即到位），下一帧捕获静止几何
      listEl.closest('.account-rail').classList.add('rail-frozen')
      document.body.classList.add('rail-drag-active')
      startRaf = requestAnimationFrame(startDrag)
    }
  }
  const onUp = (ev) => {
    if (ev?.pointerId !== undefined && e.pointerId !== undefined && ev.pointerId !== e.pointerId) return
    const commit = ev?.type === 'pointerup'
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
    window.removeEventListener('pointercancel', onUp)
    window.removeEventListener('blur', onUp)
    window.removeEventListener('keydown', onDragKey)
    cancelAnimationFrame(startRaf)
    cancelAnimationFrame(bumpRaf)
    clearTimeout(dropTimer)
    if (rafId) { cancelAnimationFrame(rafId); rafId = 0 }
    if (commit && dragging && liveList.value) {
      setPref('accountOrder', liveList.value.map((a) => a.user_id))
      // click 紧跟 pointerup 触发，延后一帧清除以吞掉这次拖拽点击
      clickTimer = setTimeout(() => { suppressClick = false }, 0)
    }
    if (!commit) {
      ghost?.remove()
      ghost = null
      suppressClick = false
    }
    if (ghost) dropGhost()
    else listEl.closest('.account-rail')?.classList.remove('rail-frozen')
    document.body.classList.remove('rail-drag-active')
    dragActive = false
    if (!ghost) {
      liveList.value = null
      dragIdx.value = -1
      heights = null
      cancelDrag = null
    }
    dragging = false
    bumpMap.value = {}
  }
  const onDragKey = (ev) => {
    if (ev.key !== 'Escape') return
    ev.preventDefault()
    onUp()
  }
  cancelDrag = () => onUp()
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
  window.addEventListener('pointercancel', onUp)
  window.addEventListener('blur', onUp)
  window.addEventListener('keydown', onDragKey)
}

function onItemClick(acc) {
  if (suppressClick) { suppressClick = false; return }
  menu.value = null
  if (props.current?.user_id === acc.user_id) return
  emit('select', acc)
}

let dragActive = false

// 悬停快速平滑展开，移出后延迟收起；拖拽期间冻结展开状态，避免中途布局突变
function onRailEnter() {
  hovering = true
  if (dragActive) return
  clearTimeout(leaveTimer)
  clearTimeout(enterTimer)
  enterTimer = setTimeout(() => { expanded.value = true }, 220)
}
function onRailLeave() {
  hovering = false
  scheduleCollapse()
}
function scheduleCollapse() {
  if (dragActive || menu.value) return
  clearTimeout(enterTimer)
  clearTimeout(leaveTimer)
  leaveTimer = setTimeout(() => {
    if (!hovering && !menu.value && !railEl.value?.contains(document.activeElement)) expanded.value = false
  }, 200)
}

watch(menu, (value) => {
  if (!value && !hovering) onRailLeave()
})
watch(() => props.accounts.map(a => a.user_id).join('\n'), () => {
  cancelDrag?.()
  if (menu.value && !props.accounts.some(a => a.user_id === menu.value.acc.user_id)) menu.value = null
})

function onRailKey(e, acc) {
  if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) {
    e.preventDefault()
    onCtx(e, acc)
    return
  }
  const buttons = [...railEl.value.querySelectorAll('.rail-item, .rail-add')]
  const index = buttons.indexOf(e.currentTarget)
  let next
  if (e.key === 'ArrowDown') next = (index + 1) % buttons.length
  else if (e.key === 'ArrowUp') next = (index - 1 + buttons.length) % buttons.length
  else if (e.key === 'Home') next = 0
  else if (e.key === 'End') next = buttons.length - 1
  else return
  e.preventDefault()
  buttons[next]?.focus()
}

const groups = computed(() => {
  const map = new Map()
  for (const acc of props.accounts) {
    const pid = providerOf(acc.user_id)
    if (!map.has(pid)) map.set(pid, [])
    map.get(pid).push(acc)
  }
  const order = props.providers.map((p) => p.ID)
  return [...map.entries()]
    .sort((a, b) => {
      const ia = order.indexOf(a[0]), ib = order.indexOf(b[0])
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
    })
    .map(([pid, list]) => {
      const p = props.providers.find((x) => x.ID === pid)
      return { pid, label: p ? p.Meta.label : pid, list }
    })
})

function quotaPct(acc) {
  const u = acc.usage
  if (!u || !u.size) return 0
  return Math.min(100, Math.round((u.used / u.size) * 100))
}
function hasQuota(acc) { return acc.usage && acc.usage.size > 0 }

function iconOfAcc(acc) {
  const meta = providerMetaOf(acc, props.providers)
  return providerIconUrl(meta)
}

function labelOfAcc(acc) {
  const pid = providerOf(acc.user_id)
  const p = props.providers.find((x) => x.ID === pid)
  return p ? p.Meta.label : pid
}

function onCtx(e, acc) {
  if (!acc) return
  cancelDrag?.()
  clearTimeout(enterTimer)
  clearTimeout(leaveTimer)
  e.currentTarget.focus({ preventScroll: true })
  const rect = e.currentTarget.getBoundingClientRect()
  menu.value = { x: e.clientX || rect.right, y: e.clientY || rect.top, acc }
}

const menuItems = computed(() => {
  const acc = menu.value?.acc
  if (!acc) return []
  return [
    { icon: 'info', label: '账号信息', action: 'info' },
    { icon: 'camera', label: '自定义截图', action: 'rename' },
    { icon: 'trash', label: '移除账号', danger: true, action: 'remove' },
  ]
})

function onMenu(action) {
  if (action === 'remove') emit('remove', menu.value.acc)
  else if (action === 'info') emit('info', menu.value.acc)
  else if (action === 'rename') emit('rename', menu.value.acc)
}
</script>

<template>
  <aside
    ref="railEl"
    class="account-rail"
    :class="{ expanded }"
    @mouseenter="onRailEnter"
    @mouseleave="onRailLeave"
    @focusout="scheduleCollapse"
  >
    <TransitionGroup name="rail" tag="div" class="rail-list" :class="{ reordering: dragIdx >= 0 }">
      <button
        v-for="(acc, i) in orderedAccounts"
        :key="acc.user_id"
        type="button"
        class="rail-item"
        :class="{ active: current && current.user_id === acc.user_id, dragging: dragIdx === i, ['bump-' + (bumpMap[acc.user_id] || {}).dir]: bumpMap[acc.user_id] }"
        :style="dragIdx >= 0 && dragIdx !== i ? { transitionDelay: Math.min(Math.abs(i - dragIdx) * 35, 140) + 'ms' } : null"
        :title="`${labelOfAcc(acc)} · ${accountName(acc)}`"
        :aria-label="`${labelOfAcc(acc)} · ${accountName(acc)}`"
        :aria-current="current?.user_id === acc.user_id ? 'true' : undefined"
        aria-haspopup="menu"
        :aria-expanded="menu?.acc.user_id === acc.user_id"
        @pointerdown="onItemPointerDown($event, acc)"
        @click="onItemClick(acc)"
        @contextmenu.prevent="onCtx($event, acc)"
        @keydown="onRailKey($event, acc)"
      >
        <span class="rail-inner" :style="bumpMap[acc.user_id] ? { animationDelay: bumpMap[acc.user_id].delay + 'ms' } : null">
          <span class="rail-icon">
            <img v-if="iconOfAcc(acc)" :src="iconOfAcc(acc)" alt="" />
            <span v-else class="rail-fallback">{{ (accountName(acc) || '?')[0].toUpperCase() }}</span>
          </span>
          <span class="rail-meta">
            <span class="rail-name">{{ accountName(acc) }}</span>
            <span class="rail-sub" v-if="hasQuota(acc)">{{ acc.usage.usedStr }} / {{ acc.usage.sizeStr }}</span>
            <span class="rail-sub" v-else>{{ labelOfAcc(acc) }}</span>
            <span v-if="hasQuota(acc)" class="rail-quota"><i :style="{ width: quotaPct(acc) + '%' }"></i></span>
          </span>
        </span>
      </button>
      <div v-if="!accounts.length" class="rail-empty" key="__empty">
        <span v-show="expanded">尚未登录网盘账号</span>
      </div>
    </TransitionGroup>

    <button type="button" class="rail-add" :title="'添加网盘账号'" @click="emit('add')" @keydown="onRailKey($event)">
      <UiIcon name="plus" :size="17" class="rail-add-icon" />
      <span class="rail-add-text">添加网盘</span>
    </button>

    <ContextMenu
      v-if="menu"
      :x="menu.x"
      :y="menu.y"
      :items="menuItems"
      @close="menu = null"
      @select="onMenu"
    />
  </aside>
</template>
