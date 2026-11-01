<script setup lang="ts">
// 顶栏「待我决策 N」（设计稿 §6.1 / §16-4）；信箱待指派仍是独立入口（MailButton）。
//
//  - 计数 = 「等我」列表的未读数（收件箱 total_unread，同源同数；收件箱不可用时回落为 store/today 的「等我」列表长度）；
//  - 点它或任何页面按 g d 打开右侧全局决策浮层（components/today/DecisionOverlay.vue）；
//  - 顶栏常驻的 `meta` / `stats` 订阅照旧：配置热重载失效缓存、服务端时区、新版本提示（F8）；
//  - `pending` 快照仍接 utils/pendingAlert 弹提示条（新的人工介入请求 / 决策 / 待批准 job）；
import { computed, ref } from 'vue'
import { Bell } from '@lucide/vue'
import Icon from './Icon.vue'
import { getStats, listJobs, listOpenDecisions, listPendingInteractions } from '../api/client'
import type { PendingSnapshot, Stats } from '../api/types'
import { noteServerVersion } from '../store/staleBuild'
import { decisionCount, overlayOpen, visibleCards } from '../store/today'
import { t } from '../i18n'
import { needsMeBadge } from '../utils/shell'
import { inboxData } from '../store/inbox'
import { useLiveTopic } from '../utils/useLiveTopic'
import { invalidateMetaCache } from '../api/metaCache'
import { setServerTZOffset } from '../api/time'
import {
  PENDING_APPROVAL_LIMIT,
  approvalFromJob,
  emptySeen,
  pendingAlert,
  pendingLists,
  type PendingLists,
  type PendingSeen,
  type PendingToast,
} from '../utils/pendingAlert'
import Toast from './Toast.vue'
import { toastDuration } from './toast'
import { useRouter } from 'vue-router'

const router = useRouter()
const toast = ref<PendingToast | null>(null)

// 点提示条正文：先收起，有跳转目标再跳（无目标时只关闭）。
function gotoToast(): void {
  const to = toast.value?.to
  toast.value = null
  if (to) void router.push(to)
}
// 已弹过提示的待办（needs_human 交互 / 决策 / 待批准 job）；条目从快照里消失就被 forget。
let seen: PendingSeen = emptySeen()

const hot = computed(() => visibleCards.value.some((c) => c.urgency === 'now'))

// 同一个响应里的 server version 交给 staleBuild store 比对（F8：服务端升级后提示刷新）。
async function refreshStats(): Promise<void> {
  try {
    const s = await getStats()
    setServerTZOffset(s.server_tz_offset_sec)
    noteServerVersion(s.version)
  } catch {
    // 只是提示性信息
  }
}

function onStatsSnap(data: unknown): void {
  const st = data as Stats
  setServerTZOffset(st.server_tz_offset_sec)
  noteServerVersion(st.version)
}

// `pending` 快照：{interactions, decisions, approvals}，interactions / decisions 与
// /v1/interactions + /v1/decisions?state=OPEN 同形，approvals 是 awaiting_approval 的 job。
function onPendingSnap(data: unknown): void {
  applyPending(pendingLists((data ?? {}) as PendingSnapshot))
}

// WS 断了 15s 以上时的兜底轮询：三次拉取拼成同一形状（待批 job 走 GET /v1/jobs 的状态过滤）。
async function fetchPending(): Promise<void> {
  const [iresp, dresp, jresp] = await Promise.all([
    listPendingInteractions(),
    listOpenDecisions(),
    listJobs({ status: 'awaiting_approval', limit: PENDING_APPROVAL_LIMIT }),
  ])
  applyPending({
    interactions: iresp.interactions ?? [],
    decisions: dresp.decisions ?? [],
    approvals: (jresp.jobs ?? []).map(approvalFromJob),
  })
}

// 只为提示条：新出现的待批准 job / needs_human interaction / OPEN decision 弹一次（utils/pendingAlert）。
function applyPending(lists: PendingLists): void {
  const r = pendingAlert(seen, toast.value, lists)
  seen = r.seen
  toast.value = r.toast
}

// 顶栏常驻组件顺带订阅 `meta`：配置热重载 / agent 降级恢复时失效 agents、meta 缓存。
useLiveTopic('meta', { fetch: invalidateMetaCache, initial: false })
useLiveTopic('stats', { fetch: refreshStats, onSnap: onStatsSnap, initial: false })
useLiveTopic('pending', { fetch: fetchPending, onSnap: onPendingSnap, initial: false })

// 徽标 = 「等我」列表的未读数（= 收件箱 total_unread）；worker 申请不在「等我」六类里，不再并入计数。
const total = computed(() => needsMeBadge(inboxData.value?.total_unread, decisionCount.value))
const label = computed(() => t('act.needsMe'))

function toggleOpen(): void {
  overlayOpen.value = !overlayOpen.value
}
</script>

<template>
  <div class="nm-wrap">
    <button
      class="nm"
      :class="{ 'nm--hot': hot, 'nm--some': total > 0 }"
      type="button"
      :aria-label="label"
      :title="`${label}（g d）`"
      :aria-expanded="overlayOpen"
      data-test="needs-me"
      @click="toggleOpen"
    >
      <Icon class="nm-icon" :icon="Bell" size="sm" />
      <span class="nm-label">{{ label }}</span>
      <span class="nm-n" data-test="needs-me-count">{{ total }}</span>
    </button>

    <Toast
      v-if="toast"
      placement="corner"
      tone="danger"
      closable
      body-action
      :title="toast.title"
      :text="toast.text"
      :duration="toastDuration()"
      @close="toast = null"
      @body="gotoToast"
    />
  </div>
</template>

<style scoped>
.nm-wrap {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.nm {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  border: 1px solid var(--line);
  color: var(--ink-3);
  border-radius: var(--r-ctl);
  padding: 4px 9px;
  font-size: 12.5px;
  line-height: 1.2;
  white-space: nowrap;
}
.nm:hover {
  border-color: var(--accent);
  color: var(--ink);
}
.nm-n {
  min-width: 16px;
  text-align: center;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.nm--some {
  color: var(--need-ink);
  border-color: var(--need);
  background: var(--need-soft);
}
.nm--hot {
  border-color: var(--fail);
}
/* 桌面有文字标签，铃铛图标只在手机（藏了文字）出现。 */
.nm-icon {
  display: none;
}
.nm--hot .nm-n {
  color: var(--fail);
}
@media (max-width: 760px) {
  .nm-label {
    display: none;
  }
  .nm {
    gap: 4px;
  }
  .nm-icon {
    display: inline-flex;
  }
}
</style>
