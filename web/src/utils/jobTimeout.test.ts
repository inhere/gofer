import { describe, expect, it } from 'vitest'
import { fmtJobTimeout, jobTimeoutClampNote, jobTimeoutTitle } from './jobTimeout'

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

  // gofer-ea2i：可见的夹紧提示要同时给出请求值与封顶的配置键（与 `gofer job show`
  // 的 "(timeout 14400s clamped to 5400s by server.max_job_timeout_sec)" 同一口径）。
  it('names the requested value and the config key that capped it', () => {
    expect(jobTimeoutClampNote(14400, 5400, 'server.max_job_timeout_sec', 'server')).toBe(
      '请求 4h00m，被 server.max_job_timeout_sec 封顶',
    )
    expect(jobTimeoutClampNote(3600, 1800, 'projects.self.max_timeout_sec', 'project')).toBe(
      '请求 1h00m，被 projects.self.max_timeout_sec 封顶',
    )
  })

  it('falls back to the ceiling level, then to a generic ceiling', () => {
    expect(jobTimeoutClampNote(14400, 5400, '', 'server')).toBe('请求 4h00m，被服务端上限封顶')
    expect(jobTimeoutClampNote(14400, 5400, '', 'project')).toBe('请求 4h00m，被项目上限封顶')
    expect(jobTimeoutClampNote(14400, 5400)).toBe('请求 4h00m，被超时上限封顶')
  })

  it('prints nothing when the request was not clamped', () => {
    expect(jobTimeoutClampNote(5400, 5400, 'server.max_job_timeout_sec', 'server')).toBeUndefined()
    expect(jobTimeoutClampNote(0, 5400)).toBeUndefined()
    expect(jobTimeoutClampNote(undefined, 5400)).toBeUndefined()
    expect(jobTimeoutClampNote(5400, undefined)).toBeUndefined()
  })
})
