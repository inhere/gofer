import { describe, expect, it, vi } from 'vitest'
import { lastLines, runTrackerSync } from './trackerSync'

const sleep = () => Promise.resolve()

describe('runTrackerSync', () => {
  it('polls until the job is done', async () => {
    const getJob = vi.fn().mockResolvedValueOnce({ status: 'queued' }).mockResolvedValueOnce({ status: 'running' }).mockResolvedValueOnce({ status: 'done' })
    const seen: string[] = []
    const out = await runTrackerSync({ start: async () => ({ job_id: 'j1' }), getJob, stderrTail: async () => '', sleep }, (id) => seen.push(id))
    expect(out).toEqual({ state: 'ok', jobId: 'j1', message: '' })
    expect(getJob).toHaveBeenCalledTimes(3)
    expect(seen).toEqual(['j1'])
  })

  it('reports the stderr tail when the job fails', async () => {
    const out = await runTrackerSync({
      start: async () => ({ job_id: 'j2' }),
      getJob: async () => ({ status: 'failed', error: 'exit 1' }),
      stderrTail: async () => 'warn\nsync server 403 Forbidden: no\n',
      sleep,
    })
    expect(out.state).toBe('failed')
    expect(out.message).toContain('403 Forbidden')
  })

  it('falls back to the job error when stderr is unreadable', async () => {
    const out = await runTrackerSync({
      start: async () => ({ job_id: 'j3' }),
      getJob: async () => ({ status: 'timeout', error: 'timed out' }),
      stderrTail: async () => { throw new Error('gone') },
      sleep,
    })
    expect(out).toEqual({ state: 'failed', jobId: 'j3', message: 'timed out' })
  })

  it('gives up after maxPolls', async () => {
    const out = await runTrackerSync({ start: async () => ({ job_id: 'j4' }), getJob: async () => ({ status: 'running' }), stderrTail: async () => '', sleep, maxPolls: 2 })
    expect(out.state).toBe('timeout')
  })

  it('lastLines keeps the tail', () => {
    expect(lastLines('a\nb\nc\nd\n', 2)).toBe('c | d')
  })
})
