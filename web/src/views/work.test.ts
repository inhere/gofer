import { describe, expect, it } from 'vitest'

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const work = read(import.meta.glob('./Work.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = read(import.meta.glob('../components/WorkDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const router = read(import.meta.glob('../router.routes.ts', { eager: true, query: '?raw', import: 'default' }))
const app = read(import.meta.glob('../ui/AppShell.vue', { eager: true, query: '?raw', import: 'default' }))
const nav = read(import.meta.glob('../utils/shell.ts', { eager: true, query: '?raw', import: 'default' }))

describe('Work page', () => {
  it('is routed and in the navigation', () => {
    expect(router).toContain("path: '/work'")
    expect(router).toContain("import('./views/Work.vue')")
    // 设计稿 §6.1：导航分组搬到 utils/shell.ts（侧栏 / 更多 / 标签栏共用），文案走 i18n 的 nav.*。
    expect(nav).toContain("{ to: '/work', key: 'nav.work', icon: 'work' }")
  })

  it('has no Home menu item: the logo goes home and carries the connection dot', () => {
    expect(app).not.toContain("label: 'Home'")
    expect(app).toContain('<RouterLink to="/today" class="brand"')
    expect(app).not.toContain('agent bridge')
    // the single conn dot lives in the sidebar head (phones get one in the top bar)
    expect(app.match(/data-testid="conn-state"/g)?.length).toBe(1)
  })

  it('shows the needs-me badge on the Works item, fed by the work topic', () => {
    expect(app).toContain('data-testid="work-needs-me-badge"')
  })

  it('offers 转为 todo in the drawer, once, with a jump to the plan', () => {
    for (const hook of ['data-test="to-todo"', 'data-test="todo-dlg"', 'data-test="todo-plan"', 'data-test="to-todo-done"', 'data-test="to-todo-open"']) {
      expect(drawer).toContain(hook)
    }
    expect(drawer).toContain('workToTodo(')
    expect(drawer).toContain('e.status === 409')
  })

  it('shows one status chip row with counts, the marker chip next to it, and a status/workspace switch', () => {
    for (const hook of ['FilterChips', 'ProjectFilter', 'laneCounts', 'data-test="view-status"', 'data-test="view-workspace"', 'data-test="status-view"', 'data-test="work-summary"']) {
      expect(work).toContain(hook)
    }
    // chips carry the counts; the filter value keeps utils/work's WorkFilter vocabulary
    for (const key of ['work.f.all', 'work.f.due']) expect(work).toContain(key)
    // 状态 chip 的文案来自泳道映射（真实状态 status.<值>，草稿 / 收集箱走本页字典）
    expect(work).toContain('laneLabelKey(l)')
    expect(work).toContain('workCounts(inProject.value, unsortedItems(inProject.value))')
    expect(work).toContain("localStorage.setItem(VIEW_KEY, v)")
    // 不再有「待整理」分段区：草稿与收集箱都是平铺列表里的泳道
    expect(work).not.toContain('showTriageArea')
    expect(work).toContain('laneHint')
  })

  it('narrows by project through the URL (one entry point for the project chip)', () => {
    expect(work).toContain("useProjectFilter('/work')")
    expect(work).toContain('narrowByProject(items.value, project.value)')
    expect(work).toContain('narrowByProject(captured.value, project.value)')
  })

  it('renders the same shared card shell as the Sessions page (no private copy)', () => {
    expect(work).toContain("import WorkCard from '../components/WorkCard.vue'")
    expect(work).toContain('class="icard-grid"')
    const card = read(import.meta.glob('../components/WorkCard.vue', { eager: true, query: '?raw', import: 'default' }))
    expect(card).toContain("import InfoCard from './InfoCard.vue'")
  })

  it('is live: pushes on the work topic refresh it, with the sessions topic for the card header', () => {
    expect(work).toContain("createLiveTopic('work'")
    expect(work).toContain("createLiveTopic('sessions'")
  })

  it('opens the session drawer for 打开会话 and offers wake with the same confirm text', () => {
    expect(work).toContain('<SessionDrawer')
    expect(work).toContain('resumeConfirmText(s)')
    expect(work).toContain('resumeSession(sid)')
  })

  it('wires the card events for the manual tidy-up and the suggestion buttons', () => {
    expect(work).toContain('summarizeWorkItem(it.id)')
    expect(work).toContain('acceptWorkSuggestion(it.id, field)')
    expect(work).toContain('dismissWorkSuggestion(it.id, field)')
    // 两个视图（按状态平铺 / 按工作区）各一份卡片
    expect((work.match(/@summarize="summarize\(it\)"/g) ?? []).length).toBe(2)
    expect((work.match(/@accept-suggestion=/g) ?? []).length).toBe(2)
  })

  it('is one column on a phone, with the chip rows scrolling inside their box', () => {
    // §4.3 froze the phone breakpoint at 760 for this redesign
    expect(work).toContain('@media (max-width: 760px)')
    expect(work).toContain('.work-flat { grid-template-columns: minmax(0, 1fr);')
    expect(work).not.toContain('.work-col--empty')
  })

  it('keeps the status text on the card, not just the left-edge colour', () => {
    const card = read(import.meta.glob('../components/WorkCard.vue', { eager: true, query: '?raw', import: 'default' }))
    expect(card).toContain('<StatusPill :status="item.status" />')
    expect(work).not.toContain('sbadge--${col.tone}')
    expect(work).not.toContain('col-cards')
  })
})

describe('Work drawer', () => {
  it('writes with the optimistic lock and recovers from a 409', () => {
    expect(drawer).toContain('patchWorkItem(d.id, { ...p, rev: d.rev })')
    expect(drawer).toContain('e.status === 409')
    expect(drawer).toContain("t('work.d.conflict')")
  })

  it('has the organising operations: status, park, remind, complete/drop, merge, split, link, note', () => {
    for (const hook of ['data-test="mark-done"', 'data-test="mark-dropped"', 'data-test="park-btn"', 'data-test="remind-btn"', 'data-test="merge-btn"', 'data-test="split-btn"', 'data-test="add-link"', 'data-test="note-composer"', 'data-test="hand-back"']) {
      expect(drawer).toContain(hook)
    }
    expect(drawer).toContain('mergeWorkItems(d.id')
    expect(drawer).toContain('splitWorkItem(d.id')
    expect(drawer).toContain("status: 'parked', park_until: until, park_note")
  })

  it('"请它汇报" / "请它写交接" go through the ledger and are greyed out only without a session', () => {
    expect(drawer).toContain('data-test="ask-report"')
    expect(drawer).toContain('data-test="ask-handoff"')
    expect(drawer).toContain(':disabled="busy || !!reportBlock"')
    expect(drawer).toContain('data-test="report-block"')
    expect(drawer).toContain("requestWorkReport(d.id, '', kind)")
    expect(drawer).toContain('data-test="drawer-requests"')
  })

  it('tidies up on demand and lets the person adopt or dismiss suggestions', () => {
    expect(drawer).toContain('data-test="drawer-summarize"')
    expect(drawer).toContain('summarizeWorkItem(d.id)')
    expect(drawer).toContain('data-test="drawer-suggestions"')
    expect(drawer).toContain('acceptWorkSuggestion(d.id, field)')
    expect(drawer).toContain('dismissWorkSuggestion(d.id, field)')
    // who wrote each field / each journal line
    expect(drawer).toContain('data-test="drawer-src-goal"')
    expect(drawer).toContain('data-test="tl-by"')
  })

  it('stacks the P2 drawer shell under the session drawer and puts the note box on Composer', () => {
    expect(drawer).toContain("import Drawer from '../ui/Drawer.vue'")
    expect(drawer).toContain('<Drawer v-model="open" :label="drawerLabel" flush')
    // P4 已合入的统一输入框：写备注不再自带一个输入框 + 按钮
    expect(drawer).toContain("import Composer from '../ui/Composer.vue'")
    expect(drawer).toContain('@send="addNote"')
    expect(drawer).not.toContain('data-test="note-input"')
  })
})

describe('session cards <-> work items', () => {
  it('Sessions shows the linked work item on ACP and pty cards and can link one', () => {
    const sessions = read(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))
    for (const hook of ['data-test="acp-work-link"', 'data-test="pty-work-link"', 'data-test="acp-link-btn"', 'data-test="pty-link-btn"', 'data-test="work-link"']) {
      expect(sessions).toContain(hook)
    }
    expect(sessions).toContain('attachWorkSession(wid, jobId)')
  })

  it('job-kind sessions open the job page, not the relay drawer', () => {
    expect(work).toContain("s.kind === 'job'")
    expect(work).toContain('router.push(`/jobs/')
  })
})
