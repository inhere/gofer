import { describe, expect, it } from 'vitest'
import { BASE_DETAIL_INCLUDES, detailIncludes, showJobMessages } from './jobDetailInclude'

describe('showJobMessages', () => {
  it('hides for unknown job', () => {
    expect(showJobMessages(null)).toBe(false)
    expect(showJobMessages(undefined)).toBe(false)
  })
  it('hides for exec / one-shot cli-agent jobs', () => {
    expect(showJobMessages({})).toBe(false)
    expect(showJobMessages({ session: false, turn_no: 0 })).toBe(false)
  })
  it('shows for ACP session jobs', () => {
    expect(showJobMessages({ session: true })).toBe(true)
  })
  it('shows for jobs with multi-turn session messages', () => {
    expect(showJobMessages({ turn_no: 2 })).toBe(true)
  })
})

describe('detailIncludes', () => {
  it('omits messages for non-session jobs', () => {
    expect(detailIncludes({})).toEqual(BASE_DETAIL_INCLUDES)
    expect(detailIncludes({})).not.toContain('messages')
    expect(detailIncludes(null)).not.toContain('messages')
  })
  it('adds messages for session jobs', () => {
    expect(detailIncludes({ session: true })).toContain('messages')
  })
})
