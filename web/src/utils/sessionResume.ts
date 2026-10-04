// 会话唤醒（takeover / resume）的展示逻辑，Sessions 列表与 SessionDrawer 共用。
import { ApiError } from '../api/client'
import type { AgentSession } from '../api/types'

// 按钮文字：已结束 = 唤醒（重新打开一个终端继续）；其它状态 = 接管（原终端若还开着会被顶掉）。
export function resumeLabel(s: Pick<AgentSession, 'state'>): string {
  return s.state === 'ended' ? '唤醒' : '接管'
}

// 悬停说明：能唤醒时说明会做什么，不能时给出（中文）原因。
export function resumeTitle(s: Pick<AgentSession, 'state' | 'can_resume' | 'resume_message' | 'resume_reason'>): string {
  if (s.resume_message) return s.resume_message
  if (s.can_resume) return s.state === 'ended' ? '起一个新终端继续这个已结束的会话' : '起一个新进程接管这个会话'
  return s.resume_reason ? `现在不能唤醒（${s.resume_reason}）` : '现在不能唤醒'
}

// 唤醒失败的提示：服务端 detail 已经是中文原因，优先原样显示。
export function resumeFailText(e: unknown): string {
  if (e instanceof ApiError) return e.detail || e.message
  return e instanceof Error ? e.message : String(e)
}

// 点“唤醒”前的确认文案。
export function resumeConfirmText(s: Pick<AgentSession, 'resume_message'>, plan?: { warning?: string }): string {
  return [s.resume_message || '起一个新进程继续这个会话。', plan?.warning].filter(Boolean).join('\n\n') + '\n\n继续吗？'
}
