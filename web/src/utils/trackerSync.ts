import type { Job } from '../api/types'

// TRK-05: the server dispatches `gofer repo sync` as a hidden exec job; the Issues page
// polls that job until it ends and reports the outcome.
export interface TrackerSyncDeps {
  start: () => Promise<{ job_id: string }>
  getJob: (id: string) => Promise<Pick<Job, 'status' | 'error'>>
  stderrTail: (id: string) => Promise<string>
  sleep: (ms: number) => Promise<void>
  intervalMs?: number
  maxPolls?: number
}

export interface TrackerSyncOutcome {
  state: 'ok' | 'failed' | 'timeout'
  jobId: string
  message: string
}

const TERMINAL = new Set(['done', 'failed', 'cancelled', 'timeout', 'rejected'])

// lastLines keeps the tail of a stderr dump short enough for an inline message.
export function lastLines(text: string, n = 3, maxChars = 300): string {
  const lines = text.split('\n').map((l) => l.trim()).filter(Boolean)
  const tail = lines.slice(-n).join(' | ')
  return tail.length > maxChars ? tail.slice(tail.length - maxChars) : tail
}

export async function runTrackerSync(deps: TrackerSyncDeps, onJob?: (id: string) => void): Promise<TrackerSyncOutcome> {
  const { job_id: jobId } = await deps.start()
  onJob?.(jobId)
  const interval = deps.intervalMs ?? 1000
  const max = deps.maxPolls ?? 90
  for (let i = 0; i < max; i++) {
    const job = await deps.getJob(jobId)
    if (TERMINAL.has(job.status)) {
      if (job.status === 'done') return { state: 'ok', jobId, message: '' }
      let tail = ''
      try { tail = lastLines(await deps.stderrTail(jobId)) } catch { /* 日志取不到时只报状态 */ }
      const why = tail || job.error || job.status
      return { state: 'failed', jobId, message: why }
    }
    await deps.sleep(interval)
  }
  return { state: 'timeout', jobId, message: '同步仍在进行，稍后刷新查看' }
}
