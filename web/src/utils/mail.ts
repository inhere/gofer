// 信箱页（design 2026-10-16「二、gofer 信箱」，计划 M7）的纯逻辑：地址解析与校验、
// 对话串分组与排序、方向 / 状态 / 对方文案、列表查询参数。组件只负责渲染与发请求，
// 规则都在这里，便于在 node 环境下单测。
import type { MailMessage, MailParty } from '../api/types'

// 页内路由与深链：Today 的「信件待指派」卡与 /v1/today 的卡片动作都指向 MAIL_HELD_LINK。
export const MAIL_PAGE_LINK = '/mail'
export const MAIL_HELD_LINK = '/mail?state=held'

// 单封上限：与服务端同一数值（32 KB），前端先拦一次，省一次往返。
export const MAIL_BODY_MAX = 32 * 1024

// ---- 地址 ----

export type MailScheme = 'session' | 'job' | 'topic' | 'path' | 'caller'

export interface ParsedMailAddress {
  ok: boolean
  // 识别出的前缀；'' 表示没写前缀（此时 ok=false）。
  scheme: MailScheme | ''
  value: string
  // 规范化后的地址，直接作为 POST /v1/mail 的 to；caller 一律写成 'caller'。
  address: string
  error: string
}

// 地址表（设计「地址写法」）：会话 / 活着的 session job / 会话主题 / 会话工作区子路径，
// 或 caller（调用方自己的收集箱；空串同义）。这里只判**形状**——能否唯一命中由服务端解析，
// 猜错了会把信投给不该收的人，所以多命中时服务端存「待指派」。
export function parseMailAddress(input: string): ParsedMailAddress {
  const raw = (input ?? '').trim()
  if (raw === '' || raw.toLowerCase() === 'caller') {
    return { ok: true, scheme: 'caller', value: '', address: 'caller', error: '' }
  }
  const sep = raw.indexOf(':')
  if (sep < 0) {
    return {
      ok: false, scheme: '', value: raw, address: '',
      error: '地址要带前缀：session: / job: / topic: / path: / caller',
    }
  }
  const prefix = raw.slice(0, sep).trim().toLowerCase()
  const value = raw.slice(sep + 1).trim()
  switch (prefix) {
    case 'session':
    case 'job':
    case 'topic':
    case 'path': {
      if (!value) {
        return { ok: false, scheme: prefix, value, address: '', error: `${prefix}: 后面要写目标` }
      }
      if (/\s/.test(value)) {
        return { ok: false, scheme: prefix, value, address: '', error: '地址中间不能有空白' }
      }
      return { ok: true, scheme: prefix, value, address: `${prefix}:${value}`, error: '' }
    }
    case 'caller': {
      if (value) {
        return { ok: false, scheme: 'caller', value, address: '', error: 'caller 后面不带值' }
      }
      return { ok: true, scheme: 'caller', value: '', address: 'caller', error: '' }
    }
    default:
      // agent: 等在第三期才开放；明确说清而不是让服务端回一个 400。
      return {
        ok: false, scheme: '', value, address: '',
        error: `不支持的地址前缀 ${prefix}:（可用 session: / job: / topic: / path: / caller）`,
      }
  }
}

// ---- 写信 / 回信 / 改投的请求体 ----

export interface MailComposeForm {
  // 手写地址串，或写信框拼好的地址数组（会话多选 + 「全部」选器 + 手写）。
  to: string | string[]
  body: string
  urgent: boolean
}

export interface MailSendBody {
  // 一个地址是不带前缀 / 数组两种写法的旧形态；多个地址（或 each: 选择器）是群发。
  to: string | string[]
  body: string
  urgent?: boolean
}

// ---- 收件人（群发，设计 2026-10-22「D1」）----

// 可选的选择器：`each:` 前缀取全部可投递命中。
const EACH_SCHEMES = new Set(['topic', 'path', 'project', 'agent'])

export interface MailRecipients {
  ok: boolean
  // 规范化后的地址；空选择 = 自己的收集箱（caller）。
  addresses: string[]
  error: string
}

// parseMailRecipients 把写信框的收件人拆成地址表：逗号 / 空白分隔，每一项要么是普通地址
// （session: / job: / topic: / path: / caller），要么是 each:<选择器>。多个地址就是群发；
// 能否命中、能不能投递由服务端解析，这里只判形状。
export function parseMailRecipients(input: string): MailRecipients {
  const raw = (input ?? '').trim()
  if (raw === '') return { ok: true, addresses: ['caller'], error: '' }
  const parts = raw.split(/[\s,]+/).filter(Boolean)
  const out: string[] = []
  for (const part of parts) {
    if (/^each:/i.test(part)) {
      const rest = part.slice('each:'.length)
      const sep = rest.indexOf(':')
      const scheme = sep < 0 ? '' : rest.slice(0, sep).trim().toLowerCase()
      const value = sep < 0 ? '' : rest.slice(sep + 1).trim()
      if (!EACH_SCHEMES.has(scheme) || !value || /\s/.test(value)) {
        return { ok: false, addresses: [], error: 'each: 后面要写 topic: / path: / project: / agent: 选择器，例如 each:project:gofer' }
      }
      out.push(`each:${scheme}:${value}`)
      continue
    }
    const addr = parseMailAddress(part)
    if (!addr.ok) return { ok: false, addresses: [], error: addr.error }
    out.push(addr.address)
  }
  return { ok: true, addresses: out, error: '' }
}

// PreviewSession 是预览只需要的那几个会话字段（GET /v1/sessions 的一行）。
export interface PreviewSession {
  session_id: string
  agent?: string
  project_key?: string
  topic?: string
}

// mailRecipientPreview 预览这封信大约投给几个会话：显式地址按个数算，each:project: /
// each:agent: / each:topic: 按已加载的会话匹配。服务端还会按权限再筛，所以这只是上限；
// each:path: 需要服务端的范围匹配，这里不估（返回 -1 表示「里面有路径选择器，以服务端为准」）。
export function mailRecipientPreview(input: string, sessions: PreviewSession[]): number {
  const parsed = parseMailRecipients(input)
  if (!parsed.ok) return 0
  const seen = new Set<string>()
  let unknown = false
  for (const a of parsed.addresses) {
    if (!a.startsWith('each:')) {
      // 一个显式会话地址与 each:project: 选中的同一个会话只算一次。
      seen.add(a.startsWith('session:') ? a.slice('session:'.length) : a)
      continue
    }
    const [, scheme, ...rest] = a.split(':')
    const value = rest.join(':')
    if (scheme === 'path') {
      unknown = true
      continue
    }
    for (const s of sessions) {
      if (scheme === 'project' && s.project_key === value) seen.add(s.session_id)
      else if (scheme === 'agent' && s.agent === value) seen.add(s.session_id)
      else if (scheme === 'topic' && (s.topic ?? '').includes(value)) seen.add(s.session_id)
    }
  }
  return unknown ? -1 : seen.size
}

// ---- 写信框的收件人（gofer-ndy5：会话多选 + 「按项目 / 主题 / agent 全部」+ 手写地址）----

// MailScope 是一种「全部」选器：项目 / 主题 / agent / 子路径，生成 each:<scheme>:<value>。
export interface MailScope {
  scheme: 'project' | 'topic' | 'agent' | 'path'
  value: string
}

// MailComposeState 是写信框选出来的收件人：会话 id 多选、若干个「全部」选器、手写地址（高级）；
// 正文与急件也放这里，写信框就一个状态对象。
export interface MailComposeState {
  sessions: string[]
  scopes: MailScope[]
  manual: string
  body: string
  urgent: boolean
}

export function emptyMailCompose(): MailComposeState {
  return { sessions: [], scopes: [], manual: '', body: '', urgent: false }
}

// mailComposeAddresses 把写信框的状态拼成地址表（保序去重）：每个选中会话一个 session:<sid>，
// 每个选器一个 each:<scheme>:<value>，手写部分按老规则解析。什么都没选 = 自己的收集箱（caller）。
export function mailComposeAddresses(st: MailComposeState): MailRecipients {
  const out: string[] = []
  const push = (a: string): void => {
    if (a && !out.includes(a)) out.push(a)
  }
  for (const sid of st.sessions) {
    const v = (sid ?? '').trim()
    if (v) push(`session:${v}`)
  }
  for (const sc of st.scopes) {
    const scheme = (sc.scheme ?? '').trim().toLowerCase()
    const value = (sc.value ?? '').trim()
    if (!EACH_SCHEMES.has(scheme) || !value || /\s/.test(value)) {
      return { ok: false, addresses: [], error: `「全部」选器要填值（按${mailScopeLabel(scheme as MailScope['scheme'])}），且不能有空白` }
    }
    push(`each:${scheme}:${value}`)
  }
  const manual = (st.manual ?? '').trim()
  if (manual) {
    const parsed = parseMailRecipients(manual)
    if (!parsed.ok) return { ok: false, addresses: [], error: parsed.error }
    // 手写部分为空时 parseMailRecipients 回 caller；这里只有填了手写才进来，照常接受。
    for (const a of parsed.addresses) push(a)
  }
  if (!out.length) return { ok: true, addresses: ['caller'], error: '' }
  return { ok: true, addresses: out, error: '' }
}

// 选器的人话名称（下拉与提示共用）。
export const MAIL_SCOPES: Array<{ key: MailScope['scheme']; label: string; hint: string }> = [
  { key: 'project', label: '按项目全部', hint: '项目 key，例如 <project>' },
  { key: 'topic', label: '按主题全部', hint: '主题关键词，例如 login' },
  { key: 'agent', label: '按 agent 全部', hint: 'agent 名，例如 claude / codex' },
  { key: 'path', label: '按工作区子路径全部', hint: '子路径，例如 web/src' },
]

export function mailScopeLabel(scheme: MailScope['scheme'] | ''): string {
  const label = MAIL_SCOPES.find((s) => s.key === scheme)?.label ?? String(scheme)
  return label.replace(/^按/, '').replace(/全部$/, '').trim()
}

// mailAddressLabel 是已选收件人标签上的文字：会话短 id、job id，选器写「项目 X 全部」。
export function mailAddressLabel(address: string): string {
  if (address === 'caller') return '我自己的收集箱'
  if (address.startsWith('each:')) {
    const [, scheme, ...rest] = address.split(':')
    const value = rest.join(':')
    return `${mailScopeLabel(scheme as MailScope['scheme'])} ${value} 全部`
  }
  const sep = address.indexOf(':')
  const scheme = sep < 0 ? '' : address.slice(0, sep)
  const value = sep < 0 ? address : address.slice(sep + 1)
  switch (scheme) {
    case 'session':
      return `会话 ${value}`
    case 'job':
      return `job ${value}`
    case 'topic':
      return `主题 ${value}`
    case 'path':
      return `路径 ${value}`
  }
  return address
}

// ComposeSessionOption 是会话多选里的一行（GET /v1/sessions 的一行 + 标题）。
export type ComposeSessionOption = PreviewSession & { title?: string }

// filterComposeSessions 按搜索词过滤会话多选的可选项（会话 id / 标题 / agent / 项目 / 主题）。
export function filterComposeSessions(sessions: ComposeSessionOption[], q: string): ComposeSessionOption[] {
  const needle = (q ?? '').trim().toLowerCase()
  if (!needle) return sessions
  return sessions.filter((s) =>
    [s.session_id, s.title, s.agent, s.project_key, s.topic].some((v) => (v ?? '').toLowerCase().includes(needle)),
  )
}

// mailSessionOptionLabel 是会话多选里的一行：会话名（短 id）+ 标题 + agent + 项目。
export function mailSessionOptionLabel(s: PreviewSession & { title?: string; state?: string }): string {
  const bits = [s.title || '(无标题)', s.agent || '—', s.project_key || '(无项目)']
  return `${shortId(s.session_id)} · ${bits.join(' · ')}`
}

// mailComposePreviewText 是发送按钮上的预览文案：有服务端的 dry_run 回答就用它（「将投递 M 个
// 会话（略过 K 个）」），否则显示本地估算或等待中。
export interface MailPreviewCount {
  delivered: number
  skipped: number
  matched: number
}

export function mailComposePreviewText(
  addresses: string[],
  preview: MailPreviewCount | null,
  localEstimate: number,
): string {
  if (!addresses.length || (addresses.length === 1 && addresses[0] === 'caller')) {
    return '收件人留空 = 投给自己的收集箱'
  }
  if (preview) {
    const skip = preview.skipped > 0 ? `（略过 ${preview.skipped} 个）` : ''
    return `将投递 ${preview.delivered} 个会话${skip}`
  }
  if (localEstimate < 0) return '含 path: 选择器：投递数由服务端按范围匹配'
  return `约 ${localEstimate} 个会话（服务端确认中…）`
}

export interface MailBuild {
  req: MailSendBody | null
  error: string
}

// buildMailSend 校验写信表单并构造 POST /v1/mail 的请求体（urgent 只在勾选时带上）。to 可以是
// 手写地址串（老写法）或写信框拼好的地址数组。
export function buildMailSend(f: MailComposeForm): MailBuild {
  const body = f.body.trim()
  if (!body) return { req: null, error: '先写点内容' }
  if (new TextEncoder().encode(body).length > MAIL_BODY_MAX) {
    return { req: null, error: `正文最多 ${MAIL_BODY_MAX} 字节` }
  }
  const addr = Array.isArray(f.to)
    ? { ok: true, addresses: f.to, error: '' }
    : parseMailRecipients(f.to)
  if (!addr.ok) return { req: null, error: addr.error }
  if (!addr.addresses.length) return { req: null, error: '没有收件人' }
  // 一个普通地址保持原来的字符串写法；多个地址 / any each: 选择器发成数组（群发）。
  const broadcast = addr.addresses.length > 1 || addr.addresses.some((a) => a.startsWith('each:'))
  const req: MailSendBody = { to: broadcast ? addr.addresses : addr.addresses[0], body }
  if (f.urgent) req.urgent = true
  return { req, error: '' }
}

// buildMailReply 校验回复正文（地址由原信决定，不用填）。
export function buildMailReply(body: string, urgent = false): { req: { body: string; urgent?: boolean } | null; error: string } {
  const text = body.trim()
  if (!text) return { req: null, error: '先写点内容' }
  if (new TextEncoder().encode(text).length > MAIL_BODY_MAX) {
    return { req: null, error: `正文最多 ${MAIL_BODY_MAX} 字节` }
  }
  return { req: urgent ? { body: text, urgent: true } : { body: text }, error: '' }
}

// buildMailReaddress 校验「改投」的目标地址。
export function buildMailReaddress(to: string): { req: { to: string } | null; error: string } {
  const addr = parseMailAddress(to)
  if (!addr.ok) return { req: null, error: addr.error }
  return { req: { to: addr.address }, error: '' }
}

// ---- 文案 ----

// 状态文案（服务端的信件状态机：pending / delivering / delivered / answered / held /
// expired / failed / cancelled）。
export const MAIL_STATE_LABEL: Record<string, string> = {
  pending: '待送达',
  delivering: '投递中',
  delivered: '已送达',
  answered: '已答复',
  held: '待指派',
  expired: '已过期',
  failed: '投递失败',
  cancelled: '已撤回',
}

export function mailStateLabel(state: string): string {
  return MAIL_STATE_LABEL[state] ?? (state || '—')
}

// 状态配色用的语义色名（页面映射到 styles/tokens.css 的变量）。
export type MailTone = 'run' | 'done' | 'fail' | 'queue'

export function mailStateTone(state: string): MailTone {
  switch (state) {
    case 'pending':
    case 'delivering':
      return 'run'
    case 'delivered':
    case 'answered':
      return 'done'
    case 'held':
      return 'queue'
    case 'expired':
    case 'failed':
    case 'cancelled':
      return 'fail'
  }
  return 'queue'
}

function shortId(id: string | undefined): string {
  return id ? id.slice(0, 12) : '—'
}

// 一方（出处 / 收件方）的可读名：服务端给了 display 就用它，否则按类型拼。
export function mailPartyLabel(p: MailParty | undefined): string {
  if (!p) return '未知'
  if (p.display) return p.display
  switch (p.kind) {
    case 'session':
      return `会话 ${shortId(p.id)}`
    case 'job':
      return `job ${shortId(p.id)}`
    case 'caller':
      return `调用方 ${p.caller || p.id || '—'}`
  }
  return p.id ? shortId(p.id) : '未知'
}

// 方向：web 只认得调用方（弹出来自是谁），抽屉里再带上当前会话，免得自己的会话发的
// 信被显示成别人给的。
export interface MailIdentity {
  caller?: string
  session?: string
}

export function mailOutgoing(m: MailMessage, me: MailIdentity = {}): boolean {
  if (me.session && m.from.kind === 'session' && m.from.id === me.session) return true
  return !!me.caller && !!m.from.caller && m.from.caller === me.caller
}

// 对方的名字：我发的看收件地址（收件会话 / job 的 display 服务端不给），别人发的看出处。
export function mailPeerLabel(m: MailMessage, me: MailIdentity = {}): string {
  if (!mailOutgoing(m, me)) return mailPartyLabel(m.from)
  return m.to_address || mailPartyLabel(m.to)
}

// 「读」= 收件方确认过：pending / delivering / delivered 且没有 read_at 的都算未读
// （待指派、已撤回、已过期的信不挂在任何人的收件箱里）。
export function mailUnread(m: MailMessage): boolean {
  if (m.read_at) return false
  return m.state === 'pending' || m.state === 'delivering' || m.state === 'delivered'
}

// 列表里的正文摘录：压成一行再截断。
export function mailExcerpt(body: string, max = 120): string {
  const one = (body ?? '').replace(/\s+/g, ' ').trim()
  const runes = [...one]
  return runes.length > max ? `${runes.slice(0, max).join('')}…` : one
}

// ---- 对话串 ----

export interface MailThread {
  threadId: string
  // 串内从旧到新（前端按时间顺序渲染）。
  mails: MailMessage[]
  latest: MailMessage
  // 对方（串里最新一封不是我发的信的出处；只有我发的就取收件地址）。
  peer: string
  state: string
  stateLabel: string
  urgent: boolean
  unread: number
  activityAt: number
}

// groupMailThreads 按 thread_id 分组、串内按 id 升序、串按最近活动倒序（列表就是
// 「最近有往来的串在最上面」）。没有 thread_id 的回退成自身一封一串。
export function groupMailThreads(mails: MailMessage[], me: MailIdentity = {}): MailThread[] {
  const byThread = new Map<string, MailMessage[]>()
  for (const m of mails) {
    const key = m.thread_id || `m${m.id}`
    const list = byThread.get(key)
    if (list) list.push(m)
    else byThread.set(key, [m])
  }
  const out: MailThread[] = []
  for (const [threadId, list] of byThread) {
    const sorted = [...list].sort((a, b) => a.id - b.id)
    const latest = sorted[sorted.length - 1]
    const incoming = sorted.filter((m) => !mailOutgoing(m, me))
    const peer = incoming.length
      ? mailPartyLabel(incoming[incoming.length - 1].from)
      : mailPeerLabel(latest, me)
    out.push({
      threadId,
      mails: sorted,
      latest,
      peer,
      state: latest.state,
      stateLabel: mailStateLabel(latest.state),
      urgent: sorted.some((m) => !!m.urgent),
      unread: sorted.filter(mailUnread).length,
      activityAt: latest.created_at,
    })
  }
  return out.sort((a, b) => b.activityAt - a.activityAt || b.latest.id - a.latest.id)
}

// ---- 过滤与查询参数 ----

export type MailFilter = 'all' | 'in' | 'out' | 'held'

export const MAIL_FILTERS: Array<{ key: MailFilter; label: string }> = [
  { key: 'all', label: '全部' },
  { key: 'in', label: '收件' },
  { key: 'out', label: '发件' },
  { key: 'held', label: '待指派' },
]

function first(v: unknown): string {
  const x = Array.isArray(v) ? v[0] : v
  return typeof x === 'string' ? x.trim() : ''
}

export function isMailFilter(v: string): v is MailFilter {
  return v === 'all' || v === 'in' || v === 'out' || v === 'held'
}

// mailFilterFromQuery：?box=in|out 是箱，?state=held（Today 卡的深链）与 ?filter=… 直接
// 当页签；带 session 时缺省看全部（会话抽屉点进来要看到这个会话的所有往来）。
export function mailFilterFromQuery(query: { box?: unknown; state?: unknown; filter?: unknown; session?: unknown }): MailFilter {
  const box = first(query.box).toLowerCase()
  if (box === 'in' || box === 'out') return box
  const filter = first(query.filter).toLowerCase()
  if (isMailFilter(filter)) return filter
  if (first(query.state).toLowerCase() === 'held') return 'held'
  if (first(query.session)) return 'all'
  return 'all'
}

export interface MailListQuery {
  box: 'in' | 'out' | 'all'
  state?: string[]
  session?: string
  thread?: string
  limit: number
  offset?: number
}

// mailListQuery 把页签映射成 GET /v1/mail 的参数：「待指派」是一个**状态**过滤，箱子两边
// 都要（我发的信也可能没投出去）。session / thread 由深链带来的固定过滤。
export function mailListQuery(
  filter: MailFilter,
  opts: { session?: string; thread?: string; limit?: number; offset?: number } = {},
): MailListQuery {
  const q: MailListQuery = { box: filter === 'in' ? 'in' : filter === 'out' ? 'out' : 'all', limit: opts.limit ?? 200 }
  if (filter === 'held') q.state = ['held']
  if (opts.session) q.session = opts.session
  if (opts.thread) q.thread = opts.thread
  if (opts.offset) q.offset = opts.offset
  return q
}

// 顶栏信封按钮（components/MailButton.vue）的计数查询与文案：待指派（held）的信件要我去
// 处理，计数上限取服务端列表的 limit 上限（200），超过只显示 99+；箱子两边都要（我发的信
// 也可能没投出去）。
export function mailHeldQuery(): MailListQuery {
  return mailListQuery('held')
}

// 徽标文案：0 由调用方决定不渲染，这里只压 99+（与「等我」徽标同口径）。
export function mailBadgeLabel(count: number): string {
  return count > 99 ? '99+' : String(count)
}

// 一个串的收件侧代表信（用来回复的那封）：串里最新一封不是我发的；都是自己发的就用
// 最新一封（回复仍会送回原收件地址）。
export function mailReplyTarget(thread: MailThread, me: MailIdentity = {}): MailMessage {
  const incoming = thread.mails.filter((m) => !mailOutgoing(m, me))
  return incoming.length ? incoming[incoming.length - 1] : thread.latest
}

// 待指派信件能否在这里处理：held 才可改投 / 撤回。
export function mailCanReaddress(m: MailMessage): boolean {
  return m.state === 'held'
}
