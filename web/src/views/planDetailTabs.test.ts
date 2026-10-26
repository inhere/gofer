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

  it('keeps the view choice in the URL and remembers it in this browser', () => {
    // URL 仍然优先、默认看板不写进 URL（可分享、后退可复原）。
    expect(detail).toContain("queryWithTab(route.query, next, VIEW_ITEMS, 'view', PLAN_VIEW_DEFAULT)")
    // issue gofer-5inz：选择同时记到本浏览器（按 plan + 全局「最近一次」），刷新 / 再打开沿用；
    // 判定顺序（URL → 记忆 → 看板）只在 utils/planView.ts 里写一份。
    expect(detail).toContain('resolvePlanView(route.query.view, props.id)')
    expect(detail).toContain('rememberPlanView(props.id, next)')
    expect(detail).not.toContain('tabFromQuery(route.query, VIEW_ITEMS')
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

describe('board empty columns (gofer-5inz)', () => {
  const board = raw(import.meta.glob('../components/PlanBoard.vue', { eager: true, query: '?raw', import: 'default' }))

  it('narrows empty columns to about a third of a normal one', () => {
    // 宽度轨道按每列卡片数算（纯函数在 utils/planBoard.ts，用例见 utils/planBoard.test.ts）。
    expect(board).toContain('boardTracks(columns.value.map((c) => c.cards.length))')
    expect(board).toContain("'--board-tracks': tracks") // :style 变量
    expect(board).toContain('grid-template-columns: var(--board-tracks')
    expect(board).toContain("'col--empty': c.cards.length === 0")
  })

  it('keeps the empty column as a drop target and leaves the phone layout alone', () => {
    // 空列仍渲染 col-body 与落点（仍能拖进去）
    expect(board).toContain('@dragover="onDragOverCol(c.key, $event)"')
    expect(board).toContain("@media (min-width: 761px) {\n  .col--empty .col-name {")
    expect(board).toContain('.col--empty .zones {')
  })
})

describe('todo detail drawer (gofer-5inz)', () => {
  const board = raw(import.meta.glob('../components/PlanBoard.vue', { eager: true, query: '?raw', import: 'default' }))

  it('opens from a board card, from the list title and from a details button', () => {
    expect(board).toContain("@click=\"emit('open', card.todo)\"")
    expect(detail).toContain('@open="openTodo"')
    expect(detail).toContain('data-test="todo-open"')
    expect(detail).toContain('data-test="todo-detail-btn"')
    expect(detail).toContain('@click="openTodo(t)"')
  })

  it('renders the shared drawer with the plan data and the existing write paths', () => {
    expect(detail).toContain('<TodoDrawer')
    expect(detail).toContain('v-model="todoDrawerOpen"')
    expect(detail).toContain(':todos="plan?.todos ?? []"')
    expect(detail).toContain(':jobs="plan?.jobs ?? []"')
    // 计划级交接说明不再穿过抽屉（抽屉里那份已删，计划详情页原本就有）。
    expect(detail).not.toContain(':handoff="handoffLatest"')
    expect(detail).toContain(':write-block="planWriteBlock()"')
    // 写操作复用列表行那两条路，不另起 PATCH。
    expect(detail).toContain('@status="onTodoStatus"')
    expect(detail).toContain('void dispatchTodo(todo, agent)')
    expect(detail).toContain('@open-job="openJob"')
  })
})

describe('todo list row (toggling done by accident)', () => {
  // 行容器的整段模板：从 <div class="todo-row"> 到接续信息之前。
  const rowStart = detail.indexOf('<div class="todo-row">')
  const row = detail.slice(rowStart, detail.indexOf('<!-- gofer-rnp3：接续信息'))

  it('is not a label: clicking the blank part of the row must not toggle done', () => {
    expect(rowStart).toBeGreaterThanOrEqual(0)
    // label 会把行内任何点击转给复选框——这就是误勾选的根因。
    expect(detail).not.toContain('<label class="todo-row">')
    expect(row).not.toMatch(/<div class="todo-row"[^>]*@click/)
    expect(row).not.toMatch(/class="todo-item"[^>]*@change/)
  })

  it('toggles only from the checkbox itself, which keeps an accessible name', () => {
    // 整个行里只有复选框绑 onToggleTodo。
    expect(row.match(/onToggleTodo/g)?.length).toBe(1)
    expect(row).toContain('@change="onToggleTodo(t)"')
    expect(row).toContain(':aria-label="t.title"')
  })

  it('drops the anti-bubbling modifiers the label made necessary', () => {
    expect(row).not.toContain('@click.prevent')
    expect(row).not.toContain('.stop=')
  })

  it('renders the note as markdown so links work and lines stay apart', () => {
    expect(detail).toContain('<MarkdownBlock :text="t.note" breaks external-links />')
    // 备注不再以纯文本插值渲染（那样裸 URL 点不了）。
    expect(detail).not.toContain('{{ t.note }}')
    // 换行交给 breaks，容器不再用 white-space: pre-line（会把 HTML 源码换行当换行）。
    const noteCss = detail
      .slice(detail.indexOf('.todo-note {'), detail.indexOf('/* 接续信息'))
      .replace(/\/\*[\s\S]*?\*\//g, '')
    expect(noteCss).not.toContain('pre-line')
  })
})
