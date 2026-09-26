import { describe, expect, it } from 'vitest'
import {
  addReviewComment,
  buildReviewRequest,
  createReviewDraftState,
  removeReviewComment,
  setReviewSummary,
  updateReviewComment,
} from './reviewDrafts'

describe('reviewDrafts', () => {
  it('isolates immutable add, edit, and delete operations by thread', () => {
    const empty = createReviewDraftState()
    const withA = addReviewComment(empty, 's:thread-a', {
      id: 'comment-a',
      path: 'a.go',
      line: 7,
      side: 'new',
      text: 'first',
    })
    const withBoth = addReviewComment(withA, 's:thread-b', {
      id: 'comment-b',
      path: 'b.go',
      line: 3,
      side: 'old',
      text: 'second',
    })
    const edited = updateReviewComment(withBoth, 's:thread-a', 'comment-a', 'edited')
    const removed = removeReviewComment(edited, 's:thread-a', 'comment-a')

    expect(empty).toEqual({})
    expect(withA['s:thread-a'].comments[0].text).toBe('first')
    expect(edited['s:thread-a'].comments[0].text).toBe('edited')
    expect(edited['s:thread-b']).toEqual(withBoth['s:thread-b'])
    expect(removed['s:thread-a'].comments).toEqual([])
    expect(removed['s:thread-b'].comments).toHaveLength(1)
  })

  it('builds the API body without browser-only comment ids', () => {
    let state = createReviewDraftState()
    state = setReviewSummary(state, 's:thread-a', '  summary first  ')
    state = addReviewComment(state, 's:thread-a', {
      id: 'local-only',
      path: 'main.go',
      line: 12,
      side: 'new',
      text: '  explain this  ',
    })

    expect(buildReviewRequest(state, 's:thread-a')).toEqual({
      summary: 'summary first',
      comments: [{ path: 'main.go', line: 12, side: 'new', text: 'explain this' }],
    })
    expect(buildReviewRequest(state, 's:missing')).toEqual({ summary: '', comments: [] })
  })
})
