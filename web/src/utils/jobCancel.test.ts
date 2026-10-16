import { describe, expect, it } from 'vitest'
import type { JobStatus } from '../api/types'
import { canCancelJob } from './jobCancel'

// 与后端 internal/job/model.go 的状态常量一一对应（那边新增状态时这里也要补一行）。
const ALL_STATUSES: JobStatus[] = [
  'queued',
  'running',
  'awaiting_input',
  'pending_interaction',
  'recovering',
  'waiting_dir',
  'awaiting_approval',
  'paused_for_update',
  'needs_review',
  'done',
  'failed',
  'cancelled',
  'timeout',
  'rejected',
]

// 服务端 cancel 会真的动手的状态。
const CANCELLABLE: JobStatus[] = [
  'queued',
  'running',
  'awaiting_input',
  'pending_interaction',
  'recovering',
  'waiting_dir',
  'awaiting_approval',
  'paused_for_update',
]

// 终态（空操作，不显示）与 needs_review（409，走 accept/reject，不显示）。
const NOT_CANCELLABLE: JobStatus[] = ['done', 'failed', 'cancelled', 'timeout', 'rejected', 'needs_review']

describe('canCancelJob', () => {
  it('classifies every job status', () => {
    expect([...ALL_STATUSES].sort()).toEqual([...CANCELLABLE, ...NOT_CANCELLABLE].sort())
  })

  it('offers cancel on every non-terminal status the server accepts', () => {
    for (const status of CANCELLABLE) {
      expect(canCancelJob(status), status).toBe(true)
    }
  })

  it('hides cancel on terminal statuses and on needs_review', () => {
    for (const status of NOT_CANCELLABLE) {
      expect(canCancelJob(status), status).toBe(false)
    }
  })

  it('hides cancel while the job is still loading', () => {
    expect(canCancelJob(undefined)).toBe(false)
    expect(canCancelJob('')).toBe(false)
  })

  it('defaults an unknown status to cancelable', () => {
    expect(canCancelJob('some_new_queue_state')).toBe(true)
  })
})
