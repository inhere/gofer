import type { AgentSession, Decision, SessionMessage, SessionMessageStatus } from '../api/types'
import { t } from '../i18n'

export function sessionDisplayName(session: Pick<AgentSession, 'peer_name' | 'title' | 'agent' | 'session_id'>): string {
  return session.peer_name || session.title || `${session.agent} · ${session.session_id.slice(0, 8)}`
}

// Claude Code 会话名 + 来源的展示文本：`name`（来源）；没有名字返回空串。
export function peerNameLabel(session: Pick<AgentSession, 'peer_name' | 'peer_name_source'>): string {
  if (!session.peer_name) return ''
  return session.peer_name_source ? `${session.peer_name}（${session.peer_name_source}）` : session.peer_name
}

// 剪贴板不可用（非安全上下文等）时返回 false，调用方静默即可。
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export function peerMessagingLabel(session: Pick<AgentSession, 'peer_name' | 'peer_status' | 'peer_messaging'>): string {
  if (!session.peer_name && !session.peer_status && !session.peer_messaging) return t('util.msg.peerPending')
  return session.peer_messaging ? t('util.msg.peerOk') : t('util.msg.peerNo')
}

export function shortAgentSessionId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id
}

export function upsertSessionMessage(messages: SessionMessage[], next: SessionMessage): SessionMessage[] {
  const index = messages.findIndex((message) => message.id === next.id)
  if (index < 0) return [...messages, next]
  return messages.map((message, i) => (i === index ? next : message))
}

function normalizeMessage(text: string | undefined): string {
  return (text ?? '').trim().replace(/\s+/g, ' ')
}

export function shouldShowLastMessage(lastMessage: string | undefined, waitingQuestion: string | undefined, waiting: boolean): boolean {
  if (!lastMessage || !waiting) return true
  const normalizedLast = normalizeMessage(lastMessage)
  const normalizedQuestion = normalizeMessage(waitingQuestion)
  return !normalizedLast || normalizedLast !== normalizedQuestion
}

export type SessionTimelineEntry =
  // relayed：这条 turn 的答复同时作为 outbox 消息出现在时间线里（消息的 decision_id = turn.id）。
  // 两边都画就是同一条消息两个气泡，渲染时只留 outbox 那条（它带送达状态与附件）。
  | { kind: 'turn'; turn: Decision; at: number; relayed: boolean }
  | { kind: 'message'; message: SessionMessage; at: number }

export function mergeSessionTimeline(turns: Decision[], messages: SessionMessage[]): SessionTimelineEntry[] {
  const relayed = new Set(messages.map((message) => message.decision_id).filter((id): id is string => !!id))
  let seq = 0
  return [
    ...turns.map((turn) => ({ kind: 'turn' as const, turn, at: turn.asked_at, relayed: relayed.has(turn.id), seq: seq++ })),
    ...messages.map((message) => ({ kind: 'message' as const, message, at: message.created_at, seq: seq++ })),
  ].sort((a, b) => a.at - b.at || a.seq - b.seq)
}

// gofer-qzv5：outbox 消息状态的展示文案与「重试」资格。
//
// 只有「确实未送达」（undelivered：投递阶梯根本没试过，或内部 job 未启动就被撤销）才开放重试——
// 失败但可能已经送达的（delivered 之外的 failed）再发一次可能重复送达。
export function sessionMessageStatusText(message: Pick<SessionMessage, 'status' | 'channel' | 'error'>): string {
  const channel = message.channel ? ` · ${message.channel}` : ''
  switch (message.status) {
    case 'queued':
      return t('util.msg.queued')
    case 'delivering':
      return t('util.msg.delivering', { channel })
    case 'delivered':
      return `${t('util.msg.delivered')}${channel || t('util.msg.channelNone')}`
    case 'undelivered':
      return t('util.msg.undelivered', { error: message.error || t('util.msg.unknownReason') })
    default:
      return t('util.msg.failed', { error: message.error || t('util.msg.unknownReason') })
  }
}

// canRetrySessionMessage 只对确实未送达的消息开放重试。
export function canRetrySessionMessage(status: SessionMessageStatus): boolean {
  return status === 'undelivered'
}

// gofer-6er0：outbox 里会话自己的回复（不是 web 发出的消息）。
export function isSessionReply(message: Pick<SessionMessage, 'direction'>): boolean {
  return message.direction === 'reply'
}

// 回复气泡的抬头：会话名 · 回复；传话人兜底收到的标「经传话人收到」。
// sessionMessageAuthorLabel：你发出的消息气泡的署名。答的是 agent 正在等的那一轮（channel=turn，
// 比如 Stop hook 停下来等回复）就是直接回复；其余路径（传话人 / 在线会话 / 终端）才是「经转达」。
export function sessionMessageAuthorLabel(message: Pick<SessionMessage, 'channel'>): string {
  return message.channel === 'turn' ? t('util.msg.authorReply') : t('util.msg.authorRelayed')
}

export function sessionReplyLabel(
  message: Pick<SessionMessage, 'source'>,
  session: Pick<AgentSession, 'peer_name' | 'title' | 'agent' | 'session_id'> | null | undefined,
): string {
  const name = session ? sessionDisplayName(session) : 'agent'
  return message.source === 'messenger'
    ? t('util.msg.replyVia', { name })
    : t('util.msg.reply', { name })
}
