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
import { heldLabel, INJECT_MODES, injectModeLabel, injectModeTitle } from '../utils/injectMode'
import SessionNudges from './SessionNudges.vue'
import Icon from '../ui/Icon.vue'
import { ChevronDown, ChevronRight } from '@lucide/vue'
import SessionTopic from './SessionTopic.vue'
import SessionGrantDialog from './SessionGrantDialog.vue'
import GrantRenewDialog from './GrantRenewDialog.vue'
import { revokeAuthGrant } from '../api/permissions'
import { refreshSessionGrants, useSessionGrants } from '../store/sessionGrants'
import { useWhoami } from '../store/whoami'
import { badgeTitle, canGrantSession, sessionGrantBadge } from '../utils/sessionGrant'
import { badgeRenewIds, canRenewBadge, grantRemainingText } from '../utils/grantRenew'
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
  listMail,
  setSessionMailPaused,
  listSessionMessages,
  releaseSessionTakeover,
  resumeSession,
  saySession,
  sendSessionMessage,
  sendHeldSessionMessages,
  setSessionInjectMode,
  setSessionRelay,
  unackSessionTurn,
} from '../api/client'
import { turnWorkbenchThread } from '../api/workbench'
import { unsupportedOutsideThreadReason, type AttachmentCapability } from '../utils/attachments'
import { attachmentBubbleText } from '../utils/attachmentText'
import Composer from '../ui/Composer.vue'
import Drawer from '../ui/Drawer.vue'
import AttachmentThumbs from './AttachmentThumbs.vue'
import { topicDisplay } from '../utils/sessionTopic'
import { t } from '../i18n'
import { mobileHeaderFallback, sessionHeaderSummary } from '../utils/mobileLayout'
import { fmtAgo, fmtDateTime } from '../api/time'
import { canRetrySessionMessage, copyText, isSessionReply, mergeSessionTimeline, peerNameLabel, sessionMessageAuthorLabel, sessionMessageStatusText, sessionReplyLabel, shouldShowLastMessage, upsertSessionMessage } from '../utils/sessionMessaging'
import { resumeConfirmText, resumeFailText, resumeLabel, resumeTitle } from '../utils/sessionResume'
import { refusedOwnerHelp, refusedOwnerNotice, transferFailText, transferOwnerLabel, transferSessionOwner } from '../utils/sessionOwner'
import { fromSessionChoice, fromSessionQuery, type FromSessionSource } from '../utils/fromSession'
import { getAgentsCached } from '../api/metaCache'
import { mergeNewestPage, mergeOlderPage, preserveScrollAfterPrepend, shouldFollowBottom } from '../utils/sessionPagination'
import type {
  AgentInfo,
  AgentSession,
  AgentSessionInjectMode,
  AgentSessionRelayMode,
  AgentSessionState,
  Decision,
  SessionMessage,
  SessionResumePlan,
} from '../api/types'

const props = withDefaults(defineProps<{
  sid: string
  embedded?: boolean
  threadId?: string
  expandLastMessage?: boolean
  // 附图能力位（gofer-ha0x）：工作台里由 ThreadView 按 thread 的能力位传入，单独用时一律置灰。
  attachmentCapability?: AttachmentCapability
}>(), {
  embedded: false,
  threadId: '',
  expandLastMessage: false,
  attachmentCapability: () => ({ mode: 'none', enabled: false, reason: unsupportedOutsideThreadReason() }),
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
// 与该会话相关的信件数（收件 + 发件），meta 里给一个去信箱的入口（/mail?session=<sid>）。
const mailCount = ref(0)
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
// 归属被拒（gofer-eh53）：切 token 后 hook 以另一个 caller 身份上报被拒，会话停更。
// 管理员在这里把会话转给 hook 现在用的那个 caller（需要 can_admin），心跳就重新落下。
const ownerBusy = ref(false)
const ownerError = ref('')

async function transferSessionOwnerToRefused(): Promise<void> {
  const current = session.value
  const target = current?.refused_caller_id ?? ''
  if (!current || !target || ownerBusy.value) return
  ownerBusy.value = true
  ownerError.value = ''
  try {
    const updated = await transferSessionOwner(current.session_id, target)
    session.value = updated
    actionInfo.value = t('sessions.drawer.transferred', { id: updated.caller_id ?? '' })
    actionError.value = ''
    emit('changed')
  } catch (e) {
    ownerError.value = transferFailText(e)
  } finally {
    ownerBusy.value = false
  }
}
// takeover* 是路径 B 的二次确认（§9.1 B）：deliver 报 no_tmux / pane_missing 后，
// 输入框旁出现"{{ t('sessions.drawer.takeoverAndSend') }}"，点一次进入确认态，确认后才真的起进程。
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
// 会话授权（根管理员）：「授权」按钮、「已授权」徽标与条上的撤销。
const { mode: whoMode } = useWhoami()
const { grants: sessionGrantRows } = useSessionGrants()
const grantOpen = ref(false)
// 续期：徽标上「已授权」的条在剩余不足 1 小时时给出剩余时间与续期入口（同一个弹窗组件）。
const grantRenewOpen = ref(false)
const grantRevoking = ref(false)
const grantError = ref('')
const grantBadge = computed(() =>
  session.value ? sessionGrantBadge(sessionGrantRows.value, session.value, nowSec.value) : sessionGrantBadge([], { session_id: props.sid }, nowSec.value),
)
watch(() => [whoMode.value.canManage, props.sid], () => { if (whoMode.value.canManage) void refreshSessionGrants() }, { immediate: true })

async function revokeSessionGrants(): Promise<void> {
  if (grantRevoking.value) return
  grantRevoking.value = true
  grantError.value = ''
  try {
    for (const g of grantBadge.value.grants) await revokeAuthGrant(g.id)
  } catch (e) {
    grantError.value = e instanceof Error ? e.message : String(e)
  } finally {
    grantRevoking.value = false
    await refreshSessionGrants()
  }
}
const copied = ref(false)
const copiedLast = ref(false)
const copiedName = ref(false)
const lastMessageOpen = ref(props.expandLastMessage)
const progressOpen = ref(false)
const expanded = ref<Set<string>>(new Set())
// 手机档（≤900px）的头部折叠与输入框说明折叠：桌面完全不受影响（折叠条 / 「?」默认不显示）。
// 默认都收回：手机上一整块头部（主题 / 范围 / 接管 / 中继说明）能占掉半屏。
const headerOpen = ref(false)
const hintOpen = ref(false)
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
  get running() { return t('util.session.state.running') },
  get idle() { return t('util.session.state.idle') },
  get waiting_reply() { return t('util.session.state.waiting_reply') },
  get needs_attention() { return t('util.session.state.needs_attention') },
  get handed_off() { return t('util.session.state.handed_off') },
  get ended() { return t('util.session.state.ended') },
  get offline() { return t('util.session.state.offline') },
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
// 附图条：只有工作台里的会话（有 threadId）能带附件，可用性只看 thread 的能力位——
// 工作台 composer 也是这么量的。agent 没在等回复也能收图：后端走 SendMessage / 传话人 / 送达阶梯。
const attachCapability = computed<AttachmentCapability>(() => {
  if (!props.threadId) {
    return { mode: 'none', enabled: false, reason: unsupportedOutsideThreadReason() }
  }
  return props.attachmentCapability
})
// 附图条随统一的 Composer（P4）：附件、提示与发送门禁都由它管，这里只取就绪 id。
const composerRef = ref<InstanceType<typeof Composer> | null>(null)
// 抽屉外壳换成 ui/Drawer（P2）：宽度沿用会话抽屉的规格；关闭即发 close。
const drawerOpen = computed({
  get: () => true,
  set: (value: boolean) => {
    if (!value && !props.embedded) emit('close')
  },
})
const drawerWidth = 'min(max(760px, 62vw), 1200px, 94vw)'
// showWake：已被接管的会话由下方的接管条处理（跳转 / 解除），其余状态都给唤醒入口。
const showWake = computed(() => !!session.value && session.value.state !== 'handed_off')

// 「从此会话新开」（gofer-ldmp）：带 from_session 打开新建表单，新会话继承此会话上下文。
// 能力来自 /v1/agents（模块级缓存）；不可用时灰显并写原因。
const fsAgents = ref<AgentInfo[]>([])
void getAgentsCached()
  .then((r) => {
    fsAgents.value = r.agents ?? []
  })
  .catch(() => {})
const fromSessionSrc = computed<FromSessionSource>(() => ({
  agent: session.value?.agent ?? '',
  sessionId: session.value?.session_id,
  project: session.value?.project_key,
  runner: session.value?.runner,
  cwd: session.value?.cwd,
}))
const fromSessionPick = computed(() => fromSessionChoice(fromSessionSrc.value, fsAgents.value))
function openFromSession(): void {
  const c = fromSessionPick.value
  if (c.disabled) return
  void router.push({ path: '/new', query: fromSessionQuery(fromSessionSrc.value, c) })
}

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
    actionError.value = t('sessions.drawer.wakePlanFailed', { err: resumeFailText(e) })
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
    actionInfo.value = t('sessions.drawer.wakeStarted', { id: res.job_id ?? '' })
    await load({ silent: true })
    emit('changed')
    if (res.job_id) await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
  } catch (e) {
    actionError.value = t('sessions.drawer.wakeFailed', { err: resumeFailText(e) })
    await load({ silent: true })
  } finally {
    waking.value = false
  }
}

// toTerminal：这封消息走的是注入路径（没有 turn 在等），占位与回执据此切换。
const toTerminal = computed(() => !openTurn.value)

// 统一的输入框（P4 Composer）：占位文字与说明跟会话状态走。
const composerPlaceholder = computed(() =>
  session.value?.state === 'ended'
    ? t('sessions.drawer.ph.ended')
    : session.value?.state === 'handed_off'
      ? t('sessions.drawer.ph.handedOff')
      : openTurn.value
        ? t('sessions.drawer.ph.reply')
        : session.value
          ? t('sessions.drawer.ph.next')
          : t('sessions.drawer.ph.send'),
)
const composerHint = computed(() => {
  if (openTurn.value) return t('sessions.drawer.hint.reply')
  if (session.value?.state === 'ended') return t('sessions.drawer.hint.ended')
  if (session.value?.state === 'handed_off') return t('sessions.drawer.hint.handedOff')
  if (takeoverOffered.value) {
    return t('sessions.drawer.hint.cannotDeliver', { why: session.value?.tmux_pane ? t('sessions.drawer.hint.paneGone') : t('sessions.drawer.hint.noPane') })
  }
  if (toTerminal.value) return t('sessions.drawer.hint.notWaitingTerminal')
  return t('sessions.drawer.hint.notWaiting')
})
// handedOffJob：会话当前被哪个 pty job 接管（§9.1 B），空 = 没被接管。
const handedOffJob = computed(() => session.value?.handed_off_job_id ?? '')
// takeoverHint 是二次确认的文案（设计 §9.1 B 的原话）：说明要起什么进程、原终端会怎样。
const takeoverHint = computed(
  () =>
    t('sessions.drawer.takeoverConfirm', { agent: session.value?.agent ?? 'agent' }),
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

// headerSummary 是手机折叠条上的那一行：「主题 · 范围」（主题是推断值也照显示）。
const headerSummary = computed(() => {
  const s = session.value
  if (!s) return mobileHeaderFallback()
  return sessionHeaderSummary(topicDisplay(s).text, s.scope ?? [])
})

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
  return t('sessions.drawer.relayDemoted', { time: hhmm })
})

// RELAY_MODES 是三态开关的展示顺序（R1）。
const RELAY_MODES: AgentSessionRelayMode[] = ['auto', 'on', 'off']

// injectBusy 是投递模式 / 「现在送」请求在途（按钮禁用，防重复点）。
const injectBusy = ref(false)
// heldPending 是这个会话里等待投递的消息数（留待的消息由会话下一次 hook 注入）。
const heldPending = computed(() => session.value?.mail_pending ?? 0)

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
    return s.wait_reason_detail ? t('sessions.drawer.relay.notWaitingWith', { mode, why: s.wait_reason_detail }) : t('sessions.drawer.relay.notWaiting', { mode })
  }
  switch (s.wait_reason) {
    case 'mode_on':
      return t('sessions.drawer.relay.on')
    case 'idle_probe':
      return t('sessions.drawer.relay.idle', { idle: idleText(s.idle_sec) })
    case 'turn_age':
      return t('sessions.drawer.relay.noProbe', { silence: idleText(humanSilence(s)) })
    default:
      return t('sessions.drawer.relay.waiting', { mode })
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
  return sessionMessageStatusText(message)
}

async function retryMessage(message: SessionMessage): Promise<void> {
  if (retryingMessage.value || !canRetrySessionMessage(message.status)) return
  retryingMessage.value = message.id
  actionError.value = ''
  actionInfo.value = ''
  try {
    const next = await sendSessionMessage(props.sid, message.text)
    messages.value = upsertSessionMessage(messages.value, next)
    actionInfo.value = t('sessions.drawer.requeued')
    await load({ silent: true })
  } catch (e) {
    actionError.value = t('sessions.drawer.retryFailed', { err: errorMessage(e) })
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
    // 信件：尽力而为（旧服务端 / 无权读信时保持 0，入口照常显示）。
    try {
      const mail = await listMail({ session: props.sid, box: 'all', limit: 200 })
      mailCount.value = (mail.mail ?? []).length
    } catch {
      mailCount.value = 0
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
    error.value = t('sessions.drawer.popupBlocked')
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

async function toggleMailPause(): Promise<void> {
  const s = session.value
  if (!s || relayBusy.value) return
  relayBusy.value = true
  try { session.value = await setSessionMailPaused(s.session_id, !s.mail_paused); emit('changed') }
  catch (e) { actionError.value = errorMessage(e) }
  finally { relayBusy.value = false }
}

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
    actionError.value = t('sessions.drawer.relaySwitchFailed', { err: errorMessage(e) })
  } finally {
    relayBusy.value = false
  }
}

// 投递模式（2026-10-22 D5）：点击即 POST，乐观更新，失败回滚。
async function setInjectMode(mode: AgentSessionInjectMode): Promise<void> {
  const s = session.value
  if (!s || injectBusy.value || (s.inject_mode || 'auto') === mode) {
    return
  }
  const prev = s.inject_mode
  injectBusy.value = true
  actionError.value = ''
  actionInfo.value = ''
  s.inject_mode = mode
  try {
    session.value = await setSessionInjectMode(s.session_id, mode)
    emit('changed')
  } catch (e) {
    if (session.value) {
      session.value.inject_mode = prev
    }
    actionError.value = t('sessions.drawer.injectSwitchFailed', { err: errorMessage(e) })
  } finally {
    injectBusy.value = false
  }
}

// 现在送：把留待的消息立刻粘贴进终端（跳过输入区检查与投递模式）。
async function sendHeldNow(): Promise<void> {
  const s = session.value
  if (!s || injectBusy.value) {
    return
  }
  injectBusy.value = true
  actionError.value = ''
  actionInfo.value = ''
  try {
    await sendHeldSessionMessages(s.session_id)
    actionInfo.value = t('sessions.drawer.heldSent')
    await load()
    emit('changed')
  } catch (e) {
    actionError.value = t('sessions.drawer.sendNowFailed', { err: errorMessage(e) })
  } finally {
    injectBusy.value = false
  }
}

async function send(): Promise<void> {
  const text = draft.value.trim()
  if (!text || !canSend.value) {
    return
  }
  const attachmentIDs = composerRef.value?.readyIDs() ?? []
  if (attachmentIDs.length > 0 && !props.threadId) {
    // 送入终端 / 纯会话窗口没有附件字段：不静默丢图。
    actionError.value = t('sessions.drawer.attachUnsupported')
    return
  }
  sending.value = true
  actionError.value = ''
  actionInfo.value = ''
  takeoverOffered.value = false
  takeoverConfirm.value = false
  try {
    // 带图就必须走工作台 turn（唯一带 attachments 的入口）；agent 没在等回复时它照旧送达
    // （SendMessage / 传话人 / 送达阶梯），所以附件不再要求回合处于等待态。发送结果不再另外
    // 提示：时间线上的 outbox 气泡本身带「已送达」状态。
    if (props.threadId && (openTurn.value || attachmentIDs.length > 0)) {
      await turnWorkbenchThread(props.threadId, text, attachmentIDs)
    } else if (props.threadId) {
      await sendSessionMessage(props.sid, text)
    } else if (openTurn.value) {
      await saySession(props.sid, text)
    } else {
      await sendSessionMessage(props.sid, text)
    }
    draft.value = ''
    // 发送成功才清空附件条（失败时图还在，可直接重试）。
    composerRef.value?.clearAttachments()
    await load({ silent: true })
    scrollToBottom()
    emit('changed')
  } catch (e) {
    if (toTerminal.value && !props.threadId) {
      actionError.value = t('sessions.drawer.deliverFailed', { err: deliverErrorMessage(e) })
      // 消息还在草稿里：A 送不进去时会提示接管（§9.1 B），由用户二次确认后再发。
      takeoverOffered.value = takeoverAvailable(e)
    } else {
      actionError.value = t('sessions.drawer.sendFailed', { err: errorMessage(e) })
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
    actionError.value = t('sessions.drawer.markWaitingFailed', { err: errorMessage(e) })
  } finally {
    ackBusy.value = null
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
      // 送达结果不再单独提示（outbox 气泡自己带「已送达」状态）；只有起新进程接管这种
      // 换了地方继续的状态变化还需要一句。
      draft.value = ''
      await load({ silent: true })
      emit('changed')
      return
    }
    draft.value = ''
    actionInfo.value = t('sessions.drawer.takeoverStarted', { id: res.job_id })
    await load({ silent: true })
    emit('changed')
    await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
  } catch (e) {
    actionError.value = t('sessions.drawer.takeoverFailed', { err: deliverErrorMessage(e) })
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
    actionInfo.value = t('sessions.drawer.released')
    await load({ silent: true })
    emit('changed')
  } catch (e) {
    actionError.value = t('sessions.drawer.releaseFailed', { err: errorMessage(e) })
  } finally {
    releasing.value = false
  }
}

// deliverErrorMessage 把服务端的原因码翻成能照做的提示：no_tmux / pane_missing 提示
// 接管（§9.1 B），no_resume_template 等说清为什么接管也不可用。
function deliverErrorMessage(e: unknown): string {
  const code = e instanceof ApiError ? `${e.code ?? ''} ${e.detail ?? ''}` : String(e)
  if (code.includes('session_alive')) {
    return t('sessions.drawer.takeoverReason.stillOnline')
  }
  if (code.includes('deliver_failed')) {
    return t('sessions.drawer.takeoverReason.agentFailed', { err: e instanceof ApiError ? (e.detail ?? e.code ?? '') : String(e) })
  }
  if (code.includes('not_running')) {
    return t('sessions.drawer.takeoverReason.notRunning')
  }
  if (code.includes('no_tmux')) {
    return t('sessions.drawer.takeoverReason.notTmux')
  }
  if (code.includes('no_runner')) {
    return t('sessions.drawer.takeoverReason.noRunner')
  }
  if (code.includes('handed_off')) {
    return t('sessions.drawer.takeoverReason.alreadyHandedOff')
  }
  if (code.includes('no_resume_template')) {
    return t('sessions.drawer.takeoverReason.noResumeTemplate')
  }
  if (code.includes('interactive_not_allowed')) {
    return t('sessions.drawer.takeoverReason.noInteractive')
  }
  if (code.includes('cwd_outside_project')) {
    return t('sessions.drawer.takeoverReason.noRelPath')
  }
  if (code.includes('ended')) {
    return t('sessions.drawer.takeoverReason.ended')
  }
  return errorMessage(e)
}

async function remove(): Promise<void> {
  if (deleting.value) {
    return
  }
  if (!window.confirm(t('sessions.drawer.removeConfirm', { id: shortSid(props.sid) }))) {
    return
  }
  deleting.value = true
  actionError.value = ''
  try {
    await deleteAgentSession(props.sid)
    emit('deleted', props.sid)
    emit('close')
  } catch (e) {
    actionError.value = t('sessions.drawer.removeFailed', { err: errorMessage(e) })
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
  <Drawer v-model="drawerOpen" :label="titleText" :embedded="embedded" flush :width="drawerWidth">
    <div
      class="drawer-panel"
      :class="{ 'drawer-panel--head-open': headerOpen, 'drawer-panel--hint-open': hintOpen, 'drawer-panel--embedded': embedded }"
      :role="embedded ? 'region' : undefined"
      :aria-label="t('sessions.drawer.aria')"
    >
      <div v-if="!embedded" class="drawer-head">
        <div v-if="session" class="head-main">
          <span
            class="state-badge mono"
            :class="`state--${session.state}`"
          >
            {{ stateLabel(session.state) }}
          </span>
        </div>
        <div class="head-actions mono">
          <button class="relay-mode" type="button" :disabled="relayBusy" @click="toggleMailPause">
            {{ session?.mail_paused ? t('sessions.drawer.mailResume') : t('sessions.drawer.mailPause') }}
          </button>
          <span
            v-if="session"
            class="relay-modes mono"
            :title="t('sessions.drawer.relayTitle')"
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
          <span
            v-if="session"
            class="relay-modes mono"
            :class="{ busy: injectBusy }"
            :title="t('sessions.drawer.injectTitle')"
          >
            <button
              v-for="m in INJECT_MODES"
              :key="m"
              type="button"
              class="relay-mode"
              :class="{ active: (session.inject_mode || 'auto') === m }"
              :disabled="injectBusy || session.state === 'ended'"
              :title="injectModeTitle(m)"
              @click="setInjectMode(m)"
            >
              {{ injectModeLabel(m) }}
            </button>
          </span>
          <button
            v-if="session && heldPending > 0"
            class="act mono"
            type="button"
            data-test="drawer-send-held"
            :disabled="injectBusy || session.state === 'ended'"
            :title="t('sessions.drawer.sendNowTitle')"
            @click="sendHeldNow()"
          >
            {{ heldLabel(heldPending) }}
          </button>
          <button
            v-if="canGrantSession(whoMode.canManage, session)"
            class="act mono"
            type="button"
            data-test="drawer-grant"
            :title="t('sessions.drawer.grantTitle')"
            @click="grantOpen = true"
          >
            {{ t('sessions.drawer.grant') }}
          </button>
          <button class="act mono" type="button" :disabled="loading" @click="load()">
            {{ loading ? t('sessions.drawer.refreshing') : t('sessions.drawer.refresh') }}
          </button>
          <button
            v-if="!embedded"
            class="act act--warn mono"
            type="button"
            :disabled="deleting"
            :title="t('sessions.drawer.removeTitle')"
            @click="remove"
          >
            {{ deleting ? t('sessions.drawer.removing') : t('sessions.drawer.remove') }}
          </button>
        </div>
      </div>

      <p v-if="error" class="error mono">{{ error }}</p>
      <p v-if="relayDemotedText" class="relay-demoted mono head-collapsible" data-test="relay-demoted">{{ relayDemotedText }}</p>
      <div v-if="whoMode.canManage && grantBadge.granted" class="session-grant mono" data-test="session-grant-strip">
        <span class="session-grant-badge" data-test="session-granted-badge" :title="badgeTitle(grantBadge, fmtDateTime)">{{ t('sessions.drawer.granted') }}</span>
        <span class="session-grant-perms">{{ grantBadge.permissions.join('  ') }}</span>
        <span v-if="grantBadge.expiresAt" class="dim">{{ t('sessions.drawer.grantedUntil', { time: fmtDateTime(grantBadge.expiresAt) }) }}</span>
        <span v-if="canRenewBadge(whoMode.canManage, grantBadge, nowSec)" class="session-grant-remaining" data-test="session-grant-remaining">{{ grantRemainingText(grantBadge.expiresAt, nowSec) }}</span>
        <button
          v-if="canRenewBadge(whoMode.canManage, grantBadge, nowSec)"
          class="act mono"
          type="button"
          data-test="session-grant-renew"
          :title="t('sessions.drawer.renewTitle')"
          @click="grantRenewOpen = true"
        >{{ t('sessions.drawer.renew') }}</button>
        <button class="act act--warn mono" type="button" data-test="session-grant-revoke" :disabled="grantRevoking" @click="revokeSessionGrants">
          {{ grantRevoking ? t('sessions.drawer.revoking') : t('sessions.drawer.revoke') }}
        </button>
        <span v-if="grantError" class="owner-refused-err" role="alert">{{ grantError }}</span>
      </div>
      <SessionGrantDialog v-if="grantOpen" :sid="sid" :label="titleText" @close="grantOpen = false" @granted="refreshSessionGrants()" />
      <GrantRenewDialog
        v-if="grantRenewOpen"
        :ids="badgeRenewIds(grantBadge)"
        :label="titleText"
        :expires-at="grantBadge.expiresAt"
        @close="grantRenewOpen = false"
        @renewed="refreshSessionGrants()"
      />
      <div v-if="session?.refused_caller_id" class="owner-refused mono" data-test="refused-owner-banner">
        <span>{{ refusedOwnerNotice(session) }}</span>
        <button
          class="act act--warn mono"
          type="button"
          data-test="refused-owner-transfer"
          :disabled="ownerBusy"
          :title="refusedOwnerHelp(session)"
          @click="transferSessionOwnerToRefused"
        >{{ ownerBusy ? t('sessions.drawer.transferring') : transferOwnerLabel(session) }}</button>
        <span v-if="ownerError" class="owner-refused-err" data-test="refused-owner-error">{{ t('sessions.drawer.transferFailed', { err: ownerError }) }}</span>
      </div>

      <!-- 手机档（≤900px）：主题 / 范围 / 中继状态说明这几块收成一行「主题 · 范围」加展开箭头，
           点开才显示；接管 / 唤醒 / 从此会话新开这一组主操作不进折叠区，一直可见。
           宽屏这条折叠条不显示，头部照旧全展开。 -->
      <button
        v-if="session"
        class="head-summary mono"
        type="button"
        :aria-expanded="headerOpen"
        data-test="session-head-toggle"
        @click="headerOpen = !headerOpen"
      >
        <Icon class="caret" :icon="headerOpen ? ChevronDown : ChevronRight" size="sm" />
        <span class="head-summary-text" data-test="session-head-summary">{{ headerSummary }}</span>
      </button>

      <div v-if="session" class="topic-wrap head-collapsible" data-test="drawer-topic">
        <SessionTopic :session="session" @updated="(u) => { session = u; emit('changed') }" />
      </div>

      <div v-if="session && !embedded" class="meta-wrap">
        <button
          class="meta-toggle mono"
          type="button"
          :aria-expanded="metaOpen"
          @click="toggleMeta"
        >
          <Icon class="caret" :icon="metaOpen ? ChevronDown : ChevronRight" size="sm" />
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
          <span class="meta-hint">{{ metaOpen ? t('util.action.collapse') : t('util.action.expand') }}</span>
        </button>
      <dl v-show="metaOpen" class="meta mono">
        <dt>sid</dt>
        <dd class="meta-sid">
          <span :title="session.session_id">{{ session.session_id }}</span>
          <button class="copy-btn mono" type="button" @click="copySid">
            {{ copied ? t('util.action.copied') : t('util.action.copy') }}
          </button>
        </dd>
        <dt v-if="session.peer_name">{{ t('sessions.drawer.meta.name') }}</dt>
        <dd v-if="session.peer_name" class="meta-sid">
          <span data-test="peer-name-detail" :title="peerNameLabel(session)">{{ session.peer_name }}</span>
          <span v-if="session.peer_name_source" class="dim">（{{ session.peer_name_source }}）</span>
          <button class="copy-btn mono" type="button" @click="copyName">{{ copiedName ? t('util.action.copied') : t('util.action.copy') }}</button>
        </dd>
        <dt>agent</dt>
        <dd>{{ session.agent }}</dd>
        <dt>project</dt>
        <dd>{{ session.project_key || '—' }}</dd>
        <dt>runner</dt>
        <dd>{{ runnerLabel(session.runner) || '—' }}</dd>
        <dt>cwd</dt>
        <dd class="meta-path" :title="session.cwd">{{ session.cwd || '—' }}</dd>
        <dt v-if="session.last_cwd">{{ t('sessions.drawer.meta.cwd') }}</dt>
        <dd v-if="session.last_cwd" class="meta-path" :title="session.last_cwd">{{ session.last_cwd }}</dd>
        <template v-for="u in sessionUsageRows(session.usage)" :key="u.label">
          <dt>{{ u.label }}{{ t('sessions.drawer.meta.usage') }}</dt>
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
          <span v-if="session.idle_sec >= 0" class="dim">{{ t('sessions.drawer.meta.idle', { idle: idleText(session.idle_sec) }) }}</span>
          <span v-if="session.last_human_at" class="dim">{{ t('sessions.drawer.meta.lastHuman', { ago: fmtAgo(session.last_human_at, nowSec) }) }}</span>
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
        <dt>{{ t('sessions.drawer.meta.mail') }}</dt>
        <dd data-test="session-mail">
          <span v-if="mailCount">{{ t('sessions.drawer.meta.mailCount', { n: mailCount }) }}</span><span v-else class="dim">{{ t('sessions.drawer.meta.none') }}</span>
          <RouterLink class="mail-link" :to="`/mail?session=${encodeURIComponent(session.session_id)}`">{{ t('sessions.drawer.meta.toMail') }}</RouterLink>
        </dd>
      </dl>
      </div>

      <!-- 唤醒/接管：常驻，不依赖发送失败；已结束的会话也能唤醒。Workbench 嵌入同样显示。
           手机档这组主操作（唤醒 / 接管 / 从此会话新开）也不进折叠区 —— 它是手机上最常用的。 -->
      <div v-if="showWake" class="wake-bar mono" data-test="wake-bar">
        <span class="wake-text">{{ session?.state === 'ended' ? t('sessions.drawer.wake.ended') : session?.state === 'offline' ? t('sessions.drawer.wake.offline') : t('sessions.drawer.wake.prompt') }}</span>
        <button
          class="act act--primary mono"
          type="button"
          data-test="wake-btn"
          :disabled="!session?.can_resume || waking || wakePlanLoading"
          :title="session ? resumeTitle(session) : ''"
          @click="openWake"
        >{{ wakePlanLoading ? t('sessions.drawer.querying') : resumeLabel(session!) }}</button>
        <button
          v-if="fromSessionPick.visible"
          class="act mono"
          type="button"
          data-test="from-session-btn"
          :disabled="fromSessionPick.disabled"
          :title="fromSessionPick.note || t('sessions.drawer.fromSessionTitle')"
          @click="openFromSession"
        >{{ t('sessions.drawer.fromSession') }}</button>
        <span v-if="fromSessionPick.visible && fromSessionPick.note" class="wake-why" data-test="from-session-note">{{ fromSessionPick.note }}</span>
        <span v-if="session && !session.can_resume" class="wake-why" data-test="wake-why">{{ session.resume_message }}</span>
      </div>
      <div v-if="wakeOpen && wakePlan" class="takeover-confirm wake-confirm mono" data-test="wake-confirm">
        <span class="takeover-text">
          {{ session ? resumeConfirmText(session, wakePlan) : '' }}
          <code v-if="wakePlan.command?.length" class="wake-cmd">{{ wakePlan.command.join(' ') }}</code>
          <template v-if="draft.trim()"><br />{{ t('sessions.drawer.fromSessionDraft') }}</template>
        </span>
        <span class="takeover-acts">
          <button class="act act--primary mono" type="button" :disabled="waking" @click="confirmWake">
            {{ waking ? t('sessions.drawer.starting') : t('sessions.drawer.confirmWake', { label: session ? resumeLabel(session) : t('sessions.drawer.wake') }) }}
          </button>
          <button class="act mono" type="button" :disabled="waking" @click="wakeOpen = false">{{ t('util.action.cancel') }}</button>
        </span>
      </div>

      <div ref="timelineEl" class="timeline" @scroll="onTimelineScroll">
        <div v-if="conversationTimeline.length > 0" class="older-page mono">
          <button v-if="hasMore || messagesHasMore" type="button" class="link-btn" :disabled="loadingMore" @click="loadMore">
            {{ loadingMore ? t('sessions.drawer.loadingMore') : t('sessions.drawer.loadMore') }}
          </button>
          <span v-else>{{ t('sessions.drawer.noMore') }}</span>
        </div>
        <div v-if="!loading && conversationTimeline.length === 0" class="empty mono">
          {{ t('sessions.drawer.noTurns') }}
        </div>
        <template v-for="entry in conversationTimeline" :key="entry.kind === 'turn' ? entry.turn.id : entry.message.id">
          <div v-if="entry.kind === 'message' && isSessionReply(entry.message)" class="turn">
            <div class="bubble bubble--agent session-reply msg-body" data-test="session-reply">
              <div class="bubble-meta mono">
                <span>{{ sessionReplyLabel(entry.message, session) }}</span>
                <span :title="fmtDateTime(entry.message.created_at)">{{ fmtAgo(entry.message.created_at, nowSec) }}</span>
              </div>
              <div
                class="bubble-md msg-body"
                :class="{ clamped: isLong(entry.message.text) && !expanded.has(entry.message.id) }"
                v-html="renderMd(entry.message.text)"
              ></div>
              <button
                v-if="isLong(entry.message.text)"
                class="link-btn mono"
                type="button"
                @click="toggleExpand(entry.message.id)"
              >
                {{ expanded.has(entry.message.id) ? t('util.action.collapse') : t('sessions.drawer.expandFull', { n: entry.message.text.length }) }}
              </button>
            </div>
          </div>
          <div v-else-if="entry.kind === 'message'" class="turn">
            <div class="bubble bubble--human relay-message msg-body">
              <div class="bubble-meta mono">
                <span>{{ sessionMessageAuthorLabel(entry.message) }}</span>
                <span :title="fmtDateTime(entry.message.created_at)">{{ fmtAgo(entry.message.created_at, nowSec) }}</span>
              </div>
              <pre
                v-if="attachmentBubbleText(entry.message.text, entry.message.attachments)"
                class="bubble-text msg-body"
              >{{ attachmentBubbleText(entry.message.text, entry.message.attachments) }}</pre>
              <AttachmentThumbs v-if="entry.message.attachments?.length" :items="entry.message.attachments" />
              <div class="relay-message-status mono" :class="`outbox-status--${entry.message.status}`">
                <span>{{ messageStatusLabel(entry.message) }}</span>
                <button
                  v-if="canRetrySessionMessage(entry.message.status)"
                  class="link-btn mono"
                  type="button"
                  :disabled="retryingMessage === entry.message.id"
                  @click="retryMessage(entry.message)"
                >{{ retryingMessage === entry.message.id ? t('sessions.drawer.retrying') : t('sessions.drawer.retry') }}</button>
              </div>
            </div>
          </div>
          <div v-else-if="entry.turn.kind === 'permission'" class="turn">
            <SessionPermissionPrompt :sid="sid" :decision="entry.turn" :now-sec="nowSec" @answered="onPermissionAnswered" />
          </div>
          <div v-else class="turn">
            <div class="bubble bubble--agent msg-body">
              <div class="bubble-meta mono">
                <span>{{ session?.agent || 'agent' }}</span>
                <span :title="fmtDateTime(entry.turn.asked_at)">{{ fmtAgo(entry.turn.asked_at, nowSec) }}</span>
              </div>
              <div
                v-if="entry.turn.question"
                class="bubble-md msg-body"
                :class="{ clamped: isLong(entry.turn.question) && !expanded.has(entry.turn.id) }"
                v-html="renderMd(entry.turn.question)"
              ></div>
              <pre v-else class="bubble-text msg-body">{{ t('sessions.drawer.noMessage') }}</pre>
              <button
                v-if="isLong(entry.turn.question)"
                class="link-btn mono"
                type="button"
                @click="toggleExpand(entry.turn.id)"
              >
                {{ expanded.has(entry.turn.id) ? t('util.action.collapse') : t('sessions.drawer.expandFull', { n: entry.turn.question.length }) }}
              </button>
            </div>
            <div v-if="entry.turn.state === 'ANSWERED' && !entry.relayed" class="bubble bubble--human msg-body">
              <div class="bubble-meta mono">
                <span>{{ entry.turn.answered_by || 'web' }}</span>
                <span :title="fmtDateTime(entry.turn.answered_at)">{{ fmtAgo(entry.turn.answered_at, nowSec) }}</span>
              </div>
              <pre
                v-if="attachmentBubbleText(entry.turn.answer, entry.turn.attachments)"
                class="bubble-text msg-body"
              >{{ attachmentBubbleText(entry.turn.answer, entry.turn.attachments) }}</pre>
              <AttachmentThumbs v-if="entry.turn.attachments?.length" :items="entry.turn.attachments" />
            </div>
            <template v-else-if="entry.turn.state !== 'ANSWERED'">
              <div v-if="entry.turn.state === 'EXPIRED'" class="bubble bubble--expired mono">
                {{ entry.turn.released_by === 'user_returned' ? t('sessions.drawer.turn.releasedUser') : t('sessions.drawer.turn.releasedExpired') }}
              </div>
              <div v-else-if="entry.turn.acked_at" class="bubble bubble--acked mono">
                <span>{{ t('sessions.drawer.turn.acked') }}</span>
                <button class="link-btn ack-btn mono" type="button" :disabled="ackBusy === entry.turn.id" @click="toggleAck(entry.turn)">
                  {{ ackBusy === entry.turn.id ? t('sessions.drawer.working') : t('sessions.drawer.undo') }}
                </button>
              </div>
              <div v-else class="bubble bubble--pending mono">
                <span>{{ t('sessions.drawer.turn.waiting') }}</span>
                <button class="link-btn ack-btn mono" type="button" :disabled="ackBusy === entry.turn.id" @click="toggleAck(entry.turn)">
                  {{ ackBusy === entry.turn.id ? t('sessions.drawer.working') : t('sessions.drawer.turn.noReplyNeeded') }}
                </button>
              </div>
            </template>
          </div>
        </template>
        <section v-if="session?.last_message && showLastMessage" class="last-message-fixed">
          <div class="last-message-head mono">
            <strong>{{ t('sessions.drawer.lastMessage') }}</strong>
            <span v-if="session.wait_reason_detail?.match(/^supervising (\d+) jobs$/)" class="last-message-released" :title="t('sessions.drawer.releasedSupervising', { n: session.wait_reason_detail.match(/^supervising (\d+) jobs$/)?.[1] ?? 0 })">
              {{ t('sessions.drawer.releasedSupervising', { n: session.wait_reason_detail.match(/^supervising (\d+) jobs$/)?.[1] ?? 0 }) }}
            </span>
            <span v-if="session.released_by === 'subagent_done'" class="last-message-released" :title="t('sessions.drawer.releasedSubagent')">
              {{ t('sessions.drawer.releasedSubagent') }}
            </span>
            <span class="last-message-preview mono" :title="session.last_message">{{ session.last_message }}</span>
            <button class="link-btn mono" type="button" @click="lastMessageOpen = !lastMessageOpen">
              {{ lastMessageOpen ? t('util.action.collapse') : t('sessions.drawer.expand') }}
            </button>
            <button v-if="lastMessageOpen" class="link-btn mono" type="button" @click="copyLastMessage">
              {{ copiedLast ? t('util.action.copied') : t('sessions.drawer.copyFull') }}
            </button>
            <button class="link-btn mono" type="button" @click="openMessageHistory">{{ t('sessions.drawer.history') }}</button>
          </div>
          <div
            v-if="lastMessageOpen"
            class="last-message-text bubble-md msg-body"
            v-html="renderMd(session.last_message)"
          ></div>
        </section>
      </div>

      <section v-if="session?.state === 'running' && session.progress_text" class="session-progress">
        <div class="session-progress-head mono">
          <strong>{{ t('sessions.drawer.inProgress') }}</strong>
          <span>· {{ fmtAgo(session.progress_at || session.last_seen_at, nowSec) }}</span>
          <button class="link-btn mono" type="button" @click="progressOpen = !progressOpen">
            {{ progressOpen ? t('util.action.collapse') : t('sessions.drawer.expand') }}
          </button>
        </div>
        <p class="session-progress-preview mono" :title="session.progress_text">{{ session.progress_text }}</p>
        <div v-if="progressOpen" class="session-progress-full bubble-md msg-body" v-html="renderMd(session.progress_text)"></div>
      </section>

      <SessionNudges v-if="session && sid" :sid="sid" :state="session.state" />

      <div class="composer">
        <p v-if="actionError" class="error mono">{{ actionError }}</p>
        <p v-else-if="actionInfo" class="receipt mono">{{ actionInfo }}</p>
        <!-- 已接管（§9.1 B）：对话在 pty job 里继续，这里只提供跳转与解除。 -->
        <div v-if="handedOffJob" class="takeover-bar mono">
          <span class="takeover-text">{{ t('sessions.drawer.handedOffTo') }}</span>
          <RouterLink
            class="takeover-link"
            :to="`/jobs/${encodeURIComponent(handedOffJob)}?attach=1`"
          >
            {{ t('sessions.drawer.handedOffJob', { id: handedOffJob }) }}
          </RouterLink>
          <button
            class="act mono"
            type="button"
            :disabled="releasing"
            :title="t('sessions.drawer.releaseTitle')"
            @click="releaseTakeover"
          >
            {{ releasing ? t('sessions.drawer.releasing') : t('sessions.drawer.release') }}
          </button>
        </div>
        <!-- 二次确认（§9.1 B）：起新进程会把会话从原终端移走，必须先说清楚。 -->
        <div v-if="takeoverConfirm" class="takeover-confirm mono">
          <span class="takeover-text">{{ takeoverHint }}</span>
          <span class="takeover-acts">
            <button class="act act--primary mono" type="button" :disabled="sending" @click="takeOver">
              {{ sending ? t('sessions.drawer.takingOver') : t('sessions.drawer.confirmTakeover') }}
            </button>
            <button class="act mono" type="button" :disabled="sending" @click="takeoverConfirm = false">
              {{ t('util.action.cancel') }}
            </button>
          </span>
        </div>
        <Composer
          ref="composerRef"
          v-model="draft"
          :placeholder="composerPlaceholder"
          :disabled="!canSend"
          :sending="sending"
          :can-send="canSend && !takeoverConfirm"
          :send-label="toTerminal ? t('sessions.drawer.sendToSession') : t('sessions.drawer.send')"
          attachments
          :attach-capability="attachCapability"
          :hint="composerHint"
          hint-collapsible
          @send="send"
          @attachment-error="actionError = $event"
        >
          <template #actions>
            <button
              v-if="takeoverOffered && !takeoverConfirm"
              class="act mono"
              type="button"
              :disabled="!draft.trim() || sending"
              :title="t('sessions.drawer.takeoverAndSendTitle')"
              @click="takeoverConfirm = true"
            >
              {{ t('sessions.drawer.takeoverAndSend') }}
            </button>
          </template>
        </Composer>
      </div>
    </div>
  </Drawer>
</template>

<style scoped>
.topic-wrap { padding: 8px 14px; border-bottom: 1px solid var(--line); }
.drawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 80; /* above EscalationBell (60) and Toast corner (70): the composer must stay clickable */
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.6);
}
.drawer-panel {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  /* PC 上随屏宽放大（对话里的长消息 / 授权参数不再被省略），手机仍贴满。 */
  width: min(max(760px, 62vw), 1200px, 94vw);
  height: 100%;
  background: var(--surface);
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
.drawer-panel--embedded {
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
  color: var(--ink);
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
  color: var(--ink-3);
}
.state--running {
  color: var(--accent);
  border-color: var(--accent);
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
  color: var(--ink-3);
  border-color: var(--ink-3);
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

/* 归属被拒（gofer-eh53）：与「中继回退」同款的警示条，带一键转移的按钮 */
.session-grant {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 10px;
  margin: 0 0 8px;
  padding: 6px 10px;
  font-size: 12px;
  border: 1px dashed var(--accent);
  border-radius: var(--r-chip);
}
.session-grant-badge { color: var(--accent); border: 1px dashed var(--accent); border-radius: 3px; padding: 0 6px; }
.session-grant-remaining { color: var(--run); }
.session-grant-perms { color: var(--ink); overflow-wrap: anywhere; min-width: 0; }
.owner-refused {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin: 0;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--run);
  border-left: 2px solid var(--run);
}
.owner-refused-err {
  color: var(--fail);
  font-size: 12px;
}

.relay-modes {
  display: inline-flex;
  align-items: center;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: hidden;
}
.relay-mode {
  all: unset;
  padding: 2px 8px;
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
.relay-mode.active:first-child {
  background: rgba(224, 162, 74, 0.18);
  color: var(--run);
}
.relay-mode.active:nth-child(2) {
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

.act {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 11px;
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
  border-color: var(--accent);
}
.act--primary:hover:not(:disabled) {
  background: var(--accent);
  color: var(--bg);
}
.act--warn:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
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
  color: var(--ink);
  font-size: 11px;
  text-align: left;
  cursor: pointer;
}
.meta-toggle:hover {
  background: var(--surface-2);
}
.meta-toggle .caret {
  flex: none;
  color: var(--ink-3);
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
  color: var(--ink-3);
}
.meta-hint {
  flex: none;
  margin-left: auto;
  color: var(--ink-3);
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
  color: var(--ink-3);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.meta dd {
  margin: 0;
  color: var(--ink);
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
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 6px;
  font-size: 10px;
  cursor: pointer;
}
.copy-btn:hover {
  color: var(--accent);
  border-color: var(--accent);
}
.dim {
  color: var(--ink-3);
}

/* 信件入口：meta 里的一行小链接（/mail?session=<sid>）。 */
.mail-link {
  margin-left: 8px;
  color: var(--accent);
  font-size: 11px;
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
  color: var(--ink-3);
  font-size: 11px;
  min-height: 18px;
}
/* 对话流的最后一项：随消息一起滚动，不固定。折叠时只占一行。 */
.last-message-fixed {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  color: var(--ink);
}
.last-message-preview {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-3);
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
@media (max-width: 760px) {
  .last-message-head { gap: 8px; }
  /* 手机：放行说明缩成标记，完整说明在 title 里 */
  .last-message-released { font-size: 0; }
  .last-message-released::before { content: '已放行'; font-size: 11px; color: var(--ink-3); }
}
/* 渲染后的 markdown：不能 pre-wrap（标签之间的换行会变成一行行空白），也不用等宽字体（代码块自带） */
.last-message-text {
  max-height: 45vh;
  overflow: auto;
  margin: 8px 0 0;
  overflow-wrap: anywhere;
  font-size: 12px;
  line-height: 1.55;
}
.session-progress {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  color: var(--ink);
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
  color: var(--ink-3);
}
.session-progress-full {
  max-height: 35vh;
  overflow: auto;
  margin-top: 8px;
  overflow-wrap: anywhere;
}
.outbox-panel {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
.outbox-status--queued, .outbox-status--delivering { color: var(--run); }
.outbox-status--failed, .outbox-status--undelivered, .outbox-error { color: var(--fail); }
.relay-message-status { display: flex; align-items: center; gap: 10px; margin-top: 6px; font-size: 11px; }
/* 气泡只定外观；长内容不撑宽容器（min-width / 断行 / pre 与表格横滚）由 .msg-body 统一处理。 */
.turn {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.bubble {
  max-width: 88%;
  min-width: 0;
  border-radius: 8px;
  padding: 8px 11px;
  font-size: 13px;
  line-height: 1.5;
}
.bubble--agent {
  align-self: flex-start;
  background: var(--bg);
  border: 1px solid var(--line);
  color: var(--ink);
}
.bubble--human {
  align-self: flex-end;
  background: color-mix(in srgb, var(--accent) 16%, transparent);
  border: 1px solid var(--accent);
  color: var(--ink);
}
.bubble--expired,
.bubble--pending,
.bubble--acked {
  align-self: flex-end;
  font-size: 11px;
  color: var(--ink-3);
  border: 1px dashed var(--line);
}
.bubble--pending,
.bubble--acked {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.bubble--acked {
  color: var(--ink-3);
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
  color: var(--ink-3);
  margin-bottom: 4px;
}
.bubble-text {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: pre-wrap;
}
/* markdown 渲染的 agent 消息：紧凑排版，折叠时用高度裁剪 + 渐隐，
   避免截断 markdown 源码破坏语法。溢出（pre / table / 长行）在 .msg-body 里统一处理。 */
.bubble-md {
  min-width: 0;
  font-size: 12px;
  line-height: 1.55;
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
  background: var(--surface-2);
}
.bubble-md pre {
  margin: 0 0 8px;
  padding: 8px 10px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: var(--surface-2);
}
.bubble-md pre code {
  padding: 0;
  background: transparent;
}
.bubble-md blockquote {
  padding-left: 10px;
  border-left: 2px solid var(--line);
  color: var(--ink-3);
}
.bubble-md a {
  color: var(--accent);
}
.bubble-md table {
  border-collapse: collapse;
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
  color: var(--accent);
  padding: 4px 0 0;
  font-size: 11px;
  cursor: pointer;
}
.link-btn:hover {
  text-decoration: underline;
}
.empty {
  color: var(--ink-3);
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
/* 附图条的说明（上传中 / 置灰原因）：跟错误同一行位置，字号更小 */
.composer .attach-note {
  margin: 0;
  color: var(--ink-3);
  font-size: 11px;
}
/* 成功回执（已送入终端 ✓）：绿色一边，和错误同位置同尺寸，避免布局跳动 */
.composer .receipt {
  color: var(--done);
  font-size: 12px;
  border: 1px solid currentcolor;
  border-radius: var(--r-chip);
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
  background: var(--bg);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
  color: var(--accent);
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
  color: var(--ink-3);
}
.wake-confirm { margin: 6px 14px; }
.wake-why { color: var(--run); flex-basis: 100%; word-break: break-word; }
.wake-cmd { display: block; margin-top: 4px; color: var(--accent); word-break: break-all; }
.takeover-confirm {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--run);
  border: 1px solid currentcolor;
  border-radius: var(--r-chip);
  padding: 6px 10px;
}
.takeover-confirm .takeover-acts {
  display: inline-flex;
  gap: 6px;
}
.composer-input:focus {
  outline: none;
  border-color: var(--accent);
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
  color: var(--ink-3);
  font-size: 11px;
  line-height: 1.4;
}
.hint code {
  color: var(--accent);
}

@media (max-width: 760px) {
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
  border-radius: var(--r-chip);
  background: var(--surface);
  color: var(--accent);
  font-size: 11px;
  line-height: 1.6;
}
.ack-btn:hover:not(:disabled) { border-color: var(--accent); }

/* 手机会话头部折叠条：默认不显示（宽屏头部照旧全展开，见 .head-collapsible 的 900px 规则）。 */
.head-summary {
  display: none;
  align-items: baseline;
  gap: 6px;
  width: 100%;
  padding: 6px 12px;
  color: var(--ink-3);
  background: transparent;
  border: 0;
  border-bottom: 1px solid var(--line);
  text-align: left;
  font-size: 11px;
  cursor: pointer;
}
.head-summary:hover { color: var(--ink); }
.head-summary .caret { flex: none; }
.head-summary-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 输入框说明的「?」开关：同样只在手机档出现。 */
.hint-toggle {
  display: none;
  flex: none;
  width: 22px;
  height: 22px;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: 50%;
  background: transparent;
  color: var(--ink-3);
  font-size: 11px;
  line-height: 1;
  cursor: pointer;
}
.hint-toggle:hover { border-color: var(--accent); color: var(--accent); }

/* 手机档（≤900px）：主题 / 范围 / 中继说明收成一行（接管那组按钮不进折叠区，始终可见）、
   输入框说明收起——把纵向空间留给消息。宽屏一并不受影响：折叠条 / 「?」 始终 display: none，
   头部与说明永远展开。 */
@media (max-width: 900px) {
  .head-summary { display: flex; }
  .drawer-panel:not(.drawer-panel--head-open) .head-collapsible,
  .drawer-panel:not(.drawer-panel--head-open) .meta-wrap { display: none; }
  .hint-toggle { display: inline-block; }
  .drawer-panel:not(.drawer-panel--hint-open) .hint { display: none; }
  /* 常驻的唤醒/接管条在窄屏上收紧一点，但它始终可见。 */
  .wake-bar { gap: 5px 8px; padding: 5px 10px; }
}
</style>
