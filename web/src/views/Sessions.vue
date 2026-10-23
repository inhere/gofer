<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import InfoCard from '../components/InfoCard.vue'
import { toggleExpanded } from '../utils/cardExpand'
import { agentStateDim, agentStateLabel, agentStateTone } from '../utils/sessionState'
import { workspaceLabel } from '../utils/work'
import { sessionUsageRows } from '../utils/sessionUsage'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useRoute, useRouter } from 'vue-router'
import {
  downloadPtyRecording,
  listAgentSessions,
  listJobs,
  listRecentPtySessions,
  listWorkItems,
  attachWorkSession,
  resumeSession,
  setSessionRelay,
} from '../api/client'
import { fmtAgo, fmtDateTime, fmtDuration } from '../api/time'
import type { AgentSession, AgentSessionRelayMode, Job, PtySession, WorkItem } from '../api/types'
import SessionTopic from '../components/SessionTopic.vue'
import SessionDrawer from '../components/SessionDrawer.vue'
import SessionGrantDialog from '../components/SessionGrantDialog.vue'
import { refreshSessionGrants, useSessionGrants } from '../store/sessionGrants'
import { useWhoami } from '../store/whoami'
import { badgeTitle, canGrantSession, sessionGrantBadge } from '../utils/sessionGrant'
import { runnerLabel } from '../utils/runnerDisplay'
import { copyText, peerMessagingLabel, peerNameLabel, sessionDisplayName as formatSessionDisplayName, shortAgentSessionId } from '../utils/sessionMessaging'
import { resumeConfirmText, resumeFailText, resumeTitle } from '../utils/sessionResume'
import { refusedOwnerHelp, refusedOwnerNotice, transferFailText, transferOwnerLabel, transferSessionOwner } from '../utils/sessionOwner'
import { buildFindParams, findSummary, hitReasons, topicDisplay } from '../utils/sessionTopic'
import { t } from '../i18n'
import Tabs from '../ui/Tabs.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import FilterChips from '../ui/FilterChips.vue'
import NewSessionSheet from '../ui/NewSessionSheet.vue'
import { useProjectFilter } from '../ui/projectFilter'
import type { FilterChipItem } from '../ui/filterChips'
import { tabFromQuery } from '../ui/tabs'

const DEFAULT_LIMIT = 50
// 手机上新建表单默认收起：展开时它占满第一屏，会话列表要往下滑才看得到。
const relayHelpOpen = ref(false)
// 新建会话统一走 NewSessionSheet（设计稿 §8）；类型由入口决定。
const sheetOpen = ref(false)
const sheetType = ref<'pty' | 'acp'>('pty')

const route = useRoute()
const router = useRouter()

// 项目筛选（设计稿 §6.4）：URL ?project= 是唯一真源，三组会话都按它收窄。
const { project, setProject, options: projectOptions, loadOptions: loadProjectOptions } = useProjectFilter('/sessions')

function openSheet(type: 'pty' | 'acp'): void {
  sheetType.value = type
  sheetOpen.value = true
}

async function onSessionCreated(payload: { jobID: string; sessionID: string; type: 'pty' | 'acp' }): Promise<void> {
  if (payload.type === 'pty') {
    await router.push(`/jobs/${encodeURIComponent(payload.jobID)}?attach=1`)
  } else if (payload.sessionID) {
    await router.push({ path: '/workbench', query: { thread: `s:${payload.sessionID}` } })
  } else {
    // ACP 会话正常会在提交响应里带 session_id；保留 job 查询作为异步建会话窗口，
    // 仍然停留在对话工作台而不是跳到 job 详情页。
    await router.push({ path: '/workbench', query: { job: payload.jobID } })
  }
}

// ---------------- 页签、状态筛选与项目筛选（设计稿 §7.1） ----------------
type SessionTab = 'agent' | 'acp' | 'pty'
const tabItems = computed(() => [
  { value: 'agent', label: t('sessions.tab.agent'), count: agentSessions.value.length, badge: waitingCount.value > 0 },
  { value: 'acp', label: t('sessions.tab.acp'), count: acpSessions.value.length },
  { value: 'pty', label: t('sessions.tab.pty'), count: sessions.value.length },
])
const activeTab = computed<SessionTab>(() => {
  const value = tabFromQuery(route.query, tabItems.value) as SessionTab
  return value || 'agent'
})

// 状态 chip（一个页签一套口径）：值就是接口里的状态值，计数只算当前项目下的行。
const statusFilter = ref('')
function countStates(rows: string[]): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const state of rows) counts[state] = (counts[state] ?? 0) + 1
  return counts
}
const statusChips = computed<FilterChipItem[]>(() => {
  const all = { value: '', label: t('sessions.status.all') }
  if (activeTab.value === 'agent') {
    const rows = agentSessions.value.filter((s) => !project.value || s.project_key === project.value)
    const counts = countStates(rows.map((s) => s.state))
    return [
      { ...all, count: rows.length },
      { value: 'waiting_reply', label: t('sessions.status.waiting'), count: counts.waiting_reply },
      { value: 'running', label: t('sessions.status.running'), count: counts.running },
      { value: 'idle', label: t('sessions.status.idle'), count: counts.idle },
      { value: 'offline', label: t('sessions.status.offline'), count: counts.offline },
      { value: 'ended', label: t('sessions.status.ended'), count: counts.ended },
    ].filter((chip) => chip.value === '' || (chip.count ?? 0) > 0)
  }
  if (activeTab.value === 'acp') {
    const rows = acpSessions.value.filter((j) => !project.value || j.project_key === project.value)
    const counts = countStates(rows.map((j) => j.status))
    const ended = ['done', 'failed', 'cancelled', 'timeout', 'rejected']
    return [
      { ...all, count: rows.length },
      { value: 'awaiting_input', label: t('sessions.status.awaitingInput'), count: counts.awaiting_input },
      { value: 'running', label: t('sessions.status.running'), count: counts.running },
      { value: 'ended', label: t('sessions.status.ended'), count: ended.reduce((n, s) => n + (counts[s] ?? 0), 0) },
    ].filter((chip) => chip.value === '' || (chip.count ?? 0) > 0)
  }
  const counts = countStates(sessions.value.map((s) => (s.state === 'open' || s.state === 'attached' ? 'active' : 'ended')))
  return [
    { ...all, count: sessions.value.length },
    { value: 'active', label: t('sessions.status.active'), count: counts.active },
    { value: 'ended', label: t('sessions.status.ended'), count: counts.ended },
  ].filter((chip) => chip.value === '' || (chip.count ?? 0) > 0)
})

function matchesStatus(state: string): boolean {
  if (!statusFilter.value) return true
  if (statusFilter.value === 'ended') return ['done', 'failed', 'cancelled', 'timeout', 'rejected', 'closed'].includes(state)
  if (statusFilter.value === 'active') return state === 'open' || state === 'attached'
  return state === statusFilter.value
}

// 换页签时清掉上一个页签的状态筛选（两套状态值不通用）。
// 注意：这个 watch 会立即求值一次 activeTab → tabItems → 三个列表 ref，所以必须放在
// agentSessions / acpSessions / sessions 声明之后（放在这里会触发 TDZ，页面白屏）。

// 组内按状态排（需要人的在前：等回复 > 运行 > 空闲 > 离线 > 已结束），读起来就是按状态分组。
const AGENT_STATE_ORDER: Record<string, number> = {
  waiting_reply: 0,
  running: 1,
  idle: 2,
  offline: 3,
  ended: 4,
}
const ACP_STATE_ORDER: Record<string, number> = {
  awaiting_input: 0,
  pending_interaction: 0,
  running: 1,
  queued: 2,
}
function stateRank(order: Record<string, number>, state: string): number {
  return order[state] ?? 3
}

const filteredAgent = computed(() =>
  agentSessions.value
    .filter((s) => !project.value || s.project_key === project.value)
    .filter((s) => matchesStatus(s.state))
    .slice()
    .sort((a, b) => stateRank(AGENT_STATE_ORDER, a.state) - stateRank(AGENT_STATE_ORDER, b.state)),
)
const filteredAcp = computed(() =>
  acpSessions.value
    .filter((j) => !project.value || j.project_key === project.value)
    .filter((j) => matchesStatus(j.status))
    .slice()
    .sort((a, b) => stateRank(ACP_STATE_ORDER, a.status) - stateRank(ACP_STATE_ORDER, b.status)),
)
const filteredPty = computed(() =>
  sessions.value
    .filter((s) => matchesStatus(s.state))
    .slice()
    .sort((a, b) => Number(canAttachSession(b)) - Number(canAttachSession(a))),
)

// ---------------- Agent 会话（会话中继 SESS-01） ----------------
const agentSessions = ref<AgentSession[]>([])
const agentLoading = ref(false)
const agentError = ref('')
const showEnded = ref(false)
const relayBusyIds = ref<Set<string>>(new Set())
  const relayErrors = ref<Map<string, string>>(new Map())
  const copiedSessionIDs = ref<Set<string>>(new Set())
// 当前打开详情抽屉的会话 id（与 query ?sid= 同步）
const openSid = ref<string>(typeof route.query.sid === 'string' ? route.query.sid : '')
const openLastMessage = ref(false)


// 行内“唤醒”：已结束的会话也能唤醒。点一下先用 can_resume / resume_message 做确认，
// 再 POST resume，成功后跳到新 job 的终端。不能唤醒时按钮灰显，悬停显示原因。
const wakingIds = ref<Set<string>>(new Set())
const wakeErrors = ref<Map<string, string>>(new Map())

// 行内「转给 <caller>」（gofer-eh53）：多人调用方 server 上切 token 后，终端的 hook 以
// 另一个身份上报会被 owner 校验拒掉，会话状态就停更（refused_caller_id 有值）。管理员
// 点一下把会话转给 hook 现在用的那个 caller，心跳就会重新落下。
const ownerBusyIds = ref<Set<string>>(new Set())
const ownerErrors = ref<Map<string, string>>(new Map())

async function onTransferOwner(s: AgentSession): Promise<void> {
  const target = s.refused_caller_id ?? ''
  if (!target || ownerBusyIds.value.has(s.session_id)) return
  ownerBusyIds.value = new Set(ownerBusyIds.value).add(s.session_id)
  const errs = new Map(ownerErrors.value)
  errs.delete(s.session_id)
  ownerErrors.value = errs
  try {
    const updated = await transferSessionOwner(s.session_id, target)
    agentSessions.value = agentSessions.value.map((it) =>
      it.session_id === updated.session_id ? updated : it,
    )
  } catch (e) {
    ownerErrors.value = new Map(ownerErrors.value).set(s.session_id, transferFailText(e))
  } finally {
    const busy = new Set(ownerBusyIds.value)
    busy.delete(s.session_id)
    ownerBusyIds.value = busy
  }
}

async function onWake(s: AgentSession): Promise<void> {
  if (!s.can_resume || wakingIds.value.has(s.session_id)) return
  if (!window.confirm(resumeConfirmText(s))) return
  wakingIds.value = new Set(wakingIds.value).add(s.session_id)
  const errs = new Map(wakeErrors.value)
  errs.delete(s.session_id)
  wakeErrors.value = errs
  try {
    const res = await resumeSession(s.session_id)
    if (res.job_id) await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
    else void loadAgentSessions({ silent: true })
  } catch (e) {
    wakeErrors.value = new Map(wakeErrors.value).set(s.session_id, resumeFailText(e))
  } finally {
    const busy = new Set(wakingIds.value)
    busy.delete(s.session_id)
    wakingIds.value = busy
  }
}

// 展开 / 收起：三个区各自记自己的展开集合（与「工作」页共用 utils/cardExpand）。
const expandedAgent = ref<Set<string>>(new Set())
const expandedAcp = ref<Set<string>>(new Set())
const expandedPty = ref<Set<string>>(new Set())

// 会话所属的工作项（W1）：卡片上显示工作项标题并可跳转到「工作」页。
const workItems = ref<WorkItem[]>([])
const workBySession = computed(() => {
  const m = new Map<string, WorkItem>()
  for (const it of workItems.value) {
    for (const sid of it.session_ids ?? []) if (!m.has(sid)) m.set(sid, it)
  }
  return m
})

// ACP 持续会话 / 终端会话卡片按 job id 关联工作项（终端中继会话按 session id，见上）。
function workForJob(jobId?: string, sessionId?: string): WorkItem | undefined {
  return (jobId ? workBySession.value.get(jobId) : undefined) ?? (sessionId ? workBySession.value.get(sessionId) : undefined)
}
const linkPick = ref<Record<string, string>>({})
const linkBusy = ref('')
const linkError = ref('')
const linkableItems = computed(() => workItems.value.filter((i) => !['done', 'dropped'].includes(i.status)))
async function linkJobToWork(jobId: string): Promise<void> {
  const wid = linkPick.value[jobId]
  if (!wid) return
  linkBusy.value = jobId
  linkError.value = ''
  try {
    await attachWorkSession(wid, jobId)
    await loadWorkItems()
  } catch (e) {
    linkError.value = e instanceof Error ? e.message : String(e)
  } finally {
    linkBusy.value = ''
  }
}

async function loadWorkItems(): Promise<void> {
  try {
    workItems.value = (await listWorkItems({ limit: 500 })).items ?? []
  } catch {
    // 工作项只是附加信息：拉不到就不显示所属工作项，不影响会话列表。
    workItems.value = []
  }
}

const waitingCount = computed(
  () => agentSessions.value.filter((s) => s.state === 'waiting_reply').length,
)

// 会话状态文案（页面字典）：接口值 → i18n key，未知值（新状态）回落到共用映射。
const STATE_KEYS: Record<string, string> = {
  running: 'sessions.state.running',
  idle: 'sessions.state.idle',
  waiting_reply: 'sessions.state.waiting',
  needs_attention: 'sessions.state.attention',
  handed_off: 'sessions.state.handedOff',
  ended: 'sessions.state.ended',
  offline: 'sessions.state.offline',
}
function stateText(state: string): string {
  const key = STATE_KEYS[state]
  return key ? t(key) : agentStateLabel(state)
}
function wakeLabel(s: AgentSession): string {
  if (wakingIds.value.has(s.session_id)) return '…'
  return s.state === 'ended' ? t('sessions.card.wake') : t('sessions.card.takeover')
}
  function sessionDisplayName(s: AgentSession): string {
    return formatSessionDisplayName(s)
  }

  async function copySessionID(id: string, text: string = id): Promise<void> {
    // Clipboard is optional; the full id remains available in the title.
    if (!(await copyText(text))) return
    copiedSessionIDs.value = new Set(copiedSessionIDs.value).add(id)
    window.setTimeout(() => {
      const next = new Set(copiedSessionIDs.value)
      next.delete(id)
      copiedSessionIDs.value = next
    }, 1500)
  }

// idleText renders an idle reading (seconds) for the 中继 column: "8m", "1h05m",
// "—" when the hook could not tell (-1) or never reported.
function idleText(sec: number): string {
  if (sec < 0) {
    return '—'
  }
  if (sec < 60) {
    return `${sec}s`
  }
  const mins = Math.floor(sec / 60)
  if (mins < 60) {
    return `${mins}m`
  }
  const hours = Math.floor(mins / 60)
  return `${hours}h${String(mins % 60).padStart(2, '0')}m`
}

// RELAY_MODES 是三态开关的展示顺序（R1）。
const RELAY_MODES: AgentSessionRelayMode[] = ['auto', 'on', 'off']

// humanSilence 是人在这个会话里安静了多久（秒，-1 = 还没见过人工输入）；容器里
// 探测不到键盘时，server 用 last_human_at 算这个值（R2）。
function humanSilence(s: AgentSession): number {
  if (!s.last_human_at) {
    return -1
  }
  return Math.max(0, nowSec.value - s.last_human_at)
}

// relayEvidence 是 auto 判定的依据文字（R2）：键盘探得到就读空闲秒数，探不到的
// 终端读“距上次人工输入多久”。不等待时显示原因（SUP-01 D：caller 监督在跑的 job
// 时刻意不布防），两者都没有则留空。
function relayEvidence(s: AgentSession): string {
  if (s.relay_mode !== 'auto') {
    return ''
  }
  if (!s.wait_reason) {
    const supervising = s.wait_reason_detail?.match(/^supervising (\d+) jobs$/)
    return supervising
      ? t('sessions.relay.supervising', { n: supervising[1] })
      : s.wait_reason_detail
        ? t('sessions.relay.notArmed', { detail: s.wait_reason_detail })
        : ''
  }
  if (s.wait_reason === 'idle_probe') {
    return `auto (idle ${idleText(s.idle_sec)})`
  }
  if (s.wait_reason === 'turn_age') {
    return `auto (no input ${idleText(humanSilence(s))})`
  }
  return ''
}

// peerMessagingPending：「待上报」状态用稳定谓词判定，不跟界面文案比较（文案随语言变）。
function peerMessagingPending(s: AgentSession): boolean {
  return !s.peer_name && !s.peer_status && !s.peer_messaging
}

function relayTitle(s: AgentSession): string {
  const err = relayErrors.value.get(s.session_id)
  if (err) {
    return t('sessions.relay.switchFailed', { err })
  }
  const reason =
    s.wait_reason === 'mode_on'
      ? t('sessions.relay.reason.modeOn')
      : s.wait_reason === 'idle_probe'
        ? t('sessions.relay.reason.idle', { d: idleText(s.idle_sec) })
        : s.wait_reason === 'turn_age'
          ? t('sessions.relay.reason.turnAge', { d: idleText(humanSilence(s)) })
          : s.wait_reason_detail
            ? t('sessions.relay.reason.skipSupervising', { detail: s.wait_reason_detail })
            : t('sessions.relay.reason.none')
  switch (s.relay_mode) {
    case 'on':
      return t('sessions.relay.on', { reason })
    case 'off':
      return t('sessions.relay.off')
    default:
      return t('sessions.relay.auto', { reason })
  }
}

// 会话授权（根管理员）：行上「授权」按钮与「已授权」徽标。列表来自 GET /v1/auth/grants，
// 只有能管理授权的人才去读（别人读会 403）。
const { mode: whoMode, readOnly } = useWhoami()
const { grants: sessionGrantRows } = useSessionGrants()
const grantTarget = ref<AgentSession | null>(null)
function grantBadgeOf(s: AgentSession) {
  return sessionGrantBadge(sessionGrantRows.value, s, nowSec.value)
}
function grantBadgeTitle(s: AgentSession): string {
  return badgeTitle(grantBadgeOf(s), fmtDateTime)
}
watch(() => whoMode.value.canManage, (ok) => { if (ok) void refreshSessionGrants() }, { immediate: true })

// 查找（会话主题）：输入含 / 或 \\ 且无空白当路径，其余当关键词；结果按服务端相关度排序，卡片上带命中原因。
// 查找生效时刷新也走查找；清空输入回到普通列表。已结束的会话不参与查找。
const findInput = ref('')
const activeFind = ref<{ q?: string; path?: string } | null>(null)
const activeFindText = ref('')
function runFind(): void {
  activeFind.value = buildFindParams(findInput.value)
  activeFindText.value = findInput.value
  void loadAgentSessions()
}
function clearFind(): void {
  findInput.value = ''
  runFind()
}
function updateSession(u: AgentSession): void {
  agentSessions.value = agentSessions.value.map((it) =>
    it.session_id === u.session_id ? { ...u, hits: it.hits, maybe_offline: it.maybe_offline } : it,
  )
}

async function loadAgentSessions(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) {
    agentLoading.value = true
  }
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await listAgentSessions({ all: showEnded.value, limit: 100, ...(activeFind.value ?? {}) })
    agentSessions.value = resp.sessions ?? []
    agentError.value = ''
    if (whoMode.value.canManage) void refreshSessionGrants()
  } catch (e) {
    agentError.value = e instanceof Error ? e.message : String(e)
  } finally {
    agentLoading.value = false
  }
}

// 工作项变化（合并 / 拆分 / 关联会话）后刷新「所属工作项」。
const liveWork = createLiveTopic('work', { initial: false, fetch: () => loadWorkItems() })

// Q3：`sessions` 主题（会话状态 / 轮次 / 中继决策变化，不含心跳）的失效通知触发重拉；
// WS 断开超过 15s 才由 30s 兜底轮询接手（恢复后自动停）。
const liveSessions = createLiveTopic('sessions', {
  initial: false,
  fetch: () => loadAgentSessions({ silent: true }),
})

// 中继三态开关（行内）：点击即 POST，乐观更新，失败回滚并在行内提示。
async function onSetRelayMode(s: AgentSession, mode: AgentSessionRelayMode): Promise<void> {
  if (relayBusyIds.value.has(s.session_id) || s.relay_mode === mode) {
    return
  }
  const prev = s.relay_mode
  relayBusyIds.value = new Set(relayBusyIds.value).add(s.session_id)
  const errs = new Map(relayErrors.value)
  errs.delete(s.session_id)
  relayErrors.value = errs
  s.relay_mode = mode
  try {
    const updated = await setSessionRelay(s.session_id, mode)
    agentSessions.value = agentSessions.value.map((it) =>
      it.session_id === updated.session_id ? updated : it,
    )
  } catch (e) {
    const cur = agentSessions.value.find((it) => it.session_id === s.session_id)
    if (cur) {
      cur.relay_mode = prev
    }
    relayErrors.value = new Map(relayErrors.value).set(
      s.session_id,
      e instanceof Error ? e.message : String(e),
    )
  } finally {
    const busy = new Set(relayBusyIds.value)
    busy.delete(s.session_id)
    relayBusyIds.value = busy
  }
}

function openDrawer(sid: string, expandLastMessage = false): void {
  openSid.value = sid
  openLastMessage.value = expandLastMessage
  if (route.query.sid !== sid) {
    void router.replace({ query: { ...route.query, sid } })
  }
}

function closeDrawer(): void {
  openSid.value = ''
  openLastMessage.value = false
  if (route.query.sid) {
    const q = { ...route.query }
    delete q.sid
    void router.replace({ query: q })
  }
}

function onSessionDeleted(sid: string): void {
  agentSessions.value = agentSessions.value.filter((s) => s.session_id !== sid)
}

// 铃铛 / 外链跳到 /sessions?sid=xxx 时自动打开抽屉
watch(
  () => route.query.sid,
  (v) => {
    openSid.value = typeof v === 'string' ? v : ''
  },
)

watch(showEnded, () => {
  void loadAgentSessions()
  void loadAcpSessions()
})

// ---------------- pty 会话（既有） ----------------

const sessions = ref<PtySession[]>([])
const loading = ref(false)
const error = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))
const downloadingRecordingIds = ref<Set<string>>(new Set())

const hasSessions = computed(() => sessions.value.length > 0)

const acpSessions = ref<Job[]>([])
const acpLoading = ref(false)
const acpError = ref('')

function acpStatusLabel(job: Job): string {
  if (job.session_ending) return t('sessions.acp.ending')
  if (job.status === 'awaiting_input') return t('sessions.acp.awaitingInput')
  if (job.status === 'running') return t('sessions.acp.running')
  if (['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(job.status)) return t('sessions.acp.ended')
  return job.status
}

function acpTone(job: Job): 'live' | 'hot' | 'off' {
  if (['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(job.status)) return 'off'
  return job.status === 'awaiting_input' ? 'hot' : 'live'
}

function acpPreview(job: Job): string {
  return job.error || (job.status === 'awaiting_input' ? t('sessions.acp.previewAwaiting') : t('sessions.acp.previewOpen'))
}

async function loadAcpSessions(): Promise<void> {
  acpLoading.value = true
  try {
    const resp = await listJobs({ limit: 100 })
    acpSessions.value = (resp.jobs ?? [])
      .filter((job) => job.session)
      .filter((job) => showEnded.value || !['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(job.status))
      .sort((a, b) => (b.started_at ?? 0) - (a.started_at ?? 0))
    acpError.value = ''
  } catch (e) {
    acpError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acpLoading.value = false
  }
}

function fmtTime(v: number | undefined): string {
  if (!v) {
    return '—'
  }
  return new Date(v * 1000).toLocaleString()
}

function duration(s: PtySession): string {
  const end = s.ended_at && s.ended_at > 0 ? s.ended_at : nowSec.value
  return fmtDuration(Math.max(0, end - s.started_at))
}

function bytesText(s: PtySession): string {
  return `${s.bytes_in} / ${s.bytes_out} B`
}

function shortId(id?: string): string {
  if (!id) {
    return '—'
  }
  return id.length > 10 ? id.slice(-10) : id
}

function shortSessionID(id?: string): string {
  if (!id) {
    return '—'
  }
  return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id
}

function canAttachSession(s: PtySession): boolean {
  return !!s.job_id && (s.state === 'open' || s.state === 'attached') && !s.ended_at
}

async function onDownloadRecording(s: PtySession): Promise<void> {
  if (!s.job_id || !s.has_recording || downloadingRecordingIds.value.has(s.pty_session_id)) {
    return
  }
  downloadingRecordingIds.value = new Set(downloadingRecordingIds.value).add(s.pty_session_id)
  try {
    await downloadPtyRecording(s.job_id)
  } finally {
    const next = new Set(downloadingRecordingIds.value)
    next.delete(s.pty_session_id)
    downloadingRecordingIds.value = next
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await listRecentPtySessions(DEFAULT_LIMIT)
    sessions.value = resp.sessions ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 换页签时清掉上一个页签的状态筛选（两套状态值不通用）。
// 放在这里：watch 会立即求值一次 activeTab → tabItems → 三个列表 ref，必须等它们都初始化。
watch(activeTab, () => { statusFilter.value = '' })

onMounted(() => {
  void loadProjectOptions()
  void load()
  void loadAgentSessions()
  void loadAcpSessions()
  void loadWorkItems()
  liveSessions.start()
  liveWork.start()
})

onUnmounted(() => {
  liveSessions.stop()
  liveWork.stop()
})
</script>

<template>
  <div class="board">
    <header class="board-head">
      <div class="head-main">
        <h1 class="title">{{ t('sessions.title') }}</h1>
        <p class="head-summary">
          {{ t('sessions.summary', { n: agentSessions.length + acpSessions.length + sessions.length, waiting: waitingCount }) }}
        </p>
      </div>
      <div class="head-filters">
        <ProjectFilter
          :model-value="project"
          :options="projectOptions"
          @update:model-value="setProject"
        />
        <button
          v-if="!readOnly"
          class="act act--primary"
          type="button"
          data-test="new-session"
          @click="openSheet('pty')"
        >
          {{ t('sessions.new.title') }}
        </button>
      </div>
    </header>

    <div class="status-row">
      <FilterChips v-model="statusFilter" :items="statusChips" :label="t('sessions.filter.status')" />
    </div>

    <NewSessionSheet v-model="sheetOpen" :type="sheetType" @created="onSessionCreated" />

    <Tabs :tabs="tabItems" fallback="agent" :label="t('sessions.tabsLabel')">
      <template #default="{ active }">

    <!-- Agent 会话（会话中继 SESS-01）：hook 登记的 claude/codex 会话，点击行开详情抽屉 -->
    <section v-if="active === 'agent'" class="group">
      <header class="group-head">
        <div class="group-tools">
          <button class="relay-help-btn" type="button" :aria-expanded="relayHelpOpen" :title="t('sessions.relay.helpTitle')" @click="relayHelpOpen = !relayHelpOpen">?</button>
          <span v-if="waitingCount > 0" class="group-count group-count--hot">{{ t('sessions.waiting', { n: waitingCount }) }}</span>
        </div>
        <div class="controls">
          <label class="check">
            <input v-model="showEnded" type="checkbox" />
            {{ t('sessions.showEnded') }}
          </label>
          <button
            class="act"
            type="button"
            :disabled="agentLoading"
            @click="loadAgentSessions()"
          >
            {{ agentLoading ? t('sessions.refreshing') : t('sessions.refresh') }}
          </button>
        </div>
      </header>
      <!-- 中继三态开关说明：默认收起，点标题旁的「?」展开 -->
      <p v-if="relayHelpOpen" class="relay-note">
        {{ t('sessions.relay.help') }}
      </p>

      <form class="find-box" data-test="find-box" role="search" @submit.prevent="runFind">
        <input
          v-model="findInput"
          class="find-input"
          type="search"
          data-test="find-input"
          :placeholder="t('sessions.find.placeholder')"
          :aria-label="t('sessions.find.label')"
        />
        <button class="act" type="submit" data-test="find-submit">{{ t('sessions.find.submit') }}</button>
        <button v-if="activeFind || findInput" class="act" type="button" data-test="find-clear" @click="clearFind">{{ t('sessions.find.clear') }}</button>
      </form>
      <p v-if="activeFind && !agentLoading" class="find-note" data-test="find-summary">{{ findSummary(filteredAgent.length, activeFindText) }}</p>

      <p v-if="agentError" class="error mono">{{ agentError }}</p>

      <div v-if="filteredAgent.length > 0" class="icard-grid" data-test="agent-cards">
        <InfoCard
          v-for="s in filteredAgent"
          :key="s.session_id"
          :tone="agentStateTone(s.state)"
          :dim="agentStateDim(s.state)"
          :active="openSid === s.session_id"
          :expanded="expandedAgent.has(s.session_id)"
          :card-id="s.session_id"
          openable
          @toggle="expandedAgent = toggleExpanded(expandedAgent, s.session_id)"
          @open="openDrawer(s.session_id)"
        >
          <template #title><span :title="`${sessionDisplayName(s)}\n${s.session_id}`">{{ sessionDisplayName(s) }}</span></template>
          <template #badges>
            <span
              class="sbadge mono"
              :class="`sbadge--${agentStateTone(s.state)}`"
              :title="s.state === 'offline' ? t('sessions.card.offlineTitle', { time: fmtTime(s.last_seen_at) }) : undefined"
            >{{ stateText(s.state) }}</span>
            <span
              v-if="whoMode.canManage && grantBadgeOf(s).granted"
              class="sbadge mono sbadge--granted"
              data-test="session-granted-badge"
              :title="grantBadgeTitle(s)"
            >{{ t('sessions.card.granted') }}</span>
            <span
              v-if="s.refused_caller_id"
              class="sbadge mono sbadge--warn"
              data-test="refused-owner-badge"
              :title="refusedOwnerHelp(s)"
            >{{ t('sessions.card.ownerRefused') }}</span>
          </template>
          <template #meta>
            <button
              v-if="s.peer_name"
              class="a-peername mono"
              type="button"
              data-test="peer-name"
              :title="t('sessions.card.peerNameTitle', { name: peerNameLabel(s) })"
              @click.stop="copySessionID(`name:${s.session_id}`, s.peer_name!)"
            >{{ s.peer_name }}<span class="a-peername-copy">{{ copiedSessionIDs.has(`name:${s.session_id}`) ? t('sessions.card.copied') : '' }}</span></button>
            <span class="a-agent mono">{{ s.agent }}</span>
            <span class="a-project mono" :title="s.project_key">{{ s.project_key || '—' }}</span>
            <span
              v-if="s.refused_caller_id"
              class="a-owner-refused mono"
              data-test="refused-owner-notice"
              :title="refusedOwnerHelp(s)"
            >{{ refusedOwnerNotice(s) }}</span>
            <span
              v-if="topicDisplay(s).text"
              class="a-topic mono"
              :class="{ 'a-topic--inferred': topicDisplay(s).inferred }"
              data-test="card-topic"
              :title="topicDisplay(s).title || t('sessions.card.inferredTitle')"
            >{{ topicDisplay(s).text }}<em v-if="topicDisplay(s).inferred" class="a-topic-tag">{{ t('sessions.card.inferred') }}</em></span>
            <span v-if="s.maybe_offline" class="sbadge mono sbadge--off" data-test="maybe-offline">{{ t('sessions.card.maybeOffline') }}</span>
            <span v-for="(r, i) in hitReasons(s.hits)" :key="i" class="icard-chip mono" data-test="hit-reason">{{ r }}</span>
            <span v-if="s.cwd" class="mono" :title="s.cwd">{{ workspaceLabel(s.cwd) }}</span>
            <span class="a-seen mono" :title="fmtTime(s.last_seen_at)">{{ fmtAgo(s.last_seen_at, nowSec) }}</span>
            <RouterLink
              v-if="workBySession.get(s.session_id)"
              class="icard-chip mono"
              data-test="work-link"
              :to="`/work?id=${encodeURIComponent(workBySession.get(s.session_id)!.id)}`"
              :title="t('sessions.card.workItemTitle', { title: workBySession.get(s.session_id)!.title })"
            >{{ t('sessions.card.workItem', { title: workBySession.get(s.session_id)!.title }) }}</RouterLink>
          </template>
          <template #actions>
            <button
              v-if="s.refused_caller_id"
              class="icard-btn owner-fix mono"
              type="button"
              data-test="refused-owner-transfer"
              :disabled="ownerBusyIds.has(s.session_id)"
              :title="refusedOwnerHelp(s)"
              @click="onTransferOwner(s)"
            >{{ ownerBusyIds.has(s.session_id) ? '…' : transferOwnerLabel(s) }}</button>
            <span v-if="ownerErrors.get(s.session_id)" class="relay-err mono" data-test="refused-owner-error">{{ t('sessions.card.ownerFailed', { err: ownerErrors.get(s.session_id) }) }}</span>
            <button class="icard-btn icard-btn--primary mono" type="button" data-test="row-open" @click="openDrawer(s.session_id)">{{ t('sessions.card.open') }}</button>
            <button
              v-if="canGrantSession(whoMode.canManage, s)"
              class="icard-btn mono"
              type="button"
              data-test="row-grant"
              :title="t('sessions.card.grantTitle')"
              @click="grantTarget = s"
            >{{ t('sessions.card.grant') }}</button>
            <span class="a-wake">
              <RouterLink
                v-if="s.state === 'handed_off' && s.handed_off_job_id"
                class="icard-btn wake-link mono"
                :to="`/jobs/${encodeURIComponent(s.handed_off_job_id)}?attach=1`"
                :title="t('sessions.card.handedOffTitle')"
              >{{ t('sessions.card.handedOff') }}</RouterLink>
              <button
                v-else
                class="icard-btn wake-btn mono"
                type="button"
                data-test="row-wake"
                :disabled="!s.can_resume || wakingIds.has(s.session_id)"
                :title="wakeErrors.get(s.session_id) ? t('sessions.card.wakeFailed', { err: wakeErrors.get(s.session_id) }) : resumeTitle(s)"
                @click="onWake(s)"
              >{{ wakeLabel(s) }}</button>
              <span v-if="wakeErrors.get(s.session_id)" class="relay-err mono" :title="wakeErrors.get(s.session_id)">!</span>
            </span>
          </template>
          <template #details>
            <dl class="icard-kv mono">
              <template v-if="s.peer_name">
                <dt>{{ t('sessions.card.name') }}</dt>
                <dd>
                  <span class="a-session-id" data-test="peer-name-detail">{{ s.peer_name }}</span>
                  <span v-if="s.peer_name_source" class="a-peer-status">（{{ s.peer_name_source }}）</span>
                  <button class="copy-btn mono" type="button" @click="copySessionID(`name:${s.session_id}`, s.peer_name!)">{{ copiedSessionIDs.has(`name:${s.session_id}`) ? t('sessions.card.copied') : t('sessions.card.copy') }}</button>
                </dd>
              </template>
              <dt>{{ t('sessions.card.topic') }}</dt>
              <dd><SessionTopic :session="s" @updated="updateSession" /></dd>
              <dt>{{ t('sessions.card.session') }}</dt>
              <dd>
                <span class="a-session-id" :title="s.session_id">{{ shortAgentSessionId(s.session_id) }}</span>
                <button class="copy-btn mono" type="button" @click="copySessionID(s.session_id)">{{ copiedSessionIDs.has(s.session_id) ? t('sessions.card.copied') : t('sessions.card.copy') }}</button>
              </dd>
              <dt>{{ t('sessions.card.runner') }}</dt><dd :title="s.runner">{{ runnerLabel(s.runner) || '—' }}</dd>
              <template v-if="s.cwd"><dt>{{ t('sessions.card.cwd') }}</dt><dd>{{ s.cwd }}</dd></template>
              <template v-if="s.last_cwd"><dt>{{ t('sessions.card.lastCwd') }}</dt><dd>{{ s.last_cwd }}</dd></template>
              <template v-for="u in sessionUsageRows(s.usage)" :key="u.label">
                <dt>{{ t('sessions.card.usage', { label: u.label }) }}</dt><dd class="mono" data-test="session-usage">{{ u.text }}</dd>
              </template>
              <dt>{{ t('sessions.card.turns') }}</dt><dd class="a-turns">{{ s.turn_no }}</dd>
              <template v-if="s.peer_status || s.peer_name">
                <dt>{{ t('sessions.card.peer') }}</dt>
                <dd>
                  <span v-if="s.peer_status" class="a-peer-status">peer {{ s.peer_status }}</span>
                  <span class="a-peer-messaging" :class="{ ready: s.peer_messaging, pending: peerMessagingPending(s) }">{{ peerMessagingLabel(s) }}</span>
                </dd>
              </template>
              <template v-else><dt>{{ t('sessions.card.peer') }}</dt><dd><span class="a-peer-messaging" :class="{ ready: s.peer_messaging, pending: peerMessagingPending(s) }">{{ peerMessagingLabel(s) }}</span></dd></template>
              <template v-if="s.issue_id"><dt>{{ t('sessions.card.issue') }}</dt><dd><RouterLink :to="`/issues?issue=${encodeURIComponent(s.issue_id)}`">issue {{ s.issue_id }}</RouterLink></dd></template>
              <template v-if="s.transcript"><dt>{{ t('sessions.card.transcript') }}</dt><dd>{{ s.transcript }}</dd></template>
            </dl>
            <div class="a-relay">
              <span class="relay-label mono">{{ t('sessions.card.relay') }}</span>
              <span class="relay-modes mono" :class="{ busy: relayBusyIds.has(s.session_id) }" :title="relayTitle(s)">
                <button
                  v-for="m in RELAY_MODES"
                  :key="m"
                  type="button"
                  class="relay-mode"
                  :class="{ active: s.relay_mode === m, on: m === 'on', auto: m === 'auto' }"
                  :disabled="relayBusyIds.has(s.session_id) || s.state === 'ended'"
                  @click="onSetRelayMode(s, m)"
                >
                  {{ m }}
                </button>
              </span>
              <span v-if="relayEvidence(s)" class="relay-auto mono" :title="relayTitle(s)">{{ relayEvidence(s) }}</span>
              <span v-if="relayErrors.get(s.session_id)" class="relay-err mono">!</span>
            </div>
            <details v-if="s.state === 'running' && s.progress_text" class="a-progress mono">
              <summary>{{ t('sessions.card.progressAt', { time: fmtAgo(s.progress_at || s.last_seen_at, nowSec), text: s.progress_text }) }}</summary>
              <p>{{ s.progress_text }}</p>
            </details>
            <div v-if="s.last_message" class="a-lastbox">
              <span class="a-last mono">{{ s.last_message }}</span>
              <button class="last-message-open mono" type="button" @click="openDrawer(s.session_id, true)">{{ t('sessions.card.viewFull') }}</button>
            </div>
            <span v-if="s.watches?.length" class="session-watches mono">
              <RouterLink
                v-for="watch in s.watches"
                :key="watch.job_id"
                class="session-watch"
                :to="`/jobs/${encodeURIComponent(watch.job_id)}`"
                :title="watch.title || watch.job_id"
              >
                {{ watch.job_id }} · {{ watch.title || 'job' }} · {{ watch.status }}
              </RouterLink>
            </span>
          </template>
        </InfoCard>
      </div>

      <div v-else-if="!agentLoading && !agentError" class="empty">
        {{ t('sessions.empty.agent') }}
      </div>
    </section>

    <section v-else-if="active === 'acp'" class="group">
      <header class="group-head">
        <span v-if="filteredAcp.length" class="group-count">{{ filteredAcp.length }}</span>
        <span class="grow"></span>
        <button class="act" type="button" :disabled="acpLoading" @click="loadAcpSessions()">{{ acpLoading ? t('sessions.refreshing') : t('sessions.refresh') }}</button>
      </header>
      <p v-if="acpError" class="error mono">{{ acpError }}</p>
      <p v-if="linkError" class="error mono" data-test="link-error">{{ linkError }}</p>
      <div v-if="filteredAcp.length > 0" class="icard-grid" data-test="acp-cards">
        <InfoCard
          v-for="item in filteredAcp"
          :key="item.id"
          :tone="acpTone(item)"
          :dim="acpTone(item) === 'off'"
          :expanded="expandedAcp.has(item.id)"
          :card-id="item.id"
          @toggle="expandedAcp = toggleExpanded(expandedAcp, item.id)"
        >
          <template #title>{{ item.title || item.id }}</template>
          <template #badges><span class="sbadge mono" :class="`sbadge--${acpTone(item)}`">{{ acpStatusLabel(item) }}</span></template>
          <template #meta>
            <span class="mono">{{ item.agent }}</span>
            <span class="mono">{{ item.project_key }}</span>
            <span class="mono">{{ runnerLabel(item.runner) }}</span>
            <span class="mono">{{ t('sessions.card.turns') }} {{ item.turn_no ?? 0 }}</span>
            <RouterLink
              v-if="workForJob(item.id, item.session_id)"
              class="icard-chip mono"
              data-test="acp-work-link"
              :to="`/work?id=${encodeURIComponent(workForJob(item.id, item.session_id)!.id)}`"
              :title="t('sessions.card.workItemTitle', { title: workForJob(item.id, item.session_id)!.title })"
            >{{ t('sessions.card.workItem', { title: workForJob(item.id, item.session_id)!.title }) }}</RouterLink>
          </template>
          <template #actions>
            <RouterLink class="icard-btn icard-btn--primary mono" :to="`/jobs/${encodeURIComponent(item.id)}`">{{ t('sessions.acp.openJob') }}</RouterLink>
          </template>
          <template #details>
            <dl class="icard-kv mono">
              <dt>Job</dt><dd>{{ item.id }}</dd>
              <template v-if="item.session_id"><dt>Session</dt><dd>{{ item.session_id }}</dd></template>
              <dt>{{ t('sessions.card.started') }}</dt><dd>{{ fmtTime(item.started_at) }}</dd>
              <dt>{{ t('sessions.card.lastReply') }}</dt><dd>{{ acpPreview(item) }}</dd>
              <template v-if="!workForJob(item.id, item.session_id) && linkableItems.length">
                <dt>{{ t('sessions.card.linkWork') }}</dt>
                <dd class="link-pick">
                  <select v-model="linkPick[item.id]" class="mono" data-test="acp-link-pick"><option value="">{{ t('sessions.card.pickWork') }}</option><option v-for="w in linkableItems" :key="w.id" :value="w.id">{{ w.title }}</option></select>
                  <button class="icard-btn mono" type="button" :disabled="!linkPick[item.id] || linkBusy === item.id" data-test="acp-link-btn" @click="linkJobToWork(item.id)">{{ t('sessions.card.link') }}</button>
                </dd>
              </template>
            </dl>
          </template>
        </InfoCard>
      </div>
      <div v-else-if="!acpLoading && !acpError" class="empty">{{ t('sessions.empty.acp') }}</div>
    </section>

    <section v-else class="group">
      <header class="group-head">
        <span v-if="filteredPty.length" class="group-count">{{ filteredPty.length }}</span>
        <span class="grow"></span>
        <div class="controls">
          <button v-if="!readOnly" class="act act--primary" type="button" data-test="new-terminal-session" @click="openSheet('pty')">
            {{ t('sessions.new.title') }}
          </button>
          <button class="act" type="button" :disabled="loading" @click="load()">
            {{ loading ? t('sessions.refreshing') : t('sessions.refresh') }}
          </button>
        </div>
      </header>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div v-if="filteredPty.length > 0" class="icard-grid" data-test="pty-cards">
      <InfoCard
        v-for="s in filteredPty"
        :key="s.pty_session_id"
        :tone="canAttachSession(s) ? 'live' : 'off'"
        :dim="!canAttachSession(s)"
        :expanded="expandedPty.has(s.pty_session_id)"
        :card-id="s.pty_session_id"
        @toggle="expandedPty = toggleExpanded(expandedPty, s.pty_session_id)"
      >
        <template #title>{{ t('sessions.tab.pty') }} · {{ shortId(s.job_id) }}</template>
        <template #badges><span class="sbadge mono" :class="canAttachSession(s) ? 'sbadge--live' : 'sbadge--off'">{{ s.state }}</span></template>
        <template #meta>
          <span class="size mono">{{ s.cols }}×{{ s.rows }}</span>
          <span class="duration mono">{{ duration(s) }}</span>
          <span class="started mono">{{ fmtTime(s.started_at) }}</span>
          <RouterLink
            v-if="workForJob(s.job_id, s.session_id)"
            class="icard-chip mono"
            data-test="pty-work-link"
            :to="`/work?id=${encodeURIComponent(workForJob(s.job_id, s.session_id)!.id)}`"
            :title="t('sessions.card.workItemTitle', { title: workForJob(s.job_id, s.session_id)!.title })"
          >{{ t('sessions.card.workItem', { title: workForJob(s.job_id, s.session_id)!.title }) }}</RouterLink>
        </template>
        <template #actions>
          <RouterLink
            v-if="canAttachSession(s)"
            class="icard-btn icard-btn--primary mono"
            :to="`/jobs/${encodeURIComponent(s.job_id ?? '')}?attach=1`"
          >
            {{ t('sessions.card.openTerminal') }}
          </RouterLink>
          <button
            v-if="s.has_recording"
            class="icard-btn mono"
            type="button"
            :disabled="downloadingRecordingIds.has(s.pty_session_id)"
            @click="onDownloadRecording(s)"
          >
            {{ downloadingRecordingIds.has(s.pty_session_id) ? t('sessions.card.downloading') : t('sessions.card.downloadRecording') }}
          </button>
        </template>
        <template #details>
          <dl class="icard-kv mono">
            <dt>Job</dt>
            <dd><RouterLink v-if="s.job_id" :to="`/jobs/${encodeURIComponent(s.job_id)}`" :title="s.job_id">{{ s.job_id }}</RouterLink><template v-else>—</template></dd>
            <dt>Session ID</dt><dd :title="s.session_id || ''">{{ shortSessionID(s.session_id) }}</dd>
            <dt>{{ t('sessions.card.size') }}</dt><dd>{{ bytesText(s) }}</dd>
            <template v-if="s.job_id && !workForJob(s.job_id, s.session_id) && linkableItems.length">
              <dt>{{ t('sessions.card.linkWork') }}</dt>
              <dd class="link-pick">
                <select v-model="linkPick[s.job_id]" class="mono" data-test="pty-link-pick"><option value="">{{ t('sessions.card.pickWork') }}</option><option v-for="w in linkableItems" :key="w.id" :value="w.id">{{ w.title }}</option></select>
                <button class="icard-btn mono" type="button" :disabled="!linkPick[s.job_id] || linkBusy === s.job_id" data-test="pty-link-btn" @click="linkJobToWork(s.job_id)">{{ t('sessions.card.link') }}</button>
              </dd>
            </template>
            <dt>{{ t('sessions.card.encrypted') }}</dt><dd>{{ s.encrypted ? t('sessions.card.encryptedYes') : t('sessions.card.encryptedNo') }}</dd>
            <dt>{{ t('sessions.card.recording') }}</dt><dd>{{ s.has_recording ? t('sessions.card.recorded') : t('sessions.card.notRecorded') }}</dd>
          </dl>
        </template>
      </InfoCard>
    </div>

    <div v-else-if="!loading && !error" class="empty">
      {{ t('sessions.empty.pty') }}
    </div>
    <p v-if="hasSessions" class="sessions-note">
      {{ t('sessions.ptyNote') }}
    </p>
    </section>
      </template>
    </Tabs>

    <SessionGrantDialog
      v-if="grantTarget"
      :sid="grantTarget.session_id"
      :label="sessionDisplayName(grantTarget)"
      @close="grantTarget = null"
      @granted="refreshSessionGrants()"
    />

    <SessionDrawer
      v-if="openSid"
      :sid="openSid"
      :expand-last-message="openLastMessage"
      @close="closeDrawer"
      @changed="loadAgentSessions({ silent: true })"
      @deleted="onSessionDeleted"
    />
  </div>
</template>

<style scoped>
.find-box { display: flex; gap: 8px; margin: 0 0 10px; }
.find-input {
  flex: 1; min-width: 0; padding: 5px 10px; border: 1px solid var(--line); border-radius: var(--r-chip);
  background: var(--sunken); color: var(--ink); font-size: 12px;
}
.find-input:focus { outline: none; border-color: var(--accent); }
.find-note { margin: 0 0 10px; color: var(--ink-3); font-size: 12px; }
.a-topic { color: var(--ink); max-width: 100%; overflow-wrap: anywhere; }
.a-topic--inferred { color: var(--ink-3); font-style: italic; }
.a-topic-tag {
  margin-left: 6px; padding: 0 5px; border: 1px dashed var(--line); border-radius: 999px;
  font-size: 10px; font-style: normal;
}
@media (max-width: 760px) { .find-input { font-size: 16px; } }
.link-pick { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.link-pick select { max-width: 100%; min-width: 0; }
.board {
  max-width: 1280px;
  margin: 0 auto;
}
.session-create {
  margin: 0 0 20px;
  padding: 12px 14px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
}
.session-create-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.relay-help-btn {
  width: 18px;
  height: 18px;
  margin-left: 6px;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: 50%;
  background: transparent;
  color: var(--ink-3);
  font-size: 11px;
  line-height: 16px;
  cursor: pointer;
  vertical-align: middle;
}
.session-create-actions { display: flex; align-items: center; gap: 8px; }
.session-create-fields { margin-top: 10px; }
.session-create-head .group-title { margin: 0; }
.session-create-fields {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
  align-items: end;
}
.session-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: var(--ink-3);
  font-size: 11px;
}
.session-field input,
.session-field select,
.session-field textarea {
  min-width: 0;
  color: var(--ink);
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 6px 7px;
  font: inherit;
}
.session-field textarea { resize: vertical; }
/* 调用方上限藏起来的 project 说明（不是错误，只是这个 token 看不到那些项目）。 */
.session-hint { color: var(--run); font-size: 11px; line-height: 1.3; }
.session-field-wide { grid-column: span 2; }
.session-create-submit { min-height: 34px; }
@media (max-width: 760px) {
  .session-create-fields { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .session-field-wide { grid-column: span 2; }
}
@media (max-width: 460px) {
  .session-create-fields { grid-template-columns: 1fr; }
  .session-field-wide { grid-column: span 1; }
}
.board-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 14px;
}
.head-main {
  min-width: 0;
}
.head-summary {
  margin: 2px 0 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.head-filters {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
}
.status-row {
  margin: 4px 0 var(--s2);
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--ink);
  margin: 0;
}
.controls {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--ink-3);
  font-size: 12px;
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}
.act {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 11px;
  text-decoration: none;
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--primary {
  color: var(--accent);
}
.sessions-note {
  margin: 10px 0 0;
  color: var(--ink-3);
  font-size: 11px;
}

/* 分组（Agent 会话 / ACP 持续会话 / 终端会话） */
.group {
  margin-bottom: 26px;
}
.group-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 10px;
}
.group-tools {
  display: flex;
  align-items: center;
  gap: 10px;
}
.grow {
  flex: 1;
}
.group-title {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0;
  font-size: 13px;
  letter-spacing: 0.08em;
  color: var(--ink);
  text-transform: uppercase;
}
.group-count {
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 0 7px;
  font-size: 10px;
  color: var(--ink-3);
  letter-spacing: 0;
  text-transform: none;
}
.group-count--hot {
  border-color: var(--run);
  color: var(--run);
}
.check {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  cursor: pointer;
  font-size: 11px;
  color: var(--ink-3);
  user-select: none;
}
.check input {
  accent-color: var(--accent);
  margin: 0;
}
.empty code {
  color: var(--run);
}

/* 卡片里的内容（卡片外壳 / 网格 / 徽标样式在 InfoCard 里，两页共用） */
.wake-btn {
  white-space: nowrap;
}
.wake-btn:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.wake-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.wake-link {
  color: var(--accent);
}
.a-wake {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.a-session-id {
  color: var(--ink);
}
.a-peername {
  background: transparent;
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 5px;
  color: var(--accent);
  font-size: inherit;
  cursor: pointer;
}
.a-peername:hover {
  border-color: var(--accent);
}
.a-peer-status,
.a-peer-messaging {
  color: var(--ink-3);
}
.a-peer-messaging.ready {
  color: var(--done);
}
.a-peer-messaging.pending {
  color: var(--muted);
}
.copy-btn {
  margin-left: 6px;
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 5px;
  font-size: 10px;
  cursor: pointer;
}
.copy-btn:hover {
  color: var(--accent);
  border-color: var(--accent);
}
.a-lastbox {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.a-last {
  color: var(--ink-3);
  font-size: 11px;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  overflow-wrap: anywhere;
}
.last-message-open {
  align-self: flex-start;
  padding: 0;
  color: var(--accent);
  background: transparent;
  border: 0;
  font-size: 11px;
  cursor: pointer;
}
.a-progress {
  display: block;
  color: var(--run);
  font-size: 11px;
  min-width: 0;
}
.a-progress summary {
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.a-progress p {
  margin: 4px 0 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: var(--ink);
}
.session-watches {
  display: flex;
  flex-wrap: wrap;
  gap: 3px 6px;
  font-size: 11px;
}
.session-watch {
  color: var(--accent);
  text-decoration: none;
}
.session-watch:hover {
  text-decoration: underline;
}
.a-relay {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.relay-label {
  font-size: 11px;
  color: var(--ink-3);
}
.relay-err {
  color: var(--fail);
  font-weight: 600;
  font-size: 12px;
}

/* 归属被拒（gofer-eh53）：提示行与「转给 <caller>」按钮用 warn 色，和状态徽标区分开 */
.a-owner-refused {
  color: var(--run);
  font-size: 11px;
}
.owner-fix {
  color: var(--run);
  border-color: var(--run);
}

/* 自动布防说明：默认收起，点标题旁的「?」展开 */
.relay-note {
  margin: 0;
  padding: 6px 14px;
  margin-bottom: 12px;
  color: var(--ink-3);
  font-size: 10px;
  line-height: 1.5;
}
.relay-note code {
  color: var(--run);
}

/* 中继三态开关（auto / on / off，与 SessionDrawer 同款） */
.relay-modes {
  display: inline-flex;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: hidden;
}
.relay-mode {
  all: unset;
  padding: 3px 9px;
  font-size: 11px;
  line-height: 16px;
  color: var(--ink-3);
  cursor: pointer;
  border-right: 1px solid var(--line);
}
.relay-mode:last-child {
  border-right: none;
}
.relay-mode:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.06);
}
.relay-mode.active {
  background: rgba(255, 255, 255, 0.1);
  color: var(--ink);
}
.relay-mode.active.auto {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  color: var(--run);
}
.relay-mode.active.on {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  color: var(--accent);
}
.relay-mode:disabled {
  opacity: 0.5;
  cursor: default;
}
.relay-modes.busy {
  opacity: 0.6;
  cursor: progress;
}
/* auto 判定的依据：键盘空闲 / 距上次人工输入 */
.relay-auto {
  font-size: 10px;
  color: var(--run);
}
.empty {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink-3);
  font-size: 13px;
  padding: 28px 14px;
  text-align: center;
}

@media (max-width: 760px) {
  .board-head {
    flex-direction: column;
    align-items: stretch;
    gap: var(--s2);
    margin-bottom: var(--s3);
  }
  .head-filters {
    justify-content: space-between;
  }
  .head-filters .act {
    min-height: 40px;
  }
}

@media (max-width: 900px) {
  .group-head {
    flex-wrap: wrap;
    gap: 8px;
  }
}
</style>
