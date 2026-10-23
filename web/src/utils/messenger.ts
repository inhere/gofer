// 传话人（常驻 Claude SendMessage 进程）的展示逻辑：状态文字/配色、能否列出会话、
// 与 gofer 已登记会话的对照。纯函数，便于单测；组件只负责渲染。
import type {
  AgentSession,
  MessengerAgent,
  MessengerSnapshot,
  MessengerUsage,
  Runner,
} from '../api/types'
import { t, te } from '../i18n'

// 列出会话需要的 worker 协议版本（wsproto.MessengerListMinProtocolVersion）。
export const MESSENGER_LIST_MIN_PROTOCOL = 16

export type MessengerTone = 'idle' | 'busy' | 'stopped' | 'unknown'

export interface MessengerDisplay {
  // 状态文字
  text: string
  // 颜色类别：idle 绿 / busy 琥珀 / stopped 灰 / unknown 虚线灰
  tone: MessengerTone
  // 拿到了真实状态（false = 未知：旧 worker 没有上报 / 离线）
  known: boolean
}

// 一个 runner 当前的传话人状态。local 直接读快照；worker 以心跳里的快照为准，
// 没有快照（旧版 worker、刚连上还没有心跳、离线）一律显示“未知”，不猜 stopped。
export function messengerDisplay(r: Runner): MessengerDisplay {
  if (r.type === 'worker' && r.status !== 'connected') {
    return { text: t('util.messenger.offline'), tone: 'unknown', known: false }
  }
  const status = r.messenger_detail?.status ?? (r.type === 'local' ? r.messenger : undefined)
  if (!status) return { text: t('util.messenger.unknown'), tone: 'unknown', known: false }
  const key = `util.messenger.state.${status}`
  if (!te(key)) return { text: status, tone: 'unknown', known: false }
  return { text: t(key), tone: status as MessengerTone, known: true }
}

// 空串 = 可以列出该 runner 传话人看到的会话；否则是不可用的原因（直接显示给用户）。
export function messengerListBlock(r: Runner): string {
  if (r.type === 'peer-http') return t('util.messenger.noMessenger')
  if (r.type === 'worker') {
    if (r.status !== 'connected' || !r.worker) return t('util.messenger.workerOffline')
    const proto = r.worker.protocol_version ?? 0
    if (proto < MESSENGER_LIST_MIN_PROTOCOL) {
      return t('util.messenger.protocolOld', { v: proto || '?', min: MESSENGER_LIST_MIN_PROTOCOL })
    }
  }
  return ''
}

// 距空闲退出还有多久（空串 = 无 / 已过）。
export function idleLeftText(snap: MessengerSnapshot | undefined, nowSec: number): string {
  if (!snap?.idle_deadline || snap.status === 'stopped') return ''
  const left = snap.idle_deadline - nowSec
  if (left <= 0) return t('util.messenger.soonIdle')
  if (left < 60) return t('util.messenger.idleInSec', { s: left })
  return t('util.messenger.idleInMin', { m: Math.floor(left / 60) })
}

export function opLabel(op: string | undefined): string {
  if (op === 'list_agents') return t('util.messenger.op.list')
  if (op === 'reply') return t('util.messenger.op.reply')
  return t('util.messenger.op.other')
}

// 一次常驻进程重建的原因（未知原因原样显示）。
export function rotationReasonLabel(reason: string | undefined): string {
  switch (reason) {
    case 'max_messages':
      return t('util.messenger.rot.maxMessages')
    case 'max_age':
      return t('util.messenger.rot.maxAge')
    case 'idle':
      return t('util.messenger.rot.idle')
    case 'failed':
      return t('util.messenger.rot.failed')
  }
  return reason ?? ''
}

// 秒数转中文时长（1h20m / 8m / 40s；0 = 空串）。
export function formatAge(sec: number | undefined): string {
  if (!sec || sec <= 0) return ''
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m`
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return m ? `${h}h${m}m` : `${h}h`
}

// 轮换上限（0 = 该限制关闭）。
export function rotationLimitText(snap: MessengerSnapshot | undefined): string {
  if (!snap) return ''
  const msgs = snap.max_messages ? t('util.messenger.rot.msgs', { n: snap.max_messages }) : t('util.messenger.rot.msgsUnlimited')
  const age = snap.max_age_sec ? formatAge(snap.max_age_sec) : t('util.messenger.rot.ageUnlimited')
  return t('util.messenger.rot.line', { msgs, age })
}

// 当前进程的 token / 成本用量（空串 = 没有上报）。
export function usageText(u: MessengerUsage | undefined): string {
  if (!u) return ''
  const parts: string[] = []
  const n = (v?: number) => (v ?? 0).toLocaleString()
  if (u.input_tokens) parts.push(t('util.messenger.usage.in', { v: n(u.input_tokens) }))
  if (u.output_tokens) parts.push(t('util.messenger.usage.out', { v: n(u.output_tokens) }))
  if (u.cache_read_input_tokens) parts.push(t('util.messenger.usage.cacheRead', { v: n(u.cache_read_input_tokens) }))
  if (u.cache_creation_input_tokens) parts.push(t('util.messenger.usage.cacheWrite', { v: n(u.cache_creation_input_tokens) }))
  if (u.cost_usd) parts.push(`$${u.cost_usd.toFixed(4)}`)
  return parts.join(' · ')
}

export interface AgentRow {
  agent: MessengerAgent
  // gofer 已登记的对应会话（按 peer_name 或 session id 前缀匹配）；null = 未登记
  session: AgentSession | null
}

// 把传话人看到的会话与 gofer 登记的会话对照：先按 peer_name 精确匹配，再按
// ListAgents 给的短 id 对 session_id 前缀匹配（短 id >= 6 位才用）。
export function crossReference(agents: MessengerAgent[], sessions: AgentSession[]): AgentRow[] {
  return agents.map((agent) => {
    const byName = sessions.find((s) => !!s.peer_name && s.peer_name === agent.name)
    const sid = agent.short_id ?? ''
    const byId = sid.length >= 6 ? sessions.find((s) => s.session_id.startsWith(sid)) : undefined
    return { agent, session: byName ?? byId ?? null }
  })
}

// 一行的“给它传话”能否点击：空串 = 可以；否则是原因。
export function messageBlockReason(row: AgentRow): string {
  if (!row.session) return t('util.messenger.err.noSession')
  if (row.session.state === 'ended') return t('util.messenger.err.ended')
  if (!row.session.peer_messaging) return t('util.messenger.err.noPeerAddress')
  return ''
}
