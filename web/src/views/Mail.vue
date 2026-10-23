<script setup lang="ts">
// 信箱页（design 2026-10-20「信箱按对话显示、会话以人物呈现」，计划 M2；UI 重设计 P4 按 §7.1 落地）：
//  - 左栏：标题、筛选 chip（全部 / 收件 / 发件 / 待指派，带计数）、项目 chip、写信按钮；
//    对话列表（服务端按参与方对聚合的 conversations 接口，一行一个对话）——两个人物卡、
//    最后一封的纯文本摘要、未读数、急件与待指派标记、信件数、活动时间；底部是常驻 agent；
//  - 右栏：所选对话的时间线（GET /v1/mail?peer=&with=，时间正序）——气泡按「我方在右」分边，
//    回信标「回复 #N」可跳到被回复的那封，正文按 markdown 渲染（MarkdownBlock，含消毒）、
//    长信折叠；顶部「加载更早的 N 条」；底部回复框（ui/Composer，带急件）；
//  - 待指派：原因、候选（服务端在发信 / 改投的回答里带回）、改投（改地址重发）、撤回；
//  - 写信：Sheet 里的收件人（会话多选 / 「按项目 · 主题 · agent 全部」选器 / 手写地址）、
//    投递数预览、正文（ui/Composer）、急件。
// 规则（地址解析校验、对话 key、气泡方向、人物卡取色、摘要去 markdown、查询参数）都在
// utils/mail.ts 与 utils/mailConversations.ts，这里只管渲染与请求。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { X } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ackMail,
  cancelMail,
  listAgentSessions,
  listMail,
  listMailConversations,
  previewMail,
  readdressMail,
  replyMail,
  sendMail,
} from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import type { MailCandidate, MailConversation, MailMessage } from '../api/types'
import { useWhoami } from '../store/whoami'
import { t, te } from '../i18n'
import { pageWriteBlock } from '../utils/pageGate'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import MarkdownBlock from '../components/MarkdownBlock.vue'
import MailPersonaChip from '../components/mail/PersonaChip.vue'
import MailMinimap from '../components/mail/MailMinimap.vue'
import MailResidentAgents from '../components/mail/ResidentAgents.vue'
import SessionDrawer from '../components/SessionDrawer.vue'
import BackLink from '../ui/BackLink.vue'
import Composer from '../ui/Composer.vue'
import FilterChips from '../ui/FilterChips.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import Sheet from '../ui/Sheet.vue'
import { type FilterChipItem } from '../ui/filterChips'
import { useProjectFilter } from '../ui/projectFilter'
import {
  buildMailReaddress,
  buildMailReply,
  buildMailSend,
  emptyMailCompose,
  filterComposeSessions,
  mailComposeAddresses,
  mailFilterFromQuery,
  mailHeldQuery,
  mailListQuery,
  mailOutgoing,
  mailPartyLabel,
  mailRecipientPreview,
  mailSessionOptionLabel,
  mailStateTone,
  parseMailRecipients,
  type ComposeSessionOption,
  type MailComposeState,
  type MailFilter,
  type MailIdentity,
  type MailPreviewCount,
  type MailScope,
} from '../utils/mail'
import {
  bubbleSide,
  groupMailConversations,
  mailConversationBox,
  mailConversationInProject,
  mailConversationKey,
  mailConversationSeq,
  mailCountConversations,
  mailFoldedBody,
  mailLongBody,
  mailPartyAddress,
  mailSummary,
  mailTimelineAddressable,
  mailTimelineQuery,
  mailUnread,
  personaLabel,
  replyTarget,
  sortMailTimeline,
  type MailTimelineQuery,
} from '../utils/mailConversations'
import { buildMinimapMarks, minimapBand, minimapScrollTopForPct, minimapVisible } from '../utils/mailMinimap'
import {
  MAIL_TIMELINE_PAGE,
  oldestMailId,
  scrollTopAfterPrepend,
  timelineFirstPage,
  timelineNearBottom,
  timelinePage,
  timelineWithNewest,
  timelineWithOlder,
} from '../utils/mailPaging'

const route = useRoute()
const router = useRouter()
const { whoami } = useWhoami()

// 左栏：对话（服务端聚合，或「待指派」/ 会话深链时本地按参与方对分组）。
const rows = ref<MailConversation[]>([])
// 右栏：所选对话的时间线（时间正序）。
const timeline = ref<MailMessage[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))
const activeKey = ref('')
const openSid = ref('')
// 长信折叠：默认折叠，记下展开过的那几封。
const expandedIds = ref<Set<number>>(new Set())
// 「回复 #N」跳过去时闪一下被回复的那封。
const highlightId = ref(0)
// 窄屏：列表与时间线切换显示。
const mobileDetail = ref(false)
// 时间线的滚动量（小地图的可视范围高亮带、点击标记后的滚动目标都靠它）；
// 容器 ref 只在有对话时挂上，没挂上就保持 0。
const timelineEl = ref<HTMLElement | null>(null)
const timelineScroll = ref({ scrollTop: 0, clientHeight: 0, scrollHeight: 0 })
// 时间线分页：打开对话只要最新的 MAIL_TIMELINE_PAGE 封，往上翻再取更早的一页。hasOlder 是
// 服务端说的「还有更早的」；timelineKey 记住已加载的这一页属于哪个对话（刷新同一对话时只补
// 最新的信，不重载已加载的页）。
const hasOlder = ref(false)
const loadingOlder = ref(false)
const timelineKey = ref('')

const filter = ref<MailFilter>(mailFilterFromQuery(route.query))
const sessionFilter = ref(queryValue(route.query.session))

// 项目是筛选条件，不是全局状态（设计稿 §6.4）：值写进 URL 的 ?project=，对话按参与方的
// 所属项目在本地过滤（conversations 接口没有项目参数）。
const { project, setProject, options: projectOptions, loadOptions: loadProjectOptions, loadError: projectLoadError } =
  useProjectFilter('/mail')

// 各筛选下的对话数（chip 上的计数）。口径与页头「N 个对话」一致：这个筛选下有几个对话。
// conversations 接口的 count 是这一页的条数（limit=1 时恒为 1，之前 chip 因此全显示 1），所以按
// 返回的对话数数；待指派没有对话接口，按本地分组的对话数算。
const MAIL_COUNT_LIMIT = 200 // = 服务端 mailConvMax：一次取满再数，不给计数另建接口
const counts = ref<Record<MailFilter, number>>({ all: 0, in: 0, out: 0, held: 0 })

const filterItems = computed<FilterChipItem[]>(() => [
  { value: 'all', label: t('mail.filter.all'), count: counts.value.all },
  { value: 'in', label: t('mail.filter.in'), count: counts.value.in },
  { value: 'out', label: t('mail.filter.out'), count: counts.value.out },
  { value: 'held', label: t('mail.filter.held'), count: counts.value.held },
])

// 服务端只在发信 / 改投的回答里带候选，收件箱列表不带：这里按信件 id 记下见过的那几组，
// 打开待指派信件时能一键填进改投输入框。
const candidates = ref<Record<number, MailCandidate[]>>({})

const composeOpen = ref(false)
// 写信框的收件人（gofer-ndy5，设计 2026-10-22「D5」）：会话多选 + 「按项目 / 主题 / agent
// 全部」选器 + 手写地址（高级）。地址怎么拼是 utils/mail.ts 的 mailComposeAddresses。
const compose = ref<MailComposeState>(emptyMailCompose())
const composeSearch = ref('')
const scopePick = ref<MailScope>({ scheme: 'project', value: '' })
const composeAddresses = computed(() => mailComposeAddresses(compose.value))
// 标签只显示真选的收件人（空选择时的 caller 是内部缺省，不画成标签）。
const composeTags = computed(() => composeAddresses.value.addresses.filter((a) => a !== 'caller'))
// 会话多选的可选项（打开写信框时惰性加载一次），搜索词在本地过滤。
const composeSessions = ref<ComposeSessionOption[]>([])
const composeSessionOptions = computed(() => filterComposeSessions(composeSessions.value, composeSearch.value))
// 服务端的投递预览（POST /v1/mail dry_run）：与真发同一套解析、不落库。
const composePreview = ref<MailPreviewCount | null>(null)
const composePreviewError = ref('')
const composeLocalEstimate = computed(() =>
  mailRecipientPreview(composeAddresses.value.addresses.join(' '), composeSessions.value),
)
const composePreviewText = computed(() =>
  previewText(composeAddresses.value.addresses, composePreview.value, composeLocalEstimate.value),
)

async function loadComposeSessions(): Promise<void> {
  try {
    const resp = await listAgentSessions({ limit: 200 })
    composeSessions.value = resp.sessions.map((s) => ({
      session_id: s.session_id, agent: s.agent, project_key: s.project_key, topic: s.topic, title: s.title,
    }))
  } catch {
    composeSessions.value = []
  }
}

let previewTimer = 0
async function refreshComposePreview(): Promise<void> {
  composePreview.value = null
  composePreviewError.value = ''
  if (!composeAddresses.value.ok) return
  // 没选收件人（投给自己）不用问服务端，文案本来就说「留空 = 收集箱」。
  if (composeAddresses.value.addresses.length === 1 && composeAddresses.value.addresses[0] === 'caller') return
  try {
    // 预览也要过正文校验（服务端拒绝空正文）：还没写正文时先带个占位。
    const resp = await previewMail({ to: composeAddresses.value.addresses, body: compose.value.body.trim() || t('mail.compose.previewBody') })
    composePreview.value = { delivered: resp.delivered_count, skipped: resp.skipped_count, matched: resp.matched }
  } catch (e) {
    composePreviewError.value = e instanceof Error ? e.message : String(e)
  }
}

watch(composeOpen, (open) => {
  if (open && composeSessions.value.length === 0) void loadComposeSessions()
})

// 收件人一变（抖一下）就向服务端要一次投递预览：选器可能命中很多会话，只有服务端知道得准。
watch(
  () => [
    composeOpen.value,
    compose.value.sessions.join(','),
    compose.value.scopes.map((s) => `${s.scheme}:${s.value}`).join(','),
    compose.value.manual,
  ],
  () => {
    if (!composeOpen.value) return
    window.clearTimeout(previewTimer)
    previewTimer = window.setTimeout(() => void refreshComposePreview(), 400)
  },
)

function toggleComposeSession(sid: string): void {
  const list = compose.value.sessions
  compose.value.sessions = list.includes(sid) ? list.filter((x) => x !== sid) : [...list, sid]
}

function addComposeScope(): void {
  const value = scopePick.value.value.trim()
  if (!value) return
  compose.value.scopes = [...compose.value.scopes, { scheme: scopePick.value.scheme, value }]
  scopePick.value = { ...scopePick.value, value: '' }
}

function removeComposeAddress(address: string): void {
  if (address.startsWith('session:')) {
    const sid = address.slice('session:'.length)
    compose.value.sessions = compose.value.sessions.filter((x) => x !== sid)
    return
  }
  if (address.startsWith('each:')) {
    const [, scheme, ...rest] = address.split(':')
    const value = rest.join(':')
    compose.value.scopes = compose.value.scopes.filter((s) => !(s.scheme === scheme && s.value === value))
    return
  }
  // 手写地址：从手写串里去掉那一个。
  const manual = parseMailRecipients(compose.value.manual)
  compose.value.manual = manual.ok ? manual.addresses.filter((a) => a !== address).join(' ') : compose.value.manual
}

const composeError = ref('')
const sending = ref(false)

const reply = ref({ body: '', urgent: false })
const replyError = ref('')
const replying = ref(false)

const readdressDraft = ref<Record<number, string>>({})
const readdressError = ref('')
const acting = ref(false)

function queryValue(v: unknown): string {
  const x = Array.isArray(v) ? v[0] : v
  return typeof x === 'string' ? x.trim() : ''
}

// 我在这台 web 上的身份：调用方 id；带 ?session= 进来时那个会话也算「我」（会话抽屉里
// 点进来，要把它发的信认成我方，气泡在自己这一侧）。
const me = computed<MailIdentity>(() => ({ caller: whoami.value?.caller_id ?? '', session: sessionFilter.value }))

// 回信（POST /v1/mail/{id}/reply）：信的两端任意一端是自己就不需要权限（收发都算自己的信）；
// 投到别人的会话要 session.reply.others、别人的 job / 调用方要 job.manage.others——
// 路由上就是 InHandler(permDeny, 这两条)，不是同时要求。只读身份一律灰显（gofer-bp9c，
// 判定见 utils/pageGate.ts，服务端仍是权威）。
const replyWriteBlock = computed(() => {
  const target = replyTarget(timeline.value, me.value)
  const caller = whoami.value?.caller_id
  const own = !!target && !!caller && (target.from.caller === caller || target.to.caller === caller)
  return pageWriteBlock(whoami.value, {
    ownerId: own ? caller : undefined,
    anyPerms: ['session.reply.others', 'job.manage.others'],
  })
})

const active = computed(() => rows.value.find((r) => r.key === activeKey.value) ?? null)
const seq = computed(() => mailConversationSeq(timeline.value))
// 「待指派」与会话深链没有对应的 conversations 过滤：按信件在本地分组成对话。
const groupedLocally = computed(() => filter.value === 'held' || !!sessionFilter.value)
const activeTitle = computed(() => {
  const conv = active.value
  if (!conv) return ''
  return `${personaLabel(conv.a)} / ${personaLabel(conv.b)}`
})
// 左栏真正显示的对话：在当前筛选的结果里再按项目收窄（§6.4）。
const visibleRows = computed(() =>
  project.value ? rows.value.filter((r) => mailConversationInProject(r, project.value)) : rows.value,
)
// 回到列表的地址（手机上的返回链接）：保留当前筛选，别把用户选的页签 / 会话弄丢。
const listUrl = computed(() => {
  const params = new URLSearchParams()
  if (filter.value !== 'all') params.set('filter', filter.value)
  if (sessionFilter.value) params.set('session', sessionFilter.value)
  if (project.value) params.set('project', project.value)
  const qs = params.toString()
  return qs ? `/mail?${qs}` : '/mail'
})

async function loadRows(): Promise<void> {
  if (groupedLocally.value) {
    const resp = await listMail(mailListQuery(filter.value, { session: sessionFilter.value }))
    rows.value = groupMailConversations(resp.mail ?? [])
    return
  }
  const resp = await listMailConversations({ box: mailConversationBox(filter.value), limit: 100 })
  rows.value = resp.conversations ?? []
}

// loadCounts 取四个筛选 chip 的计数：收件 / 发件 / 全部看 conversations 接口返回的对话
// （按当前项目收窄，跟页头的数一致），待指派按本地分组算对话数（它就是「还没投出去的信」，
// 没有单独的对话接口）。计数只是提示，失败就不显示变化，不影响看信。
async function loadCounts(): Promise<void> {
  try {
    const [all, inbox, outbox, held] = await Promise.all([
      listMailConversations({ box: 'all', limit: MAIL_COUNT_LIMIT }),
      listMailConversations({ box: 'in', limit: MAIL_COUNT_LIMIT }),
      listMailConversations({ box: 'out', limit: MAIL_COUNT_LIMIT }),
      listMail(mailHeldQuery()),
    ])
    counts.value = {
      all: mailCountConversations(all.conversations ?? [], project.value),
      in: mailCountConversations(inbox.conversations ?? [], project.value),
      out: mailCountConversations(outbox.conversations ?? [], project.value),
      held: mailCountConversations(groupMailConversations(held.mail ?? []), project.value),
    }
  } catch {
    // 计数失败保持上一次的值
  }
}

// timelineBase 是一个对话时间线的公共取数参数（分页只在它上面加 limit / before / order）。
function timelineBase(q: MailTimelineQuery, addressable: boolean) {
  return {
    box: q.box,
    state: q.state,
    peer: addressable ? q.peer : undefined,
    with: addressable ? q.with : undefined,
    session: sessionFilter.value || undefined,
  }
}

// loadTimeline 打开一个对话：只取最新的一页（旧在上、新在下），然后停在最底部。
async function loadTimeline(): Promise<void> {
  const conv = active.value
  timelineKey.value = conv?.key ?? ''
  if (!conv) {
    timeline.value = []
    hasOlder.value = false
    return
  }
  const q = mailTimelineQuery(conv, filter.value)
  const addressable = mailTimelineAddressable(q)
  // 一端的地址没解析到人（topic: / path: 之类的待指派信）时服务端不许用 peer= / with=：
  // 退回按对话 key 本地过滤（这种对话不服务端分页，一次取一页上限）。
  const resp = await listMail({
    ...timelineBase(q, addressable),
    ...(addressable ? timelinePage() : { limit: 200 }),
  })
  const all = resp.mail ?? []
  if (!addressable) {
    timeline.value = sortMailTimeline(all.filter((m) => mailConversationKey(m) === q.key))
    hasOlder.value = false
    return
  }
  const pages = timelineFirstPage(all, !!resp.has_more)
  timeline.value = pages.mails
  hasOlder.value = pages.hasOlder
  await scrollTimelineToBottom()
}

// loadOlder 取「比已加载最旧那封更早」的一页，插到上方；插入前后按 scrollHeight 的差值
// 修正 scrollTop，正在读的那封信不会跟着往下跳。
async function loadOlder(): Promise<void> {
  const conv = active.value
  if (!conv || loadingOlder.value || !hasOlder.value) return
  const before = oldestMailId(timeline.value)
  const q = mailTimelineQuery(conv, filter.value)
  if (!before || !mailTimelineAddressable(q)) return
  loadingOlder.value = true
  const el = timelineEl.value
  const prevTop = el?.scrollTop ?? 0
  const prevHeight = el?.scrollHeight ?? 0
  try {
    const resp = await listMail({ ...timelineBase(q, true), ...timelinePage(MAIL_TIMELINE_PAGE, before) })
    const pages = timelineWithOlder(
      { mails: timeline.value, hasOlder: hasOlder.value },
      resp.mail ?? [],
      !!resp.has_more,
    )
    timeline.value = pages.mails
    hasOlder.value = pages.hasOlder
    await nextTick()
    if (el) el.scrollTop = scrollTopAfterPrepend(prevTop, prevHeight, el.scrollHeight)
    syncTimelineScroll()
    // 翻出来的更早的信也是「打开对话就看得到的」：收到的未读同样算读过。
    const unreadOlder = (resp.mail ?? []).some((m) => mailUnread(m) && !mailOutgoing(m, me.value))
    if (unreadOlder) await ackUnread()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingOlder.value = false
  }
}

// refreshTimeline 是同一对话的刷新 / 新信到达：只补最新的信，已加载的页不重取。
async function refreshTimeline(): Promise<void> {
  const conv = active.value
  if (!conv) return
  const q = mailTimelineQuery(conv, filter.value)
  if (!mailTimelineAddressable(q)) {
    await loadTimeline()
    return
  }
  const el = timelineEl.value
  const atBottom = el ? timelineNearBottom(el.scrollTop, el.clientHeight, el.scrollHeight) : true
  const resp = await listMail({ ...timelineBase(q, true), ...timelinePage() })
  const pages = timelineWithNewest({ mails: timeline.value, hasOlder: hasOlder.value }, resp.mail ?? [])
  timeline.value = pages.mails
  hasOlder.value = pages.hasOlder
  // 正在往上翻历史时不把视图拽到底部；贴底看最新时才跟着走。
  if (atBottom) await scrollTimelineToBottom()
  else await refreshTimelineMetrics()
}

// scrollTimelineToBottom 把时间线滚到最底部（最新的一封）。
async function scrollTimelineToBottom(): Promise<void> {
  await nextTick()
  const el = timelineEl.value
  if (el) el.scrollTop = el.scrollHeight
  syncTimelineScroll()
}

async function load(silent = false): Promise<void> {
  if (!silent) loading.value = true
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    await loadRows()
    const shown = visibleRows.value
    if (!shown.some((r) => r.key === activeKey.value)) {
      activeKey.value = shown[0]?.key ?? ''
    }
    // 同一个对话的静默刷新只补最新的信；换对话（或首屏）才整段重取。
    if (silent && timelineKey.value && timelineKey.value === activeKey.value && timeline.value.length) {
      await refreshTimeline()
    } else {
      await loadTimeline()
    }
    error.value = ''
  } catch (e) {
    rows.value = []
    timeline.value = []
    hasOlder.value = false
    timelineKey.value = ''
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
  // 计数跟着刷新走（异步、失败不影响看信）。静默刷新（读信后的补最新）不必重算：
  // 读信不改计数，发信 / 改投 / 撤回会单独再取一次。
  if (!silent) void loadCounts()
}

// 页签切换写回地址栏（可分享 / 可收藏），再由 watch 重新拉；Today 卡与抽屉的深链也是
// 同一套参数（?state=held / ?session=<sid>）。
function setFilter(f: MailFilter): void {
  const query: Record<string, string> = {}
  if (f !== 'all') query.filter = f
  if (sessionFilter.value) query.session = sessionFilter.value
  if (project.value) query.project = project.value
  void router.replace({ path: '/mail', query })
}

function clearSession(): void {
  const query: Record<string, string> = {}
  if (filter.value !== 'all') query.filter = filter.value
  if (project.value) query.project = project.value
  void router.replace({ path: '/mail', query })
}

watch(
  () => [route.query.filter, route.query.box, route.query.state, route.query.session],
  () => {
    filter.value = mailFilterFromQuery(route.query)
    sessionFilter.value = queryValue(route.query.session)
    activeKey.value = ''
    void load()
  },
)

// 换项目：当前对话不在新项目里就自动选这个项目的第一条。
watch(project, async () => {
  if (!visibleRows.value.some((r) => r.key === activeKey.value)) {
    activeKey.value = visibleRows.value[0]?.key ?? ''
    await loadTimeline().catch(() => {
      timeline.value = []
    })
  }
})

// 打开一个对话 = 读过它的信：把自己收的未读信件逐封 ack（读 = 送达；服务端只对收件方生效）。
async function selectConversation(key: string): Promise<void> {
  activeKey.value = key
  mobileDetail.value = true
  replyError.value = ''
  notice.value = ''
  highlightId.value = 0
  try {
    await loadTimeline()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    timeline.value = []
    hasOlder.value = false
    timelineKey.value = ''
    return
  }
  await ackUnread()
}

async function ackUnread(): Promise<void> {
  const unread = timeline.value.filter((m) => mailUnread(m) && !mailOutgoing(m, me.value))
  if (!unread.length) return
  try {
    await Promise.all(unread.map((m) => ackMail(m.id)))
    await load(true)
  } catch {
    // 读信失败不影响看信：状态保持原样，服务端仍是权威。
  }
}

function backToList(): void {
  mobileDetail.value = false
}

async function sendCompose(): Promise<void> {
  const built = buildMailSend({ to: composeAddresses.value.addresses, body: compose.value.body, urgent: compose.value.urgent })
  composeError.value = composeAddresses.value.ok ? built.error : composeAddresses.value.error
  if (!built.req) return
  sending.value = true
  try {
    const resp = await sendMail(built.req)
    const count = resp.delivered_count ?? 1
    const skipped = resp.skipped_count ?? 0
    if (count > 1 || skipped > 0) {
      // 广播：回一句投递了几个人（略过几个），不再说「信件 #N 已发出」。
      notice.value =
        skipped > 0
          ? t('mail.notice.broadcastSkipped', { n: count, k: skipped })
          : t('mail.notice.broadcast', { n: count })
      await load(true)
    } else {
      noteSendResult(resp.mail, resp.held, resp.candidates ?? [])
      await load(true)
    }
    void loadCounts()
    compose.value = emptyMailCompose()
    composeSearch.value = ''
    composePreview.value = null
    composeOpen.value = false
  } catch (e) {
    composeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}

function noteSendResult(m: MailMessage, held: boolean, cands: MailCandidate[]): void {
  if (cands.length) candidates.value = { ...candidates.value, [m.id]: cands }
  if (held) {
    notice.value = cands.length
      ? t('mail.notice.heldCandidates', { id: m.id, n: cands.length })
      : t('mail.notice.held', { id: m.id })
  } else {
    notice.value = t('mail.notice.sent', { id: m.id, state: stateLabel(m.state) })
  }
}

async function sendReply(text: string): Promise<void> {
  // 回复送回原发件方地址（不是我发出的那封的最新一封）。
  const target = replyTarget(timeline.value, me.value)
  if (!target) return
  const built = buildMailReply(text, reply.value.urgent)
  replyError.value = built.error
  if (!built.req) return
  replying.value = true
  try {
    const resp = await replyMail(target.id, built.req)
    noteSendResult(resp.mail, resp.held, resp.candidates ?? [])
    reply.value = { body: '', urgent: false }
    await load(true)
    void loadCounts()
  } catch (e) {
    replyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    replying.value = false
  }
}

async function doReaddress(m: MailMessage): Promise<void> {
  const built = buildMailReaddress(readdressDraft.value[m.id] ?? '')
  readdressError.value = built.error
  if (!built.req) return
  acting.value = true
  try {
    const resp = await readdressMail(m.id, built.req)
    noteSendResult(resp.mail, resp.held, resp.candidates ?? [])
    await load(true)
    void loadCounts()
  } catch (e) {
    readdressError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = false
  }
}

async function doCancel(m: MailMessage): Promise<void> {
  acting.value = true
  try {
    await cancelMail(m.id)
    notice.value = t('mail.notice.cancelled', { id: m.id })
    await load(true)
    void loadCounts()
  } catch (e) {
    readdressError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = false
  }
}

function fillReaddress(m: MailMessage, to: string): void {
  readdressDraft.value = { ...readdressDraft.value, [m.id]: to }
}

// ---- 文案（页面字典；服务端返回的状态值是文案来源，业务内容不翻译）----

// 信件状态：服务端状态值 → 本页字典（没有翻译的状态值原样显示）。
function stateLabel(state: string): string {
  const key = `mail.state.${state}`
  return te(key) ? t(key) : state || '—'
}

// 收件人地址的人话名称（写信框的标签、预览共用）。
function addressLabel(address: string): string {
  if (address === 'caller') return t('mail.addr.caller')
  if (address.startsWith('each:')) {
    const [, scheme, ...rest] = address.split(':')
    const word = te(`mail.scopeWord.${scheme}`) ? t(`mail.scopeWord.${scheme}`) : scheme
    return t('mail.addr.each', { scope: word, value: rest.join(':') })
  }
  const sep = address.indexOf(':')
  const scheme = sep < 0 ? '' : address.slice(0, sep)
  const value = sep < 0 ? address : address.slice(sep + 1)
  const key = `mail.addr.${scheme}`
  return te(key) ? t(key, { value }) : address
}

// 投递数预览：服务端的 dry_run 优先，其次本地估算（规则同 utils/mail.ts 的 mailRecipientPreview）。
function previewText(addresses: string[], preview: MailPreviewCount | null, localEstimate: number): string {
  if (!addresses.length || (addresses.length === 1 && addresses[0] === 'caller')) return t('mail.preview.caller')
  if (preview) {
    return preview.skipped > 0
      ? t('mail.preview.countSkipped', { n: preview.delivered, k: preview.skipped })
      : t('mail.preview.count', { n: preview.delivered })
  }
  if (localEstimate < 0) return t('mail.preview.path')
  return t('mail.preview.estimate', { n: localEstimate })
}

// 回信角标与跳转（设计「同一个 thread 里的回信用细线串起来，标注回复 #N，点击跳到被回复的那封」）。
function replyLabel(m: MailMessage): string {
  if (!m.reply_to) return ''
  const n = seq.value.get(m.reply_to)
  return n ? t('mail.letter.replyRef', { n }) : t('mail.letter.replyRefPlain')
}

// 滚到某一封信：时间线是自己的滚动容器（整页不再滚动），scrollIntoView 会找最近的可滚动祖先。
function scrollToLetter(id: number): void {
  const target = document.getElementById(`mail-${id}`)
  if (!target) return
  target.scrollIntoView({ behavior: 'smooth', block: 'center' })
}

function jumpToReply(m: MailMessage): void {
  if (!m.reply_to) return
  scrollToLetter(m.reply_to)
  highlightId.value = m.reply_to
  window.setTimeout(() => {
    if (highlightId.value === m.reply_to) highlightId.value = 0
  }, 1600)
}

// ---- 对话小地图 ----
// 每封信一个标记（位置 / 颜色 / 悬停文案在 utils/mailMinimap.ts）；一两封信时整条不显示。
const minimapMarks = computed(() =>
  minimapVisible(timeline.value.length) ? buildMinimapMarks(timeline.value, me.value, nowSec.value) : [],
)
const minimapVisibleBand = computed(() =>
  minimapBand(timelineScroll.value.scrollTop, timelineScroll.value.clientHeight, timelineScroll.value.scrollHeight),
)

// 时间线滚一下就把可视范围带挪过去（scroll 不冒泡，绑在容器上）。
function syncTimelineScroll(): void {
  const el = timelineEl.value
  if (!el) return
  timelineScroll.value = { scrollTop: el.scrollTop, clientHeight: el.clientHeight, scrollHeight: el.scrollHeight }
}

// 换对话 / 收到新信后容器高度会变：滚完再量一次，免得高亮带停在旧比例上。
async function refreshTimelineMetrics(): Promise<void> {
  await nextTick()
  syncTimelineScroll()
}

// 点小地图上的标记：把那封信放到可视区中间（计算见 minimapScrollTopForPct），再闪一下。
function jumpToMark(id: number): void {
  const el = timelineEl.value
  const mark = minimapMarks.value.find((m) => m.id === id)
  if (!el || !mark) return
  el.scrollTo({
    top: minimapScrollTopForPct(mark.topPct, el.clientHeight, el.scrollHeight),
    behavior: 'smooth',
  })
  highlightId.value = id
  window.setTimeout(() => {
    if (highlightId.value === id) highlightId.value = 0
  }, 1600)
}

watch(timeline, () => void refreshTimelineMetrics())
watch(activeKey, () => void refreshTimelineMetrics())

function bodyText(m: MailMessage): string {
  return expandedIds.value.has(m.id) ? m.body : mailFoldedBody(m.body)
}

function toggleFold(id: number): void {
  const next = new Set(expandedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedIds.value = next
}

// 人物卡上的会话可点开（与会话页 / 抽屉里的打开方式一致：挂一个 SessionDrawer）。
function openSession(sid: string): void {
  openSid.value = sid
}

onMounted(() => {
  void load()
  void loadProjectOptions()
  // 窗口尺寸变了时间线高度跟着变：重算高亮带（整页不滚动，所以不会触发 scroll）。
  window.addEventListener('resize', syncTimelineScroll)
})

onUnmounted(() => {
  window.removeEventListener('resize', syncTimelineScroll)
})
</script>

<template>
  <section class="mail-page">
    <div class="split" :class="{ 'split--detail': mobileDetail }">
      <!-- 左栏：标题、筛选 chip、项目 chip、写信按钮 + 对话列表 + 常驻 agent。 -->
      <aside class="mail-list" :aria-label="t('mail.list.label')">
        <header class="list-head">
          <div class="head-row">
            <h1 class="mail-title">{{ t('mail.h.title') }}<ExperimentalBadge /></h1>
            <span class="dim" data-test="mail-summary">{{ t('mail.h.summary', { n: visibleRows.length }) }}</span>
            <button class="act mono" type="button" data-test="mail-refresh" @click="load()">
              {{ t('mail.h.refresh') }}
            </button>
            <button
              class="act act--primary mono"
              type="button"
              data-test="mail-compose-toggle"
              @click="composeOpen = true"
            >
              {{ t('mail.h.compose') }}
            </button>
          </div>
          <div class="head-filters">
            <FilterChips
              :items="filterItems"
              :model-value="filter"
              :label="t('mail.h.filters')"
              @update:model-value="(v) => setFilter(v as MailFilter)"
            />
            <ProjectFilter
              :model-value="project"
              :options="projectOptions"
              :loading="false"
              :error="projectLoadError"
              @update:model-value="setProject"
            />
            <span v-if="sessionFilter" class="filter-chip mono" data-test="mail-session-filter">
              {{ t('mail.h.session', { sid: sessionFilter }) }}
              <button class="chip-x" type="button" :title="t('mail.h.clearSession')" @click="clearSession"><Icon :icon="X" size="sm" /></button>
            </span>
          </div>
        </header>

        <div class="convs" role="list">
          <p v-if="loading" class="dim pad">{{ t('mail.list.loading') }}</p>
          <p v-else-if="!visibleRows.length" class="dim pad" data-test="mail-empty">
            {{ project ? t('mail.list.emptyProject') : t('mail.list.empty') }}
          </p>
          <button
            v-for="c in visibleRows"
            :key="c.key"
            class="conv"
            :class="{ 'conv--active': c.key === activeKey }"
            type="button"
            role="listitem"
            :aria-current="c.key === activeKey"
            :aria-label="t('mail.conv.select', { who: `${personaLabel(c.a)} / ${personaLabel(c.b)}` })"
            data-test="mail-conversation"
            @click="selectConversation(c.key)"
          >
            <span class="conv-people">
              <MailPersonaChip variant="row" :party="c.a" :now-sec="nowSec" @open-session="openSession" />
              <span class="conv-arrow dim" aria-hidden="true">↔</span>
              <MailPersonaChip variant="row" :party="c.b" :now-sec="nowSec" @open-session="openSession" />
              <span v-if="c.urgent" class="tag tag--urgent" data-test="mail-conv-urgent">
                {{ t('mail.conv.urgent') }}
              </span>
              <span v-if="c.last_mail.state === 'held'" class="tag tag--held" data-test="mail-conv-held">
                {{ t('mail.conv.held') }}
              </span>
              <span
                v-if="c.unread"
                class="unread"
                :title="t('mail.conv.unread', { n: c.unread })"
                data-test="mail-conv-unread"
              >{{ c.unread }}</span>
            </span>
            <span class="conv-excerpt dim" data-test="mail-conv-excerpt">{{ mailSummary(c.last_mail.body, 80) }}</span>
            <span class="conv-meta dim mono">{{ t('mail.conv.meta', { n: c.count, ago: fmtAgo(c.activity_at, nowSec) }) }}</span>
          </button>
        </div>

        <!-- 常驻 agent（设计 2026-10-22 mail-agent-address）：独立组件，这里只挂载。 -->
        <MailResidentAgents />
      </aside>

      <section class="detail" :aria-label="t('mail.thread.label')">
        <template v-if="active">
          <header class="detail-head">
            <BackLink
              v-if="mobileDetail"
              class="back"
              list="mail"
              :to="listUrl"
              :label="t('mail.thread.back')"
              data-test="mail-back"
              @click="backToList"
            />
            <MailPersonaChip :party="active.a" :now-sec="nowSec" @open-session="openSession" />
            <span class="dim" aria-hidden="true">↔</span>
            <MailPersonaChip :party="active.b" :now-sec="nowSec" @open-session="openSession" />
            <span class="dim mono">{{ t('mail.thread.count', { n: timeline.length }) }}</span>
          </header>

          <!-- 时间线是页面自己的滚动区（整页不滚）：右边缘一条小地图，标记＜3 封时不画。 -->
          <div class="timeline-wrap">
            <div ref="timelineEl" class="timeline" data-test="mail-timeline" @scroll="syncTimelineScroll">
              <!-- 分页：打开对话只加载最新的一页，更早的信按需往上翻。「还有更早」的提示与
                   按钮都在这一行里（截图检查 P6-2：提示原先浮在小地图旁，压在信件正文上）。 -->
              <div class="older" data-test="mail-older">
                <template v-if="hasOlder">
                  <span class="older-hint dim mono" data-test="mail-older-hint">{{ t('mail.thread.minimapMore') }}</span>
                  <button
                    class="act mono"
                    type="button"
                    :disabled="loadingOlder"
                    data-test="mail-older-load"
                    @click="loadOlder"
                  >
                    {{ loadingOlder ? t('mail.older.loading') : t('mail.older.load', { n: MAIL_TIMELINE_PAGE }) }}
                  </button>
                </template>
                <span v-else class="dim mono" data-test="mail-older-end">{{ t('mail.older.end') }}</span>
              </div>
              <article
                v-for="m in timeline"
                :id="`mail-${m.id}`"
                :key="m.id"
                class="bubble"
                :class="[
                  `bubble--${bubbleSide(m, active, me)}`,
                  { 'bubble--reply': !!m.reply_to, 'bubble--hot': highlightId === m.id, 'bubble--urgent': !!m.urgent },
                ]"
                :data-side="bubbleSide(m, active, me)"
                data-test="mail-letter"
              >
                <header class="bubble-head">
                  <MailPersonaChip variant="row" :party="m.from" :now-sec="nowSec" @open-session="openSession" />
                  <span class="dim mono bubble-to" :title="mailPartyAddress(m.to) || m.to_address">
                    {{ t('mail.letter.to', { to: m.to_address || mailPartyLabel(m.to) }) }}
                  </span>
                  <button v-if="replyLabel(m)" class="reply-ref mono" type="button" data-test="mail-reply-ref" @click="jumpToReply(m)">
                    {{ replyLabel(m) }}
                  </button>
                  <span v-if="m.urgent" class="tag tag--urgent">{{ t('mail.letter.urgent') }}</span>
                  <span class="letter-state mono" :class="`tone--${mailStateTone(m.state)}`" data-test="mail-letter-state">
                    {{ stateLabel(m.state) }}
                  </span>
                  <span class="dim mono" :title="fmtDateTime(m.created_at)">
                    {{ t('mail.letter.id', { id: m.id, ago: fmtAgo(m.created_at, nowSec) }) }}
                  </span>
                </header>

                <!-- 广播（设计 2026-10-22「D5」）：这张卡是一封群发的副本，下面是整组的收件人状态条。 -->
                <div v-if="m.broadcast" class="broadcast" data-test="mail-broadcast">
                  <span class="broadcast-head mono">
                    {{ t('mail.broadcast.head', {
                      id: m.broadcast.id,
                      count: m.broadcast.count,
                      delivered: m.broadcast.delivered,
                      answered: m.broadcast.answered,
                    }) }}<template v-if="m.broadcast.expired">{{ t('mail.broadcast.expired', { n: m.broadcast.expired }) }}</template><template v-if="m.broadcast.pending">{{ t('mail.broadcast.pending', { n: m.broadcast.pending }) }}</template>
                  </span>
                  <span
                    v-for="r in m.broadcast.recipients"
                    :key="r.mail_id || r.address"
                    class="broadcast-dot mono"
                    :title="`${r.address}${r.skipped ? ' · ' + t('mail.broadcast.skipReason') : r.state ? ' · ' + stateLabel(r.state) : ''}`"
                  >{{ r.id || r.address }} · {{ r.skipped ? t('mail.broadcast.skipped') : stateLabel(r.state || '') }}</span>
                </div>
                <!-- 正文是别的 agent 会话写的 markdown：走 MarkdownBlock（marked + DOMPurify 消毒）。 -->
                <div class="bubble-body" data-test="mail-letter-body">
                  <MarkdownBlock :text="bodyText(m)" />
                </div>
                <button v-if="mailLongBody(m.body)" class="fold mono" type="button" data-test="mail-letter-fold" @click="toggleFold(m.id)">
                  {{ expandedIds.has(m.id) ? t('mail.letter.unfold') : t('mail.letter.fold', { n: [...m.body].length }) }}
                </button>

                <div v-if="m.state === 'held'" class="held" data-test="mail-held">
                  <p class="held-why">
                    <b>{{ t('mail.held.title') }}</b>
                    <span v-if="m.last_error" class="dim">· {{ m.last_error }}</span>
                    <!-- 候选与「新建 job / 丢弃」在收集箱条目里：待指派时生成的条目带推荐的分派目标。 -->
                    <RouterLink
                      v-if="m.capture_item_id"
                      class="held-item"
                      data-test="mail-held-item"
                      :to="{ path: '/work', query: { id: m.capture_item_id } }"
                    >{{ t('mail.held.item') }}</RouterLink>
                  </p>
                  <div v-if="candidates[m.id]?.length" class="cands" data-test="mail-candidates">
                    <span class="dim">{{ t('mail.held.candidates') }}</span>
                    <button
                      v-for="c in candidates[m.id]"
                      :key="`${c.kind}:${c.id}`"
                      class="cand mono"
                      type="button"
                      :title="(c.why ?? []).join(' / ')"
                      @click="fillReaddress(m, `${c.kind}:${c.id}`)"
                    >
                      {{ c.display || `${c.kind}:${c.id}` }}
                    </button>
                  </div>
                  <div class="readdress">
                    <input
                      v-model="readdressDraft[m.id]"
                      class="mono"
                      type="text"
                      :placeholder="t('mail.held.readdressTo')"
                      data-test="mail-readdress-to"
                    />
                    <button class="act act--primary mono" type="button" :disabled="acting" data-test="mail-readdress" @click="doReaddress(m)">
                      {{ t('mail.held.readdress') }}
                    </button>
                    <button class="act mono" type="button" :disabled="acting" data-test="mail-cancel" @click="doCancel(m)">
                      {{ t('mail.held.cancel') }}
                    </button>
                  </div>
                </div>
              </article>
            </div>
            <MailMinimap
              v-if="minimapMarks.length"
              :marks="minimapMarks"
              :band="minimapVisibleBand"
              @jump="(mk) => jumpToMark(mk.id)"
            />
          </div>

          <!-- 回复框走统一的 Composer（设计稿 §8、§10）：急件开关由调用方声明。 -->
          <div class="reply" data-test="mail-reply-box">
            <Composer
              v-model="reply.body"
              v-model:urgent="reply.urgent"
              :placeholder="t('mail.reply.placeholder', { who: activeTitle })"
              :send-label="t('mail.reply.send')"
              :sending="replying"
              :can-send="!replyWriteBlock"
              :hint="replyWriteBlock || ''"
              :aria-label="t('mail.reply.send')"
              urgent-option
              @send="sendReply"
            />
            <p v-if="replyError" class="err" data-test="mail-reply-error">{{ replyError }}</p>
            <p v-if="readdressError" class="err" data-test="mail-readdress-error">{{ readdressError }}</p>
          </div>
        </template>
        <p v-else class="dim pad">{{ t('mail.thread.select') }}</p>
      </section>
    </div>

    <p v-if="notice" class="notice" data-test="mail-notice">{{ notice }}</p>
    <p v-if="error" class="err" data-test="mail-error">{{ error }}</p>

    <!-- 写信：收件人选择、投递数预览、正文与急件都放进统一的底部抽屉（设计稿 §7.1、§8）。 -->
    <Sheet v-model="composeOpen" :label="t('mail.compose.title')" data-test="mail-compose-sheet">
      <form class="compose" data-test="mail-compose" @submit.prevent="sendCompose">
        <!-- 收件人（gofer-ndy5，设计 2026-10-22「D5」）：已选的显示为可删除标签，下面是会话多选
             与「按项目 / 主题 / agent 全部」选器，手写地址收在高级里。 -->
        <div class="compose-tags" data-test="mail-to-tags">
          <span v-for="a in composeTags" :key="a" class="tag-chip mono" data-test="mail-to-tag">
            {{ addressLabel(a) }}
            <button class="chip-x" type="button" :title="t('mail.compose.remove', { address: a })" data-test="mail-to-tag-x" @click="removeComposeAddress(a)"><Icon :icon="X" size="sm" /></button>
          </span>
          <span v-if="!composeTags.length" class="dim mono">{{ t('mail.compose.none') }}</span>
        </div>
        <p v-if="!composeAddresses.ok" class="err" data-test="mail-to-error">{{ composeAddresses.error }}</p>

        <div class="compose-pick">
          <input
            v-model="composeSearch"
            class="compose-to mono"
            type="text"
            :placeholder="t('mail.compose.search')"
            data-test="mail-session-search"
          />
          <div class="pick-list" data-test="mail-session-options">
            <label v-for="s in composeSessionOptions" :key="s.session_id" class="pick-row mono">
              <input
                type="checkbox"
                :checked="compose.sessions.includes(s.session_id)"
                :data-test="`mail-session-${s.session_id}`"
                @change="toggleComposeSession(s.session_id)"
              />
              <span class="pick-label">{{ mailSessionOptionLabel(s) }}</span>
            </label>
            <p v-if="!composeSessionOptions.length" class="dim mono">{{ t('mail.compose.noSessions') }}</p>
          </div>
        </div>

        <div class="compose-scope">
          <select v-model="scopePick.scheme" class="compose-to mono" data-test="mail-scope-scheme">
            <option value="project">{{ t('mail.scope.project') }}</option>
            <option value="topic">{{ t('mail.scope.topic') }}</option>
            <option value="agent">{{ t('mail.scope.agent') }}</option>
            <option value="path">{{ t('mail.scope.path') }}</option>
          </select>
          <input
            v-model="scopePick.value"
            class="compose-to mono"
            type="text"
            :placeholder="t(`mail.scopeHint.${scopePick.scheme}`)"
            data-test="mail-scope-value"
            @keydown.enter.prevent="addComposeScope"
          />
          <button class="act mono" type="button" data-test="mail-scope-add" @click="addComposeScope">
            {{ t('mail.compose.scopeAdd') }}
          </button>
        </div>

        <p v-if="composePreviewError" class="err" data-test="mail-preview-error">{{ composePreviewError }}</p>

        <details class="compose-advanced">
          <summary class="mono">{{ t('mail.compose.advanced') }}</summary>
          <input
            v-model="compose.manual"
            class="compose-to mono"
            type="text"
            :placeholder="t('mail.compose.manual')"
            data-test="mail-to"
          />
        </details>

        <Composer
          v-model="compose.body"
          v-model:urgent="compose.urgent"
          :placeholder="t('mail.compose.body')"
          :send-label="t('mail.compose.send')"
          :sending="sending"
          :aria-label="t('mail.compose.send')"
          urgent-option
          @send="sendCompose"
        />
        <span class="compose-preview mono" data-test="mail-to-preview">{{ composePreviewText }}</span>
        <p v-if="composeError" class="err" data-test="mail-compose-error">{{ composeError }}</p>
      </form>
    </Sheet>

    <SessionDrawer v-if="openSid" :sid="openSid" @close="openSid = ''" @changed="load(true)" />
  </section>
</template>

<style scoped>
/* 整页占满顶栏以下的视口（App.vue 的 content--fill）：左右两栏自己滚，页面不出现外层滚动条。 */
.mail-page {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
  min-width: 0;
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}
/* 与预览同形的两栏：320px 列表 + 自适应线程；1279–1100 收窄到 270px。 */
.split {
  display: grid;
  grid-template-columns: 340px minmax(0, 1fr);
  gap: 0;
  align-items: stretch;
  min-width: 0;
  flex: 1 1 auto;
  min-height: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface);
  overflow: hidden;
}
.mail-list {
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  border-right: 1px solid var(--line);
  background: var(--surface-2);
}
.list-head {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  padding: var(--s3);
  border-bottom: 1px solid var(--line);
}
.head-row,
.head-filters {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s2);
  min-width: 0;
}
.mail-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: var(--fs-lg);
  margin: 0;
  color: var(--ink);
  letter-spacing: -0.015em;
}
.filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 3px 8px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.chip-x {
  background: none;
  border: 0;
  color: var(--fail);
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
  cursor: pointer;
}
.convs {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: var(--s1);
}
.conv {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 2px 8px;
  text-align: left;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  color: var(--ink);
  cursor: pointer;
}
.conv:hover {
  background: var(--sunken);
}
.conv--active {
  background: var(--surface);
  border-color: var(--line);
  box-shadow: var(--shadow-1);
}
.conv:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.conv-people {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.conv-arrow {
  font-size: var(--fs-xs);
}
.conv-excerpt,
.conv-meta {
  font-size: var(--fs-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.detail {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
  min-height: 0;
  padding: var(--s2);
}
.detail-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s2);
  border-bottom: 1px solid var(--line);
  padding-bottom: var(--s2);
}
.back {
  display: none;
}
.timeline-wrap {
  position: relative;
  display: flex;
  gap: var(--s1);
  flex: 1 1 auto;
  min-height: 0;
}
.timeline {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: var(--s2) 4px var(--s2) 0;
}
/* 分页入口：时间线顶端一行（还有更早的就出提示 + 按钮，到底了说一声）。
   这一行占满整行、不叠在气泡上（截图检查 P6-2）。 */
.older {
  display: flex;
  justify-content: center;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1) var(--s2);
  flex: 0 0 auto;
}
.older .act {
  font-size: var(--fs-xs);
  padding: 2px 8px;
}
.older-hint {
  font-size: var(--fs-xs);
}
/* 气泡：自己的一方在右（--right），对方在左；回信用虚线边，急件勾红线。
   宽度取对话区的大部分（max-width 用 ch 限制超宽屏上的行长），手机宽度下满宽。 */
.bubble {
  max-width: min(94%, 96ch);
  background: var(--surface);
  border: 1px solid var(--line);
  border-left: 2px solid var(--line-strong);
  border-radius: 4px var(--r-panel) var(--r-panel) var(--r-panel);
  padding: var(--s2) var(--s3);
  transition: outline-color 0.2s;
}
.bubble--left {
  align-self: flex-start;
}
.bubble--right {
  align-self: flex-end;
  border-left-color: var(--accent);
  border-radius: var(--r-panel) 4px var(--r-panel) var(--r-panel);
  background: var(--accent-soft);
}
.bubble--reply {
  border-style: dashed;
}
.bubble--urgent {
  border-right: 2px solid var(--fail);
}
.bubble--hot {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
.bubble-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.bubble-to {
  font-size: var(--fs-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 24ch;
  white-space: nowrap;
}
.reply-ref {
  background: none;
  border: 1px dashed var(--line);
  border-radius: 999px;
  color: var(--accent);
  font-size: 10px;
  padding: 0 6px;
  cursor: pointer;
}
.bubble-body {
  margin-top: 6px;
  font-size: var(--fs-md);
  color: var(--ink);
}
.fold {
  margin-top: 4px;
  background: none;
  border: 0;
  color: var(--accent);
  font-size: var(--fs-xs);
  padding: 0;
  cursor: pointer;
}
.held {
  margin-top: var(--s2);
  border-top: 1px dashed var(--line);
  padding-top: var(--s2);
}
.held-why {
  margin: 0 0 6px;
  font-size: var(--fs-xs);
  color: var(--need-ink);
}
.cands {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
  font-size: var(--fs-xs);
}
.cand {
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--accent);
  padding: 2px 8px;
  font-size: var(--fs-xs);
  cursor: pointer;
}
.readdress {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.readdress input {
  flex: 1 1 240px;
}
/* 回复框固定在对话底部（Composer 自带面板外形，这里只留间距）。 */
.reply {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  flex: 0 0 auto;
  min-width: 0;
}
/* 广播（设计 2026-10-22）：副本卡上的收件人状态条。 */
.broadcast {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin: 4px 0 6px;
}
.broadcast-head {
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.broadcast-dot {
  font-size: var(--fs-xs);
  padding: 0 5px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink-3);
}
/* 写信框（Sheet 内容）：内容多时自己滚，不把抽屉撑出视口。 */
.compose {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
}
.compose-to,
.readdress input {
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  color: var(--ink);
  padding: 6px 8px;
  font-size: var(--fs-sm);
}
.compose-tags {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.tag-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 2px 6px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.compose-preview {
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.compose-pick {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.pick-list {
  max-height: 150px;
  overflow-y: auto;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface-2);
  padding: 4px 6px;
}
.pick-row {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: var(--fs-xs);
  color: var(--ink);
  padding: 2px 0;
}
.pick-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.compose-scope {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.compose-scope .compose-to {
  flex: 1 1 160px;
  min-width: 0;
}
.compose-advanced summary {
  cursor: pointer;
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.compose-advanced input {
  width: 100%;
  margin-top: 6px;
}
.tag {
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 0 5px;
  font-size: 10px;
}
.tag--urgent {
  color: var(--fail);
}
.tag--held {
  color: var(--need-ink);
  border-color: var(--need);
}
.unread {
  min-width: 18px;
  height: 18px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--accent-ink);
  font-size: 11px;
  font-weight: 600;
  text-align: center;
  padding: 0 5px;
  display: inline-grid;
  place-items: center;
}
.tone--run {
  color: var(--run);
}
.tone--done {
  color: var(--done);
}
.tone--fail {
  color: var(--fail);
}
.tone--queue {
  color: var(--ink-3);
}
.act {
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  color: var(--ink);
  padding: 4px 10px;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.act--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.err {
  color: var(--fail);
  font-size: var(--fs-xs);
  margin: 0;
}
.notice {
  color: var(--done);
  font-size: var(--fs-xs);
  margin: 0;
}
.dim {
  color: var(--ink-3);
}
.pad {
  padding: 8px 0;
  font-size: var(--fs-xs);
}
.held-item {
  margin-left: 0.5em;
  color: var(--accent);
}

/* 手机（≤760，§4.3）：列表与时间线两级切换；气泡占满宽度、操作按钮铺满。 */
@media (max-width: 760px) {
  .split {
    grid-template-columns: minmax(0, 1fr);
  }
  .mail-list {
    border-right: 0;
  }
  .mail-list .convs {
    min-height: 0;
  }
  .split:not(.split--detail) .detail {
    display: none;
  }
  .split--detail .mail-list {
    display: none;
  }
  .back {
    display: inline-flex;
  }
  /* 手机上小地图不画（见 MailMinimap）；「还有更早」的提示本来就在时间线顶端那一行，不占气泡。 */
  .bubble {
    max-width: 100%;
  }
  .head-row .act,
  .held .act,
  .readdress .act {
    min-height: 40px;
  }
  .conv {
    min-height: 56px;
  }
}
</style>
