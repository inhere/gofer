import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createUndoQueue, type UndoEntry } from './undoQueue'

function entry(key: string, log: string[]): UndoEntry {
  return {
    key,
    label: `已处理 ${key}`,
    run: (keepalive) => {
      log.push(`run:${key}:${keepalive}`)
      return Promise.resolve()
    },
    onUndo: () => log.push(`undo:${key}`),
    onDone: () => log.push(`done:${key}`),
  }
}

describe('undo window', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('calls the write API only when the 5s countdown ends', async () => {
    const log: string[] = []
    const q = createUndoQueue(5000)
    q.push(entry('a', log))
    expect(q.current.value).toEqual({ key: 'a', label: '已处理 a', left: 5 })
    vi.advanceTimersByTime(4000)
    expect(q.current.value?.left).toBe(1)
    expect(log).toEqual([])
    vi.advanceTimersByTime(1000)
    await Promise.resolve()
    expect(q.current.value).toBeNull()
    expect(log).toEqual(['run:a:false', 'done:a'])
  })

  it('undo cancels the pending action and nothing is sent', () => {
    const log: string[] = []
    const q = createUndoQueue(5000)
    q.push(entry('a', log))
    vi.advanceTimersByTime(2000)
    expect(q.undo()).toBe(true)
    vi.advanceTimersByTime(10_000)
    expect(log).toEqual(['undo:a'])
    expect(q.current.value).toBeNull()
    expect(q.undo()).toBe(false)
  })

  it('a new action commits the previous one first; flush sends with keepalive', async () => {
    const log: string[] = []
    const q = createUndoQueue(5000)
    q.push(entry('a', log))
    q.push(entry('b', log))
    expect(log).toEqual(['run:a:false'])
    expect(q.current.value?.key).toBe('b')
    q.flush(true)
    await Promise.resolve()
    expect(log).toEqual(['run:a:false', 'run:b:true', 'done:a', 'done:b'])
    expect(q.current.value).toBeNull()
  })

  it('now() sends immediately (cards that time out within 30s)', () => {
    const log: string[] = []
    const q = createUndoQueue(5000)
    q.now(entry('hot', log))
    expect(log).toEqual(['run:hot:false'])
    expect(q.current.value).toBeNull()
  })

  it('a failed write reports through onError', async () => {
    const errors: unknown[] = []
    const q = createUndoQueue(5000)
    q.now({ key: 'x', label: 'x', run: () => Promise.reject(new Error('409')), onError: (e) => errors.push(e) })
    await Promise.resolve()
    await Promise.resolve()
    expect((errors[0] as Error).message).toBe('409')
  })
})
