// N3「今天」决策队列的纯逻辑：分组、短标记、时间文案、卡片操作到现有写接口的映射、跳转目标。
// 组件只管渲染与交互，便于在 node 环境下单测。
import {
  ackSessionTurn,
  acceptJob,
  acceptWorkSuggestion,
  addWorkNote,
  answerDecision,
  answerInteraction,
  answerSessionPermission,
  approveJob,
  dismissWorkSuggestion,
  patchWorkItem,
  planResume,
  rejectJob,
  requestWorkReport,
  saySession,
  sendSessionMessage,
} from '../api/client'
import { acceptMergeSuggestion, dismissMergeSuggestion } from '../api/steward'
import { CAPTURE_BOX_LINK } from './capture'
import { ENROLLMENTS_LINK } from './enrollments'
import { MAIL_HELD_LINK } from './mail'
import { adoptMemorySuggestion, dismissMemorySuggestion } from '../api/today'
import type { MemoryAction, TodayAction, TodayCard } from '../api/today'
import { t } from '../i18n'

export const TOP_N = 5
// 撤销窗口（design §2.3）：5 秒后才真正调用写接口；会在 30 秒内超时的卡立即发送。
export const UNDO_MS = 5000
export const SEND_NOW_WITHIN_SEC = 30

export type Tier = 'now' | 'block' | 'normal'

export const TIER_LABEL: Record<Tier, string> = {
  get now() { return t('today.tier.now') },
  get block() { return t('today.tier.block') },
  get normal() { return t('today.tier.normal') },
}

export function tierOf(card: TodayCard): Tier {
  if (card.urgency === 'now') return 'now'
  return card.blocks.score > 0 ? 'block' : 'normal'
}

// 「卡住 N」短标记：阻塞项数；没有阻塞时不显示。
export function blockShort(card: TodayCard): string {
  return card.blocks.items > 0 ? t('today.blockShort', { n: card.blocks.items }) : ''
}

export function minutesText(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  if (s < 60) return t('today.minutes.under1')
  if (s < 3600) return t('today.minutes.m', { n: Math.floor(s / 60) })
  if (s < 86400) return t('today.minutes.h', { n: Math.floor(s / 3600) })
  return t('today.minutes.d', { n: Math.floor(s / 86400) })
}

export function waitText(card: TodayCard, nowSec: number): string {
  return card.waiting_since > 0 ? t('today.wait', { d: minutesText(nowSec - card.waiting_since) }) : ''
}

export function expiresText(card: TodayCard, nowSec: number): string {
  if (!card.expires_at) return ''
  const left = card.expires_at - nowSec
  return left <= 0 ? t('today.expires.overdue') : t('today.expires.left', { d: minutesText(left) })
}

export interface TierGroup {
  tier: Tier
  label: string
  cards: TodayCard[]
}

// 按出现顺序把（已排好序的）卡切成分组；后端排序已保证同组相邻。
export function groupByTier(cards: TodayCard[]): TierGroup[] {
  const out: TierGroup[] = []
  for (const c of cards) {
    const t = tierOf(c)
    const last = out[out.length - 1]
    if (last && last.tier === t) last.cards.push(c)
    else out.push({ tier: t, label: TIER_LABEL[t], cards: [c] })
  }
  return out
}

// 管家建议对应的操作（T4 才会有）；有就做成最前面的「按建议：X」主按钮。
export function adviceAction(card: TodayCard): TodayAction | null {
  const id = card.advice?.action_id
  if (!id) return null
  return card.actions.find((a) => actionKey(a) === id || a.id === id) ?? null
}

// actionKey 是一个操作的稳定标识：answer 类按 value 区分。
export function actionKey(a: TodayAction): string {
  return a.id === 'answer' ? `answer:${a.value ?? ''}` : a.id
}

// ACTION_LABEL_KEYS 把服务端下发的动作名（internal/today 的 Action.Label，固定中文）对到本页
// i18n key：动作的语义是「id + 文案」一起定的——open 在不同卡上是「看详情」/「去整理」/「去指派」/
// 「打开 plan」——所以按 id:label 配对映射。表里没有的（answer 的选项文案、服务端新加的动作、
// 业务内容）原样显示服务端给的文案。
const ACTION_LABEL_KEYS: Record<string, string> = {
  'reply:回复': 'today.action.reply',
  'ack:已读': 'today.action.ack',
  'accept:通过': 'today.action.accept',
  'rerun:附意见重跑': 'today.action.rerun',
  'diff:看 diff': 'today.action.diff',
  'approve:批准': 'today.action.approve',
  'reject:拒绝': 'today.action.reject',
  'open:看详情': 'today.action.openDetail',
  'open:去整理': 'today.action.openCapture',
  'open:去指派': 'today.action.openMailHeld',
  'open:打开 plan': 'today.action.openPlan',
  'open:去处理': 'today.action.openEnroll',
  'report:请求汇报': 'today.action.report',
  'park:搁置': 'today.action.park',
  'adopt:采纳': 'today.action.adopt',
  'dismiss:忽略': 'today.action.dismiss',
  'resume:继续': 'today.action.resume',
}

// actionLabel 是动作按钮上显示的文案：已知动作走 i18n（跟界面语言一致），未知的原样显示。
export function actionLabel(a: Pick<TodayAction, 'id' | 'label'>): string {
  const key = ACTION_LABEL_KEYS[`${a.id}:${a.label}`]
  return key ? t(key) : a.label
}

// handledActionLabel 是「已处理」列表里的动作名：采纳 / 忽略建议的 label 是点的时候按当时的界面
// 语言记下的（dismiss:goal 这类），换语言后不能把旧中文直接展示，按 action_id 前缀重新取；其余
// 动作走 actionLabel，都不认识就原样显示。
export function handledActionLabel(actionId: string, label: string): string {
  if (actionId.startsWith('adopt:')) return t('today.suggestion.adopt')
  if (actionId.startsWith('dismiss:')) return t('today.suggestion.dismiss')
  return actionLabel({ id: actionId, label })
}

// 纯跳转 / 展开类操作：不进撤销窗口、不记审计。
export function isLocalAction(a: TodayAction): boolean {
  return a.id === 'diff' || a.id === 'open' || a.id === 'reply' || a.id === 'rerun'
}

// 卡片标题的跳转目标（design §6）：会话类 → 工作台；工作项 → Works 抽屉；plan → 详情；job → 详情。
export function cardLink(card: TodayCard): string {
  const r = card.refs
  switch (card.kind) {
    case 'relay':
    case 'permission':
    case 'interaction':
      if (r.thread_id) return `/workbench?thread=${encodeURIComponent(r.thread_id)}`
      if (r.job_id) return `/jobs/${encodeURIComponent(r.job_id)}`
      break
    case 'review':
    case 'approval':
      if (r.job_id) return `/jobs/${encodeURIComponent(r.job_id)}`
      break
    case 'work':
    case 'suggestion':
    case 'merge':
      if (r.work_item_id) return `/work?id=${encodeURIComponent(r.work_item_id)}`
      break
    case 'capture':
      return CAPTURE_BOX_LINK
    case 'mail_held':
      // 信箱「信件待指派」汇总卡（gofer 信箱 M7）：没人接收的信件在信箱的待指派页签里改投 / 撤回
      return MAIL_HELD_LINK
    case 'worker_enroll':
      // worker 自助注册：卡片的唯一动作就是跳 Runners 页的「待接受」区
      return ENROLLMENTS_LINK
    case 'decision':
    case 'plan_blocked':
      if (r.plan_id) return `/plans/${encodeURIComponent(r.plan_id)}`
      break
    case 'memory':
      if (r.tracker_id) {
        const q = new URLSearchParams({ tab: 'memories', memory_scope: 'repo', tracker: r.tracker_id })
        if (r.memory_key) q.set('memory', r.memory_key)
        return `/issues?${q.toString()}`
      }
      break
  }
  if (r.plan_id) return `/plans/${encodeURIComponent(r.plan_id)}`
  return '/today'
}

// 卡片上「跳转类」操作的目标：diff 去 job；open 带站内路径 value（如收集箱）就用它；其余用卡片标题的目标。
export function actionLink(card: TodayCard, a: TodayAction): string {
  if (a.id === 'diff' && card.refs.job_id) return `/jobs/${encodeURIComponent(card.refs.job_id)}`
  if (a.id === 'open' && a.value && a.value.startsWith('/') && !a.value.startsWith('//')) return a.value
  return cardLink(card)
}

// toast 文案：「已批准」「已回复」……
export function doneLabel(a: TodayAction, viaAdvice = false): string {
  switch (a.id) {
    case 'reply':
      return t('today.done.reply')
    case 'rerun':
      return t('today.done.rerun')
    case 'ack':
      return t('today.done.ack')
  }
  return viaAdvice ? t('today.done.viaAdvice', { label: a.label }) : t('today.done.plain', { label: a.label })
}

// 明早 9:00（本地时区）——「搁置」不另弹输入，默认搁置到明早。
export function tomorrowNine(now = new Date()): number {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 9, 0, 0, 0)
  return Math.floor(d.getTime() / 1000)
}

// 把一次卡片操作映射为现有写接口调用。返回的函数同步发起第一个请求（keepalive 包装依赖这一点）。
export function runCardAction(card: TodayCard, a: TodayAction, text = ''): () => Promise<unknown> {
  const r = card.refs
  switch (card.kind) {
    case 'interaction':
      return () => answerInteraction(r.job_id ?? '', r.interaction_id ?? '', a.id === 'answer' ? a.value ?? '' : text)
    case 'decision':
      return () => answerDecision(r.decision_id ?? '', a.id === 'answer' ? a.value ?? '' : text)
    case 'permission':
      // 允许 / 总是允许 / 拒绝 带 value；「附原因拒绝」把输入的原因拼成 deny:<原因>
      return () =>
        answerSessionPermission(r.session_id ?? '', r.decision_id ?? '', a.id === 'answer' ? a.value ?? '' : `deny:${text}`)
    case 'relay':
      // 「已读」确认这张卡代表的所有未读 turn（同会话只出一张卡）；请求同步发起。
      if (a.id === 'ack') {
        const ids = r.decision_ids?.length ? r.decision_ids : [r.decision_id ?? '']
        return () => Promise.all(ids.map((id) => ackSessionTurn(r.session_id ?? '', id)))
      }
      return () => saySession(r.session_id ?? '', text)
    case 'review':
      if (a.id === 'rerun') return () => rejectJob(r.job_id ?? '', text, a.value !== '0')
      return () => acceptJob(r.job_id ?? '')
    case 'approval':
      // gofer-9b1b：/v1/today/actions 只记审计，批准 / 拒绝要直接调 job 接口。拒绝理由可空
      // （optional_text），resume 恒 false——待批的拒绝不续投。
      if (a.id === 'reject') return () => rejectJob(r.job_id ?? '', text.trim(), false)
      if (a.id === 'approve') return () => approveJob(r.job_id ?? '')
      break
    case 'work': {
      const id = r.work_item_id ?? ''
      if (a.id === 'report') return () => requestWorkReport(id)
      if (a.id === 'park') return () => patchWorkItem(id, { status: 'parked', park_until: tomorrowNine() })
      if (a.id.startsWith('adopt:')) return () => acceptWorkSuggestion(id, a.id.slice(6))
      if (a.id.startsWith('dismiss:')) return () => dismissWorkSuggestion(id, a.id.slice(8))
      // 回复 = 写一条日志；有在线会话时同时经会话送达（web 传话），两个请求同步发起。
      return () => {
        const note = addWorkNote(id, text)
        const msg = r.session_id ? sendSessionMessage(r.session_id, text) : Promise.resolve(null)
        return Promise.all([note, msg])
      }
    }
    case 'suggestion':
      if (a.id === 'dismiss') return () => dismissWorkSuggestion(r.work_item_id ?? '', r.field ?? '')
      return () => acceptWorkSuggestion(r.work_item_id ?? '', r.field ?? '')
    case 'merge':
      if (a.id === 'dismiss') return () => dismissMergeSuggestion(r.merge_id ?? 0)
      return () => acceptMergeSuggestion(r.merge_id ?? 0)
    case 'plan_blocked':
      return () => planResume(r.plan_id ?? '')
    case 'memory':
      if (a.id === 'dismiss') return () => dismissMemorySuggestion(r.memory_suggestion_id ?? 0)
      return () => adoptMemorySuggestion(r.memory_suggestion_id ?? 0)
  }
  return () => Promise.resolve(null)
}

// settleHidden：重拉 /v1/today 后决定哪些卡继续藏着。
// - 还在撤销窗口或写请求在途（未进 committed）的卡：服务端还返回就继续藏；
// - 写操作已成功（committed 记下了成功时的刷新序号 doneSeq）且这次刷新是在那之后才发起的
//   （doneSeq < seq）：不再藏——服务端没返回就自然消失；还返回（如「已读」后又来新 turn、
//   工作项「回复」/「请求汇报」后仍是等我）就重新显示，不会在这个标签页里永远消失。
// committed 会被就地清理。
export function settleHidden(
  hidden: Set<string>,
  committed: Map<string, number>,
  serverKeys: Set<string>,
  pendingKey: string | undefined,
  seq: number,
): Set<string> {
  const next = new Set<string>()
  for (const k of hidden) {
    const doneSeq = committed.get(k)
    if (doneSeq !== undefined && doneSeq < seq) {
      committed.delete(k)
      continue
    }
    if (serverKeys.has(k) || k === pendingKey) next.add(k)
  }
  for (const k of [...committed.keys()]) {
    if (!next.has(k)) committed.delete(k)
  }
  return next
}

// 「自上次打开」水位只在看过首页之后推进：可见满这么久才算看过。
export const TODAY_SEEN_SEC = 10

// 会在 30 秒内超时的卡不等撤销窗口，立即发送。
export function sendImmediately(card: TodayCard, nowSec: number): boolean {
  return !!card.expires_at && card.expires_at - nowSec < SEND_NOW_WITHIN_SEC
}

// 「g」再「d」全局快捷键（800ms 内），输入框里不触发。
export function createChordDetector(windowMs = 800, now: () => number = () => Date.now()) {
  let gAt: number | null = null
  return (key: string, typing: boolean): boolean => {
    if (typing) return false
    if (key === 'g') {
      gAt = now()
      return false
    }
    const hit = key === 'd' && gAt !== null && now() - gAt <= windowMs
    gAt = null
    return hit
  }
}

export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as { tagName?: string; isContentEditable?: boolean } | null
  if (!el || !el.tagName) return false
  const tag = el.tagName.toUpperCase()
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || !!el.isContentEditable
}

// 「自上次打开」的时间水位：localStorage 记上一次打开首页的时间。
export const LAST_OPEN_KEY = 'gofer.today.lastOpen'
export const INCLUDE_EXEC_KEY = 'gofer.today.includeExec'

export function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

// memory 卡「详情」里的提议文案（P4）。
export const MEMORY_ACTION_LABEL: Record<MemoryAction, string> = {
  get archive() { return t('today.memory.archive') },
  get merge() { return t('today.memory.merge') },
  get kind() { return t('today.memory.kind') },
  get summary() { return t('today.memory.summary') },
  get when() { return t('today.memory.when') },
}

export function memoryProposalText(card: TodayCard): string {
  const m = card.memory
  if (!m) return ''
  const p = m.payload ?? {}
  switch (m.action) {
    case 'archive':
      return t('today.memoryText.archive', { key: m.key })
    case 'merge':
      return t('today.memoryText.merge', {
        key: m.key,
        into: p.into ?? '',
        how: p.content ? t('today.memoryText.mergeWithContent') : t('today.memoryText.mergeAppend'),
      })
    case 'kind':
      return t('today.memoryText.kind', { from: m.current_kind ?? '—', to: p.kind ?? '' })
    case 'summary':
      return t('today.memoryText.summary', { summary: p.summary ?? '' })
    case 'when':
      return t('today.memoryText.when', { keywords: (p.keywords ?? []).join(', ') })
  }
  return ''
}
