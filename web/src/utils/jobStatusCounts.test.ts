import { describe, expect, it } from 'vitest'
import { isCountedStatus, statusCountSince, STATUS_COUNT_WINDOW_SEC } from './jobStatusCounts'
import boardSrc from '../views/Board.vue?raw'

describe('job status counts', () => {
  it('counts only the in-progress states', () => {
    for (const s of ['queued', 'waiting_dir', 'running', 'awaiting_input', 'recovering', 'pending_interaction', 'needs_review', 'awaiting_approval', 'paused_for_update'] as const) {
      expect(isCountedStatus(s)).toBe(true)
    }
    for (const s of ['done', 'failed', 'cancelled', 'timeout', 'rejected', ''] as const) {
      expect(isCountedStatus(s)).toBe(false)
    }
  })

  it('looks back three days, or less when a shorter range is selected', () => {
    const now = 2_000_000_000
    expect(statusCountSince(now)).toBe(now - STATUS_COUNT_WINDOW_SEC)
    expect(statusCountSince(now, now - 3600)).toBe(now - 3600)
    expect(statusCountSince(now, now - 7 * 24 * 3600)).toBe(now - STATUS_COUNT_WINDOW_SEC)
  })

  it('is wired into the jobs board', () => {
    expect(boardSrc).toContain('v-if="isCountedStatus(opt.value)"')
    expect(boardSrc).toContain('since: statusCountSince(Math.floor(Date.now() / 1000), sinceParam())')
    expect(boardSrc).toContain('limit: STATUS_COUNT_LIMIT')
  })
})
