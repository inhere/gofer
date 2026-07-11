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
  puntInteraction,
  resumeJob,
} from '../api/client'
import { appendCapped, streamJob } from '../api/sse'
import { fmtDuration, jobDurationSec, toUnixSec } from '../api/time'
import type {
  Artifact,
  Delivery,
  Interaction,
  Job,
  JobEvent,
  JobStatus,
  LogStream,
  PtySession,
  SSEEvent,
  SSEInteractionData,
  SSEJobEventData,
  SSELogData,
  SSELogRotatedData,
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

// 终态集合
const TERMINAL: JobStatus[] = ['done', 'failed', 'cancelled', 'timeout']
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
  preview.value = null
  previewingNames.value = new Set()
  previewError.value = ''
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

async function onDownload(name: string): Promise<void> {
  if (downloadingNames.value.has(name)) {
    return
  }
  const next = new Set(downloadingNames.value)
  next.add(name)
  downloadingNames.value = next
  try {
    await downloadArtifact(props.id, name)
  } catch (e) {
    artifactError.value = e instanceof Error ? e.message : String(e)
  } finally {
    const after = new Set(downloadingNames.value)
    after.delete(name)
    downloadingNames.value = after
  }
}

// ── 产物 inline 预览（E19a）──────────────────────────────────────────
// 点「预览」→ 取 blob（带鉴权）→ 弹层挂 FilePreview（md/图/json/文本，按 D5）。
// previewingNames 标记取数中（防重复点击）；preview 为当前弹层文件（null=未打开）。
const preview = ref<{ name: string; blob: Blob } | null>(null)
const previewingNames = ref<Set<string>>(new Set())
const previewError = ref('')

async function onPreview(name: string): Promise<void> {
  if (previewingNames.value.has(name)) {
    return
  }
  previewError.value = ''
  const next = new Set(previewingNames.value)
  next.add(name)
  previewingNames.value = next
  try {
    const blob = await fetchArtifactBlob(props.id, name)
    preview.value = { name, blob }
  } catch (e) {
    previewError.value = e instanceof Error ? e.message : String(e)
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
    void onDownload(preview.value.name)
  }
}

// 人类可读文件大小（B/KB/MB），mono 列展示。
function fmtSize(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
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
    diffSummary.value !== '',
)

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
        <StatusBadge v-if="job" :status="status" />
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
      <div class="meta-item">
        <span class="meta-k mono">started</span><span class="meta-v mono">{{ fmtTime(job.started_at) }}</span>
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
</style>
