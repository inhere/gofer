import { describe, expect, it } from 'vitest'
import { trackerIssueMatches, trackerRepoLabel } from './trackerView'

describe('tracker view helpers', () => {
  it('formats repositories with an unassigned marker', () => {
    expect(trackerRepoLabel({ project_key: '', prefix: 'go', rel_path: 'repo' })).toBe('未归属 · go · repo')
  })
  it('applies status, type, tag and keyword filters together', () => {
    const issue = { status: 'open', type: 'bug', tags: ['ui'], title: 'drawer polish', description: 'tracker' }
    expect(trackerIssueMatches(issue, 'x-1', ['open'], 'bug', 'ui', 'drawer')).toBe(true)
    expect(trackerIssueMatches(issue, 'x-1', ['closed'], 'bug', 'ui', 'drawer')).toBe(false)
  })
})
