// 信箱「按对话显示」的纯逻辑（design 2026-10-20-mail-conversations-ui-design.md，计划 M2）：
// 对话 key（参与方对、方向无关）、气泡方向、人物卡的取色 / 名字 / 副行 / 状态、摘要去
// markdown、回信标号。组件只负责渲染，规则都在这里，便于在 node 环境下单测。
import { fmtAgo } from '../api/time'
import { t } from '../i18n'
import type { MailConversation, MailMessage, MailParty } from '../api/types'
import { mailExcerpt, mailOutgoing, mailPartyLabel, type MailFilter, type MailIdentity } from './mail'

// ---- 参与方地址 ----

// mailPartyAddress 是一端的规范地址（与服务端 mail.AddressOf 同口径）：session:<sid> /
// job:<id> / caller:<id>；待指派信用的是没解析到人的原文地址，服务端把它放在 display 里。
export function mailPartyAddress(p: MailParty | undefined): string {
  if (!p) return ''
  switch (p.kind) {
    case 'session':
      return p.id ? `session:${p.id}` : ''
    case 'job':
      return p.id ? `job:${p.id}` : ''
    case 'caller':
      return p.caller ? `caller:${p.caller}` : 'caller'
  }
  return (p.display ?? '').trim()
}

// conversationKey 是一个对话的 key：两端地址排序后拼接，from→to 与 to→from 得到同一个 key
// （服务端也是这个口径，前端分组出来的 key 能直接对上服务端返回的 key）。
export function conversationKey(a: string, b: string): string {
  return a <= b ? `${a}|${b}` : `${b}|${a}`
}

// mailConversationKey 是一封信所属对话的 key。
export function mailConversationKey(m: MailMessage): string {
  return conversationKey(mailPartyAddress(m.from), mailPartyAddress(m.to))
}

// mailAddressFetchable：GET /v1/mail?peer=&with= 只接受 session: / job: / caller: 三种地址
// （topic: / path: 这类没解析到人的地址会被 400），这种对话要回退到本地过滤。
export function mailAddressFetchable(address: string): boolean {
  return /^(session|job|caller):/.test(address)
}

// ---- 对话 ----

// groupMailConversations 按参与方对把信件分组（方向无关）：多个 thread、双向往来都归成
// 一个对话。没有服务端 conversations 接口可用时（「待指派」页签、?session= 深链）用它本地分组。
export function groupMailConversations(mails: MailMessage[]): MailConversation[] {
  const byKey = new Map<string, MailMessage[]>()
  for (const m of mails) {
    const key = mailConversationKey(m)
    const list = byKey.get(key)
    if (list) list.push(m)
    else byKey.set(key, [m])
  }
  const out: MailConversation[] = []
  for (const [key, list] of byKey) {
    const sorted = sortMailTimeline(list)
    const first = sorted[0]
    const last = sorted[sorted.length - 1]
    out.push({
      key,
      a: first.from,
      b: first.to,
      last_mail: last,
      count: sorted.length,
      unread: sorted.filter(mailUnread).length,
      urgent: sorted.filter((m) => !!m.urgent).length,
      activity_at: last.created_at,
    })
  }
  return out.sort((x, y) => y.activity_at - x.activity_at || y.last_mail.id - x.last_mail.id)
}

// mailUnread 与服务端同口径：没有 read_at，且没有走到撤回 / 失败 / 过期 / 已答复。
export function mailUnread(m: MailMessage): boolean {
  if (m.read_at) return false
  switch (m.state) {
    case 'cancelled':
    case 'failed':
    case 'expired':
    case 'answered':
      return false
  }
  return true
}

// sortMailTimeline 把对话里的信按时间正序（同一秒按 id）：时间线就是从早到晚的一条线。
export function sortMailTimeline(mails: MailMessage[]): MailMessage[] {
  return [...mails].sort((a, b) => a.created_at - b.created_at || a.id - b.id)
}

// mailConversationInProject：这个对话是不是属于某个项目（UI 重设计 §6.4：信箱按参与方
// 所属项目过滤，conversations 接口没有项目参数，只能在本地收窄）。两端的 persona 任一属于
// 该项目即算——跨项目的对话出现在两个项目的筛选里，比「莫名消失」好解释。
export function mailConversationInProject(conv: MailConversation, project: string): boolean {
  const key = (project ?? '').trim()
  if (!key) return true
  return [conv.a?.persona?.project_key, conv.b?.persona?.project_key].some((p) => (p ?? '').trim() === key)
}

// mailConversationBox 把页签映射成对话列表 / 时间线的箱参数（「待指派」两边都要）。
export function mailConversationBox(filter: MailFilter): 'in' | 'out' | 'all' {
  return filter === 'in' ? 'in' : filter === 'out' ? 'out' : 'all'
}

// mailTimelineQuery 是一个对话时间线的取数参数。
export interface MailTimelineQuery {
  peer: string
  with: string
  box: 'in' | 'out' | 'all'
  state?: string[]
  // key 是本地过滤的回退口径（地址有 topic: / path: 时用）。
  key: string
}

// mailTimelineQuery 构造 GET /v1/mail?peer=&with= 的参数：「待指派」页签带上 state=held，
// 会话深链再收窄到那个会话。
export function mailTimelineQuery(conv: MailConversation, filter: MailFilter): MailTimelineQuery {
  const q: MailTimelineQuery = {
    peer: mailPartyAddress(conv.a),
    with: mailPartyAddress(conv.b),
    box: mailConversationBox(filter),
    key: conv.key,
  }
  if (filter === 'held') q.state = ['held']
  return q
}

// mailTimelineAddressable：这个对话的两端都能用 ?peer= / ?with= 表达（否则本地过滤）。
export function mailTimelineAddressable(q: MailTimelineQuery): boolean {
  return mailAddressFetchable(q.peer) && mailAddressFetchable(q.with)
}

// ---- 气泡方向（设计决策 4）----

export type MailBubbleSide = 'left' | 'right'

// mailAddressIsMine：这一端是不是当前查看者自己的一方（自己的会话 / 自己的收集箱）。
export function mailAddressIsMine(address: string, me: MailIdentity = {}): boolean {
  if (!address) return false
  if (me.session && address === `session:${me.session}`) return true
  if (me.caller && address === `caller:${me.caller}`) return true
  return false
}

// bubbleSide：自己发的一方在右、自己收的一方在左；两边都不是自己时，对话里先发信的一方
// （服务端把最早那封的发件方放在 a）在左，另一方在右。
export function bubbleSide(m: MailMessage, conv: MailConversation | null, me: MailIdentity = {}): MailBubbleSide {
  if (mailOutgoing(m, me)) return 'right'
  if (mailAddressIsMine(mailPartyAddress(m.to), me)) return 'left'
  if (!conv) return 'left'
  const first = mailPartyAddress(conv.a)
  if (!first) return 'left'
  return mailPartyAddress(m.from) === first ? 'left' : 'right'
}

// ---- 回信标号 ----

// mailConversationSeq 给对话里的每封信一个从 1 开始的顺序号（时间线位置）。
export function mailConversationSeq(mails: MailMessage[]): Map<number, number> {
  const seq = new Map<number, number>()
  sortMailTimeline(mails).forEach((m, i) => seq.set(m.id, i + 1))
  return seq
}

// mailReplyLabel 是回信的气泡角标：「回复 #N」（N 是被回复那封在对话里的位置）；不是回信给 ''。
export function mailReplyLabel(m: MailMessage, seq: Map<number, number>): string {
  if (!m.reply_to) return ''
  const n = seq.get(m.reply_to)
  return n ? t('mail.replyTo', { n }) : t('mail.reply')
}

// replyTarget 是这个对话的回复对象：最新一封不是自己发的信；都是自己发的就用最新一封
// （回复仍送回原发件方地址）。
export function replyTarget(mails: MailMessage[], me: MailIdentity = {}): MailMessage | null {
  const sorted = sortMailTimeline(mails)
  const incoming = sorted.filter((m) => !mailOutgoing(m, me))
  return incoming.length ? incoming[incoming.length - 1] : (sorted[sorted.length - 1] ?? null)
}

// ---- 人物卡 ----

// PERSONA_COLORS 是人物卡的固定色板：按 id 稳定取一色，深浅两套主题下都能读（中饱和、
// 与 token 里的 --paper / --ink 都有足够对比）。
export const PERSONA_COLORS = [
  '#4f7fe0',
  '#2f9163',
  '#c2841f',
  '#a85bbd',
  '#2b8fa3',
  '#c2544f',
  '#7d6fd0',
  '#5f8a33',
] as const

// personaSeed 是取色的种子：会话 / job 用它的 id，收集箱用调用方 id，人的地址就用地址本身。
export function personaSeed(p: MailParty | undefined): string {
  if (!p) return ''
  if (p.id) return `${p.kind}:${p.id}`
  if (p.caller) return `caller:${p.caller}`
  return mailPartyAddress(p)
}

// personaColor 按种子稳定取色：同一个 id 永远同一个颜色（列表与时间线一致）。
export function personaColor(seed: string): string {
  const s = seed || '?'
  let h = 0
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0
  return PERSONA_COLORS[h % PERSONA_COLORS.length]
}

// personaLabel 是人物卡的名字：服务端给的 persona.name；读不到人（persona 省略）时退回短 id。
export function personaLabel(p: MailParty | undefined): string {
  const name = (p?.persona?.name ?? '').trim()
  return name || mailPartyLabel(p)
}

// personaInitial 是头像上的首字。
export function personaInitial(name: string): string {
  const runes = [...(name ?? '').trim()]
  return runes.length ? runes[0] : '?'
}

// personaSubline 是副行：agent · 项目 · 执行机（缺的跳过；收集箱没有这些）。
export function personaSubline(p: MailParty | undefined): string {
  const persona = p?.persona
  if (!persona) return ''
  return [persona.agent, persona.project_key, persona.runner]
    .map((x) => (x ?? '').trim())
    .filter(Boolean)
    .join(' · ')
}

// 人物状态文案：会话是会话状态，job 是 job 状态，两者共用这张表（缺的照原样显示）。
export const PERSONA_STATE_LABEL: Record<string, string> = {
  get running() { return t('mail.persona.running') },
  get idle() { return t('mail.persona.idle') },
  get awaiting_input() { return t('mail.persona.awaiting_input') },
  get offline() { return t('mail.persona.offline') },
  get ended() { return t('mail.persona.ended') },
  get pending() { return t('mail.persona.queued') },
  get queued() { return t('mail.persona.queued') },
  get succeeded() { return t('mail.persona.done') },
  get done() { return t('mail.persona.done') },
  get failed() { return t('mail.persona.failed') },
  get cancelled() { return t('mail.persona.cancelled') },
  get canceled() { return t('mail.persona.cancelled') },
  get timeout() { return t('mail.persona.timeout') },
}

export function personaStateLabel(state: string): string {
  return PERSONA_STATE_LABEL[state] ?? (state || '')
}

// personaState 是人物卡状态点取的状态原文（没有 persona 就没有状态点）。
export function personaState(p: MailParty | undefined): string {
  return (p?.persona?.state ?? '').trim()
}

// personaStateTone 把状态映射到页面的语义色（tokens.css 的 --run / --done / --queue / --fail）。
export function personaStateTone(state: string): 'run' | 'done' | 'fail' | 'queue' {
  switch (state) {
    case 'running':
      return 'run'
    case 'idle':
    case 'succeeded':
    case 'done':
      return 'done'
    case 'failed':
    case 'timeout':
      return 'fail'
  }
  return 'queue'
}

// personaTooltip 是人物卡的悬停提示：完整地址（id）、cwd、最后活动时间。
export function personaTooltip(p: MailParty | undefined, nowSec = 0): string {
  if (!p) return ''
  const lines = [mailPartyAddress(p)]
  const persona = p.persona
  if (persona?.cwd) lines.push(persona.cwd)
  if (persona?.last_seen_at) lines.push(t('mail.persona.lastSeen', { ago: fmtAgo(persona.last_seen_at, nowSec) }))
  return lines.filter(Boolean).join('\n')
}

// ---- 摘要 ----

// stripMarkdown 去掉 markdown 标记，给列表里的摘要用（服务端信件正文是 markdown）。
export function stripMarkdown(text: string): string {
  let s = text ?? ''
  s = s.replace(/```[\s\S]*?```/g, ' ')
  s = s.replace(/~~~[\s\S]*?~~~/g, ' ')
  s = s.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
  s = s.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
  s = s.replace(/<[^>]+>/g, ' ')
  s = s.replace(/^\s{0,3}#{1,6}\s+/gm, '')
  s = s.replace(/^\s{0,3}>\s?/gm, '')
  s = s.replace(/^\s{0,3}(?:[-*+]|\d+[.)])\s+/gm, '')
  s = s.replace(/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/gm, ' ')
  s = s.replace(/`([^`]*)`/g, '$1')
  s = s.replace(/\*\*([^*]+)\*\*/g, '$1')
  s = s.replace(/__([^_]+)__/g, '$1')
  s = s.replace(/~~([^~]+)~~/g, '$1')
  s = s.replace(/(^|[\s(])\*([^*\n]+)\*/g, '$1$2')
  s = s.replace(/(^|[\s(])_([^_\n]+)_/g, '$1$2')
  return s.replace(/\s+/g, ' ').trim()
}

// mailSummary 是列表行的摘要：去掉 markdown 标记再压成一行截断。
export function mailSummary(body: string, max = 80): string {
  return mailExcerpt(stripMarkdown(body), max)
}

// 长信折叠的阈值（字符数）：超过就默认折叠，读完点开。
export const MAIL_LONG_BODY = 1200

// mailLongBody：这封信够长，默认折叠正文。
export function mailLongBody(body: string, max = MAIL_LONG_BODY): boolean {
  return [...(body ?? '')].length > max
}

// mailFoldedBody 是折叠状态下显示的正文（保留开头）。
export function mailFoldedBody(body: string, max = MAIL_LONG_BODY): string {
  const runes = [...(body ?? '')]
  return runes.length > max ? `${runes.slice(0, max).join('')}…` : (body ?? '')
}
