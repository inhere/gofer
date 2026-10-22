// 格式化的测试（设计稿 §9.1、§13）：同一批输入在 zh-CN / en-US 下的输出。
import { describe, expect, it } from 'vitest'
import {
  formatDate,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatPercent,
  formatRelative,
  formatTime,
  toDate,
} from './format'

const NOW = new Date('2026-10-23T12:00:00Z')

describe('toDate', () => {
  it('accepts Date, ISO string, millisecond and second timestamps', () => {
    expect(toDate('2026-10-23T12:00:00Z')?.toISOString()).toBe('2026-10-23T12:00:00.000Z')
    expect(toDate(Date.UTC(2026, 9, 23, 12))?.toISOString()).toBe('2026-10-23T12:00:00.000Z')
    // 10 位秒级时间戳
    const seconds = Date.UTC(2026, 9, 23, 12) / 1000
    expect(toDate(seconds)?.toISOString()).toBe('2026-10-23T12:00:00.000Z')
  })

  it('returns null for empty and invalid input', () => {
    expect(toDate(null)).toBeNull()
    expect(toDate('')).toBeNull()
    expect(toDate('not a date')).toBeNull()
  })
})

describe('formatRelative', () => {
  it('localises the same distance in zh and en', () => {
    const threeMinAgo = new Date(NOW.getTime() - 3 * 60_000)
    const zh = formatRelative(threeMinAgo, NOW, 'zh-CN')
    const en = formatRelative(threeMinAgo, NOW, 'en-US')
    expect(zh).toContain('3')
    expect(zh.endsWith('前')).toBe(true)
    expect(en).toContain('3')
    expect(en).toContain('minutes ago')
  })

  it('switches units by distance', () => {
    expect(formatRelative(new Date(NOW.getTime() - 10_000), NOW, 'en-US')).toContain('seconds')
    expect(formatRelative(new Date(NOW.getTime() - 5 * 3_600_000), NOW, 'en-US')).toContain('hours')
    // numeric: 'auto' → 昨天 / yesterday
    expect(formatRelative(new Date(NOW.getTime() - 86_400_000), NOW, 'zh-CN')).toBe('昨天')
    expect(formatRelative(new Date(NOW.getTime() - 86_400_000), NOW, 'en-US')).toBe('yesterday')
  })

  it('returns empty string for invalid input', () => {
    expect(formatRelative(undefined, NOW, 'en-US')).toBe('')
  })
})

describe('formatDuration', () => {
  it('picks a readable unit for every magnitude', () => {
    expect(formatDuration(0)).toBe('0ms')
    expect(formatDuration(480)).toBe('480ms')
    expect(formatDuration(1500)).toBe('1.5s')
    expect(formatDuration(45_000)).toBe('45s')
    expect(formatDuration(192_000)).toBe('3m 12s')
    expect(formatDuration(180_000)).toBe('3m')
    expect(formatDuration(3_720_000)).toBe('1h 02m')
    expect(formatDuration(7_200_000)).toBe('2h')
  })

  it('returns empty string for missing or negative durations', () => {
    expect(formatDuration(null)).toBe('')
    expect(formatDuration(undefined)).toBe('')
    expect(formatDuration(-1)).toBe('')
    expect(formatDuration(Number.NaN)).toBe('')
  })
})

describe('number, percent, date and time', () => {
  it('formats numbers per locale', () => {
    expect(formatNumber(1_234_567, 'en-US')).toBe('1,234,567')
    expect(formatNumber(42, 'zh-CN')).toBe('42')
    expect(formatNumber(null, 'en-US')).toBe('')
  })

  it('formats ratios as percent', () => {
    expect(formatPercent(0.256, 'en-US')).toBe('26%')
    expect(formatPercent(0.5, 'en-US', 1)).toBe('50.0%')
    expect(formatPercent(undefined, 'en-US')).toBe('')
  })

  it('formats dates and times per locale', () => {
    expect(formatDate('2026-10-23T12:00:00Z', 'en-US')).toContain('2026')
    expect(formatDate('2026-10-23T12:00:00Z', 'zh-CN')).toContain('2026')
    expect(formatDateTime('2026-10-23T12:00:00Z', 'en-US')).not.toBe('')
    expect(formatTime('2026-10-23T12:00:00Z', 'en-US')).not.toBe('')
    expect(formatDate('', 'en-US')).toBe('')
  })
})
