<script setup>
// 网盘账号头像（右上角）：圆形头像(object-fit:cover 裁掉黑边)；
// 悬停弹窗显示已用/剩余/总容量；启动时或用户手动触发容量同步。
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { refreshAccountSilently, refreshAccountNow, accountName, providerIconUrl, providerMetaOf, formatBytes } from '../api'
import { recordAccountHealth } from '../workspace'
import UiIcon from './UiIcon.vue'

const refreshInflight = new Map()

function refreshAccountOnce(userID, force = false) {
  if (!userID) return Promise.resolve(null)
  if (refreshInflight.has(userID)) return refreshInflight.get(userID)
  const promise = (force ? refreshAccountNow(userID) : refreshAccountSilently(userID)).finally(() => refreshInflight.delete(userID))
  refreshInflight.set(userID, promise)
  return promise
}

const props = defineProps({
  account: { type: Object, required: false, default: null },
  providers: { type: Array, default: () => [] },
})

// 本地 token 副本：静默刷新后更新展示，不依赖父组件重传
const tok = ref(props.account ? (props.account.token || {}) : {})
const quota = ref(props.account ? (props.account.usage || null) : null)
const familyQuota = ref(props.account ? (props.account.family_usage || null) : null)
watch(() => props.account, (a) => {
  tok.value = a ? (a.token || {}) : {}
  quota.value = a ? (a.usage || null) : null
  familyQuota.value = a ? (a.family_usage || null) : null
})

const avatar = computed(() => tok.value.avatar || '')
const nick = computed(() => accountName(props.account))
const providerMeta = computed(() => providerMetaOf(props.account, props.providers))
const providerIcon = computed(() => providerIconUrl(providerMeta.value))
const pid = computed(() => providerMeta.value.key || (props.account ? props.account.user_id : ''))
const providerLabel = computed(() => providerMeta.value.label || pid.value)

const total = computed(() => Math.max(0, Number(quota.value?.size ?? tok.value.total_size) || 0))
const used = computed(() => {
  const u = Math.max(0, Number(quota.value?.used ?? tok.value.used_size) || 0)
  if (u > 0) return u
  // used 缺失时用 total - free 推算
  const f = Math.max(0, Number(tok.value.free_size) || 0)
  return total.value > 0 && f > 0 ? Math.max(0, total.value - f) : 0
})
const free = computed(() => {
  const f = Math.max(0, Number(tok.value.free_size) || 0)
  if (f > 0) return f
  return total.value > 0 ? Math.max(0, total.value - used.value) : 0
})
const pct = computed(() => total.value > 0 ? Math.min(100, Math.round((used.value / total.value) * 100)) : 0)
const hasQuota = computed(() => total.value > 0)
const familyTotal = computed(() => Math.max(0, Number(familyQuota.value?.size) || 0))
const hasFamilyQuota = computed(() => familyTotal.value > 0)
const familyUsed = computed(() => Math.min(familyTotal.value, Math.max(0, Number(familyQuota.value?.used) || 0)))
const familyFree = computed(() => familyTotal.value - familyUsed.value)
const familyPct = computed(() => hasFamilyQuota.value ? Math.min(100, Math.round((familyUsed.value / familyTotal.value) * 100)) : 0)
const quotaStatus = computed(() => String(quota.value?.status || (hasQuota.value ? 'available' : 'unknown')))
const quotaStatusText = computed(() => {
  if (quota.value?.type === 'unlimited') return '总空间不限量；已用空间暂未提供'
  if (quota.value?.description) return quota.value.description
  if (quotaStatus.value === 'rate_limited') return '刷新受限'
  if (quotaStatus.value === 'error') return '刷新失败'
  if (quotaStatus.value === 'unsupported') return '暂无容量信息'
  if (quotaStatus.value === 'unknown') return '暂无容量信息'
  return ''
})
const quotaUpdatedText = computed(() => {
  const timestamp = Number(quota.value?.updated_at) || 0
  return timestamp > 0 ? `更新于 ${new Date(timestamp * 1000).toLocaleString('zh-CN', { hour12: false })}` : ''
})

const vipName = computed(() => tok.value.vipname || '')
const vipExpire = computed(() => tok.value.vipexpire || '')

// 头像加载失败回退到 provider 图标，再失败回退昵称首字
const avatarFailed = ref(false)
watch(avatar, () => { avatarFailed.value = false })
const avatarText = computed(() => {
  const n = nick.value || ''
  return n ? n.slice(0, 2) : '?'
})

// ---------- 悬停弹窗 ----------
const rootRef = ref(null)
const show = ref(false)
const popStyle = ref({})
const refreshing = ref(false)
let enterTimer = null, leaveTimer = null

function updatePos() {
  if (!rootRef.value) return
  const r = rootRef.value.getBoundingClientRect()
  const w = 270
  const right = Math.max(8, window.innerWidth - r.right)
  const top = r.bottom + 6
  popStyle.value = {
    top: top + 'px',
    right: right + 'px',
    width: w + 'px',
  }
}

function onEnter() {
  clearTimeout(leaveTimer)
  enterTimer = setTimeout(() => {
    updatePos()
    show.value = true
  }, 120)
}
function onLeave() {
  clearTimeout(enterTimer)
  leaveTimer = setTimeout(() => { show.value = false }, 200)
}
onBeforeUnmount(() => {
  clearTimeout(enterTimer)
  clearTimeout(leaveTimer)
})

// ---------- 启动/手动同步容量 ----------
async function syncQuota(force = false) {
  if (!props.account) return null
  const snapUid = props.account.user_id
  try {
    const acc = await refreshAccountOnce(snapUid, force)
    if (snapUid === (props.account && props.account.user_id) && acc) {
      if (acc.token) tok.value = acc.token
      quota.value = acc.usage || null
      familyQuota.value = acc.family_usage || null
    }
    return acc
  } catch(error) {
    if (snapUid === props.account?.user_id) recordAccountHealth(snapUid, error)
  }
}

async function manualRefresh() {
  if (!props.account || refreshing.value) return
  refreshing.value = true
  try {
    await syncQuota(true)
  } finally {
    refreshing.value = false
  }
}
watch(() => props.account && props.account.user_id, (uid) => {
  if (uid) void syncQuota()
})
onMounted(() => {
  if (props.account) void syncQuota()
})
</script>

<template>
  <div ref="rootRef" class="acc-ava" @mouseenter="onEnter" @mouseleave="onLeave">
    <div class="ava-circle">
      <img v-if="avatar && !avatarFailed" :src="avatar" alt="" class="ava-img" @error="avatarFailed = true" />
      <img v-else-if="providerIcon" :src="providerIcon" alt="" class="ava-img" />
      <span v-else class="ava-text">{{ avatarText }}</span>
    </div>

    <teleport to="body">
      <transition name="popover-zoom">
        <div
          v-if="show"
          class="acc-pop"
          :style="popStyle"
          @mouseenter="onEnter"
          @mouseleave="onLeave"
        >
          <div class="ap-head">
            <div class="ava-circle sm">
              <img v-if="avatar && !avatarFailed" :src="avatar" alt="" class="ava-img" @error="avatarFailed = true" />
              <img v-else-if="providerIcon" :src="providerIcon" alt="" class="ava-img" />
              <span v-else class="ava-text">{{ avatarText }}</span>
            </div>
            <div class="ap-meta">
              <div class="ap-name">{{ nick }}</div>
              <div class="ap-provider">{{ providerLabel }}</div>
              <div v-if="vipName" class="ap-vip"><UiIcon name="star" :size="12" />{{ vipName }}<template v-if="vipExpire"> · {{ vipExpire }}</template></div>
            </div>
          </div>
          <div class="ap-quota">
            <div v-if="hasFamilyQuota" class="ap-space-title">个人云</div>
            <div v-if="hasQuota" class="ap-qrow">
              <span class="ap-qpct">{{ pct }}%</span>
              <span class="ap-qhint">已用 / 总容量</span>
            </div>
            <div v-if="hasQuota" class="ap-bar">
              <div class="ap-bar-fill" :class="{ full: pct >= 95 }" :style="{ width: pct + '%' }"></div>
            </div>
            <div v-if="hasQuota" class="ap-nums">
              <div><span class="ap-num-k">已用</span><span class="ap-num-v">{{ formatBytes(used) }}</span></div>
              <div><span class="ap-num-k">剩余</span><span class="ap-num-v">{{ formatBytes(free) }}</span></div>
              <div><span class="ap-num-k">总容量</span><span class="ap-num-v">{{ formatBytes(total) }}</span></div>
            </div>
            <div v-else class="ap-noquota">{{ quotaStatusText || '暂无容量信息' }}</div>
            <div v-if="hasQuota && quotaStatusText" class="ap-qstatus">{{ quotaStatusText }}</div>
            <div v-if="quotaUpdatedText" class="ap-qupdated">{{ quotaUpdatedText }}</div>
            <template v-if="hasFamilyQuota">
              <div class="ap-space-title ap-family-title">家庭云</div>
              <div class="ap-qrow"><span class="ap-qpct">{{ familyPct }}%</span><span class="ap-qhint">已用 / 总容量</span></div>
              <div class="ap-bar"><div class="ap-bar-fill" :class="{ full: familyPct >= 95 }" :style="{ width: familyPct + '%' }"></div></div>
              <div class="ap-nums">
                <div><span class="ap-num-k">已用</span><span class="ap-num-v">{{ formatBytes(familyUsed) }}</span></div>
                <div><span class="ap-num-k">剩余</span><span class="ap-num-v">{{ formatBytes(familyFree) }}</span></div>
                <div><span class="ap-num-k">总容量</span><span class="ap-num-v">{{ formatBytes(familyTotal) }}</span></div>
              </div>
            </template>
            <button class="btn sm ap-refresh" type="button" :disabled="refreshing" @click.stop="manualRefresh">
              <span v-if="refreshing" class="spin"></span>
              <UiIcon v-else name="refresh" :size="12" />
              {{ refreshing ? '刷新中…' : '刷新' }}
            </button>
          </div>
        </div>
      </transition>
    </teleport>
  </div>
</template>

<style scoped>
.acc-ava { position: relative; display: inline-flex; align-items: center; flex-shrink: 0; }
.ava-circle {
  width: 30px; height: 30px; border-radius: 50%; overflow: hidden;
  background: var(--bg-subtle); color: var(--text-secondary);
  display: inline-flex; align-items: center; justify-content: center;
  border: 1px solid var(--border-light); position: relative; cursor: pointer;
  flex-shrink: 0;
  transition: border-color var(--motion-fast) var(--motion-ease), box-shadow var(--motion-fast) var(--motion-ease), transform var(--motion-fast) var(--motion-spring);
}
.acc-ava:hover > .ava-circle { border-color: color-mix(in srgb, var(--color-primary) 42%, var(--border-light)); box-shadow: 0 4px 12px color-mix(in srgb, var(--color-primary) 14%, transparent); transform: translateY(-1px) scale(1.03); }
.ava-circle.sm { width: 36px; height: 36px; }
.ava-img { display: block; width: 100%; height: 100%; object-fit: cover; }
.ava-text { font-size: 12px; font-weight: 600; }
.acc-pop {
  position: fixed; z-index: 9999; min-width: 260px; padding: 12px;
  background: var(--bg-elevated); border: 1px solid var(--border-light);
  border-radius: var(--radius-lg); box-shadow: var(--shadow-modal);
}
.ap-head { display: flex; align-items: center; gap: 10px; padding-bottom: 10px; border-bottom: 1px solid var(--border-lighter); }
.ap-meta { min-width: 0; }
.ap-name { font-size: 14px; font-weight: 600; color: var(--text-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ap-provider { font-size: 12px; color: var(--text-tertiary); }
.ap-vip { display: inline-flex; align-items: center; gap: 3px; margin-top: 3px; font-size: 11px; color: var(--color-warning); background: color-mix(in srgb, var(--color-warning) 12%, transparent); padding: 1px 6px; border-radius: 999px; }
.ap-quota { padding-top: 10px; }
.ap-space-title { font-size: 11px; font-weight: 600; color: var(--text-secondary); margin-bottom: 5px; }
.ap-family-title { border-top: 1px solid var(--border-lighter); margin-top: 10px; padding-top: 10px; }
.ap-qrow { display: flex; align-items: baseline; justify-content: space-between; }
.ap-qpct { font-size: 16px; font-weight: 700; color: var(--color-primary); }
.ap-qhint { font-size: 11px; color: var(--text-tertiary); }
.ap-bar { height: 6px; border-radius: 3px; background: var(--bg-subtle); overflow: hidden; margin: 6px 0 8px; }
.ap-bar-fill { height: 100%; border-radius: 3px; background: var(--color-primary); transition: width var(--motion-normal) var(--motion-ease); }
.ap-bar-fill.full { background: var(--color-error); }
.ap-nums { display: flex; gap: 8px; }
.ap-nums > div { flex: 1; display: flex; flex-direction: column; gap: 1px; }
.ap-num-k { font-size: 11px; color: var(--text-tertiary); }
.ap-num-v { font-size: 12px; color: var(--text-primary); font-weight: 600; }
.ap-noquota { font-size: 12px; color: var(--text-tertiary); text-align: center; padding: 6px 0; }
.ap-qstatus { margin-top: 7px; color: var(--color-warning); font-size: 11px; text-align: center; }
.ap-qupdated { margin-top: 5px; color: var(--text-tertiary); font-size: 10.5px; text-align: center; }
.ap-refresh { margin-top: 8px; width: 100%; display: inline-flex; align-items: center; justify-content: center; gap: 5px; }
@media (prefers-reduced-motion: reduce) { .ava-circle, .ap-bar-fill { transition-duration: 1ms; } }
</style>
