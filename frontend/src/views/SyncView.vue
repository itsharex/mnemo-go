<script setup>
// 文件夹同步页：本地文件夹与网盘目录的双向/单向同步任务管理。
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { ListSyncConfigs, SaveSyncConfig, DeleteSyncConfig, RunSync, CancelSync, ListRunningSyncIDs, onEvent, accountName, PickDirectory } from '../api'
import Modal from '../components/Modal.vue'
import SegTabs from '../components/SegTabs.vue'
import SelectDirModal from '../components/SelectDirModal.vue'
import UiIcon from '../components/UiIcon.vue'
import UiSelect from '../components/UiSelect.vue'
import ConfirmModal from '../components/ConfirmModal.vue'

const props = defineProps({
  account: { type: Object, default: null },
  accounts: { type: Array, default: () => [] },
  providers: { type: Array, default: () => [] },
})
const emit = defineEmits(['toast'])

const confirmDialog = ref(null)

const jobs = ref([])
const progress = ref({}) // id -> { done, total }
const running = ref(new Set())
let syncStateVersion = 0

const showEdit = ref(false)
const showDirPick = ref(false)
const editingId = ref('')
let editVersion = 0
const form = ref(emptyForm())

const dirOptions = [
  { key: 'two-way', label: '双向同步' },
  { key: 'push', label: '仅上传' },
  { key: 'pull', label: '仅下载' },
]

const dirLabel = { 'two-way': '双向', push: '仅上传', pull: '仅下载' }
const dirArrow = { 'two-way': '⇄', push: '→', pull: '←' }

function emptyForm() {
  return { name: '', local_dir: '', user_id: '', drive_id: '', remote_dir: 'root', remote_name: '根目录', direction: 'two-way', intervalMin: 0, deletePropagation: false }
}

let refreshSeq = 0
async function refresh() {
  const seq = ++refreshSeq
  try {
    const list = await ListSyncConfigs()
    if (seq === refreshSeq) jobs.value = list || []
  } catch (e) {
    if (seq === refreshSeq) emit('toast', String(e), 'error')
  }
}

function accountOf(job) {
  return props.accounts.find((a) => a.user_id === job.user_id) || null
}

const formAccount = computed(() => props.accounts.find((a) => a.user_id === form.value.user_id) || null)

function openCreate() {
  ++editVersion
  editingId.value = ''
  form.value = emptyForm()
  if (props.account) {
    form.value.user_id = props.account.user_id
    form.value.drive_id = props.account.drive_id
  } else if (props.accounts.length) {
    form.value.user_id = props.accounts[0].user_id
    form.value.drive_id = props.accounts[0].drive_id
  }
  showEdit.value = true
}

function openEdit(job) {
  ++editVersion
  editingId.value = job.id
  form.value = {
    name: job.name || '',
    local_dir: job.local_dir || '',
    user_id: job.user_id || '',
    drive_id: job.drive_id || '',
    remote_dir: job.remote_dir || 'root',
    remote_name: job.remote_name || (job.remote_dir && job.remote_dir !== 'root' ? job.remote_dir : '根目录'),
    direction: job.direction || 'two-way',
    intervalMin: Number(job.intervalMin) || 0,
    deletePropagation: !!job.deletePropagation,
  }
  showEdit.value = true
}

function onFormAccount() {
  const acc = formAccount.value
  if (acc) {
    form.value.drive_id = acc.drive_id
    form.value.remote_dir = 'root'
    form.value.remote_name = '根目录'
  }
}

// 选择本地同步文件夹（原生目录选择对话框）
async function pickLocalDir() {
  let dir
  try { dir = await PickDirectory('选择本地文件夹', form.value.local_dir || '') } catch { return }
  if (dir) form.value.local_dir = dir
}

const savingEdit = ref(false)
async function save() {
  if (savingEdit.value) return
  if (!form.value.name.trim()) { emit('toast', '请填写任务名称', 'error'); return }
  if (!form.value.local_dir.trim()) { emit('toast', '请填写本地文件夹路径', 'error'); return }
  if (!formAccount.value) { emit('toast', '请选择绑定账号', 'error'); return }
  savingEdit.value = true
  const version = editVersion
  const id = editingId.value || 'sync-' + Date.now() + '-' + Math.random().toString(36).slice(2, 8)
  try {
    await SaveSyncConfig({
      id,
      name: form.value.name.trim(),
      user_id: form.value.user_id,
      drive_id: form.value.drive_id,
      local_dir: form.value.local_dir.trim(),
      remote_dir: form.value.remote_dir || 'root',
      remote_name: form.value.remote_name || '',
       direction: form.value.direction,
       enabled: editingId.value ? (jobs.value.find((j) => j.id === editingId.value) || {}).enabled !== false : true,
       intervalMin: Math.max(0, Math.floor(Number(form.value.intervalMin) || 0)),
       deletePropagation: !!form.value.deletePropagation,
    })
    if (version === editVersion) showEdit.value = false
    refresh()
    emit('toast', '已保存同步任务', 'success')
  } catch (e) { emit('toast', String(e), 'error') }
  finally { savingEdit.value = false }
}

async function toggle(job) {
  if (running.value.has(job.id)) return
  syncStateVersion++
  running.value = new Set([...running.value, job.id])
  const next = { ...job, enabled: !job.enabled }
  try {
    await SaveSyncConfig(next)
    job.enabled = next.enabled
  } catch (e) { emit('toast', String(e), 'error') }
  finally {
    syncStateVersion++
    const s = new Set(running.value); s.delete(job.id); running.value = s
  }
}

async function run(job) {
  if (running.value.has(job.id)) return
  syncStateVersion++
  running.value = new Set([...running.value, job.id])
  emit('toast', `开始同步「${job.name}」…`, 'success')
  try {
    await RunSync(job.id)
    emit('toast', `「${job.name}」同步完成`, 'success')
  } catch (e) {
    const message = String(e)
    if (/cancel|取消|停止/i.test(message)) emit('toast', `「${job.name}」已停止`, 'info')
    else emit('toast', message, 'error')
  }
  syncStateVersion++
  const s = new Set(running.value)
  s.delete(job.id)
  running.value = s
  const p = { ...progress.value }
  delete p[job.id]
  progress.value = p
}

async function cancel(job) {
  try {
    const accepted = await CancelSync(job.id)
    if (accepted) emit('toast', `正在停止「${job.name}」…`, 'info')
    else emit('toast', '任务已经结束', 'info')
  } catch (e) {
    emit('toast', String(e), 'error')
  }
}

function remove(job) {
  if (running.value.has(job.id)) {
    emit('toast', '任务正在同步中，请先等待完成或停止', 'warn')
    return
  }
  confirmDialog.value = { message: `删除同步任务「${job.name}」？本地与网盘文件不受影响。`, onOk: async () => {
    confirmDialog.value = null
    try {
      await DeleteSyncConfig(job.id)
      refresh()
      emit('toast', '已删除同步任务', 'success')
    } catch (e) { emit('toast', String(e), 'error') }
  }, danger: true, title: '删除同步任务' }
}

function pct(id) {
  const p = progress.value[id]
  if (!p || !p.total) return 0
  return Math.min(100, Math.round((p.done / p.total) * 100))
}

const offs = []
onMounted(() => {
  refresh()
  offs.push(onEvent('sync:progress', (ev) => {
    if (!ev || !ev.id) return
    progress.value = { ...progress.value, [ev.id]: { done: ev.done || 0, total: ev.total || 0 } }
  }))
  offs.push(onEvent('account:changed', refresh))
  offs.push(onEvent('sync:state', (ev) => {
    if (!ev || !ev.id) return
    syncStateVersion++
    const next = new Set(running.value)
    if (ev.running) next.add(ev.id)
    else next.delete(ev.id)
    running.value = next
    if (!ev.running) {
      const nextProgress = { ...progress.value }
      delete nextProgress[ev.id]
      progress.value = nextProgress
    }
  }))
  const requestedAtVersion = syncStateVersion
  ListRunningSyncIDs().then((ids) => {
    if (syncStateVersion === requestedAtVersion) running.value = new Set(ids || [])
  }).catch(() => {})
})
onBeforeUnmount(() => {
  ++refreshSeq
  ++syncStateVersion
  offs.forEach((off) => off && off())
})
</script>

<template>
  <div class="syncpage">
    <header class="syncpage-head">
      <div class="syncpage-title">
        <UiIcon name="refresh" :size="18" />
        <strong>文件夹同步</strong>
        <span class="syncpage-sub">本地文件夹与网盘文件夹保持同步</span>
      </div>
      <button class="btn primary sm" :disabled="!accounts.length" @click="openCreate">
        <UiIcon name="plus" :size="13" /> 新建同步任务
      </button>
    </header>

    <div v-if="!jobs.length" class="workspace-empty-state" style="flex:1">
      <UiIcon name="refresh" :size="40" style="opacity:.4" />
      <span class="wes-title">还没有同步任务</span>
      <span class="wes-desc">新建一个任务，把本地文件夹和网盘文件夹关联起来</span>
    </div>

    <div v-else class="syncpage-list">
      <div v-for="job in jobs" :key="job.id" class="sync-task" :class="{ disabled: !job.enabled }">
        <div class="sync-task-main">
          <div class="sync-task-name">
            <strong>{{ job.name }}</strong>
            <span class="sync-task-direction">{{ dirLabel[job.direction] || job.direction }}</span>
            <span v-if="!accountOf(job)" class="sync-task-warn">绑定账号不可用</span>
          </div>
          <div class="sync-task-paths">
            <span class="sync-path" :title="job.local_dir">本地：{{ job.local_dir }}</span>
            <span style="color:var(--text-tertiary);flex-shrink:0">{{ dirArrow[job.direction] || '⇄' }}</span>
            <span class="sync-path" :title="job.remote_name || job.remote_dir">网盘：{{ accountOf(job) ? accountName(accountOf(job)) + ' / ' : '' }}{{ job.remote_name || (job.remote_dir === 'root' ? '根目录' : job.remote_dir) }}</span>
          </div>
          <div v-if="running.has(job.id) && progress[job.id]" class="sync-task-progress">
            <div class="progress-total">
              <div class="progress-current active" :style="{ width: pct(job.id) + '%' }"></div>
            </div>
            <div class="sync-task-meta" style="margin-top:4px">
              <span>{{ progress[job.id].done || 0 }} / {{ progress[job.id].total || 0 }}（{{ pct(job.id) }}%）</span>
            </div>
          </div>
        </div>
        <div class="sync-task-actions">
          <div class="switch" :class="{ on: job.enabled }" :title="job.enabled ? '点击停用' : '点击启用'" @click="toggle(job)"></div>
          <button class="btn-circle" :disabled="!running.has(job.id) && !accountOf(job)" :title="running.has(job.id) ? '停止同步' : '立即同步'" @click="running.has(job.id) ? cancel(job) : run(job)">
            <UiIcon :name="running.has(job.id) ? 'close' : 'play'" :size="14" />
          </button>
          <button class="btn-circle" title="编辑" @click="openEdit(job)"><UiIcon name="pencil" :size="14" /></button>
          <button class="btn-circle" title="删除" style="color:var(--color-error)" @click="remove(job)"><UiIcon name="trash" :size="14" /></button>
        </div>
      </div>
    </div>

    <Modal v-if="showEdit" :title="editingId ? '编辑同步任务' : '新建同步任务'" width="480px" @close="showEdit = false">
      <div class="field">
        <label>任务名称</label>
        <input class="input" style="width:100%" v-model="form.name" placeholder="如：工作文档同步" />
      </div>
      <div class="field">
        <label>本地文件夹</label>
        <div style="display:flex;gap:8px">
          <input class="input" style="flex:1" v-model="form.local_dir" placeholder="选择本地文件夹" />
          <button class="btn sm" @click="pickLocalDir">选择目录</button>
        </div>
      </div>
      <div class="field">
        <label>绑定账号</label>
        <UiSelect
          v-model="form.user_id"
          block
          placeholder="请选择账号"
          :options="accounts.map((a) => ({ value: a.user_id, label: accountName(a) }))"
          @change="onFormAccount"
        />
      </div>
      <div class="field">
        <label>网盘目录</label>
        <div style="display:flex;gap:8px">
          <input class="input" style="flex:1" :value="form.remote_name" readonly placeholder="根目录" />
          <button class="btn sm" :disabled="!formAccount" @click="showDirPick = true">选择目录</button>
        </div>
      </div>
      <div class="field">
        <label>同步方向</label>
        <SegTabs v-model="form.direction" :options="dirOptions" />
      </div>
      <div class="field">
        <label>自动同步周期（分钟，0 为关闭）</label>
        <input class="input" type="number" min="0" step="1" v-model.number="form.intervalMin" placeholder="0" />
      </div>
      <div class="field switch-row">
        <label>删除传播</label>
        <div class="switch" :class="{ on: form.deletePropagation }" role="switch" :aria-checked="form.deletePropagation" @click="form.deletePropagation = !form.deletePropagation"></div>
        <span class="hint">启用后会同步删除两端缺失的文件</span>
      </div>
      <template #actions>
        <button class="btn" :disabled="savingEdit" @click="showEdit = false">取消</button>
        <button class="btn primary" :disabled="savingEdit" @click="save">
          <span v-if="savingEdit" class="spin spin-on-primary"></span>
          <span>{{ savingEdit ? '保存中…' : '保存' }}</span>
        </button>
      </template>
    </Modal>

    <SelectDirModal
      v-if="showDirPick && formAccount"
      title="选择网盘目录"
      :account="formAccount"
      :providers="providers"
      @close="showDirPick = false"
      @select="(d) => { form.remote_dir = d.id; form.remote_name = d.name; showDirPick = false }"
      @toast="(msg, type) => emit('toast', msg, type)"
    />

    <ConfirmModal
      v-if="confirmDialog"
      :title="confirmDialog.title"
      :message="confirmDialog.message"
      :danger="confirmDialog.danger"
      @ok="confirmDialog.onOk(); confirmDialog = null"
      @cancel="confirmDialog = null"
    />
  </div>
</template>
