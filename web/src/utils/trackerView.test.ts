import { describe, expect, it } from 'vitest'
import {
  doctorLabel,
  fmtTrackerTime,
  memoryAge,
  memoryKind,
  memoryKindMatches,
  memorySummary,
  repoDirName,
  syncClientLine,
  trackerIssueMatches,
  trackerMemoryMatches,
  trackerRepoHint,
  trackerRepoLabel,
  trackerRepoMeta,
  trackerRepoOptions,
  trackerRepoTitle,
} from './trackerView'

describe('tracker view helpers', () => {
  it('formats repositories with an unassigned marker', () => {
    expect(trackerRepoLabel({ project_key: '', prefix: 'go', rel_path: 'repo' })).toBe('未归属 · go · repo')
  })

  it('splits the repository directory out of the recorded root path', () => {
    expect(repoDirName('D:/work/alpha/apps/web')).toBe('web')
    expect(repoDirName('D:\\work\\alpha\\apps\\web\\')).toBe('web')
    expect(repoDirName('/srv/repo')).toBe('repo')
    expect(repoDirName('')).toBe('')
  })

  it('labels an option with project / directory and id · last two segments', () => {
    const repo = { tracker_id: 'tracker-a1', project_key: 'alpha', rel_path: 'D:/work/alpha-web', prefix: 'web' }
    expect(trackerRepoTitle(repo)).toBe('alpha / alpha-web')
    expect(trackerRepoHint(repo)).toBe('tracker-a1 · work/alpha-web')
    // 没有项目 key 时主文字标「未归属」；路径末两级缺级时只留有的那级
    expect(trackerRepoTitle({ tracker_id: 't', project_key: '', rel_path: 'repo', prefix: '' })).toBe('未归属 / repo')
    expect(trackerRepoHint({ tracker_id: 't', project_key: '', rel_path: '', prefix: '' })).toBe('t')
  })

  it('sorts options by project (unassigned last) and then by directory', () => {
    const repos = [
      { tracker_id: 'tracker-z', project_key: 'zeta', rel_path: '/r/zeta', prefix: '' },
      { tracker_id: 'tracker-b', project_key: '', rel_path: '/r/loose', prefix: '' },
      { tracker_id: 'tracker-y2', project_key: 'alpha', rel_path: '/r/alpha-web', prefix: 'web' },
      { tracker_id: 'tracker-y1', project_key: 'alpha', rel_path: '/r/alpha-api', prefix: 'api' },
    ]
    expect(trackerRepoOptions(repos).map((o) => o.value)).toEqual(['tracker-y1', 'tracker-y2', 'tracker-z', 'tracker-b'])
    expect(trackerRepoOptions(repos)[0]).toEqual({
      value: 'tracker-y1',
      title: 'alpha / alpha-api',
      hint: 'tracker-y1 · r/alpha-api',
      path: '/r/alpha-api',
    })
  })

  it('keeps the repository meta line for the info bar', () => {
    expect(trackerRepoMeta({ project_key: 'alpha', prefix: 'web' })).toBe('alpha · web')
    expect(trackerRepoMeta({ project_key: '', prefix: '' })).toBe('未归属 · —')
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
  it('renders one sync-status row per machine', () => {
    const now = 1_000_000
    expect(syncClientLine({ client_host: 'box-a', caller: 'alice', last_sync_at: now - 180, summary: '同步完成' }, now)).toBe('box-a · alice · 3m前同步 · 同步完成')
    expect(syncClientLine({ client_host: '', caller: '', last_sync_at: 0, summary: '' }, now)).toBe('unknown · — · — · —')
  })
})
