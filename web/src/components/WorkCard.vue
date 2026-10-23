<script setup lang="ts">
// 工作项卡片（「工作项」页）。外壳是与 Sessions 页共用的 InfoCard；本组件只决定
// 「关键信息放哪、详情放哪」，不发请求——所有操作都 emit 给页面。
// 状态改用 P0 的 StatusPill（状态族的字形 + 文字），重启时标 / 停滞标记改用页面字典。
import { computed } from 'vue'
import InfoCard from './InfoCard.vue'
import StatusPill from '../ui/StatusPill.vue'
import { t } from '../i18n'
import { fmtAgo } from '../api/time'
import { runnerLabel } from '../utils/runnerDisplay'
import { agentStateLabel } from '../utils/sessionState'
import { workUsageLabel } from '../utils/sessionUsage'
import { resumeLabel, resumeTitle } from '../utils/sessionResume'
import {
  fieldSourceKind,
  fieldSourceText,
  inflightRequests,
  lastFinishedRequest,
  linkTags,
  pendingSuggestions,
  primarySession,
  suggestionValueText,
  summarizeBlock,
  WORK_STATUSES,
  statusTone,
  workspaceLabel,
} from '../utils/work'
import { dueLabelKey, stalledDays, suggestionLabelKey, workRequestLine } from '../utils/workBoard'
import type { AgentSession, WorkItem, WorkStatus } from '../api/types'

const props = withDefaults(
  defineProps<{
    item: WorkItem
    expanded?: boolean
    active?: boolean
    // 会话全量信息（can_resume 等）；没有就只显示工作项自带的简要信息
    sessions?: Record<string, AgentSession>
    busy?: boolean
    nowSec?: number
    // 写操作灰显原因（只读身份 / 缺权限）：空串 = 可点，非空挂到按钮 title（utils/pageGate.ts）。
    writeBlock?: string
  }>(),
  { expanded: false, active: false, sessions: () => ({}), busy: false, nowSec: undefined, writeBlock: '' },
)

const emit = defineEmits<{
  (e: 'toggle'): void
  (e: 'open'): void
  (e: 'open-session', sid: string): void
  (e: 'wake', sid: string): void
  (e: 'set-status', status: WorkStatus): void
  // W2a：手动整理、采纳 / 忽略整理建议
  (e: 'summarize'): void
  (e: 'accept-suggestion', field: string): void
  (e: 'dismiss-suggestion', field: string): void
}>()

const primary = computed(() => primarySession(props.item))
const tags = computed(() => linkTags(props.item))
const full = computed(() => (primary.value ? props.sessions[primary.value.session_id] : undefined))
const canWake = computed(() => !!primary.value && primary.value.offline && !!full.value?.can_resume)
const wakeTitle = computed(() => (full.value ? resumeTitle(full.value) : ''))
const sessionLine = computed(() => {
  const s = primary.value
  if (!s) return t('work.card.noSession')
  if (s.missing) return t('work.card.missingSession')
  return [s.agent, runnerLabel(s.runner), agentStateLabel(s.state)].filter(Boolean).join(' · ')
})
const statusOptions = WORK_STATUSES
const suggestions = computed(() => pendingSuggestions(props.item))
const inflight = computed(() => inflightRequests(props.item))
const finished = computed(() => lastFinishedRequest(props.item))
const tidying = computed(() => inflight.value.some((r) => r.kind === 'summarize'))
const tidyBlock = computed(() => summarizeBlock(props.item))
const now = computed(() => props.nowSec ?? Math.floor(Date.now() / 1000))
const stalled = computed(() => stalledDays(props.item, now.value))
const dueKey = computed(() => dueLabelKey(props.item))

function src(field: string): string {
  return fieldSourceText(props.item, field, props.nowSec)
}

function suggestionField(field: string): string {
  return t(suggestionLabelKey(field))
}

function onStatusChange(e: Event): void {
  const v = (e.target as HTMLSelectElement).value as WorkStatus
  if (v && v !== props.item.status) emit('set-status', v)
  ;(e.target as HTMLSelectElement).value = ''
}

function whenText(sec: number | undefined): string {
  if (!sec) return ''
  return new Date(sec * 1000).toLocaleString()
}
</script>

<template>
  <InfoCard
    :tone="item.due ? 'hot' : statusTone(item.status)"
    :expanded="expanded"
    :active="active"
    :dim="item.status === 'dropped' || item.status === 'done'"
    openable
    :card-id="item.id"
    @toggle="emit('toggle')"
    @open="emit('open')"
  >
    <template #title>{{ item.title }}</template>
    <template #badges>
      <span data-test="card-status" class="card-status">
        <StatusPill :status="item.status" />
        <span v-if="item.status_source === 'human'" class="manual" :title="t('work.card.statusManualTitle')" data-test="status-manual">· {{ t('work.card.manual') }}</span>
      </span>
      <span v-if="dueKey" class="mark mark--need" data-test="due-badge">{{ t(dueKey) }}</span>
      <span v-if="stalled" class="mark mark--time" :title="t('work.card.stalledTitle')" data-test="stalled-badge">{{ t('work.card.stalled', { n: stalled }) }}</span>
      <span v-if="item.unsorted" class="mark mark--idle" data-test="unsorted-badge">{{ t('work.card.unsorted') }}</span>
      <span v-if="item.session_offline" class="mark mark--idle" data-test="offline-badge" :title="t('work.card.offlineTitle')">{{ t('work.card.offline') }}</span>
    </template>
    <template #meta>
      <span data-test="session-line">{{ sessionLine }}</span>
      <span>{{ workspaceLabel(item.workspace, item.project_key) }}</span>
      <span :title="whenText(item.last_activity_at)">{{ fmtAgo(item.last_activity_at, nowSec) }}</span>
    </template>
    <template #body>
      <p v-if="item.goal" class="icard-line icard-line--clamp" data-test="goal">{{ item.goal }}<span v-if="src('goal')" class="src mono" :class="`src--${fieldSourceKind(item, 'goal')}`" data-test="src-goal" :title="t('work.card.statusManualTitle')">{{ src('goal') }}</span></p>
      <p v-else-if="item.unsorted" class="icard-line icard-line--muted">{{ t('work.card.goalPending') }}</p>
      <p v-if="item.blocker_text" class="icard-line" data-test="blocker"><span class="icard-line--muted">{{ t('work.card.blocker') }}</span>{{ item.blocker_text }}<span v-if="src('blocker')" class="src mono" :class="`src--${fieldSourceKind(item, 'blocker')}`" data-test="src-blocker">{{ src('blocker') }}</span></p>
      <p v-if="item.next_step" class="icard-line" data-test="next"><span class="icard-line--muted">{{ t('work.card.next') }}</span>{{ item.next_step }}<span v-if="src('next')" class="src mono" :class="`src--${fieldSourceKind(item, 'next')}`" data-test="src-next">{{ src('next') }}</span></p>
      <div v-if="suggestions.length" class="suggest" data-test="suggestions">
        <div v-for="sg in suggestions" :key="sg.field" class="suggest-row" :data-field="sg.field">
          <span class="suggest-text"><span class="icard-line--muted">{{ t('work.card.suggestion', { field: suggestionField(sg.field) }) }}</span>{{ suggestionValueText(sg) }}</span>
          <span class="suggest-btns">
            <button class="btn btn--primary mono" type="button" :disabled="busy || !!writeBlock" :title="writeBlock || undefined" data-test="accept-suggestion" @click.stop="emit('accept-suggestion', sg.field)">{{ t('work.card.accept') }}</button>
            <button class="btn mono" type="button" :disabled="busy || !!writeBlock" :title="writeBlock || undefined" data-test="dismiss-suggestion" @click.stop="emit('dismiss-suggestion', sg.field)">{{ t('work.card.dismiss') }}</button>
          </span>
        </div>
      </div>
      <div v-if="inflight.length || finished" class="reqs" data-test="requests">
        <p v-for="r in inflight" :key="r.id" class="req mono req--live" data-test="request-live">{{ workRequestLine(r, now) }}</p>
        <p v-if="!inflight.length && finished" class="req mono" :class="`req--${finished.state}`" data-test="request-done">{{ workRequestLine(finished, now) }}</p>
      </div>
      <div v-if="tags.length" class="icard-meta">
        <span v-for="tag in tags" :key="tag" class="icard-chip mono">{{ tag }}</span>
      </div>
    </template>
    <template #actions>
      <button
        class="btn btn--primary mono"
        type="button"
        data-test="open-session"
        :disabled="!primary || primary.missing"
        :title="primary ? '' : t('work.card.noSession')"
        @click="primary && emit('open-session', primary.session_id)"
      >{{ t('work.card.openSession') }}</button>
      <button
        v-if="canWake && primary"
        class="btn mono"
        type="button"
        data-test="wake"
        :disabled="busy"
        :title="wakeTitle"
        @click="emit('wake', primary.session_id)"
      >{{ resumeLabel(full!) }}</button>
      <button
        class="btn mono"
        type="button"
        data-test="summarize"
        :disabled="busy || tidying || !!tidyBlock"
        :title="tidyBlock || t('work.card.summarizeTitle')"
        @click="emit('summarize')"
      >{{ tidying ? t('work.card.summarizing') : t('work.card.summarize') }}</button>
      <select class="btn status-select mono" data-test="status-select" :aria-label="t('work.card.statusAria')" :disabled="busy" @change="onStatusChange">
        <option value="">{{ t('work.card.statusPlaceholder') }}</option>
        <option v-for="m in statusOptions" :key="m.key" :value="m.key" :disabled="m.key === item.status">{{ t(`status.${m.key}`) }}</option>
      </select>
      <button class="btn mono" type="button" data-test="open-detail" @click="emit('open')">{{ t('work.card.edit') }}</button>
    </template>
    <template #details>
      <dl class="icard-kv mono">
        <template v-if="item.goal"><dt>{{ t('work.card.goal') }}</dt><dd>{{ item.goal }}<span v-if="src('goal')" class="src mono">{{ src('goal') }}</span></dd></template>
        <template v-if="item.blocker_text || item.blocker_kind"><dt>{{ t('work.card.blockerField') }}</dt><dd>{{ [item.blocker_kind, item.blocker_text].filter(Boolean).join(' · ') }}<span v-if="src('blocker')" class="src mono">{{ src('blocker') }}</span></dd></template>
        <template v-if="item.next_step"><dt>{{ t('work.card.nextField') }}</dt><dd>{{ item.next_step }}<span v-if="src('next')" class="src mono">{{ src('next') }}</span></dd></template>
        <template v-if="item.summary"><dt>{{ t('work.card.summary') }}</dt><dd>{{ item.summary }}<span v-if="src('summary')" class="src mono" data-test="src-summary">{{ src('summary') }}</span></dd></template>
        <template v-if="item.project_key"><dt>{{ t('work.card.project') }}</dt><dd>{{ item.project_key }}</dd></template>
        <template v-if="item.workspace"><dt>{{ t('work.card.workspace') }}</dt><dd>{{ item.workspace }}</dd></template>
        <template v-if="item.status === 'parked' && (item.park_until || item.park_note)">
          <dt>{{ t('work.card.parked') }}</dt><dd>{{ [item.park_until ? t('work.card.parkedUntil', { time: whenText(item.park_until) }) : '', item.park_note].filter(Boolean).join(' · ') }}</dd>
        </template>
        <template v-if="item.remind_at"><dt>{{ t('work.card.remind') }}</dt><dd>{{ whenText(item.remind_at) }}</dd></template>
        <dt>{{ t('work.card.source') }}</dt><dd>{{ item.source === 'auto' ? t('work.d.sourceAuto') : item.source === 'steward' ? t('work.d.sourceSteward') : t('work.d.sourceHuman') }}</dd>
        <dt>{{ t('work.card.status') }}</dt><dd>{{ item.status_source === 'auto' ? t('work.d.statusSrcAuto') : item.status_source === 'human' ? t('work.d.statusSrcHuman') : t('work.d.statusSrcReport') }}</dd>
        <dt>{{ t('work.card.id') }}</dt><dd>{{ item.id }}</dd>
      </dl>
      <div v-if="item.sessions.length" data-test="card-sessions">
        <div v-for="s in item.sessions" :key="s.session_id" class="icard-meta mono">
          <span class="icard-chip">{{ s.role === 'current' ? t('work.card.current') : t('work.card.past') }}</span>
          <span>{{ s.agent || '—' }} · {{ runnerLabel(s.runner) || '—' }} · {{ s.missing ? t('work.card.deleted') : agentStateLabel(s.state) }}</span>
          <button v-if="!s.missing" class="btn mono" type="button" @click="emit('open-session', s.session_id)">{{ t('work.card.open') }}</button>
        </div>
      </div>
      <div v-if="workUsageLabel(item.usage)" class="icard-meta mono" data-test="work-usage">{{ workUsageLabel(item.usage) }}</div>
      <div v-if="item.links.length" class="icard-meta">
        <span v-for="l in item.links" :key="l.kind + l.ref" class="icard-chip mono">{{ l.kind }} {{ l.ref }}</span>
      </div>
    </template>
  </InfoCard>
</template>

<style scoped>
.card-status { display: inline-flex; align-items: center; gap: 4px; }
.manual { font-size: var(--fs-xs); color: var(--ink-3); }
/* 卡片上的标记：到期 / 停滞 / 未整理 / 会话离线。颜色只表达「需要人 / 停滞 / 中性」。 */
.mark { display: inline-flex; align-items: center; gap: 4px; padding: 0 8px; border: 1px solid var(--line); border-radius: 999px; font-size: var(--fs-xs); color: var(--ink-3); white-space: nowrap; }
.mark--need { color: var(--need); border-color: var(--need); }
.mark--time { color: var(--time); border-color: var(--time); }
.mark--idle { color: var(--ink-3); }
.src {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  font-size: 10px;
  line-height: 16px;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: 999px;
  white-space: nowrap;
  vertical-align: baseline;
}
.src--summarizer { color: var(--run); border-color: var(--run); }
.src--session { color: var(--accent); border-color: var(--accent); }
.src--steward { color: var(--done); border-color: var(--done); }
.suggest { display: flex; flex-direction: column; gap: 4px; margin: 4px 0; padding: 6px 8px; border: 1px dashed var(--run); border-radius: var(--r-panel); }
.suggest-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px; font-size: var(--fs-sm); }
.suggest-text { min-width: 0; flex: 1 1 160px; overflow-wrap: anywhere; }
.suggest-btns { display: flex; gap: 6px; flex: none; }
.reqs { margin: 2px 0; }
.req { margin: 0; font-size: var(--fs-xs); color: var(--ink-3); }
.req--live { color: var(--accent); }
.req--failed, .req--expired { color: var(--need); }
.status-select { appearance: auto; max-width: 110px; }
@media (max-width: 760px) {
  .status-select, .btn { min-height: 40px; }
}
</style>
