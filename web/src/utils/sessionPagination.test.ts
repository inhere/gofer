import { describe, expect, it } from 'vitest'
import { mergeNewestPage, mergeOlderPage, preserveScrollAfterPrepend, shouldFollowBottom } from './sessionPagination'

describe('session pagination helpers', () => {
  it('keeps newest first and removes duplicate cursor rows', () => {
    expect(mergeOlderPage(['new', 'old'], ['old', 'older'])).toEqual(['new', 'old', 'older'])
  })

  it('merges a polling page without dropping already loaded older rows', () => {
    expect(mergeNewestPage(['new-3', 'new-2'], ['new-2', 'old-1', 'old-0'])).toEqual([
      'new-3', 'new-2', 'old-1', 'old-0',
    ])
  })

  it('calculates the scroll correction after prepending older content', () => {
    expect(preserveScrollAfterPrepend({ beforeHeight: 1000, beforeTop: 240, afterHeight: 1600 })).toBe(840)
  })

  it('only follows new content when the reader was already at the bottom', () => {
    expect(shouldFollowBottom({ scrollHeight: 1000, scrollTop: 700, clientHeight: 300 })).toBe(true)
    expect(shouldFollowBottom({ scrollHeight: 1000, scrollTop: 200, clientHeight: 300 })).toBe(false)
  })
})
