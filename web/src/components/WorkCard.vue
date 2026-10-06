<script setup lang="ts">
// 工作项卡片（「工作」页）。外壳是与 Sessions 页共用的 InfoCard；本组件只决定
// 「关键信息放哪、详情放哪」，不发请求——所有操作都 emit 给页面。
import { computed } from 'vue'
import InfoCard from './InfoCard.vue'
import { fmtAgo } from '../api/time'
import { runnerLabel } from '../utils/runnerDisplay'
import { agentStateLabel } from '../utils/sessionState'
import { resumeLabel, resumeTitle } from '../utils/sessionResume'
import {
  dueText,
  fieldSourceKind,
  fieldSourceText,
  inflightRequests,
  lastFinishedRequest,
  linkTags,
  pendingSuggestions,
  primarySession,
  requestLine,
  statusLabel,
  statusTone,
  suggestionLabel,
  suggestionValueText,
  summarizeBlock,
  WORK_STATUSES,
  workspaceLabel,
} from '../utils/work'
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
  }>(),
  { expanded: false, active: false, sessions: () => ({}), busy: false, nowSec: undefined },
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
const full = computed(() => (primary.value ? props.sessions[primary.value.session_id] : undefined))
const tone = computed(() => (props.item.due ? 'hot' : statusTone(props.item.status)))
const tags = computed(() => linkTags(props.item))
const canWake = computed(() => !!primary.value && primary.value.offline && !!full.value?.can_resume)
const wakeTitle = computed(() => (full.value ? resumeTitle(full.value) : ''))
const sessionLine = computed(() => {
  const s = primary.value
  if (!s) return '没有关联会话'
  if (s.missing) return '会话记录已不存在'
  return [s.agent, runnerLabel(s.runner), agentStateLabel(s.state)].filter(Boolean).join(' · ')
})
const statusOptions = WORK_STATUSES
const suggestions = computed(() => pendingSuggestions(props.item))
const inflight = computed(() => inflightRequests(props.item))
const finished = computed(() => lastFinishedRequest(props.item))
const tidying = computed(() => inflight.value.some((r) => r.kind === 'summarize'))
const tidyBlock = computed(() => summarizeBlock(props.item))
const now = computed(() => props.nowSec ?? Math.floor(Date.now() / 1000))

function src(field: string): string {
  return fieldSourceText(props.item, field, props.nowSec)
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
    :tone="tone"
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
      <span class="sbadge mono" :class="`sbadge--${statusTone(item.status)}`" :title="item.status_source === 'human' ? '你手动设置的状态（不会被会话自动覆盖）' : ''">{{ statusLabel(item.status) }}<template v-if="item.status_source === 'human'"> · 手动</template></span>
      <span v-if="item.due" class="sbadge sbadge--hot mono" data-test="due-badge">{{ dueText(item) }}</span>
      <span v-if="item.unsorted" class="sbadge sbadge--idle mono">未整理</span>
      <span v-if="item.session_offline" class="sbadge sbadge--off mono" data-test="offline-badge" title="当前会话进程已不在，可唤醒继续；工作项状态不变">会话已离线</span>
    </template>
    <template #meta>
      <span class="mono" data-test="session-line">{{ sessionLine }}</span>
      <span class="mono">{{ workspaceLabel(item.workspace, item.project_key) }}</span>
      <span class="mono" :title="whenText(item.last_activity_at)">{{ fmtAgo(item.last_activity_at, nowSec) }}</span>
    </template>
    <template #body>
      <p v-if="item.goal" class="icard-line icard-line--clamp" data-test="goal">{{ item.goal }}<span v-if="src('goal')" class="src mono" :class="`src--${fieldSourceKind(item, 'goal')}`" data-test="src-goal" title="这条信息是谁写的、何时">{{ src('goal') }}</span></p>
      <p v-else-if="item.unsorted" class="icard-line icard-line--muted">目标待补（会话汇报或手动填写）</p>
      <p v-if="item.blocker_text" class="icard-line" data-test="blocker"><span class="icard-line--muted">阻塞：</span>{{ item.blocker_text }}<span v-if="src('blocker')" class="src mono" :class="`src--${fieldSourceKind(item, 'blocker')}`" data-test="src-blocker">{{ src('blocker') }}</span></p>
      <p v-if="item.next_step" class="icard-line" data-test="next"><span class="icard-line--muted">下一步：</span>{{ item.next_step }}<span v-if="src('next')" class="src mono" :class="`src--${fieldSourceKind(item, 'next')}`" data-test="src-next">{{ src('next') }}</span></p>
      <div v-if="suggestions.length" class="suggest" data-test="suggestions">
        <div v-for="sg in suggestions" :key="sg.field" class="suggest-row" :data-field="sg.field">
          <span class="suggest-text"><span class="icard-line--muted">整理建议 · {{ suggestionLabel(sg.field) }}：</span>{{ suggestionValueText(sg) }}</span>
          <span class="suggest-btns">
            <button class="icard-btn icard-btn--primary mono" type="button" :disabled="busy" data-test="accept-suggestion" @click.stop="emit('accept-suggestion', sg.field)">采纳</button>
            <button class="icard-btn mono" type="button" :disabled="busy" data-test="dismiss-suggestion" @click.stop="emit('dismiss-suggestion', sg.field)">忽略</button>
          </span>
        </div>
      </div>
      <div v-if="inflight.length || finished" class="reqs" data-test="requests">
        <p v-for="r in inflight" :key="r.id" class="req mono req--live" data-test="request-live">{{ requestLine(r, now) }}</p>
        <p v-if="!inflight.length && finished" class="req mono" :class="`req--${finished.state}`" data-test="request-done">{{ requestLine(finished, now) }}</p>
      </div>
      <div v-if="tags.length" class="icard-meta"><span v-for="t in tags" :key="t" class="icard-chip mono">{{ t }}</span></div>
    </template>
    <template #actions>
      <button
        class="icard-btn icard-btn--primary mono"
        type="button"
        data-test="open-session"
        :disabled="!primary || primary.missing"
        :title="primary ? '' : '没有关联会话'"
        @click="primary && emit('open-session', primary.session_id)"
      >打开会话</button>
      <button
        v-if="canWake && primary"
        class="icard-btn mono"
        type="button"
        data-test="wake"
        :disabled="busy"
        :title="wakeTitle"
        @click="emit('wake', primary.session_id)"
      >{{ resumeLabel(full!) }}</button>
 <button
        class="icard-btn mono"
        type="button"
        data-test="summarize"
        :disabled="busy || tidying || !!tidyBlock"
        :title="tidyBlock || '读会话最近的对话，提炼目标 / 阻塞 / 下一步（一次性只读，不打扰会话）'"
        @click="emit('summarize')"
      >{{ tidying ? '整理中…' : '整理' }}</button>
      <select class="icard-btn mono status-select" data-test="status-select" aria-label="标状态" :disabled="busy" @change="onStatusChange">
        <option value="">标状态…</option>
        <option v-for="m in statusOptions" :key="m.key" :value="m.key" :disabled="m.key === item.status">{{ m.label }}</option>
      </select>
      <button class="icard-btn mono" type="button" data-test="open-detail" @click="emit('open')">编辑</button>
    </template>
    <template #details>
      <dl class="icard-kv mono">
        <template v-if="item.goal"><dt>目标</dt><dd>{{ item.goal }}<span v-if="src('goal')" class="src mono">{{ src('goal') }}</span></dd></template>
        <template v-if="item.blocker_text || item.blocker_kind"><dt>阻塞</dt><dd>{{ [item.blocker_kind, item.blocker_text].filter(Boolean).join(' · ') }}<span v-if="src('blocker')" class="src mono">{{ src('blocker') }}</span></dd></template>
        <template v-if="item.next_step"><dt>下一步</dt><dd>{{ item.next_step }}<span v-if="src('next')" class="src mono">{{ src('next') }}</span></dd></template>
        <template v-if="item.summary"><dt>摘要</dt><dd>{{ item.summary }}<span v-if="src('summary')" class="src mono" data-test="src-summary">{{ src('summary') }}</span></dd></template>
        <template v-if="item.project_key"><dt>项目</dt><dd>{{ item.project_key }}</dd></template>
        <template v-if="item.workspace"><dt>工作区</dt><dd>{{ item.workspace }}</dd></template>
        <template v-if="item.status === 'parked' && (item.park_until || item.park_note)">
          <dt>搁置</dt><dd>{{ [item.park_until ? `到 ${whenText(item.park_until)}` : '', item.park_note].filter(Boolean).join(' · ') }}</dd>
        </template>
        <template v-if="item.remind_at"><dt>提醒</dt><dd>{{ whenText(item.remind_at) }}</dd></template>
        <dt>来源</dt><dd>{{ item.source === 'auto' ? '会话首次提问自动创建' : item.source === 'steward' ? '管家整理' : '手动创建' }}</dd>
        <dt>状态</dt><dd>{{ item.status_source === 'auto' ? '跟随会话自动判定' : item.status_source === 'human' ? '你手动设置（优先于会话）' : '会话自汇报' }}</dd>
        <dt>ID</dt><dd>{{ item.id }}</dd>
      </dl>
      <div v-if="item.sessions.length" data-test="card-sessions">
        <div v-for="s in item.sessions" :key="s.session_id" class="icard-meta mono">
          <span class="icard-chip">{{ s.role === 'current' ? '当前' : '历史' }}</span>
          <span>{{ s.agent || '—' }} · {{ runnerLabel(s.runner) || '—' }} · {{ s.missing ? '记录已删除' : agentStateLabel(s.state) }}</span>
          <button v-if="!s.missing" class="icard-btn mono" type="button" @click="emit('open-session', s.session_id)">打开</button>
        </div>
      </div>
      <div v-if="item.links.length" class="icard-meta">
        <span v-for="l in item.links" :key="l.kind + l.ref" class="icard-chip mono">{{ l.kind }} {{ l.ref }}</span>
      </div>
    </template>
  </InfoCard>
</template>

<style scoped>
.src {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  font-size: 10px;
  line-height: 16px;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: 9px;
  white-space: nowrap;
  vertical-align: baseline;
}
.src--summarizer { color: var(--run); border-color: var(--run); }
.src--session { color: var(--phosphor); border-color: var(--phosphor); }
.src--steward { color: var(--done); border-color: var(--done); }
.suggest { display: flex; flex-direction: column; gap: 4px; margin: 4px 0; padding: 6px 8px; border: 1px dashed var(--run); border-radius: var(--radius); }
.suggest-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px; font-size: 12px; }
.suggest-text { min-width: 0; flex: 1 1 160px; overflow-wrap: anywhere; }
.suggest-btns { display: flex; gap: 6px; flex: none; }
.reqs { margin: 2px 0; }
.req { margin: 0; font-size: 11px; color: var(--queue); }
.req--live { color: var(--phosphor); }
.req--failed, .req--expired { color: var(--warning, var(--queue)); }
.status-select {
  appearance: auto;
  background: var(--panel);
  max-width: 92px;
}
</style>
