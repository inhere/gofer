<script setup lang="ts">
// 顶栏「待我决策 N」（N3 §1 / §6）：点它或在任何页面按 g d 打开右侧全局决策浮层
// （components/today/DecisionOverlay.vue，与 /today 页同一套队列组件）。原来的铃铛下拉
// （interaction / decision 就地作答）已并入决策卡，这里只保留：
//  - 计数与「会超时」高亮（来自 store/today 的同一份队列）；
//  - 新的人工介入请求 / 决策请求 / 待批准 job（gofer-5wvc，--hold）的提示条
//    （`pending` 快照主题，弹哪一条的逻辑在 utils/pendingAlert，可单测）；
//  - 顶栏常驻的 `meta` / `stats` 订阅：配置热重载失效缓存、服务端时区、新版本提示（F8）。
import { computed, ref } from 'vue'
import { getStats, listJobs, listOpenDecisions, listPendingInteractions } from '../api/client'
import type { PendingSnapshot, Stats } from '../api/types'
import { noteServerVersion } from '../store/staleBuild'
import { decisionCount, overlayOpen, visibleCards } from '../store/today'
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
import InteractionToast from './InteractionToast.vue'

const toast = ref<PendingToast | null>(null)
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

function toggleOpen(): void {
  overlayOpen.value = !overlayOpen.value
}
</script>

<template>
  <div class="bell-wrap">
    <button
      class="bell mono"
      :class="{ 'bell--hot': hot, 'bell--some': decisionCount > 0 }"
      type="button"
      aria-label="待我决策"
      title="待我决策（任何页面按 g 再按 d 打开）"
      :aria-expanded="overlayOpen"
      data-test="decision-bell"
      @click="toggleOpen"
    >
      <span class="bell-label">待我决策</span>
      <span class="bell-n">{{ decisionCount }}</span>
    </button>

    <InteractionToast
      v-if="toast"
      :title="toast.title"
      :text="toast.text"
      :to="toast.to"
      @close="toast = null"
      @goto="toast = null"
    />
  </div>
</template>

<style scoped>
.bell-wrap {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.bell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  border: 1px solid var(--line);
  color: var(--queue);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 12px;
  line-height: 1.2;
  white-space: nowrap;
}

.bell:hover {
  border-color: var(--phosphor);
  color: var(--paper);
}

.bell-n {
  min-width: 16px;
  text-align: center;
  font-weight: 600;
}

.bell--some {
  color: var(--paper);
  border-color: var(--run);
}

.bell--some .bell-n {
  color: var(--run);
}

.bell--hot {
  border-color: var(--fail);
}

.bell--hot .bell-n {
  color: var(--fail);
}

@media (max-width: 768px) {
  .bell-label {
    display: none;
  }
  .bell::before {
    content: '待决';
  }
}
</style>
