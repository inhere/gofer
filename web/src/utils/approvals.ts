import type { Job } from '../api/types'

// Hold 的提交时刻由服务端在提交时固定为 expires_at - timeout_sec。
export function holdSubmittedAt(job: Pick<Job, 'hold'>): number | undefined {
  const hold = job.hold
  if (!hold || !Number.isFinite(hold.expires_at) || !Number.isFinite(hold.timeout_sec)) {
    return undefined
  }
  if (hold.expires_at <= 0 || hold.timeout_sec < 0) {
    return undefined
  }
  return hold.expires_at - hold.timeout_sec
}

// 列表里同时展示认证调用方和提交渠道；缺一项时不留下多余分隔符。
export function holdOriginLabel(job: Pick<Job, 'caller_id' | 'hold'>): string {
  return [job.caller_id, job.hold?.origin].filter((value): value is string => Boolean(value)).join(' · ')
}

export type DecisionTone = 'done' | 'fail' | 'time' | 'idle'

export function decisionTone(decision: string | undefined): DecisionTone {
  switch (decision) {
    case 'approved':
      return 'done'
    case 'rejected':
      return 'fail'
    case 'expired':
      return 'time'
    default:
      return 'idle'
  }
}
