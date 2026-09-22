<script setup lang="ts">
// Job 详情：getJob 填头部；非终态走 SSE 回放+跟随，终态日志走 HTTP 按行分页。
//  - SSE from 仅用于断线重连（已收 stdout 字节数）。
//  - status 事件回填头部/徽标/耗时；end/终态停 live；running 显示 cancel。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import StatusBadge from '../components/StatusBadge.vue'
import Signal from '../components/Signal.vue'
import LogTape from '../components/LogTape.vue'
import InteractionCard from '../components/InteractionCard.vue'
import FilePreview from '../components/FilePreview.vue'
import ReviewPanel from '../components/ReviewPanel.vue'
import AttachTerminal from '../components/AttachTerminal.vue'
import {
  answerInteraction,
  cancelJob,
  downloadArtifact,
  downloadPtyRecording,
  fetchArtifactBlob,
  fetchDiffText,
  fetchJobLog,
  getInteractions,
  getJob,
  listArtifacts,
  listDeliveries,
  listEvents,
  listJobs,
  listPtySessions,
  listRetries,
  listWakeups,
  puntInteraction,
  resumeJob,
  createWakeup,
  deleteWakeup,
  setWakeupEnabled,
} from '../api/client'
import { appendCapped, streamJob } from '../api/sse'
import { fmtDuration, jobDurationSec, toUnixSec } from '../api/time'
import { shortSha, usageLine, verifyClass, verifyLabel } from '../utils/jobOutcome'
import type {
  Artifact,
  Delivery,
  Interaction,
  Job,
  JobCommit,
  JobEvent,
  JobStatus,
  JobUsage,
  JobVerify,
  JobXfer,
  JobXferCollected,
  JobXferSkipped,
  JobXferUpload,
  LogStream,
  PtySession,
  Retry,
  SSEEvent,
  SSEInteractionData,
  SSEJobEventData,
  SSELogData,
  SSELogRotatedData,
  Wakeup,
  WakeupKind,
} from '../api/types'

const props = defineProps<{ id: string }>()
const route = useRoute()
const router = useRouter()

const job = ref<Job | null>(null)
const stdout = ref('')
const stderr = ref('')
const headError = ref('')
const streamError = ref('')
const cancelling = ref(false)
const LOG_PAGE_SIZE = 200

interface LogPageState {
  offset: number
  total: number
  loading: boolean
}

const logPages = ref<Record<LogStream, LogPageState>>({
  stdout: { offset: 0, total: 0, loading: false },
  stderr: { offset: 0, total: 0, loading: false },
})

// 运行中交互：按 id upsert（SSE interaction 事件 + answer 返回回填）
const interactions = ref<Map<string, Interaction>>(new Map())
// 每张卡的提交态（防重复提交）
const submittingIds = ref<Set<string>>(new Set())
// 单条交互作答失败文案（按 id）
const interactionErrors = ref<Map<string, string>>(new Map())

function upsertInteraction(it: Interaction): void {
  // 重新赋值 new Map 触发响应式
  const next = new Map(interactions.value)
  next.set(it.id, it)
  interactions.value = next
}

function setInteractionSubmitting(iid: string, submitting: boolean): void {
  const next = new Set(submittingIds.value)
  if (submitting) {
    next.add(iid)
  } else {
    next.delete(iid)
  }
  submittingIds.value = next
}

function clearInteractionError(iid: string): void {
  if (!interactionErrors.value.has(iid)) {
    return
  }
  const next = new Map(interactionErrors.value)
  next.delete(iid)
  interactionErrors.value = next
}

function setInteractionError(iid: string, message: string): void {
  interactionErrors.value = new Map(interactionErrors.value).set(iid, message)
}

async function refreshInteractions(): Promise<void> {
  const resp = await getInteractions(props.id)
  interactions.value = new Map(
    (resp.interactions ?? []).map((it) => [it.id, it]),
  )
}

// ── 事件时间线（E13）──────────────────────────────────────────────
// append-only 生命周期事件，按 seq 去重有序。初始 listEvents 拉全量 + SSE event
// 帧增量 append（seq 已存在则跳过，保证幂等且不依赖到达顺序）。
const timelineSeqs = new Set<number>()
const timelineEvents = ref<JobEvent[]>([])

function addTimelineEvent(ev: JobEvent): void {
  if (timelineSeqs.has(ev.seq)) {
    return
  }
  timelineSeqs.add(ev.seq)
  // 二分插入保持 seq 升序（事件量小，splice 足够；不依赖到达顺序）。
  const arr = timelineEvents.value
  let lo = 0
  let hi = arr.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (arr[mid].seq < ev.seq) {
      lo = mid + 1
    } else {
      hi = mid
    }
  }
  const next = arr.slice()
  next.splice(lo, 0, ev)
  timelineEvents.value = next
}

// 事件 type -> 图标 + 中文标签（仿 interactions 渲染风格，单行）。
const EVENT_META: Record<string, { icon: string; label: string }> = {
  'job.submitted': { icon: '✓', label: '已提交' },
  'job.dispatched': { icon: '→', label: '已派发' },
  'job.running': { icon: '▶', label: '开始运行' },
  'job.terminal': { icon: '■', label: '结束' },
  'job.cancelled': { icon: '✕', label: '请求取消' },
  'interaction.created': { icon: '?', label: '发起交互' },
  'interaction.answered': { icon: '✎', label: '交互已答' },
  // 审批门（GATE-01 S1）+ acp 回合汇总（bd h-aii-rnxk：时间线只留生命周期，
  // 工具调用等执行细节走 stderr 的紧凑事件行，由 NdjsonTimeline 渲染）
  'job.acp_summary': { icon: '⚙', label: 'ACP 回合' },
  'job.permission_requested': { icon: '⚠', label: '求批' },
  'job.permission_answered': { icon: '✎', label: '审批已答' },
  'job.permission_timed_out': { icon: '⏱', label: '审批超时' },
  // JOB-11 / AUTO-05：等目录锁（非终态，等同 queued）与输出停滞（job 已被看门狗杀掉）。
  'job.waiting_dir': { icon: '⏳', label: '等目录锁' },
  'job.stalled': { icon: '⚠', label: '输出停滞（已杀）' },
  // AGT-04：事后捕获到 session_id（by=fallback 说明该 agent 还没写自己的正则）。
  'job.session_captured': { icon: '⚿', label: '捕获会话' },
}

function eventIcon(type: string): string {
  return EVENT_META[type]?.icon ?? '•'
}
function eventLabel(type: string): string {
  return EVENT_META[type]?.label ?? type
}

// 解析 detail_json，提取每类事件的关键字段拼成一行补充说明（无则空）。
function eventDetailText(ev: JobEvent): string {
  if (!ev.detail) {
    return ''
  }
  let d: Record<string, unknown>
  try {
    d = JSON.parse(ev.detail) as Record<string, unknown>
  } catch {
    return ''
  }
  switch (ev.type) {
    case 'job.submitted':
      return [d.agent, d.runner].filter(Boolean).join(' · ')
    case 'job.dispatched':
      return [d.runner, d.worker_id].filter(Boolean).join(' · ')
    case 'job.terminal': {
      const parts: string[] = []
      if (d.status) {
        parts.push(String(d.status))
      }
      if (typeof d.exit_code === 'number' && d.exit_code !== 0) {
        parts.push(`exit ${d.exit_code}`)
      }
      if (d.error) {
        parts.push(String(d.error))
      }
      return parts.join(' · ')
    }
    case 'interaction.created':
      return String(d.prompt ?? '')
    case 'interaction.answered':
      return String(d.answer ?? '')
    // 审批门（GATE-01 S1）：求批/作答/超时都带工具调用与选项，时间线据此可读；
    // acp 回合汇总（bd h-aii-rnxk）给的是本回合的执行计数。
    case 'job.acp_summary': {
      // permissions_auto (F4) = 本回合 gofer 自动裁决的求批数（off / auto_allow_kind /
      // remembered / timeout）。自动裁决不再各占一条 job.permission_answered，所以
      // 时间线上只有这里能看到它们。
      const auto = Number(d.permissions_auto ?? 0)
      const permissions = `${d.permissions ?? 0} 次求批`
      const parts = [
        `${d.tool_calls ?? 0} 次工具调用`,
        `${d.thoughts ?? 0} 段思考`,
        auto > 0 ? `${permissions}（自动 ${auto}）` : permissions,
      ]
      if (d.stop_reason) {
        parts.push(String(d.stop_reason))
      }
      return parts.join(' · ')
    }
    case 'job.permission_requested':
      return [d.kind, d.title, d.policy_hint].filter(Boolean).join(' · ')
    case 'job.permission_answered': {
      const parts = [d.kind, d.option_id].filter(Boolean).map(String)
      if (d.auto === true) {
        parts.push('自动')
      } else if (d.by) {
        parts.push(`by ${d.by}`)
      }
      return parts.join(' · ')
    }
    case 'job.permission_timed_out':
      return [d.kind, d.title, `on_timeout=${d.on_timeout ?? 'reject'}`].filter(Boolean).join(' · ')
    // JOB-11：谁在占着目录（芯片/详情只给结论，时间线说清"等的是谁"）。
    case 'job.waiting_dir':
      return [`holder=${d.holder_job ?? '?'}`, d.dir].filter(Boolean).join(' · ')
    // AUTO-05：静默了多久、窗口多长——解释 job 为什么被判为停滞。
    case 'job.stalled':
      return [`静默 ${d.silent_sec ?? '?'}s`, `窗口 ${d.stall_timeout_sec ?? '?'}s`].join(' · ')
    // AGT-04：哪个 agent、从哪读到、是内置/显式正则还是通用兜底——by=fallback
    // 就是「该给这个 agent 写条 session_capture」的信号。
    case 'job.session_captured':
      return [`agent=${d.agent ?? '?'}`, d.source, d.by === 'fallback' ? '兜底' : 'agent 配置']
        .filter(Boolean)
        .join(' · ')
    default:
      return ''
  }
}

// 待应答（pending），按 created_at 升序排队作答
const pendingInteractions = computed<Interaction[]>(() =>
  Array.from(interactions.value.values())
    .filter((it) => it.status === 'pending')
    .sort((a, b) => a.created_at - b.created_at),
)

// 已应答（answered），按 created_at 升序，折叠淡化展示
const answeredInteractions = computed<Interaction[]>(() =>
  Array.from(interactions.value.values())
    .filter((it) => it.status === 'answered')
    .sort((a, b) => a.created_at - b.created_at),
)

async function onAnswer(iid: string, value: string): Promise<void> {
  if (submittingIds.value.has(iid)) {
    return
  }
  // 标记 submitting + 清旧错误
  setInteractionSubmitting(iid, true)
  clearInteractionError(iid)
  try {
    const updated = await answerInteraction(props.id, iid, value)
    // 乐观回填（SSE answered 事件也会回填，幂等）
    upsertInteraction(updated)
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    setInteractionError(iid, msg)
  } finally {
    setInteractionSubmitting(iid, false)
  }
}

async function onPunt(iid: string): Promise<void> {
  if (submittingIds.value.has(iid)) {
    return
  }
  setInteractionSubmitting(iid, true)
  clearInteractionError(iid)
  try {
    await puntInteraction(props.id, iid)
    await refreshInteractions()
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    setInteractionError(iid, msg)
  } finally {
    setInteractionSubmitting(iid, false)
  }
}

// 终态集合（rejected = GATE-01 S3：人拒绝，与 failed 一样是终态，只是不会被自动重试/续投）
const TERMINAL: JobStatus[] = ['done', 'failed', 'cancelled', 'timeout', 'rejected']
function isTerminal(s: JobStatus | undefined): boolean {
  return s != null && TERMINAL.includes(s)
}

const status = computed<JobStatus>(() => job.value?.status ?? 'queued')
const live = computed(() => status.value === 'running')
const showCancel = computed(() => status.value === 'running' && !cancelling.value)
const canOpenTerminal = computed(
  () =>
    !!job.value?.interactive &&
    status.value === 'running' &&
    !!job.value?.can_attach,
)
const showTerminalButton = computed(
  () => !!job.value?.interactive && status.value === 'running',
)
// 头部 exit_code 仅在终态展示（运行中无意义）
const isTerminalView = computed(() => isTerminal(job.value?.status))
const stdoutCanLoadEarlier = computed(() => canLoadEarlier('stdout'))
const stderrCanLoadEarlier = computed(() => canLoadEarlier('stderr'))

// 实时秒级时钟（驱动 running 耗时刷新）
const nowSec = ref(Math.floor(Date.now() / 1000))
let clockTimer: number | null = null

const durationSec = computed(() => {
  if (!job.value) {
    return null
  }
  return jobDurationSec(job.value, nowSec.value)
})
const durationText = computed(() => fmtDuration(durationSec.value))

// SSE 速率估算：滑动窗口内收到的 stdout/stderr 文本行数 / 时间 -> 行每秒
const recentLines = ref<Array<{ t: number; n: number }>>([])
const logRate = computed(() => {
  const cutoff = Date.now() - 4000
  const recent = recentLines.value.filter((r) => r.t >= cutoff)
  const lines = recent.reduce((a, r) => a + r.n, 0)
  return lines > 0 ? lines / 4 : 0
})

// 已累计接收的 stdout 字节数（按 UTF-8 字节计，用于断线重连 from）
const encoder = new TextEncoder()
let stdoutBytes = 0

let abortCtrl: AbortController | null = null
let reconnectedOnce = false

function applyStatus(j: Job): void {
  // 合并字段（status 事件可能只带部分信息，但后端给的是完整 Job）
  job.value = { ...(job.value ?? {}), ...j } as Job
}

function onEvent(ev: SSEEvent): void {
  if (ev.type === 'status') {
    applyStatus(ev.data as Job)
    return
  }
  if (ev.type === 'log-rotated') {
    // 后端日志轮转：清空该 stream 的缓冲后续读新文件（不重置 stdoutBytes，
    // 它用于断线重连的 from offset，由后端 offset 语义对齐）。
    const d = ev.data as SSELogRotatedData
    if (d.stream === 'stderr') {
      stderr.value = ''
    } else {
      stdout.value = ''
    }
    return
  }
  if (ev.type === 'log') {
    const d = ev.data as SSELogData
    // 帧按到达顺序（= seq 顺序，单连接 TCP 有序）追加，并窗口化到字节上限：
    // 超大/高频日志只保留最近 N 字节，避免浏览器内存无界增长（C4 前端兜底）。
    if (d.stream === 'stdout') {
      stdout.value = appendCapped(stdout.value, d.text)
      stdoutBytes += encoder.encode(d.text).length
    } else {
      stderr.value = appendCapped(stderr.value, d.text)
    }
    const n = countLines(d.text)
    if (n > 0) {
      recentLines.value.push({ t: Date.now(), n })
      // 限制窗口大小
      if (recentLines.value.length > 200) {
        recentLines.value.splice(0, recentLines.value.length - 200)
      }
    }
    return
  }
  if (ev.type === 'interaction') {
    const d = ev.data as SSEInteractionData
    // action: open/answered/cancelled —— 统一按 id upsert（幂等）
    upsertInteraction(d.interaction)
    return
  }
  if (ev.type === 'event') {
    // E13 生命周期事件：按 seq 去重 append 到时间线（与初始 listEvents 合并）。
    addTimelineEvent(ev.data as SSEJobEventData)
    return
  }
  // end：无更多事件；终态由 status 事件回填，这里仅停 live 由 status 决定
}

function countLines(text: string): number {
  if (!text) {
    return 0
  }
  let c = 0
  for (const ch of text) {
    if (ch === '\n') {
      c++
    }
  }
  return c
}

async function startStream(from?: number): Promise<void> {
  const ctrl = new AbortController()
  abortCtrl = ctrl
  try {
    await streamJob(props.id, { from, signal: ctrl.signal, onEvent })
    // 流正常结束：若非终态且未重连过 -> 自动用 from 重连一次
    if (!isTerminal(job.value?.status) && !reconnectedOnce) {
      reconnectedOnce = true
      void startStream(stdoutBytes)
    }
  } catch (e) {
    if (ctrl.signal.aborted) {
      return
    }
    // 异常结束：非终态自动重连一次，再失败提示手动重连
    if (!isTerminal(job.value?.status) && !reconnectedOnce) {
      reconnectedOnce = true
      void startStream(stdoutBytes)
    } else {
      streamError.value = e instanceof Error ? e.message : String(e)
    }
  }
}

function manualReconnect(): void {
  streamError.value = ''
  reconnectedOnce = false
  void startStream(stdoutBytes)
}

function logTextRef(stream: LogStream) {
  return stream === 'stdout' ? stdout : stderr
}

function canLoadEarlier(stream: LogStream): boolean {
  if (!isTerminalView.value) {
    return false
  }
  const page = logPages.value[stream]
  return page.offset + LOG_PAGE_SIZE < page.total
}

async function loadTerminalLog(
  stream: LogStream,
  mode: 'initial' | 'earlier' | 'all',
): Promise<void> {
  const page = logPages.value[stream]
  if (page.loading) {
    return
  }
  page.loading = true
  streamError.value = ''
  try {
    const nextOffset = mode === 'earlier' ? page.offset + LOG_PAGE_SIZE : 0
    const resp = await fetchJobLog(props.id, stream, {
      lines: LOG_PAGE_SIZE,
      offset: nextOffset,
      full: mode === 'all',
    })
    const target = logTextRef(stream)
    if (mode === 'earlier') {
      target.value = resp.text + target.value
      page.offset = resp.offset
    } else {
      target.value = resp.text
      page.offset = mode === 'all' ? resp.total : resp.offset
    }
    page.total = resp.total
  } catch (e) {
    streamError.value = e instanceof Error ? e.message : String(e)
  } finally {
    page.loading = false
  }
}

async function loadTerminalLogs(): Promise<void> {
  await Promise.all([
    loadTerminalLog('stdout', 'initial'),
    loadTerminalLog('stderr', 'initial'),
  ])
}

function onLoadEarlier(stream: LogStream): void {
  void loadTerminalLog(stream, 'earlier')
}

function onLoadAll(stream: LogStream): void {
  void loadTerminalLog(stream, 'all')
}

// session 续跑：仅对有 session_id 的 job 可用。续投新 job（同会话、继承 plan_id），跳新 job。
const showResumeForm = ref(false)
const resumePrompt = ref('')
const resuming = ref(false)
const resumeError = ref('')

async function doResume(): Promise<void> {
  if (resuming.value) return
  resuming.value = true
  resumeError.value = ''
  try {
    const newJob = await resumeJob(props.id, resumePrompt.value)
    resumePrompt.value = ''
    showResumeForm.value = false
    // 交互源续接为 pty job：跳转即自动打开终端（?attach=1，:567 已有处理），在 TUI 里继续。
    const q = job.value?.interactive ? '?attach=1' : ''
    void router.push(`/jobs/${encodeURIComponent(newJob.id)}${q}`)
  } catch (e) {
    resumeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    resuming.value = false
  }
}

// 会话链：同 session_id 的全部 job（含本 job）。resume 复用源 job 的 session_id，
// 故一条 resume 链共享同一 sid。后端无 parent_job_id，链内顺序按 started_at 升序还原。
const sessionJobs = ref<Job[]>([])
const sessionJobsOpen = ref(false)

async function loadSessionJobs(): Promise<void> {
  const sid = job.value?.session_id
  if (!sid) return
  try {
    const resp = await listJobs({ session: sid, limit: 50 })
    sessionJobs.value = [...resp.jobs].sort((a, b) => a.started_at - b.started_at)
  } catch {
    sessionJobs.value = []  // 链表是增强信息，拉取失败静默降级，不打断详情页
  }
}

// 人工验收（GATE-01 S3 + REV-01）：验收面板是一个独立组件（五个页签 + 底部裁决条），
// 出现时机 = 待裁决（needs_review）、已拒绝（rejected）或按要求验收且已完成（done +
// require_review，此时面板只读地回显 reviewed_by/at/note）。裁决与跳转由面板回传。
const showReviewPanel = computed<boolean>(() => {
  const j = job.value
  if (!j) {
    return false
  }
  if (j.status === 'needs_review' || j.status === 'rejected') {
    return true
  }
  return j.status === 'done' && !!j.require_review
})

// reject 勾了「自动续投」时后端另起一个 job：跳过去继续盯（与旧验收卡同行为）。
function onReviewResumed(jobId: string): void {
  void router.push(`/jobs/${encodeURIComponent(jobId)}`)
}

async function doCancel(): Promise<void> {
  cancelling.value = true
  try {
    const j = await cancelJob(props.id)
    // 乐观：以返回回填；最终终态以后续 status 事件为准
    applyStatus(j)
  } catch (e) {
    cancelling.value = false
    streamError.value = e instanceof Error ? e.message : String(e)
  }
}

function startClock(): void {
  clockTimer = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 1000)
}

async function loadCurrentJob(): Promise<void> {
  if (abortCtrl) {
    abortCtrl.abort()
    abortCtrl = null
  }
  job.value = null
  stdout.value = ''
  stderr.value = ''
  headError.value = ''
  streamError.value = ''
  cancelling.value = false
  showResumeForm.value = false
  resumePrompt.value = ''
  resumeError.value = ''
  sessionJobs.value = []
  sessionJobsOpen.value = false
  interactions.value = new Map()
  submittingIds.value = new Set()
  interactionErrors.value = new Map()
  timelineSeqs.clear()
  timelineEvents.value = []
  recentLines.value = []
  stdoutBytes = 0
  reconnectedOnce = false
  logPages.value = {
    stdout: { offset: 0, total: 0, loading: false },
    stderr: { offset: 0, total: 0, loading: false },
  }
  artifacts.value = []
  downloadingNames.value = new Set()
  artifactError.value = ''
  deliveries.value = []
  ptySessions.value = []
  ptySessionError.value = ''
  downloadingRecordingIds.value = new Set()
  wakeups.value = []
  wakeupError.value = ''
  wakeupBusy.value = new Set()
  wakeupFormOpen.value = false
  wakeupFormError.value = ''
  preview.value = null
  previewingNames.value = new Set()
  previewError.value = ''
  xferFileError.value = ''
  diffError.value = ''
  diffLoading.value = false
  diffOpen.value = false
  fullDiffText.value = ''
  terminalOpen.value = false
  termExitCode.value = null
  termExited.value = false
  termError.value = ''
  // 先取头部（即便 stream 也会回填，但 getJob 让头部更快可见）
  try {
    job.value = await getJob(props.id)
    void loadSessionJobs()
    if (route.query.attach === '1') {
      openTerminal()
    }
  } catch (e) {
    headError.value = e instanceof Error ? e.message : String(e)
  }
  // 产物清单：与头部一起拉一次（终态 job 才有；运行中通常为空）。
  void loadArtifacts()
  // 事件时间线：初始拉全量（SSE event 帧再增量 append，按 seq 去重幂等）。
  void loadTimeline()
  // webhook 投递状态（E14）：拉一次只读快照（无通知配置时为空，整节不展示）。
  void loadDeliveries()
  // pty 会话元数据：只读辅助面板，失败不阻断详情主流程。
  void loadPtySessions()
  // 唤醒（JOB-09）：登记在 job 上的订阅/定时器，失败只在本块显示。
  void loadWakeups()
  // 可靠重试（AUTO-03）：源 job 失败后服务端排的重试，失败静默忽略（头部提示用）。
  void loadRetries()
  if (isTerminal(job.value?.status)) {
    // 终态 job 不再走 SSE 全量回放：按行分页加载，避免 2MiB 前端窗口丢历史。
    void loadTerminalLogs()
  } else {
    // 非终态保持原 SSE 路径：历史回放 + 实时跟随 + 断线 from 重连。
    void startStream()
  }
}

function fmtTime(v: string | number | undefined): string {
  const sec = toUnixSec(v)
  if (sec == null) {
    return '—'
  }
  return new Date(sec * 1000).toLocaleString()
}

function shortId(id: string): string {
  return id.length > 8 ? id.slice(-8) : id
}

// ── 浏览器终端 attach（WEB-03 P4）──────────────────────────────────
const terminalOpen = ref(false)
const termMode = ref<'write' | 'read'>('write')
const termExitCode = ref<number | null>(null)
const termExited = ref(false)
const termError = ref('')

function openTerminal(): void {
  if (!canOpenTerminal.value) {
    return
  }
  termError.value = ''
  termExited.value = false
  termExitCode.value = null
  terminalOpen.value = true
}

function closeTerminal(): void {
  terminalOpen.value = false
}

async function refreshJob(): Promise<void> {
  try {
    job.value = { ...(job.value ?? {}), ...(await getJob(props.id)) } as Job
  } catch {
    // 终端 ready 状态是辅助态，刷新失败不影响详情主流程。
  }
}

async function onTermExit(code?: number): Promise<void> {
  termExited.value = true
  termExitCode.value = code ?? null
  await refreshJob()
  void loadPtySessions()
}

function onTermError(msg: string): void {
  termError.value = msg
}

const terminalExitText = computed(() => {
  if (!termExited.value && !isTerminalView.value) {
    return ''
  }
  const code = isTerminalView.value ? job.value?.exit_code : termExitCode.value
  return code == null ? '进程已退出' : `进程已退出 · exit ${code}`
})

// ── 产出与审计（job-outcomes-audit）──────────────────────────────
// 渲染命令(E15)：后端 rendered_command 是 {command,args,env_keys} 的 JSON 字符串。
interface RenderedCommand {
  command: string
  args?: string[]
  env_keys?: string[]
}
const renderedCommand = computed<RenderedCommand | null>(() => {
  const raw = job.value?.rendered_command
  if (!raw) {
    return null
  }
  try {
    return JSON.parse(raw) as RenderedCommand
  } catch {
    return null
  }
})
// 命令行展示文本：command + args（空格连接，仅用于「复制」）。
const renderedCommandLine = computed<string>(() => {
  const rc = renderedCommand.value
  if (!rc) {
    return ''
  }
  return [rc.command, ...(rc.args ?? [])].join(' ')
})
const commandMarkdownMode = ref(false)
const canViewCommandMarkdown = computed<boolean>(() => {
  return (job.value?.agent ?? '') !== 'exec' && renderedCommandLine.value.length > 200
})
const renderedCommandMarkdown = computed<string>(() => {
  return DOMPurify.sanitize(marked.parse(renderedCommandLine.value, { async: false }))
})

// 结构化结果(E6)：后端 result_json 是原始 JSON 字符串，pretty-print 展示。
const resultJsonPretty = computed<string>(() => {
  const raw = job.value?.result_json
  if (!raw) {
    return ''
  }
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    // 后端已校验为合法 JSON；万一解析失败则原样展示，不丢内容。
    return raw
  }
})

// 产物清单(E1)：终态后拉取一次（GET /v1/jobs/{id}/artifacts）。
const artifacts = ref<Artifact[]>([])
const downloadingNames = ref<Set<string>>(new Set())
const artifactError = ref('')

// 文件传输（XFER-01 X2）：后端只在 job 真用过 --upload/--collect 时发 xfer（没用过 =
// 缺省 → 整个「文件」块不渲染）。收集到的文件就列在产物清单的 collected/ 下，故它复用
// 下面同一套预览/下载（只是错误提示就近落在自己的块里，见 ArtifactScope）。
const xfer = computed<JobXfer | null>(() => job.value?.xfer ?? null)
const xferUploads = computed<JobXferUpload[]>(() => xfer.value?.uploads ?? [])
const xferCollected = computed<JobXferCollected[]>(() => xfer.value?.collected ?? [])
const xferSkipped = computed<JobXferSkipped[]>(() => xfer.value?.skipped ?? [])
// 三段全空（如提交了 collect 但一个都没匹配上）等同没有内容，不摆一个空盒子。
const hasXferDetail = computed<boolean>(
  () =>
    xferUploads.value.length > 0 ||
    xferCollected.value.length > 0 ||
    xferSkipped.value.length > 0,
)
// 收集到的文件在产物里的固定前缀（后端落盘位置）：name 是项目根相对路径，产物名再加这层。
const COLLECTED_PREFIX = 'collected/'
function collectedName(name: string): string {
  return COLLECTED_PREFIX + name
}

async function loadArtifacts(): Promise<void> {
  try {
    const resp = await listArtifacts(props.id)
    artifacts.value = resp.artifacts ?? []
  } catch (e) {
    artifactError.value = e instanceof Error ? e.message : String(e)
  }
}

// 时间线初始拉取（E13）：SSE event 帧与之合并去重，故初拉失败不致命（静默）。
async function loadTimeline(): Promise<void> {
  try {
    const resp = await listEvents(props.id)
    for (const ev of resp.events ?? []) {
      addTimelineEvent(ev)
    }
  } catch {
    // 时间线为辅助信息：拉取失败静默（SSE event 帧仍会增量补齐）。
  }
}

// ── webhook 投递（E14）──────────────────────────────────────────────
// 只读：拉取本 job 的事件外发投递记录（无通知配置时为空，整节不展示）。
const deliveries = ref<Delivery[]>([])

async function loadDeliveries(): Promise<void> {
  try {
    const resp = await listDeliveries(props.id)
    deliveries.value = resp.deliveries ?? []
  } catch {
    // 投递为辅助信息：拉取失败静默（不影响详情主流程）。
  }
}

// ── pty sessions 元数据（WEB-03 P4）───────────────────────────────
const ptySessions = ref<PtySession[]>([])
const ptySessionError = ref('')
const downloadingRecordingIds = ref<Set<string>>(new Set())

async function loadPtySessions(): Promise<void> {
  try {
    const resp = await listPtySessions(props.id)
    ptySessions.value = resp.sessions ?? []
    ptySessionError.value = ''
  } catch (e) {
    ptySessionError.value = e instanceof Error ? e.message : String(e)
  }
}

function ptySessionDuration(s: PtySession): string {
  const end = s.ended_at && s.ended_at > 0 ? s.ended_at : nowSec.value
  return fmtDuration(Math.max(0, end - s.started_at))
}

function ptySessionBytes(s: PtySession): string {
  return `输入 ${s.bytes_in} / 输出 ${s.bytes_out} 字节`
}

async function onDownloadRecording(sessionID: string): Promise<void> {
  if (downloadingRecordingIds.value.has(sessionID)) {
    return
  }
  ptySessionError.value = ''
  downloadingRecordingIds.value = new Set(downloadingRecordingIds.value).add(sessionID)
  try {
    await downloadPtyRecording(props.id)
  } catch (e) {
    ptySessionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    const next = new Set(downloadingRecordingIds.value)
    next.delete(sessionID)
    downloadingRecordingIds.value = next
  }
}

// ── 唤醒（JOB-09）─────────────────────────────────────────────────
// 登记在 job 上的事件订阅/定时器：条件到达时 gofer 自动续投这个 job。列表是只读快照
// （开关/新建后重拉），触发历史直接用事件时间线里的 job.wakeup_* 事件，不再单独请求。
const wakeups = ref<Wakeup[]>([])
const wakeupError = ref('')
const wakeupBusy = ref<Set<string>>(new Set())
const wakeupFormOpen = ref(false)
const wakeupSubmitting = ref(false)
const wakeupFormError = ref('')
const wakeupForm = ref<{
  kind: WakeupKind
  after: string
  every: string
  cron: string
  timezone: string
  event: string
  filterJob: string
  status: string
  mode: string
  instruction: string
}>({
  kind: 'at',
  after: '10m',
  every: '1h',
  cron: '',
  timezone: '',
  event: 'job.terminal',
  filterJob: '',
  status: '',
  mode: '',
  instruction: '',
})

// 事件目录（与后端 job.WakeupEventTypes 同序）：下拉/提示共用一份。
const WAKEUP_EVENT_TYPES = [
  'job.terminal',
  'job.verify_finished',
  'job.needs_review',
  'job.reviewed',
  'job.fell_back',
  'job.stalled',
  'interaction.answered',
  'session.takeover_released',
]

async function loadWakeups(): Promise<void> {
  try {
    const resp = await listWakeups(props.id)
    wakeups.value = resp.wakeups ?? []
    wakeupError.value = ''
  } catch (e) {
    wakeupError.value = e instanceof Error ? e.message : String(e)
  }
}

// wakeupSummary 是头部一行摘要：还有几条在等、分别是什么形态。
const wakeupSummary = computed<string>(() => {
  const on = wakeups.value.filter((w) => w.enabled)
  if (wakeups.value.length === 0) {
    return ''
  }
  if (on.length === 0) {
    return `0 条生效（${wakeups.value.length} 条已停用）`
  }
  const counts = new Map<string, number>()
  for (const w of on) {
    counts.set(w.kind, (counts.get(w.kind) ?? 0) + 1)
  }
  const parts = ['at', 'every', 'cron', 'event']
    .filter((k) => counts.has(k))
    .map((k) => `${counts.get(k)} ${k}`)
  return `${on.length} 条生效（${parts.join('、')}）`
})

// wakeupTrigger 是「它在等什么」：定时器的下次时刻，或订阅的事件（含状态过滤）。
function wakeupTrigger(w: Wakeup): string {
  switch (w.kind) {
    case 'event': {
      const types = (w.event_types ?? []).join(', ')
      const st = (w.filter_status ?? []).length > 0 ? `[${(w.filter_status ?? []).join(',')}]` : ''
      return `监听 ${types}${st}`
    }
    case 'every':
      return `每 ${fmtDuration(w.every_sec ?? 0)}`
    case 'cron':
      return `cron ${w.cron ?? ''}${w.timezone ? `（${w.timezone}）` : ''}`
    default:
      return `到点 ${fmtTime(w.next_run_at || w.at)}`
  }
}

// wakeupTarget 说明这条唤醒是谁在等谁：事件订阅可指向另一个 job（--job-id）。
function wakeupTarget(w: Wakeup): string {
  if (w.kind !== 'event') {
    return w.job_id === props.id ? '本 job' : shortId(w.job_id)
  }
  const src = w.filter_job_id ?? ''
  if (!src || src === props.id) {
    return '本 job 的事件'
  }
  return `job ${shortId(src)} 的事件`
}

function wakeupBusyOn(id: string): boolean {
  return wakeupBusy.value.has(id)
}

function setWakeupBusy(id: string, busy: boolean): void {
  const next = new Set(wakeupBusy.value)
  if (busy) {
    next.add(id)
  } else {
    next.delete(id)
  }
  wakeupBusy.value = next
}

async function onToggleWakeup(w: Wakeup): Promise<void> {
  setWakeupBusy(w.id, true)
  wakeupError.value = ''
  try {
    await setWakeupEnabled(w.id, !w.enabled)
    await loadWakeups()
  } catch (e) {
    wakeupError.value = e instanceof Error ? e.message : String(e)
  } finally {
    setWakeupBusy(w.id, false)
  }
}

async function onDeleteWakeup(w: Wakeup): Promise<void> {
  setWakeupBusy(w.id, true)
  wakeupError.value = ''
  try {
    await deleteWakeup(w.id)
    await loadWakeups()
  } catch (e) {
    wakeupError.value = e instanceof Error ? e.message : String(e)
  } finally {
    setWakeupBusy(w.id, false)
  }
}

// parseDuration 只认 <数字><单位> 的简单写法（s/m/h），够用且不会把 10m 误读成别的。
function parseDuration(text: string): number | null {
  const m = /^(\d+)([smh])$/.exec(text.trim())
  if (!m) {
    return null
  }
  const n = Number(m[1])
  return m[2] === 's' ? n : m[2] === 'm' ? n * 60 : n * 3600
}

// onCreateWakeup 把表单折成四种 kind 各自的最小请求体（其余字段后端有默认值）。
async function onCreateWakeup(): Promise<void> {
  const f = wakeupForm.value
  const spec: Record<string, unknown> = { kind: f.kind }
  if (f.instruction.trim()) {
    spec.instruction = f.instruction.trim()
  }
  if (f.mode) {
    spec.mode = f.mode
  }
  switch (f.kind) {
    case 'at': {
      const sec = parseDuration(f.after)
      if (sec == null) {
        wakeupFormError.value = '--after 需要 <数字><s|m|h>，例如 10m / 2h'
        return
      }
      spec.at = Math.floor(Date.now() / 1000) + sec
      break
    }
    case 'every': {
      const sec = parseDuration(f.every)
      if (sec == null || sec < 60) {
        wakeupFormError.value = '--every 需要 <数字><s|m|h> 且不小于 60s，例如 1h'
        return
      }
      spec.every_sec = sec
      break
    }
    case 'cron': {
      if (!f.cron.trim()) {
        wakeupFormError.value = 'cron 表达式不能为空'
        return
      }
      spec.cron = f.cron.trim()
      if (f.timezone.trim()) {
        spec.timezone = f.timezone.trim()
      }
      break
    }
    default: {
      const types = f.event.split(',').map((x) => x.trim()).filter(Boolean)
      if (types.length === 0) {
        wakeupFormError.value = '事件类型不能为空'
        return
      }
      spec.event_types = types
      if (f.filterJob.trim()) {
        spec.filter_job_id = f.filterJob.trim()
      }
      const st = f.status.split(',').map((x) => x.trim()).filter(Boolean)
      if (st.length > 0) {
        spec.filter_status = st
      }
    }
  }
  wakeupSubmitting.value = true
  wakeupFormError.value = ''
  try {
    await createWakeup(props.id, spec as never)
    wakeupFormOpen.value = false
    await loadWakeups()
  } catch (e) {
    wakeupFormError.value = e instanceof Error ? e.message : String(e)
  } finally {
    wakeupSubmitting.value = false
  }
}

// wakeupHistory 是触发历史：直接筛事件时间线里的 job.wakeup_* 行（倒序，最近的在前）。
const wakeupHistory = computed<JobEvent[]>(() =>
  timelineEvents.value.filter((ev) => ev.type.startsWith('job.wakeup')).slice().reverse(),
)

function wakeupHistoryText(ev: JobEvent): string {
  return eventDetailText(ev) || eventLabel(ev.type)
}

// ── 可靠重试（AUTO-03）─────────────────────────────────────────────
// 这个 job 作为源的重试链：只在还有待发（pending）或已被调度器认领（claimed）的重试时，
// 头部才显示一行提示——回答"它还会不会再跑一次、什么时候"。失败静默忽略：这是辅助信息，
// 不该把详情主流程带崩（与 wakeups 不同，这里不渲染错误块，没有空态 UI）。
const retries = ref<Retry[]>([])

async function loadRetries(): Promise<void> {
  try {
    const resp = await listRetries(props.id)
    retries.value = resp.retries ?? []
  } catch {
    retries.value = []
  }
}

// 重试提示只到分钟（HH:MM），不显示日期：notice 要挤在头部一行内，日期让 meta.v 变宽。
// 与 time.ts 的 fmtDateTime 不同，这里不套服务端时区——只需要一个粗略的相对时刻。
function fmtRetryClock(sec: number): string {
  const d = new Date(sec * 1000)
  const p = (x: number): string => String(x).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}`
}

// retryHint 取"最先要跑的那条"：ListRetries 已按 attempt 升序，多条待发时取第一条，
// 用 (+N) 说明后面还排着几条。没有待发/认领中的重试就整条不显示。
const retryHint = computed<{ text: string; rest: number } | null>(() => {
  const pending = retries.value.filter((r) => r.state === 'pending' || r.state === 'claimed')
  if (pending.length === 0) {
    return null
  }
  const first = pending[0]
  // max_attempts 为 0/缺失 = 这一行没带策略上限，只报"第几次"，别编造分母。
  const head = `重试 ${first.attempt}${first.max_attempts ? `/${first.max_attempts}` : ''}`
  const text = first.next_run_at > 0 ? `${head} · 下次 ${fmtRetryClock(first.next_run_at)}` : head
  return { text, rest: pending.length - 1 }
})

// 投递 status -> 中文标签（pending 区分「重试中」：attempts>0 已失败过）。
function deliveryLabel(d: Delivery): string {
  switch (d.status) {
    case 'delivered':
      return '已送达'
    case 'failed':
      return '失败'
    case 'pending':
      return d.attempts > 0 ? `重试中（第 ${d.attempts} 次）` : '待投递'
    default:
      return d.status
  }
}

// 产物（含文件传输收集到的 collected/*）的下载/预览走同一套 helper + 同一条 URL
// （client 里逐段编码路径）。区别只在失败提示落哪一块：产物块与「文件」块各自行文，
// 谁触发的就写在谁那里 —— 否则点收集行失败时，错误可能落在没渲染的产物块里看不见。
type ArtifactScope = 'artifact' | 'xfer'
const xferFileError = ref('')

function scopeError(scope: ArtifactScope, message: string): void {
  if (scope === 'xfer') {
    xferFileError.value = message
  } else {
    artifactError.value = message
  }
}

async function onDownload(name: string, scope: ArtifactScope = 'artifact'): Promise<void> {
  if (downloadingNames.value.has(name)) {
    return
  }
  const next = new Set(downloadingNames.value)
  next.add(name)
  downloadingNames.value = next
  try {
    await downloadArtifact(props.id, name)
  } catch (e) {
    scopeError(scope, e instanceof Error ? e.message : String(e))
  } finally {
    const after = new Set(downloadingNames.value)
    after.delete(name)
    downloadingNames.value = after
  }
}

// ── 产物 inline 预览（E19a）──────────────────────────────────────────
// 点「预览」→ 取 blob（带鉴权）→ 弹层挂 FilePreview（md/图/json/文本，按 D5）。
// previewingNames 标记取数中（防重复点击）；preview 为当前弹层文件（null=未打开）。
// scope 随弹层记住，弹层里的「下载」沿用同一条错误落点。
const preview = ref<{ name: string; blob: Blob; scope: ArtifactScope } | null>(null)
const previewingNames = ref<Set<string>>(new Set())
const previewError = ref('')

async function onPreview(name: string, scope: ArtifactScope = 'artifact'): Promise<void> {
  if (previewingNames.value.has(name)) {
    return
  }
  scopeError(scope, '')
  const next = new Set(previewingNames.value)
  next.add(name)
  previewingNames.value = next
  try {
    const blob = await fetchArtifactBlob(props.id, name)
    preview.value = { name, blob, scope }
  } catch (e) {
    scopeError(scope, e instanceof Error ? e.message : String(e))
  } finally {
    const after = new Set(previewingNames.value)
    after.delete(name)
    previewingNames.value = after
  }
}

function closePreview(): void {
  preview.value = null
}

// FilePreview 在「过大/二进制」回退时 emit download，或弹层「下载」按钮 → 复用 onDownload。
function onPreviewDownload(): void {
  if (preview.value) {
    void onDownload(preview.value.name, preview.value.scope)
  }
}

// 人类可读文件大小（B/KB/MB），mono 列展示。缺省（后端 omitempty 省略 0 字节）按 0 计。
function fmtSize(bytes: number | undefined): string {
  const n = bytes ?? 0
  if (n < 1024) {
    return `${n} B`
  }
  if (n < 1024 * 1024) {
    return `${(n / 1024).toFixed(1)} KB`
  }
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

// diff 快照(E12)：后端 diff_summary 是 `git diff --stat` 摘要文本（未提交改动，
// tracked vs HEAD/index）。有摘要时给「查看完整 diff」（/v1/jobs/{id}/diff?full=1）。
const diffSummary = computed<string>(() => job.value?.diff_summary ?? '')
const diffError = ref('')
const diffLoading = ref(false)
const diffOpen = ref(false)
const fullDiffText = ref('')

// 完整 diff 走右侧抽屉展示（内容通常较长，内联高度不够）。已加载过则直接开抽屉。
async function onViewDiff(): Promise<void> {
  if (diffLoading.value) {
    return
  }
  if (fullDiffText.value !== '') {
    diffOpen.value = true
    return
  }
  diffLoading.value = true
  diffError.value = ''
  try {
    fullDiffText.value = await fetchDiffText(props.id)
    diffOpen.value = true
  } catch (e) {
    diffError.value = e instanceof Error ? e.message : String(e)
  } finally {
    diffLoading.value = false
  }
}

function closeDiff(): void {
  diffOpen.value = false
}

// 执行来源标注（P4）：source = "" | "worker:<id>" | "peer:<name>"。远端执行时
// 产出由执行机回传（清单+小结果），大产物文件留执行机（worker/peer 侧或共享盘）。
const sourceKind = computed<'local' | 'worker' | 'peer'>(() => {
  const s = job.value?.source ?? ''
  if (s.startsWith('worker:')) {
    return 'worker'
  }
  if (s.startsWith('peer:')) {
    return 'peer'
  }
  return 'local'
})
const isRemoteSource = computed<boolean>(() => sourceKind.value !== 'local')
// 人类可读来源标签，如「在 worker w-gpu 执行」「在 peer docker-1 执行」。
const sourceLabel = computed<string>(() => {
  const s = job.value?.source ?? ''
  if (sourceKind.value === 'worker') {
    return `在 worker ${s.slice('worker:'.length)} 执行`
  }
  if (sourceKind.value === 'peer') {
    return `在 peer ${s.slice('peer:'.length)} 执行`
  }
  return ''
})

// 整个「产出与审计」面板是否有内容（避免空面板）。
const hasOutcomes = computed<boolean>(
  () =>
    resultJsonPretty.value !== '' ||
    artifacts.value.length > 0 ||
    diffSummary.value !== '' ||
    commits.value.length > 0 ||
    // SUP-01 P2：验证步骤是"到底验没验、过没过"的结论，即使 job 没有其他产出也要展示。
    verify.value !== null ||
    // SUP-01 E：用量/成本是这个 job 花了多少的唯一记录，同样独立于其他产出。
    usageText.value !== '' ||
    // XFER-01 X2：传了文件（上传/收集）就是实打实的产出，没别的产出时面板也要出现。
    hasXferDetail.value,
)

// 提交列表（SUP-01 C）：本 job 从 base_sha 到 HEAD 产出的提交，新→旧。
const commits = computed<JobCommit[]>(() => job.value?.commits ?? [])

// 验证步骤（SUP-01 P2）：agent 结束后本机跑的验收命令。块只在后端有结果时出现（无步骤=无块），
// 颜色随 status（passed 绿 / failed、timeout 红 / skipped 灰），命令与耗时直接可读。
// 文案与配色口径与验收台共用（utils/jobOutcome）。
const verify = computed<JobVerify | null>(() => job.value?.verify ?? null)
const verifyCommand = computed<string>(() => (verify.value?.command ?? []).join(' '))
const verifyText = computed<string>(() => verifyLabel(verify.value))
const verifyTone = computed<string>(() => verifyClass(verify.value))

// 用量/成本（SUP-01 E）：agent 自报的 token 与成本。缺项省略（agent 没报 ≠ 0），行尾括号
// 是来源——与后端 `job show` / job.FormatUsage 同一行格式（口径见 utils/jobOutcome）。
const usage = computed<JobUsage | null>(() => job.value?.usage ?? null)
const usageText = computed<string>(() => usageLine(usage.value))

// scrollToVerifyOutput：把读者带到验证输出（stderr 末尾）。
const logTape = ref<InstanceType<typeof LogTape> | null>(null)
function scrollToVerifyOutput(): void {
  logTape.value?.focusStderr()
}

function copyCommit(sha: string): void {
  void navigator.clipboard.writeText(sha).catch(() => {
    // 剪贴板不可用（非安全上下文）时静默：sha 仍可手动选中复制。
  })
}

const copied = ref(false)
async function copyCommand(): Promise<void> {
  const text = renderedCommandLine.value
  if (!text) {
    return
  }
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // 剪贴板不可用（非安全上下文等）时静默：用户仍可手动选择文本复制。
  }
}

watch(
  () => props.id,
  () => {
    void loadCurrentJob()
  },
)

onMounted(() => {
  startClock()
  void loadCurrentJob()
})

onUnmounted(() => {
  if (abortCtrl) {
    abortCtrl.abort()
  }
  if (clockTimer != null) {
    window.clearInterval(clockTimer)
    clockTimer = null
  }
})
</script>

<template>
  <div class="detail">
    <div class="detail-head">
      <RouterLink to="/board" class="back mono">← board</RouterLink>
      <div class="head-right">
        <StatusBadge v-if="job" :status="status" :holder="job.waiting_on_job" />
        <Signal v-if="job" :status="status" :rate="logRate" :duration-sec="durationSec" />
        <RouterLink
          v-if="job && isTerminalView"
          class="rebuild-btn mono"
          :to="`/new?from=${encodeURIComponent(job.id)}`"
          title="用本 job 的参数预填新建表单，提交为一个新 job（env 保留在服务端）"
        >
          快速重建
        </RouterLink>
        <button
          v-if="showTerminalButton"
          class="terminal-open mono"
          type="button"
          :disabled="!canOpenTerminal"
          :title="canOpenTerminal ? '打开终端' : '终端未就绪，稍后重试'"
          @click="openTerminal"
        >
          打开终端
        </button>
        <button
          v-if="showCancel"
          class="cancel mono"
          type="button"
          @click="doCancel"
        >
          取消
        </button>
        <span v-else-if="cancelling && live" class="cancelling mono">取消中…</span>
      </div>
    </div>

    <header v-if="job" class="job-header">
      <h1 v-if="job.title" class="job-name" :title="job.title">{{ job.title }}</h1>
      <h1 v-else class="job-name job-name--id mono" :title="job.id">{{ job.id }}</h1>
      <span v-if="job.title" class="job-subid mono" :title="job.id">{{ shortId(job.id) }}</span>
    </header>

    <p v-if="headError" class="error mono">{{ headError }}</p>

    <div v-if="job" class="meta">
      <div class="meta-item">
        <span class="meta-k mono">id</span><span class="meta-v mono id">{{ job.id }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">project</span><span class="meta-v mono">{{ job.project_key }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">agent</span><span class="meta-v mono">{{ job.agent }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">runner</span>
        <span class="meta-v mono" :class="{ remote: job.runner !== 'local' }">{{ job.runner }}</span>
      </div>
      <div v-if="job.worker_id" class="meta-item">
        <span class="meta-k mono">worker_id</span>
        <span class="meta-v mono remote" :title="job.worker_id">{{ job.worker_id }}</span>
      </div>
      <div v-if="job.channel" class="meta-item">
        <span class="meta-k mono">channel</span><span class="meta-v mono">{{ job.channel }}</span>
      </div>
      <div v-if="job.plan_id" class="meta-item">
        <span class="meta-k mono">plan</span>
        <RouterLink class="meta-v mono" :to="`/plans/${encodeURIComponent(job.plan_id)}`">
          {{ job.plan_id }}
        </RouterLink>
      </div>
      <div v-if="job.source_job_id" class="meta-item">
        <span class="meta-k mono">派生自</span>
        <RouterLink class="meta-v mono" :to="`/jobs/${encodeURIComponent(job.source_job_id)}`">
          {{ job.source_job_id }}
        </RouterLink>
      </div>
      <div v-if="job.client" class="meta-item">
        <span class="meta-k mono">client</span><span class="meta-v mono">{{ job.client }}</span>
      </div>
      <div v-if="job.session_id && isTerminalView" class="meta-item">
        <span class="meta-k mono">session_id</span>
        <span class="meta-v mono" :title="job.session_id">{{ job.session_id }}</span>
        <button class="resume-btn mono" type="button" @click="showResumeForm = !showResumeForm">
          {{ showResumeForm ? '收起' : '继续会话' }}
        </button>
      </div>
      <div v-if="job.session_id && isTerminalView && showResumeForm" class="resume-form">
        <textarea
          v-if="!job.interactive"
          v-model="resumePrompt"
          class="resume-input mono"
          rows="3"
          placeholder="续接指令（必填）"
        ></textarea>
        <div class="resume-actions">
          <button
            class="resume-go mono"
            type="button"
            :disabled="resuming || (!job.interactive && !resumePrompt.trim())"
            @click="doResume"
          >
            {{ resuming ? '续投中…' : (job.interactive ? '续接终端' : '续投新 job') }}
          </button>
          <span v-if="resumeError" class="resume-err mono">{{ resumeError }}</span>
        </div>
      </div>
      <div v-if="job.session_id && sessionJobs.length > 1" class="meta-item">
        <span class="meta-k mono">会话内 job</span>
        <button class="chain-toggle mono" type="button" @click="sessionJobsOpen = !sessionJobsOpen">
          共 {{ sessionJobs.length }} 个{{ sessionJobsOpen ? ' ▾' : ' ▸' }}
        </button>
      </div>
      <ol v-if="sessionJobsOpen && sessionJobs.length > 1" class="chain-list">
        <li v-for="sj in sessionJobs" :key="sj.id" class="chain-item">
          <RouterLink
            v-if="sj.id !== props.id"
            class="chain-link mono"
            :to="`/jobs/${encodeURIComponent(sj.id)}`"
          >{{ sj.id }}</RouterLink>
          <span v-else class="chain-self mono">{{ sj.id }}（当前）</span>
          <StatusBadge :status="sj.status" />
        </li>
      </ol>
      <div class="meta-item">
        <span class="meta-k mono">cwd</span><span class="meta-v mono">{{ job.cwd }}</span>
      </div>
      <!-- 只读 job（bd h-aii-0ql3）：agent 不能写文件；resume 继承该状态，不可升级为可写。 -->
      <div v-if="job.read_only" class="meta-item">
        <span class="meta-k mono">read_only</span>
        <span class="meta-v mono">只读（agent 不能写文件）</span>
      </div>
      <!-- 同 cwd 串行锁（JOB-11）：独占=这个 job 不接受别的独占 job 与它共用工作目录（祖先/
           子目录也算同一棵）；等待时点名持有者，回答"为什么还没跑"。 -->
      <div v-if="job.dir_exclusive || job.waiting_on_job" class="meta-item">
        <span class="meta-k mono">dir</span>
        <span class="meta-v mono">
          {{ job.dir_exclusive ? '独占（同目录串行）' : '共享' }}
          <template v-if="job.waiting_on_job">· 等待目录锁，持有者 {{ job.waiting_on_job }}</template>
        </span>
      </div>
      <!-- 可靠重试（AUTO-03）：这个 job 失败后服务端还排着重试，点出第几次/上限与下次时刻；
           多条待发只报最先那条，(+N) 表示后面还排着几条。没有待发重试则整条不渲染。 -->
      <div v-if="retryHint" class="meta-item">
        <span class="meta-k mono">retry</span>
        <span class="meta-v mono"
          >{{ retryHint.text }}<template v-if="retryHint.rest > 0"> (+{{ retryHint.rest }})</template></span
        >
      </div>
      <!-- 人工验收（GATE-01 S3）：是否要求人验收 + 已经做出的裁决（谁/何时/为什么）。
           needs_review 时 reviewed_* 为空，正说明还没人裁。 -->
      <div v-if="job.require_review" class="meta-item">
        <span class="meta-k mono">require_review</span>
        <span class="meta-v mono">需要人验收</span>
      </div>
      <div v-if="job.reviewed_by" class="meta-item">
        <span class="meta-k mono">reviewed_by</span>
        <span class="meta-v mono"
          >{{ job.reviewed_by }}<template v-if="job.reviewed_at"> · {{ fmtTime(job.reviewed_at) }}</template></span
        >
      </div>
      <div v-if="job.review_note" class="meta-item">
        <span class="meta-k mono">review_note</span>
        <span class="meta-v mono">{{ job.review_note }}</span>
      </div>
      <!-- WT-01：受管 worktree。commits_ahead>0 = 分支上已有提交、还没合回基线分支，
           这就是"job 干完了但代码还没合"的可视信号。 -->
      <div v-if="job.worktree_path" class="meta-item">
        <span class="meta-k mono">worktree</span>
        <span class="meta-v mono" :title="job.worktree_path">{{ job.worktree_path }}</span>
      </div>
      <div v-if="job.worktree_branch" class="meta-item">
        <span class="meta-k mono">wt_branch</span>
        <span class="meta-v mono">{{ job.worktree_branch }}<template v-if="job.commits_ahead"> · {{ job.commits_ahead }} commit(s) ahead</template></span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">started</span><span class="meta-v mono">{{ fmtTime(job.started_at) }}</span>
      </div>
      <!-- RECOV-01：worker 断线后 job 被 held 在 recovering；显示进入该状态的时刻（0/缺省不渲染） -->
      <div v-if="job.recovering_since" class="meta-item">
        <span class="meta-k mono">recovering_since</span>
        <span class="meta-v mono">{{ fmtTime(job.recovering_since) }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">duration</span><span class="meta-v mono">{{ durationText }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">exit_code</span>
        <span class="meta-v mono" :class="{ bad: job.exit_code !== 0 && isTerminalView }">
          {{ isTerminalView ? job.exit_code : '—' }}
        </span>
      </div>
    </div>

    <p v-if="job?.error" class="error mono">{{ job.error }}</p>

    <p v-if="streamError" class="stream-err mono">
      连接断开：{{ streamError }}
      <button class="reconnect" type="button" @click="manualReconnect">点击重连</button>
    </p>

    <!-- 验收面板（REV-01）：汇报 / 提交 / Diff / 验证 / 用量五页签 + 底部 Accept/Reject
         （已验收则回显 reviewed_by/at/note）。裁决与跳转经事件回传本页。 -->
    <ReviewPanel
      v-if="job && showReviewPanel"
      :job="job"
      @updated="applyStatus"
      @resumed="onReviewResumed"
    />

    <!-- 渲染命令：独立于「产出与审计」，running 态只要后端给出 rendered_command 即展示。 -->
    <section v-if="renderedCommand" class="rendered-command">
      <div class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">渲染命令</span>
          <span class="outcome-actions">
            <button
              v-if="canViewCommandMarkdown"
              class="copy-btn mono"
              type="button"
              @click="commandMarkdownMode = !commandMarkdownMode"
            >
              {{ commandMarkdownMode ? '查看原文' : 'Markdown查看' }}
            </button>
            <button class="copy-btn mono" type="button" @click="copyCommand">
              {{ copied ? '已复制' : '复制' }}
            </button>
          </span>
        </div>
        <div
          v-if="canViewCommandMarkdown && commandMarkdownMode"
          class="outcome-pre outcome-pre--cmd outcome-md"
          v-html="renderedCommandMarkdown"
        />
        <pre v-else class="outcome-pre outcome-pre--cmd mono"><span class="cmd-bin">{{ renderedCommand.command }}</span><template
          v-for="(a, i) in renderedCommand.args ?? []"
          :key="i"
        > {{ a }}</template></pre>
        <details
          v-if="(renderedCommand.env_keys ?? []).length > 0"
          class="env-fold"
        >
          <summary class="mono">
            env keys（{{ (renderedCommand.env_keys ?? []).length }}，仅键名）
          </summary>
          <ul class="env-list mono">
            <li v-for="k in renderedCommand.env_keys ?? []" :key="k">{{ k }}</li>
          </ul>
        </details>
      </div>
    </section>

    <!-- 运行中交互区：待应答卡片（排队作答）+ 已应答折叠 -->
    <section
      v-if="pendingInteractions.length > 0 || answeredInteractions.length > 0"
      class="interactions"
    >
      <h2 class="interactions-title mono">
        <span class="warn">⚠</span> 待应答交互
        <span v-if="pendingInteractions.length > 0" class="count mono"
          >{{ pendingInteractions.length }}</span
        >
      </h2>

      <div v-for="it in pendingInteractions" :key="it.id" class="icard-wrap">
        <InteractionCard
          :interaction="it"
          :submitting="submittingIds.has(it.id)"
          @answer="(v) => onAnswer(it.id, v)"
          @punt="onPunt(it.id)"
        />
        <p v-if="interactionErrors.get(it.id)" class="icard-err mono">
          操作失败：{{ interactionErrors.get(it.id) }}
        </p>
      </div>

      <details v-if="answeredInteractions.length > 0" class="answered-fold">
        <summary class="mono">
          已应答 {{ answeredInteractions.length }} 条
        </summary>
        <InteractionCard
          v-for="it in answeredInteractions"
          :key="it.id"
          :interaction="it"
        />
      </details>
    </section>

    <!-- 事件时间线（E13）：append-only 生命周期事件，每事件一行（图标 + 标签 +
         关键 detail + 相对时间）。仿 interactions 渲染风格，仅在有事件时展示。 -->
    <section v-if="timelineEvents.length > 0" class="timeline">
      <h2 class="timeline-title mono">事件时间线</h2>
      <ul class="timeline-list">
        <li
          v-for="ev in timelineEvents"
          :key="ev.seq"
          class="timeline-row"
          :class="'ev-' + ev.type.replace('.', '-')"
        >
          <span class="ev-icon mono">{{ eventIcon(ev.type) }}</span>
          <span class="ev-label mono">{{ eventLabel(ev.type) }}</span>
          <span v-if="eventDetailText(ev)" class="ev-detail mono" :title="eventDetailText(ev)">
            {{ eventDetailText(ev) }}
          </span>
          <span class="ev-time mono">{{ fmtTime(ev.at) }}</span>
        </li>
      </ul>
    </section>

    <!-- webhook 投递（E14）：只读，每条投递一行（状态徽标 + 目标 + 关键信息）。
         无通知配置时 deliveries 为空，整节不展示。 -->
    <section v-if="deliveries.length > 0" class="deliveries">
      <h2 class="deliveries-title mono">通知投递</h2>
      <ul class="deliveries-list">
        <li
          v-for="d in deliveries"
          :key="d.id"
          class="delivery-row"
        >
          <span class="dl-badge mono" :class="'dl-' + d.status">{{ deliveryLabel(d) }}</span>
          <span class="dl-target mono" :title="d.target">{{ d.target }}</span>
          <span
            v-if="d.last_error"
            class="dl-error mono"
            :title="d.last_error"
          >{{ d.last_error }}</span>
          <span
            v-if="d.status === 'pending' && d.next_retry_at > 0 && d.attempts > 0"
            class="dl-time mono"
          >下次 {{ fmtTime(d.next_retry_at) }}</span>
        </li>
      </ul>
    </section>

    <!-- 终端会话：pty relay 元数据与录制下载入口。 -->
    <section v-if="ptySessions.length > 0" class="pty-sessions">
      <h2 class="pty-sessions-title mono">终端会话</h2>
      <ul class="pty-sessions-list">
        <li
          v-for="s in ptySessions"
          :key="s.pty_session_id"
          class="pty-session-row"
        >
          <span class="pty-size mono">{{ s.cols }}×{{ s.rows }}</span>
          <span class="pty-bytes mono">{{ ptySessionBytes(s) }}</span>
          <span class="pty-duration mono">{{ ptySessionDuration(s) }}</span>
          <span v-if="s.encrypted" class="pty-encrypted mono">加密</span>
          <span class="pty-state mono">{{ s.state }}</span>
          <button
            v-if="s.has_recording"
            class="pty-download mono"
            type="button"
            :disabled="downloadingRecordingIds.has(s.pty_session_id)"
            @click="onDownloadRecording(s.pty_session_id)"
          >
            {{ downloadingRecordingIds.has(s.pty_session_id) ? '下载中…' : '下载录制' }}
          </button>
        </li>
      </ul>
      <p v-if="ptySessionError" class="artifact-err mono">{{ ptySessionError }}</p>
    </section>

    <!-- 唤醒（JOB-09）：登记在 job 上的事件订阅/定时器 —— 条件到达时 gofer 自动续投
         这个 job。列表 + 开关 + 新建表单 + 触发历史（来自 job.wakeup_* 事件）。 -->
    <section v-if="wakeups.length > 0 || wakeupFormOpen" class="wakeups">
      <h2 class="wakeups-title mono">
        唤醒
        <span v-if="wakeupSummary" class="wakeups-sum mono">{{ wakeupSummary }}</span>
        <button class="wakeups-add mono" type="button" @click="wakeupFormOpen = !wakeupFormOpen">
          {{ wakeupFormOpen ? '收起' : '新建' }}
        </button>
      </h2>

      <ul v-if="wakeups.length > 0" class="wakeups-list">
        <li v-for="w in wakeups" :key="w.id" class="wakeup-row" :class="{ 'wakeup-row--off': !w.enabled }">
          <span class="wakeup-kind mono">{{ w.kind }}</span>
          <span class="wakeup-target mono">{{ wakeupTarget(w) }}</span>
          <span class="wakeup-trigger mono" :title="wakeupTrigger(w)">{{ wakeupTrigger(w) }}</span>
          <span class="wakeup-instruction mono" :title="w.instruction || ''">{{ w.instruction || '（无指令）' }}</span>
          <span class="wakeup-count mono" :title="`coalesced ${w.coalesced_count}`">
            触发 {{ w.fired_count }}<template v-if="w.coalesced_count > 0"> · 合并 {{ w.coalesced_count }}</template>
          </span>
          <RouterLink
            v-if="w.continuation_job_id"
            class="wakeup-cont mono"
            :to="`/jobs/${w.continuation_job_id}`"
          >最近续投 {{ shortId(w.continuation_job_id) }}</RouterLink>
          <button
            class="wakeup-btn mono"
            type="button"
            :disabled="wakeupBusyOn(w.id)"
            @click="onToggleWakeup(w)"
          >{{ w.enabled ? '停用' : '启用' }}</button>
          <button
            class="wakeup-btn mono"
            type="button"
            :disabled="wakeupBusyOn(w.id)"
            @click="onDeleteWakeup(w)"
          >删除</button>
        </li>
      </ul>

      <form v-if="wakeupFormOpen" class="wakeup-form" @submit.prevent="onCreateWakeup">
        <label class="wakeup-field mono">
          类型
          <select v-model="wakeupForm.kind" class="mono">
            <option value="at">at（到点一次）</option>
            <option value="every">every（每隔一段）</option>
            <option value="cron">cron（按表达式）</option>
            <option value="event">event（订阅事件）</option>
          </select>
        </label>
        <label v-if="wakeupForm.kind === 'at'" class="wakeup-field mono">
          多久之后
          <input v-model="wakeupForm.after" class="mono" placeholder="10m" />
        </label>
        <label v-if="wakeupForm.kind === 'every'" class="wakeup-field mono">
          间隔
          <input v-model="wakeupForm.every" class="mono" placeholder="1h" />
        </label>
        <template v-if="wakeupForm.kind === 'cron'">
          <label class="wakeup-field mono">
            表达式
            <input v-model="wakeupForm.cron" class="mono" placeholder="0 9 * * 1-5" />
          </label>
          <label class="wakeup-field mono">
            时区
            <input v-model="wakeupForm.timezone" class="mono" placeholder="Asia/Shanghai（留空=服务器本地）" />
          </label>
        </template>
        <template v-if="wakeupForm.kind === 'event'">
          <label class="wakeup-field mono">
            事件
            <input v-model="wakeupForm.event" class="mono" :placeholder="WAKEUP_EVENT_TYPES.join(',')" />
          </label>
          <label class="wakeup-field mono">
            监听哪个 job
            <input v-model="wakeupForm.filterJob" class="mono" placeholder="留空 = 本 job" />
          </label>
          <label class="wakeup-field mono">
            状态过滤
            <input v-model="wakeupForm.status" class="mono" placeholder="done,failed（仅 job.terminal）" />
          </label>
        </template>
        <label class="wakeup-field mono">
          模式
          <select v-model="wakeupForm.mode" class="mono">
            <option value="">默认（at/event=once，every/cron=continuous）</option>
            <option value="once">once</option>
            <option value="continuous">continuous</option>
          </select>
        </label>
        <label class="wakeup-field wakeup-field--wide mono">
          指令（续投时的提示词）
          <input v-model="wakeupForm.instruction" class="mono" placeholder="检查 CI 结果并汇报" />
        </label>
        <button class="wakeup-btn wakeup-btn--primary mono" type="submit" :disabled="wakeupSubmitting">
          {{ wakeupSubmitting ? '提交中…' : '创建' }}
        </button>
        <p v-if="wakeupFormError" class="artifact-err mono">{{ wakeupFormError }}</p>
      </form>

      <div v-if="wakeupHistory.length > 0" class="wakeup-history">
        <p class="diff-note mono">触发历史（{{ wakeupHistory.length }}）</p>
        <ul class="timeline-list">
          <li v-for="ev in wakeupHistory" :key="ev.seq" class="timeline-row">
            <span class="ev-icon mono">{{ eventIcon(ev.type) }}</span>
            <span class="ev-label mono">{{ eventLabel(ev.type) }}</span>
            <span class="ev-detail mono" :title="wakeupHistoryText(ev)">{{ wakeupHistoryText(ev) }}</span>
            <span class="ev-time mono">{{ fmtTime(ev.at) }}</span>
          </li>
        </ul>
      </div>

      <p v-if="wakeupError" class="artifact-err mono">{{ wakeupError }}</p>
    </section>

    <!-- 产出与审计：结构化结果(E6) + 产物 + diff。仅在有内容时展示。 -->
    <section v-if="hasOutcomes" class="outcomes">
      <h2 class="outcomes-title mono">
        产出与审计
        <span v-if="isRemoteSource" class="source-badge mono" :class="'source-' + sourceKind">{{ sourceLabel }}</span>
      </h2>

      <!-- 结构化结果：<result_dir>/result.json pretty-print。 -->
      <div v-if="resultJsonPretty" class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">结构化结果</span>
        </div>
        <pre class="outcome-pre result-json mono">{{ resultJsonPretty }}</pre>
      </div>

      <!-- 产物清单(E1)：name(title) + size(mono) + 下载（带鉴权 fetch+blob）。
           远端执行(P4)：仅回清单元数据，文件留执行机 → 不提供下载，标注来源。 -->
      <div v-if="artifacts.length > 0" class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">产物文件（{{ artifacts.length }}）</span>
        </div>
        <p v-if="isRemoteSource" class="diff-note mono">
          清单来自{{ sourceLabel }}；文件留在执行机（worker / 共享盘 / peer 侧），本机不提供下载
        </p>
        <ul class="artifact-list">
          <li v-for="a in artifacts" :key="a.name" class="artifact-row">
            <span class="artifact-name" :title="a.name">{{ a.name }}</span>
            <span class="artifact-size mono">{{ fmtSize(a.size) }}</span>
            <template v-if="!isRemoteSource">
              <button
                class="artifact-dl mono"
                type="button"
                :disabled="previewingNames.has(a.name)"
                @click="onPreview(a.name)"
              >
                {{ previewingNames.has(a.name) ? '打开中…' : '预览' }}
              </button>
              <button
                class="artifact-dl mono"
                type="button"
                :disabled="downloadingNames.has(a.name)"
                @click="onDownload(a.name)"
              >
                {{ downloadingNames.has(a.name) ? '下载中…' : '下载' }}
              </button>
            </template>
            <span v-else class="artifact-remote mono">留在执行机</span>
          </li>
        </ul>
        <p v-if="artifactError" class="artifact-err mono">{{ artifactError }}</p>
        <p v-if="previewError" class="artifact-err mono">{{ previewError }}</p>
      </div>

      <!-- 文件传输（XFER-01 X2）：--upload / --collect 的落地结果。后端只在真用过时发
           xfer；三段全空也不摆空盒子。收集到的文件本体就在产物的 collected/ 下（且即使
           远端执行也已在 hub 上），故点击直接复用产物预览/下载通道。 -->
      <div v-if="xfer && hasXferDetail" class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">文件</span>
        </div>

        <!-- 上传：执行机在 agent 起跑前放进 dest（cwd 相对）的文件；失败的会说明原因
             （此时 agent 没跑、job 已 failed）。 -->
        <div v-if="xferUploads.length > 0" class="xfer-part">
          <p class="diff-note mono">上传（{{ xferUploads.length }}）· 起跑前放进执行机</p>
          <ul class="artifact-list">
            <li v-for="(u, i) in xferUploads" :key="`up-${i}-${u.dest}`" class="artifact-row">
              <span class="artifact-name mono" :title="u.dest">{{ u.dest }}</span>
              <span class="artifact-size mono">{{ fmtSize(u.size) }}</span>
              <span class="xfer-chip mono" :class="u.ok ? 'xfer-chip--ok' : 'xfer-chip--bad'">
                {{ u.ok ? '已就位' : '失败' }}
              </span>
              <span v-if="!u.ok && u.error" class="xfer-note mono" :title="u.error">{{ u.error }}</span>
            </li>
          </ul>
        </div>

        <!-- 收集：job 结束后按 cwd 匹配到的文件，本体落在本 job 产物的 collected/ 下。 -->
        <div v-if="xferCollected.length > 0" class="xfer-part">
          <p class="diff-note mono">收集（{{ xferCollected.length }}）· 点名字预览</p>
          <ul class="artifact-list">
            <li v-for="c in xferCollected" :key="`col-${c.name}`" class="artifact-row">
              <button
                class="artifact-name xfer-name-btn mono"
                type="button"
                :title="`预览 ${collectedName(c.name)}`"
                :disabled="previewingNames.has(collectedName(c.name))"
                @click="onPreview(collectedName(c.name), 'xfer')"
              >
                {{ c.name }}
              </button>
              <span class="artifact-size mono">{{ fmtSize(c.size) }}</span>
              <button
                class="artifact-dl mono"
                type="button"
                :disabled="downloadingNames.has(collectedName(c.name))"
                @click="onDownload(collectedName(c.name), 'xfer')"
              >
                {{ downloadingNames.has(collectedName(c.name)) ? '下载中…' : '下载' }}
              </button>
            </li>
          </ul>
        </div>

        <!-- 跳过：匹配到但没传回的文件（超单文件/总量上限等），连同原因列出。 -->
        <div v-if="xferSkipped.length > 0" class="xfer-part">
          <p class="diff-note mono">跳过（{{ xferSkipped.length }}）· 匹配到但没传回</p>
          <ul class="artifact-list">
            <li v-for="(sk, i) in xferSkipped" :key="`skip-${i}-${sk.name || sk.pattern || ''}`" class="artifact-row">
              <span class="artifact-name mono" :title="sk.name || sk.pattern || ''">{{ sk.name || sk.pattern || '（未命名）' }}</span>
              <span v-if="sk.pattern && sk.name" class="artifact-size mono" :title="`pattern ${sk.pattern}`">{{ sk.pattern }}</span>
              <span class="xfer-note mono" :title="sk.reason">{{ sk.reason }}</span>
            </li>
          </ul>
        </div>

        <p v-if="xferFileError" class="artifact-err mono">{{ xferFileError }}</p>
      </div>

      <!-- 提交列表（SUP-01 C）：本 job 产出的提交（base..HEAD，新→旧）。worktree job
           的交付物就在这些提交里；点 sha 复制，便于合回主干或核对。 -->
      <div v-if="commits.length > 0" class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">提交（{{ commits.length }}）</span>
          <span v-if="job?.base_sha" class="diff-note mono" :title="job.base_sha">基线 {{ shortSha(job.base_sha) }}</span>
        </div>
        <ul class="artifact-list">
          <li v-for="c in commits" :key="c.sha" class="artifact-row">
            <button class="artifact-dl mono" type="button" :title="`复制 ${c.sha}`" @click="copyCommit(c.sha)">
              {{ c.sha }}
            </button>
            <span class="artifact-name" :title="c.subject">{{ c.subject }}</span>
          </li>
        </ul>
      </div>

      <!-- 验证步骤（SUP-01 P2）：agent 正常结束后在执行机同 cwd/env 跑的验收命令。
           failed/timeout 就是该 job 失败的原因；点「查看输出」跳到 stderr 末尾看命令的原始输出。 -->
      <div v-if="verify" class="outcome-block" :class="verifyTone">
        <div class="outcome-head">
          <span class="outcome-k mono">验证</span>
          <span class="verify-status mono" :class="verifyTone">{{ verifyText }}</span>
          <button class="copy-btn mono" type="button" @click="scrollToVerifyOutput">
            查看输出
          </button>
        </div>
        <pre class="outcome-pre verify-cmd mono">{{ verifyCommand }}</pre>
      </div>

      <!-- 用量/成本（SUP-01 E）：agent 自报的 token/成本结算。缺项不显示（没报 ≠ 0），
           行尾括号是来源；远端 job 的数字由执行机采集后随 Outcome 回传。 -->
      <div v-if="usageText" class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">用量</span>
        </div>
        <pre class="outcome-pre verify-cmd mono">{{ usageText }}</pre>
      </div>

      <!-- diff 快照(E12)：git diff --stat 摘要（未提交改动）+ 查看完整 diff。 -->
      <div v-if="diffSummary" class="outcome-block">
        <div class="outcome-head">
          <span class="outcome-k mono">改了什么</span>
          <button
            class="copy-btn mono"
            type="button"
            :disabled="diffLoading"
            @click="onViewDiff"
          >
            {{ diffLoading ? '加载中…' : '查看完整 diff' }}
          </button>
        </div>
        <p class="diff-note mono">未提交改动（uncommitted changes，tracked vs HEAD）</p>
        <pre class="outcome-pre diff-stat mono">{{ diffSummary }}</pre>
        <p v-if="diffError" class="artifact-err mono">{{ diffError }}</p>
      </div>
    </section>

    <LogTape
      ref="logTape"
      :stdout="stdout"
      :stderr="stderr"
      :live="live"
      :mode="isTerminalView ? 'paged' : 'live'"
      :stdout-total="logPages.stdout.total"
      :stderr-total="logPages.stderr.total"
      :stdout-can-load-earlier="stdoutCanLoadEarlier"
      :stderr-can-load-earlier="stderrCanLoadEarlier"
      :stdout-loading="logPages.stdout.loading"
      :stderr-loading="logPages.stderr.loading"
      @load-earlier="onLoadEarlier"
      @load-all="onLoadAll"
    />

    <!-- 产物预览弹层（E19a）：点击产物「预览」打开；md/图/json/文本经 FilePreview
         渲染（md 走 marked + DOMPurify sanitize）。点遮罩或「关闭」收起。 -->
    <div
      v-if="preview"
      class="preview-overlay"
      @click.self="closePreview"
    >
      <div class="preview-modal">
        <div class="preview-head">
          <span class="preview-name mono" :title="preview.name">{{ preview.name }}</span>
          <div class="preview-actions">
            <button class="copy-btn mono" type="button" @click="onPreviewDownload">下载</button>
            <button class="copy-btn mono" type="button" @click="closePreview">关闭</button>
          </div>
        </div>
        <div class="preview-body">
          <FilePreview
            :name="preview.name"
            :blob="preview.blob"
            @download="onPreviewDownload"
          />
        </div>
      </div>
    </div>

    <!-- 完整 diff 右侧抽屉：点遮罩或「关闭」收起。diff 内容通常较长，抽屉给足高度纵向滚动。 -->
    <div
      v-if="diffOpen"
      class="drawer-overlay"
      @click.self="closeDiff"
    >
      <div class="drawer-panel" role="dialog" aria-label="完整 diff">
        <div class="drawer-head">
          <span class="drawer-title mono">完整 diff · 未提交改动</span>
          <button class="copy-btn mono" type="button" @click="closeDiff">关闭</button>
        </div>
        <div class="drawer-body">
          <pre class="drawer-diff mono">{{ fullDiffText }}</pre>
        </div>
      </div>
    </div>

    <!-- 浏览器终端右侧抽屉：与日志 SSE 并存，pty 输出不写 stdout.log。 -->
    <div
      v-if="terminalOpen"
      class="drawer-overlay"
      @click.self="closeTerminal"
    >
      <div
        class="drawer-panel terminal-drawer"
        role="dialog"
        aria-label="终端"
      >
        <div class="drawer-head terminal-drawer-head">
          <span class="drawer-title mono">终端 · {{ shortId(props.id) }}</span>
          <div class="term-mode-toggle mono" role="group" aria-label="终端模式">
            <button
              type="button"
              :class="{ active: termMode === 'write' }"
              @click="termMode = 'write'"
            >
              写入
            </button>
            <button
              type="button"
              :class="{ active: termMode === 'read' }"
              @click="termMode = 'read'"
            >
              只读
            </button>
          </div>
          <button class="copy-btn mono" type="button" @click="closeTerminal">关闭</button>
        </div>
        <div class="drawer-body terminal-drawer-body">
          <AttachTerminal
            :key="termMode"
            :job-id="props.id"
            :mode="termMode"
            @exit="onTermExit"
            @closed="closeTerminal"
            @error="onTermError"
          />
          <p v-if="terminalExitText" class="term-exit mono">{{ terminalExitText }}</p>
          <p v-if="termError" class="term-error mono">{{ termError }}</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.detail {
  max-width: 1200px;
  margin: 0 auto;
}
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.back {
  font-size: 13px;
  color: var(--queue);
}
.back:hover {
  color: var(--phosphor);
}
.head-right {
  display: flex;
  align-items: center;
  gap: 16px;
}
.terminal-open,
.rebuild-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
}
.terminal-open:hover:not(:disabled),
.rebuild-btn:hover {
  background: var(--phosphor);
  color: var(--ink);
}
.terminal-open:disabled {
  color: var(--queue);
  border-color: var(--line);
  cursor: default;
  opacity: 0.65;
}
.cancel {
  background: transparent;
  color: var(--fail);
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
}
.cancel:hover {
  background: var(--fail);
  color: var(--ink);
}
.cancelling {
  color: var(--run);
  font-size: 12px;
}

/* Prominent job name (the human title) with the short id as secondary; falls
   back to the full id as the name when the job has no title. */
.job-header {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin: 0 0 12px;
  min-width: 0;
}
.job-name {
  font-size: 18px;
  font-weight: 600;
  color: var(--paper);
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.job-name--id {
  font-size: 15px;
  color: var(--phosphor);
}
.job-subid {
  font-size: 12px;
  color: var(--phosphor);
  flex: none;
}

.meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px 24px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 16px;
  margin-bottom: 14px;
}
.meta-item {
  display: flex;
  gap: 12px;
  font-size: 12px;
  min-width: 0;
}
.meta-k {
  color: var(--queue);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  width: 78px;
  flex: none;
}
.meta-v {
  color: var(--paper);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta-v.id {
  color: var(--phosphor);
}
/* 远端执行（peer-http / worker）：runner / worker_id 用 phosphor 凸显「在哪执行」。 */
.meta-v.remote {
  color: var(--phosphor);
}
.meta-v.bad {
  color: var(--fail);
}
.resume-btn,
.chain-toggle {
  flex: none;
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 2px 9px;
  font-size: 11px;
}
.resume-btn:hover,
.chain-toggle:hover {
  background: var(--phosphor);
  color: var(--ink);
}
/* 验收面板（REV-01）自带样式；这里只留 resume 系列输入框给 resume 表单复用。 */
.resume-form {
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-left: 90px;
}
.resume-input {
  min-height: 70px;
  resize: vertical;
  background: var(--term-bg);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.45;
  outline: none;
}
.resume-input:focus {
  border-color: var(--phosphor);
}
.resume-input::placeholder {
  color: var(--queue);
}
.resume-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.resume-go {
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
  font-weight: 600;
}
.resume-go:hover:not(:disabled) {
  opacity: 0.9;
}
.resume-go:disabled {
  background: transparent;
  color: var(--queue);
  border-color: var(--line);
  cursor: default;
  opacity: 0.65;
}
.resume-err {
  color: var(--fail);
  font-size: 11px;
}
.chain-list {
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding: 0 0 0 90px;
  list-style: none;
}
.chain-item {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  font-size: 12px;
}
.chain-link,
.chain-self {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chain-link {
  color: var(--phosphor);
}
.chain-self {
  color: var(--queue);
  opacity: 0.75;
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
.stream-err {
  color: var(--run);
  font-size: 12px;
  margin: 0 0 12px;
}
.reconnect {
  margin-left: 10px;
  background: var(--run);
  color: var(--ink);
  border: none;
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
  font-weight: 600;
}
.rendered-command {
  margin: 0 0 14px;
}

.interactions {
  margin: 0 0 14px;
}
.interactions-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  letter-spacing: 0.06em;
  color: var(--phosphor);
  text-transform: uppercase;
  margin: 0 0 10px;
}
.interactions-title .warn {
  color: var(--phosphor);
}
.interactions-title .count {
  background: var(--phosphor);
  color: var(--ink);
  border-radius: var(--radius);
  padding: 0 6px;
  font-size: 11px;
  font-weight: 600;
}
.icard-wrap {
  margin-bottom: 4px;
}
.icard-err {
  color: var(--fail);
  font-size: 11px;
  margin: -4px 0 10px;
}
.answered-fold {
  margin-top: 6px;
}
.answered-fold > summary {
  cursor: pointer;
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
  padding: 4px 0;
  list-style: revert;
}
.answered-fold > summary:hover {
  color: var(--phosphor);
}

/* 事件时间线面板（E13）：每事件一行，等宽、紧凑，仿 interactions 风格。 */
.timeline {
  margin: 0 0 14px;
}
.timeline-title {
  font-size: 12px;
  letter-spacing: 0.06em;
  color: var(--phosphor);
  text-transform: uppercase;
  margin: 0 0 10px;
}
.timeline-list {
  margin: 0;
  padding: 0;
  list-style: none;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.timeline-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 5px 12px;
  font-size: 12px;
  border-bottom: 1px solid var(--line);
}
.timeline-row:last-child {
  border-bottom: none;
}
.ev-icon {
  flex: 0 0 auto;
  width: 14px;
  text-align: center;
  color: var(--queue);
}
.ev-label {
  flex: 0 0 auto;
  color: var(--paper);
}
.ev-detail {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ev-time {
  flex: 0 0 auto;
  color: var(--queue);
  font-size: 11px;
}
/* 关键转换点用 phosphor 凸显图标。 */
.ev-job-running .ev-icon,
.ev-job-terminal .ev-icon {
  color: var(--phosphor);
}
.ev-job-cancelled .ev-icon {
  color: var(--fail);
}

/* webhook 投递面板（E14）：状态徽标 + 目标 + 错误/下次重试。 */
.deliveries {
  margin: 0 0 14px;
}
.deliveries-title {
  font-size: 12px;
  letter-spacing: 0.06em;
  color: var(--phosphor);
  text-transform: uppercase;
  margin: 0 0 10px;
}
.deliveries-list {
  margin: 0;
  padding: 0;
  list-style: none;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.delivery-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 5px 12px;
  font-size: 12px;
  border-bottom: 1px solid var(--line);
}
.delivery-row:last-child {
  border-bottom: none;
}
.dl-badge {
  flex: 0 0 auto;
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 3px;
  border: 1px solid var(--queue);
  color: var(--queue);
}
.dl-badge.dl-delivered {
  color: var(--done);
  border-color: var(--done);
}
.dl-badge.dl-failed {
  color: var(--fail);
  border-color: var(--fail);
}
.dl-badge.dl-pending {
  color: var(--run);
  border-color: var(--run);
}
.dl-target {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dl-error {
  flex: 0 1 auto;
  min-width: 0;
  color: var(--fail);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dl-time {
  flex: 0 0 auto;
  color: var(--queue);
  font-size: 11px;
}

/* pty_sessions 元数据：只读列表，与 timeline / deliveries 保持相同密度。 */
.pty-sessions {
  margin: 0 0 14px;
}
.pty-sessions-title {
  font-size: 12px;
  letter-spacing: 0.06em;
  color: var(--phosphor);
  text-transform: uppercase;
  margin: 0 0 10px;
}
.pty-sessions-list {
  margin: 0;
  padding: 0;
  list-style: none;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.pty-session-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 6px 12px;
  font-size: 12px;
  border-bottom: 1px solid var(--line);
}
.pty-session-row:last-child {
  border-bottom: none;
}
.pty-size {
  flex: 0 0 auto;
  color: var(--phosphor);
}
.pty-bytes {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pty-duration,
.pty-state {
  flex: 0 0 auto;
  color: var(--queue);
}
.pty-encrypted {
  flex: 0 0 auto;
  color: var(--run);
  border: 1px solid var(--run);
  border-radius: 3px;
  padding: 1px 6px;
  font-size: 11px;
}
.pty-download {
  flex: 0 0 auto;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 10px;
  font-size: 11px;
}
.pty-download:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.pty-download:disabled {
  cursor: default;
  opacity: 0.5;
}

/* 产出与审计面板：渲染命令 + 结构化结果。 */
.outcomes {
  margin: 0 0 14px;
}
.outcomes-title {
  font-size: 12px;
  letter-spacing: 0.06em;
  color: var(--phosphor);
  text-transform: uppercase;
  margin: 0 0 10px;
}
/* 执行来源徽标（P4）：远端 worker/peer 执行时标注。 */
.source-badge {
  display: inline-block;
  margin-left: 8px;
  padding: 1px 7px;
  font-size: 10px;
  letter-spacing: 0.04em;
  text-transform: none;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
}
.source-badge.source-worker {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.source-badge.source-peer {
  color: var(--queue);
  border-color: var(--queue);
}
.artifact-remote {
  color: var(--queue);
  font-size: 11px;
  opacity: 0.75;
}
.outcome-block {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
  margin-bottom: 10px;
}
.outcome-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 8px;
}
.outcome-k {
  color: var(--queue);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-size: 11px;
}
.copy-btn {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 10px;
  font-size: 11px;
}
.copy-btn:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.outcome-actions {
  display: inline-flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
}
.outcome-pre {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--paper);
  white-space: pre-wrap;
  word-break: break-word;
  overflow-x: auto;
}
.outcome-pre--cmd {
  max-height: 360px;
  overflow: auto;
}
.outcome-pre .cmd-bin {
  color: var(--phosphor);
  font-weight: 600;
}
/* 验证步骤（SUP-01 P2）：块边框随结论变色，右上角状态文字同色——一眼看出"验收过没过"。 */
.outcome-block.verify--ok {
  border-color: var(--done);
}
.outcome-block.verify--bad {
  border-color: var(--fail);
}
.verify-status {
  font-size: 12px;
}
.verify-status.verify--ok {
  color: var(--done);
}
.verify-status.verify--bad {
  color: var(--fail);
}
.verify-status.verify--skip {
  color: var(--queue);
  opacity: 0.85;
}
.verify-cmd {
  max-height: 160px;
  overflow: auto;
  color: var(--phosphor);
}
.outcome-md {
  white-space: normal;
}
.outcome-md :deep(h1),
.outcome-md :deep(h2),
.outcome-md :deep(h3),
.outcome-md :deep(h4) {
  color: var(--paper);
  line-height: 1.3;
  margin: 1em 0 0.5em;
}
.outcome-md :deep(p),
.outcome-md :deep(ul),
.outcome-md :deep(ol) {
  margin: 0.5em 0;
}
.outcome-md :deep(a) {
  color: var(--phosphor);
}
/* inline code：加发丝边框 + 暖色前景，确保在 --panel 容器上（两种主题）都清晰可辨。 */
.outcome-md :deep(code) {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: 3px;
  color: var(--run);
  font-family: var(--font-mono, monospace);
  font-size: 0.92em;
  padding: 0 5px;
}
.outcome-md :deep(pre) {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: auto;
  padding: 10px 12px;
}
.outcome-md :deep(pre code) {
  background: none;
  border: 0;
  color: inherit;
  padding: 0;
}
.outcome-pre.result-json {
  max-height: 360px;
  overflow: auto;
  white-space: pre;
}
/* diff --stat 摘要：等宽、可滚，与 result-json 同款。 */
.outcome-pre.diff-stat {
  max-height: 360px;
  overflow: auto;
  white-space: pre;
}
/* diff 语义提示：澄清「未提交改动」，避免误读为「全部改动」。 */
.diff-note {
  margin: 0 0 6px;
  font-size: 11px;
  color: var(--queue);
}
.env-fold {
  margin-top: 8px;
}
.env-fold > summary {
  cursor: pointer;
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
  padding: 4px 0;
  list-style: revert;
}
.env-fold > summary:hover {
  color: var(--phosphor);
}
.env-list {
  margin: 6px 0 0;
  padding-left: 18px;
  font-size: 12px;
  color: var(--paper);
}
.env-list li {
  padding: 1px 0;
}

/* 产物清单：name 占满 + size + 下载按钮一行。 */
.artifact-list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.artifact-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 0;
  border-bottom: 1px solid var(--line);
}
.artifact-row:last-child {
  border-bottom: none;
}
.artifact-name {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--paper);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.artifact-size {
  flex: 0 0 auto;
  color: var(--queue);
  font-size: 11px;
}
.artifact-dl {
  flex: 0 0 auto;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 10px;
  font-size: 11px;
}
.artifact-dl:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.artifact-dl:disabled {
  opacity: 0.5;
  cursor: default;
}
.artifact-err {
  color: var(--fail);
  font-size: 11px;
  margin: 6px 0 0;
}

/* 文件传输（XFER-01 X2）：一块里三段（上传/收集/跳过），段间留白，段内复用产物行样式。 */
.xfer-part + .xfer-part {
  margin-top: 10px;
}
/* 收集行的文件名是个按钮（点击预览）：去掉按钮外观，保留 artifact-name 的排版。 */
.xfer-name-btn {
  flex: 1 1 auto;
  min-width: 0;
  padding: 0;
  background: transparent;
  border: none;
  text-align: left;
  font: inherit;
  color: var(--paper);
  cursor: pointer;
}
.xfer-name-btn:hover:not(:disabled) {
  color: var(--phosphor);
  text-decoration: underline;
}
.xfer-name-btn:disabled {
  cursor: default;
  opacity: 0.6;
}
/* 状态芯片：上传成功/失败一眼可辨（失败时紧随其后的 .xfer-note 给原因）。 */
.xfer-chip {
  flex: 0 0 auto;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  font-size: 11px;
  color: var(--queue);
}
.xfer-chip--ok {
  color: var(--done);
  border-color: var(--done);
}
.xfer-chip--bad {
  color: var(--fail);
  border-color: var(--fail);
}
/* 失败原因 / 跳过原因：行内一等公民但可截断，悬停看全文。 */
.xfer-note {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--fail);
  font-size: 11px;
}

/* 产物预览弹层（E19a）：居中模态，遮罩点击关闭；内容交给 FilePreview。 */
.preview-overlay {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.6);
  padding: 24px;
}
.preview-modal {
  display: flex;
  flex-direction: column;
  width: min(960px, 100%);
  max-height: 86vh;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.preview-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  flex: none;
}
.preview-name {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--paper);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.preview-actions {
  flex: none;
  display: flex;
  gap: 8px;
}
.preview-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 14px 16px;
}

/* 完整 diff 右侧抽屉：遮罩右侧滑入面板，占足高度，内容纵向滚动。 */
.drawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.6);
}
.drawer-panel {
  display: flex;
  flex-direction: column;
  width: min(880px, 92vw);
  height: 100%;
  background: var(--panel);
  border-left: 1px solid var(--line);
  overflow: hidden;
}
.drawer-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  flex: none;
}
.drawer-title {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--paper);
  font-size: 12px;
  letter-spacing: 0.04em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.drawer-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 12px 14px;
}
.drawer-diff {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--paper);
  white-space: pre;
}
.terminal-drawer {
  width: min(1040px, 96vw);
}
.terminal-drawer-head {
  align-items: center;
}
.term-mode-toggle {
  display: inline-flex;
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.term-mode-toggle button {
  background: transparent;
  color: var(--queue);
  border: 0;
  border-right: 1px solid var(--line);
  padding: 3px 10px;
  font-size: 11px;
}
.term-mode-toggle button:last-child {
  border-right: 0;
}
.term-mode-toggle button.active {
  background: var(--phosphor);
  color: var(--ink);
}
.terminal-drawer-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
}
.terminal-drawer-body :deep(.attach-terminal) {
  flex: 1 1 auto;
  min-height: 0;
}
.term-exit,
.term-error {
  flex: none;
  margin: 0;
  font-size: 12px;
}
.term-exit {
  color: var(--queue);
}
.term-error {
  color: var(--fail);
}
@media (prefers-reduced-motion: no-preference) {
  .drawer-panel {
    animation: drawer-slide-in 0.18s ease-out;
  }
}
@keyframes drawer-slide-in {
  from {
    transform: translateX(100%);
  }
  to {
    transform: translateX(0);
  }
}
/* 唤醒（JOB-09）：列表一行一条，形态/目标/等什么/指令/次数 + 开关。 */
.wakeups {
  margin: 0 0 14px;
}
.wakeups-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 6px;
  font-size: 13px;
}
.wakeups-sum {
  color: #8a94a6;
  font-weight: 400;
}
.wakeups-add {
  margin-left: auto;
  padding: 2px 8px;
  border: 1px solid #3a4354;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  cursor: pointer;
}
.wakeups-list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.wakeup-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 3px 0;
  border-bottom: 1px solid #222833;
}
.wakeup-row--off {
  opacity: 0.55;
}
.wakeup-kind {
  min-width: 48px;
  color: #8a94a6;
}
.wakeup-target {
  min-width: 110px;
}
.wakeup-trigger {
  min-width: 170px;
}
.wakeup-instruction {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #8a94a6;
}
.wakeup-count {
  color: #8a94a6;
}
.wakeup-cont {
  color: #6ea8fe;
}
.wakeup-btn {
  padding: 1px 8px;
  border: 1px solid #3a4354;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  cursor: pointer;
}
.wakeup-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.wakeup-btn--primary {
  border-color: #6ea8fe;
}
.wakeup-form {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px;
  margin-top: 8px;
}
.wakeup-field {
  display: flex;
  flex-direction: column;
  gap: 2px;
  color: #8a94a6;
}
.wakeup-field--wide {
  flex: 1;
  min-width: 240px;
}
.wakeup-field input,
.wakeup-field select {
  padding: 2px 6px;
  border: 1px solid #3a4354;
  border-radius: 4px;
  background: #171b22;
  color: inherit;
}
.wakeup-history {
  margin-top: 8px;
}
</style>
