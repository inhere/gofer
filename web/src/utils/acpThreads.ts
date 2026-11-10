import type { WorkbenchThread } from '../api/types'

const ENDED_STATUSES = new Set(['done', 'failed', 'cancelled', 'timeout', 'rejected', 'ended', 'offline'])

export function acpThreadRawStatus(thread: Pick<WorkbenchThread, 'raw_status' | 'status'>): string {
  return thread.raw_status || thread.status
}

export function acpThreadIsEnded(thread: Pick<WorkbenchThread, 'raw_status' | 'status'>): boolean {
  return ENDED_STATUSES.has(acpThreadRawStatus(thread))
}

export function acpThreadStatusValue(thread: Pick<WorkbenchThread, 'raw_status' | 'status'>): 'awaiting_input' | 'running' | 'ended' {
  const status = acpThreadRawStatus(thread)
  if (status === 'awaiting_input' || status === 'pending_interaction') return 'awaiting_input'
  return acpThreadIsEnded(thread) ? 'ended' : 'running'
}

export function acpThreadStatusKey(thread: Pick<WorkbenchThread, 'raw_status' | 'status'>): string {
  switch (acpThreadStatusValue(thread)) {
    case 'awaiting_input':
      return 'sessions.acp.awaitingInput'
    case 'ended':
      return 'sessions.acp.ended'
    default:
      return 'sessions.acp.running'
  }
}

export function acpThreadTone(thread: Pick<WorkbenchThread, 'raw_status' | 'status'>): 'live' | 'hot' | 'off' {
  if (acpThreadIsEnded(thread)) return 'off'
  return acpThreadStatusValue(thread) === 'awaiting_input' ? 'hot' : 'live'
}

export function acpThreadLink(threadID: string): string {
  return `/workbench?thread=${encodeURIComponent(threadID)}`
}

export function acpThreadJobLink(jobID: string): string {
  return `/jobs/${encodeURIComponent(jobID)}`
}

export function acpThreadCountLabel(count: number, hasMore: boolean): string {
  return `${count}${hasMore ? '+' : ''}`
}
