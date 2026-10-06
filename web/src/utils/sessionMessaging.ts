import type { AgentSession, Decision, SessionMessage } from '../api/types'

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

export function peerMessagingLabel(session: Pick<AgentSession, 'peer_name' | 'peer_status' | 'peer_messaging'>): '待上报' | '可接收消息' | '不可接收消息' {
  if (!session.peer_name && !session.peer_status && !session.peer_messaging) return '待上报'
  return session.peer_messaging ? '可接收消息' : '不可接收消息'
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
  | { kind: 'turn'; turn: Decision; at: number }
  | { kind: 'message'; message: SessionMessage; at: number }

export function mergeSessionTimeline(turns: Decision[], messages: SessionMessage[]): SessionTimelineEntry[] {
  let seq = 0
  return [
    ...turns.map((turn) => ({ kind: 'turn' as const, turn, at: turn.asked_at, seq: seq++ })),
    ...messages.map((message) => ({ kind: 'message' as const, message, at: message.created_at, seq: seq++ })),
  ].sort((a, b) => a.at - b.at || a.seq - b.seq)
}
