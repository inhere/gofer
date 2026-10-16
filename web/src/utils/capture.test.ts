import { describe, expect, it } from 'vitest'
import type { TodayAction, TodayCard } from '../api/today'
import type { WorkItem } from '../api/types'
import {
  boardListOpts,
  buildCaptureRequest,
  captureKindLabel,
  capturedListOpts,
  captureSource,
  excludeCaptured,
  onlyCaptured,
  triageQuery,
  triageTabFromQuery,
} from './capture'
import { actionLink, cardLink } from './today'
import { buildClearTopicPatch, buildTopicPatch, hasStoredTopic } from './sessionTopic'

function item(over: Partial<WorkItem>): WorkItem {
  return {
    id: 'w', title: 't', goal: '', status: 'active', status_source: 'auto', source: 'human', unsorted: false,
    rev: 1, created_at: 1, updated_at: 1, last_activity_at: 1, due: false, sessions: [], session_ids: [],
    session_offline: false, links: [], ...over,
  }
}

describe('capture kinds and form', () => {
  it('labels kinds in Chinese', () => {
    expect(['idea', 'issue', 'request', 'note'].map(captureKindLabel)).toEqual(['想法', '问题', '需求', '记录'])
    expect(captureKindLabel(undefined)).toBe('记录')
  })
  it('rejects empty text and unknown kind', () => {
    expect(buildCaptureRequest({ text: '  ', kind: 'note', project: '' }).error).not.toBe('')
    expect(buildCaptureRequest({ text: 'x', kind: 'bogus', project: '' }).req).toBeNull()
  })
  it('builds a trimmed request and omits an empty project', () => {
    expect(buildCaptureRequest({ text: ' 加个导出 ', kind: 'idea', project: ' ' }).req).toEqual({ text: '加个导出', kind: 'idea' })
    expect(buildCaptureRequest({ text: 'a', kind: 'issue', project: ' gofer ' }).req).toEqual({ text: 'a', kind: 'issue', project: 'gofer' })
  })
})

describe('triage tab routing', () => {
  it('?tab=capture opens the box, anything else the drafts', () => {
    expect(triageTabFromQuery('capture')).toBe('capture')
    expect(triageTabFromQuery(['capture'])).toBe('capture')
    expect(triageTabFromQuery(undefined)).toBe('drafts')
    expect(triageTabFromQuery('x')).toBe('drafts')
  })
  it('switching keeps other params and drops tab for drafts', () => {
    expect(triageQuery({ id: 'a' }, 'capture')).toEqual({ id: 'a', tab: 'capture' })
    expect(triageQuery({ id: 'a', tab: 'capture' }, 'drafts')).toEqual({ id: 'a' })
  })
})

describe('board never shows captured', () => {
  it('the board request has no status filter; the box requests captured only', () => {
    expect(boardListOpts(false)).toEqual({ closed: false, limit: 1000 })
    expect('status' in boardListOpts(true)).toBe(false)
    expect(capturedListOpts().status).toEqual(['captured'])
  })
  it('splits captured items out defensively', () => {
    const list = [item({ id: 'a' }), item({ id: 'b', status: 'captured', created_at: 5 }), item({ id: 'c', status: 'captured', created_at: 9 })]
    expect(excludeCaptured(list).map((i) => i.id)).toEqual(['a'])
    expect(onlyCaptured(list).map((i) => i.id)).toEqual(['c', 'b'])
  })
  it('describes the source', () => {
    expect(captureSource({})).toBe('')
    expect(captureSource({ capture_source_session: 'abcdef123456', capture_source_job: 'job-1' })).toBe('会话 abcdef12 / job job-1')
  })
})

describe('Today capture card', () => {
  const card = { key: 'capture:box', kind: 'capture', refs: {}, actions: [] } as unknown as TodayCard
  it('links to the capture tab, and the open action uses its value', () => {
    expect(cardLink(card)).toBe('/work?tab=capture')
    const a: TodayAction = { id: 'open', label: '去整理', style: 'link', value: '/work?tab=capture' }
    expect(actionLink(card, a)).toBe('/work?tab=capture')
    expect(actionLink(card, { ...a, value: '//evil.example' })).toBe('/work?tab=capture')
  })
})

describe('clearing a session topic', () => {
  it('sends an explicit empty topic, scope untouched', () => {
    expect(buildClearTopicPatch()).toEqual({ topic: '' })
    expect(JSON.stringify(buildClearTopicPatch())).toBe('{"topic":""}')
  })
  it('empty input still means unchanged; only stored (non-inferred) topics can be cleared', () => {
    expect(buildTopicPatch({ topic: 'x', topic_source: 'human', scope: [] }, '', '').unchanged).toBe(true)
    expect(hasStoredTopic({ topic: 'x', topic_source: 'human' })).toBe(true)
    expect(hasStoredTopic({ topic: 'x', topic_source: 'inferred' })).toBe(false)
    expect(hasStoredTopic({ topic: '', topic_source: undefined })).toBe(false)
  })
})
