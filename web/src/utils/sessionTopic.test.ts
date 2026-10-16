import { describe, expect, it } from 'vitest'
import {
  buildFindParams,
  buildTopicPatch,
  canEditTopic,
  findSummary,
  hitLabel,
  hitReasons,
  parseScopeInput,
  topicDisplay,
  validateTopic,
} from './sessionTopic'
import type { AuthWhoami } from './permissions'

const me = (perms: string[] = [], root = false): AuthWhoami => ({
  caller_id: 'alice',
  kind: 'user',
  root,
  permissions: perms.map((name) => ({ name, source: 'role' })),
  grants_version: 1,
})

describe('topic display rules', () => {
  it('greys an inferred topic and tags it 推断', () => {
    const d = topicDisplay({ topic: 'web/src · dev', topic_source: 'inferred' })
    expect(d.inferred).toBe(true)
    expect(d.tag).toBe('推断')
  })
  it('shows self / human sources as small tags, not greyed', () => {
    expect(topicDisplay({ topic: 'x', topic_source: 'self' })).toMatchObject({ inferred: false, tag: '自述' })
    expect(topicDisplay({ topic: 'x', topic_source: 'human' })).toMatchObject({ inferred: false, tag: '人工' })
  })
  it('has no text when there is no topic', () => {
    expect(topicDisplay({}).text).toBe('')
    expect(topicDisplay({}).tag).toBe('')
  })
})

describe('edit permission', () => {
  const mine = { state: 'running' as const, caller_id: 'alice' }
  const theirs = { state: 'running' as const, caller_id: 'bob' }
  it('owner and ownerless sessions need nothing', () => {
    expect(canEditTopic(me(), mine).ok).toBe(true)
    expect(canEditTopic(me(), { state: 'idle', caller_id: '' }).ok).toBe(true)
  })
  it("someone else's session needs session.manage.others", () => {
    const r = canEditTopic(me(), theirs)
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('session.manage.others')
    expect(canEditTopic(me(['session.manage.others']), theirs).ok).toBe(true)
    expect(canEditTopic(me([], true), theirs).ok).toBe(true)
  })
  it('an unknown whoami does not hide the edit', () => {
    expect(canEditTopic(null, theirs).ok).toBe(true)
  })
  it('an ended session cannot be edited, with a reason', () => {
    const r = canEditTopic(me([], true), { state: 'ended', caller_id: 'alice' })
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('已结束')
  })
})

describe('scope validation', () => {
  it('cleans and dedupes', () => {
    expect(parseScopeInput('./a/b/\na/b, c').items).toEqual(['a/b', 'c'])
  })
  it('rejects absolute paths, .. and the root', () => {
    for (const bad of ['/etc', '\\x', 'C:\\x', 'D:/x', 'a/../b', '..', '.', './']) {
      expect(parseScopeInput(bad).error, bad).not.toBe('')
    }
  })
  it('caps at 20 items', () => {
    const many = Array.from({ length: 21 }, (_, i) => `d${i}`).join('\n')
    expect(parseScopeInput(many).error).toContain('20')
    expect(parseScopeInput(many.split('\n').slice(0, 20).join('\n')).error).toBe('')
  })
  it('validates topic length and control chars', () => {
    expect(validateTopic('x'.repeat(200))).toBe('')
    expect(validateTopic('x'.repeat(201))).not.toBe('')
    expect(validateTopic('a\u0001b')).not.toBe('')
  })
})

describe('patch building', () => {
  const s = { topic: 'old', topic_source: 'self', scope: ['a'] }
  it('sends only changed fields', () => {
    expect(buildTopicPatch(s, 'new', 'a').patch).toEqual({ topic: 'new' })
    expect(buildTopicPatch(s, 'old', 'a\nb').patch).toEqual({ scope: ['a', 'b'] })
    expect(buildTopicPatch(s, 'old', 'a').unchanged).toBe(true)
  })
  it('clears scope with an empty list', () => {
    expect(buildTopicPatch(s, 'old', '').patch).toEqual({ scope: [] })
  })
  it('does not freeze an inferred topic', () => {
    expect(buildTopicPatch({ topic: 'dir', topic_source: 'inferred' }, '', '').unchanged).toBe(true)
  })
  it('reports validation errors', () => {
    expect(buildTopicPatch(s, 'x', '/abs').error).not.toBe('')
  })
})

describe('find', () => {
  it('treats slash / backslash without whitespace as a path', () => {
    expect(buildFindParams('internal/job')).toEqual({ path: 'internal/job' })
    expect(buildFindParams('internal\\job')).toEqual({ path: 'internal/job' })
  })
  it('treats everything else as keywords', () => {
    expect(buildFindParams('billing refactor')).toEqual({ q: 'billing refactor' })
    expect(buildFindParams('a/b c')).toEqual({ q: 'a/b c' })
    expect(buildFindParams('  ')).toBeNull()
  })
  it('labels hits in Chinese', () => {
    expect(hitLabel({ kind: 'scope' })).toBe('范围命中')
    expect(hitLabel({ kind: 'topic' })).toBe('主题命中')
    expect(hitLabel({ kind: 'topic', source: 'inferred' })).toBe('主题命中（推断）')
    expect(hitLabel({ kind: 'peer_name' })).toBe('会话名')
    expect(hitLabel({ kind: 'project' })).toBe('项目')
    expect(hitReasons([{ kind: 'scope', match: 'internal/job' }])).toEqual(['范围命中：internal/job'])
  })
  it('summarises an empty result', () => {
    expect(findSummary(0, 'zzz')).toContain('没有找到')
    expect(findSummary(3, 'zzz')).toContain('3')
  })
})
