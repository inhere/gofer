import { describe, expect, it } from 'vitest'
import { expiresState, expiresText, expiresTone, isStale, lastUsedText } from './callerExpiry'

// 设计 2026-11-14 §4：到期 / 最近使用的展示与「超过 30 天标灰」的判定。
const NOW = 1_800_000_000
const DAY = 86400

describe('caller expiry display', () => {
  it('treats a missing expiry as never expiring', () => {
    expect(expiresState(0, NOW)).toBe('never')
    expect(expiresState(undefined, NOW)).toBe('never')
    expect(expiresTone(0, NOW)).toBe('ok')
    expect(expiresText(0, NOW)).toBe('永不过期')
  })

  it('marks a past expiry as expired, a near one as soon and the rest as ok', () => {
    expect(expiresState(NOW - 1, NOW)).toBe('expired')
    expect(expiresState(NOW, NOW)).toBe('expired')
    expect(expiresState(NOW + 6 * DAY, NOW)).toBe('soon')
    expect(expiresState(NOW + 30 * DAY, NOW)).toBe('ok')
    expect(expiresTone(NOW - 1, NOW)).toBe('off')
    expect(expiresTone(NOW + 2 * DAY, NOW)).toBe('warn')
    expect(expiresTone(NOW + 60 * DAY, NOW)).toBe('ok')
  })

  it('shows the exact time for an expiry that is still in the future', () => {
    expect(expiresText(NOW - 60, NOW)).toContain('已过期')
    expect(expiresText(NOW + 3 * DAY, NOW)).toContain('到期')
    expect(expiresText(NOW + 60 * DAY, NOW)).not.toContain('过期')
  })

  it('labels a caller that was never used', () => {
    expect(lastUsedText(0)).toBe('从未使用')
    expect(lastUsedText(NOW)).not.toBe('从未使用')
  })
})

describe('stale caller rows', () => {
  it('grays out a caller unused for more than 30 days', () => {
    expect(isStale({ last_used_at: NOW - 31 * DAY }, NOW)).toBe(true)
    expect(isStale({ last_used_at: NOW - 29 * DAY }, NOW)).toBe(false)
    expect(isStale({ last_used_at: NOW }, NOW)).toBe(false)
  })

  it('grays out a never-used caller created more than 30 days ago', () => {
    expect(isStale({ last_used_at: 0, created_at: NOW - 31 * DAY }, NOW)).toBe(true)
    // 刚建的还没用过：不算陈旧
    expect(isStale({ last_used_at: 0, created_at: NOW - DAY }, NOW)).toBe(false)
    // 两个时间都缺（老数据）：不猜
    expect(isStale({}, NOW)).toBe(false)
  })
})
