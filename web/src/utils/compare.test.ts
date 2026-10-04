import { describe, expect, it } from 'vitest'
import type { Job, WorkflowStep } from '../api/types'
import {
  awaitingPick,
  buildColumns,
  canPickColumn,
  columnMergeReason,
  latestFans,
  mergeAvailability,
  mergeErrorText,
  mergeRequestBody,
  mergeResultText,
  parseMergeError,
  tailPreview,
} from './compare'

const row = (over: Partial<WorkflowStep>): WorkflowStep => ({ step_index: 1, fan_index: 1, attempt: 1, status: 'done', join: 'pick', job_id: 'j1', ...over }) as WorkflowStep
const job = (over: Partial<Job>): Job => ({ id: 'j1', agent: 'claude', runner: 'local', status: 'done', started_at: 100, ended_at: 160, ...over }) as Job

describe('compare data shaping', () => {
  it('keeps only the latest attempt of each fan, ordered by fan', () => {
    const rows = [
      row({ fan_index: 2, attempt: 1, job_id: 'b1', status: 'failed' }),
      row({ fan_index: 1, attempt: 1, job_id: 'a1', status: 'failed' }),
      row({ fan_index: 2, attempt: 2, job_id: 'b2' }),
      row({ step_index: 1, fan_index: 0, job_id: 'x' }),
    ]
    expect(latestFans(rows).map((r) => r.job_id)).toEqual(['a1', 'b2'])
  })

  it('builds one column per fan with agent, duration, diff, verify, tail and retries', () => {
    const rows = [row({ job_id: 'j1' }), row({ fan_index: 2, job_id: 'j2', attempt: 2, picked: true }), row({ fan_index: 2, job_id: 'j2old', attempt: 1, status: 'failed' })]
    const cols = buildColumns(
      rows,
      {
        j1: job({ diff_summary: ' a.txt | 1 +\n', commits_ahead: 2, worktree_branch: 'gofer/j1', verify: { status: 'passed', exit_code: 0, duration_ms: 5 } }),
        j2: job({ id: 'j2', agent: 'codex', status: 'failed', ended_at: 130, verify: { status: 'failed', exit_code: 1, duration_ms: 5 } }),
      },
      { j1: 'l1\nl2\nl3\n' },
      999,
    )
    expect(cols).toHaveLength(2)
    expect(cols[0]).toMatchObject({ fanIndex: 1, agent: 'claude', durationSec: 60, diffSummary: 'a.txt | 1 +', commitsAhead: 2, branch: 'gofer/j1', tail: 'l1\nl2\nl3', remote: false, retries: 0 })
    expect(cols[0].verify).toEqual({ status: 'passed', text: '通过' })
    expect(cols[1]).toMatchObject({ fanIndex: 2, agent: 'codex', status: 'failed', durationSec: 30, picked: true, retries: 1 })
    expect(cols[1].verify?.text).toBe('失败')
  })

  it('uses wall clock for a running fan and tolerates a missing job', () => {
    const cols = buildColumns([row({ status: 'running' }), row({ fan_index: 2, job_id: '', status: undefined })], { j1: job({ status: 'running', ended_at: 0 }) }, {}, 130)
    expect(cols[0].durationSec).toBe(30)
    expect(cols[1].status).toBe('pending')
    expect(cols[1].durationSec).toBeNull()
  })

  it('marks a remote runner column', () => {
    const cols = buildColumns([row({})], { j1: job({ runner: 'w1', source: 'worker:wk1' }) })
    expect(cols[0].remote).toBe(true)
  })

  it('tail preview keeps the last lines and bounds the size', () => {
    expect(tailPreview('1\n2\n3\n4\n', 2)).toBe('3\n4')
    expect(tailPreview(undefined)).toBe('')
    expect(tailPreview('x'.repeat(2000), 8, 100).length).toBe(101)
  })
})

describe('pick state', () => {
  const ctx = { workflowStatus: 'running' as const, currentStep: 1, stepIndex: 1 }
  it('waits for pick only when every fan is terminal and nothing is picked', () => {
    const rows = [row({}), row({ fan_index: 2, job_id: 'j2' })]
    expect(awaitingPick(rows, ctx)).toBe(true)
    expect(awaitingPick([row({}), row({ fan_index: 2, status: 'running' })], ctx)).toBe(false)
    expect(awaitingPick([row({ picked: true }), row({ fan_index: 2 })], ctx)).toBe(false)
    expect(awaitingPick(rows, { ...ctx, workflowStatus: 'done' })).toBe(false)
    expect(awaitingPick(rows, { ...ctx, currentStep: 2 })).toBe(false)
    expect(awaitingPick([row({ join: undefined }), row({ fan_index: 2, join: undefined })], ctx)).toBe(false)
  })

  it('only successful fans can be picked', () => {
    const [ok, bad] = buildColumns([row({}), row({ fan_index: 2, status: 'failed', job_id: 'j2' })], { j1: job({}), j2: job({ id: 'j2', status: 'failed' }) })
    expect(canPickColumn(ok, true)).toBe(true)
    expect(canPickColumn(bad, true)).toBe(false)
    expect(canPickColumn(ok, false)).toBe(false)
  })
})

describe('merge helpers', () => {
  it('greys the merge out for remote runners and jobs without a worktree', () => {
    expect(mergeAvailability(job({ worktree_path: '/w' })).ok).toBe(true)
    expect(mergeAvailability(job({ worktree_path: '/w', runner: 'w1' }))).toMatchObject({ ok: false })
    expect(mergeAvailability(job({ worktree_path: '/w', runner: 'w1' })).reason).toContain('本机 runner')
    expect(mergeAvailability(job({ runner: 'local' })).ok).toBe(false)
    expect(mergeAvailability(job({ worktree_path: '/w', runner: 'server' })).ok).toBe(true)
  })

  it('explains why a fan column cannot be merged', () => {
    expect(columnMergeReason({ remote: false, branch: 'gofer/x' })).toBe('')
    expect(columnMergeReason({ remote: true, branch: 'gofer/x' })).toContain('本机 runner')
    expect(columnMergeReason({ remote: false, branch: '' })).toContain('worktree')
  })

  it('parses the 409 conflict body into files and a reassuring message', () => {
    const info = parseMergeError(409, 'worktree merge has conflicts: a.txt, dir/b.go')
    expect(info.kind).toBe('conflict')
    expect(info.files).toEqual(['a.txt', 'dir/b.go'])
    expect(mergeErrorText(info)).toContain('已复原')
    expect(parseMergeError(409, 'worktree merge has conflicts').files).toEqual([])
    expect(parseMergeError(409, 'main checkout is not ready for a merge: main checkout has uncommitted changes').kind).toBe('main-not-ready')
    expect(parseMergeError(409, 'worktree merge is supported only for a local runner (job x ran on runner "w1")').kind).toBe('unsupported')
    expect(parseMergeError(500, 'boom')).toEqual({ kind: 'other', files: [], message: 'boom' })
  })

  it('builds the merge request body and result text', () => {
    expect(mergeRequestBody({ squash: true, cleanupOthers: true }, true)).toEqual({ squash: true, cleanup_others: true })
    expect(mergeRequestBody({ squash: false, cleanupOthers: true }, false)).toEqual({ squash: false, cleanup_others: false })
    expect(mergeResultText(true, ['a', 'b'])).toContain('squash')
    expect(mergeResultText(false, [])).toBe('已以 merge 提交合并到基线分支。')
  })
})
