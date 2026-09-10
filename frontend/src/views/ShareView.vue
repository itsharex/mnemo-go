<script setup>
// 分享记录页：聚合全部账号的分享历史，按网盘 + 账号分组展示。
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { ListShareHistory, OpenBrowser, onEvent, providerOf, accountName, providerIconUrl, providerMetaOf, capsOf, formatTime, copyText, importShare, saveImportedShare, cancelShare } from '../api'
import UiIcon from '../components/UiIcon.vue'
import UiSelect from '../components/UiSelect.vue'
import Modal from '../components/Modal.vue'
import SelectDirModal from '../components/SelectDirModal.vue'
import ConfirmModal from '../components/ConfirmModal.vue'
import { parseShareText } from '../workspace'

const props = defineProps({
  accounts: { type: Array, default: () => [] },
  providers: { type: Array, default: () => [] },
})
const emit = defineEmits(['toast', 'navigate'])
const importedLocation = ref(null)

const history = ref([])
const loading = ref(false)
const loadError = ref('')
const kw = ref('')
const kwRaw = ref('')   // 输入原值，kw 为防抖后值
let kwTimer = null
watch(kwRaw, (v) => {
  clearTimeout(kwTimer)
  kwTimer = setTimeout(() => { kw.value = v }, 120)
})
onBeforeUnmount(() => clearTimeout(kwTimer))
const filterProvider = ref('')
const filterAccount = ref('')
const canceling = ref(new Set())
const confirmDialog = ref(null)

// 分享导入始终绑定一个明确的目标账号，避免多账号之间串用会话或目录。
const importOpen = ref(false)
const importStep = ref('form')
const importBusy = ref(false)
const importTargetId = ref('')
const importForm = ref({ url: '', password: '' })
const importSession = ref(null)
const importSelected = ref([])
const importDirPick = ref(false)
const importDir = ref({ id: 'root', name: '根目录' })

const detectedShare = computed(() => parseShareText(importForm.value.url))
const importAccounts = computed(() => (props.accounts || []).filter((account) => capsOf(account, props.providers).importShare && (!detectedShare.value.provider || providerOf(account.user_id) === detectedShare.value.provider)))
watch(() => detectedShare.value.password, password => { if (password) importForm.value.password = password })
const importTarget = computed(() => importAccounts.value.find((account) => account.user_id === importTargetId.value) || null)
let importSeq = 0
watch(() => [importTarget.value?.user_id, importTarget.value?.drive_id], ([user, drive], [oldUser, oldDrive]) => {
  if (user === oldUser && drive === oldDrive) return
  ++importSeq
  importBusy.value = false
  importStep.value = 'form'
  importSession.value = null
  importSelected.value = []
  importDirPick.value = false
})
onBeforeUnmount(() => { ++importSeq })
const importDirRoot = computed(() => {
  const meta = providerMetaOf(importTarget.value, props.providers)
  return { id: meta.rootKey || 'root', name: meta.rootTitle || '根目录' }
})
const importAccountOptions = computed(() => importAccounts.value.map((account) => ({
  value: account.user_id,
  img: providerIconUrl(providerMetaOf(account, props.providers)),
  label: `${providerMetaOf(account, props.providers).label || providerOf(account.user_id)} · ${accountName(account)}`,
})))

function formatSize(size) {
  const value = Number(size) || 0
  if (value <= 0) return '0 B'
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB`
  return `${(value / 1024 / 1024 / 1024).toFixed(1)} GB`
}

watch(importAccounts, (list) => {
  if (!list.some((account) => account.user_id === importTargetId.value)) {
    importTargetId.value = list[0]?.user_id || ''
  }
}, { immediate: true })

function openImport() {
  importOpen.value = true
  importStep.value = 'form'
  importSession.value = null
  importSelected.value = []
  importDir.value = importDirRoot.value
}

function closeImport() {
  if (importBusy.value) return
  importOpen.value = false
  importDirPick.value = false
  importSession.value = null
  importSelected.value = []
}

async function parseImport() {
  const url = detectedShare.value.url
  if (!url || !importTarget.value || importBusy.value) return
  const seq = ++importSeq
  importBusy.value = true
  try {
    const session = await importShare(importTarget.value.user_id, importTarget.value.drive_id, url, importForm.value.password.trim())
    if (seq !== importSeq) return
    if (!session || !Array.isArray(session.files) || !session.files.length) {
      throw new Error('分享中没有可导入的文件')
    }
    importSession.value = session
    importSelected.value = []
    importDir.value = importDirRoot.value
    importStep.value = 'files'
  } catch (e) {
    if (seq !== importSeq) return
    emit('toast', String(e), 'error')
  } finally {
    if (seq === importSeq) importBusy.value = false
  }
}

function toggleImportFile(file) {
  const id = file && file.fileId
  if (!id) return
  const next = new Set(importSelected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  importSelected.value = [...next]
}

function toggleAllImportFiles() {
  const files = importSession.value?.files || []
  importSelected.value = importSelected.value.length === files.length ? [] : files.map((file) => file.fileId).filter(Boolean)
}

function backToImportForm() {
  if (importBusy.value) return
  importStep.value = 'form'
  importSession.value = null
  importSelected.value = []
}

async function saveImport() {
  if (!importTarget.value || !importSession.value || !importSelected.value.length || importBusy.value) return
  const seq = ++importSeq
  importBusy.value = true
  try {
    const saved = await saveImportedShare(
      importTarget.value.user_id,
      importTarget.value.drive_id,
      importSession.value,
      importSelected.value,
      importDir.value.id,
    )
    if (seq !== importSeq) return
    const count = Array.isArray(saved) ? saved.length : importSelected.value.length
    emit('toast', `已导入 ${count} 个项目`, 'success')
    importedLocation.value = { userId: importTarget.value.user_id, dirId: importDir.value.id, name: importDir.value.name }
    importOpen.value = false
    importDirPick.value = false
    importSession.value = null
    importSelected.value = []
  } catch (e) {
    if (seq !== importSeq) return
    emit('toast', String(e), 'error')
  } finally {
    if (seq === importSeq) importBusy.value = false
  }
}

let refreshSeq = 0
async function refresh() {
  const seq = ++refreshSeq
  loading.value = true
  loadError.value = ''
  try {
    const list = (await ListShareHistory('')) || []
    if (seq === refreshSeq) history.value = list
  } catch (e) {
    if (seq === refreshSeq) {
      loadError.value = '分享记录加载失败，请重试'
      emit('toast', String(e), 'error')
    }
  } finally {
    if (seq === refreshSeq) loading.value = false
  }
}

watch(() => props.accounts, (list) => {
  if (filterAccount.value && !list.some((a) => a.user_id === filterAccount.value)) {
    filterAccount.value = ''
  }
  if (filterProvider.value && !props.providers.some((p) => p.ID === filterProvider.value)) {
    filterProvider.value = ''
  }
}, { deep: true })

function itemKey(h, idx) {
  return h.share_id || h.share_url || `${h.account_id || ''}_${idx}`
}

function metaOf(pid) {
  const p = props.providers.find((x) => x.ID === pid)
  return (p && p.Meta) || {}
}

function labelOf(pid) {
  return metaOf(pid).label || pid || '未知网盘'
}

function accountOf(userId) {
  return props.accounts.find((a) => a.user_id === userId) || null
}

// 从记录中聚合出现过的 provider 列表
const providerOptions = computed(() => {
  const seen = new Set()
  for (const h of history.value) {
    const pid = h.provider || providerOf(h.account_id)
    if (pid) seen.add(pid)
  }
  return [...new Set([...props.accounts.map(a => providerOf(a.user_id)).filter(pid => seen.has(pid)), ...seen])]
})

// 账号筛选只列出有分享记录的账号
const accountOptions = computed(() => {
  const seen = new Map()
  for (const h of history.value) {
    if (!h.account_id || seen.has(h.account_id)) continue
    const acc = accountOf(h.account_id)
    seen.set(h.account_id, acc ? accountName(acc) : h.account_id)
  }
  const ids = [...new Set([...props.accounts.map(a => a.user_id).filter(id => seen.has(id)), ...seen.keys()])]
  return ids.map(id => ({ id, name: seen.get(id), img: providerIconUrl(providerMetaOf(accountOf(id) || { user_id: id }, props.providers)) }))
})

const filtered = computed(() => {
  const k = kw.value.trim().toLowerCase()
  return history.value.filter((h) => {
    const pid = h.provider || providerOf(h.account_id)
    if (filterProvider.value && pid !== filterProvider.value) return false
    if (filterAccount.value && h.account_id !== filterAccount.value) return false
    if (!k) return true
    return (
      String(h.share_name || '').toLowerCase().includes(k) ||
      String(h.share_url || '').toLowerCase().includes(k) ||
      String(h.share_pwd || '').toLowerCase().includes(k)
    )
  })
})

// 按 provider + account 分组
const groups = computed(() => {
  const map = new Map()
  for (const h of filtered.value) {
    const pid = h.provider || providerOf(h.account_id)
    const key = pid + '|' + h.account_id
    if (!map.has(key)) {
      const acc = accountOf(h.account_id)
      map.set(key, {
        key,
        accountId: h.account_id,
        pid,
        icon: providerIconUrl(acc ? providerMetaOf(acc, props.providers) : metaOf(pid)),
        label: labelOf(pid),
        accName: acc ? accountName(acc) : h.account_id,
        items: [],
      })
    }
    map.get(key).items.push(h)
  }
  const ranks = new Map(props.accounts.map((a, index) => [a.user_id, index]))
  return [...map.values()].sort((a, b) => (ranks.get(a.accountId) ?? Infinity) - (ranks.get(b.accountId) ?? Infinity))
})

function openLink(h) {
  if (!h.share_url) return
  OpenBrowser(h.share_url)
}

const copiedMap = ref({})

async function copy(text, tip, key) {
  const ok = await copyText(text)
  if (ok && key) {
    copiedMap.value[key] = true
    setTimeout(() => {
      const m = { ...copiedMap.value }
      delete m[key]
      copiedMap.value = m
    }, 1600)
  }
  emit('toast', ok ? tip : '复制失败', ok ? 'success' : 'error')
}

function copyAll(h, key) {
  const lines = [h.share_name || '分享文件', h.share_url || '']
  if (h.share_pwd) lines.push('提取码: ' + h.share_pwd)
  copy(lines.join('\n'), '已复制分享信息', key)
}

function canCancel(h) {
  const account = accountOf(h.account_id)
  return Boolean(account && h.drive_id && h.share_id && capsOf(account, props.providers).cancelCreatedShares)
}

function isCancelling(h, idx) {
  return canceling.value.has(itemKey(h, idx))
}

function askCancel(h, idx) {
  if (!canCancel(h) || isCancelling(h, idx)) return
  confirmDialog.value = {
    title: '取消分享',
    message: '取消后，分享链接会立即失效。',
    okText: '取消分享',
    danger: true,
    onOk: () => cancelRemote(h, idx),
  }
}

async function cancelRemote(h, idx) {
  const key = itemKey(h, idx)
  if (canceling.value.has(key)) return
  canceling.value = new Set([...canceling.value, key])
  try {
    await cancelShare(h)
    history.value = history.value.filter((entry) => {
      if (entry.account_id !== h.account_id) return true
      return h.share_id
        ? entry.share_id !== h.share_id
        : entry.share_url !== h.share_url
    })
    emit('toast', '分享已取消', 'success')
  } catch (e) {
    emit('toast', String(e), 'error')
  } finally {
    const next = new Set(canceling.value)
    next.delete(key)
    canceling.value = next
  }
}

function closeConfirm() { confirmDialog.value = null }
function handleConfirmOk() {
  const callback = confirmDialog.value?.onOk
  closeConfirm()
  if (typeof callback === 'function') callback()
}

const offs = []
onMounted(() => {
  refresh()
  offs.push(onEvent('account:changed', refresh))
  offs.push(onEvent('share:history-changed', refresh))
})
onBeforeUnmount(() => offs.forEach((off) => off && off()))
</script>

<template>
  <div class="share-page">
    <div class="share-layout">
      <!-- 工具条 -->
      <header class="share-toolbar">
        <div class="share-toolbar-main">
          <span class="search-quick-wrap">
            <span class="sq-icon"><UiIcon name="search" :size="13" /></span>
            <input class="search-quick share-search" v-model="kwRaw" placeholder="搜索名称 / 链接 / 提取码" />
            <button v-if="kwRaw" class="sq-clear" title="清空搜索" @click="kwRaw = ''"><UiIcon name="close" :size="11" /></button>
          </span>
          <UiSelect
            v-model="filterProvider"
            class="share-filter"
            :options="[{ value: '', label: '全部网盘' }, ...providerOptions.map((pid) => ({ value: pid, label: labelOf(pid), img: providerIconUrl(metaOf(pid)) }))]"
          />
          <UiSelect
            v-model="filterAccount"
            class="share-filter"
            :options="[{ value: '', label: '全部账号' }, ...accountOptions.map((a) => ({ value: a.id, label: a.name, img: a.img }))]"
          />
        </div>
        <div class="share-toolbar-actions">
          <button v-if="importedLocation" class="tbtn" @click="emit('navigate', importedLocation)">打开目录</button>
          <button class="btn primary" @click="openImport">
            <UiIcon name="download" :size="14" />导入分享
          </button>
          <button class="tbtn" :disabled="loading" @click="refresh">
            <span v-if="loading" class="spin"></span><template v-else><UiIcon name="refresh" :size="14" />刷新</template>
          </button>
        </div>
      </header>

      <section class="share-content">
        <div v-if="loadError" role="alert" class="share-empty">
          <span class="wes-title">{{ loadError }}</span>
          <button class="btn" :disabled="loading" @click="refresh">重新加载</button>
        </div>
        <!-- 骨架屏加载状态 -->
        <div v-else-if="loading && !history.length" class="skeleton-list share-skeleton">
          <div v-for="i in 4" :key="i" class="skeleton-row share-skeleton-row">
            <div class="skeleton skeleton-icon share-skeleton-icon"></div>
            <div class="share-skeleton-copy">
              <div class="skeleton" :style="{ width: (30 + (i * 11) % 40) + '%', height: '14px' }"></div>
              <div class="skeleton" style="width:50%;height:10px"></div>
            </div>
            <div class="skeleton share-skeleton-action"></div>
          </div>
        </div>
        <div v-else-if="!groups.length" class="share-empty">
          <div class="share-empty-icon"><UiIcon name="link" :size="28" /></div>
          <span class="wes-title">{{ history.length ? '没有匹配的分享记录' : '还没有分享记录' }}</span>
          <template v-if="!history.length">
            <span class="wes-desc">在「网盘」页选中文件并创建分享后，可在这里查看链接、复制提取码和管理分享。</span>
            <span class="wes-desc">收到别人的分享链接？可以直接导入到支持的网盘账号。</span>
            <button class="btn" @click="openImport"><UiIcon name="download" :size="14" />导入分享</button>
          </template>
          <template v-else>
            <span class="wes-desc">试试其他关键词，或清除筛选查看全部记录。</span>
            <button class="btn" @click="kwRaw = ''; kw = ''; filterProvider = ''; filterAccount = ''">清除筛选</button>
          </template>
        </div>

        <!-- 分组记录 -->
        <div v-else class="share-groups">
          <section v-for="g in groups" :key="g.key" class="share-group">
            <header class="share-group-header">
              <img v-if="g.icon" :src="g.icon" class="share-group-icon" alt="" />
              <span class="share-group-provider">{{ g.label }}</span>
              <span class="share-group-account">{{ g.accName }}</span>
              <span class="share-group-count">{{ g.items.length }} 条</span>
            </header>

            <div v-for="(h, idx) in g.items" :key="itemKey(h, idx)" class="share-record">
              <div class="share-record-main">
                <div class="share-record-name" :title="h.share_name">{{ h.share_name || '未命名分享' }}</div>
                <div class="share-record-meta">
                  <span>创建于 {{ formatTime(h.created_at) }}</span>
                </div>
                <div class="share-record-link" :title="h.share_url"><UiIcon name="link" :size="12" /><span>{{ h.share_url }}</span></div>
              </div>
              <div class="share-record-side">
                <span v-if="h.share_pwd" class="share-passcode" title="提取码"><UiIcon name="info" :size="11" />{{ h.share_pwd }}</span>
                <div class="share-record-actions">
                  <button class="btn-circle" title="打开分享链接" :disabled="!h.share_url" @click="openLink(h)"><UiIcon name="external" :size="14" /></button>
                  <button class="tbtn" :disabled="!h.share_url" @click="copy(h.share_url, '已复制链接', 'url_' + itemKey(h, idx))">
                    <UiIcon v-if="copiedMap['url_' + itemKey(h, idx)]" name="check" :size="13" class="icon-check-pop" />
                    <UiIcon v-else name="copy" :size="13" />
                    <span>{{ copiedMap['url_' + itemKey(h, idx)] ? '已复制' : '链接' }}</span>
                  </button>
                  <button v-if="h.share_pwd" class="tbtn" @click="copy(h.share_pwd, '已复制提取码', 'pwd_' + itemKey(h, idx))">
                    <UiIcon v-if="copiedMap['pwd_' + itemKey(h, idx)]" name="check" :size="13" class="icon-check-pop" />
                    <UiIcon v-else name="copy" :size="13" />
                    <span>{{ copiedMap['pwd_' + itemKey(h, idx)] ? '已复制' : '提取码' }}</span>
                  </button>
                  <button class="tbtn" @click="copyAll(h, 'all_' + itemKey(h, idx))">
                    <UiIcon v-if="copiedMap['all_' + itemKey(h, idx)]" name="check" :size="13" class="icon-check-pop" />
                    <UiIcon v-else name="copy" :size="13" />
                    <span>{{ copiedMap['all_' + itemKey(h, idx)] ? '已复制' : '全部' }}</span>
                  </button>
                  <button v-if="canCancel(h)" class="tbtn danger" :disabled="isCancelling(h, idx)" @click="askCancel(h, idx)">
                    <span v-if="isCancelling(h, idx)" class="spin"></span>
                    <template v-else><UiIcon name="trash" :size="13" /><span>取消</span></template>
                  </button>
                </div>
              </div>
            </div>
          </section>
        </div>
      </section>
    </div>

    <Modal v-if="importOpen" title="导入分享" width="620px" @close="closeImport">
    <div v-if="!accounts.some(a => capsOf(a, providers).importShare)" class="workspace-empty-state">
      <UiIcon name="cloud" :size="32" />
      <strong>请先添加支持分享导入的网盘账号</strong>
      <p>通过左侧「添加网盘」登录账号后，再来粘贴分享链接。导入能力取决于网盘支持情况。</p>
    </div>
    <template v-else-if="importStep === 'form'">
      <div class="field">
        <label>目标网盘账号</label>
        <UiSelect v-model="importTargetId" block :disabled="importBusy" :options="importAccountOptions" placeholder="选择账号" />
      </div>
      <div class="field">
        <label>分享链接</label>
        <textarea v-model="importForm.url" class="input" placeholder="粘贴链接或分享文案" :disabled="importBusy" rows="3" autofocus></textarea>
        <span class="hint" v-if="detectedShare.provider">已识别 {{ providers.find(p => p.ID === detectedShare.provider)?.Meta?.label || detectedShare.provider }}，仅显示兼容账号</span>
        <span v-if="!importAccounts.length" class="hint">没有兼容账号，请添加对应网盘，或更换分享链接</span>
      </div>
      <div class="field">
        <label>提取码（可选）</label>
        <input v-model="importForm.password" class="input" placeholder="没有提取码可留空" :disabled="importBusy" @keydown.enter="parseImport" />
      </div>
    </template>

    <template v-else>
      <div class="import-summary">
        <div><strong>{{ importSession?.shareUrl || importForm.url }}</strong></div>
        <span>{{ importSession?.files?.length || 0 }} 个项目 · 已选 {{ importSelected.length }} 个</span>
      </div>
      <div class="import-file-toolbar">
        <button class="tbtn xs" @click="toggleAllImportFiles">{{ importSelected.length === importSession?.files?.length ? '取消全选' : '全选' }}</button>
      </div>
      <div class="import-file-list">
        <label v-for="file in importSession.files" :key="file.fileId" class="import-file-row">
          <input type="checkbox" :checked="importSelected.includes(file.fileId)" @change="toggleImportFile(file)" />
          <UiIcon :name="file.isDir ? 'folder' : 'file'" :size="16" />
          <span class="import-file-name" :title="file.name">{{ file.name }}</span>
          <span class="import-file-size">{{ file.isDir ? '文件夹' : formatSize(file.size) }}</span>
        </label>
      </div>
      <div class="field import-target-dir">
        <label>保存到</label>
        <div class="import-dir-row">
          <input class="input" :value="importDir.name" readonly />
          <button class="btn" :disabled="importBusy || !importTarget" @click="importDirPick = true">选择目录</button>
        </div>
      </div>
    </template>

    <template #actions>
      <button class="btn" :disabled="importBusy" @click="closeImport">取消</button>
      <template v-if="importStep === 'form'">
        <button class="btn primary" :disabled="importBusy || !importForm.url.trim() || !importTarget" @click="parseImport">
          <span v-if="importBusy" class="spin spin-on-primary"></span>{{ importBusy ? '解析中…' : '解析分享' }}
        </button>
      </template>
      <template v-else>
        <button class="btn" :disabled="importBusy" @click="backToImportForm">返回</button>
        <button class="btn primary" :disabled="importBusy || !importSelected.length" @click="saveImport">
          <span v-if="importBusy" class="spin spin-on-primary"></span>{{ importBusy ? '导入中…' : '导入所选项目' }}
        </button>
      </template>
    </template>
  </Modal>

    <SelectDirModal
      v-if="importDirPick && importTarget"
      title="选择分享保存目录"
      :account="importTarget"
      :providers="providers"
      @close="importDirPick = false"
      @select="(dir) => { importDir = dir; importDirPick = false }"
      @toast="(message, type) => emit('toast', message, type)"
    />
    <ConfirmModal v-if="confirmDialog" :title="confirmDialog.title" :message="confirmDialog.message" :okText="confirmDialog.okText" :danger="confirmDialog.danger" @ok="handleConfirmOk" @cancel="closeConfirm" />
  </div>
</template>

<style scoped>
.import-summary { display:flex; flex-direction:column; gap:4px; padding:9px 10px; margin-bottom:10px; border:1px solid var(--border-lighter); border-radius:var(--radius-md); color:var(--text-secondary); font-size:12px; }
.import-summary strong { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; color:var(--text-primary); font-weight:600; }
.import-file-toolbar { display:flex; align-items:center; gap:8px; margin-bottom:6px; }
.import-file-list { max-height:260px; overflow-y:auto; border:1px solid var(--border-lighter); border-radius:var(--radius-md); padding:4px; }
.import-file-row { display:flex; align-items:center; gap:8px; min-height:34px; padding:4px 7px; border-radius:var(--radius-sm); cursor:pointer; }
.import-file-row:hover { background:var(--surface-hover); }
.import-file-name { min-width:0; flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.import-file-size { flex:0 0 auto; color:var(--text-tertiary); font-size:12px; }
.import-dir-row { display:flex; gap:6px; }
.import-dir-row .input { flex:1; }
</style>
