import { describe, expect, it } from 'vitest'
import type { TodayLane } from '../../api/todayLanes'
import {
  agentText,
  dotTone,
  elapsedText,
  healthText,
  LANE_FOLD_LIMIT,
  laneSummaryText,
  laneTarget,
  pipsView,
  splitLanes,
} from '../../utils/todayLanes'

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const lanesVue = read(import.meta.glob('./TodayLanes.vue', { eager: true, query: '?raw', import: 'default' }))
const rowVue = read(import.meta.glob('./TodayLaneRow.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = read(import.meta.glob('../WorkDrawer.vue', { eager: true, query: '?raw', import: 'default' }))

function lane(id: string, over: Partial<TodayLane> = {}): TodayLane {
  return {
    id,
    kind: 'work',
    title: id,
    status: 'active',
    agents: [],
    progress: {},
    health: 'ok',
    started_at: 0,
    elapsed_sec: 0,
    activity_at: 0,
    links: {},
    ...over,
  }
}

describe('TodayLanes display rules', () => {
  it('summarises the header, with the attention part only when something needs it', () => {
    expect(laneSummaryText({ total: 8, agents_running: 5, attention: 2 })).toEqual({ main: '8 条 · 5 个 agent 在跑', attention: '2 条要留意' })
    expect(laneSummaryText({ total: 3, agents_running: 0, attention: 0 }).attention).toBe('')
  })

  it('shows health only when it is not ok', () => {
    expect(healthText('ok')).toBe('')
    expect(healthText(undefined)).toBe('')
    expect(healthText('blocked')).toBe('阻塞')
    expect(healthText('stalled')).toBe('停滞')
    expect(healthText('at_risk')).toBe('有风险')
    expect(rowVue).toContain('data-test="lane-health"')
    expect(rowVue).toContain('{{ health }}')
  })

  it('keeps the server order and folds only the healthy remainder past 12 lanes', () => {
    const few = [lane('a'), lane('b')]
    expect(splitLanes(few)).toEqual({ shown: few, folded: [] })

    const many = Array.from({ length: 15 }, (_, i) => lane(`l${i}`, { health: i < 2 ? 'blocked' : 'ok' }))
    const s = splitLanes(many)
    expect(s.shown.map((l) => l.id)).toEqual(many.slice(0, LANE_FOLD_LIMIT).map((l) => l.id))
    expect(s.folded).toHaveLength(3)
    expect(s.folded.every((l) => l.health === 'ok')).toBe(true)

    // More lanes needing attention than the limit: all of them stay visible.
    const bad = Array.from({ length: 14 }, (_, i) => lane(`b${i}`, { health: 'stalled' })).concat([lane('ok1'), lane('ok2')])
    const s2 = splitLanes(bad)
    expect(s2.shown).toHaveLength(14)
    expect(s2.folded.map((l) => l.id)).toEqual(['ok1', 'ok2'])
    expect(lanesVue).toContain('还有 ${split.folded.length} 条正常运行中')
    expect(lanesVue).toContain('data-test="lanes-fold"')
  })

  it('renders agents as name·state, joining several with +', () => {
    expect(agentText([])).toEqual({ text: '—', state: '' })
    expect(agentText([{ agent: 'codex', state: 'running', kind: 'job', ref: 'j' }]).text).toBe('codex·running')
    expect(agentText([{ agent: 'claude', state: 'awaiting_input', kind: 'session', ref: 's' }]).text).toBe('claude·等输入')
    const two = agentText([
      { agent: 'codex', state: 'idle', kind: 'job', ref: 'j' },
      { agent: 'claude', state: 'running', kind: 'session', ref: 's' },
    ])
    expect(two).toEqual({ text: 'codex+claude·running', state: 'running' })
  })

  it('formats elapsed time and caps the pips', () => {
    expect(elapsedText(48 * 60)).toBe('48m')
    expect(elapsedText(3 * 3600 + 12 * 60)).toBe('3h12m')
    expect(elapsedText(6 * 3600)).toBe('6h')
    expect(elapsedText(2 * 86400 + 5)).toBe('2d')
    const pips = Array.from({ length: 15 }, (_, i) => ({ todo_id: `t${i}`, title: `t${i}`, status: 'pending' as const }))
    expect(pipsView(pips)).toMatchObject({ more: 3 })
    expect(pipsView(pips).shown).toHaveLength(12)
    expect(pipsView(undefined)).toEqual({ shown: [], more: 0 })
  })

  it('colours the dot (blocked first) and routes rows to the work drawer or the plan', () => {
    expect(dotTone({ health: 'blocked', status: 'active' })).toBe('blocked')
    expect(dotTone({ health: 'ok', status: 'needs_me' })).toBe('needs_me')
    expect(dotTone({ health: 'ok', status: 'open' })).toBe('active')
    expect(laneTarget({ kind: 'work', id: 'w-1' })).toEqual({ path: '/work', query: { id: 'w-1' } })
    expect(laneTarget({ kind: 'plan', id: 'plan-1' })).toEqual({ path: '/plans/plan-1' })
  })
})

describe('TodayLanes component', () => {
  it('fetches /v1/today/lanes and refreshes on work / plans / jobs / sessions with a 1s debounce', () => {
    expect(lanesVue).toContain('getTodayLanes')
    expect(lanesVue).toContain("['work', 'plans', 'jobs', 'sessions']")
    expect(lanesVue).toContain('createLiveTopic(topic')
    expect(lanesVue).toContain('const LIVE_DEBOUNCE_MS = 1000')
    expect(lanesVue).toContain('data-test="lanes-summary"')
  })

  it('rows are keyboard accessible and open on Enter', () => {
    expect(rowVue).toContain('role="button"')
    expect(rowVue).toContain('tabindex="0"')
    expect(rowVue).toContain('@keydown.enter.prevent="open"')
    expect(lanesVue).toContain('router.push(laneTarget(l))')
  })

  it('stacks into three lines on a phone without horizontal scroll', () => {
    expect(rowVue).toContain('@media (max-width: 760px)')
    expect(rowVue).toContain("grid-template-areas: 'd t n' '. p p' '. a h'")
    expect(rowVue).toContain('grid-template-columns: 10px minmax(0, 1fr) auto')
    // every text cell can shrink and ellipsize instead of widening the row
    for (const sel of ['.t b', '.ag', '.cur']) {
      expect(rowVue).toMatch(new RegExp(`${sel.replace('.', '\\.')} \\{[^}]*text-overflow: ellipsis`))
    }
  })
})

describe('WorkDrawer journal level and health', () => {
  it('defaults to milestones only with a 只看里程碑 / 全部 toggle', () => {
    expect(drawer).toContain('const journalAll = ref(false)')
    expect(drawer).toContain("e.level === 'milestone'")
    expect(drawer).toContain('data-test="journal-milestones"')
    expect(drawer).toContain('data-test="journal-all"')
    expect(drawer).toContain('只看里程碑')
    expect(drawer).toContain('>全部</button>')
  })

  it('shows health and its reason in the header only when not ok', () => {
    expect(drawer).toContain('v-if="detail && healthLabel"')
    expect(drawer).toContain('data-test="drawer-health"')
    expect(drawer).toContain('detail.health_reason')
  })
})
