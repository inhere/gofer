// 计划详情页签化与计划总览的新进度数据（UI 重设计 P3，gofer-hez1、gofer-rj5e）：
// 页签值走 ?tab= 深链、每个页签的内容由 active 门控；总览的「待验收」段与卡点列读
// 接口新给的 needs_review / failed_jobs。
import { describe, expect, it } from 'vitest'
import { tabFromQuery, queryWithTab, type TabItem } from '../ui/tabs'
import { planProgressModel } from '../ui/progress'
import type { Plan } from '../api/types'

const raw = (mods: Record<string, unknown>): string => Object.values(mods)[0] as string

const detail = raw(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const overview = raw(import.meta.glob('./Plans.vue', { eager: true, query: '?raw', import: 'default' }))
const row = raw(import.meta.glob('../ui/PlanOverviewRow.vue', { eager: true, query: '?raw', import: 'default' }))

describe('plan detail tabs', () => {
  it('declares the six tabs of the plan page', () => {
    for (const value of ['todos', 'jobs', 'decisions', 'handoff', 'comments', 'events']) {
      expect(detail).toContain(`value: '${value}'`)
    }
    expect(detail).toContain('<Tabs :tabs="planTabs"')
  })

  it('gates every pane on the active tab', () => {
    for (const value of ['todos', 'jobs', 'decisions', 'handoff', 'comments', 'events']) {
      expect(detail).toContain(`active === '${value}'`)
    }
  })

  it('defaults to the todo tab and keeps the chain controls out of the panes', () => {
    expect(detail).toContain(`tabFromQuery(route.query, planTabs.value, 'tab', 'todos')`)
    // 链控制行在页签之前（头部），所以切换页签不会把它藏起来。
    expect(detail.indexOf('class="chain-ctl mono"')).toBeLessThan(detail.indexOf('<Tabs :tabs="planTabs"'))
  })

  it('returns to the overview through BackLink, keeping the list filters', () => {
    expect(detail).toContain('<BackLink list="plans" to="/plans"')
  })

  it('draws the header progress from the server roll-ups', () => {
    expect(detail).toContain('planProgressModel(plan.value)')
    expect(detail).toContain('data-test="plan-detail-progress"')
    expect(detail).toContain('plan.failed_jobs')
  })
})

describe('?tab= deep link', () => {
  const tabs: TabItem[] = [
    { value: 'todos', label: 'Todos' },
    { value: 'jobs', label: 'Jobs' },
  ]

  it('reads a known tab, ignores the default and unknown values', () => {
    expect(tabFromQuery({ tab: 'jobs' }, tabs)).toBe('jobs')
    expect(tabFromQuery({ tab: 'todos' }, tabs)).toBe('todos')
    expect(tabFromQuery({ tab: 'nope' }, tabs)).toBe('todos')
    expect(tabFromQuery({}, tabs)).toBe('todos')
  })

  it('writes the tab into the URL and drops it again on the default tab', () => {
    expect(queryWithTab({}, 'jobs', tabs)).toEqual({ tab: 'jobs' })
    expect(queryWithTab({ tab: 'jobs' }, 'todos', tabs)).toEqual({})
  })
})

describe('plan overview review / failure figures', () => {
  const plan = (patch: Partial<Plan>): Plan => ({
    plan_id: 'gofer-hez1',
    status: 'open',
    created_at: 1_700_000_000,
    updated_at: 1_700_000_000,
    ...patch,
  })

  it('fills the needs-review segment instead of leaving it at zero', () => {
    const model = planProgressModel(
      plan({
        todo_counts: { total: 4, pending: 1, doing: 1, done: 2, skipped: 0 },
        counts: { total: 2, queued: 0, running: 1, done: 0, failed: 1 },
        needs_review: 1,
        failed_jobs: 1,
      }),
    )
    expect(model.parts.find((p) => p.family === 'review')?.count).toBe(1)
  })

  it('shows the review blocker, the failed jobs and the doing todos in the row', () => {
    expect(row).toContain("t('plans.overview.review', { n: review })")
    expect(row).toContain('plan.failed_jobs ?? plan.counts?.failed')
    expect(row).toContain("t('plans.overview.doing', { n: doing })")
  })

  it('marks a paused plan and one that auto-advances', () => {
    expect(row).toContain("t('plans.overview.paused')")
    expect(row).toContain("t('plans.overview.leaderOn')")
  })

  it('keeps the overview filters and paging in the URL', () => {
    expect(overview).toContain('useListReturn(\'plans\', \'/plans\')')
    expect(overview).toContain("t('plans.list.page'")
    expect(overview).toContain('<PlanOverviewRow')
  })
})

describe('plan board on a phone', () => {
  const board = raw(import.meta.glob('../components/PlanBoard.vue', { eager: true, query: '?raw', import: 'default' }))

  it('shows one column at a time with a column switcher', () => {
    expect(board).toContain('class="col-tabs mono"')
    expect(board).toContain("'col--active': mobileCol === c.key")
    // 桌面仍是五列并排：切列标签只在 ≤760 出现，其它列在手机上隐藏。
    expect(board).toMatch(/@media \(max-width: 760px\) \{[\s\S]*?\.col \{\n    display: none;/)
    expect(board).toMatch(/\.col--active \{\n    display: flex;/)
  })
})
