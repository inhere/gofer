import { describe, expect, it } from 'vitest'
import { shouldShowWorkBadge, workBadgeLabel } from './workNeedsMe'

describe('work needs-me badge', () => {
  it('is hidden at zero', () => {
    expect(shouldShowWorkBadge(0)).toBe(false)
    expect(shouldShowWorkBadge(2)).toBe(true)
  })
  it('caps the label', () => {
    expect(workBadgeLabel(3)).toBe('3')
    expect(workBadgeLabel(120)).toBe('99+')
  })
})
