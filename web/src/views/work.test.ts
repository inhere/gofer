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
    expect(app).toContain("{ to: '/work', label: '工作' }")
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

  it('"请它汇报" is greyed out with the reason when no session is running', () => {
    expect(drawer).toContain('data-test="ask-report"')
    expect(drawer).toContain(':disabled="busy || !!reportBlock"')
    expect(drawer).toContain('data-test="report-block"')
    expect(drawer).toContain('requestWorkReport(d.id)')
  })

  it('stacks under the session drawer so opening a session from here stays on top', () => {
    expect(drawer).toContain('z-index: 78')
  })
})
