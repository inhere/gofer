<script setup lang="ts">
// 工作项详情抽屉：字段编辑、日志时间线、会话（当前 / 历史）、关联项，以及整理类操作
// （标状态 / 搁置 / 提醒 / 完成·放弃 / 请它汇报 / 合并 / 拆分）。
// 修改都带 rev 做乐观锁：别人改过会收到 409，面板提示并重新拉取。
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import {
  acceptWorkSuggestion,
  addWorkNote,
  ApiError,
  dismissWorkSuggestion,
  attachWorkSession,
  detachWorkSession,
  getWorkItem,
  linkWorkItem,
  mergeWorkItems,
  patchWorkItem,
  requestWorkReport,
  splitWorkItem,
  summarizeWorkItem,
  unlinkWorkItem,
} from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import { runnerLabel } from '../utils/runnerDisplay'
import { agentStateLabel } from '../utils/sessionState'
import { resumeLabel, resumeTitle } from '../utils/sessionResume'
import {
  actorKind,
  actorLabel,
  fieldSourceKind,
  fieldSourceText,
  inflightRequests,
  localInputToUnix,
  pendingSuggestions,
  reportRequestBlock,
  requestLine,
  statusLabel,
  statusTone,
  suggestionLabel,
  suggestionValueText,
  summarizeBlock,
  unixToLocalInput,
  whenPresets,
  WORK_STATUSES,
} from '../utils/work'
import type { AgentSession, WorkDetail, WorkItem, WorkItemPatch, WorkReportRequestResult, WorkStatus } from '../api/types'

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
      error.value = '这个工作项刚被别人（或会话）改过，已刷新到最新；请确认后再改一次。'
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
    '已保存',
  )
}

async function setStatus(st: WorkStatus): Promise<void> {
  if (st === 'dropped' && !window.confirm('确定放弃这个工作项？它会移出看板（可在「显示已完成」里找回）。')) return
  await patch({ status: st }, `状态已改为「${statusLabel(st)}」`)
}

async function handBackToAuto(): Promise<void> {
  await patch({ status_source: 'auto' }, '状态已交还给会话自动判定')
}

async function markSorted(): Promise<void> {
  await patch({ unsorted: false }, '已标为整理好')
}

// ---------------- 搁置 / 提醒 ----------------
const parkUntil = ref('')
const parkNote = ref('')
const remindAt = ref('')
const presets = computed(() => whenPresets(new Date(nowSec.value * 1000)))

async function park(): Promise<void> {
  const until = localInputToUnix(parkUntil.value)
  if (!until && !parkNote.value.trim()) {
    error.value = '搁置需要写明什么时候回来（时间）和 / 或什么条件满足再继续。'
    return
  }
  const ok = await patch({ status: 'parked', park_until: until, park_note: parkNote.value.trim() }, '已搁置')
  if (ok) {
    parkUntil.value = ''
    parkNote.value = ''
  }
}

async function setRemind(): Promise<void> {
  const at = localInputToUnix(remindAt.value)
  if (!at) {
    error.value = '请选一个提醒时间。'
    return
  }
  if (await patch({ remind_at: at }, '提醒已设置')) remindAt.value = ''
}

async function clearRemind(): Promise<void> {
  await patch({ remind_at: 0 }, '提醒已清除')
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
  notice.value = r.sent ? '已请会话回复：它写回后这里会自动更新' : '没能送达给会话，已改为自动整理（见下方）'
  void load({ silent: true })
}

async function tidyNow(): Promise<void> {
  const d = detail.value
  if (!d || tidyBlock.value || tidying.value) return
  if (await run(() => summarizeWorkItem(d.id), '已开始整理：完成后这里会自动更新')) void load({ silent: true })
}

async function onSuggestion(field: string, accept: boolean): Promise<void> {
  const d = detail.value
  if (!d) return
  const r = await run(
    () => (accept ? acceptWorkSuggestion(d.id, field) : dismissWorkSuggestion(d.id, field)),
    accept ? `已采纳「${suggestionLabel(field)}」建议` : `已忽略「${suggestionLabel(field)}」建议`,
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
async function addNote(): Promise<void> {
  const d = detail.value
  const text = note.value.trim()
  if (!d || !text) return
  if (await run(() => addWorkNote(d.id, text), '备注已写入')) {
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
  const r = await run(() => attachWorkSession(d.id, attachSid.value), '已关联会话')
  if (r) {
    detail.value = r
    attachSid.value = ''
  }
}

async function detach(sid: string): Promise<void> {
  const d = detail.value
  if (!d || !window.confirm('取消关联这个会话？它会转入历史。')) return
  const r = await run(() => detachWorkSession(d.id, sid), '已取消关联')
  if (r) detail.value = r
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
  const r = await run(() => linkWorkItem(d.id, linkKind.value, ref), '已关联')
  if (r) {
    detail.value = r
    linkRef.value = ''
  }
}

async function removeLink(kind: string, ref: string): Promise<void> {
  const d = detail.value
  if (!d) return
  const r = await run(() => unlinkWorkItem(d.id, kind, ref), '已取消关联')
  if (r) detail.value = r
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
  if (!window.confirm(`把选中的 ${mergeIds.value.size} 个工作项合并进「${d.title}」？它们的会话、日志和关联项会并到这里，原工作项隐藏。`)) return
  const r = await run(() => mergeWorkItems(d.id, [...mergeIds.value]), '已合并')
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
    error.value = '拆分需要给新工作项起个标题。'
    return
  }
  const r = await run(
    () => splitWorkItem(d.id, { title: splitTitle.value.trim(), goal: splitGoal.value.trim(), session_ids: [...splitSids.value], keep_sessions: splitKeep.value }),
    '已拆分出新的工作项',
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
const journal = computed(() => [...(detail.value?.journal ?? [])].reverse())
const currentSessions = computed(() => (detail.value?.sessions ?? []).filter((s) => s.role === 'current'))
const pastSessions = computed(() => (detail.value?.sessions ?? []).filter((s) => s.role === 'past'))
// 「完成 / 放弃」有单独的按钮，状态行只放活着的 6 个
const STATUS_BUTTONS = WORK_STATUSES.filter((m) => m.key !== 'done' && m.key !== 'dropped')
const closed = computed(() => detail.value?.status === 'done' || detail.value?.status === 'dropped')

const KIND_LABEL: Record<string, string> = { report: '汇报', note: '备注', status: '变更', steward: '整理', link: '关联' }

const live = createLiveTopic('work', { initial: false, fetch: () => load({ silent: true }) })
watch(() => props.id, () => void load())
onMounted(() => {
  void load()
  live.start()
})
onUnmounted(() => live.stop())
</script>

<template>
  <div class="drawer-overlay" @click.self="emit('close')">
    <div class="drawer-panel" role="dialog" aria-label="工作项详情">
      <div class="drawer-head">
        <div class="head-main">
          <span class="drawer-title mono" :title="detail?.title">{{ detail?.title || id }}</span>
          <span v-if="detail" class="sbadge mono" :class="`sbadge--${statusTone(detail.status)}`">{{ statusLabel(detail.status) }}</span>
          <span v-if="detail?.due" class="sbadge sbadge--hot mono">到期</span>
        </div>
        <div class="head-actions mono">
          <button class="icard-btn mono" type="button" :disabled="loading" @click="load()">{{ loading ? '刷新中…' : '刷新' }}</button>
          <button class="icard-btn mono" type="button" data-test="drawer-close" @click="emit('close')">关闭</button>
        </div>
      </div>

      <div class="drawer-body">
        <p v-if="error" class="msg msg--err mono" data-test="drawer-error">{{ error }}</p>
        <p v-if="notice" class="msg msg--ok mono">{{ notice }}</p>
        <p v-if="!detail && loading" class="empty mono">加载中…</p>

        <template v-if="detail">
          <section class="sec">
            <h4 class="sec-title mono">状态</h4>
            <div class="status-row">
              <button
                v-for="m in STATUS_BUTTONS"
                :key="m.key"
                type="button"
                class="icard-btn mono"
                :class="{ 'icard-btn--primary': m.key === detail.status }"
                :disabled="busy || m.key === detail.status"
                :title="m.hint"
                :data-test="`set-${m.key}`"
                @click="setStatus(m.key)"
              >{{ m.label }}</button>
            </div>
            <p class="hint mono">
              <template v-if="detail.status_source === 'human'">状态是你手动设置的，会话再怎么跑也不会覆盖它。</template>
              <template v-else-if="detail.status_source === 'report'">状态来自会话自汇报；你改过之后以你为准。</template>
              <template v-else>状态跟随会话自动判定（执行中→进行中，等回复→等我，关联 job 待验收→待验收）。</template>
              <button v-if="detail.status_source !== 'auto' && !closed" class="link-btn mono" type="button" :disabled="busy" data-test="hand-back" @click="handBackToAuto">交还给自动判定</button>
              <button v-if="detail.unsorted" class="link-btn mono" type="button" :disabled="busy" @click="markSorted">标为已整理</button>
            </p>
            <div class="status-row">
              <button class="icard-btn mono" type="button" :disabled="busy || closed" data-test="mark-done" @click="setStatus('done')">✓ 完成</button>
              <button class="icard-btn mono" type="button" :disabled="busy || closed" data-test="mark-dropped" @click="setStatus('dropped')">放弃</button>
              <button v-if="closed" class="icard-btn mono" type="button" :disabled="busy" @click="setStatus('active')">重新打开</button>
            </div>
          </section>

          <section v-if="suggestions.length" class="sec" data-test="drawer-suggestions">
            <h4 class="sec-title mono">整理建议</h4>
            <p class="hint mono">整理器读了会话的对话后，对「你或会话已经写过」的内容给出的建议；采纳后算你写的，忽略后同一条不会再提。</p>
            <article v-for="sg in suggestions" :key="sg.field" class="srow" :data-field="sg.field">
              <div class="srow-main mono">
                <strong>{{ suggestionLabel(sg.field) }}</strong>
                <span class="sg-val">{{ suggestionValueText(sg) }}</span>
                <span class="hint">{{ actorLabel(sg.by) }}<template v-if="sg.confidence"> · 把握 {{ Math.round(sg.confidence * 100) }}%</template> · {{ fmtAgo(sg.at, nowSec) }}</span>
              </div>
              <div class="status-row">
                <button class="icard-btn icard-btn--primary mono" type="button" :disabled="busy" data-test="drawer-accept" @click="onSuggestion(sg.field, true)">采纳</button>
                <button class="icard-btn mono" type="button" :disabled="busy" data-test="drawer-dismiss" @click="onSuggestion(sg.field, false)">忽略</button>
              </div>
            </article>
          </section>

          <section class="sec">
            <h4 class="sec-title mono">内容</h4>
            <div class="form">
              <label class="field mono">标题<input v-model="form.title" type="text" data-test="f-title" /></label>
              <label class="field mono">目标<span v-if="src('goal')" class="src" :class="`src--${srcKind('goal')}`" data-test="drawer-src-goal">{{ src('goal') }}</span><textarea v-model="form.goal" rows="2" data-test="f-goal"></textarea></label>
              <div class="row2">
                <label class="field mono">阻塞类型<input v-model="form.blocker_kind" type="text" placeholder="设备 / 账号 / 现场 / 人…" /></label>
                <label class="field mono">阻塞原因<span v-if="src('blocker')" class="src" :class="`src--${srcKind('blocker')}`">{{ src('blocker') }}</span><input v-model="form.blocker_text" type="text" /></label>
              </div>
              <label class="field mono">下一步<span v-if="src('next')" class="src" :class="`src--${srcKind('next')}`">{{ src('next') }}</span><textarea v-model="form.next_step" rows="2"></textarea></label>
              <label class="field mono">摘要<span v-if="src('summary')" class="src" :class="`src--${srcKind('summary')}`">{{ src('summary') }}</span><textarea v-model="form.summary" rows="2"></textarea></label>
              <div class="row2">
                <label class="field mono">项目<input v-model="form.project_key" type="text" /></label>
                <label class="field mono">优先级<input v-model="form.priority" type="number" /></label>
              </div>
              <label class="field mono">工作区<input v-model="form.workspace" type="text" /></label>
              <div class="status-row">
                <button class="icard-btn icard-btn--primary mono" type="button" :disabled="busy || !dirty" data-test="save-fields" @click="saveFields">{{ busy ? '保存中…' : '保存修改' }}</button>
                <span v-if="dirty" class="hint mono">有未保存的修改</span>
              </div>
            </div>
          </section>

          <section class="sec">
            <h4 class="sec-title mono">搁置与提醒</h4>
            <p v-if="detail.status === 'parked'" class="hint mono">
              已搁置<template v-if="detail.park_until">到 {{ fmtDateTime(detail.park_until) }}</template><template v-if="detail.park_note">；条件：{{ detail.park_note }}</template>
            </p>
            <p v-if="detail.remind_at" class="hint mono">
              提醒：{{ fmtDateTime(detail.remind_at) }}
              <button class="link-btn mono" type="button" :disabled="busy" data-test="clear-remind" @click="clearRemind">清除{{ detail.due ? '（知道了）' : '' }}</button>
            </p>
            <div class="form">
              <div class="preset-row">
                <button v-for="p in presets" :key="'p' + p.key" type="button" class="chip-btn mono" @click="parkUntil = unixToLocalInput(p.at)">{{ p.label }}</button>
              </div>
              <div class="row2">
                <label class="field mono">搁置到<input v-model="parkUntil" type="datetime-local" data-test="park-until" /></label>
                <label class="field mono">或等条件<input v-model="parkNote" type="text" placeholder="例如：设备到货后继续" data-test="park-note" /></label>
              </div>
              <div class="status-row">
                <button class="icard-btn mono" type="button" :disabled="busy" data-test="park-btn" @click="park">搁置</button>
              </div>
              <div class="preset-row">
                <button v-for="p in presets" :key="'r' + p.key" type="button" class="chip-btn mono" @click="remindAt = unixToLocalInput(p.at)">{{ p.label }}</button>
              </div>
              <div class="row2">
                <label class="field mono">提醒我<input v-model="remindAt" type="datetime-local" data-test="remind-at" /></label>
                <div class="field"><button class="icard-btn mono" type="button" :disabled="busy" data-test="remind-btn" @click="setRemind">设提醒</button></div>
              </div>
            </div>
          </section>

          <section class="sec">
            <h4 class="sec-title mono">会话</h4>
            <div class="status-row">
              <button
                class="icard-btn icard-btn--primary mono"
                type="button"
                data-test="ask-report"
                :disabled="busy || !!reportBlock"
                :title="reportBlock || '向会话发一段固定的汇报请求，它用 gofer work report --request 写回；会话不在运行时改为自动整理'"
                @click="askReport('report')"
              >请它汇报</button>
              <button
                class="icard-btn mono"
                type="button"
                data-test="ask-handoff"
                :disabled="busy || !!reportBlock"
                title="请会话写一段交接：做到哪了、卡在哪、回来第一步"
                @click="askReport('handoff')"
              >请它写交接</button>
              <button
                class="icard-btn mono"
                type="button"
                data-test="drawer-summarize"
                :disabled="busy || tidying || !!tidyBlock"
                :title="tidyBlock || '读会话最近的对话，提炼目标 / 阻塞 / 下一步（一次性只读，不打扰会话）'"
                @click="tidyNow"
              >{{ tidying ? '整理中…' : '整理' }}</button>
              <span v-if="reportBlock" class="hint mono" data-test="report-block">{{ reportBlock }}</span>
            </div>
            <ul v-if="reportResults.length" class="plain">
              <li v-for="r in reportResults" :key="r.session_id" class="mono hint">
                {{ sessionLabel(r.session_id) }}：{{ r.sent ? '已送达' : r.kind === 'summarize' ? `已改为整理（${r.reason || '会话未在运行'}）` : `未送达（${r.reason || '原因不明'}）` }}
              </li>
            </ul>
            <ul v-if="requests.length" class="plain" data-test="drawer-requests">
              <li v-for="r in requests" :key="r.id" class="mono hint">{{ requestLine(r, nowSec) }} · {{ r.id }} · {{ fmtAgo(r.created_at, nowSec) }}</li>
            </ul>
            <p v-if="!currentSessions.length" class="hint mono">没有关联会话。</p>
            <article v-for="s in currentSessions" :key="s.session_id" class="srow" data-test="session-row">
              <div class="srow-main mono">
                <strong>{{ s.title || s.session_id.slice(0, 8) }}</strong>
                <span class="hint">{{ s.agent || '—' }} · {{ runnerLabel(s.runner) || '—' }} · {{ s.missing ? '记录已删除' : agentStateLabel(s.state) }} · {{ fmtAgo(s.last_seen_at, nowSec) }}</span>
              </div>
              <div class="status-row">
                <button v-if="!s.missing" class="icard-btn mono" type="button" @click="emit('open-session', s.session_id)">打开 / 传话</button>
                <button
                  v-if="s.offline && sessionById.get(s.session_id)?.can_resume"
                  class="icard-btn mono"
                  type="button"
                  :title="resumeTitle(sessionById.get(s.session_id)!)"
                  @click="emit('wake', s.session_id)"
                >{{ resumeLabel(sessionById.get(s.session_id)!) }}</button>
                <button class="icard-btn mono" type="button" :disabled="busy" @click="detach(s.session_id)">取消关联</button>
              </div>
            </article>
            <details v-if="pastSessions.length" class="past">
              <summary class="mono">历史会话（{{ pastSessions.length }}）</summary>
              <div v-for="s in pastSessions" :key="s.session_id" class="srow mono hint">
                {{ s.title || s.session_id.slice(0, 8) }} · {{ s.agent || '—' }} · {{ s.missing ? '记录已删除' : agentStateLabel(s.state) }}
                <button v-if="!s.missing" class="link-btn mono" type="button" @click="emit('open-session', s.session_id)">打开</button>
              </div>
            </details>
            <div v-if="attachCandidates.length" class="row2">
              <label class="field mono">关联会话
                <select v-model="attachSid"><option value="">选一个会话…</option><option v-for="s in attachCandidates" :key="s.session_id" :value="s.session_id">{{ s.title || s.session_id.slice(0, 8) }} · {{ s.agent }}</option></select>
              </label>
              <div class="field"><button class="icard-btn mono" type="button" :disabled="busy || !attachSid" @click="attach">关联</button></div>
            </div>
          </section>

          <section class="sec">
            <h4 class="sec-title mono">关联项</h4>
            <ul v-if="detail.links.length" class="plain">
              <li v-for="l in detail.links" :key="l.kind + l.ref" class="mono link-row">
                <span class="icard-chip">{{ l.kind }}</span>
                <RouterLink v-if="l.kind === 'job'" :to="`/jobs/${encodeURIComponent(l.ref)}`">{{ l.ref }}</RouterLink>
                <RouterLink v-else-if="l.kind === 'plan'" :to="`/plans/${encodeURIComponent(l.ref)}`">{{ l.ref }}</RouterLink>
                <RouterLink v-else-if="l.kind === 'issue'" :to="`/issues?issue=${encodeURIComponent(l.ref)}`">{{ l.ref }}</RouterLink>
                <span v-else>{{ l.ref }}</span>
                <button class="link-btn mono" type="button" :disabled="busy" @click="removeLink(l.kind, l.ref)">移除</button>
              </li>
            </ul>
            <p v-else class="hint mono">没有关联 issue / plan / job。（转成 todo 暂未支持，先把 todo id 关联上。）</p>
            <div class="row3">
              <select v-model="linkKind" class="field-sel mono" data-test="link-kind"><option value="issue">issue</option><option value="plan">plan</option><option value="todo">todo</option><option value="job">job</option></select>
              <input v-model="linkRef" class="field-in mono" type="text" placeholder="id" data-test="link-ref" @keydown.enter="addLink" />
              <button class="icard-btn mono" type="button" :disabled="busy || !linkRef.trim()" data-test="add-link" @click="addLink">关联</button>
            </div>
          </section>

          <section class="sec">
            <h4 class="sec-title mono">合并与拆分</h4>
            <details class="past">
              <summary class="mono">合并其它工作项进来（同一件事开了多个会话）</summary>
              <p v-if="!sameWorkspaceFirst.length" class="hint mono">没有其它未结束的工作项。</p>
              <label v-for="i in sameWorkspaceFirst" :key="i.id" class="check mono">
                <input type="checkbox" :checked="mergeIds.has(i.id)" @change="toggleMerge(i.id)" />
                {{ i.title }} <span class="hint">{{ i.workspace === detail.workspace ? '· 同工作区' : '' }}</span>
              </label>
              <button class="icard-btn mono" type="button" :disabled="busy || !mergeIds.size" data-test="merge-btn" @click="merge">合并 {{ mergeIds.size }} 个</button>
            </details>
            <details class="past">
              <summary class="mono">拆分出一个新工作项（一个会话做了两件事）</summary>
              <div class="form">
                <label class="field mono">新标题<input v-model="splitTitle" type="text" data-test="split-title" /></label>
                <label class="field mono">目标（可选）<input v-model="splitGoal" type="text" /></label>
                <label v-for="s in currentSessions" :key="s.session_id" class="check mono">
                  <input type="checkbox" :checked="splitSids.has(s.session_id)" @change="toggleSplitSid(s.session_id)" />
                  带走会话 {{ s.title || s.session_id.slice(0, 8) }}
                </label>
                <label class="check mono"><input v-model="splitKeep" type="checkbox" /> 同时保留在原工作项（一个会话两件事）</label>
                <button class="icard-btn mono" type="button" :disabled="busy" data-test="split-btn" @click="split">拆分</button>
              </div>
            </details>
          </section>

          <section class="sec">
            <h4 class="sec-title mono">日志</h4>
            <div class="row3">
              <input v-model="note" class="field-in mono" type="text" placeholder="写一条备注…" data-test="note-input" @keydown.enter="addNote" />
              <button class="icard-btn mono" type="button" :disabled="busy || !note.trim()" data-test="add-note" @click="addNote">写入</button>
            </div>
            <ol class="timeline" data-test="journal">
              <li v-for="e in journal" :key="e.id" class="tl-item" :class="[`tl--${e.kind}`, `tl-actor--${actorKind(e.by)}`]">
                <div class="tl-head mono">
                  <span class="tl-kind">{{ KIND_LABEL[e.kind] || e.kind }}</span>
                  <span class="tl-by" :class="`tl-by--${actorKind(e.by)}`" data-test="tl-by">{{ actorLabel(e.by) }}</span>
                  <span v-if="e.origin_item" class="hint" :title="`来自合并前的工作项 ${e.origin_item}`">· 来自 {{ e.origin_item }}</span>
                  <span class="hint">{{ fmtDateTime(e.at) }}</span>
                </div>
                <div class="tl-text">{{ e.text }}</div>
              </li>
            </ol>
          </section>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 78; /* 在会话抽屉(80)之下：从这里打开会话时，会话抽屉盖在上面 */
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.6);
}
.drawer-panel {
  display: flex;
  flex-direction: column;
  width: min(640px, 100vw);
  height: 100%;
  background: var(--panel);
  border-left: 1px solid var(--line);
  overflow: hidden;
}
.drawer-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  flex: none;
}
.head-main { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1; }
.drawer-title { min-width: 0; font-size: 13px; color: var(--paper); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.head-actions { display: flex; gap: 6px; flex: none; }
.drawer-body { flex: 1; overflow-y: auto; padding: 12px 14px 40px; display: flex; flex-direction: column; gap: 14px; }
.sec { display: flex; flex-direction: column; gap: 8px; padding-bottom: 12px; border-bottom: 1px dashed var(--line); }
.sec:last-child { border-bottom: 0; }
.sec-title { margin: 0; font-size: 11px; letter-spacing: 0.08em; color: var(--queue); text-transform: uppercase; }
.status-row, .preset-row { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.hint { margin: 0; font-size: 11px; color: var(--queue); }
.msg { margin: 0; padding: 6px 10px; font-size: 12px; border-radius: var(--radius); border: 1px solid var(--line); }
.msg--err { color: var(--fail); border-color: var(--fail); }
.msg--ok { color: var(--done); border-color: var(--done); }
.empty { color: var(--queue); font-size: 12px; text-align: center; padding: 20px; }
.form { display: flex; flex-direction: column; gap: 8px; }
.field { display: flex; flex-direction: column; gap: 3px; font-size: 11px; color: var(--queue); min-width: 0; }
.field input, .field textarea, .field select, .field-in, .field-sel {
  min-width: 0; color: var(--paper); background: var(--ink); border: 1px solid var(--line);
  border-radius: var(--radius); padding: 6px 7px; font: inherit; font-size: 12px;
}
.field textarea { resize: vertical; }
.row2 { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; align-items: end; }
.row3 { display: flex; gap: 6px; align-items: center; }
.row3 .field-in { flex: 1; }
.chip-btn { padding: 2px 8px; font-size: 10px; color: var(--queue); background: transparent; border: 1px solid var(--line); border-radius: 9px; cursor: pointer; }
.chip-btn:hover { color: var(--phosphor); border-color: var(--phosphor); }
.link-btn { background: none; border: 0; padding: 0 4px; font-size: 11px; color: var(--phosphor); cursor: pointer; text-decoration: underline; }
.link-btn:disabled { opacity: 0.45; cursor: default; }
.plain { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.link-row { display: flex; align-items: center; gap: 8px; font-size: 12px; }
.srow { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px; padding: 6px 8px; border: 1px solid var(--line); border-radius: var(--radius); }
.srow-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; font-size: 12px; }
.past { border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 8px; }
.past summary { cursor: pointer; font-size: 11px; color: var(--queue); }
.check { display: flex; align-items: center; gap: 6px; font-size: 12px; padding: 3px 0; }
.check input { accent-color: var(--phosphor); }
.timeline { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.tl-item { padding: 6px 8px; border: 1px solid var(--line); border-left-width: 3px; border-radius: var(--radius); }
.tl--report { border-left-color: var(--phosphor); }
.tl--note { border-left-color: var(--run); }
.tl--steward { border-left-color: var(--done); }
.tl-head { display: flex; flex-wrap: wrap; gap: 4px 10px; font-size: 10px; color: var(--paper); }
.tl-kind { color: var(--queue); }
.tl-by { padding: 0 6px; border: 1px solid var(--line); border-radius: 9px; }
.tl-by--human { color: var(--paper); }
.tl-by--session { color: var(--phosphor); border-color: var(--phosphor); }
.tl-by--summarizer { color: var(--run); border-color: var(--run); }
.tl-by--steward { color: var(--done); border-color: var(--done); }
.tl-actor--summarizer { border-left-color: var(--run); }
.tl-actor--session { border-left-color: var(--phosphor); }
.src { align-self: flex-start; padding: 0 6px; font-size: 10px; color: var(--queue); border: 1px solid var(--line); border-radius: 9px; }
.src--summarizer { color: var(--run); border-color: var(--run); }
.src--session { color: var(--phosphor); border-color: var(--phosphor); }
.src--steward { color: var(--done); border-color: var(--done); }
.sg-val { overflow-wrap: anywhere; color: var(--paper); }
.tl-text { margin-top: 3px; font-size: 12px; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--paper); }
@media (max-width: 640px) {
  .drawer-panel { width: 100vw; }
  .row2 { grid-template-columns: minmax(0, 1fr); }
}
</style>
