<script setup lang="ts">
// 顶栏「待我决策 N」（N3 §1 / §6）：点它或在任何页面按 g d 打开右侧全局决策浮层
// （components/today/DecisionOverlay.vue，与 /today 页同一套队列组件）。原来的铃铛下拉
// （interaction / decision 就地作答）已并入决策卡，这里只保留：
//  - 计数与「会超时」高亮（来自 store/today 的同一份队列）；
//  - 新的人工介入请求 / 决策请求的提示条（`pending` 快照主题）；
//  - 顶栏常驻的 `meta` / `stats` 订阅：配置热重载失效缓存、服务端时区、新版本提示（F8）。
import { computed, ref } from 'vue'
import { getStats, listOpenDecisions, listPendingInteractions } from '../api/client'
import type { Decision, Interaction, Stats } from '../api/types'
import { noteServerVersion } from '../store/staleBuild'
import { decisionCount, overlayOpen, visibleCards } from '../store/today'
import { useLiveTopic } from '../utils/useLiveTopic'
import { invalidateMetaCache } from '../api/metaCache'
import { setServerTZOffset } from '../api/time'
import InteractionToast from './InteractionToast.vue'

interface ToastPayload {
  title: string
  text: string
  to?: string
  // 对应的待办条目；条目不再待处理（已回复 / 已标「无需回复」/ 过期）时提示自动消失。
  key?: string
}

type PendingItem =
  | { source: 'interaction'; key: string; interaction: Interaction }
  | { source: 'decision'; key: string; decision: Decision }

const toast = ref<ToastPayload | null>(null)
const seenNeedsHuman = new Set<string>()
const seenDecisions = new Set<string>()

const hot = computed(() => visibleCards.value.some((c) => c.urgency === 'now'))

function truncLine(s: string, max: number): string {
  const first = s.split(/\r?\n/)[0]?.trim() ?? ''
  return first.length > max ? `${first.slice(0, max)}...` : first
}

function shortId(id: string): string {
  return id.length > 10 ? `...${id.slice(-10)}` : id
}

function shortSid(id?: string): string {
  return id ? id.slice(0, 8) : '—'
}

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

// `pending` 快照：{interactions, decisions}，与 /v1/interactions + /v1/decisions?state=OPEN 同形。
function onPendingSnap(data: unknown): void {
  const d = data as { interactions?: Interaction[]; decisions?: Decision[] }
  applyPending(d.interactions ?? [], d.decisions ?? [])
}

async function fetchPending(): Promise<void> {
  const [iresp, dresp] = await Promise.all([listPendingInteractions(), listOpenDecisions()])
  applyPending(iresp.interactions ?? [], dresp.decisions ?? [])
}

// 只为提示条：新出现的 needs_human interaction / OPEN decision 弹一次。
function applyPending(interactions: Interaction[], decisions: Decision[]): void {
  const next: PendingItem[] = [
    ...interactions.map((i) => ({ source: 'interaction' as const, key: `interaction:${i.id}`, interaction: i })),
    // 已标「无需回复」的中继轮次仍是 OPEN（等待继续），但不算待处理。
    ...decisions.filter((d) => !d.acked_at).map((d) => ({ source: 'decision' as const, key: `decision:${d.id}`, decision: d })),
  ]
  const freshNeedsHuman = next.find(
    (it) => it.source === 'interaction' && it.interaction.needs_human === 1 && !seenNeedsHuman.has(it.key),
  )
  const freshDecision = next.find((it) => it.source === 'decision' && !seenDecisions.has(it.decision.id))
  next.forEach((it) => {
    if (it.source === 'interaction' && it.interaction.needs_human === 1) seenNeedsHuman.add(it.key)
    if (it.source === 'decision') seenDecisions.add(it.decision.id)
  })
  if (toast.value?.key && !next.some((it) => it.key === toast.value?.key)) toast.value = null
  if (freshNeedsHuman && freshNeedsHuman.source === 'interaction') {
    const i = freshNeedsHuman.interaction
    toast.value = {
      key: freshNeedsHuman.key,
      title: '⚠ 新的人工介入请求 · needs_human',
      text: `job ${shortId(i.job_id)} — ${truncLine(i.prompt, 96) || '等待人工介入'}`,
      to: `/jobs/${encodeURIComponent(i.job_id)}`,
    }
  } else if (freshDecision && freshDecision.source === 'decision') {
    const d = freshDecision.decision
    const relay = d.kind === 'relay' && !!d.session_id
    const permission = d.kind === 'permission' && !!d.session_id
    toast.value = permission
      ? {
          key: freshDecision.key,
          title: `会话需要授权 · ${d.title || shortSid(d.session_id)}`,
          text: truncLine(d.permission?.summary || d.question, 96) || '终端在等待工具授权',
          to: `/workbench?thread=${encodeURIComponent(`r:${d.session_id ?? ''}`)}`,
        }
      : relay
      ? {
          key: freshDecision.key,
          title: `会话等待回复 · ${d.title || shortSid(d.session_id)}`,
          text: truncLine(d.question, 96) || '会话停下等你回复',
          to: `/workbench?thread=${encodeURIComponent(`r:${d.session_id ?? ''}`)}`,
        }
      : {
          key: freshDecision.key,
          title: `新的决策请求 · ${d.title || d.id}`,
          text: truncLine(d.question, 96) || '等待人工作答',
          to: d.plan_id ? `/plans/${encodeURIComponent(d.plan_id)}` : undefined,
        }
  }
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
