import { describe, expect, it } from 'vitest'
import type { InboxItem, InboxResponse } from '../api/inbox'
import {
  INBOX_KINDS,
  applyStateLocally,
  emptyInbox,
  inboxBadge,
  itemFamily,
  itemLink,
  kindLabelKey,
  normalizeInbox,
  seenKeys,
  stateRequest,
  unreadKeys,
} from './inbox'

function item(key: string, column: 'action' | 'fyi', state: InboxItem['state'], over: Partial<InboxItem> = {}): InboxItem {
  return {
    key,
    column,
    kind: column === 'action' ? 'review' : 'job.terminal',
    title: key,
    summary: '',
    source_revision: 'r1',
    state,
    actions: [],
    refs: {},
    activity_at: 1_760_000_000,
    ...over,
  }
}

function inbox(): InboxResponse {
  return normalizeInbox({
    columns: {
      action: [item('a1', 'action', 'unread'), item('a2', 'action', 'seen'), item('a3', 'action', 'unread')],
      fyi: [item('f1', 'fyi', 'unread'), item('f2', 'fyi', 'seen'), item('f3', 'fyi', 'cleared')],
    },
    unread: { action: 99, fyi: 99 },
    total_unread: 99,
    generated_at: 5,
  })
}

describe('normalizeInbox / badge', () => {
  it('derives per-column unread counts and a badge that only counts the action column', () => {
    const data = inbox()
    expect(data.unread).toEqual({ action: 2, fyi: 1 })
    expect(data.total_unread).toBe(2)
    expect(inboxBadge(data)).toBe(2)
  })

  it('tolerates missing columns and bad badge values', () => {
    expect(normalizeInbox(null)).toEqual(emptyInbox())
    expect(normalizeInbox({ columns: { action: [item('a', 'action', 'unread')] } as InboxResponse['columns'] }).columns.fyi).toEqual([])
    expect(inboxBadge(null)).toBe(0)
    expect(inboxBadge({ ...emptyInbox(), total_unread: -3 })).toBe(0)
    expect(inboxBadge({ ...emptyInbox(), total_unread: Number.NaN })).toBe(0)
    expect(inboxBadge({ ...emptyInbox(), total_unread: 4.9 })).toBe(4)
  })

  it('does not let fyi unread leak into the badge', () => {
    const data = normalizeInbox({ columns: { action: [], fyi: [item('f1', 'fyi', 'unread'), item('f2', 'fyi', 'unread')] } })
    expect(data.unread.fyi).toBe(2)
    expect(inboxBadge(data)).toBe(0)
  })
})

describe('stateRequest', () => {
  it('sends keys for single items and omits an empty key list', () => {
    expect(stateRequest('seen', { keys: ['a1'] })).toEqual({ action: 'seen', keys: ['a1'] })
    expect(stateRequest('seen', { keys: [] })).toEqual({ action: 'seen' })
  })

  it('carries column on bulk requests', () => {
    expect(stateRequest('seen', { column: 'fyi' })).toEqual({ action: 'seen', column: 'fyi' })
    expect(stateRequest('clear', { keys: ['a2'], column: 'action' })).toEqual({ action: 'clear', keys: ['a2'], column: 'action' })
    expect(stateRequest('restore', { keys: ['f3'] })).toEqual({ action: 'restore', keys: ['f3'] })
  })

  it('picks the read / unread keys of a column', () => {
    const data = inbox()
    expect(seenKeys(data.columns.action)).toEqual(['a2'])
    expect(unreadKeys(data.columns.fyi)).toEqual(['f1'])
  })
})

describe('applyStateLocally', () => {
  it('marks one item read and updates counts and badge at once', () => {
    const next = applyStateLocally(inbox(), 'seen', { keys: ['a1'] })
    expect(next.columns.action.find((i) => i.key === 'a1')?.state).toBe('seen')
    expect(next.unread).toEqual({ action: 1, fyi: 1 })
    expect(next.total_unread).toBe(1)
  })

  it('marks a whole column read without touching the other one', () => {
    const next = applyStateLocally(inbox(), 'seen', { column: 'fyi' })
    expect(next.unread).toEqual({ action: 2, fyi: 0 })
    expect(next.total_unread).toBe(2)
    expect(next.columns.fyi.find((i) => i.key === 'f3')?.state).toBe('cleared')
  })

  it('drops cleared items unless cleared ones are shown', () => {
    const hidden = applyStateLocally(inbox(), 'clear', { keys: ['a2', 'a3'] })
    expect(hidden.columns.action.map((i) => i.key)).toEqual(['a1'])
    expect(hidden.unread.action).toBe(1)
    const shown = applyStateLocally(inbox(), 'clear', { keys: ['a2'], showCleared: true })
    expect(shown.columns.action.find((i) => i.key === 'a2')?.state).toBe('cleared')
  })

  it('restores a cleared item to read', () => {
    const next = applyStateLocally(inbox(), 'restore', { keys: ['f3'] })
    expect(next.columns.fyi.find((i) => i.key === 'f3')?.state).toBe('seen')
  })

  it('does not mutate its input', () => {
    const data = inbox()
    applyStateLocally(data, 'clear', { column: 'action' })
    expect(data.columns.action).toHaveLength(3)
  })
})

describe('labels, families and links', () => {
  it('maps kinds to dictionary keys and falls back for unknown kinds', () => {
    expect(kindLabelKey('job.failed')).toBe('inbox.kind.job.failed')
    expect(kindLabelKey('something_new')).toBe('inbox.kind.other')
    expect(INBOX_KINDS).toContain('job.terminal')
  })

  it('colours failures as fail, fyi as idle and the rest of the action column as need', () => {
    expect(itemFamily({ kind: 'job.failed', column: 'action' })).toBe('fail')
    expect(itemFamily({ kind: 'job.terminal', column: 'fyi' })).toBe('idle')
    expect(itemFamily({ kind: 'review', column: 'action' })).toBe('need')
  })

  it('prefers a same-origin deep link and rejects foreign ones', () => {
    expect(itemLink({ kind: 'review', deep_link: '/jobs/j1', refs: {} })).toBe('/jobs/j1')
    expect(itemLink({ kind: 'review', deep_link: '//evil.example/x', refs: { job_id: 'j1' } })).toBe('/jobs/j1')
    expect(itemLink({ kind: 'review', deep_link: 'https://evil.example', refs: {} })).toBe('')
  })

  it('derives a target from refs and kind', () => {
    expect(itemLink({ kind: 'job.failed', refs: { job_id: 'j 1' } })).toBe('/jobs/j%201')
    expect(itemLink({ kind: 'plan_blocked', refs: { plan_id: 'p1' } })).toBe('/plans/p1')
    expect(itemLink({ kind: 'mail_held', refs: {} })).toBe('/mail?state=held')
    expect(itemLink({ kind: 'interaction', refs: {} })).toBe('')
  })
})
