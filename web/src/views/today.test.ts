import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { TodayCard, TodayResponse } from '../api/today'
import { hiddenKeys, overlayOpen, todayData } from '../store/today'
import { SHELL_NAV_GROUPS, TAB_BAR_ITEMS, moreItems } from '../utils/shell'
import Today from './Today.vue'
import DecisionOverlay from '../components/today/DecisionOverlay.vue'

const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
const todaySrc = raw(import.meta.glob('./Today.vue', { eager: true, query: '?raw', import: 'default' }))
const appSrc = raw(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))
const routerSrc = raw(import.meta.glob('../router.routes.ts', { eager: true, query: '?raw', import: 'default' }))
const bellSrc = raw(import.meta.glob('../ui/NeedsMeButton.vue', { eager: true, query: '?raw', import: 'default' }))
const shellSrc = raw(import.meta.glob('../ui/AppShell.vue', { eager: true, query: '?raw', import: 'default' }))
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
    waiting: cards,
    running: { sessions: 0, jobs: 0 },
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

describe('Today page (design 2026-11-01 §4.3)', () => {
  it('renders the hero count and the top 5 waiting cards, with no digest, summary line, filters or lanes', async () => {
    hiddenKeys.value = new Set(['work:w7'])
    todayData.value = data([1, 2, 3, 4, 5, 6, 7, 8].map((i) => card(i)))
    const html = await render(Today)
    // 首屏：「N 件事在等你」（8 张，1 张在撤销窗口里已收起 → 7 张可见）
    expect(html).toContain('data-test="today-title"')
    expect(html).toContain('7 件事在等你')
    // 队列只展开前 5 张，其余折成「还有 N 张」
    expect(html.match(/data-test="decision-card"/g)?.length).toBe(5)
    expect(html).toContain('还有 2 张')
    expect(html).not.toContain('工作 7')
    // 卡片底部入口全部保留
    expect(html).toContain('data-test="open-focus"')
    expect(html).toContain('data-test="open-snoozed"')
    expect(html).toContain('data-test="open-handled"')
    expect(html).toContain('含 exec 待验收')
    // 收缩掉的东西不再出现：digest、一行摘要、类型 / 项目筛选、并行中泳道、刚处理过
    for (const gone of ['today-digest', 'today-summary', 'filter-chips', 'project-filter', 'today-lanes', 'today-recent']) {
      expect(html).not.toContain(`data-test="${gone}"`)
    }
    expect(html).not.toContain('自上次打开')
  })

  it('shows only the empty line when the 「等我」 list is empty', async () => {
    hiddenKeys.value = new Set()
    todayData.value = data([])
    const html = await render(Today)
    expect(html).toContain('今天没有等你的事')
    expect(html).not.toContain('data-test="decision-card"')
  })

  it('shows what is running as one line linking to the workbench, only when something runs', async () => {
    hiddenKeys.value = new Set()
    todayData.value = { ...data([card(1)]), running: { sessions: 2, jobs: 3 } }
    const html = await render(Today)
    expect(html).toContain('data-test="today-running"')
    expect(html).toContain('2 个会话 · 3 个 job 在跑')
    expect(html).toMatch(/<a[^>]*href="\/workbench"[^>]*>2 个会话/)

    todayData.value = data([card(1)])
    expect(await render(Today)).not.toContain('data-test="today-running"')
  })

  it('mounts the plans-in-progress block and no lanes / recent-handled column', () => {
    expect(todaySrc).toContain("import TodayPlans from '../components/today/TodayPlans.vue'")
    expect(todaySrc).toContain('<TodayPlans />')
    for (const gone of ['TodayLanes', 'TodayRecentHandled', 'FilterChips', 'ProjectFilter', 'today-digest']) {
      expect(todaySrc).not.toContain(gone)
    }
  })

  it('puts the status bar at the bottom only when there is an alert, alerting node first, same links as before', async () => {
    hiddenKeys.value = new Set()
    todayData.value = data([1, 2].map((i) => card(i)))
    const html = await render(Today)
    const bar = html.slice(html.indexOf('data-test="today-status"'))
    const node = bar.indexOf('data-test="status-nodes"')
    expect(node).toBeGreaterThan(-1)
    expect(node).toBeLessThan(bar.indexOf('data-test="status-usage"'))
    expect(bar).toContain('w-mac 离线')
    expect(bar).toContain('item--alert')
    expect(bar).toContain('在跑 2')
    expect(bar).toContain('今天失败 1')
    expect(bar).toContain('今日 job 41 · 1.5M tok · $4.86')
    expect(bar).toContain('v0.127.0')
    // 链接目标与迁移前一致
    for (const to of ['/runners', '/dashboard', '/settings/work', '/settings/about']) {
      expect(bar).toContain(`href="${to}"`)
    }

    // 健康时状态条不占地方
    const healthy = data([1, 2].map((i) => card(i)))
    todayData.value = { ...healthy, status: { ...healthy.status, alerts: [] } }
    expect(await render(Today)).not.toContain('data-test="today-status"')
  })

  it('shows one empty sentence when nothing waits: no queue, no focus button, no readable extras', async () => {
    hiddenKeys.value = new Set()
    todayData.value = { ...data([]), status: { ...data([]).status, alerts: [] } }
    const html = await render(Today)
    expect(html).toContain('今天没有等你的事')
    // 空态是一句话：没有队列、没有「没有等你的事 · 今天处理了 N 张」第二句、没有专注处理、没有状态条
    expect(html).not.toContain('data-test="decision-queue"')
    expect(html).not.toContain('今天处理了')
    expect(html).not.toContain('data-test="open-focus"')
    expect(html).not.toContain('data-test="today-status"')
    expect(html).not.toContain('data-test="today-running"')
    expect(html).not.toContain('data-test="focus-mode"')
    // 页面主体的可读文字只有标题那一句（脚部的入口是控件，不是正文）
    const start = html.indexOf('>', html.indexOf('class="today-main"')) + 1
    const main = html.slice(start, html.lastIndexOf('<', html.indexOf('class="today-foot"')))
    const text = main.replace(/<[^>]*>/g, '').replace(/<!--[\s\S]*?-->/g, '').trim()
    expect(text).toBe('今天没有等你的事')
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

  it('subscribes the five live topics and keeps the undo window global', () => {
    expect(storeSrc).toContain("export const TODAY_TOPICS = ['pending', 'jobs', 'work', 'sessions', 'plans'] as const")
    expect(storeSrc).toContain('export const REFRESH_DEBOUNCE_MS = 1000')
    expect(storeSrc).toContain("window.addEventListener('pagehide', () => undoQueue.flush(true))")
  })

  it('keeps the page copy in the page dictionary', () => {
    // 本页文案只写在 i18n/pages/today.ts，页面与组件都走 t()
    expect(todaySrc).toContain("from '../i18n'")
    expect(todaySrc).toContain("t('today.title.n', { n: shownCards.length })")
    expect(statusSrc).toContain("t('today.status.nodes'")
    expect(cardSrc).toContain('today.kind.${props.card.kind}')
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
    expect(bellSrc).toContain('{{ label }}')
    expect(bellSrc).toContain('overlayOpen.value = !overlayOpen.value')
    expect(appSrc).toContain('installTodayGlobals(router)')
    expect(appSrc).toContain('<DecisionOverlay />')
    expect(storeSrc).toContain('if (chord(ev.key, typing))')
  })
})

describe('navigation (design §6)', () => {
  it('groups the sidebar into 核心 / 协作 / 自动化 / 基础设施 and renames Board as /jobs', () => {
    expect(SHELL_NAV_GROUPS.map((g) => g.key)).toEqual(['nav.group.core', 'nav.group.collab', 'nav.group.auto', 'nav.group.infra'])
    expect(SHELL_NAV_GROUPS[0].items.map((i) => i.key)).toEqual(['nav.today', 'nav.workbench', 'nav.work', 'nav.plans', 'nav.jobs', 'nav.sessions'])
    expect(SHELL_NAV_GROUPS[0].items.find((i) => i.key === 'nav.jobs')?.to).toBe('/jobs')
  })

  it('keeps 工作项 as the first item of the phone 更多 sheet, 计划 in the tab bar', () => {
    expect(TAB_BAR_ITEMS.map((i) => i.to)).toEqual(['/today', '/workbench', '/jobs', '/plans'])
    expect(moreItems()[0].to).toBe('/work')
  })

  it('lands on /today, drops the /review badge and the top-bar 新建 cron, keeps /review routable', () => {
    expect(routerSrc).toContain("{ path: '/', redirect: '/today' }")
    expect(routerSrc).toContain("path: '/review'")
    expect(shellSrc).toContain('to="/today"')
    expect(appSrc).not.toContain('nav-review-badge')
    expect(shellSrc).not.toContain('to="/schedules/new"')
    expect(schedulesSrc).toMatch(/<RouterLink[^>]*to="\/schedules\/new" class="btn btn--primary"/)
  })

  it('workbench no longer carries its own 等你 list', () => {
    expect(workbenchSrc).not.toContain('WorkbenchAttention')
    expect(workbenchSrc).not.toContain('attentionItems')
  })
})

describe('Today narrow layout (≤760)', () => {
  it('is one column, wraps the card actions and keeps the status bar and overlay inside the viewport', () => {
    // 单列：没有右栏，主栏限宽但不撑破视口
    expect(todaySrc).toMatch(/\.today-main \{\s*min-width: 0;\s*max-width: 880px/)
    expect(todaySrc).not.toContain('grid-template-columns')
    // 卡片主操作换行、点按目标够大
    expect(cardSrc).toMatch(/\.dc-acts \{[^}]*flex-wrap: wrap/)
    expect(cardSrc).toMatch(/\.dc-title \{[^}]*overflow-wrap: anywhere/)
    expect(cardSrc).toMatch(/min-height: 40px/)
    // 状态条在手机上也换行
    expect(statusSrc).toMatch(/\.statbar \{[^}]*flex-wrap: wrap/)
    // 浮层仍是右侧抽屉，宽度不撑破视口
    expect(overlaySrc).toContain('width: min(max(480px, 40vw), 760px, 100vw)')
  })
})
