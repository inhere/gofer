import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { TodayCard, TodayResponse } from '../api/today'
import { hiddenKeys, overlayOpen, todayData } from '../store/today'
import { navGroups } from '../utils/nav'
import Today from './Today.vue'
import DecisionOverlay from '../components/today/DecisionOverlay.vue'

const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
const todaySrc = raw(import.meta.glob('./Today.vue', { eager: true, query: '?raw', import: 'default' }))
const appSrc = raw(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))
const routerSrc = raw(import.meta.glob('../router.ts', { eager: true, query: '?raw', import: 'default' }))
const bellSrc = raw(import.meta.glob('../components/EscalationBell.vue', { eager: true, query: '?raw', import: 'default' }))
const schedulesSrc = raw(import.meta.glob('./Schedules.vue', { eager: true, query: '?raw', import: 'default' }))
const workbenchSrc = raw(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))
const cardSrc = raw(import.meta.glob('../components/today/DecisionCard.vue', { eager: true, query: '?raw', import: 'default' }))
const overlaySrc = raw(import.meta.glob('../components/today/DecisionOverlay.vue', { eager: true, query: '?raw', import: 'default' }))
const statusSrc = raw(import.meta.glob('../components/today/TodayStatusBar.vue', { eager: true, query: '?raw', import: 'default' }))
const storeSrc = raw(import.meta.glob('../store/today.ts', { eager: true, query: '?raw', import: 'default' }))

function card(i: number, over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: `work:w${i}`,
    kind: 'work',
    tag: '等我',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: `工作 ${i}`,
    waiting_since: 1_760_000_000 - i * 60,
    activity_at: 0,
    summary: `一句话 ${i}`,
    refs: { work_item_id: `w${i}` },
    actions: [{ id: 'reply', label: '回复', needs_text: true }],
    advice: null,
    ...over,
  }
}

function data(cards: TodayCard[]): TodayResponse {
  return {
    digest: { since_last: { since: 1, jobs_done: 12, jobs_failed: 1, commits: 9 }, title: '工作摘要 · 2026-10-09', text: '等我 2 · 等资源 1' },
    decisions: cards,
    snoozed: 0,
    status: {
      usage_today: { jobs: 41, total_tokens: 1_200_000, cost_usd: 4.86, session_tokens: 300_000 },
      steward_today: { enabled: true, notes: 3, summaries: 12 },
      runners: { online: 4, total: 5, offline: ['w-mac'], running_jobs: 2 },
      version: 'v0.127.0',
      alerts: ['runner w-mac 离线'],
    },
    generated_at: 1_760_000_000,
  }
}

async function render(component: unknown): Promise<string> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  const app = createSSRApp({ render: () => h(component as never) })
  app.use(router)
  await router.push('/today')
  return renderToString(app)
}

describe('Today page', () => {
  it('renders the digest line, the top 5 cards, 「还有 N 张」 and the status bar with alerts first', async () => {
    hiddenKeys.value = new Set(['work:w7'])
    todayData.value = data([1, 2, 3, 4, 5, 6, 7, 8].map((i) => card(i)))
    const html = await render(Today)
    expect(html).toMatch(/自上次打开：完成 <b[^>]*>12<\/b>/)
    expect(html).toMatch(/新提交 <b[^>]*>9<\/b>/)
    expect(html).toContain('待我决策')
    // 8 张，1 张在撤销窗口里已收起 → 7 张可见，展开 5 张
    expect(html).toContain('7 项')
    expect(html.match(/data-test="decision-card"/g)?.length).toBe(5)
    expect(html).toContain('还有 2 张')
    expect(html).not.toContain('工作 7')
    expect(html).toContain('data-test="open-handled"')
    // 状态条：runner 告警排第一
    const runner = html.indexOf('data-test="status-runner"')
    expect(runner).toBeGreaterThan(-1)
    expect(runner).toBeLessThan(html.indexOf('data-test="status-usage"'))
    expect(html).toContain('w-mac 离线')
    expect(html).toContain('sb-item--alert')
    expect(html).toContain('今日 job 41 · 1.5M tok · $4.86')
    expect(html).toContain('v0.127.0')
  })

  it('shows the one-line empty state with 专注处理 disabled (T3)', async () => {
    hiddenKeys.value = new Set()
    todayData.value = data([])
    const html = await render(Today)
    expect(html).toContain('没有等你的事 · 今天处理了')
    expect(html).toMatch(/<button[^>]*data-test="open-focus"[^>]*disabled/)
    expect(html).toContain('已稍后 0')
    expect(html).not.toContain('data-test="focus-mode"')
  })

  it('offers 专注处理 in the header and on the 「还有 N 张」 row, and 「已稍后 N」 in the footer (T3)', async () => {
    hiddenKeys.value = new Set()
    todayData.value = { ...data([1, 2, 3, 4, 5, 6].map((i) => card(i))), snoozed: 2 }
    const html = await render(Today)
    expect(html).not.toMatch(/<button[^>]*data-test="open-focus"[^>]*disabled/)
    expect(html).toContain('data-test="dq-focus"')
    expect(html).toContain('已稍后 2')
    expect(todaySrc).toContain('<FocusMode v-if="focusOpen"')
    expect(todaySrc).toContain('<SnoozedDrawer />')
    expect(todaySrc).toContain('@snooze="snoozeCard"')
  })

  it('mounts the parallel lanes between the queue and the status bar and subscribes the five live topics', () => {
    expect(todaySrc).toContain("import TodayLanes from '../components/today/TodayLanes.vue'")
    expect(todaySrc.indexOf('<TodayLanes />')).toBeGreaterThan(todaySrc.indexOf('<DecisionQueue'))
    expect(todaySrc.indexOf('<TodayLanes />')).toBeLessThan(todaySrc.indexOf('<TodayStatusBar'))
    expect(storeSrc).toContain("export const TODAY_TOPICS = ['pending', 'jobs', 'work', 'sessions', 'plans'] as const")
    expect(storeSrc).toContain('export const REFRESH_DEBOUNCE_MS = 1000')
    expect(storeSrc).toContain("window.addEventListener('pagehide', () => undoQueue.flush(true))")
  })
})

describe('global decision overlay', () => {
  it('renders the full queue when open', async () => {
    hiddenKeys.value = new Set()
    todayData.value = data([1, 2, 3, 4, 5, 6, 7].map((i) => card(i)))
    overlayOpen.value = false
    expect(await render(DecisionOverlay)).not.toContain('data-test="decision-overlay"')
    overlayOpen.value = true
    const html = await render(DecisionOverlay)
    expect(html).toContain('data-test="decision-overlay"')
    expect(html).toContain('任何页面按 g d')
    expect(html.match(/data-test="decision-card"/g)?.length).toBe(7)
    overlayOpen.value = false
  })

  it('is opened by the top-bar 「待我决策 N」 and the g d shortcut installed by App', () => {
    expect(bellSrc).toContain('<span class="bell-label">待我决策</span>')
    expect(bellSrc).toContain('overlayOpen.value = !overlayOpen.value')
    expect(appSrc).toContain('installTodayGlobals(router)')
    expect(appSrc).toContain('<DecisionOverlay />')
    expect(storeSrc).toContain('if (chord(ev.key, typing))')
  })
})

describe('navigation (N3 §6)', () => {
  it('groups 工作 / 配置 and relabels Board as Jobs', () => {
    expect(navGroups.map((g) => g.label)).toEqual(['工作', '配置'])
    expect(navGroups[0].items.map((i) => i.label)).toEqual(['今天', '工作台', 'Works', 'Plans', 'Jobs', 'Sessions', 'Issues', 'Dashboard'])
    expect(navGroups[0].items.find((i) => i.label === 'Jobs')?.to).toBe('/board')
    expect(navGroups[1].items.map((i) => i.label)).toEqual(['Agents', 'Runners', 'Projects', 'Workflows', 'Schedules'])
  })

  it('lands on /today, drops the /review badge and the top-bar 新建 cron, keeps /review routable', () => {
    expect(routerSrc).toContain("{ path: '/', redirect: '/today' }")
    expect(routerSrc).toContain("path: '/review'")
    expect(appSrc).toContain("const homeTo = '/today'")
    expect(appSrc).not.toContain('nav-review-badge')
    expect(appSrc).not.toContain('to="/schedules/new"')
    expect(schedulesSrc).toContain('<RouterLink to="/schedules/new" class="new-sched"')
  })

  it('workbench no longer carries its own 等你 list', () => {
    expect(workbenchSrc).not.toContain('WorkbenchAttention')
    expect(workbenchSrc).not.toContain('attentionItems')
  })
})

describe('Today mobile (~400px) layout', () => {
  it('wraps instead of scrolling horizontally', () => {
    expect(cardSrc).toMatch(/\.dc-acts \{[^}]*flex-wrap: wrap/)
    expect(cardSrc).toMatch(/\.dc-top \{[^}]*flex-wrap: wrap/)
    expect(cardSrc).toMatch(/\.dc-body \{[^}]*min-width: 0/)
    expect(cardSrc).toMatch(/\.dc-title \{[^}]*overflow-wrap: anywhere/)
    expect(cardSrc).toMatch(/\.dc-reply input \{[^}]*min-width: 0/)
    expect(overlaySrc).toContain('width: min(480px, 100vw)')
    expect(statusSrc).toMatch(/\.sb \{[^}]*flex-wrap: wrap/)
    expect(todaySrc).toMatch(/\.sh \{[^}]*flex-wrap: wrap/)
  })
})
