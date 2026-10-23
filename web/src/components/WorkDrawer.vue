<script setup lang="ts">
// 工作项详情抽屉：字段编辑、日志时间线、会话（当前 / 历史）、关联项，以及整理类操作
// （标状态 / 搁置 / 提醒 / 完成·放弃 / 请它汇报 / 合并 / 拆分）。
// 外壳用 P2 的 ui/Drawer（手机铺满宽 = 两级页面的详情层）；写备注的输入框用统一 Composer。
// 修改都带 rev 做乐观锁：别人改过会收到 409，面板提示并重新拉取。
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import WorkDispatchDialog from './WorkDispatchDialog.vue'
import Drawer from '../ui/Drawer.vue'
import Composer from '../ui/Composer.vue'
import StatusPill from '../ui/StatusPill.vue'
import Button from '../ui/Button.vue'
import { t } from '../i18n'
import { isPendingSync, pendingSyncHint, pendingSyncLabel } from '../utils/dispatch'
import {
  acceptWorkSuggestion,
  addWorkNote,
  ApiError,
  dismissWorkSuggestion,
  attachWorkSession,
  deleteWorkItem,
  detachWorkSession,
  getWorkItem,
  linkWorkItem,
  mergeWorkItems,
  patchWorkItem,
  requestWorkReport,
  splitWorkItem,
  summarizeWorkItem,
  unlinkWorkItem,
  listPlans,
  workToTodo,
} from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import { runnerLabel } from '../utils/runnerDisplay'
import { agentStateLabel } from '../utils/sessionState'
import { healthText } from '../utils/todayLanes'
import { resumeLabel, resumeTitle } from '../utils/sessionResume'
import { copyText } from '../utils/sessionMessaging'
import {
  actorKind,
  actorLabel,
  fieldSourceKind,
  fieldSourceText,
  inflightRequests,
  linkedTodo,
  localInputToUnix,
  pendingSuggestions,
  reportRequestBlock,
  suggestionValueText,
  summarizeBlock,
  unixToLocalInput,
  whenPresets,
  WORK_STATUSES,
} from '../utils/work'
import { suggestionLabelKey, workRequestLine } from '../utils/workBoard'
import type { AgentSession, Plan, WorkDetail, WorkItem, WorkItemPatch, WorkReportRequestResult, WorkStatus } from '../api/types'

const props = defineProps<{
  id: string
  // 会话全量信息（can_resume / 状态），用来画「会话」区的按钮
  sessions: AgentSession[]
  // 其它工作项：合并候选
  items: WorkItem[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'changed'): void
  (e: 'open-session', sid: string): void
  (e: 'wake', sid: string): void
  (e: 'open-item', id: string): void
}>()

const detail = ref<WorkDetail | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))

// Drawer 外壳的开关：关闭时把意图交回页面（页面负责清掉 URL 里的 ?id=）。
const open = ref(true)
watch(open, (v) => {
  if (!v) emit('close')
})

const form = reactive({
  title: '', goal: '', blocker_kind: '', blocker_text: '', next_step: '', summary: '', project_key: '', workspace: '', priority: '0',
})

function fillForm(d: WorkDetail): void {
  form.title = d.title
  form.goal = d.goal ?? ''
  form.blocker_kind = d.blocker_kind ?? ''
  form.blocker_text = d.blocker_text ?? ''
  form.next_step = d.next_step ?? ''
  form.summary = d.summary ?? ''
  form.project_key = d.project_key ?? ''
  form.workspace = d.workspace ?? ''
  form.priority = String(d.priority ?? 0)
}

const dirty = computed(() => {
  const d = detail.value
  if (!d) return false
  return (
    form.title !== d.title || form.goal !== (d.goal ?? '') || form.blocker_kind !== (d.blocker_kind ?? '') ||
    form.blocker_text !== (d.blocker_text ?? '') || form.next_step !== (d.next_step ?? '') ||
    form.summary !== (d.summary ?? '') || form.project_key !== (d.project_key ?? '') ||
    form.workspace !== (d.workspace ?? '') || form.priority !== String(d.priority ?? 0)
  )
})

async function load(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) loading.value = true
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const d = await getWorkItem(props.id)
    detail.value = d
    // 正在编辑时（有未保存改动）不覆盖输入，只刷新其它区域
    if (!opts?.silent || !dirty.value) fillForm(d)
    error.value = ''
  } catch (e) {
    error.value = errText(e)
  } finally {
    loading.value = false
  }
}

function errText(e: unknown): string {
  if (e instanceof ApiError) return e.detail ? `${e.message}：${e.detail}` : e.message
  return e instanceof Error ? e.message : String(e)
}

// 所有写操作的统一入口：带上 rev，409 时提示并重拉。
async function patch(p: WorkItemPatch, okText = ''): Promise<boolean> {
  const d = detail.value
  if (!d || busy.value) return false
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const next = await patchWorkItem(d.id, { ...p, rev: d.rev })
    detail.value = next
    fillForm(next)
    if (okText) notice.value = okText
    emit('changed')
    return true
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      error.value = t('work.d.conflict')
      await load()
    } else {
      error.value = errText(e)
    }
    return false
  } finally {
    busy.value = false
  }
}

async function run<T>(fn: () => Promise<T>, okText = ''): Promise<T | undefined> {
  if (busy.value) return undefined
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await fn()
    if (okText) notice.value = okText
    emit('changed')
    return r
  } catch (e) {
    error.value = errText(e)
    return undefined
  } finally {
    busy.value = false
  }
}

async function saveFields(): Promise<void> {
  const prio = Number.parseInt(form.priority, 10)
  await patch(
    {
      title: form.title, goal: form.goal, blocker_kind: form.blocker_kind, blocker_text: form.blocker_text,
      next_step: form.next_step, summary: form.summary, project_key: form.project_key, workspace: form.workspace,
      priority: Number.isFinite(prio) ? prio : 0,
    },
    t('work.d.saved'),
  )
}

async function setStatus(st: WorkStatus): Promise<void> {
  if (st === 'dropped' && !window.confirm(t('work.d.dropConfirm'))) return
  await patch({ status: st }, t('work.d.statusSet', { status: t(`status.${st}`) }))
}

// 永久删除（仅已完成 / 已放弃的项）：二次确认，成功后通知列表刷新并关闭抽屉。
async function removeItem(): Promise<void> {
  const d = detail.value
  if (!d || busy.value || !closed.value) return
  if (!window.confirm(t('work.d.deleteConfirm', { title: d.title }))) return
  busy.value = true
  error.value = ''
  try {
    await deleteWorkItem(d.id)
    emit('changed')
    emit('close')
  } catch (e) {
    error.value = errText(e)
  } finally {
    busy.value = false
  }
}

async function handBackToAuto(): Promise<void> {
  await patch({ status_source: 'auto' }, t('work.d.handedBack'))
}

async function markSorted(): Promise<void> {
  await patch({ unsorted: false }, t('work.d.markedSorted'))
}

// ---------------- 搁置 / 提醒 ----------------
const parkUntil = ref('')
const parkNote = ref('')
const remindAt = ref('')
const presets = computed(() => whenPresets(new Date(nowSec.value * 1000)))

async function park(): Promise<void> {
  const until = localInputToUnix(parkUntil.value)
  if (!until && !parkNote.value.trim()) {
    error.value = t('work.d.parkNeedsWhen')
    return
  }
  const ok = await patch({ status: 'parked', park_until: until, park_note: parkNote.value.trim() }, t('work.d.parkedOk'))
  if (ok) {
    parkUntil.value = ''
    parkNote.value = ''
  }
}

async function setRemind(): Promise<void> {
  const at = localInputToUnix(remindAt.value)
  if (!at) {
    error.value = t('work.d.remindNeedsTime')
    return
  }
  if (await patch({ remind_at: at }, t('work.d.remindSet'))) remindAt.value = ''
}

async function clearRemind(): Promise<void> {
  await patch({ remind_at: 0 }, t('work.d.remindCleared'))
}

// ---------------- 请它汇报 / 写交接 / 整理 ----------------
const reportResults = ref<WorkReportRequestResult[]>([])
const reportBlock = computed(() => (detail.value ? reportRequestBlock(detail.value) : ''))
const tidyBlock = computed(() => (detail.value ? summarizeBlock(detail.value) : ''))
const tidying = computed(() => (detail.value ? inflightRequests(detail.value).some((r) => r.kind === 'summarize') : false))

async function askReport(kind: 'report' | 'handoff' = 'report'): Promise<void> {
  const d = detail.value
  if (!d || reportBlock.value) return
  const r = await run(() => requestWorkReport(d.id, '', kind), '')
  if (!r) return
  reportResults.value = r.results
  notice.value = r.sent ? t('work.d.reportSent') : t('work.d.reportFallback')
  void load({ silent: true })
}

async function tidyNow(): Promise<void> {
  const d = detail.value
  if (!d || tidyBlock.value || tidying.value) return
  if (await run(() => summarizeWorkItem(d.id), t('work.d.tidyStarted'))) void load({ silent: true })
}

async function onSuggestion(field: string, accept: boolean): Promise<void> {
  const d = detail.value
  if (!d) return
  const label = t(suggestionLabelKey(field))
  const r = await run(
    () => (accept ? acceptWorkSuggestion(d.id, field) : dismissWorkSuggestion(d.id, field)),
    accept ? t('work.d.suggestionAccepted', { field: label }) : t('work.d.suggestionDismissed', { field: label }),
  )
  if (r) {
    detail.value = r
    fillForm(r)
  }
}

const suggestions = computed(() => (detail.value ? pendingSuggestions(detail.value) : []))
const requests = computed(() => detail.value?.requests ?? [])
function src(field: string): string {
  return detail.value ? fieldSourceText(detail.value, field, nowSec.value) : ''
}
function srcKind(field: string): string {
  return detail.value ? fieldSourceKind(detail.value, field) : ''
}

const note = ref('')
async function addNote(text?: string): Promise<void> {
  const d = detail.value
  const body = (text ?? note.value).trim()
  if (!d || !body) return
  if (await run(() => addWorkNote(d.id, body), t('work.d.noteAdded'))) {
    note.value = ''
    await load({ silent: true })
  }
}

// ---------------- 会话 ----------------
const sessionById = computed(() => new Map(props.sessions.map((s) => [s.session_id, s])))
const attachCandidates = computed(() => {
  const have = new Set((detail.value?.sessions ?? []).filter((s) => s.role === 'current').map((s) => s.session_id))
  return props.sessions.filter((s) => !have.has(s.session_id) && s.state !== 'ended')
})
const attachSid = ref('')

async function attach(): Promise<void> {
  const d = detail.value
  if (!d || !attachSid.value) return
  const r = await run(() => attachWorkSession(d.id, attachSid.value), t('work.d.attachOk'))
  if (r) {
    detail.value = r
    attachSid.value = ''
  }
}

async function detach(sid: string): Promise<void> {
  const d = detail.value
  if (!d || !window.confirm(t('work.d.detachConfirm'))) return
  const r = await run(() => detachWorkSession(d.id, sid), t('work.d.detached'))
  if (r) detail.value = r
}

// job 会话（ACP / 终端）的 id 是完整 job id，不截断；中继会话取标题或 id 前 8 位。
function sessionTitle(s: { title?: string; session_id: string; kind?: string }): string {
  return s.title || (s.kind === 'job' ? s.session_id : s.session_id.slice(0, 8))
}

const copiedPeer = ref('')
async function copyPeerName(sid: string, name: string): Promise<void> {
  if (!(await copyText(name))) return
  copiedPeer.value = sid
  window.setTimeout(() => { if (copiedPeer.value === sid) copiedPeer.value = '' }, 1500)
}

function sessionLabel(sid: string): string {
  const s = sessionById.value.get(sid)
  return s?.title || sid.slice(0, 8)
}

// ---------------- 关联项 ----------------
const linkKind = ref<'issue' | 'plan' | 'todo' | 'job'>('issue')
const linkRef = ref('')

async function addLink(): Promise<void> {
  const d = detail.value
  const ref = linkRef.value.trim()
  if (!d || !ref) return
  const r = await run(() => linkWorkItem(d.id, linkKind.value, ref), t('work.d.linkOk'))
  if (r) {
    detail.value = r
    linkRef.value = ''
  }
}

async function removeLink(kind: string, ref: string): Promise<void> {
  const d = detail.value
  if (!d) return
  const r = await run(() => unlinkWorkItem(d.id, kind, ref), t('work.d.unlinkOk'))
  if (r) detail.value = r
}

// ---------------- 收集箱分派 ----------------
const dispatchDlg = ref(false)
const isCaptured = computed(() => (detail.value?.status as string | undefined) === 'captured')

function onDispatched(d: WorkDetail, text: string): void {
  detail.value = d
  fillForm(d)
  dispatchDlg.value = false
  notice.value = text
  error.value = ''
  emit('changed')
}

function onDispatchRefresh(): void {
  void load({ silent: true })
  emit('changed')
}

// ---------------- 转为 todo ----------------
const todoDlg = ref(false)
const todoPlans = ref<Plan[]>([])
const todoPlanId = ref('') // '' = 新建 plan
const todoNewTitle = ref('')
const todoErr = ref('')
const todoInfo = computed(() => (detail.value ? linkedTodo(detail.value.links) : null))

async function openTodoDlg(): Promise<void> {
  if (!detail.value) return
  todoErr.value = ''
  todoPlanId.value = ''
  todoNewTitle.value = detail.value.title
  todoDlg.value = true
  try {
    todoPlans.value = (await listPlans({ statuses: ['open', 'blocked'], limit: 100 })).plans ?? []
  } catch {
    todoPlans.value = []
  }
}

async function submitTodo(): Promise<void> {
  const d = detail.value
  if (!d || busy.value) return
  busy.value = true
  todoErr.value = ''
  try {
    const r = await workToTodo(d.id, todoPlanId.value ? { plan_id: todoPlanId.value } : { new_plan_title: todoNewTitle.value.trim() })
    detail.value = r.item
    todoDlg.value = false
    notice.value = r.plan_created
      ? t('work.d.toTodoCreatedPlan', { plan: r.plan_id })
      : t('work.d.toTodoCreated', { plan: r.plan_id })
    emit('changed')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      const b = (e.body ?? {}) as { todo_id?: string; plan_id?: string }
      todoErr.value = t('work.d.toTodoExists', { todo: b.todo_id ?? '', plan: b.plan_id ?? '' })
      void load({ silent: true })
    } else {
      todoErr.value = errText(e)
    }
  } finally {
    busy.value = false
  }
}

// ---------------- 合并 / 拆分 ----------------
const mergeIds = ref<Set<string>>(new Set())
const mergeCandidates = computed(() => props.items.filter((i) => i.id !== props.id && !['done', 'dropped'].includes(i.status)))
const sameWorkspaceFirst = computed(() => {
  const ws = detail.value?.workspace ?? ''
  return [...mergeCandidates.value].sort((a, b) => Number((b.workspace ?? '') === ws) - Number((a.workspace ?? '') === ws))
})

function toggleMerge(id: string): void {
  const next = new Set(mergeIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  mergeIds.value = next
}

async function merge(): Promise<void> {
  const d = detail.value
  if (!d || mergeIds.value.size === 0) return
  if (!window.confirm(t('work.d.mergeConfirm', { n: mergeIds.value.size, title: d.title }))) return
  const r = await run(() => mergeWorkItems(d.id, [...mergeIds.value]), t('work.d.merged'))
  if (r) {
    detail.value = r
    fillForm(r)
    mergeIds.value = new Set()
  }
}

const splitTitle = ref('')
const splitGoal = ref('')
const splitSids = ref<Set<string>>(new Set())
const splitKeep = ref(false)

function toggleSplitSid(sid: string): void {
  const next = new Set(splitSids.value)
  if (next.has(sid)) next.delete(sid)
  else next.add(sid)
  splitSids.value = next
}

async function split(): Promise<void> {
  const d = detail.value
  if (!d || !splitTitle.value.trim()) {
    error.value = t('work.d.splitNeedsTitle')
    return
  }
  const r = await run(
    () => splitWorkItem(d.id, { title: splitTitle.value.trim(), goal: splitGoal.value.trim(), session_ids: [...splitSids.value], keep_sessions: splitKeep.value }),
    t('work.d.splitDone'),
  )
  if (r) {
    detail.value = r.source
    fillForm(r.source)
    splitTitle.value = ''
    splitGoal.value = ''
    splitSids.value = new Set()
    emit('open-item', r.item.id)
  }
}

// ---------------- 展示 ----------------
// WORK-06：日志默认只看里程碑，点「全部」看流水（里程碑以外的自动状态、关联、整理过程）。
const journalAll = ref(false)
const journalMilestones = computed(() => (detail.value?.journal ?? []).filter((e) => e.level === 'milestone'))
const journal = computed(() => [...(journalAll.value ? (detail.value?.journal ?? []) : journalMilestones.value)].reverse())
// N3：健康度只在不正常时显示（阻塞 / 停滞 / 有风险）
const healthLabel = computed(() => healthText(detail.value?.health))
const currentSessions = computed(() => (detail.value?.sessions ?? []).filter((s) => s.role === 'current'))
const pastSessions = computed(() => (detail.value?.sessions ?? []).filter((s) => s.role === 'past'))
// 「完成 / 放弃」有单独的按钮，状态行只放活着的 6 个
const STATUS_BUTTONS = WORK_STATUSES.filter((m) => m.key !== 'done' && m.key !== 'dropped')
const closed = computed(() => detail.value?.status === 'done' || detail.value?.status === 'dropped')
const drawerLabel = computed(() => detail.value?.title || t('work.d.label'))

const KIND_LABEL_KEY: Record<string, string> = {
  report: 'work.d.kindReport',
  note: 'work.d.kindNote',
  status: 'work.d.kindStatus',
  steward: 'work.d.kindSteward',
  link: 'work.d.kindLink',
}

const live = createLiveTopic('work', { initial: false, fetch: () => load({ silent: true }) })
watch(() => props.id, () => void load())
onMounted(() => {
  void load()
  live.start()
})
onUnmounted(() => live.stop())
</script>

<template>
  <Drawer v-model="open" :label="drawerLabel" flush width="clamp(640px, 52vw, 1040px)">
    <div class="detail-head">
      <div class="head-main">
        <StatusPill v-if="detail" :status="detail.status" size="md" />
        <span v-if="detail?.due" class="mark mark--need">{{ t('work.card.dueRemind') }}</span>
        <span
          v-if="detail && healthLabel"
          class="health"
          :class="`health--${detail.health}`"
          :title="detail.health_reason"
          data-test="drawer-health"
        >{{ healthLabel }}<template v-if="detail.health_reason">：{{ detail.health_reason }}</template></span>
      </div>
      <div class="head-actions">
        <Button size="sm" :disabled="loading" data-test="drawer-refresh" @click="load()">{{ loading ? t('work.h.refreshing') : t('act.refresh') }}</Button>
      </div>
    </div>

    <div class="drawer-body">
      <p v-if="error" class="msg msg--err" data-test="drawer-error">{{ error }}</p>
      <p v-if="notice" class="msg msg--ok" data-test="drawer-notice">{{ notice }}</p>
      <p v-if="!detail && loading" class="empty">{{ t('work.d.loading') }}</p>

      <template v-if="detail">
        <section v-if="isCaptured" class="sec" data-test="drawer-dispatch">
          <h4 class="sec-title">{{ t('work.d.inbox') }}</h4>
          <p class="hint">{{ t('work.d.inboxHint') }}</p>
          <div class="row3">
            <Button variant="primary" size="sm" :disabled="busy" data-test="open-dispatch" @click="dispatchDlg = true">{{ t('work.d.dispatch') }}</Button>
          </div>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.status') }}</h4>
          <div class="status-row">
            <Button
              v-for="m in STATUS_BUTTONS"
              :key="m.key"
              size="sm"
              :variant="m.key === detail.status ? 'primary' : 'secondary'"
              :disabled="busy || m.key === detail.status"
              :title="m.hint"
              :data-test="`set-${m.key}`"
              @click="setStatus(m.key)"
            >{{ t(`status.${m.key}`) }}</Button>
          </div>
          <p class="hint">
            <template v-if="detail.status_source === 'human'">{{ t('work.d.statusHuman') }}</template>
            <template v-else-if="detail.status_source === 'report'">{{ t('work.d.statusReport') }}</template>
            <template v-else>{{ t('work.d.statusAuto') }}</template>
            <Button v-if="detail.status_source !== 'auto' && !closed" variant="ghost" size="sm" :disabled="busy" data-test="hand-back" @click="handBackToAuto">{{ t('work.d.handBack') }}</Button>
            <Button v-if="detail.unsorted" variant="ghost" size="sm" :disabled="busy" @click="markSorted">{{ t('work.d.markSorted') }}</Button>
          </p>
          <div class="status-row">
            <Button size="sm" :disabled="busy || closed" data-test="mark-done" @click="setStatus('done')">{{ t('work.d.done') }}</Button>
            <Button size="sm" :disabled="busy || closed" data-test="mark-dropped" @click="setStatus('dropped')">{{ t('work.d.drop') }}</Button>
            <Button v-if="closed" size="sm" :disabled="busy" @click="setStatus('active')">{{ t('work.d.reopen') }}</Button>
            <Button v-if="closed" size="sm" :disabled="busy" data-test="delete-item" @click="removeItem">{{ t('work.d.delete') }}</Button>
          </div>
        </section>

        <section v-if="suggestions.length" class="sec" data-test="drawer-suggestions">
          <h4 class="sec-title">{{ t('work.d.suggestions') }}</h4>
          <p class="hint">{{ t('work.d.suggestionsHint') }}</p>
          <article v-for="sg in suggestions" :key="sg.field" class="srow" :data-field="sg.field">
            <div class="srow-main">
              <strong>{{ t(suggestionLabelKey(sg.field)) }}</strong>
              <span class="sg-val">{{ suggestionValueText(sg) }}</span>
              <span class="hint">{{ actorLabel(sg.by) }}<template v-if="sg.confidence"> · {{ t('work.d.confidence', { n: Math.round(sg.confidence * 100) }) }}</template> · {{ fmtAgo(sg.at, nowSec) }}</span>
            </div>
            <div class="status-row">
              <Button variant="primary" size="sm" :disabled="busy" data-test="drawer-accept" @click="onSuggestion(sg.field, true)">{{ t('work.card.accept') }}</Button>
              <Button size="sm" :disabled="busy" data-test="drawer-dismiss" @click="onSuggestion(sg.field, false)">{{ t('work.card.dismiss') }}</Button>
            </div>
          </article>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.content') }}</h4>
          <div class="form">
            <label class="field">{{ t('work.d.title') }}<input v-model="form.title" type="text" data-test="f-title" /></label>
            <label class="field">{{ t('work.card.goal') }}<span v-if="src('goal')" class="src" :class="`src--${srcKind('goal')}`" data-test="drawer-src-goal">{{ src('goal') }}</span><textarea v-model="form.goal" rows="2" data-test="f-goal"></textarea></label>
            <div class="row2">
              <label class="field">{{ t('work.d.blockerKind') }}<input v-model="form.blocker_kind" type="text" :placeholder="t('work.d.blockerKindHint')" /></label>
              <label class="field">{{ t('work.d.blockerReason') }}<span v-if="src('blocker')" class="src" :class="`src--${srcKind('blocker')}`">{{ src('blocker') }}</span><input v-model="form.blocker_text" type="text" /></label>
            </div>
            <label class="field">{{ t('work.card.nextField') }}<span v-if="src('next')" class="src" :class="`src--${srcKind('next')}`">{{ src('next') }}</span><textarea v-model="form.next_step" rows="2"></textarea></label>
            <label class="field">{{ t('work.card.summary') }}<span v-if="src('summary')" class="src" :class="`src--${srcKind('summary')}`">{{ src('summary') }}</span><textarea v-model="form.summary" rows="2"></textarea></label>
            <div class="row2">
              <label class="field">{{ t('work.card.project') }}<input v-model="form.project_key" type="text" /></label>
              <label class="field">{{ t('work.d.priority') }}<input v-model="form.priority" type="number" /></label>
            </div>
            <label class="field">{{ t('work.card.workspace') }}<input v-model="form.workspace" type="text" /></label>
            <div class="status-row">
              <Button variant="primary" size="sm" :disabled="busy || !dirty" data-test="save-fields" @click="saveFields">{{ busy ? t('work.d.saving') : t('work.d.save') }}</Button>
              <span v-if="dirty" class="hint">{{ t('work.d.dirty') }}</span>
            </div>
          </div>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.parkAndRemind') }}</h4>
          <p v-if="detail.status === 'parked'" class="hint">
            <template v-if="detail.park_until">{{ t('work.d.parkedUntil', { time: fmtDateTime(detail.park_until) }) }}</template>
            <template v-else>{{ t('work.card.parked') }}</template>
            <template v-if="detail.park_note"> · {{ t('work.d.parkedNote', { note: detail.park_note }) }}</template>
          </p>
          <p v-if="detail.remind_at" class="hint">
            {{ t('work.d.remind', { time: fmtDateTime(detail.remind_at) }) }}
            <Button variant="ghost" size="sm" :disabled="busy" data-test="clear-remind" @click="clearRemind">{{ t('work.d.clear') }}{{ detail.due ? t('work.d.acknowledge') : '' }}</Button>
          </p>
          <div class="form">
            <div class="preset-row">
              <button v-for="p in presets" :key="'p' + p.key" type="button" class="chip-btn" @click="parkUntil = unixToLocalInput(p.at)">{{ p.label }}</button>
            </div>
            <div class="row2">
              <label class="field">{{ t('work.d.parkUntil') }}<input v-model="parkUntil" type="datetime-local" data-test="park-until" /></label>
              <label class="field">{{ t('work.d.parkNote') }}<input v-model="parkNote" type="text" :placeholder="t('work.d.parkNoteHint')" data-test="park-note" /></label>
            </div>
            <div class="status-row">
              <Button size="sm" :disabled="busy" data-test="park-btn" @click="park">{{ t('work.d.park') }}</Button>
            </div>
            <div class="preset-row">
              <button v-for="p in presets" :key="'r' + p.key" type="button" class="chip-btn" @click="remindAt = unixToLocalInput(p.at)">{{ p.label }}</button>
            </div>
            <div class="row2">
              <label class="field">{{ t('work.d.remindAt') }}<input v-model="remindAt" type="datetime-local" data-test="remind-at" /></label>
              <div class="field"><Button size="sm" :disabled="busy" data-test="remind-btn" @click="setRemind">{{ t('work.d.setRemind') }}</Button></div>
            </div>
          </div>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.sessions') }}</h4>
          <div class="status-row">
            <Button
              variant="primary"
              type="button"
              data-test="ask-report"
              :disabled="busy || !!reportBlock"
              :title="reportBlock || t('work.d.askReportTitle')"
              @click="askReport('report')"
            >{{ t('work.d.askReport') }}</Button>
            <Button
              size="sm"
              type="button"
              data-test="ask-handoff"
              :disabled="busy || !!reportBlock"
              :title="t('work.d.askHandoffTitle')"
              @click="askReport('handoff')"
            >{{ t('work.d.askHandoff') }}</Button>
            <Button
              size="sm"
              type="button"
              data-test="drawer-summarize"
              :disabled="busy || tidying || !!tidyBlock"
              :title="tidyBlock || t('work.card.summarizeTitle')"
              @click="tidyNow"
            >{{ tidying ? t('work.card.summarizing') : t('work.card.summarize') }}</Button>
            <span v-if="reportBlock" class="hint" data-test="report-block">{{ reportBlock }}</span>
          </div>
          <ul v-if="reportResults.length" class="plain">
            <li v-for="r in reportResults" :key="r.session_id" class="hint">
              {{ sessionLabel(r.session_id) }}：{{ r.sent ? t('work.d.delivered') : r.kind === 'summarize' ? t('work.d.summarizeFallback', { reason: r.reason || '' }) : t('work.d.notDelivered', { reason: r.reason || t('work.d.reasonUnknown') }) }}
            </li>
          </ul>
          <ul v-if="requests.length" class="plain" data-test="drawer-requests">
            <li v-for="r in requests" :key="r.id" class="hint">{{ workRequestLine(r, nowSec) }} · {{ r.id }} · {{ fmtAgo(r.created_at, nowSec) }}</li>
          </ul>
          <p v-if="!currentSessions.length" class="hint">{{ t('work.d.noSession') }}</p>
          <article v-for="s in currentSessions" :key="s.session_id" class="srow" data-test="session-row">
            <div class="srow-main">
              <strong>{{ sessionTitle(s) }}</strong>
              <Button
                v-if="s.peer_name"
                class="link-btn peer-name"
                type="button"
                data-test="session-peer-name"
                :title="t('work.d.peerName')"
                @click="copyPeerName(s.session_id, s.peer_name!)"
              >{{ s.peer_name }}{{ copiedPeer === s.session_id ? t('work.d.peerCopied') : t('work.d.peerCopy') }}</Button>
              <span class="hint">{{ s.agent || '—' }} · {{ runnerLabel(s.runner) || '—' }} · {{ s.missing ? t('work.card.deleted') : agentStateLabel(s.state) }} · {{ fmtAgo(s.last_seen_at, nowSec) }}</span>
            </div>
            <div class="status-row">
              <Button v-if="!s.missing" size="sm" :data-test="s.kind === 'job' ? 'open-job-session' : undefined" @click="emit('open-session', s.session_id)">{{ s.kind === 'job' ? t('work.d.openJob') : t('work.d.openRelay') }}</Button>
              <Button
                v-if="s.offline && sessionById.get(s.session_id)?.can_resume"
              size="sm"
                type="button"
                :title="resumeTitle(sessionById.get(s.session_id)!)"
                @click="emit('wake', s.session_id)"
              >{{ resumeLabel(sessionById.get(s.session_id)!) }}</Button>
              <Button size="sm" :disabled="busy" @click="detach(s.session_id)">{{ t('work.d.detach') }}</Button>
            </div>
          </article>
          <details v-if="pastSessions.length" class="past">
            <summary>{{ t('work.d.pastSessions', { n: pastSessions.length }) }}</summary>
            <div v-for="s in pastSessions" :key="s.session_id" class="srow hint">
              {{ sessionTitle(s) }} · {{ s.agent || '—' }} · {{ s.missing ? t('work.card.deleted') : agentStateLabel(s.state) }}
              <Button v-if="!s.missing" variant="ghost" size="sm" @click="emit('open-session', s.session_id)">{{ t('work.card.open') }}</Button>
            </div>
          </details>
          <div v-if="attachCandidates.length" class="row2">
            <label class="field">{{ t('work.d.attach') }}
              <select v-model="attachSid"><option value="">{{ t('work.d.attachPick') }}</option><option v-for="s in attachCandidates" :key="s.session_id" :value="s.session_id">{{ sessionTitle(s) }} · {{ s.agent }}</option></select>
            </label>
            <div class="field"><Button size="sm" :disabled="busy || !attachSid" @click="attach">{{ t('work.d.attachBtn') }}</Button></div>
          </div>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.links') }}</h4>
          <ul v-if="detail.links.length" class="plain">
            <li v-for="l in detail.links" :key="l.kind + l.ref" class="link-row">
              <span class="icard-chip">{{ l.kind }}</span>
              <RouterLink v-if="l.kind === 'job'" :to="`/jobs/${encodeURIComponent(l.ref)}`">{{ l.ref }}</RouterLink>
              <RouterLink v-else-if="l.kind === 'plan'" :to="`/plans/${encodeURIComponent(l.ref)}`">{{ l.ref }}</RouterLink>
              <RouterLink v-else-if="l.kind === 'todo' && todoInfo?.planId" :to="`/plans/${encodeURIComponent(todoInfo.planId)}`" :title="t('work.d.openPlan')">{{ l.ref }}</RouterLink>
              <RouterLink v-else-if="l.kind === 'issue'" :to="`/issues?issue=${encodeURIComponent(l.ref)}`">{{ l.ref }}</RouterLink>
              <span v-else>{{ l.ref }}</span>
              <span v-if="isPendingSync(l)" class="icard-chip pending-sync" :title="pendingSyncHint()" data-test="pending-sync">{{ pendingSyncLabel() }}</span>
              <Button variant="ghost" size="sm" :disabled="busy" @click="removeLink(l.kind, l.ref)">{{ t('work.d.remove') }}</Button>
            </li>
          </ul>
          <p v-else class="hint">{{ t('work.d.noLinks') }}</p>
          <div class="row3" data-test="to-todo-row">
            <Button v-if="!todoInfo" size="sm" :disabled="busy" data-test="to-todo" @click="openTodoDlg">{{ t('work.d.toTodo') }}</Button>
            <template v-else>
              <Button size="sm" disabled data-test="to-todo-done">{{ t('work.d.toTodoDone') }}</Button>
              <RouterLink v-if="todoInfo.planId" :to="`/plans/${encodeURIComponent(todoInfo.planId)}`" data-test="to-todo-open">{{ t('work.d.openPlan') }}</RouterLink>
            </template>
          </div>
          <div class="row3">
            <select v-model="linkKind" class="field-sel" data-test="link-kind"><option value="issue">issue</option><option value="plan">plan</option><option value="todo">todo</option><option value="job">job</option></select>
            <input v-model="linkRef" class="field-in" type="text" placeholder="id" data-test="link-ref" @keydown.enter="addLink" />
            <Button size="sm" :disabled="busy || !linkRef.trim()" data-test="add-link" @click="addLink">{{ t('work.d.linkBtn') }}</Button>
          </div>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.mergeSplit') }}</h4>
          <details class="past">
            <summary>{{ t('work.d.mergeSummary') }}</summary>
            <p v-if="!sameWorkspaceFirst.length" class="hint">{{ t('work.d.mergeNone') }}</p>
            <label v-for="i in sameWorkspaceFirst" :key="i.id" class="check">
              <input type="checkbox" :checked="mergeIds.has(i.id)" @change="toggleMerge(i.id)" />
              {{ i.title }} <span class="hint">{{ i.workspace === detail.workspace ? t('work.d.mergeSameWorkspace') : '' }}</span>
            </label>
            <Button size="sm" :disabled="busy || !mergeIds.size" data-test="merge-btn" @click="merge">{{ t('work.d.merge', { n: mergeIds.size }) }}</Button>
          </details>
          <details class="past">
            <summary>{{ t('work.d.splitSummary') }}</summary>
            <div class="form">
              <label class="field">{{ t('work.d.splitTitle') }}<input v-model="splitTitle" type="text" data-test="split-title" /></label>
              <label class="field">{{ t('work.d.splitGoal') }}<input v-model="splitGoal" type="text" /></label>
              <label v-for="s in currentSessions" :key="s.session_id" class="check">
                <input type="checkbox" :checked="splitSids.has(s.session_id)" @change="toggleSplitSid(s.session_id)" />
                {{ t('work.d.takeSession', { name: sessionTitle(s) }) }}
              </label>
              <label class="check"><input v-model="splitKeep" type="checkbox" /> {{ t('work.d.splitKeep') }}</label>
              <Button size="sm" :disabled="busy" data-test="split-btn" @click="split">{{ t('work.d.split') }}</Button>
            </div>
          </details>
        </section>

        <section class="sec">
          <h4 class="sec-title">{{ t('work.d.journal') }}</h4>
          <Composer
            v-model="note"
            :placeholder="t('work.d.journalPlaceholder')"
            :send-label="t('work.d.journalWrite')"
            :disabled="busy"
            :can-send="!busy"
            :aria-label="t('work.d.note')"
            data-test="note-composer"
            @send="addNote"
          />
          <div class="seg" role="group" :aria-label="t('work.d.journalLevel')" data-test="journal-level">
            <button type="button" :aria-pressed="!journalAll" data-test="journal-milestones" @click="journalAll = false">{{ t('work.d.journalMilestones') }}</button>
            <button type="button" :aria-pressed="journalAll" data-test="journal-all" @click="journalAll = true">{{ t('work.d.journalAll') }}</button>
          </div>
          <p v-if="!journalAll && !journalMilestones.length" class="hint" data-test="journal-no-milestones">{{ t('work.d.journalNone') }}</p>
          <ol class="timeline" data-test="journal">
            <li v-for="e in journal" :key="e.id" class="tl-item" :class="[`tl--${e.kind}`, `tl-actor--${actorKind(e.by)}`, { 'tl--detail': journalAll && e.level !== 'milestone' }]">
              <div class="tl-head">
                <span class="tl-kind">{{ KIND_LABEL_KEY[e.kind] ? t(KIND_LABEL_KEY[e.kind]) : e.kind }}</span>
                <span class="tl-by" :class="`tl-by--${actorKind(e.by)}`" data-test="tl-by">{{ actorLabel(e.by) }}</span>
                <span v-if="e.origin_item" class="hint" :title="t('work.d.journalFrom', { id: e.origin_item })">{{ t('work.d.journalFromShort', { id: e.origin_item }) }}</span>
                <span class="hint">{{ fmtDateTime(e.at) }}</span>
              </div>
              <div class="tl-text">{{ e.text }}</div>
            </li>
          </ol>
        </section>
      </template>
    </div>

    <WorkDispatchDialog
      v-if="dispatchDlg && detail"
      :item="detail"
      :sessions="sessions"
      @close="dispatchDlg = false"
      @done="onDispatched"
      @refresh="onDispatchRefresh"
    />
    <div v-if="todoDlg" class="todo-dlg-mask" @click.self="todoDlg = false">
      <div class="todo-dlg" role="dialog" :aria-label="t('work.d.toTodoTitle')" data-test="todo-dlg">
        <h4 class="sec-title">{{ t('work.d.toTodoTitle') }}</h4>
        <p class="hint">{{ t('work.d.toTodoHint') }}</p>
        <label class="field">
          <span>{{ t('work.d.toTodoPlan') }}</span>
          <select v-model="todoPlanId" class="field-sel" data-test="todo-plan">
            <option value="">{{ t('work.d.toTodoNewPlan') }}</option>
            <option v-for="p in todoPlans" :key="p.plan_id" :value="p.plan_id">{{ p.title || p.plan_id }}</option>
          </select>
        </label>
        <label v-if="!todoPlanId" class="field">
          <span>{{ t('work.d.toTodoNewPlanTitle') }}</span>
          <input v-model="todoNewTitle" class="field-in" type="text" data-test="todo-new-title" />
        </label>
        <p v-if="todoErr" class="msg msg--err" data-test="todo-dlg-error">{{ todoErr }}</p>
        <div class="row3">
          <Button variant="primary" size="sm" :disabled="busy" data-test="todo-submit" @click="submitTodo">{{ t('work.d.toTodoCreate') }}</Button>
          <Button size="sm" @click="todoDlg = false">{{ t('work.d.cancel') }}</Button>
        </div>
      </div>
    </div>
  </Drawer>
</template>

<style scoped>
/* 外壳（遮罩 / 面板 / 标题 / 关闭）由 ui/Drawer 提供；这里只负责内容。
   flush 让内容自己管滚动：头部固定，正文滚动。 */
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s2);
  flex: none;
  padding: var(--s2) var(--s4);
  border-bottom: 1px solid var(--line);
}
.head-main { display: flex; align-items: center; gap: var(--s2); min-width: 0; flex: 1; }
.head-actions { display: flex; gap: 6px; flex: none; }
.drawer-body { flex: 1; min-height: 0; overflow-y: auto; padding: var(--s3) var(--s4) 40px; display: flex; flex-direction: column; gap: var(--s4); }
.sec { display: flex; flex-direction: column; gap: var(--s2); padding-bottom: var(--s3); border-bottom: 1px dashed var(--line); }
.sec:last-child { border-bottom: 0; }
.sec-title { margin: 0; font-size: var(--fs-xs); letter-spacing: 0.08em; color: var(--ink-3); text-transform: uppercase; }
.status-row, .preset-row { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.hint { margin: 0; font-size: var(--fs-xs); color: var(--ink-3); }
.msg { margin: 0; padding: 6px 10px; font-size: var(--fs-sm); border-radius: var(--r-ctl); border: 1px solid var(--line); }
.msg--err { color: var(--fail); border-color: var(--fail); }
.msg--ok { color: var(--done); border-color: var(--done); }
.empty { color: var(--ink-3); font-size: var(--fs-sm); text-align: center; padding: 20px; }
.form { display: flex; flex-direction: column; gap: var(--s2); }
.field { display: flex; flex-direction: column; gap: 3px; font-size: var(--fs-xs); color: var(--ink-3); min-width: 0; }
.field input, .field textarea, .field select, .field-in, .field-sel {
  min-width: 0; color: var(--ink); background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--r-ctl); padding: 6px 7px; font: inherit; font-size: var(--fs-sm);
}
.field textarea { resize: vertical; }
.row2 { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--s2); align-items: end; }
.row3 { display: flex; gap: 6px; align-items: center; }
.row3 .field-in { flex: 1; }
.chip-btn { padding: 2px 8px; font: inherit; font-size: var(--fs-xs); color: var(--ink-3); background: transparent; border: 1px solid var(--line); border-radius: 999px; cursor: pointer; }
.chip-btn:hover { color: var(--accent); border-color: var(--accent); }
.link-btn { background: none; border: 0; padding: 0 4px; font: inherit; font-size: var(--fs-xs); color: var(--accent); cursor: pointer; text-decoration: underline; }
.link-btn:disabled { opacity: 0.45; cursor: default; }
.plain { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.link-row { display: flex; align-items: center; gap: var(--s2); font-size: var(--fs-sm); }
.srow { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px; padding: 6px 8px; border: 1px solid var(--line); border-radius: var(--r-panel); }
.peer-name { align-self: flex-start; padding: 0; }
.srow-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; font-size: var(--fs-sm); }
.past { border: 1px solid var(--line); border-radius: var(--r-panel); padding: 6px 8px; }
.past summary { cursor: pointer; font-size: var(--fs-xs); color: var(--ink-3); }
.check { display: flex; align-items: center; gap: 6px; font-size: var(--fs-sm); padding: 3px 0; }
.check input { accent-color: var(--accent); }
.timeline { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.tl--detail { opacity: 0.72; }
.seg { display: inline-flex; align-self: flex-start; border: 1px solid var(--line); border-radius: var(--r-ctl); overflow: hidden; }
.seg button { padding: 3px 10px; font: inherit; font-size: var(--fs-xs); color: var(--ink-3); background: transparent; border: 0; cursor: pointer; }
.seg button + button { border-left: 1px solid var(--line); }
.seg button[aria-pressed='true'] { color: var(--ink); background: var(--surface-2); }
.health { min-width: 0; font-size: var(--fs-xs); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.health--blocked, .health--stalled { color: var(--fail); }
.health--at_risk { color: var(--time); }
.mark { display: inline-flex; align-items: center; gap: 4px; padding: 0 8px; border: 1px solid var(--line); border-radius: 999px; font-size: var(--fs-xs); color: var(--ink-3); white-space: nowrap; }
.mark--need { color: var(--need); border-color: var(--need); }
.tl-item { padding: 6px 8px; border: 1px solid var(--line); border-left-width: 3px; border-radius: var(--r-panel); }
.tl--report { border-left-color: var(--accent); }
.tl--note { border-left-color: var(--run); }
.tl--steward { border-left-color: var(--done); }
.tl-head { display: flex; flex-wrap: wrap; gap: 4px 10px; font-size: var(--fs-xs); color: var(--ink); }
.tl-kind { color: var(--ink-3); }
.tl-by { padding: 0 6px; border: 1px solid var(--line); border-radius: 999px; }
.tl-by--human { color: var(--ink); }
.tl-by--session { color: var(--accent); border-color: var(--accent); }
.tl-by--summarizer { color: var(--run); border-color: var(--run); }
.tl-by--steward { color: var(--done); border-color: var(--done); }
.tl-actor--summarizer { border-left-color: var(--run); }
.tl-actor--session { border-left-color: var(--accent); }
.src { align-self: flex-start; padding: 0 6px; font-size: 10px; color: var(--ink-3); border: 1px solid var(--line); border-radius: 999px; }
.src--summarizer { color: var(--run); border-color: var(--run); }
.src--session { color: var(--accent); border-color: var(--accent); }
.src--steward { color: var(--done); border-color: var(--done); }
.sg-val { overflow-wrap: anywhere; color: var(--ink); }
.tl-text { margin-top: 3px; font-size: var(--fs-sm); white-space: pre-wrap; overflow-wrap: anywhere; color: var(--ink); }
.pending-sync { color: var(--run); border-color: var(--run); }
.todo-dlg-mask { position: fixed; inset: 0; z-index: 90; background: var(--scrim); display: flex; align-items: center; justify-content: center; padding: 16px; }
.todo-dlg { width: min(420px, 100%); background: var(--surface); border: 1px solid var(--line-strong); border-radius: var(--r-panel); padding: 14px; display: flex; flex-direction: column; gap: var(--s2); }
@media (max-width: 760px) {
  .row2 { grid-template-columns: minmax(0, 1fr); }
  .btn { min-height: 40px; }
}
</style>
