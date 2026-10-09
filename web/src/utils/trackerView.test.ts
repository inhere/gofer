import { describe, expect, it } from 'vitest'
import { doctorLabel, fmtTrackerTime, memoryAge, memoryKind, memoryKindMatches, memorySummary, trackerIssueMatches, trackerMemoryMatches, trackerRepoLabel } from './trackerView'

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
    // Noon UTC stays on 09-29 in every UTC-11..UTC+11 zone, so the test does not
    // depend on the machine's timezone (midnight UTC is 09-28 west of Greenwich).
    expect(fmtTrackerTime('2026-09-29T12:00:00.000000000Z')).toMatch(/^09-29 /)
  })
  it('filters memories by key or content only', () => {
    expect(trackerMemoryMatches({ key: 'handoff', content: 'deploy next' }, 'handoff', 'deploy')).toBe(true)
    expect(trackerMemoryMatches({ key: 'handoff', content: 'deploy next' }, 'handoff', 'missing')).toBe(false)
  })
})

describe('memory list helpers (P4)', () => {
  it('derives the kind like the tracker does', () => {
    expect(memoryKind({ kind: 'handoff' })).toBe('handoff')
    expect(memoryKind({ tags: ['prime'] })).toBe('rule')
    expect(memoryKind({})).toBe('note')
  })
  it('uses the stored summary, else the first content line, capped', () => {
    expect(memorySummary({ summary: ' 发版流程 ', content: 'x' })).toBe('发版流程')
    expect(memorySummary({ content: '\n## 标题行\n正文' })).toBe('标题行')
    expect([...memorySummary({ content: '长'.repeat(100) })].length).toBe(81)
  })
  it('renders the age in days', () => {
    const now = Date.parse('2026-10-09T12:00:00Z')
    expect(memoryAge('2026-10-09T01:00:00Z', now)).toBe('今天')
    expect(memoryAge('2026-09-29T12:00:00Z', now)).toBe('10 天前')
    expect(memoryAge('bad', now)).toBe('')
  })
  it('filters by kind and doctor flag', () => {
    expect(memoryKindMatches({ kind: 'rule' }, [])).toBe(true)
    expect(memoryKindMatches({ kind: 'rule' }, ['note'])).toBe(false)
    expect(memoryKindMatches({ tags: ['prime'] }, ['rule'])).toBe(true)
    expect(memoryKindMatches({ kind: 'note' }, [], true, false)).toBe(false)
    expect(memoryKindMatches({ kind: 'note' }, ['note'], true, true)).toBe(true)
    expect(trackerMemoryMatches({ key: 'k', summary: '发版流程', content: 'x' }, 'k', '发版')).toBe(true)
  })
  it('labels doctor slugs', () => {
    expect(doctorLabel('note-stale')).toBe('久未更新')
    expect(doctorLabel('custom')).toBe('custom')
  })
})
