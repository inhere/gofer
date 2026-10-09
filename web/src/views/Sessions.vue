<script setup lang="ts">
import { runnerLabel } from '../utils/runnerDisplay'
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
  submitJob,
} from '../api/client'
import { getMetaCached } from '../api/metaCache'
import { fmtAgo, fmtDuration } from '../api/time'
import type { AgentSession, AgentSessionRelayMode, Job, MetaAgent, MetaProject, MetaResp, MetaRunner, PtySession, SubmitJobReq, WorkItem } from '../api/types'
import SessionDrawer from '../components/SessionDrawer.vue'
import { copyText, peerMessagingLabel, peerNameLabel, sessionDisplayName as formatSessionDisplayName, shortAgentSessionId } from '../utils/sessionMessaging'
import { resumeConfirmText, resumeFailText, resumeLabel, resumeTitle } from '../utils/sessionResume'
import { computeRunnerBlocks, effectiveRunnerBlocks, pickRunner } from '../utils/runnerChoice'

const DEFAULT_LIMIT = 50
// 手机上新建表单默认收起：展开时它占满第一屏，会话列表要往下滑才看得到。
const relayHelpOpen = ref(false)
const createOpen = ref(typeof window === 'undefined' || !window.matchMedia?.('(max-width: 640px)').matches)
// Agent 会话列表轮询间隔（页面可见时）

const route = useRoute()
const router = useRouter()

const sessionMeta = ref<MetaResp>({ projects: [], agents: [], runners: [], workers: [] })
const sessionType = ref<'pty' | 'acp'>('pty')
const sessionProject = ref('')
const sessionAgent = ref('')
const sessionRunner = ref('local')
const sessionTitle = ref('')
const sessionPrompt = ref('')
const sessionModel = ref('')
const sessionCreating = ref(false)
const sessionCreateError = ref('')

const selectedSessionProject = computed<MetaProject | undefined>(() =>
  sessionMeta.value.projects.find((p) => p.key === sessionProject.value),
)
const sessionAgents = computed<MetaAgent[]>(() => {
  const allowed = new Set(selectedSessionProject.value?.allowed_agents ?? [])
  return sessionMeta.value.agents
    .filter((a) => allowed.size === 0 || allowed.has(a.key))
    .filter((a) => sessionType.value === 'acp' ? a.type === 'acp-agent' : !!a.interactive)
    .sort((a, b) => a.key.localeCompare(b.key))
})
const sessionRunners = computed<MetaRunner[]>(() => {
  const allowed = new Set(selectedSessionProject.value?.allowed_runners ?? [])
  return sessionMeta.value.runners
    .filter((r) => r.type === 'local' || allowed.size === 0 || allowed.has(r.name))
    // ACP 持续会话：local 与协议 >= v13 的 worker 可用，其它 runner 不放出
    .filter((r) => sessionType.value !== 'acp' || r.type === 'local' || r.type === 'worker')
})
// 灰显原因：project 级阻塞（worker 不具备该 project 等）+ ACP 会话的 worker 协议门槛
const sessionRunnerBlocks = computed(() =>
  effectiveRunnerBlocks(
    sessionRunners.value,
    computeRunnerBlocks(selectedSessionProject.value, sessionRunners.value, sessionMeta.value.workers),
    sessionMeta.value.workers,
    sessionType.value === 'acp',
  ),
)

function chooseSessionDefaults(): void {
  if (!sessionProject.value) sessionProject.value = sessionMeta.value.projects[0]?.key ?? ''
  if (!sessionAgent.value || !sessionAgents.value.some((a) => a.key === sessionAgent.value)) {
    sessionAgent.value = sessionAgents.value[0]?.key ?? ''
  }
  sessionRunner.value = pickRunner(sessionRunner.value, selectedSessionProject.value, sessionRunners.value, sessionRunnerBlocks.value) || 'local'
}

const fullSessionConfigURL = computed(() => {
  const q = new URLSearchParams({ mode: 'session' })
  if (sessionProject.value) q.set('project', sessionProject.value)
  if (sessionAgent.value) q.set('agent', sessionAgent.value)
  if (sessionRunner.value) q.set('runner', sessionRunner.value)
  if (sessionTitle.value.trim()) q.set('title', sessionTitle.value.trim())
  if (sessionPrompt.value.trim()) q.set('prompt', sessionPrompt.value.trim())
  if (sessionModel.value.trim()) q.set('model', sessionModel.value.trim())
  return `/new?${q.toString()}`
})

async function createSession(): Promise<void> {
  if (sessionCreating.value || !sessionProject.value || !sessionAgent.value) return
  sessionCreating.value = true
  sessionCreateError.value = ''
  const req: SubmitJobReq = {
    project_key: sessionProject.value,
    agent: sessionAgent.value,
    runner: sessionRunner.value,
    title: sessionTitle.value.trim() || undefined,
    prompt: sessionPrompt.value.trim() || undefined,
    model: sessionModel.value.trim() || undefined,
    session: sessionType.value === 'acp',
    interactive: sessionType.value === 'pty',
  }
  try {
    const result = await submitJob(req)
    if (sessionType.value === 'pty') {
      await router.push(`/jobs/${encodeURIComponent(result.job.id)}?attach=1`)
    } else if (result.job.session_id) {
      await router.push({ path: '/workbench', query: { thread: `s:${result.job.session_id}` } })
    } else {
      // ACP 会话正常会在提交响应里带 session_id；保留 job 查询作为异步建会话窗口，
      // 仍然停留在对话工作台而不是跳到 job 详情页。
      await router.push({ path: '/workbench', query: { job: result.job.id } })
    }
  } catch (e) {
    sessionCreateError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sessionCreating.value = false
  }
}

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

const hasAgentSessions = computed(() => agentSessions.value.length > 0)
const waitingCount = computed(
  () => agentSessions.value.filter((s) => s.state === 'waiting_reply').length,
)

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
    return supervising ? `已放行：正在监督 ${supervising[1]} 个 job` : s.wait_reason_detail ? `未布防：${s.wait_reason_detail}` : ''
  }
  if (s.wait_reason === 'idle_probe') {
    return `auto (idle ${idleText(s.idle_sec)})`
  }
  if (s.wait_reason === 'turn_age') {
    return `auto (no input ${idleText(humanSilence(s))})`
  }
  return ''
}

function relayTitle(s: AgentSession): string {
  const err = relayErrors.value.get(s.session_id)
  if (err) {
    return `切换失败：${err}`
  }
  const reason =
    s.wait_reason === 'mode_on'
      ? '显式开关：每次停下都在 web 等你回复'
      : s.wait_reason === 'idle_probe'
        ? `键盘空闲 ${idleText(s.idle_sec)} ≥ session.auto_relay_idle_sec，本次停下会在 web 等回复`
        : s.wait_reason === 'turn_age'
          ? `探测不到键盘，距上次人工输入 ${idleText(humanSilence(s))} ≥ session.auto_relay_turn_sec，本次停下会在 web 等回复`
          : s.wait_reason_detail
            ? `${s.wait_reason_detail}：你在监督在跑的 job，本次不停下等回复（session.auto_relay_skip_when_supervising；job 结束即恢复自动判定）`
            : '当前不等：没有判据成立，下一次停下直接放行'
  switch (s.relay_mode) {
    case 'on':
      return `中继 on（显式开关）：${reason}；终端输入或 web /off 才会关掉`
    case 'off':
      return '中继 off：这个会话从不等 web 回复，已打开的 turn 会被释放'
    default:
      return `中继 auto：server 判定——${reason}；人回来（键盘 / 直接输入 / Esc）即放行`
  }
}

async function loadAgentSessions(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) {
    agentLoading.value = true
  }
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await listAgentSessions({ all: showEnded.value, limit: 100 })
    agentSessions.value = resp.sessions ?? []
    agentError.value = ''
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

watch(sessionProject, chooseSessionDefaults)
watch(sessionType, () => {
  sessionAgent.value = ''
  sessionRunner.value = sessionType.value === 'acp' ? 'local' : sessionRunner.value
  chooseSessionDefaults()
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
const hasAcpSessions = computed(() => acpSessions.value.length > 0)

function acpStatusLabel(job: Job): string {
  if (job.session_ending) return '结束中'
  if (job.status === 'awaiting_input') return '等待输入'
  if (job.status === 'running') return '运行中'
  if (['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(job.status)) return '已结束'
  return job.status
}

function acpTone(job: Job): 'live' | 'hot' | 'off' {
  if (['done', 'failed', 'cancelled', 'timeout', 'rejected'].includes(job.status)) return 'off'
  return job.status === 'awaiting_input' ? 'hot' : 'live'
}

function acpPreview(job: Job): string {
  return job.error || (job.status === 'awaiting_input' ? '等待你的下一句话' : '打开会话查看过程')
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

onMounted(() => {
  void getMetaCached().then((meta) => {
    sessionMeta.value = meta
    chooseSessionDefaults()
  }).catch((e) => {
    sessionCreateError.value = e instanceof Error ? e.message : String(e)
  })
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
      <h1 class="title mono">SESSIONS</h1>
    </header>

    <section class="session-create" aria-label="新建会话">
      <div class="session-create-head">
        <h2 class="group-title mono">新建会话</h2>
        <div class="session-create-actions">
          <button class="act mono" type="button" :aria-expanded="createOpen" @click="createOpen = !createOpen">
            {{ createOpen ? '收起' : '＋ 新建会话' }}
          </button>
          <RouterLink class="act mono" :to="fullSessionConfigURL">完整配置</RouterLink>
        </div>
      </div>
      <div v-show="createOpen" class="session-create-fields">
        <label class="session-field mono">会话类型
          <select v-model="sessionType">
            <option value="pty">终端 PTY</option>
            <option value="acp">ACP 持续会话</option>
          </select>
        </label>
        <label class="session-field mono">项目
          <select v-model="sessionProject">
            <option v-for="project in sessionMeta.projects" :key="project.key" :value="project.key">{{ project.key }}</option>
          </select>
        </label>
        <label class="session-field mono">agent
          <select v-model="sessionAgent" :disabled="!sessionAgents.length">
            <option v-for="agent in sessionAgents" :key="agent.key" :value="agent.key">{{ agent.key }}</option>
          </select>
        </label>
        <label class="session-field mono">runner
          <select v-model="sessionRunner">
            <option v-for="runner in sessionRunners" :key="runner.name" :value="runner.name" :disabled="!!sessionRunnerBlocks[runner.name]">{{ runnerLabel(runner.name) }}<template v-if="sessionRunnerBlocks[runner.name]"> · {{ sessionRunnerBlocks[runner.name].short }}</template></option>
          </select>
        </label>
        <label class="session-field mono">模型（可选）
          <input v-model="sessionModel" type="text" placeholder="留空 = agent 默认" />
        </label>
        <label class="session-field session-field-wide mono">标题（可选）
          <input v-model="sessionTitle" type="text" placeholder="会话标题" />
        </label>
        <label class="session-field session-field-wide mono">第一句话（可选）
          <textarea v-model="sessionPrompt" rows="2" placeholder="创建后发送的第一句话"></textarea>
        </label>
        <button class="act act--primary mono session-create-submit" type="button" :disabled="sessionCreating || !sessionAgent" @click="createSession">
          {{ sessionCreating ? '创建中…' : '创建并打开' }}
        </button>
      </div>
      <p v-if="sessionCreateError" class="error mono">{{ sessionCreateError }}</p>
    </section>

    <!-- Agent 会话（会话中继 SESS-01）：hook 登记的 claude/codex 会话，点击行开详情抽屉 -->
    <section class="group">
      <header class="group-head">
        <h2 class="group-title mono">
          AGENT 会话
          <button class="relay-help-btn mono" type="button" :aria-expanded="relayHelpOpen" title="中继三态开关说明" @click="relayHelpOpen = !relayHelpOpen">?</button>
          <span v-if="waitingCount > 0" class="group-count group-count--hot mono">{{ waitingCount }} 等待回复</span>
          <span v-else-if="hasAgentSessions" class="group-count mono">{{ agentSessions.length }}</span>
        </h2>
        <div class="controls mono">
          <label class="check">
            <input v-model="showEnded" type="checkbox" />
            显示已结束
          </label>
          <button
            class="act mono"
            type="button"
            :disabled="agentLoading"
            @click="loadAgentSessions()"
          >
            {{ agentLoading ? '刷新中…' : '刷新' }}
          </button>
        </div>
      </header>
      <!-- 中继三态开关说明：默认收起，点标题旁的「?」展开 -->
      <p v-if="relayHelpOpen" class="relay-note mono">
        三态开关：<code>on</code> = 每次停下都等你回复；<code>off</code> = 从不等；<code>auto</code> =
        键盘空闲 ≥ <code>session.auto_relay_idle_sec</code>（默认 5 分钟）时等你回复，探测不到键盘的终端（容器）改用
        距上次人工输入 ≥ <code>session.auto_relay_turn_sec</code>（默认 15 分钟）判定——不用拨开关。
        自动判定开的等待，人回来即放行（键盘一碰即放，或按 Esc / 直接输入一条）。
        <code>on</code> 适合人离开时用：在终端里输入一条，就视为人回来了，开关自动回到 <code>auto</code>（网页送进去的话不算）。
      </p>

      <p v-if="agentError" class="error mono">{{ agentError }}</p>

      <div v-if="hasAgentSessions" class="icard-grid" data-test="agent-cards">
        <InfoCard
          v-for="s in agentSessions"
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
              :title="s.state === 'offline' ? `长时间没有心跳（最后心跳 ${fmtTime(s.last_seen_at)}），进程可能已退出；仍可唤醒` : undefined"
            >{{ agentStateLabel(s.state) }}</span>
          </template>
          <template #meta>
            <button
              v-if="s.peer_name"
              class="a-peername mono"
              type="button"
              data-test="peer-name"
              :title="`Claude 会话名 ${peerNameLabel(s)}，点击复制（其它会话用它作 SendMessage 地址）`"
              @click.stop="copySessionID(`name:${s.session_id}`, s.peer_name!)"
            >{{ s.peer_name }}<span class="a-peername-copy">{{ copiedSessionIDs.has(`name:${s.session_id}`) ? ' 已复制' : '' }}</span></button>
            <span class="a-agent mono">{{ s.agent }}</span>
            <span class="a-project mono" :title="s.project_key">{{ s.project_key || '—' }}</span>
            <span v-if="s.cwd" class="mono" :title="s.cwd">{{ workspaceLabel(s.cwd) }}</span>
            <span class="a-seen mono" :title="fmtTime(s.last_seen_at)">{{ fmtAgo(s.last_seen_at, nowSec) }}</span>
            <RouterLink
              v-if="workBySession.get(s.session_id)"
              class="icard-chip mono"
              data-test="work-link"
              :to="`/work?id=${encodeURIComponent(workBySession.get(s.session_id)!.id)}`"
              :title="`所属工作项：${workBySession.get(s.session_id)!.title}`"
            >工作项 · {{ workBySession.get(s.session_id)!.title }}</RouterLink>
          </template>
          <template #actions>
            <button class="icard-btn icard-btn--primary mono" type="button" data-test="row-open" @click="openDrawer(s.session_id)">打开</button>
            <span class="a-wake">
              <RouterLink
                v-if="s.state === 'handed_off' && s.handed_off_job_id"
                class="icard-btn wake-link mono"
                :to="`/jobs/${encodeURIComponent(s.handed_off_job_id)}?attach=1`"
                title="这个会话已被一个终端 job 接管，点开继续对话"
              >已接管 →</RouterLink>
              <button
                v-else
                class="icard-btn wake-btn mono"
                type="button"
                data-test="row-wake"
                :disabled="!s.can_resume || wakingIds.has(s.session_id)"
                :title="wakeErrors.get(s.session_id) ? `唤醒失败：${wakeErrors.get(s.session_id)}` : resumeTitle(s)"
                @click="onWake(s)"
              >{{ wakingIds.has(s.session_id) ? '…' : resumeLabel(s) }}</button>
              <span v-if="wakeErrors.get(s.session_id)" class="relay-err mono" :title="wakeErrors.get(s.session_id)">!</span>
            </span>
          </template>
          <template #details>
            <dl class="icard-kv mono">
              <template v-if="s.peer_name">
                <dt>名称</dt>
                <dd>
                  <span class="a-session-id" data-test="peer-name-detail">{{ s.peer_name }}</span>
                  <span v-if="s.peer_name_source" class="a-peer-status">（{{ s.peer_name_source }}）</span>
                  <button class="copy-btn mono" type="button" @click="copySessionID(`name:${s.session_id}`, s.peer_name!)">{{ copiedSessionIDs.has(`name:${s.session_id}`) ? '已复制' : '复制' }}</button>
                </dd>
              </template>
              <dt>Session</dt>
              <dd>
                <span class="a-session-id" :title="s.session_id">{{ shortAgentSessionId(s.session_id) }}</span>
                <button class="copy-btn mono" type="button" @click="copySessionID(s.session_id)">{{ copiedSessionIDs.has(s.session_id) ? '已复制' : '复制' }}</button>
              </dd>
              <dt>Runner</dt><dd :title="s.runner">{{ runnerLabel(s.runner) || '—' }}</dd>
              <template v-if="s.cwd"><dt>目录</dt><dd>{{ s.cwd }}</dd></template>
              <template v-if="s.last_cwd"><dt>当前目录</dt><dd>{{ s.last_cwd }}</dd></template>
              <template v-for="u in sessionUsageRows(s.usage)" :key="u.label">
                <dt>{{ u.label }}用量</dt><dd class="mono" data-test="session-usage">{{ u.text }}</dd>
              </template>
              <dt>Turns</dt><dd class="a-turns">{{ s.turn_no }}</dd>
              <template v-if="s.peer_status || s.peer_name">
                <dt>Peer</dt>
                <dd>
                  <span v-if="s.peer_status" class="a-peer-status">peer {{ s.peer_status }}</span>
                  <span class="a-peer-messaging" :class="{ ready: s.peer_messaging, pending: peerMessagingLabel(s) === '待上报' }">{{ peerMessagingLabel(s) }}</span>
                </dd>
              </template>
              <template v-else><dt>Peer</dt><dd><span class="a-peer-messaging" :class="{ ready: s.peer_messaging, pending: peerMessagingLabel(s) === '待上报' }">{{ peerMessagingLabel(s) }}</span></dd></template>
              <template v-if="s.issue_id"><dt>Issue</dt><dd><RouterLink :to="`/issues?issue=${encodeURIComponent(s.issue_id)}`">issue {{ s.issue_id }}</RouterLink></dd></template>
              <template v-if="s.transcript"><dt>Transcript</dt><dd>{{ s.transcript }}</dd></template>
            </dl>
            <div class="a-relay">
              <span class="relay-label mono">中继</span>
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
              <summary>进行中 · {{ fmtAgo(s.progress_at || s.last_seen_at, nowSec) }}：{{ s.progress_text }}</summary>
              <p>{{ s.progress_text }}</p>
            </details>
            <div v-if="s.last_message" class="a-lastbox">
              <span class="a-last mono">{{ s.last_message }}</span>
              <button class="last-message-open mono" type="button" @click="openDrawer(s.session_id, true)">查看全文</button>
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

      <div v-else-if="!agentLoading && !agentError" class="empty mono">
        暂无 agent 会话。执行 <code>gofer init hooks</code> 装配后，agent 会话会自动登记到这里。
      </div>
    </section>

    <section class="group">
      <header class="group-head">
        <h2 class="group-title mono">ACP 持续会话 <span v-if="hasAcpSessions" class="group-count mono">{{ acpSessions.length }}</span></h2>
        <button class="act mono" type="button" :disabled="acpLoading" @click="loadAcpSessions()">{{ acpLoading ? '刷新中…' : '刷新' }}</button>
      </header>
      <p v-if="acpError" class="error mono">{{ acpError }}</p>
      <p v-if="linkError" class="error mono" data-test="link-error">{{ linkError }}</p>
      <div v-if="hasAcpSessions" class="icard-grid" data-test="acp-cards">
        <InfoCard
          v-for="item in acpSessions"
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
            <span class="mono">第 {{ item.turn_no ?? 0 }} 轮</span>
            <RouterLink
              v-if="workForJob(item.id, item.session_id)"
              class="icard-chip mono"
              data-test="acp-work-link"
              :to="`/work?id=${encodeURIComponent(workForJob(item.id, item.session_id)!.id)}`"
              :title="`所属工作项：${workForJob(item.id, item.session_id)!.title}`"
            >工作项 · {{ workForJob(item.id, item.session_id)!.title }}</RouterLink>
          </template>
          <template #actions>
            <RouterLink class="icard-btn icard-btn--primary mono" :to="`/jobs/${encodeURIComponent(item.id)}`">查看过程</RouterLink>
          </template>
          <template #details>
            <dl class="icard-kv mono">
              <dt>Job</dt><dd>{{ item.id }}</dd>
              <template v-if="item.session_id"><dt>Session</dt><dd>{{ item.session_id }}</dd></template>
              <dt>开始</dt><dd>{{ fmtTime(item.started_at) }}</dd>
              <dt>最后一条回复</dt><dd>{{ acpPreview(item) }}</dd>
              <template v-if="!workForJob(item.id, item.session_id) && linkableItems.length">
                <dt>关联工作项</dt>
                <dd class="link-pick">
                  <select v-model="linkPick[item.id]" class="mono" data-test="acp-link-pick"><option value="">选一个工作项…</option><option v-for="w in linkableItems" :key="w.id" :value="w.id">{{ w.title }}</option></select>
                  <button class="icard-btn mono" type="button" :disabled="!linkPick[item.id] || linkBusy === item.id" data-test="acp-link-btn" @click="linkJobToWork(item.id)">关联</button>
                </dd>
              </template>
            </dl>
          </template>
        </InfoCard>
      </div>
      <div v-else-if="!acpLoading && !acpError" class="empty mono">暂无 ACP 持续会话</div>
    </section>

    <section class="group">
      <header class="group-head">
        <h2 class="group-title mono">
          终端会话
          <span v-if="hasSessions" class="group-count mono">{{ sessions.length }}</span>
        </h2>
        <div class="controls mono">
          <RouterLink class="act act--primary mono" to="/new?mode=session">
            新建会话
          </RouterLink>
          <button class="act mono" type="button" :disabled="loading" @click="load()">
            {{ loading ? '刷新中…' : '刷新' }}
          </button>
        </div>
      </header>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div v-if="hasSessions" class="icard-grid" data-test="pty-cards">
      <InfoCard
        v-for="s in sessions"
        :key="s.pty_session_id"
        :tone="canAttachSession(s) ? 'live' : 'off'"
        :dim="!canAttachSession(s)"
        :expanded="expandedPty.has(s.pty_session_id)"
        :card-id="s.pty_session_id"
        @toggle="expandedPty = toggleExpanded(expandedPty, s.pty_session_id)"
      >
        <template #title>终端会话 · {{ shortId(s.job_id) }}</template>
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
            :title="`所属工作项：${workForJob(s.job_id, s.session_id)!.title}`"
          >工作项 · {{ workForJob(s.job_id, s.session_id)!.title }}</RouterLink>
        </template>
        <template #actions>
          <RouterLink
            v-if="canAttachSession(s)"
            class="icard-btn icard-btn--primary mono"
            :to="`/jobs/${encodeURIComponent(s.job_id ?? '')}?attach=1`"
          >
            打开终端
          </RouterLink>
          <button
            v-if="s.has_recording"
            class="icard-btn mono"
            type="button"
            :disabled="downloadingRecordingIds.has(s.pty_session_id)"
            @click="onDownloadRecording(s)"
          >
            {{ downloadingRecordingIds.has(s.pty_session_id) ? '下载中' : '下载录制' }}
          </button>
        </template>
        <template #details>
          <dl class="icard-kv mono">
            <dt>Job</dt>
            <dd><RouterLink v-if="s.job_id" :to="`/jobs/${encodeURIComponent(s.job_id)}`" :title="s.job_id">{{ s.job_id }}</RouterLink><template v-else>—</template></dd>
            <dt>Session ID</dt><dd :title="s.session_id || ''">{{ shortSessionID(s.session_id) }}</dd>
            <dt>流量(输入/输出)</dt><dd>{{ bytesText(s) }}</dd>
            <template v-if="s.job_id && !workForJob(s.job_id, s.session_id) && linkableItems.length">
              <dt>关联工作项</dt>
              <dd class="link-pick">
                <select v-model="linkPick[s.job_id]" class="mono" data-test="pty-link-pick"><option value="">选一个工作项…</option><option v-for="w in linkableItems" :key="w.id" :value="w.id">{{ w.title }}</option></select>
                <button class="icard-btn mono" type="button" :disabled="!linkPick[s.job_id] || linkBusy === s.job_id" data-test="pty-link-btn" @click="linkJobToWork(s.job_id)">关联</button>
              </dd>
            </template>
            <dt>加密</dt><dd>{{ s.encrypted ? '加密' : '明文' }}</dd>
            <dt>录制</dt><dd>{{ s.has_recording ? '已录制' : '无录制' }}</dd>
          </dl>
        </template>
      </InfoCard>
    </div>

    <div v-else-if="!loading && !error" class="empty mono">
      暂无终端会话
    </div>
    <p v-if="hasSessions" class="sessions-note mono">
      输入/输出字节是 relay 计数；只有已录制的会话会保留可下载回放。
    </p>
    </section>

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
.link-pick { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.link-pick select { max-width: 100%; min-width: 0; }
.board {
  max-width: 1280px;
  margin: 0 auto;
}
.session-create {
  margin: 0 0 20px;
  padding: 12px 14px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  color: var(--queue);
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
  color: var(--queue);
  font-size: 11px;
}
.session-field input,
.session-field select,
.session-field textarea {
  min-width: 0;
  color: var(--paper);
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px 7px;
  font: inherit;
}
.session-field textarea { resize: vertical; }
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
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.controls {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--queue);
  font-size: 12px;
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}
.act {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  text-decoration: none;
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--primary {
  color: var(--phosphor);
}
.sessions-note {
  margin: 10px 0 0;
  color: var(--queue);
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
.group-title {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0;
  font-size: 13px;
  letter-spacing: 0.08em;
  color: var(--paper);
  text-transform: uppercase;
}
.group-count {
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 0 7px;
  font-size: 10px;
  color: var(--queue);
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
  color: var(--queue);
  user-select: none;
}
.check input {
  accent-color: var(--phosphor);
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
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.wake-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.wake-link {
  color: var(--phosphor);
}
.a-wake {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.a-session-id {
  color: var(--paper);
}
.a-peername {
  background: transparent;
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 5px;
  color: var(--phosphor);
  font-size: inherit;
  cursor: pointer;
}
.a-peername:hover {
  border-color: var(--phosphor);
}
.a-peer-status,
.a-peer-messaging {
  color: var(--queue);
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
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 5px;
  font-size: 10px;
  cursor: pointer;
}
.copy-btn:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.a-lastbox {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.a-last {
  color: var(--queue);
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
  color: var(--phosphor);
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
  color: var(--paper);
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
  color: var(--queue);
}
.relay-err {
  color: var(--fail);
  font-weight: 600;
  font-size: 12px;
}

/* 自动布防说明：默认收起，点标题旁的「?」展开 */
.relay-note {
  margin: 0;
  padding: 6px 14px;
  margin-bottom: 12px;
  color: var(--queue);
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
  border-radius: var(--radius);
  overflow: hidden;
}
.relay-mode {
  all: unset;
  padding: 3px 9px;
  font-size: 11px;
  line-height: 16px;
  color: var(--queue);
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
  color: var(--paper);
}
.relay-mode.active.auto {
  background: rgba(79, 176, 198, 0.18);
  color: var(--run);
}
.relay-mode.active.on {
  background: rgba(79, 176, 198, 0.18);
  color: var(--phosphor);
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
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 13px;
  padding: 28px 14px;
  text-align: center;
}

@media (max-width: 900px) {
  .group-head {
    flex-wrap: wrap;
    gap: 8px;
  }
}
</style>
