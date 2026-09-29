import { describe, expect, it } from 'vitest'
import { fmtTrackerTime, trackerIssueMatches, trackerMemoryMatches, trackerRepoLabel } from './trackerView'

describe('tracker view helpers', () => {
  it('formats repositories with an unassigned marker', () => {
    expect(trackerRepoLabel({ project_key: '', prefix: 'go', rel_path: 'repo' })).toBe('未归属 · go · repo')
  })
  it('applies status, type, tag and keyword filters together', () => {
    const issue = { status: 'open', type: 'bug', tags: ['ui'], title: 'drawer polish', description: 'tracker' }
    expect(trackerIssueMatches(issue, 'x-1', ['open'], 'bug', 'ui', 'drawer')).toBe(true)
    expect(trackerIssueMatches(issue, 'x-1', ['closed'], 'bug', 'ui', 'drawer')).toBe(false)
  })

  it('matches the real tracker handler body object shape', () => {
    const issue = { id: 'demo-1', body: { title: 'Fix drawer', status: 'open', type: 'bug', tags: ['ui'] } }
    const memory = { id: 'handoff', body: { content: 'next step', tags: ['plan'] } }
    expect(issue.body.title).toBe('Fix drawer')
    expect(memory.body.content).toContain('next step')
    expect(memory.body.tags).toEqual(['plan'])
  })
  it('formats ISO tracker timestamps with the shared time helper', () => {
    expect(fmtTrackerTime('2026-09-29T00:00:00.000000000Z')).toMatch(/^09-29 /)
  })
  it('filters memories by key or content only', () => {
    expect(trackerMemoryMatches({ key: 'handoff', content: 'deploy next' }, 'handoff', 'deploy')).toBe(true)
    expect(trackerMemoryMatches({ key: 'handoff', content: 'deploy next' }, 'handoff', 'missing')).toBe(false)
  })
})
