import type { AgentSession, SessionMessage } from '../api/types'

export function sessionDisplayName(session: Pick<AgentSession, 'peer_name' | 'title' | 'agent' | 'session_id'>): string {
  return session.peer_name || session.title || `${session.agent} · ${session.session_id.slice(0, 8)}`
}

export function shortAgentSessionId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id
}

export function upsertSessionMessage(messages: SessionMessage[], next: SessionMessage): SessionMessage[] {
  const index = messages.findIndex((message) => message.id === next.id)
  if (index < 0) return [...messages, next]
  return messages.map((message, i) => (i === index ? next : message))
}
