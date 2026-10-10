// 传话人（常驻 Claude SendMessage 进程）的展示逻辑：状态文字/配色、能否列出会话、
// 与 gofer 已登记会话的对照。纯函数，便于单测；组件只负责渲染。
import type {
  AgentSession,
  MessengerAgent,
  MessengerSnapshot,
  Runner,
} from '../api/types'

// 列出会话需要的 worker 协议版本（wsproto.MessengerListMinProtocolVersion）。
export const MESSENGER_LIST_MIN_PROTOCOL = 16

export type MessengerTone = 'idle' | 'busy' | 'stopped' | 'unknown'

export interface MessengerDisplay {
  // 中文状态文字
  text: string
  // 颜色类别：idle 绿 / busy 琥珀 / stopped 灰 / unknown 虚线灰
  tone: MessengerTone
  // 拿到了真实状态（false = 未知：旧 worker 没有上报 / 离线）
  known: boolean
}

const LABELS: Record<string, string> = { stopped: '未启动', idle: '空闲', busy: '处理中' }

// 一个 runner 当前的传话人状态。local 直接读快照；worker 以心跳里的快照为准，
// 没有快照（旧版 worker、刚连上还没有心跳、离线）一律显示“未知”，不猜 stopped。
export function messengerDisplay(r: Runner): MessengerDisplay {
  if (r.type === 'worker' && r.status !== 'connected') {
    return { text: '离线', tone: 'unknown', known: false }
  }
  const status = r.messenger_detail?.status ?? (r.type === 'local' ? r.messenger : undefined)
  if (!status) return { text: '未知', tone: 'unknown', known: false }
  const text = LABELS[status]
  if (!text) return { text: status, tone: 'unknown', known: false }
  return { text, tone: status as MessengerTone, known: true }
}

// 空串 = 可以列出该 runner 传话人看到的会话；否则是不可用的原因（直接显示给用户）。
export function messengerListBlock(r: Runner): string {
  if (r.type === 'peer-http') return '该 runner 没有传话人'
  if (r.type === 'worker') {
    if (r.status !== 'connected' || !r.worker) return 'worker 离线，无法列出会话'
    const proto = r.worker.protocol_version ?? 0
    if (proto < MESSENGER_LIST_MIN_PROTOCOL) {
      return `该 worker 协议 v${proto || '?'} 过旧，列出会话需要 v${MESSENGER_LIST_MIN_PROTOCOL} 及以上；请先升级该 worker`
    }
  }
  return ''
}

// 距空闲退出还有多久（中文，空串 = 无 / 已过）。
export function idleLeftText(snap: MessengerSnapshot | undefined, nowSec: number): string {
  if (!snap?.idle_deadline || snap.status === 'stopped') return ''
  const left = snap.idle_deadline - nowSec
  if (left <= 0) return '即将空闲退出'
  if (left < 60) return `${left} 秒后空闲退出`
  return `${Math.floor(left / 60)} 分钟后空闲退出`
}

export function opLabel(op: string | undefined): string {
  if (op === 'list_agents') return '列出会话'
  if (op === 'reply') return '收到回复'
  return '传话'
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
  if (!row.session) return 'gofer 没有登记这个会话：只有装了 gofer hooks 的会话才能经 gofer 传话'
  if (row.session.state === 'ended') return '该会话已结束'
  if (!row.session.peer_messaging) return '该会话还没有上报可用的 SendMessage 地址'
  return ''
}
