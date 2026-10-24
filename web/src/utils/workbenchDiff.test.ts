// 会话改动页签的 409 处理（gofer-yb5x）：没有基线是"说明"，不是"加载失败"。
import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import { classifyDiffFailure } from './workbenchDiff'

describe('classifyDiffFailure', () => {
  it('treats a 409 with a detail as an in-tab explanation', () => {
    const err = new ApiError(
      409,
      'get workbench thread diff failed - workbench: 首轮作业没有 base_sha/worktree_base_sha，无法建立会话改动基线',
      'workbench: 首轮作业没有 base_sha/worktree_base_sha，无法建立会话改动基线',
      'get workbench thread diff failed',
    )
    const failure = classifyDiffFailure(err)
    expect(failure.unavailable).toBe(true)
    // detail 是服务端给的可读原因，优先原样显示（不拼 error 前缀）。
    expect(failure.reason).toBe('workbench: 首轮作业没有 base_sha/worktree_base_sha，无法建立会话改动基线')
  })

  it('falls back to the message when a 409 carries no detail', () => {
    const failure = classifyDiffFailure(new ApiError(409, 'not resumable'))
    expect(failure).toEqual({ unavailable: true, reason: 'not resumable' })
  })

  it('keeps real failures as errors (500 / 504 / network)', () => {
    expect(classifyDiffFailure(new ApiError(500, 'boom', 'diff failed'))).toEqual({
      unavailable: false,
      reason: 'diff failed',
    })
    expect(classifyDiffFailure(new ApiError(504, 'diff timed out')).unavailable).toBe(false)
    expect(classifyDiffFailure(new TypeError('Failed to fetch'))).toEqual({
      unavailable: false,
      reason: 'Failed to fetch',
    })
  })
})
