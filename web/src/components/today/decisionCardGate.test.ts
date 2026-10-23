// 今天页写按钮的真实渲染（gofer-darr）：只读身份下按钮 disabled 且 title 说明原因；
// 有对应权限时照常可点。whoami 用 mock 顶替（渲染环境没有 token / sessionStorage）。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { TodayCard } from '../../api/today'
import type { AuthWhoami } from '../../utils/permissions'
import DecisionCard from './DecisionCard.vue'
import DecisionQueue from './DecisionQueue.vue'
import { ceilingReadOnlyNotice } from '../../utils/ceiling'

const state = vi.hoisted(() => ({ who: null as AuthWhoami | null }))

vi.mock('../../store/whoami', async () => {
  const { computed, ref } = await import('vue')
  return {
    useWhoami: () => ({
      whoami: ref(state.who),
      readOnly: computed(() => state.who?.ceiling?.read_only === true),
    }),
  }
})

const NOW = 1_760_000_000

function who(over: Partial<AuthWhoami>): AuthWhoami {
  return { caller_id: 'alice', kind: 'user', root: false, permissions: [], grants_version: 1, ...over }
}
const holds = (...names: string[]) => names.map((name) => ({ name, source: 'db' }))
const READ_ONLY = who({ ceiling: { allow_exec: false, global_memory: false, read_only: true } })

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'interaction:j1/i1',
    kind: 'interaction',
    tag: '工具审批',
    urgency: 'now',
    blocks: { score: 5, items: 2 },
    title: 'codex 请求执行命令',
    project_key: 'orders-api',
    waiting_since: NOW - 360,
    expires_at: NOW + 720,
    activity_at: NOW - 360,
    summary: '要在仓库根执行 go test ./... -race',
    refs: { job_id: 'j1', interaction_id: 'i1' },
    actions: [
      { id: 'answer', label: '批准', value: 'allow', style: 'ok' },
      { id: 'answer', label: '拒绝', value: 'reject', style: 'bad' },
    ],
    advice: null,
    ...over,
  }
}

// 取出文字为 label 的那个 <button ...> 起始标签。
function buttonTag(html: string, label: string): string {
  const at = html.indexOf(`>${label}</button>`)
  if (at < 0) throw new Error(`button ${label} not rendered: ${html.slice(0, 400)}`)
  return html.slice(html.lastIndexOf('<button', at), at)
}

async function render(component: unknown, props: Record<string, unknown>): Promise<string> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  const app = createSSRApp({ render: () => h(component as never, props) })
  app.use(router)
  await router.push('/today')
  return renderToString(app)
}

describe('read-only identity: the today card actions are disabled', () => {
  it('interaction answers carry disabled plus the read-only reason', async () => {
    state.who = READ_ONLY
    const html = await render(DecisionCard, { card: card(), nowSec: NOW })
    for (const label of ['批准', '拒绝']) {
      const tag = buttonTag(html, label)
      expect(tag).toContain('disabled')
      expect(tag).toContain(ceilingReadOnlyNotice())
    }
  })

  it('review accept and approval are disabled too', async () => {
    state.who = READ_ONLY
    const review = await render(DecisionCard, {
      card: card({
        key: 'review:j2',
        kind: 'review',
        actions: [
          { id: 'accept', label: '通过', style: 'ok' },
          { id: 'rerun', label: '附意见重跑' },
        ],
        review: { commits: 1, adds: 2, dels: 3 },
        refs: { job_id: 'j2' },
      }),
      nowSec: NOW,
    })
    expect(buttonTag(review, '通过')).toContain('disabled')
    expect(buttonTag(review, '附意见重跑')).toContain('disabled')
  })

  it('viewing stays available: detail toggle and 稍后 are not disabled', async () => {
    state.who = READ_ONLY
    const html = await render(DecisionCard, {
      card: card({ advice: { text: '只读源码、只写 tmp/' } }),
      nowSec: NOW,
    })
    expect(buttonTag(html, '详情')).not.toContain('disabled')
    expect(buttonTag(html, '稍后')).not.toContain('disabled')
  })

  it('the queue 专注处理 button is disabled', async () => {
    state.who = READ_ONLY
    const html = await render(DecisionQueue, { cards: [card(), card({ key: 'review:j9', kind: 'review' })], nowSec: NOW, limit: 1 })
    expect(buttonTag(html, '专注处理')).toContain('disabled')
  })
})

describe('a caller holding the permission sees the actions', () => {
  it('interaction.answer enables the answers', async () => {
    state.who = who({ permissions: holds('interaction.answer') })
    const html = await render(DecisionCard, { card: card(), nowSec: NOW })
    for (const label of ['批准', '拒绝']) {
      const tag = buttonTag(html, label)
      expect(tag).not.toContain('disabled')
      expect(tag).not.toContain('title=')
    }
  })

  it('job.review enables the review card, and 专注处理 stays enabled', async () => {
    state.who = who({ permissions: holds('job.review') })
    const review = await render(DecisionCard, {
      card: card({ key: 'review:j2', kind: 'review', actions: [{ id: 'accept', label: '通过', style: 'ok' }], refs: { job_id: 'j2' } }),
      nowSec: NOW,
    })
    expect(buttonTag(review, '通过')).not.toContain('disabled')
    const queue = await render(DecisionQueue, { cards: [card(), card({ key: 'review:j9', kind: 'review' })], nowSec: NOW, limit: 1 })
    expect(buttonTag(queue, '专注处理')).not.toContain('disabled')
  })

  it('a card whose write the server decides by ownership (work report/park) is not greyed', async () => {
    state.who = who({})
    const html = await render(DecisionCard, {
      card: card({
        key: 'work:w1',
        kind: 'work',
        actions: [
          { id: 'reply', label: '回复', needs_text: true },
          { id: 'report', label: '请求汇报' },
          { id: 'park', label: '搁置' },
        ],
        refs: { work_item_id: 'w1' },
      }),
      nowSec: NOW,
    })
    for (const label of ['回复', '请求汇报', '搁置']) {
      expect(buttonTag(html, label)).not.toContain('disabled')
    }
  })
})
