import { describe, expect, it } from 'vitest'
import {
  DEFAULT_BASE,
  DEFAULT_WIDTHS,
  PAGES,
  UsageError,
  countShots,
  evaluateSample,
  exitCode,
  expandCombos,
  navigationIssue,
  parseArgs,
  pickDetailId,
  shotName,
  summarize,
  viewportFor,
} from './shots.lib.mjs'

describe('parseArgs', () => {
  it('uses defaults and reads the token from env only', () => {
    const o = parseArgs([], { GOFER_TOKEN: 'sekret' })
    expect(o.base).toBe(DEFAULT_BASE)
    expect(o.widths).toEqual(DEFAULT_WIDTHS)
    expect(o.themes).toEqual(['light', 'dark'])
    expect(o.langs).toEqual(['zh-CN', 'en-US'])
    expect(o.pages).toEqual(PAGES.map((p) => p.name))
    expect(o.out).toBe('web/shots')
    expect(o.token).toBe('sekret')
    expect(o.detail).toBe(true)
  })

  it('supports a custom token env name and empty token', () => {
    expect(parseArgs(['--token-env', 'MY_T'], { MY_T: 'a', GOFER_TOKEN: 'b' }).token).toBe('a')
    expect(parseArgs([], {}).token).toBe('')
  })

  it('rejects a literal --token', () => {
    expect(() => parseArgs(['--token', 'abc'])).toThrow(UsageError)
    expect(() => parseArgs(['--token=abc'])).toThrow(/环境变量/)
  })

  it('parses filters and normalizes langs / base', () => {
    const o = parseArgs(['--base=http://h:1/', '--pages', 'jobs,plans', '--widths', '390,800', '--langs', 'en,zh', '--themes', 'dark', '--no-detail', '--fail-on-warn'])
    expect(o.base).toBe('http://h:1')
    expect(o.pages).toEqual(['jobs', 'plans'])
    expect(o.widths).toEqual([390, 800])
    expect(o.langs).toEqual(['en-US', 'zh-CN'])
    expect(o.themes).toEqual(['dark'])
    expect(o.detail).toBe(false)
    expect(o.failOnWarn).toBe(true)
  })

  it('rejects bad input', () => {
    expect(() => parseArgs(['--pages', 'nope'])).toThrow(/未知页面/)
    expect(() => parseArgs(['--widths', 'abc'])).toThrow(UsageError)
    expect(() => parseArgs(['--themes', 'sepia'])).toThrow(UsageError)
    expect(() => parseArgs(['--langs', 'fr'])).toThrow(UsageError)
    expect(() => parseArgs(['--base', 'ftp://x'])).toThrow(UsageError)
    expect(() => parseArgs(['--bogus'])).toThrow(/未知参数/)
    expect(() => parseArgs(['--out'])).toThrow(/需要一个值/)
    expect(() => parseArgs(['stray'])).toThrow(UsageError)
  })

  it('returns help', () => {
    expect(parseArgs(['--help']).help).toBe(true)
  })
})

describe('expandCombos', () => {
  const base = { pages: PAGES.map((p) => p.name), widths: DEFAULT_WIDTHS, themes: ['light', 'dark'], langs: ['zh-CN', 'en-US'] }

  it('expands width x theme x lang groups; detail follows its list page', () => {
    const groups = expandCombos({ ...base, detailIds: { jobs: 'j1', plans: 'p1' } })
    expect(groups).toHaveLength(4 * 2 * 2)
    const t = groups[0].targets.map((x) => `${x.page}:${x.kind}`)
    expect(t).toEqual(['today:list', 'workbench:list', 'jobs:list', 'jobs:detail', 'plans:list', 'plans:detail', 'sessions:list', 'workflows:list'])
    expect(groups[0].targets.find((x) => x.kind === 'detail' && x.page === 'jobs').path).toBe('/jobs/j1')
    expect(countShots(groups)).toBe(16 * 8)
  })

  it('skips detail when disabled or id missing, and honours page filter', () => {
    const g = expandCombos({ ...base, pages: ['jobs'], detailIds: { jobs: 'j1' }, includeDetail: false })
    expect(g[0].targets).toHaveLength(1)
    expect(expandCombos({ ...base, pages: ['jobs'] })[0].targets).toHaveLength(1)
  })

  it('encodes ids and builds stable file names', () => {
    const g = expandCombos({ ...base, pages: ['plans'], widths: [390], themes: ['dark'], langs: ['en-US'], detailIds: { plans: 'a/b' } })[0]
    expect(g.targets[1].path).toBe('/plans/a%2Fb')
    expect(shotName(g, g.targets[0])).toBe('plans__390__dark__en.png')
    expect(shotName(g, g.targets[1])).toBe('plans-detail__390__dark__en.png')
  })

  it('picks viewport height by width', () => {
    expect(viewportFor(390)).toEqual({ width: 390, height: 844 })
    expect(viewportFor(1440).height).toBe(900)
  })
})

describe('pickDetailId', () => {
  it('reads the first id', () => {
    expect(pickDetailId({ jobs: [{ id: 'x' }] }, 'jobs')).toBe('x')
    expect(pickDetailId({ jobs: [] }, 'jobs')).toBe('')
    expect(pickDetailId(null, 'jobs')).toBe('')
    expect(pickDetailId({ jobs: [{ id: 5 }] }, 'jobs')).toBe('')
  })
})

describe('evaluateSample', () => {
  const clean = { viewportWidth: 1440, scrollWidth: 1440, clientWidth: 1440, svgs: [{ el: 'svg', width: 16, height: 16 }], wraps: [{ el: 'span.pill', text: '3', lines: 1 }], cjk: [] }

  it('passes a clean sample', () => {
    expect(evaluateSample(clean)).toEqual([])
    expect(evaluateSample(undefined)).toEqual([])
  })

  it('flags horizontal overflow beyond 1px tolerance', () => {
    expect(evaluateSample({ ...clean, scrollWidth: 1441 })).toEqual([])
    const r = evaluateSample({ ...clean, scrollWidth: 1500 })
    expect(r).toHaveLength(1)
    expect(r[0]).toMatchObject({ check: 'horizontal-overflow', severity: 'error' })
  })

  it('flags inline svg over 40px but not exactly 40', () => {
    expect(evaluateSample({ ...clean, svgs: [{ el: 'svg', width: 40, height: 40 }] })).toEqual([])
    const r = evaluateSample({ ...clean, svgs: [{ el: 'svg.hero', width: 120, height: 32 }] })
    expect(r[0]).toMatchObject({ check: 'oversize-svg', severity: 'error' })
    expect(r[0].message).toContain('svg.hero')
  })

  it('flags labels wrapped to two or more lines', () => {
    const r = evaluateSample({ ...clean, wraps: [{ el: 'span.chip', text: 'Needs review', lines: 2 }] })
    expect(r[0]).toMatchObject({ check: 'wrapped-label', severity: 'error' })
  })

  it('caps repeated findings with a summary line', () => {
    const svgs = Array.from({ length: 25 }, () => ({ el: 'svg', width: 99, height: 99 }))
    const r = evaluateSample({ ...clean, svgs })
    expect(r).toHaveLength(11)
    expect(r[10].message).toContain('15')
  })

  it('reports residual CJK only for en-US, as warn', () => {
    const s = { ...clean, cjk: [{ el: 'h1', text: '今天' }] }
    expect(evaluateSample(s, { lang: 'zh-CN' })).toEqual([])
    const r = evaluateSample(s, { lang: 'en-US' })
    expect(r).toEqual([expect.objectContaining({ check: 'residual-cjk', severity: 'warn' })])
  })
})

describe('navigationIssue / summarize / exitCode', () => {
  it('detects redirects to /access', () => {
    expect(navigationIssue('/jobs', '/jobs')).toBeNull()
    expect(navigationIssue('/jobs?x=1', '/jobs/')).toBeNull()
    expect(navigationIssue('/jobs', '/access').message).toContain('token')
    expect(navigationIssue('/jobs', '/today').check).toBe('navigation')
  })

  it('summarizes by check and decides the exit code', () => {
    const entries = [
      { issues: [] },
      { issues: [{ check: 'oversize-svg', severity: 'error' }, { check: 'residual-cjk', severity: 'warn' }] },
      { issues: [{ check: 'residual-cjk', severity: 'warn' }] },
    ]
    const s = summarize(entries)
    expect(s).toMatchObject({ shots: 3, errors: 1, warnings: 2, failedShots: 1 })
    expect(s.byCheck['residual-cjk']).toEqual({ error: 0, warn: 2 })
    expect(exitCode(s)).toBe(1)
    const warnOnly = summarize([entries[2]])
    expect(exitCode(warnOnly)).toBe(0)
    expect(exitCode(warnOnly, { failOnWarn: true })).toBe(1)
    expect(exitCode(summarize([]))).toBe(0)
  })
})
