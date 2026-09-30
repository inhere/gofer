import { describe, expect, it } from 'vitest'
import { fmtJobTimeout, jobTimeoutTitle } from './jobTimeout'

describe('job timeout display', () => {
  it('formats an effective timeout with the shared duration formatter', () => {
    expect(fmtJobTimeout(5400)).toBe('超时 1h30m')
  })

  it('shows unlimited for unset or non-positive timeouts', () => {
    expect(fmtJobTimeout(0)).toBe('不限时')
    expect(fmtJobTimeout(null)).toBe('不限时')
  })

  it('explains a requested timeout that was clamped', () => {
    expect(jobTimeoutTitle(5400, 3600)).toBe('请求 1h30m，已按上限夹紧为 1h00m')
    expect(jobTimeoutTitle(3600, 3600)).toBeUndefined()
  })
})
