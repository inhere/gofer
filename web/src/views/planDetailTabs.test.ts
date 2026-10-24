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
    // 默认页签 = 列表第一项（todos）；Tabs 自己的 URL 契约在下面的 ?tab= 用例里覆盖。
    expect(detail.indexOf("value: 'todos'")).toBeLessThan(detail.indexOf("value: 'jobs'"))
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

  it('lays the checklist progress out like the shared ProgressBar (gofer-hez1)', () => {
    // 原来自画 `cbar` + `ops-frac`：条被挤成 56px、「4/6」掉到条下面、与合计 tokens 错位。
    expect(detail).not.toContain('class="cbar"')
    expect(detail).not.toContain('ops-frac')
    const ops = detail.slice(detail.indexOf('class="ops-progress"'), detail.indexOf('class="chain-ctl mono"'))
    expect(ops).toContain('<ProgressBar')
    expect(ops).toContain(':legend="false"')
    expect(ops).toContain('size="sm"')
    // 条可伸缩（与页头同一排版），不写死窄宽度
    expect(detail).toMatch(/\.ops-progress \.ops-bar \{[^}]*flex: 1 1 160px/)
    expect(detail).toContain('const boardModel = computed<ProgressModel>')
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

describe('plan board status colouring (gofer-qw2g)', () => {
  const board = raw(import.meta.glob('../components/PlanBoard.vue', { eager: true, query: '?raw', import: 'default' }))

  it('labels the columns with translated status names instead of the raw keys', () => {
    for (const key of ['pending', 'ready', 'doing', 'needsReview', 'done']) {
      expect(board).toContain(`t('plans.board.label.${key}')`)
    }
    // 解释文案仍留在列头的 title 里，不丢原来的说明。
    expect(board).toContain(":title=\"c.title\"")
    expect(board).toContain("{{ c.label }}")
  })

  it('draws the column glyph and the card status from the shared status families', () => {
    expect(board).toContain('statusFamily(key)')
    expect(board).toContain('<StatusGlyph :family="c.family" :size="13" />')
    expect(board).toContain('<StatusPill :status="card.todo.status" :label="TODO_LABELS[card.todo.status]" />')
    // 卡片与列头同一套词（不给待办状态两种叫法）。
    for (const key of ['pending', 'ready', 'doing', 'done', 'skipped']) {
      expect(board).toContain(`t('plans.board.label.${key}')`)
    }
    // job 状态也换成同一套 pill（原来是自己画点的 StatusBadge）。
    expect(board).toContain('<StatusPill :status="card.job.status" />')
    expect(board).not.toContain('StatusBadge')
    // 手机切列标签同样带图标与状态族类名。
    expect(board).toMatch(/class="col-tab"\n        :class="\[`k-\$\{c.family\}`, \{ 'col-tab--on': mobileCol === c.key \}\]"/)
  })
})

describe('todo tab: view switch and count tones (gofer-qw2g)', () => {
  it('keeps the board / list switch next to the todo heading, not in the page head', () => {
    const head = detail.indexOf('class="detail-head"')
    const headEnd = detail.indexOf('class="head-card"')
    const switchAt = detail.indexOf('class="view-switch mono"')
    const todosHead = detail.indexOf('class="section-head todos-head"')
    expect(head).toBeGreaterThanOrEqual(0)
    expect(todosHead).toBeGreaterThan(headEnd)
    // 页签之内、待办标题旁；页头那一组已经删掉。
    expect(switchAt).toBeGreaterThan(todosHead)
    expect(detail.slice(head, headEnd)).not.toContain('view-switch')
    expect(detail).toContain("t('plans.detail.viewLabel')")
  })

  it('keeps the view choice in the URL and keeps the default board link clean', () => {
    expect(detail).toContain("tabFromQuery(route.query, VIEW_ITEMS, 'view', 'board')")
    expect(detail).toContain("queryWithTab(route.query, next, VIEW_ITEMS, 'view', 'board')")
    // 旧的 localStorage 记忆已删：URL 是唯一来源（可分享、后退可复原）。
    expect(detail).not.toContain('gofer.plans.view')
    expect(detail).not.toContain('localStorage.setItem(VIEW_KEY')
  })

  it('colours the tab counts by meaning through the shared families', () => {
    expect(detail).toContain('tone: todoTabTone(todos, p?.blocked_todo ?? \'\')')
    expect(detail).toContain('tone: jobsTabTone(')
    expect(detail).toContain('tone: decisionsTabTone(')
    const tabs = raw(import.meta.glob('../ui/Tabs.vue', { eager: true, query: '?raw', import: 'default' }))
    expect(tabs).toContain('statusFamilyClass(tab.tone)')
    expect(tabs).toContain("'tab-count--tone'")
    expect(tabs).toContain('.tab-count--tone.k-need')
  })
})
