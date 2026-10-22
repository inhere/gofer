import { describe, expect, it } from 'vitest'
import { hasReviewHints, hotspotLine, removedCommentLine, reviewHintCount } from './reviewHints'

// 审阅提示（gofer-hjj1）的显示纯函数：口径与服务端 internal/job/reviewhints.go 一致，
// 前端只负责呈现，判定（阈值、排序、上限）全在服务端。
describe('review hints', () => {
  it('hides the card when every section is empty', () => {
    expect(hasReviewHints(null)).toBe(false)
    expect(hasReviewHints({})).toBe(false)
    expect(hasReviewHints({ deletion_hotspots: [], removed_comments: [], verify_lines: [] })).toBe(false)
  })

  it('counts the items of all three sections', () => {
    expect(
      reviewHintCount({
        deletion_hotspots: [{ path: 'a.go', added: 1, deleted: 12, net: -11, deletion_heavy: true }],
        removed_comments: [{ path: 'a.go', line: 3, text: '// note' }],
        verify_lines: ['PASS', 'exit=0'],
      }),
    ).toBe(4)
    expect(reviewHintCount(null)).toBe(0)
  })

  it('shows the card as soon as one section has an item', () => {
    expect(hasReviewHints({ verify_lines: ['ok  pkg'] })).toBe(true)
  })

  it('formats a hotspot as added/deleted, net change and then the path', () => {
    expect(
      hotspotLine({ path: 'internal/job/x.go', added: 4, deleted: 30, net: -26, deletion_heavy: true }),
    ).toBe('⚠ +4/-30 net -26  internal/job/x.go')
    expect(hotspotLine({ path: 'web/src/a.ts', added: 40, deleted: 12, net: 28 })).toBe(
      '+40/-12 net +28  web/src/a.ts',
    )
  })

  it('leaves out the net change for a payload that does not carry it', () => {
    expect(hotspotLine({ path: 'a.go', added: 4, deleted: 30 })).toBe('+4/-30  a.go')
  })

  it('formats a removed comment with its old-file line number', () => {
    expect(removedCommentLine({ path: 'web/src/a.ts', line: 42, text: '// dropped' })).toBe(
      'web/src/a.ts:42  // dropped',
    )
  })
})
