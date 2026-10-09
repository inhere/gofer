import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { TodayCard } from '../../api/today'
import DecisionCard from './DecisionCard.vue'
import DecisionQueue from './DecisionQueue.vue'

const source = Object.values(import.meta.glob('./DecisionCard.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

const NOW = 1_760_000_000

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'interaction:j1/i1',
    kind: 'interaction',
    tag: '工具审批',
    urgency: 'now',
    blocks: { score: 5, items: 2, text: '占着 codex 会话 6 分钟；plan 导出改造 后面 1 项在等' },
    title: 'codex 请求执行命令',
    project_key: 'orders-api',
    waiting_since: NOW - 360,
    expires_at: NOW + 720,
    activity_at: NOW - 360,
    summary: '要在仓库根执行 go test ./... -race',
    refs: { job_id: 'j1', interaction_id: 'i1', thread_id: 's:abc' },
    actions: [
      { id: 'answer', label: '批准', value: 'allow', style: 'ok' },
      { id: 'answer', label: '拒绝', value: 'reject', style: 'bad' },
      { id: 'punt', label: '交给管家' },
    ],
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

describe('DecisionCard', () => {
  it('renders the fixed 4-line skeleton: source line, title, one-line summary, actions', async () => {
    const html = await render(DecisionCard, { card: card(), nowSec: NOW })
    const lines = ['dc-line-source', 'dc-line-title', 'dc-line-summary', 'dc-line-actions']
    let last = -1
    for (const l of lines) {
      const at = html.indexOf(`data-test="${l}"`)
      expect(at).toBeGreaterThan(last)
      last = at
    }
    for (const s of ['工具审批', 'orders-api', '等了 6 分钟', '12 分钟后超时', '卡住 2', 'codex 请求执行命令', 'go test ./... -race', '批准', '拒绝', '交给管家']) {
      expect(html).toContain(s)
    }
    // 标题跳工作台对应会话
    expect(html).toContain('href="/workbench?thread=s%3Aabc"')
    // 详情默认收起，回复框不常驻，没有「稍后」（T3）
    expect(html).toContain('data-test="dc-info-toggle"')
    expect(html).not.toContain('data-test="dc-info"')
    expect(html).not.toContain('data-test="dc-reply"')
    expect(html).not.toContain('稍后')
  })

  it('turns the steward advice action into the leading 「按建议：X」 primary button', async () => {
    const withAdvice = await render(DecisionCard, {
      card: card({ advice: { text: '只读源码、只写 tmp/', action_id: 'answer:allow' } }),
      nowSec: NOW,
    })
    expect(withAdvice).toContain('data-test="dc-advice"')
    expect(withAdvice).toContain('按建议：批准')
    // 主按钮在最前，原「批准」不再重复出现
    expect(withAdvice.indexOf('按建议：批准')).toBeLessThan(withAdvice.indexOf('拒绝'))
    expect(withAdvice.match(/>批准</g)).toBeNull()

    const textOnly = await render(DecisionCard, { card: card({ advice: { text: '由你决定' } }), nowSec: NOW })
    expect(textOnly).not.toContain('data-test="dc-advice"')
  })

  it('详情 shows blocking detail, steward reasoning and review facts', async () => {
    const html = await render(DecisionCard, {
      card: card({
        kind: 'review',
        key: 'review:j2',
        tag: '待验收',
        urgency: 'normal',
        expires_at: 0,
        advice: { text: 'diff 与汇报一致' },
        review: { commits: 3, adds: 214, dels: 37, verify: 'passed' },
        refs: { job_id: 'j2' },
        actions: [
          { id: 'accept', label: '通过', style: 'ok' },
          { id: 'rerun', label: '附意见重跑' },
          { id: 'diff', label: '看 diff', style: 'link' },
        ],
      }),
      nowSec: NOW,
      initialInfoOpen: true,
    })
    expect(html).toContain('data-test="dc-info"')
    expect(html).toContain('占着 codex 会话 6 分钟')
    expect(html).toContain('diff 与汇报一致')
    expect(html).toContain('3 个提交')
    expect(html).toContain('+214')
    expect(html).toContain('−37')
    expect(html).toContain('verify passed')
    expect(html).toContain('看全部待验收')
    expect(html).toContain('href="/jobs/j2"')
    expect(html).toContain('收起')
  })

  it('toggles 详情 and only opens the reply box when 「回复」 is clicked', () => {
    expect(source).toContain('@click="infoOpen = !infoOpen"')
    expect(source).toContain("{{ infoOpen ? '收起' : '详情' }}")
    expect(source).toMatch(/if \(a\.needs_text\) \{\s*replyAction\.value = a/)
    expect(source).toContain('<div v-if="replyAction" class="dc-reply"')
    // 「附意见重跑」复用验收台的 RejectDialog
    expect(source).toContain('<RejectDialog v-if="rerunOpen"')
  })
})

describe('DecisionQueue', () => {
  it('shows the top N grouped by tier and folds the rest into 「还有 N 张」', async () => {
    const cards = [
      card({ key: 'a', urgency: 'now' }),
      card({ key: 'b', urgency: 'blocking', expires_at: 0 }),
      card({ key: 'c', urgency: 'normal', expires_at: 0, blocks: { score: 0, items: 0 } }),
      card({ key: 'd', urgency: 'normal', expires_at: 0, blocks: { score: 0, items: 0 } }),
    ]
    const html = await render(DecisionQueue, { cards, nowSec: NOW, limit: 2 })
    expect(html).toContain('会超时')
    expect(html).toContain('卡住别人')
    expect(html).not.toContain('可稍后')
    expect(html.match(/data-test="decision-card"/g)?.length).toBe(2)
    expect(html).toContain('还有 2 张')

    const all = await render(DecisionQueue, { cards, nowSec: NOW })
    expect(all.match(/data-test="decision-card"/g)?.length).toBe(4)
    expect(all).not.toContain('data-test="dq-more"')

    const empty = await render(DecisionQueue, { cards: [], nowSec: NOW, emptyText: '没有等你的事 · 今天处理了 3 张' })
    expect(empty).toContain('今天处理了 3 张')
  })
})
