<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  answerDecision,
  answerInteraction,
  listOpenDecisions,
  listPendingInteractions,
  puntInteraction,
} from '../api/client'
import type { Decision, Interaction } from '../api/types'
import InteractionToast from './InteractionToast.vue'

const POLL_MS = 5000

// 分源聚合（T4/H2）：铃铛条目 = supervisor 升级的 job interaction + OPEN decision。
// 条目键用 `{source}:{id}` 复合键——interaction PK 是 (job_id, id)，裸 id 跨 job 可撞。
type BellItem =
  | { source: 'interaction'; key: string; interaction: Interaction }
  | { source: 'decision'; key: string; decision: Decision }

interface ToastPayload {
  title: string
  text: string
  to?: string
}

const router = useRouter()

const items = ref<BellItem[]>([])
const open = ref(false)
const toast = ref<ToastPayload | null>(null)
const submittingIds = ref<Set<string>>(new Set())
const itemErrors = ref<Map<string, string>>(new Map())
// 会话中继（SESS-01）relay 条目的内联回复草稿（按 item.key）
const relayDrafts = ref<Map<string, string>>(new Map())
const seenNeedsHuman = new Set<string>()
const seenDecisions = new Set<string>()

let timer: number | null = null

function isNeedsHuman(item: BellItem): boolean {
  return item.source === 'interaction' && item.interaction.needs_human === 1
}

// 会话中继（SESS-01）：kind=relay 的 decision 是 agent 会话的一个 turn——来源标签
// 显示「会话」、跳转会话抽屉（/sessions?sid=）而非 PlanDetail，自由文本就地作答。
function isRelay(item: BellItem): boolean {
  return item.source === 'decision' && item.decision.kind === 'relay' && !!item.decision.session_id
}

function relayTarget(d: Decision): string {
  return `/sessions?sid=${encodeURIComponent(d.session_id ?? '')}`
}

function shortSid(id?: string): string {
  return id ? id.slice(0, 8) : '—'
}

function relayDraft(key: string): string {
  return relayDrafts.value.get(key) ?? ''
}

function setRelayDraft(key: string, v: string): void {
  relayDrafts.value = new Map(relayDrafts.value).set(key, v)
}

function submitRelayDraft(item: BellItem): void {
  const text = relayDraft(item.key).trim()
  if (!text) {
    return
  }
  void submitAnswer(item, text).then(() => {
    if (!itemErrors.value.has(item.key)) {
      const next = new Map(relayDrafts.value)
      next.delete(item.key)
      relayDrafts.value = next
    }
  })
}

function onRelayKeydown(item: BellItem, ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault()
    submitRelayDraft(item)
  }
}

function sortAt(item: BellItem): number {
  return item.source === 'interaction'
    ? (item.interaction.escalated_at ?? item.interaction.created_at)
    : item.decision.asked_at
}

const sortedItems = computed(() =>
  [...items.value].sort((a, b) => {
    const hot = Number(isNeedsHuman(b)) - Number(isNeedsHuman(a))
    if (hot !== 0) {
      return hot
    }
    return sortAt(b) - sortAt(a)
  }),
)

const badgeCount = computed(() => items.value.length)
const needsHumanCount = computed(() => items.value.filter(isNeedsHuman).length)

function truncLine(s: string, max: number): string {
  const first = s.split(/\r?\n/)[0]?.trim() ?? ''
  return first.length > max ? `${first.slice(0, max)}...` : first
}

async function fetchPending(): Promise<void> {
  if (document.hidden) {
    return
  }
  const [iresp, dresp] = await Promise.all([
    listPendingInteractions(),
    listOpenDecisions(),
  ])
  const next: BellItem[] = [
    ...(iresp.interactions ?? []).map((i) => ({
      source: 'interaction' as const,
      key: `interaction:${i.id}`,
      interaction: i,
    })),
    ...(dresp.decisions ?? []).map((d) => ({
      source: 'decision' as const,
      key: `decision:${d.id}`,
      decision: d,
    })),
  ]
  const freshNeedsHuman = next.find(
    (it) =>
      it.source === 'interaction' &&
      it.interaction.needs_human === 1 &&
      !seenNeedsHuman.has(it.key),
  )
  const freshDecision = next.find(
    (it) => it.source === 'decision' && !seenDecisions.has(it.decision.id),
  )
  next.forEach((it) => {
    if (it.source === 'interaction' && it.interaction.needs_human === 1) {
      seenNeedsHuman.add(it.key)
    }
    if (it.source === 'decision') {
      seenDecisions.add(it.decision.id)
    }
  })
  items.value = next
  if (freshNeedsHuman && freshNeedsHuman.source === 'interaction') {
    const i = freshNeedsHuman.interaction
    toast.value = {
      title: '⚠ 新的人工介入请求 · needs_human',
      text: `job ${shortId(i.job_id)} — ${truncLine(i.prompt, 96) || '等待人工介入'}`,
      to: `/jobs/${encodeURIComponent(i.job_id)}`,
    }
  } else if (freshDecision && freshDecision.source === 'decision') {
    const d = freshDecision.decision
    if (isRelay(freshDecision)) {
      toast.value = {
        title: `会话等待回复 · ${d.title || shortSid(d.session_id)}`,
        text: truncLine(d.question, 96) || '会话停下等你回复',
        to: relayTarget(d),
      }
    } else {
      toast.value = {
        title: `新的决策请求 · ${d.title || d.id}`,
        text: truncLine(d.question, 96) || '等待人工作答',
        to: d.plan_id ? `/plans/${encodeURIComponent(d.plan_id)}` : undefined,
      }
    }
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void fetchPending().catch(() => {
      // 顶栏提示不阻断页面；下一轮继续拉取。
    })
  }, POLL_MS)
}

function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchPending().catch(() => {})
    startPolling()
  }
}

function toggleOpen(): void {
  open.value = !open.value
}

function close(): void {
  open.value = false
}

// 按 source 分流跳转：interaction → job 详情；relay decision → 会话抽屉；
// 其它 decision → plan 详情（无 plan_id 的全局提问仅展开，无跳转目标）。
function gotoItem(item: BellItem): void {
  if (item.source === 'interaction') {
    close()
    void router.push(`/jobs/${encodeURIComponent(item.interaction.job_id)}`)
    return
  }
  if (isRelay(item)) {
    close()
    void router.push(relayTarget(item.decision))
    return
  }
  if (item.decision.plan_id) {
    close()
    void router.push(`/plans/${encodeURIComponent(item.decision.plan_id)}`)
  }
}

function shortId(id: string): string {
  return id.length > 10 ? `...${id.slice(-10)}` : id
}

function promptLine(item: Interaction): string {
  return truncLine(item.prompt, 110)
}

function setSubmitting(key: string, submitting: boolean): void {
  const next = new Set(submittingIds.value)
  if (submitting) {
    next.add(key)
  } else {
    next.delete(key)
  }
  submittingIds.value = next
}

function clearItemError(key: string): void {
  if (!itemErrors.value.has(key)) {
    return
  }
  const next = new Map(itemErrors.value)
  next.delete(key)
  itemErrors.value = next
}

function setItemError(key: string, message: string): void {
  itemErrors.value = new Map(itemErrors.value).set(key, message)
}

function removeItem(key: string): void {
  items.value = items.value.filter((item) => item.key !== key)
  clearItemError(key)
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

// 作答按 source 分流：interaction 走 job 通道，decision 走 /v1/decisions/{id}/answer。
async function submitAnswer(item: BellItem, value: string): Promise<void> {
  if (submittingIds.value.has(item.key)) {
    return
  }
  setSubmitting(item.key, true)
  clearItemError(item.key)
  try {
    if (item.source === 'interaction') {
      await answerInteraction(item.interaction.job_id, item.interaction.id, value)
    } else {
      await answerDecision(item.decision.id, value)
    }
    removeItem(item.key)
    void fetchPending().catch(() => {})
  } catch (e) {
    setItemError(item.key, errorMessage(e))
  } finally {
    setSubmitting(item.key, false)
  }
}

// punt 只属于 job interaction；decision 无 punt。
async function submitPunt(item: BellItem): Promise<void> {
  if (item.source !== 'interaction') {
    return
  }
  if (submittingIds.value.has(item.key)) {
    return
  }
  setSubmitting(item.key, true)
  clearItemError(item.key)
  try {
    await puntInteraction(item.interaction.job_id, item.interaction.id)
    removeItem(item.key)
    void fetchPending().catch(() => {})
  } catch (e) {
    setItemError(item.key, errorMessage(e))
  } finally {
    setSubmitting(item.key, false)
  }
}

function optLabel(opt: { value: string; label?: string }): string {
  return opt.label ?? opt.value
}

function confirmYes(item: Interaction): string {
  return item.options?.[0]?.value ?? 'yes'
}

function confirmNo(item: Interaction): string {
  return item.options?.[1]?.value ?? 'no'
}

function confirmYesLabel(item: Interaction): string {
  return item.options?.[0]?.label ?? '确认'
}

function confirmNoLabel(item: Interaction): string {
  return item.options?.[1]?.label ?? '取消'
}

onMounted(() => {
  void fetchPending().catch(() => {})
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="bell-wrap">
    <button
      class="bell"
      type="button"
      aria-label="人工介入请求"
      :aria-expanded="open"
      @click="toggleOpen"
    >
      🔔
      <span
        v-if="badgeCount > 0"
        class="badge"
        :class="{ 'badge--hot': needsHumanCount > 0 }"
        :title="`待应答 ${badgeCount}，其中 ${needsHumanCount} 需人工介入`"
      >
        {{ badgeCount }}
      </span>
    </button>

    <div v-if="open" class="bell-scrim" aria-hidden="true" @click="close"></div>
    <div v-if="open" class="dropdown" role="menu">
      <div class="dh mono">
        <span>待应答 {{ badgeCount }}</span>
        <span>{{ needsHumanCount }} 需人工介入</span>
      </div>

      <div
        v-for="item in sortedItems"
        :key="item.key"
        class="esc"
        :class="{ hot: isNeedsHuman(item) }"
        role="menuitem"
      >
        <!-- job interaction 条目（既有分支不动，仅键改复合键） -->
        <template v-if="item.source === 'interaction'">
          <span class="e1 mono">
            <span v-if="item.interaction.needs_human === 1" class="mark mark--needs">needs_human</span>
            <span v-else-if="(item.interaction.escalated_at ?? 0) > 0" class="mark">escalated</span>
            <span class="idp">job {{ shortId(item.interaction.job_id) }}</span>
            <span class="chan">{{ item.interaction.type }}</span>
          </span>
          <span class="p">{{ promptLine(item.interaction) || '等待人工介入' }}</span>

          <div v-if="item.interaction.type === 'choice'" class="actions">
            <button
              v-for="opt in item.interaction.options ?? []"
              :key="opt.value"
              class="mini-btn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="submitAnswer(item, opt.value)"
            >
              {{ optLabel(opt) }}
            </button>
          </div>

          <div v-else-if="item.interaction.type === 'confirmation'" class="actions">
            <button
              class="mini-btn mini-btn--primary mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="submitAnswer(item, confirmYes(item.interaction))"
            >
              {{ confirmYesLabel(item.interaction) }}
            </button>
            <button
              class="mini-btn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="submitAnswer(item, confirmNo(item.interaction))"
            >
              {{ confirmNoLabel(item.interaction) }}
            </button>
          </div>

          <div v-else class="actions">
            <button
              class="mini-btn mini-btn--primary mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="gotoItem(item)"
            >
              进详情作答
            </button>
          </div>

          <div class="foot-actions">
            <button
              class="link-btn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="gotoItem(item)"
            >
              详情
            </button>
            <button
              v-if="item.interaction.needs_human !== 1"
              class="link-btn link-btn--warn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="submitPunt(item)"
            >
              {{ submittingIds.has(item.key) ? '提交中' : 'punt' }}
            </button>
          </div>
        </template>

        <!-- relay 条目（SESS-01）：agent 会话停下等回复，自由文本就地作答；详情跳会话抽屉 -->
        <template v-else-if="isRelay(item)">
          <span class="e1 mono">
            <span class="mark mark--relay">会话</span>
            <span class="idp" :title="item.decision.session_id">{{ shortSid(item.decision.session_id) }}</span>
            <span class="chan">relay</span>
          </span>
          <span class="p">
            {{ item.decision.title ? `${item.decision.title} — ` : '' }}{{ truncLine(item.decision.question, 110) || '会话停下等你回复' }}
          </span>

          <div class="relay-reply">
            <textarea
              class="relay-input mono"
              rows="2"
              placeholder="回复 agent…（Ctrl/Cmd+Enter 发送；/off 关闭中继）"
              :value="relayDraft(item.key)"
              :disabled="submittingIds.has(item.key)"
              @input="setRelayDraft(item.key, ($event.target as HTMLTextAreaElement).value)"
              @keydown="onRelayKeydown(item, $event)"
            ></textarea>
            <button
              class="mini-btn mini-btn--primary mono"
              type="button"
              :disabled="submittingIds.has(item.key) || !relayDraft(item.key).trim()"
              @click="submitRelayDraft(item)"
            >
              {{ submittingIds.has(item.key) ? '发送中' : '发送' }}
            </button>
          </div>

          <div class="foot-actions">
            <button
              class="link-btn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="gotoItem(item)"
            >
              打开会话
            </button>
          </div>
        </template>

        <!-- decision 条目（T4）：选项型就地作答；自由文本型进 plan 详情作答；无 punt -->
        <template v-else>
          <span class="e1 mono">
            <span class="mark mark--decision">决策</span>
            <span v-if="item.decision.plan_id" class="idp">plan {{ shortId(item.decision.plan_id) }}</span>
            <span v-else class="idp">全局</span>
            <span class="chan">{{ (item.decision.options?.length ?? 0) > 0 ? 'choice' : 'question' }}</span>
          </span>
          <span class="p">
            {{ item.decision.title ? `${item.decision.title} — ` : '' }}{{ truncLine(item.decision.question, 110) || '等待人工作答' }}
          </span>

          <div v-if="(item.decision.options?.length ?? 0) > 0" class="actions">
            <button
              v-for="opt in item.decision.options ?? []"
              :key="opt"
              class="mini-btn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="submitAnswer(item, opt)"
            >
              {{ opt }}
            </button>
          </div>

          <div v-else-if="item.decision.plan_id" class="actions">
            <button
              class="mini-btn mini-btn--primary mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="gotoItem(item)"
            >
              进详情作答
            </button>
          </div>

          <div v-if="item.decision.plan_id" class="foot-actions">
            <button
              class="link-btn mono"
              type="button"
              :disabled="submittingIds.has(item.key)"
              @click="gotoItem(item)"
            >
              详情
            </button>
          </div>
        </template>

        <p v-if="itemErrors.get(item.key)" class="item-error mono">
          操作失败：{{ itemErrors.get(item.key) }}
        </p>
      </div>

      <div v-if="sortedItems.length === 0" class="empty mono">
        无待应答的交互或决策
      </div>
    </div>

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
  position: relative;
  background: transparent;
  border: 1px solid var(--line);
  color: var(--paper);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 14px;
  line-height: 1.2;
}

.bell:hover {
  border-color: var(--phosphor);
}

.badge {
  position: absolute;
  top: -7px;
  right: -7px;
  background: var(--phosphor);
  color: var(--ink);
  border-radius: 9px;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 600;
  line-height: 16px;
  min-width: 16px;
  padding: 0 5px;
}

.badge--hot {
  background: var(--fail);
  color: #fff;
}

.bell-scrim {
  position: fixed;
  inset: 0;
  z-index: 55;
  background: transparent;
}

.dropdown {
  position: fixed;
  top: 52px;
  right: 18px;
  z-index: 60;
  width: min(420px, calc(100vw - 36px));
  max-height: min(520px, calc(100vh - 70px));
  overflow-y: auto;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
}

.dh {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  color: var(--queue);
  font-size: 12px;
}

.esc {
  display: block;
  width: 100%;
  text-align: left;
  background: transparent;
  color: var(--paper);
  border: none;
  border-bottom: 1px solid var(--line);
  padding: 11px 14px;
}

.esc:last-of-type {
  border-bottom: none;
}

.esc:hover {
  background: var(--ink);
}

.esc.hot {
  background: rgba(200, 85, 61, 0.1);
}

.esc.hot:hover {
  background: rgba(200, 85, 61, 0.16);
}

.e1 {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 12px;
}

.idp {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mark,
.chan {
  display: inline-block;
  flex: none;
  border: 1px solid var(--line);
  border-radius: 9px;
  color: var(--queue);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1.4;
  padding: 1px 6px;
}

.mark--needs {
  background: var(--fail);
  border-color: var(--fail);
  color: #fff;
}

.mark--decision {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.mark--relay {
  border-color: var(--run);
  color: var(--run);
}

.relay-reply {
  display: flex;
  align-items: flex-end;
  gap: 7px;
  margin-top: 9px;
}

.relay-input {
  flex: 1 1 auto;
  min-width: 0;
  resize: vertical;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 8px;
  font-size: 11px;
  line-height: 1.45;
}

.relay-input:focus {
  outline: none;
  border-color: var(--phosphor);
}

.relay-input:disabled {
  opacity: 0.55;
}

.chan {
  color: var(--phosphor);
}

.p {
  display: block;
  margin-top: 6px;
  color: var(--paper);
  font-size: 13px;
  line-height: 1.45;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
  margin-top: 9px;
}

.mini-btn {
  flex: none;
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
}

.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.mini-btn--primary {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.mini-btn--primary:hover:not(:disabled) {
  background: var(--phosphor);
  color: var(--ink);
}

.foot-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 8px;
}

.link-btn {
  background: transparent;
  border: none;
  color: var(--queue);
  padding: 0;
  font-size: 11px;
}

.link-btn:hover:not(:disabled) {
  color: var(--phosphor);
}

.link-btn--warn:hover:not(:disabled) {
  color: var(--run);
}

.mini-btn:disabled,
.link-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.item-error {
  color: var(--fail);
  font-size: 11px;
  line-height: 1.4;
  margin: 8px 0 0;
  word-break: break-word;
}

.empty {
  padding: 14px;
  color: var(--queue);
  font-size: 12px;
}
</style>
