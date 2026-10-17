import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { TodayAction, TodayCard } from '../../api/today'
import DecisionCard from './DecisionCard.vue'
import DecisionQueue from './DecisionQueue.vue'
import { actionLink, cardLink, tierOf } from '../../utils/today'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const todayUtilSource = raw(import.meta.glob('../../utils/today.ts', { eager: true, query: 'raw', import: 'default' }))
const todayApiSource = raw(import.meta.glob('../../api/today.ts', { eager: true, query: 'raw', import: 'default' }))

const NOW = 1_760_000_000

// 服务端 internal/today/enroll.go 造出来的唯一一张汇总卡（持有 worker.manage 才追加）。
function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'worker_enroll:pending',
    kind: 'worker_enroll',
    tag: 'Worker',
    urgency: 'now',
    blocks: { score: 0, items: 2 },
    title: '2 个 worker 申请注册',
    waiting_since: NOW - 30,
    expires_at: NOW + 80,
    activity_at: NOW - 30,
    summary: 'box-1 → gpu1、box-2 等 2 个',
    refs: {},
    actions: [{ id: 'open', label: '去处理', style: 'link', value: '/runners#enrollments' }],
    advice: null,
    ...over,
  }
}

async function render(component: unknown, props: Record<string, unknown>): Promise<string> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  const app = createSSRApp({ render: () => h(component as never, props) })
  app.use(router)
  await router.push('/today')
  return renderToString(app)
}

describe('Today worker-enroll card', () => {
  it('is a known card kind of the today API', () => {
    expect(todayApiSource).toContain("| 'worker_enroll'")
  })

  it('groups as 会超时 (urgency now) and both the title and the action open the Runners section', () => {
    const c = card()
    expect(tierOf(c)).toBe('now')
    expect(cardLink(c)).toBe('/runners#enrollments')
    expect(actionLink(c, c.actions[0] as TodayAction)).toBe('/runners#enrollments')
    expect(todayUtilSource).toContain('case \'worker_enroll\':')
  })

  it('renders as a normal card whose 「去处理」 links to /runners#enrollments', async () => {
    const html = await render(DecisionCard, { card: card(), nowSec: NOW })
    for (const s of ['Worker', '2 个 worker 申请注册', 'box-1 → gpu1、box-2 等 2 个', '去处理', '卡住 2', '1 分钟后超时']) {
      expect(html).toContain(s)
    }
    // 标题与「去处理」都指到待接受区
    expect(html.match(/href="\/runners#enrollments"/g)?.length).toBe(2)
  })

  it('shows up in the queue under 会超时 with the other urgent cards', async () => {
    const html = await render(DecisionQueue, { cards: [card()], nowSec: NOW, limit: 5 })
    expect(html).toContain('会超时')
    expect(html).toContain('data-test="decision-card"')
    expect(html).toContain('href="/runners#enrollments"')
  })

  it('carries no write action: the card only navigates (no API call is wired for it)', () => {
    expect(todayUtilSource).toContain("return ENROLLMENTS_LINK")
    // 卡片 kind 没有出现在 runCardAction 的任何写分支里
    const writeSection = todayUtilSource.slice(todayUtilSource.indexOf('export function runCardAction'))
    expect(writeSection).not.toContain('worker_enroll')
  })
})
