<script setup>
// 全局快捷命令面板 (Ctrl+P / Command+P)：
// 支持模块跳转、网盘账号快切、快捷命令（刷新/上传/新建文件夹/明暗切换）
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { motion, MotionConfig } from 'motion-v'
import { accountName, providerIconUrl, providerMetaOf, providerOf } from '../api'
import UiIcon from './UiIcon.vue'
import { panelReveal, reducedMotion } from '../motion/presets'

const props = defineProps({
  show: { type: Boolean, default: false },
  accounts: { type: Array, default: () => [] },
  providers: { type: Array, default: () => [] },
  currentAccount: { type: Object, default: null },
})
const emit = defineEmits(['close', 'select-tab', 'select-account', 'action'])

const keyword = ref('')
const activeIndex = ref(0)
const inputEl = ref(null)
const listEl = ref(null)

const baseItems = computed(() => {
  const items = [
    // 页面跳转
    { id: 'tab-pan', group: '导航', title: '打开网盘', sub: '文件浏览与管理', icon: 'drive', run: () => emit('select-tab', 'pan') },
    { id: 'tab-transfer', group: '导航', title: '打开传输', sub: '下载与上传队列', icon: 'download', run: () => emit('select-tab', 'transfer') },
    { id: 'tab-sync', group: '导航', title: '打开同步', sub: '本地与网盘文件夹同步', icon: 'refresh', run: () => emit('select-tab', 'sync') },
    { id: 'tab-share', group: '导航', title: '打开分享', sub: '分享历史与外链管理', icon: 'share', run: () => emit('select-tab', 'share') },
    { id: 'tab-settings', group: '导航', title: '打开设置', sub: '通用、网络与播放器设置', icon: 'settings', run: () => emit('select-tab', 'settings') },

    // 常用命令
    { id: 'cmd-theme', group: '命令', title: '切换深色/浅色主题', sub: '即时生效', icon: 'sun', run: () => emit('action', 'toggle-theme') },
    { id: 'cmd-refresh', group: '命令', title: '刷新当前网盘目录', sub: '重新加载文件列表 (F5)', icon: 'refresh', run: () => emit('action', 'refresh') },
    { id: 'cmd-mkdir', group: '命令', title: '新建文件夹', sub: '在当前目录下创建 (Ctrl+Shift+N)', icon: 'plus', run: () => emit('action', 'mkdir') },
    { id: 'cmd-upload', group: '命令', title: '上传文件/文件夹', sub: '选择本地文件上传 (Ctrl+U)', icon: 'upload', run: () => emit('action', 'upload') },
  ]

  // 网盘账号
  for (const acc of props.accounts) {
    const meta = providerMetaOf(acc, props.providers)
    const label = (meta && meta.label) || providerOf(acc.user_id) || '网盘'
    items.push({
      id: 'acc-' + acc.user_id,
      group: '网盘账号',
      title: '切换到: ' + accountName(acc),
      sub: label + ' · ' + acc.user_id,
      icon: 'cloud',
      img: providerIconUrl(meta),
      run: () => {
        emit('select-account', acc)
        emit('select-tab', 'pan')
      },
    })
  }

  return items
})

const filteredItems = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  if (!kw) return baseItems.value
  return baseItems.value.filter(
    (item) =>
      item.title.toLowerCase().includes(kw) ||
      item.sub.toLowerCase().includes(kw) ||
      item.group.toLowerCase().includes(kw)
  )
})

watch(filteredItems, () => {
  activeIndex.value = 0
})

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      keyword.value = ''
      activeIndex.value = 0
      nextTick(() => {
        inputEl.value?.focus()
      })
    }
  }
)

function execute(item) {
  if (!item) return
  emit('close')
  item.run()
}

function onKeyDown(e) {
  const len = filteredItems.value.length
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (len > 0) {
      activeIndex.value = (activeIndex.value + 1) % len
      scrollActiveIntoView()
    }
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (len > 0) {
      activeIndex.value = (activeIndex.value - 1 + len) % len
      scrollActiveIntoView()
    }
  } else if (e.key === 'Enter') {
    e.preventDefault()
    if (len > 0 && filteredItems.value[activeIndex.value]) {
      execute(filteredItems.value[activeIndex.value])
    }
  } else if (e.key === 'Escape') {
    e.preventDefault()
    emit('close')
  }
}

function scrollActiveIntoView() {
  nextTick(() => {
    const list = listEl.value
    if (!list) return
    const active = list.querySelector('.qo-item.active')
    if (active) {
      active.scrollIntoView({ block: 'nearest' })
    }
  })
}
</script>

<template>
  <teleport to="body">
    <transition name="modal-fade">
      <div v-if="show" class="modal-mask qo-mask" @click.self="emit('close')">
        <MotionConfig :reduced-motion="reducedMotion">
          <motion.div
            class="qo-panel"
            role="dialog"
            aria-modal="true"
            aria-label="快捷命令面板"
            :initial="panelReveal.initial"
            :animate="panelReveal.animate"
            :transition="panelReveal.transition"
          >
          <div class="qo-input-wrap">
            <UiIcon name="search" :size="16" class="qo-search-icon" />
            <input
              ref="inputEl"
              v-model="keyword"
              class="qo-input"
              placeholder="输入命令、功能、或账号名称 (↑↓ 选择，Enter 确认)"
              spellcheck="false"
              @keydown="onKeyDown"
            />
            <span class="qo-badge">Ctrl+P</span>
          </div>

          <div ref="listEl" class="qo-list" role="listbox" aria-label="快捷命令">
            <div v-if="!filteredItems.length" class="qo-empty">
              <UiIcon name="search" :size="20" />
              <span>没有匹配的命令或账号</span>
            </div>
            <div
              v-for="(item, idx) in filteredItems"
              :key="item.id"
              class="qo-item"
              :class="{ active: idx === activeIndex }"
              role="option"
              :aria-selected="idx === activeIndex"
              @click="execute(item)"
              @mouseenter="activeIndex = idx"
            >
              <img v-if="item.img" :src="item.img" class="qo-item-img" alt="" />
              <UiIcon v-else :name="item.icon" :size="16" class="qo-item-icon" />
              <div class="qo-item-text">
                <span class="qo-item-title">{{ item.title }}</span>
                <span class="qo-item-sub">{{ item.sub }}</span>
              </div>
              <span class="qo-item-group">{{ item.group }}</span>
            </div>
          </div>
          </motion.div>
        </MotionConfig>
      </div>
    </transition>
  </teleport>
</template>

<style scoped>
.qo-mask {
  align-items: flex-start;
  padding-top: 12vh;
}
.qo-panel {
  width: 570px;
  max-width: 92vw;
  background: var(--bg-elevated);
  border: 1px solid var(--border-light);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-modal);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.qo-input-wrap {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 58px;
  padding: 12px 17px;
  border-bottom: 1px solid var(--border-light);
  background: var(--bg-surface);
}
.qo-search-icon { color: var(--color-primary); flex-shrink: 0; }
.qo-input {
  flex: 1;
  min-width: 0;
  border: none;
  background: transparent;
  font-size: 14.5px;
  color: var(--text-primary);
  outline: none;
  font-family: inherit;
}
.qo-input::placeholder { color: var(--control-placeholder); }
.qo-badge {
  font-size: 10.5px;
  font-weight: 650;
  padding: 3px 7px;
  border-radius: var(--radius-sm);
  background: var(--bg-subtle);
  color: var(--text-tertiary);
  border: 1px solid var(--border-lighter);
  flex-shrink: 0;
}
.qo-list {
  max-height: min(400px, 55vh);
  overflow-y: auto;
  padding: 8px;
}
.qo-empty {
  display: grid;
  justify-items: center;
  gap: 9px;
  padding: 40px 32px;
  text-align: center;
  font-size: 13px;
  color: var(--text-tertiary);
}
.qo-item {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 54px;
  padding: 9px 11px;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color var(--motion-fast) var(--motion-ease), box-shadow var(--motion-fast) var(--motion-ease), transform var(--motion-fast) var(--motion-spring);
}
.qo-item:hover { background: var(--bg-hover); transform: translateX(2px); }
.qo-item.active {
  background: var(--listselectbg);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-primary) 20%, transparent);
}
.qo-item-icon { color: var(--color-primary); flex-shrink: 0; }
.qo-item-img { width: 18px; height: 18px; object-fit: contain; flex-shrink: 0; }
.qo-item-text {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.qo-item-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.qo-item.active .qo-item-title { color: var(--color-primary); font-weight: 600; }
.qo-item-sub {
  font-size: 11.5px;
  color: var(--text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.qo-item-group {
  font-size: 10.5px;
  font-weight: 600;
  color: var(--text-tertiary);
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--bg-subtle);
  flex-shrink: 0;
}
</style>
