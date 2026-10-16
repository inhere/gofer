import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PREVIEW_DEBOUNCE_MS, createPreviewScheduler, previewPatterns } from './expandPreview'
import type { ExpandPreview, ExpandResult } from './permissions'

function pv(pattern: string, names: string[], problem?: string): ExpandPreview {
  return {
    pattern,
    subject: 'session:s1',
    kind: 'session',
    permissions: names.map((name) => ({ name, description: name, risk: 'medium', risk_label: '中', session_grantable: true })),
    excluded: [],
    ...(problem ? { problem } : {}),
  }
}

describe('previewPatterns', () => {
  it('asks the server once per pattern with the subject and merges the answers', async () => {
    const calls: string[][] = []
    const r = await previewPatterns('tunnel.open, update.apply', 'session:s1', async (p, s) => {
      calls.push([p, s])
      return pv(p, [p])
    })
    expect(calls).toEqual([['tunnel.open', 'session:s1'], ['update.apply', 'session:s1']])
    expect(r.permissions.map((p: { name: string }) => p.name)).toEqual(['tunnel.open', 'update.apply'])
  })
  it('does not call the server for empty input', async () => {
    let n = 0
    const r = await previewPatterns('  ', 'users', async (p) => { n++; return pv(p, []) })
    expect(n).toBe(0)
    expect(r.problem).toBe('empty')
  })
})

describe('createPreviewScheduler', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('debounces: rapid input produces one request for the last text', async () => {
    const fetcher = vi.fn(async (p: string) => pv(p, [p]))
    const seen: ExpandResult[] = []
    const s = createPreviewScheduler(fetcher, (r) => seen.push(r))
    s.schedule('t', 'users')
    s.schedule('tu', 'users')
    s.schedule('tunnel.open', 'users')
    expect(seen[seen.length - 1]?.problem).toBe('loading')
    expect(fetcher).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(PREVIEW_DEBOUNCE_MS)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(fetcher).toHaveBeenCalledWith('tunnel.open', 'users')
    expect(seen[seen.length - 1]?.permissions.map((p: { name: string }) => p.name)).toEqual(['tunnel.open'])
  })

  it('reports empty immediately and unavailable on failure', async () => {
    const seen: ExpandResult[] = []
    const s = createPreviewScheduler(async () => { throw new Error('缺少权限') }, (r) => seen.push(r))
    s.schedule('', 'users')
    expect(seen[seen.length - 1]?.problem).toBe('empty')
    s.schedule('x', 'users')
    await vi.advanceTimersByTimeAsync(PREVIEW_DEBOUNCE_MS)
    expect(seen[seen.length - 1]).toMatchObject({ problem: 'unavailable', message: '缺少权限' })
  })

  it('drops a late answer that a newer input already superseded, and cancel stops it', async () => {
    let release: (v: ExpandPreview) => void = () => undefined
    const slow = new Promise<ExpandPreview>((res) => { release = res })
    const seen: ExpandResult[] = []
    const s = createPreviewScheduler((p) => (p === 'old' ? slow : Promise.resolve(pv(p, [p]))), (r) => seen.push(r))
    s.schedule('old', 'users')
    await vi.advanceTimersByTimeAsync(PREVIEW_DEBOUNCE_MS)
    s.schedule('new', 'users')
    await vi.advanceTimersByTimeAsync(PREVIEW_DEBOUNCE_MS)
    release(pv('old', ['old']))
    await vi.advanceTimersByTimeAsync(0)
    expect(seen[seen.length - 1]?.permissions.map((p: { name: string }) => p.name)).toEqual(['new'])
    s.schedule('later', 'users')
    s.cancel()
    const n = seen.length
    await vi.advanceTimersByTimeAsync(PREVIEW_DEBOUNCE_MS)
    expect(seen.length).toBe(n)
  })
})
