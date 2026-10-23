import { describe, expect, it } from 'vitest'
import { clearSendQueue, normalizeQueue, readSendQueue, sendQueueKey, writeSendQueue } from './sendQueue'

function fakeStorage(): Storage {
  const map = new Map<string, string>()
  return {
    get length() { return map.size },
    clear: () => map.clear(),
    getItem: (key: string) => map.get(key) ?? null,
    key: (index: number) => [...map.keys()][index] ?? null,
    removeItem: (key: string) => { map.delete(key) },
    setItem: (key: string, value: string) => { map.set(key, value) },
  } as Storage
}

// 评审意见：待发队列要按会话持久化，刷新后恢复，发出或丢弃后清除。
describe('workbench send queue persistence', () => {
  it('round-trips a queue under a per-session key', () => {
    const storage = fakeStorage()
    writeSendQueue('s:abc', ['first', 'second'], storage)
    expect(storage.getItem(sendQueueKey('s:abc'))).toBe('["first","second"]')
    expect(readSendQueue('s:abc', storage)).toEqual(['first', 'second'])
    // 不同会话的队列互不影响
    expect(readSendQueue('s:other', storage)).toEqual([])
    writeSendQueue('s:other', ['mine'], storage)
    expect(readSendQueue('s:abc', storage)).toEqual(['first', 'second'])
    expect(readSendQueue('s:other', storage)).toEqual(['mine'])
  })

  it('clears the key once the queue is empty or discarded', () => {
    const storage = fakeStorage()
    writeSendQueue('s:abc', ['draft'], storage)
    writeSendQueue('s:abc', [], storage)
    expect(storage.getItem(sendQueueKey('s:abc'))).toBeNull()
    writeSendQueue('s:abc', ['draft'], storage)
    clearSendQueue('s:abc', storage)
    expect(storage.getItem(sendQueueKey('s:abc'))).toBeNull()
    expect(readSendQueue('s:abc', storage)).toEqual([])
  })

  it('ignores broken values instead of blocking the composer', () => {
    const storage = fakeStorage()
    storage.setItem(sendQueueKey('s:abc'), '{not json')
    expect(readSendQueue('s:abc', storage)).toEqual([])
    storage.setItem(sendQueueKey('s:abc'), '{"a":1}')
    expect(readSendQueue('s:abc', storage)).toEqual([])
    storage.setItem(sendQueueKey('s:abc'), '["ok", 3, "", "  ", " next "]')
    expect(readSendQueue('s:abc', storage)).toEqual(['ok', 'next'])
  })

  it('caps the number and length of queued messages', () => {
    expect(normalizeQueue(Array.from({ length: 40 }, (_, i) => `m${i}`))).toHaveLength(20)
    expect(normalizeQueue(['x'.repeat(9000)])[0]).toHaveLength(8000)
    expect(normalizeQueue([42, null, undefined, {}])).toEqual([])
  })

  it('degrades to an empty queue without storage', () => {
    writeSendQueue('s:abc', ['a'], null)
    expect(readSendQueue('s:abc', null)).toEqual([])
    expect(readSendQueue('', fakeStorage())).toEqual([])
  })
})
