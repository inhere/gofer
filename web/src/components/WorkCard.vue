<script setup lang="ts">
// 工作项卡片（「工作」页）。外壳是与 Sessions 页共用的 InfoCard；本组件只决定
// 「关键信息放哪、详情放哪」，不发请求——所有操作都 emit 给页面。
import { computed } from 'vue'
import InfoCard from './InfoCard.vue'
import { fmtAgo } from '../api/time'
import { runnerLabel } from '../utils/runnerDisplay'
import { agentStateLabel } from '../utils/sessionState'
import { resumeLabel, resumeTitle } from '../utils/sessionResume'
import { dueText, linkTags, primarySession, statusLabel, statusTone, WORK_STATUSES, workspaceLabel } from '../utils/work'
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
      <p v-if="item.goal" class="icard-line icard-line--clamp" data-test="goal">{{ item.goal }}</p>
      <p v-else-if="item.unsorted" class="icard-line icard-line--muted">目标待补（会话汇报或手动填写）</p>
      <p v-if="item.blocker_text" class="icard-line" data-test="blocker"><span class="icard-line--muted">阻塞：</span>{{ item.blocker_text }}</p>
      <p v-if="item.next_step" class="icard-line" data-test="next"><span class="icard-line--muted">下一步：</span>{{ item.next_step }}</p>
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
      <select class="icard-btn mono status-select" data-test="status-select" aria-label="标状态" :disabled="busy" @change="onStatusChange">
        <option value="">标状态…</option>
        <option v-for="m in statusOptions" :key="m.key" :value="m.key" :disabled="m.key === item.status">{{ m.label }}</option>
      </select>
      <button class="icard-btn mono" type="button" data-test="open-detail" @click="emit('open')">编辑</button>
    </template>
    <template #details>
      <dl class="icard-kv mono">
        <template v-if="item.goal"><dt>目标</dt><dd>{{ item.goal }}</dd></template>
        <template v-if="item.blocker_text || item.blocker_kind"><dt>阻塞</dt><dd>{{ [item.blocker_kind, item.blocker_text].filter(Boolean).join(' · ') }}</dd></template>
        <template v-if="item.next_step"><dt>下一步</dt><dd>{{ item.next_step }}</dd></template>
        <template v-if="item.summary"><dt>摘要</dt><dd>{{ item.summary }}</dd></template>
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
.status-select {
  appearance: auto;
  background: var(--panel);
  max-width: 92px;
}
</style>
