// agent 会话状态的展示：Sessions 页与「工作」页的卡片共用。
import type { AgentSessionState } from '../api/types'

const LABELS: Record<AgentSessionState, string> = {
  running: '执行中',
  idle: '空闲',
  waiting_reply: '等待回复',
  needs_attention: '需注意',
  handed_off: '已接管',
  ended: '已结束',
  offline: '离线',
}

export function agentStateLabel(s: AgentSessionState | string | undefined): string {
  if (!s) return '—'
  return (LABELS as Record<string, string>)[s] ?? s
}

// 卡片左侧色条：等回复 = 要你处理；需注意 = 异常；执行中 / 已接管 = 进行中；离线 / 已结束 = 暗淡。
export function agentStateTone(s: AgentSessionState | string | undefined): 'live' | 'hot' | 'fail' | 'idle' | 'off' {
  switch (s) {
    case 'waiting_reply':
      return 'hot'
    case 'needs_attention':
      return 'fail'
    case 'running':
    case 'handed_off':
      return 'live'
    case 'ended':
    case 'offline':
      return 'off'
    default:
      return 'idle'
  }
}

// 进程不在了（离线 / 已结束）：卡片整体变淡，操作区保持清晰。
export function agentStateDim(s: AgentSessionState | string | undefined): boolean {
  return s === 'ended' || s === 'offline'
}
