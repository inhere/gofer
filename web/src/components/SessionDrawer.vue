<script setup lang="ts">
// 会话中继（SESS-01）详情抽屉：右侧滑入面板。
//  - 头部：一行摘要（sid / agent / project / runner / turn / last_seen），点击展开
//    完整元数据（cwd / transcript / started / …）。默认收起，把纵向空间让给消息，
//    展开状态存 localStorage。
//  - 中间：turn 时间线。后端 newest-first，这里反转成最旧在上、最新在下（聊天习惯）；
//    每个 turn 两条气泡：灰 = agent 消息（question，**markdown 渲染**，marked +
//    DOMPurify.sanitize 后注入），蓝 = 人的回复（answer，纯文本原样显示）。
//  - 底部：输入框 + 发送。仅存在 OPEN turn 时可用，走 POST /v1/sessions/{sid}/say；
//    回复 `/off` 会关闭中继让会话正常停下。Ctrl/Cmd+Enter 发送。
//  - 打开期间订阅 `sessions` 推送刷新详情（断线 >15s 才 30s 兜底轮询）。
import { createLiveTopic } from '../utils/useLiveTopic'
import { sessionUsageRows } from '../utils/sessionUsage'
import { runnerLabel } from '../utils/runnerDisplay'
import SessionNudges from './SessionNudges.vue'
import SessionPermissionPrompt from './SessionPermissionPrompt.vue'
import { computed, nextTick, onMounted, onUnmounted, onUpdated, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import {
  ApiError,
  deleteAgentSession,
  deliverSession,
  getAgentSession,
  ackSessionTurn,
  getSessionMessageLog,
  getSessionTakeoverPlan,
  listSessionMessages,
  releaseSessionTakeover,
  resumeSession,
  saySession,
  sendSessionMessage,
  setSessionRelay,
  unackSessionTurn,
} from '../api/client'
import { turnWorkbenchThread } from '../api/workbench'
import { fmtAgo, fmtDateTime } from '../api/time'
import { copyText, mergeSessionTimeline, peerNameLabel, shouldShowLastMessage, upsertSessionMessage } from '../utils/sessionMessaging'
import { resumeConfirmText, resumeFailText, resumeLabel, resumeTitle } from '../utils/sessionResume'
import { mergeNewestPage, mergeOlderPage, preserveScrollAfterPrepend, shouldFollowBottom } from '../utils/sessionPagination'
import type {
  AgentSession,
  AgentSessionRelayMode,
  AgentSessionState,
  Decision,
  SessionMessage,
  SessionResumePlan,
} from '../api/types'

const props = withDefaults(defineProps<{ sid: string; embedded?: boolean; threadId?: string; expandLastMessage?: boolean }>(), {
  embedded: false,
  threadId: '',
  expandLastMessage: false,
})
const router = useRouter()
const emit = defineEmits<{
  (e: 'close'): void
  // 会话有变更（中继开关 / 作答 / 删除）→ 父级列表可即时刷新
  (e: 'changed'): void
  (e: 'deleted', sid: string): void
}>()

const TURNS_LIMIT = 10
// 超过此长度的 agent 消息默认折叠（约合 320px 裁剪高度，见 .bubble-md.clamped）
const COLLAPSE_CHARS = 900

const session = ref<AgentSession | null>(null)
const turns = ref<Decision[]>([])
const messages = ref<SessionMessage[]>([])
const loading = ref(false)
const error = ref('')
const actionError = ref('')
// actionInfo 是成功回执（"已送入终端 ✓"）：与错误分开，避免下一帧被覆盖。
const actionInfo = ref('')
const draft = ref('')
const sending = ref(false)
const retryingMessage = ref<string | null>(null)
const relayBusy = ref(false)
const deleting = ref(false)
// takeover* 是路径 B 的二次确认（§9.1 B）：deliver 报 no_tmux / pane_missing 后，
// 输入框旁出现"起新进程接管并发送"，点一次进入确认态，确认后才真的起进程。
const takeoverOffered = ref(false)
const takeoverConfirm = ref(false)
const releasing = ref(false)
// 唤醒/接管（常驻按钮，不依赖“发送失败”）：点按钮先取 takeover-plan 干跑结果，
// 展示将要起的进程（执行机 / agent / 命令）并二次确认，确认后 POST resume。
// 已结束（ended）的会话也能唤醒——这正是“关掉的终端想再打开”的入口。
const wakeOpen = ref(false)
const wakePlan = ref<SessionResumePlan | null>(null)
const wakePlanLoading = ref(false)
const waking = ref(false)
const copied = ref(false)
const copiedLast = ref(false)
const copiedName = ref(false)
const lastMessageOpen = ref(props.expandLastMessage)
const progressOpen = ref(false)
const expanded = ref<Set<string>>(new Set())
const ackBusy = ref<string | null>(null)
// 元数据面板展开状态：默认收起（消息优先），记住用户选择。
const META_OPEN_KEY = 'gofer.sessionDrawer.metaOpen'
const metaOpen = ref(readMetaOpen())
const nowSec = ref(Math.floor(Date.now() / 1000))
const timelineEl = ref<HTMLElement | null>(null)
// 手机工作台先显示会话列表，线程面板挂载时处于隐藏状态（高度 0），此时的"滚到底部"
// 不生效；点开同一个会话不会重新加载。所以在消息区从不可见变为可见时再滚到最新一次。
let timelineResize: ResizeObserver | null = null
let timelineWasHidden = true
watch(timelineEl, (el) => {
  timelineResize?.disconnect()
  timelineResize = null
  timelineWasHidden = true
  if (!el || typeof ResizeObserver === 'undefined') return
  timelineResize = new ResizeObserver(() => {
    const hidden = el.clientHeight === 0
    if (timelineWasHidden && !hidden) scrollToBottom()
    timelineWasHidden = hidden
  })
  timelineResize.observe(el)
})
const hasMore = ref(false)
const nextBefore = ref('')
const messagesHasMore = ref(false)
const messagesNextBefore = ref('')
const loadingMore = ref(false)
const initialScrollDone = ref(false)
const historyExpanded = ref(false)

watch(() => props.expandLastMessage, (open) => {
  if (open) lastMessageOpen.value = true
})

let clock: number | null = null

const STATE_LABELS: Record<AgentSessionState, string> = {
  running: '执行中',
  idle: '空闲',
  waiting_reply: '等待回复',
  needs_attention: '需注意',
  handed_off: '已接管',
  ended: '已结束',
  offline: '离线',
}

function stateLabel(s: AgentSessionState): string {
  return STATE_LABELS[s] ?? s
}

function readMetaOpen(): boolean {
  try {
    return window.localStorage.getItem(META_OPEN_KEY) === '1'
  } catch {
    return false
  }
}

function toggleMeta(): void {
  metaOpen.value = !metaOpen.value
  try {
    window.localStorage.setItem(META_OPEN_KEY, metaOpen.value ? '1' : '0')
  } catch {
    // 隐私模式 / 禁用存储：只影响记忆，不影响使用
  }
}

// agent 消息按 markdown 渲染。XSS 安全核心：marked 渲染后必经 DOMPurify.sanitize
// 才注入（与 FilePreview 同一约定）。3s 轮询会重复渲染同一条消息，故按内容缓存。
const mdCache = new Map<string, string>()

function renderMd(text: string): string {
  const key = text
  const hit = mdCache.get(key)
  if (hit !== undefined) {
    return hit
  }
  let html: string
  try {
    html = DOMPurify.sanitize(marked.parse(text, { async: false }))
  } catch {
    // 渲染失败不能吞消息：退化为转义后的纯文本
    html = `<pre>${escapeHtml(text)}</pre>`
  }
  if (mdCache.size > 200) {
    mdCache.clear()
  }
  mdCache.set(key, html)
  return html
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

// 时间线：最旧在上、最新在下
const timeline = computed(() => [...turns.value].reverse())
// 终端工具授权请求（kind=permission）不是 turn：输入框不作答它，单独用按钮答
const openTurn = computed(() => turns.value.find((t) => t.state === 'OPEN' && t.kind !== 'permission') ?? null)
const conversationTimeline = computed(() => mergeSessionTimeline(timeline.value, messages.value))
watch(conversationTimeline, async () => {
  await nextTick()
  if (isTimelineAtBottom()) scrollToBottom()
})

onUpdated(() => {
  if (!initialScrollDone.value && conversationTimeline.value.length > 0 && timelineEl.value) {
    initialScrollDone.value = true
    scrollToBottom()
  }
})
// 与最近一轮中继的内容相同就不再单独显示（不论这一轮是否已回复）：那条消息已经
// 作为气泡出现在对话流里了。只有被放行、没开中继轮的回合才需要这一块。
const latestTurn = computed(() => turns.value[0] ?? null)
const showLastMessage = computed(() => shouldShowLastMessage(
  session.value?.last_message,
  latestTurn.value?.question,
  !!latestTurn.value,
))
// canSend：当前这封消息能不能发出去 —— 有 OPEN turn 就是作答，没有就是"送到终端"
// （§9.1 A 的 tmux 注入）。会话结束后两者都不行；已接管（§9.1 B）时终端已不属于
// 本会话，要发话得先解除接管。
const canSend = computed(
  () =>
    !sending.value &&
    session.value?.state !== 'ended' &&
    session.value?.state !== 'handed_off' &&
    (!props.embedded || !!openTurn.value || !!props.threadId),
)
// showWake：已被接管的会话由下方的接管条处理（跳转 / 解除），其余状态都给唤醒入口。
const showWake = computed(() => !!session.value && session.value.state !== 'handed_off')

async function openWake(): Promise<void> {
  if (!session.value?.can_resume || wakePlanLoading.value) return
  wakeOpen.value = true
  wakePlanLoading.value = true
  actionError.value = ''
  actionInfo.value = ''
  try {
    wakePlan.value = await getSessionTakeoverPlan(props.sid)
  } catch (e) {
    wakePlan.value = null
    wakeOpen.value = false
    actionError.value = `查询唤醒方案失败：${resumeFailText(e)}`
  } finally {
    wakePlanLoading.value = false
  }
}

// 确认后真的唤醒：输入框里有草稿就当作新终端的首条输入，没有就只打开会话。成功后
// 跳到新 job 的终端（?attach=1 自动接入）。
async function confirmWake(): Promise<void> {
  if (waking.value) return
  waking.value = true
  actionError.value = ''
  actionInfo.value = ''
  try {
    const res = await resumeSession(props.sid, draft.value.trim())
    draft.value = ''
    wakeOpen.value = false
    actionInfo.value = `已起新进程（job ${res.job_id}）✓`
    await load({ silent: true })
    emit('changed')
    if (res.job_id) await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
  } catch (e) {
    actionError.value = `唤醒失败：${resumeFailText(e)}`
    await load({ silent: true })
  } finally {
    waking.value = false
  }
}

// toTerminal：这封消息走的是注入路径（没有 turn 在等），占位与回执据此切换。
const toTerminal = computed(() => !openTurn.value)
// handedOffJob：会话当前被哪个 pty job 接管（§9.1 B），空 = 没被接管。
const handedOffJob = computed(() => session.value?.handed_off_job_id ?? '')
// takeoverHint 是二次确认的文案（设计 §9.1 B 的原话）：说明要起什么进程、原终端会怎样。
const takeoverHint = computed(
  () =>
    `将用 \`${session.value?.agent ?? 'agent'} --resume\` 起一个新进程接管该会话，原终端将不能继续。是否继续？`,
)

const titleText = computed(() => {
  const s = session.value
  if (!s) {
    return props.sid.slice(0, 8)
  }
  return s.title || `${s.agent} · ${s.session_id.slice(0, 8)}`
})

function shortSid(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id
}

// idleText renders the hook's idle reading (seconds) for display; -1 = the
// probe could not tell.
function idleText(sec: number | undefined): string {
  if (sec === undefined || sec < 0) {
    return '—'
  }
  if (sec < 60) {
    return `${sec}s`
  }
  const mins = Math.floor(sec / 60)
  if (mins < 60) {
    return `${mins}m`
  }
  return `${Math.floor(mins / 60)}h${String(mins % 60).padStart(2, '0')}m`
}

// relayDemotedText 解释开关为什么从 on 变成了 auto：人在终端输入了一条（视为回来了）。
const relayDemotedText = computed(() => {
  const at = session.value?.relay_demoted_at
  if (!at || (session.value?.relay_mode || 'auto') !== 'auto') {
    return ''
  }
  const hhmm = new Date(at * 1000).toTimeString().slice(0, 5)
  return `中继已于 ${hhmm} 自动从 on 回到 auto：终端有人工输入（视为你回来了）。要继续在这里等回复，重新拨到 on。`
})

// RELAY_MODES 是三态开关的展示顺序（R1）。
const RELAY_MODES: AgentSessionRelayMode[] = ['auto', 'on', 'off']

// humanSilence 是人在这个会话里安静了多久（秒，-1 = 还没见过人工输入）；探测不到
// 键盘的终端（容器）靠它判定（R2）。
function humanSilence(s: AgentSession): number {
  if (!s.last_human_at) {
    return -1
  }
  return Math.max(0, nowSec.value - s.last_human_at)
}

// relaySummary is the one-line relay state: the switch, whether it is waiting
// right now, and the evidence behind the decision.
function relaySummary(s: AgentSession | null): string {
  if (!s) {
    return '—'
  }
  const mode = s.relay_mode
  if (!s.wait_reason) {
    // SUP-01 D: not waiting because the caller is supervising live jobs — say so
    // instead of leaving the human guessing why nothing armed.
    return s.wait_reason_detail ? `${mode}：当前不等（${s.wait_reason_detail}）` : `${mode}：当前不等`
  }
  switch (s.wait_reason) {
    case 'mode_on':
      return 'on：显式开关，本次停下在等你回复'
    case 'idle_probe':
      return `auto：键盘空闲 ${idleText(s.idle_sec)}，本次停下在等你回复`
    case 'turn_age':
      return `auto：探测不到键盘，距上次人工输入 ${idleText(humanSilence(s))}`
    default:
      return `${mode}：本次停下在等你回复`
  }
}

function isLong(text: string): boolean {
  return text.length > COLLAPSE_CHARS
}

function toggleExpand(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  expanded.value = next
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function messageStatusLabel(message: SessionMessage): string {
  if (message.status === 'queued') return '排队中'
  if (message.status === 'delivered') return `已送达 · ${message.channel || '通道'}`
  return `失败 · ${message.error || '未知原因'}`
}

async function retryMessage(message: SessionMessage): Promise<void> {
  if (retryingMessage.value || message.status !== 'failed') return
  retryingMessage.value = message.id
  actionError.value = ''
  actionInfo.value = ''
  try {
    const next = await sendSessionMessage(props.sid, message.text)
    messages.value = upsertSessionMessage(messages.value, next)
    actionInfo.value = '已重新排队 ✓'
    await load({ silent: true })
  } catch (e) {
    actionError.value = `重试失败：${errorMessage(e)}`
  } finally {
    retryingMessage.value = null
  }
}

function scrollToBottom(): void {
  void nextTick(() => void nextTick(() => {
    const el = timelineEl.value
    if (!el) return
    el.scrollTop = el.scrollHeight
    window.requestAnimationFrame(() => {
      if (timelineEl.value) timelineEl.value.scrollTop = timelineEl.value.scrollHeight
    })
  }))
}

function isTimelineAtBottom(): boolean {
  const el = timelineEl.value
  if (!el) return true
  return shouldFollowBottom(el)
}

async function load(opts?: { silent?: boolean; before?: string; outboxBefore?: string }): Promise<void> {
  if (!opts?.silent) {
    loading.value = true
  }
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const wasAtBottom = isTimelineAtBottom()
    const before = opts?.before ?? ''
    const el = timelineEl.value
    const beforeHeight = el?.scrollHeight ?? 0
    const beforeTop = el?.scrollTop ?? 0
    const resp = await getAgentSession(props.sid, { limit: TURNS_LIMIT, before })
    const prevLast = turns.value[0]?.id
    const prevLen = turns.value.length
    const prevMessageLast = messages.value[messages.value.length - 1]?.id
    const prevMessageLen = messages.value.length
    session.value = resp.session
    if (before) {
      turns.value = mergeOlderPage(turns.value, resp.turns ?? [])
      hasMore.value = resp.has_more
      nextBefore.value = resp.next_before ?? ''
    } else if (historyExpanded.value) {
      turns.value = mergeNewestPage(resp.turns ?? [], turns.value)
    } else {
      turns.value = resp.turns ?? []
      hasMore.value = resp.has_more
      nextBefore.value = resp.next_before ?? ''
    }
    try {
      const messageResp = await listSessionMessages(props.sid, { limit: TURNS_LIMIT, before: opts?.outboxBefore })
      if (opts?.outboxBefore) {
        messages.value = mergeOlderPage(messages.value, messageResp.messages ?? [])
        messagesHasMore.value = !!messageResp.has_more
        messagesNextBefore.value = messageResp.next_before ?? ''
      } else if (historyExpanded.value) {
        messages.value = mergeNewestPage(messageResp.messages ?? [], messages.value)
      } else {
        messages.value = messageResp.messages ?? []
        messagesHasMore.value = !!messageResp.has_more
        messagesNextBefore.value = messageResp.next_before ?? ''
      }
    } catch {
      messages.value = []
    }
    error.value = ''
    // 仅当用户原本就在底部时跟随新 turn 或转达消息；用户查看历史时不抢滚动位置。
    const messagesChanged = messages.value.length !== prevMessageLen || messages.value[messages.value.length - 1]?.id !== prevMessageLast
    if (before) {
      await nextTick()
      if (timelineEl.value) timelineEl.value.scrollTop = preserveScrollAfterPrepend({
        beforeHeight,
        beforeTop,
        afterHeight: timelineEl.value.scrollHeight,
      })
    } else if ((prevLen === 0 && turns.value.length > 0) || (wasAtBottom && (turns.value.length !== prevLen || turns.value[0]?.id !== prevLast || messagesChanged))) {
      scrollToBottom()
    }
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

async function loadMore(): Promise<void> {
  if (loadingMore.value || (!hasMore.value && !messagesHasMore.value)) return
  loadingMore.value = true
  historyExpanded.value = true
  try {
    await load({ silent: true, before: nextBefore.value, outboxBefore: messagesNextBefore.value })
  } finally {
    loadingMore.value = false
  }
}

function onTimelineScroll(): void {
  if ((timelineEl.value?.scrollTop ?? 1) <= 32) void loadMore()
}

async function copySid(): Promise<void> {
  try {
    await navigator.clipboard.writeText(props.sid)
    copied.value = true
    window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // 剪贴板不可用（非安全上下文等）时静默：用户仍可手动选择文本复制。
  }
}

async function copyName(): Promise<void> {
  if (!session.value?.peer_name || !(await copyText(session.value.peer_name))) return
  copiedName.value = true
  window.setTimeout(() => { copiedName.value = false }, 1500)
}

async function copyLastMessage(): Promise<void> {
  if (!session.value?.last_message) return
  await navigator.clipboard.writeText(session.value.last_message)
  copiedLast.value = true
  window.setTimeout(() => { copiedLast.value = false }, 1500)
}

async function openMessageHistory(): Promise<void> {
  // Open synchronously from the click so popup blockers permit the new tab.
  const tab = window.open('', '_blank')
  if (!tab) {
    error.value = '浏览器阻止了新标签页，请允许弹出窗口后重试'
    return
  }
  tab.opener = null
  try {
    const raw = await getSessionMessageLog(props.sid)
    const url = URL.createObjectURL(new Blob([raw], { type: 'text/plain;charset=utf-8' }))
    tab.location.href = url
    window.setTimeout(() => URL.revokeObjectURL(url), 60000)
  } catch (e) {
    tab.close()
    error.value = errorMessage(e)
  }
}

// 中继三态开关：点击即 POST，乐观更新，失败回滚。
async function setRelayMode(mode: AgentSessionRelayMode): Promise<void> {
  const s = session.value
  if (!s || relayBusy.value || s.relay_mode === mode) {
    return
  }
  const prev = s.relay_mode
  relayBusy.value = true
  actionError.value = ''
  actionInfo.value = ''
  s.relay_mode = mode
  try {
    const updated = await setSessionRelay(s.session_id, mode)
    session.value = updated
    emit('changed')
  } catch (e) {
    if (session.value) {
      session.value.relay_mode = prev
    }
    actionError.value = `切换中继失败：${errorMessage(e)}`
  } finally {
    relayBusy.value = false
  }
}

async function send(): Promise<void> {
  const text = draft.value.trim()
  if (!text || !canSend.value) {
    return
  }
  sending.value = true
  actionError.value = ''
  actionInfo.value = ''
  takeoverOffered.value = false
  takeoverConfirm.value = false
  try {
    if (props.threadId && openTurn.value) {
      await turnWorkbenchThread(props.threadId, text)
      actionInfo.value = '已回复 agent ✓'
    } else if (props.threadId) {
      const res = await sendSessionMessage(props.sid, text)
      actionInfo.value = res.status === 'delivered'
        ? `已送达（${channelLabel(res.channel)}）✓`
        : `消息状态：${res.status}`
    } else if (openTurn.value) {
      await saySession(props.sid, text)
      actionInfo.value = '已回复 agent ✓'
    } else {
      const res = await sendSessionMessage(props.sid, text)
      actionInfo.value = res.status === 'delivered'
        ? `已送达（${channelLabel(res.channel)}）✓`
        : `消息状态：${res.status}`
    }
    draft.value = ''
    await load({ silent: true })
    scrollToBottom()
    emit('changed')
  } catch (e) {
    if (toTerminal.value && !props.threadId) {
      actionError.value = `发送到终端失败：${deliverErrorMessage(e)}`
      // 消息还在草稿里：A 送不进去时会提示接管（§9.1 B），由用户二次确认后再发。
      takeoverOffered.value = takeoverAvailable(e)
    } else {
      actionError.value = `发送失败：${errorMessage(e)}`
    }
  } finally {
    sending.value = false
  }
}

function onPermissionAnswered(updated: Decision): void {
  turns.value = turns.value.map((candidate) => (candidate.id === updated.id ? updated : candidate))
}

async function toggleAck(turn: Decision): Promise<void> {
  if (turn.state !== 'OPEN' || ackBusy.value) return
  ackBusy.value = turn.id
  actionError.value = ''
  try {
    const updated = turn.acked_at
      ? await unackSessionTurn(props.sid, turn.id)
      : await ackSessionTurn(props.sid, turn.id)
    turns.value = turns.value.map((candidate) => candidate.id === turn.id ? updated : candidate)
    emit('changed')
  } catch (e) {
    actionError.value = `标记等待失败：${errorMessage(e)}`
  } finally {
    ackBusy.value = null
  }
}

// channelLabel：消息送达渠道的中文名（command = agent 自己的送话命令交给在线进程）。
function channelLabel(channel?: string): string {
  switch (channel) {
    case 'messenger':
      return '传话人'
    case 'command':
      return '在线会话'
    case 'tmux':
      return '终端 tmux'
    default:
      return '中继'
  }
}

// takeoverAvailable 判断这次失败是否能用路径 B 兜底：会话没有可用的 tmux pane
// （no_tmux / pane_missing）——服务端把 pane_missing 也算进接管兜底集合。
function takeoverAvailable(e: unknown): boolean {
  const code = e instanceof ApiError ? `${e.code ?? ''} ${e.detail ?? ''}` : String(e)
  return code.includes('no_tmux') || code.includes('pane_missing')
}

// takeOver 是确认后的路径 B 发送（§9.1 B）：服务端起 `--resume` pty job 接管会话，
// 把草稿作为它的首条输入，然后跳到该 job 的终端（?attach=1 自动接入）。
async function takeOver(): Promise<void> {
  const text = draft.value.trim()
  if (!text || sending.value) {
    return
  }
  sending.value = true
  actionError.value = ''
  actionInfo.value = ''
  try {
    const res = await deliverSession(props.sid, text, true)
    if (res.path !== 'takeover' || !res.job_id) {
      actionInfo.value = res.path === 'tmux' ? '已送入终端 ✓' : res.path === 'command' ? '已送达在线会话 ✓' : '已回复 agent ✓'
      draft.value = ''
      await load({ silent: true })
      emit('changed')
      return
    }
    draft.value = ''
    actionInfo.value = `已起新进程接管（job ${res.job_id}）✓`
    await load({ silent: true })
    emit('changed')
    await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
  } catch (e) {
    actionError.value = `接管失败：${deliverErrorMessage(e)}`
  } finally {
    sending.value = false
    takeoverOffered.value = false
    takeoverConfirm.value = false
  }
}

// releaseTakeover 解除接管（§9.1 B）：服务端 cancel 接管 job 并把会话交还原终端。
async function releaseTakeover(): Promise<void> {
  releasing.value = true
  actionError.value = ''
  actionInfo.value = ''
  try {
    session.value = await releaseSessionTakeover(props.sid)
    actionInfo.value = '已解除接管，原终端恢复中继 ✓'
    await load({ silent: true })
    emit('changed')
  } catch (e) {
    actionError.value = `解除接管失败：${errorMessage(e)}`
  } finally {
    releasing.value = false
  }
}

// deliverErrorMessage 把服务端的原因码翻成能照做的提示：no_tmux / pane_missing 提示
// 接管（§9.1 B），no_resume_template 等说清为什么接管也不可用。
function deliverErrorMessage(e: unknown): string {
  const code = e instanceof ApiError ? `${e.code ?? ''} ${e.detail ?? ''}` : String(e)
  if (code.includes('session_alive')) {
    return '会话进程仍在线（刚刚还有心跳），为避免两个进程同时写同一会话，已拒绝接管；请在原终端继续，或用在线送话'
  }
  if (code.includes('deliver_failed')) {
    return `agent 的送话命令失败：${e instanceof ApiError ? (e.detail ?? e.code ?? '') : String(e)}`
  }
  if (code.includes('not_running')) {
    return '会话进程已不在运行；可用「起新进程接管并发送」继续'
  }
  if (code.includes('no_tmux')) {
    return '该会话不在 tmux 中；可在 tmux 里启动会话，或用「起新进程接管并发送」'
  }
  if (code.includes('no_runner')) {
    return '该会话未登记执行机：容器内需起一个 gofer worker，并把 GOFER_HOOK_RUNNER 指向它'
  }
  if (code.includes('handed_off')) {
    return '该会话已被接管：请先「解除接管」，或到接管的终端里继续'
  }
  if (code.includes('no_resume_template')) {
    return '该 agent 没有交互式 resume 模板（claude/codex/omp 内置支持），无法起新进程接管'
  }
  if (code.includes('interactive_not_allowed')) {
    return '该项目未开启 allow_interactive，无法起新进程接管'
  }
  if (code.includes('cwd_outside_project')) {
    return '会话目录无法换算成执行机上的项目相对路径，无法起新进程接管'
  }
  if (code.includes('ended')) {
    return '会话已结束'
  }
  return errorMessage(e)
}

function onKeydown(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault()
    void send()
  }
}

function autoGrow(ev: Event): void {
  const el = ev.target as HTMLTextAreaElement
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, window.innerHeight * 0.4)}px`
}

async function remove(): Promise<void> {
  if (deleting.value) {
    return
  }
  if (!window.confirm(`移除会话登记 ${shortSid(props.sid)}？只删除 gofer 侧登记，不影响终端里的 agent 进程。`)) {
    return
  }
  deleting.value = true
  actionError.value = ''
  try {
    await deleteAgentSession(props.sid)
    emit('deleted', props.sid)
    emit('close')
  } catch (e) {
    actionError.value = `移除失败：${errorMessage(e)}`
  } finally {
    deleting.value = false
  }
}

function onEsc(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !props.embedded) {
    emit('close')
  }
}

watch(
  () => props.sid,
  () => {
    session.value = null
    turns.value = []
    messages.value = []
    draft.value = ''
    error.value = ''
    actionError.value = ''
    actionInfo.value = ''
    expanded.value = new Set()
    hasMore.value = false
    nextBefore.value = ''
    messagesHasMore.value = false
    messagesNextBefore.value = ''
    initialScrollDone.value = false
    historyExpanded.value = false
    progressOpen.value = false
    void load().then(scrollToBottom)
    window.setTimeout(scrollToBottom, 1000)
  },
)

// Q3：`sessions` 主题的失效通知（会话状态 / 轮次 / 消息 / 中继决策变化）触发重拉；
// WS 断开超过 15s 才由 30s 兜底轮询接手（恢复后自动停）。
const liveSession = createLiveTopic('sessions', {
  initial: false,
  fetch: () => load({ silent: true }),
})

onMounted(() => {
  // 抽屉挂载时 sid 已经给定：上面的 watch 不会触发，必须在这里立即加载；
  // 否则要等第一次轮询（兜底轮询）才显示消息。
  if (props.sid) void load().then(scrollToBottom)
  liveSession.start()
  clock = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 10000)
  document.addEventListener('keydown', onEsc)
})

onUnmounted(() => {
  timelineResize?.disconnect()
  liveSession.stop()
  if (clock != null) {
    window.clearInterval(clock)
    clock = null
  }
  document.removeEventListener('keydown', onEsc)
})

defineExpose({ load, loadMore, setRelayMode, remove })
</script>

<template>
  <div class="drawer-overlay" :class="{ 'drawer-overlay--embedded': embedded }" @click.self="!embedded && emit('close')">
    <div class="drawer-panel" :role="embedded ? 'region' : 'dialog'" aria-label="会话详情">
      <div v-if="!embedded" class="drawer-head">
        <div class="head-main">
          <span class="drawer-title mono" :title="titleText">{{ titleText }}</span>
          <span
            v-if="session"
            class="state-badge mono"
            :class="`state--${session.state}`"
          >
            {{ stateLabel(session.state) }}
          </span>
        </div>
        <div class="head-actions mono">
          <span
            v-if="session"
            class="relay-modes mono"
            :class="{ busy: relayBusy }"
            title="中继开关（三态）：on = 每次停下都在这里等你回复（在终端输入一条后自动回到 auto）；off = 从不等；auto = server 按键盘空闲 / 距上次人工输入的时长决定"
          >
            <button
              v-for="m in RELAY_MODES"
              :key="m"
              type="button"
              class="relay-mode"
              :class="{ active: (session.relay_mode || 'auto') === m }"
              :disabled="relayBusy || session.state === 'ended'"
              @click="setRelayMode(m)"
            >
              {{ m }}
            </button>
          </span>
          <button class="act mono" type="button" :disabled="loading" @click="load()">
            {{ loading ? '刷新中…' : '刷新' }}
          </button>
          <button
            v-if="!embedded"
            class="act act--warn mono"
            type="button"
            :disabled="deleting"
            title="把这个会话从 gofer 的登记表里删掉（列表中不再出现）。终端里的 agent 进程不受影响，它下次触发 hook 时会自动重新登记。"
            @click="remove"
          >
            {{ deleting ? '移除中…' : '移除登记' }}
          </button>
          <button
            v-if="!embedded"
            class="act mono"
            type="button"
            title="只关闭这个面板，不改变会话与中继开关"
            @click="emit('close')"
          >
            关闭面板
          </button>
        </div>
      </div>

      <p v-if="error" class="error mono">{{ error }}</p>
      <p v-if="relayDemotedText" class="relay-demoted mono" data-test="relay-demoted">{{ relayDemotedText }}</p>

      <div v-if="session && !embedded" class="meta-wrap">
        <button
          class="meta-toggle mono"
          type="button"
          :aria-expanded="metaOpen"
          @click="toggleMeta"
        >
          <span class="caret">{{ metaOpen ? '▾' : '▸' }}</span>
          <span class="meta-summary">
            <span v-if="session.peer_name" :title="peerNameLabel(session)">{{ session.peer_name }}</span>
            <span v-else :title="session.session_id">{{ shortSid(session.session_id) }}</span>
            <span class="dim">·</span>
            <span>{{ session.agent }}</span>
            <template v-if="session.project_key">
              <span class="dim">·</span><span>{{ session.project_key }}</span>
            </template>
            <template v-if="session.runner">
              <span class="dim">·</span><span>{{ runnerLabel(session.runner) }}</span>
            </template>
            <span class="dim">·</span>
            <span>turn {{ session.turn_no }}</span>
            <span class="dim">·</span>
            <span :title="fmtDateTime(session.last_seen_at)">{{ fmtAgo(session.last_seen_at, nowSec) }}</span>
          </span>
          <span class="meta-hint">{{ metaOpen ? '收起' : '详情' }}</span>
        </button>
      <dl v-show="metaOpen" class="meta mono">
        <dt>sid</dt>
        <dd class="meta-sid">
          <span :title="session.session_id">{{ session.session_id }}</span>
          <button class="copy-btn mono" type="button" @click="copySid">
            {{ copied ? '已复制' : '复制' }}
          </button>
        </dd>
        <dt v-if="session.peer_name">名称</dt>
        <dd v-if="session.peer_name" class="meta-sid">
          <span data-test="peer-name-detail" :title="peerNameLabel(session)">{{ session.peer_name }}</span>
          <span v-if="session.peer_name_source" class="dim">（{{ session.peer_name_source }}）</span>
          <button class="copy-btn mono" type="button" @click="copyName">{{ copiedName ? '已复制' : '复制' }}</button>
        </dd>
        <dt>agent</dt>
        <dd>{{ session.agent }}</dd>
        <dt>project</dt>
        <dd>{{ session.project_key || '—' }}</dd>
        <dt>runner</dt>
        <dd>{{ runnerLabel(session.runner) || '—' }}</dd>
        <dt>cwd</dt>
        <dd class="meta-path" :title="session.cwd">{{ session.cwd || '—' }}</dd>
        <dt v-if="session.last_cwd">当前目录</dt>
        <dd v-if="session.last_cwd" class="meta-path" :title="session.last_cwd">{{ session.last_cwd }}</dd>
        <template v-for="u in sessionUsageRows(session.usage)" :key="u.label">
          <dt>{{ u.label }}用量</dt>
          <dd data-test="session-usage">{{ u.text }}</dd>
        </template>
        <dt>transcript</dt>
        <dd class="meta-path" :title="session.transcript">{{ session.transcript || '—' }}</dd>
        <dt v-if="session.tmux_pane">tmux</dt>
        <dd v-if="session.tmux_pane">{{ session.tmux_pane }}</dd>
        <dt>relay</dt>
        <dd>
          {{ relaySummary(session) }}
          <span class="dim">· mode {{ session.relay_mode || '—' }}</span>
          <span v-if="session.idle_sec >= 0" class="dim">· 终端侧空闲 {{ idleText(session.idle_sec) }}</span>
          <span v-if="session.last_human_at" class="dim">· 上次人工输入 {{ fmtAgo(session.last_human_at, nowSec) }} 前</span>
        </dd>
        <dt>started</dt>
        <dd>{{ fmtDateTime(session.started_at) }}</dd>
        <dt>last_seen</dt>
        <dd :title="fmtDateTime(session.last_seen_at)">
          {{ fmtAgo(session.last_seen_at, nowSec) }}
          <span v-if="session.last_event" class="dim">· {{ session.last_event }}</span>
        </dd>
        <dt v-if="session.ended_at">ended</dt>
        <dd v-if="session.ended_at">{{ fmtDateTime(session.ended_at) }}</dd>
        <dt>turns</dt>
        <dd>{{ session.turn_no }}</dd>
      </dl>
      </div>

      <!-- 唤醒/接管：常驻，不依赖发送失败；已结束的会话也能唤醒。Workbench 嵌入同样显示。 -->
      <div v-if="showWake" class="wake-bar mono" data-test="wake-bar">
        <span class="wake-text">{{ session?.state === 'ended' ? '会话已结束（终端已关闭）。' : session?.state === 'offline' ? '会话长时间没有心跳，已标记为离线（原进程可能已退出）。' : '想在 web 里继续这个会话？' }}</span>
        <button
          class="act act--primary mono"
          type="button"
          data-test="wake-btn"
          :disabled="!session?.can_resume || waking || wakePlanLoading"
          :title="session ? resumeTitle(session) : ''"
          @click="openWake"
        >{{ wakePlanLoading ? '查询中…' : resumeLabel(session!) }}</button>
        <span v-if="session && !session.can_resume" class="wake-why" data-test="wake-why">{{ session.resume_message }}</span>
      </div>
      <div v-if="wakeOpen && wakePlan" class="takeover-confirm wake-confirm mono" data-test="wake-confirm">
        <span class="takeover-text">
          {{ session ? resumeConfirmText(session, wakePlan) : '' }}
          <code v-if="wakePlan.command?.length" class="wake-cmd">{{ wakePlan.command.join(' ') }}</code>
          <template v-if="draft.trim()"><br />输入框里的内容将作为新终端的首条消息。</template>
        </span>
        <span class="takeover-acts">
          <button class="act act--primary mono" type="button" :disabled="waking" @click="confirmWake">
            {{ waking ? '起进程中…' : `确认${session ? resumeLabel(session) : '唤醒'}` }}
          </button>
          <button class="act mono" type="button" :disabled="waking" @click="wakeOpen = false">取消</button>
        </span>
      </div>

      <div ref="timelineEl" class="timeline" @scroll="onTimelineScroll">
        <div v-if="conversationTimeline.length > 0" class="older-page mono">
          <button v-if="hasMore || messagesHasMore" type="button" class="link-btn" :disabled="loadingMore" @click="loadMore">
            {{ loadingMore ? '加载更早…' : '加载更早的 10 轮' }}
          </button>
          <span v-else>已到最早</span>
        </div>
        <div v-if="!loading && conversationTimeline.length === 0" class="empty mono">
          暂无 turn。打开中继后，会话下一次停下时消息会出现在这里。
        </div>
        <template v-for="entry in conversationTimeline" :key="entry.kind === 'turn' ? entry.turn.id : entry.message.id">
          <div v-if="entry.kind === 'message'" class="turn">
            <div class="bubble bubble--human relay-message">
              <div class="bubble-meta mono">
                <span>你（经转达）</span>
                <span :title="fmtDateTime(entry.message.created_at)">{{ fmtAgo(entry.message.created_at, nowSec) }}</span>
              </div>
              <pre class="bubble-text">{{ entry.message.text }}</pre>
              <div class="relay-message-status mono" :class="`outbox-status--${entry.message.status}`">
                <span>{{ messageStatusLabel(entry.message) }}</span>
                <button
                  v-if="entry.message.status === 'failed'"
                  class="link-btn mono"
                  type="button"
                  :disabled="retryingMessage === entry.message.id"
                  @click="retryMessage(entry.message)"
                >{{ retryingMessage === entry.message.id ? '重试中…' : '重试' }}</button>
              </div>
            </div>
          </div>
          <div v-else-if="entry.turn.kind === 'permission'" class="turn">
            <SessionPermissionPrompt :sid="sid" :decision="entry.turn" :now-sec="nowSec" @answered="onPermissionAnswered" />
          </div>
          <div v-else class="turn">
            <div class="bubble bubble--agent">
              <div class="bubble-meta mono">
                <span>{{ session?.agent || 'agent' }}</span>
                <span :title="fmtDateTime(entry.turn.asked_at)">{{ fmtAgo(entry.turn.asked_at, nowSec) }}</span>
              </div>
              <div
                v-if="entry.turn.question"
                class="bubble-md"
                :class="{ clamped: isLong(entry.turn.question) && !expanded.has(entry.turn.id) }"
                v-html="renderMd(entry.turn.question)"
              ></div>
              <pre v-else class="bubble-text">（无消息）</pre>
              <button
                v-if="isLong(entry.turn.question)"
                class="link-btn mono"
                type="button"
                @click="toggleExpand(entry.turn.id)"
              >
                {{ expanded.has(entry.turn.id) ? '收起' : `展开全文（${entry.turn.question.length} 字）` }}
              </button>
            </div>
            <div v-if="entry.turn.state === 'ANSWERED'" class="bubble bubble--human">
              <div class="bubble-meta mono">
                <span>{{ entry.turn.answered_by || 'web' }}</span>
                <span :title="fmtDateTime(entry.turn.answered_at)">{{ fmtAgo(entry.turn.answered_at, nowSec) }}</span>
              </div>
              <pre class="bubble-text">{{ entry.turn.answer }}</pre>
            </div>
            <div v-else-if="entry.turn.state === 'EXPIRED'" class="bubble bubble--expired mono">
              {{ entry.turn.released_by === 'user_returned' ? '人回到键盘，等待已自动放行' : '已过期 / 未回复' }}
            </div>
            <div v-else-if="entry.turn.acked_at" class="bubble bubble--acked mono">
              <span>已读 · 无需回复（仍可回复）</span>
              <button class="link-btn ack-btn mono" type="button" :disabled="ackBusy === entry.turn.id" @click="toggleAck(entry.turn)">
                {{ ackBusy === entry.turn.id ? '处理中…' : '撤销' }}
              </button>
            </div>
            <div v-else class="bubble bubble--pending mono">
              <span>等待回复…</span>
              <button class="link-btn ack-btn mono" type="button" :disabled="ackBusy === entry.turn.id" @click="toggleAck(entry.turn)">
                {{ ackBusy === entry.turn.id ? '处理中…' : '无需回复' }}
              </button>
            </div>
          </div>
        </template>
        <section v-if="session?.last_message && showLastMessage" class="last-message-fixed">
          <div class="last-message-head mono">
            <strong>最后一条消息</strong>
            <span v-if="session.wait_reason_detail?.match(/^supervising (\d+) jobs$/)" class="last-message-released" :title="'已放行：正在监督 ' + session.wait_reason_detail.match(/^supervising (\d+) jobs$/)?.[1] + ' 个 job'">
              已放行：正在监督 {{ session.wait_reason_detail.match(/^supervising (\d+) jobs$/)?.[1] }} 个 job
            </span>
            <span class="last-message-preview mono" :title="session.last_message">{{ session.last_message }}</span>
            <button class="link-btn mono" type="button" @click="lastMessageOpen = !lastMessageOpen">
              {{ lastMessageOpen ? '收起' : '展开' }}
            </button>
            <button v-if="lastMessageOpen" class="link-btn mono" type="button" @click="copyLastMessage">
              {{ copiedLast ? '已复制' : '复制全文' }}
            </button>
            <button class="link-btn mono" type="button" @click="openMessageHistory">历史消息 ↗</button>
          </div>
          <div
            v-if="lastMessageOpen"
            class="last-message-text bubble-md"
            v-html="renderMd(session.last_message)"
          ></div>
        </section>
      </div>

      <section v-if="session?.state === 'running' && session.progress_text" class="session-progress">
        <div class="session-progress-head mono">
          <strong>进行中</strong>
          <span>· {{ fmtAgo(session.progress_at || session.last_seen_at, nowSec) }}</span>
          <button class="link-btn mono" type="button" @click="progressOpen = !progressOpen">
            {{ progressOpen ? '收起' : '展开' }}
          </button>
        </div>
        <p class="session-progress-preview mono" :title="session.progress_text">{{ session.progress_text }}</p>
        <div v-if="progressOpen" class="session-progress-full bubble-md" v-html="renderMd(session.progress_text)"></div>
      </section>

      <SessionNudges v-if="session && sid" :sid="sid" :state="session.state" />

      <div class="composer">
        <p v-if="actionError" class="error mono">{{ actionError }}</p>
        <p v-else-if="actionInfo" class="receipt mono">{{ actionInfo }}</p>
        <!-- 已接管（§9.1 B）：对话在 pty job 里继续，这里只提供跳转与解除。 -->
        <div v-if="handedOffJob" class="takeover-bar mono">
          <span class="takeover-text">已接管 → </span>
          <RouterLink
            class="takeover-link"
            :to="`/jobs/${encodeURIComponent(handedOffJob)}?attach=1`"
          >
            job {{ handedOffJob }}（打开终端）
          </RouterLink>
          <button
            class="act mono"
            type="button"
            :disabled="releasing"
            title="cancel 接管 job，把会话交还原终端"
            @click="releaseTakeover"
          >
            {{ releasing ? '解除中…' : '解除接管' }}
          </button>
        </div>
        <!-- 二次确认（§9.1 B）：起新进程会把会话从原终端移走，必须先说清楚。 -->
        <div v-if="takeoverConfirm" class="takeover-confirm mono">
          <span class="takeover-text">{{ takeoverHint }}</span>
          <span class="takeover-acts">
            <button class="act act--primary mono" type="button" :disabled="sending" @click="takeOver">
              {{ sending ? '接管中…' : '确认接管并发送' }}
            </button>
            <button class="act mono" type="button" :disabled="sending" @click="takeoverConfirm = false">
              取消
            </button>
          </span>
        </div>
        <textarea
          v-model="draft"
          class="composer-input mono"
          rows="1"
          :disabled="!canSend"
          :placeholder="
            session?.state === 'ended'
              ? '会话已结束：点上方「唤醒」在新终端里继续'
              : session?.state === 'handed_off'
                ? '会话已被 web 接管：到接管终端里继续，或先解除接管'
                : openTurn
                  ? '回复 agent…（Ctrl/Cmd+Enter 发送；输入 /off 关闭中继，让会话正常停下）'
                  : session
                    ? '下一条消息'
                    : '发送给会话…（Ctrl/Cmd+Enter 发送；忙或空闲时经会话间消息转达）'
          "
          @keydown="onKeydown"
          @input="autoGrow"
        ></textarea>
        <div class="composer-foot mono">
          <span class="hint">
            <template v-if="openTurn">回复将原样进入 agent 上下文；输入 <code>/off</code> 关闭中继并让会话正常停下。</template>
            <template v-else-if="session?.state === 'ended'">
              会话已结束，不能再发消息；点上方「唤醒」可以在新终端里继续它。
            </template>
            <template v-else-if="session?.state === 'handed_off'">
              本会话已被 web 用 <code>--resume</code> 起的新进程接管；原终端不再中继，要恢复请解除接管。
            </template>
            <template v-else-if="takeoverOffered">
              送不进终端（{{ session?.tmux_pane ? 'pane 已失效' : '未登记 tmux pane' }}），可起新进程接管。
            </template>
            <template v-else-if="toTerminal">
              会话没有在等回复：消息由一次性传话人经会话间消息原样转达；对方不会把它当作你的审批。
            </template>
            <template v-else>会话未在等待回复。</template>
          </span>
          <button
            v-if="takeoverOffered && !takeoverConfirm"
            class="act mono"
            type="button"
            :disabled="!draft.trim() || sending"
            title="用 --resume 起一个新进程接管该会话，把这条消息作为它的首条输入"
            @click="takeoverConfirm = true"
          >
            起新进程接管并发送
          </button>
          <button
            class="act act--primary mono"
            type="button"
            :disabled="!canSend || !draft.trim() || takeoverConfirm"
            @click="send"
          >
            {{ sending ? '发送中…' : toTerminal ? '送到会话' : '发送' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 80; /* above EscalationBell (60) and InteractionToast (70): the composer must stay clickable */
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.6);
}
.drawer-panel {
  display: flex;
  flex-direction: column;
  width: min(760px, 94vw);
  height: 100%;
  background: var(--panel);
  border-left: 1px solid var(--line);
  overflow: hidden;
}
.drawer-overlay--embedded {
  position: static;
  inset: auto;
  z-index: auto;
  display: block;
  height: 100%;
  background: transparent;
}
.drawer-overlay--embedded .drawer-panel {
  width: 100%;
  max-width: none;
  border-left: 0;
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
.head-main {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  flex: 1 1 auto;
}
.drawer-title {
  min-width: 0;
  color: var(--paper);
  font-size: 12px;
  letter-spacing: 0.04em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}
.state-badge {
  flex: none;
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 1px 7px;
  font-size: 10px;
  color: var(--queue);
}
.state--running {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.state--waiting_reply {
  color: var(--run);
  border-color: var(--run);
}
.state--needs_attention {
  color: var(--fail);
  border-color: var(--fail);
}
.state--ended {
  opacity: 0.6;
}
/* 离线：长时间没有心跳，进程可能已被杀掉（不是正常结束）——灰色 */
.state--offline {
  color: var(--queue);
  border-color: var(--queue);
  opacity: 0.8;
}

/* 中继三态开关：auto / on / off */
.relay-demoted {
  margin: 0;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--run);
  border-left: 2px solid var(--run);
}

.relay-modes {
  display: inline-flex;
  align-items: center;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.relay-mode {
  all: unset;
  padding: 2px 8px;
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
.relay-mode.active:first-child {
  background: rgba(224, 162, 74, 0.18);
  color: var(--run);
}
.relay-mode.active:nth-child(2) {
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

.act {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
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
  border-color: var(--phosphor);
}
.act--primary:hover:not(:disabled) {
  background: var(--phosphor);
  color: var(--ink);
}
.act--warn:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 6px 10px;
  margin: 10px 14px 0;
  word-break: break-word;
}

.meta-wrap {
  flex: none;
  border-bottom: 1px solid var(--line);
}
.meta-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 14px;
  background: transparent;
  border: none;
  color: var(--paper);
  font-size: 11px;
  text-align: left;
  cursor: pointer;
}
.meta-toggle:hover {
  background: var(--hover, rgba(127, 127, 127, 0.08));
}
.meta-toggle .caret {
  flex: none;
  color: var(--queue);
}
.meta-summary {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.meta-summary .dim {
  color: var(--queue);
}
.meta-hint {
  flex: none;
  margin-left: auto;
  color: var(--queue);
}
.meta {
  flex: none;
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr) 84px minmax(0, 1fr);
  gap: 4px 10px;
  margin: 0;
  padding: 2px 14px 10px;
  font-size: 11px;
}
.meta dt {
  color: var(--queue);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.meta dd {
  margin: 0;
  color: var(--paper);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta-sid {
  display: flex;
  align-items: center;
  gap: 8px;
  grid-column: 2 / span 3;
}
.meta-sid span {
  overflow: hidden;
  text-overflow: ellipsis;
}
.meta-path {
  grid-column: 2 / span 3;
}
.copy-btn {
  flex: none;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 6px;
  font-size: 10px;
  cursor: pointer;
}
.copy-btn:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.dim {
  color: var(--queue);
}

.timeline {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.older-page {
  flex: none;
  text-align: center;
  color: var(--queue);
  font-size: 11px;
  min-height: 18px;
}
/* 对话流的最后一项：随消息一起滚动，不固定。折叠时只占一行。 */
.last-message-fixed {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  color: var(--paper);
}
.last-message-preview {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--queue);
}
.last-message-head {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: 10px;
  font-size: 11px;
  min-width: 0;
}
.last-message-head > strong,
.last-message-head .link-btn,
.last-message-released { flex: none; white-space: nowrap; }
.last-message-head .link-btn { padding: 0; }
@media (max-width: 640px) {
  .last-message-head { gap: 8px; }
  /* 手机：放行说明缩成标记，完整说明在 title 里 */
  .last-message-released { font-size: 0; }
  .last-message-released::before { content: '已放行'; font-size: 11px; color: var(--queue); }
}
.last-message-text {
  max-height: 45vh;
  overflow: auto;
  margin: 8px 0 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.55;
}
.session-progress {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  color: var(--paper);
}
.session-progress-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: var(--run);
}
.session-progress-head .link-btn {
  margin-left: auto;
  padding: 0;
}
.session-progress-preview {
  margin: 6px 0 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--queue);
}
.session-progress-full {
  max-height: 35vh;
  overflow: auto;
  margin-top: 8px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.outbox-panel {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
}
.outbox-row {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr) 72px minmax(0, 1.2fr);
  gap: 8px;
  align-items: center;
  margin-top: 6px;
  font-size: 11px;
}
.outbox-text, .outbox-error { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.outbox-status--delivered { color: var(--done); }
.outbox-status--queued { color: var(--run); }
.outbox-status--failed, .outbox-error { color: var(--fail); }
.relay-message-status { display: flex; align-items: center; gap: 10px; margin-top: 6px; font-size: 11px; }
.turn {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bubble {
  max-width: 88%;
  border-radius: 8px;
  padding: 8px 11px;
  font-size: 13px;
  line-height: 1.5;
}
.bubble--agent {
  align-self: flex-start;
  background: var(--ink);
  border: 1px solid var(--line);
  color: var(--paper);
}
.bubble--human {
  align-self: flex-end;
  background: rgba(79, 176, 198, 0.16);
  border: 1px solid var(--phosphor);
  color: var(--paper);
}
.bubble--expired,
.bubble--pending,
.bubble--acked {
  align-self: flex-end;
  font-size: 11px;
  color: var(--queue);
  border: 1px dashed var(--line);
}
.bubble--pending,
.bubble--acked {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.bubble--acked {
  color: var(--queue);
  border-style: solid;
}
.bubble--pending {
  color: var(--run);
  border-color: var(--run);
}
.bubble-meta {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 10px;
  color: var(--queue);
  margin-bottom: 4px;
}
.bubble-text {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
/* markdown 渲染的 agent 消息：紧凑排版，折叠时用高度裁剪 + 渐隐，
   避免截断 markdown 源码破坏语法。 */
.bubble-md {
  font-size: 12px;
  line-height: 1.55;
  word-break: break-word;
}
.bubble-md.clamped {
  max-height: 320px;
  overflow: hidden;
  -webkit-mask-image: linear-gradient(180deg, #000 78%, transparent 100%);
  mask-image: linear-gradient(180deg, #000 78%, transparent 100%);
}
.bubble-md :first-child {
  margin-top: 0;
}
.bubble-md :last-child {
  margin-bottom: 0;
}
.bubble-md p,
.bubble-md ul,
.bubble-md ol,
.bubble-md blockquote,
.bubble-md table {
  margin: 0 0 8px;
}
.bubble-md ul,
.bubble-md ol {
  padding-left: 20px;
}
.bubble-md li {
  margin: 2px 0;
}
.bubble-md h1,
.bubble-md h2,
.bubble-md h3,
.bubble-md h4 {
  margin: 10px 0 6px;
  font-size: 13px;
  font-weight: 600;
}
.bubble-md code {
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--hover, rgba(127, 127, 127, 0.14));
}
.bubble-md pre {
  margin: 0 0 8px;
  padding: 8px 10px;
  overflow-x: auto;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--hover, rgba(127, 127, 127, 0.08));
}
.bubble-md pre code {
  padding: 0;
  background: transparent;
}
.bubble-md blockquote {
  padding-left: 10px;
  border-left: 2px solid var(--line);
  color: var(--queue);
}
.bubble-md a {
  color: var(--phosphor);
}
.bubble-md table {
  border-collapse: collapse;
  display: block;
  overflow-x: auto;
}
.bubble-md th,
.bubble-md td {
  border: 1px solid var(--line);
  padding: 3px 7px;
  font-size: 11px;
}
.bubble-md hr {
  border: none;
  border-top: 1px solid var(--line);
  margin: 10px 0;
}
.bubble-md img {
  max-width: 100%;
}
.link-btn {
  background: transparent;
  border: none;
  color: var(--phosphor);
  padding: 4px 0 0;
  font-size: 11px;
  cursor: pointer;
}
.link-btn:hover {
  text-decoration: underline;
}
.empty {
  color: var(--queue);
  font-size: 12px;
  text-align: center;
  padding: 28px 10px;
}

.composer {
  flex: none;
  border-top: 1px solid var(--line);
  padding: 10px 14px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.composer .error {
  margin: 0;
}
/* 成功回执（已送入终端 ✓）：绿色一边，和错误同位置同尺寸，避免布局跳动 */
.composer .receipt {
  color: var(--done);
  font-size: 12px;
  border: 1px solid currentcolor;
  border-radius: var(--radius);
  padding: 6px 10px;
  margin: 0;
  word-break: break-word;
}
.composer-input {
  width: 100%;
  box-sizing: border-box;
  resize: none;
  min-height: 34px;
  max-height: 40vh;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.5;
}

/* 已接管提示条（§9.1 B）：一行说清会话去哪了，并给出跳转与解除 */
.takeover-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--run);
}
.takeover-bar .takeover-link {
  color: var(--phosphor);
  text-decoration: none;
  border-bottom: 1px dotted currentcolor;
}
/* 二次确认块：与提示条同位置，确认前不发送 */
.wake-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 10px;
  padding: 6px 14px;
  font-size: 11px;
  border-bottom: 1px solid var(--line);
  color: var(--queue);
}
.wake-confirm { margin: 6px 14px; }
.wake-why { color: var(--run); flex-basis: 100%; word-break: break-word; }
.wake-cmd { display: block; margin-top: 4px; color: var(--phosphor); word-break: break-all; }
.takeover-confirm {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--run);
  border: 1px solid currentcolor;
  border-radius: var(--radius);
  padding: 6px 10px;
}
.takeover-confirm .takeover-acts {
  display: inline-flex;
  gap: 6px;
}
.composer-input:focus {
  outline: none;
  border-color: var(--phosphor);
}
.composer-input:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.composer-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.hint {
  color: var(--queue);
  font-size: 11px;
  line-height: 1.4;
}
.hint code {
  color: var(--phosphor);
}

@media (max-width: 720px) {
  .drawer-overlay--embedded .composer { padding: 6px 8px 8px; gap: 4px; }
  .drawer-overlay--embedded .composer-input { min-height: 34px; padding: 6px 8px; }
  .drawer-overlay--embedded .composer-foot { gap: 6px; }
  .drawer-overlay--embedded .hint { flex: 1; min-width: 0; max-height: 18px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 10px; }
  .drawer-overlay--embedded .composer-foot .act { flex: none; padding: 4px 8px; }
  .meta {
    grid-template-columns: 76px minmax(0, 1fr);
  }
  .meta-sid,
  .meta-path {
    grid-column: 2;
  }
  .head-actions .relay-modes {
    display: none;
  }
}
/* 「无需回复 / 撤销」做成有边框的小按钮，与气泡里的纯文本区分开 */
.ack-btn {
  margin-left: 8px;
  padding: 2px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  color: var(--phosphor);
  font-size: 11px;
  line-height: 1.6;
}
.ack-btn:hover:not(:disabled) { border-color: var(--phosphor); }
</style>
