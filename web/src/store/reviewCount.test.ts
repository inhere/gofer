import { describe, expect, it } from 'vitest'
import { reviewBadgeLabel, shouldShowReviewBadge } from './reviewCount'

describe('review badge', () => {
  it('is hidden when there are no jobs awaiting review', () => {
    expect(shouldShowReviewBadge(0)).toBe(false)
  })

  it('uses the compact topbar label for a positive count', () => {
    expect(shouldShowReviewBadge(3)).toBe(true)
    expect(reviewBadgeLabel(3)).toBe('待验收 3')
  })
})
