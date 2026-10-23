import { computed } from 'vue'
import type { AgentSession, SessionMessage } from '../api/types'
import { t } from '../i18n'

// 「发给主 agent」的快捷语：让主 agent 在换会话 / 收尾前写交接说明。
// 引用处全在模板（按钮文案与填入草稿），导出 computed 让语言切换即时生效。
export const HANDOFF_QUICK_PHRASE = computed(() => t('util.planSupervisor.handoff'))

// 会话下拉里一项的显示名：优先 title，其次 agent + 短 id。
export function sessionLabel(s: Pick<AgentSession, 'session_id' | 'title' | 'agent' | 'state'>): string {
  const name = s.title?.trim() || `${s.agent || 'agent'} ${s.session_id.slice(0, 8)}`
  return `${name} · ${s.state}`
}

// 发送结果的一行说明（sendSessionMessage 的返回：queued / delivering / delivered /
// undelivered / failed + channel）。delivering = 内部 job 已开始但还没结束，outbox 里会回写真实结果
// （gofer-qzv5）。
export function messageOutcomeText(m: Pick<SessionMessage, 'status' | 'channel' | 'error'>): string {
  const reason = m.error || t('util.planSupervisor.unknownReason')
  if (m.status === 'failed') return t('util.planSupervisor.failed', { err: reason })
  if (m.status === 'undelivered') return t('util.planSupervisor.undelivered', { err: reason })
  const via = m.channel ? t('util.planSupervisor.via', { channel: m.channel }) : ''
  if (m.status === 'delivering') return t('util.planSupervisor.delivering', { via })
  return m.status === 'delivered'
    ? t('util.planSupervisor.delivered', { via })
    : t('util.planSupervisor.queued', { via })
}

// 绑定会话是否还能收消息：已结束的会话发了也不会有人读。
export function canMessageSession(s: Pick<AgentSession, 'state'> | null): boolean {
  return !!s && s.state !== 'ended'
}
