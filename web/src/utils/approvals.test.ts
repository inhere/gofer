import { afterEach, describe, expect, it, vi } from 'vitest'
import { listJobs } from '../api/client'
import type { Job } from '../api/types'
import { decisionTone, holdOriginLabel, holdSubmittedAt } from './approvals'

function job(overrides: Partial<Job> = {}): Job {
  return {
    id: 'j1',
    project_key: 'demo',
    agent: 'exec',
    runner: 'server',
    status: 'awaiting_approval',
    exit_code: 0,
    cwd: '',
    result_dir: '',
    started_at: 0,
    ...overrides,
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('approval helpers', () => {
  it('derives the fixed submission time from the hold deadline', () => {
    expect(holdSubmittedAt(job({ hold: { expires_at: 120, timeout_sec: 60 } }))).toBe(60)
    expect(holdSubmittedAt(job())).toBeUndefined()
    expect(holdSubmittedAt(job({ hold: { expires_at: 0, timeout_sec: 60 } }))).toBeUndefined()
  })

  it('combines caller and origin without empty separators', () => {
    expect(holdOriginLabel(job({ caller_id: 'alice', hold: { expires_at: 1, timeout_sec: 1, origin: 'web' } }))).toBe(
      'alice · web',
    )
    expect(holdOriginLabel(job({ caller_id: 'alice' }))).toBe('alice')
  })

  it('maps decisions to display tones', () => {
    expect(decisionTone('approved')).toBe('done')
    expect(decisionTone('rejected')).toBe('fail')
    expect(decisionTone('expired')).toBe('time')
    expect(decisionTone('cancelled')).toBe('idle')
  })
})

describe('listJobs hold query', () => {
  it('sends hold=decided to the jobs endpoint', async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({ jobs: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetch)

    await listJobs({ hold: 'decided' })

    expect(fetch).toHaveBeenCalledWith('/v1/jobs?hold=decided', expect.any(Object))
  })
})
