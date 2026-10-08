import type { AgentSession, SessionMessage } from '../api/types'

// 「发给主 agent」的快捷语：让主 agent 在换会话 / 收尾前写交接说明。
export const HANDOFF_QUICK_PHRASE = '请写交接说明并更新 plan handoff'

// 会话下拉里一项的显示名：优先 title，其次 agent + 短 id。
export function sessionLabel(s: Pick<AgentSession, 'session_id' | 'title' | 'agent' | 'state'>): string {
  const name = s.title?.trim() || `${s.agent || 'agent'} ${s.session_id.slice(0, 8)}`
  return `${name} · ${s.state}`
}

// 发送结果的一行说明（sendSessionMessage 的返回：queued / delivered / failed + channel）。
export function messageOutcomeText(m: Pick<SessionMessage, 'status' | 'channel' | 'error'>): string {
  if (m.status === 'failed') return `发送失败：${m.error || '未知原因'}`
  const via = m.channel ? `（${m.channel}）` : ''
  return m.status === 'delivered' ? `已送达${via}` : `已排队，等待送达${via}`
}

// 绑定会话是否还能收消息：已结束的会话发了也不会有人读。
export function canMessageSession(s: Pick<AgentSession, 'state'> | null): boolean {
  return !!s && s.state !== 'ended'
}
