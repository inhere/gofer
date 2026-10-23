// agent 自己报告的收尾原因（gofer-gk74）的显示映射。
// 后端 wrap_up 只可能是 agent 流里带来的值，规范拼写是 max_steps / max_tokens / timeout / budget（软预算用尽）；
// 未知值原样显示——将来 agent 新增的上限也要看得见。与 stop_reason（ACP 协议给的停止原因）
// 不是一回事：这里指 status 仍是 done、汇报也在，但任务很可能没做完的提前收尾。
import { t, te } from '../i18n'

const WRAP_UP_LABELS: Record<string, string> = {
  max_steps: 'jobs.wrapUp.maxSteps',
  max_tokens: 'jobs.wrapUp.maxTokens',
  timeout: 'jobs.wrapUp.timeout',
  budget: 'jobs.wrapUp.budget',
}

export function wrapUpText(reason: string | undefined | null): string {
  if (!reason) return ''
  return te(WRAP_UP_LABELS[reason]) ? t(WRAP_UP_LABELS[reason]) : t('jobs.wrapUp.unknown', { reason })
}
