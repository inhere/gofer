// agent 会话状态的展示：Sessions 页与「工作」页的卡片共用。文案走 i18n（util.session.*），
// 以 zh 为回退；没有登记的状态原样显示。
import type { AgentSessionState } from '../api/types'
import { t, te } from '../i18n'

// ACP 持续会话 / 终端 job 关联到工作项时，状态是 job 状态（SessionBrief.kind = job）。
// 两个前缀分开：agent 会话状态 util.session.state.*，job 状态 util.session.job.*，
// 其余 job 状态值直接复用主字典的 status.*。
function agentStateKey(s: string): string {
  return `util.session.state.${s}`
}

function jobStateKey(s: string): string {
  const own = `util.session.job.${s}`
  if (te(own)) return own
  const shared = `status.${s}`
  return te(shared) ? shared : ''
}

export function agentStateLabel(s: AgentSessionState | string | undefined): string {
  if (!s) return '—'
  const key = agentStateKey(s)
  if (te(key)) return t(key)
  const jobKey = jobStateKey(s)
  return jobKey ? t(jobKey) : s
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
