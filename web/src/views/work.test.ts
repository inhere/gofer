import { describe, expect, it } from 'vitest'

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const work = read(import.meta.glob('./Work.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = read(import.meta.glob('../components/WorkDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const router = read(import.meta.glob('../router.ts', { eager: true, query: '?raw', import: 'default' }))
const app = read(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))

describe('Work page', () => {
  it('is routed and in the navigation', () => {
    expect(router).toContain("path: '/work'")
    expect(router).toContain("import('./views/Work.vue')")
    expect(app).toContain("{ to: '/work', label: 'Works' }")
  })

  it('has no Home menu item: the logo goes home and carries the connection dot', () => {
    expect(app).not.toContain("label: 'Home'")
    expect(app).toContain('<RouterLink :to="homeTo" class="brand-name"')
    expect(app).not.toContain('agent bridge')
    // the single conn dot lives inside the brand block
    expect(app.match(/data-testid="conn-state"/g)?.length).toBe(1)
    expect(app.indexOf('data-testid="conn-state"')).toBeLessThan(app.indexOf('<nav class="nav mono"'))
  })

  it('shows the needs-me badge on the Works item, fed by the work topic', () => {
    expect(app).toContain('data-testid="work-needs-me-badge"')
    const store = read(import.meta.glob('../store/workNeedsMe.ts', { eager: true, query: '?raw', import: 'default' }))
    expect(store).toContain("createLiveTopic('work'")
  })

  it('offers 转为 todo in the drawer, once, with a jump to the plan', () => {
    for (const hook of ['data-test="to-todo"', 'data-test="todo-dlg"', 'data-test="todo-plan"', 'data-test="to-todo-done"', 'data-test="to-todo-open"']) {
      expect(drawer).toContain(hook)
    }
    expect(drawer).toContain('workToTodo(')
    expect(drawer).toContain('e.status === 409')
  })

  it('shows the three count chips that filter, a status/workspace switch and the unsorted area', () => {
    for (const hook of ['data-test="chip-needs-me"', 'data-test="chip-due"', 'data-test="chip-unsorted"', 'data-test="view-status"', 'data-test="view-workspace"', 'data-test="unsorted-area"']) {
      expect(work).toContain(hook)
    }
    expect(work).toContain('等我 <strong>')
    expect(work).toContain('到期提醒 <strong>')
    expect(work).toContain("localStorage.setItem(VIEW_KEY, v)")
    // the unsorted area is only a separate section when nothing is being filtered
    expect(work).toContain('const showUnsortedArea = computed(() => !filtering.value && !q.value.trim() && unsorted.value.length > 0)')
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
    expect((work.match(/@summarize="summarize\(it\)"/g) ?? []).length).toBe(4)
    expect((work.match(/@accept-suggestion=/g) ?? []).length).toBe(4)
  })

  it('hides empty columns on a phone so the first screen has content', () => {
    expect(work).toContain('.work-col--empty { display: none; }')
    expect(work).toContain('@media (max-width: 640px)')
  })
})

describe('Work drawer', () => {
  it('writes with the optimistic lock and recovers from a 409', () => {
    expect(drawer).toContain('patchWorkItem(d.id, { ...p, rev: d.rev })')
    expect(drawer).toContain('e.status === 409')
    expect(drawer).toContain('刚被别人（或会话）改过')
  })

  it('has the organising operations: status, park, remind, complete/drop, merge, split, link, note', () => {
    for (const hook of ['data-test="mark-done"', 'data-test="mark-dropped"', 'data-test="park-btn"', 'data-test="remind-btn"', 'data-test="merge-btn"', 'data-test="split-btn"', 'data-test="add-link"', 'data-test="add-note"', 'data-test="hand-back"']) {
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

  it('stacks under the session drawer so opening a session from here stays on top', () => {
    expect(drawer).toContain('z-index: 78')
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
