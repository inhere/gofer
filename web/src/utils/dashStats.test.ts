import { describe, expect, it } from 'vitest'
import type { Overview, OverviewDay } from '../api/overview'
import { browserTZ } from '../api/overview'
import {
  allowedBuckets,
  barLayout,
  bucketize,
  copyText,
  DEFAULT_RANGE,
  defaultBucket,
  fmtDur,
  fmtInt,
  fmtK,
  fmtPct,
  heatGrid,
  heatLevel,
  hourRows,
  mondayOf,
  perJob,
  RANGE_LABEL,
  RANGES,
  topN,
} from './dashStats'

function days(from: string, n: number, f: (i: number) => Partial<OverviewDay> = () => ({})): OverviewDay[] {
  const out: OverviewDay[] = []
  const d = new Date(`${from}T00:00:00Z`)
  for (let i = 0; i < n; i++) {
    out.push({ day: d.toISOString().slice(0, 10), done: 0, failed: 0, commits: 0, wall_sec: 0, ...f(i) })
    d.setUTCDate(d.getUTCDate() + 1)
  }
  return out
}

describe('dashStats buckets', () => {
  it('limits buckets per range and picks the default', () => {
    expect(allowedBuckets('today')).toEqual(['hour'])
    expect(defaultBucket('today')).toBe('hour')
    expect(allowedBuckets('7d')).toEqual(['day'])
    expect(allowedBuckets('30d')).toEqual(['day', 'week'])
    expect(allowedBuckets('all')).toEqual(['day', 'week', 'month'])
    expect(defaultBucket('all')).toBe('week')
    expect(defaultBucket('30d')).toBe('day')
  })

  it('ranges run today → 7d → 30d → all and default to 7d', () => {
    expect(RANGES).toEqual(['today', '7d', '30d', 'all'])
    expect(RANGES.map((r) => RANGE_LABEL[r])).toEqual(['今日', '近 7 天', '近 30 天', '全部'])
    expect(DEFAULT_RANGE).toBe('7d')
    expect(defaultBucket(DEFAULT_RANGE)).toBe('day')
  })

  it('today buckets by hour: 24 rows in order, the current hour flagged partial', () => {
    const hourly = Array.from({ length: 24 }, (_, h) => ({ hour: h, done: h === 13 ? 3 : 0, failed: h === 9 ? 1 : 0, commits: 0, wall_sec: 0 }))
    const rows = hourRows(hourly, 13)
    expect(rows).toHaveLength(24)
    expect(rows[0].label).toBe('00:00')
    expect(rows[23].label).toBe('23:00')
    expect(rows[13]).toMatchObject({ done: 3, failed: 0, partial: true, full: '今日 13:00–14:00' })
    expect(rows[9]).toMatchObject({ done: 0, failed: 1, partial: false })
    expect(rows.filter((r) => r.partial)).toHaveLength(1)
    expect(barLayout(rows, 600, 160).max).toBe(3)
  })

  it('weeks start on Monday and partial edges are flagged', () => {
    expect(mondayOf('2026-10-08')).toBe('2026-10-05') // Thursday
    expect(mondayOf('2026-10-05')).toBe('2026-10-05')
    expect(mondayOf('2026-10-11')).toBe('2026-10-05') // Sunday belongs to the week before
    // 2026-10-01 (Thu) … 2026-10-14 (Wed): partial, full, partial
    const rows = bucketize(days('2026-10-01', 14, (i) => ({ done: 1, failed: i % 2 })), 'week')
    expect(rows.map((r) => [r.key, r.done, r.days, r.partial])).toEqual([
      ['2026-09-28', 4, 4, true],
      ['2026-10-05', 7, 7, false],
      ['2026-10-12', 3, 3, true],
    ])
    expect(rows[1].failed).toBe(3)
  })

  it('months sum by calendar month, days keep one row each', () => {
    const daily = days('2026-09-29', 5, () => ({ done: 2 }))
    const months = bucketize(daily, 'month')
    expect(months.map((m) => [m.key, m.label, m.done, m.partial])).toEqual([
      ['2026-09', '9月', 4, true],
      ['2026-10', '10月', 6, true],
    ])
    const byDay = bucketize(daily, 'day')
    expect(byDay).toHaveLength(5)
    expect(byDay[0].label).toBe('9/29')
    expect(byDay[0].full).toBe('2026-09-29 周二')
  })

  it('bar layout stacks failed above done with a 2px gap', () => {
    const rows = bucketize(days('2026-10-01', 2, (i) => (i === 0 ? { done: 3, failed: 1 } : { done: 0 })), 'day')
    const { bars, max } = barLayout(rows, 100, 114)
    expect(max).toBe(4)
    expect(bars[0].doneH).toBe(75)
    expect(bars[0].doneY).toBe(39)
    expect(bars[0].failH).toBe(25)
    expect(bars[0].failY).toBe(39 - 25 - 2)
    expect(bars[1].doneH).toBe(0)
    expect(bars[0].x + bars[0].w).toBeLessThan(bars[1].x)
    expect(bars[0].tip).toContain('完成 3 · 失败 1')
  })
})

describe('dashStats heatmap', () => {
  it('levels split by the server thresholds; zero is level 0', () => {
    const lv = [2, 4, 9]
    expect([0, 1, 2, 3, 4, 9, 10].map((v) => heatLevel(v, lv))).toEqual([0, 1, 1, 2, 2, 3, 4])
    expect(heatLevel(5, [])).toBe(4)
  })

  it('grid has one column per week, Monday first, future days hidden', () => {
    const grid = heatGrid([{ day: '2026-10-06', done: 5 }], 6, [1, 2, 3], '2026-10-08')
    expect(grid).toHaveLength(6)
    expect(grid[5][0].day).toBe('2026-10-05')
    expect(grid[0][0].day).toBe('2026-08-31')
    expect(grid[5][1]).toEqual({ day: '2026-10-06', done: 5, level: 4, future: false })
    expect(grid[5][3].future).toBe(false) // 2026-10-08 is today
    expect(grid[5][2].future).toBe(false)
    expect(grid[5][4].future).toBe(true)
  })
})

describe('dashStats formatting', () => {
  it('unknown values render as a dash, never zero', () => {
    expect(fmtDur(null)).toBe('—')
    expect(fmtInt(null)).toBe('—')
    expect(fmtK(undefined)).toBe('—')
    expect(fmtPct(null)).toBe('—')
    expect(perJob(null, 4)).toBe('—')
    expect(perJob(10, 0)).toBe('—')
  })

  it('durations and big numbers', () => {
    expect(fmtDur(45)).toBe('45s')
    expect(fmtDur(600)).toBe('10m')
    expect(fmtDur(3600 * 235 + 51 * 60)).toBe('235h 51m')
    expect(fmtK(16012)).toBe('16k')
    expect(fmtK(1500)).toBe('1.5k')
    expect(fmtK(1_900_000)).toBe('1.9M')
    expect(fmtPct(0.914)).toBe('91%')
    expect(perJob(17, 5)).toBe('3.4')
  })

  it('topN drops empty entries', () => {
    const rows = [{ k: 'a', n: 1 }, { k: 'b', n: 5 }, { k: 'c', n: 0 }, { k: 'd', n: 3 }]
    expect(topN(rows, (r) => r.n).map((r) => r.k)).toEqual(['b', 'd', 'a'])
    expect(topN(rows, (r) => (r.k === 'c' ? 1 : 0)).map((r) => r.k)).toEqual(['c'])
  })

  it('browser tz is east-positive minutes', () => {
    const fake = { getTimezoneOffset: () => -480 } as Date
    expect(browserTZ(fake)).toBe(480)
  })
})

describe('dashStats copy text', () => {
  it('writes a dash for metrics without data', () => {
    const ov = {
      range: { key: '7d', from: 0, to: 0, tz: 480, first_job_at: 0 },
      totals: { jobs: 12, sessions: 2, wall_sec: 7200 },
      jobs: { total: 10, done: 8, failed: 1, cancelled: 1, rejected: 0, needs_review: 0, in_progress: 2, success_rate: 8 / 9 },
      time: { wall_sec: 7200, avg_sec: 720, median_sec: 600, active_sec: null, human_wait_sec: null },
      git: { commits: 4, jobs_with_commits: 2, files: null, insertions: null, deletions: null, git_jobs: 0 },
      signal: null,
      review: { accept_rate: null, plans_done: 1 },
      usage: null,
    } as unknown as Overview
    const text = copyText(ov)
    expect(text).toContain('近 7 天 · 12 jobs · 2 会话 · 运行 2h 00m')
    expect(text).toContain('Jobs 12（完成 8 / 失败 1 / 进行中 2）· 成功率 89%')
    expect(text).toContain('Git 提交 4 · 文件 — · +— −—')
    expect(text).toContain('轮次 — · 工具调用 — · 人介入 —')
    expect(text).toContain('费用 —')
  })
})
