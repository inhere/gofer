import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { TodayCard, TodayResponse } from '../../api/today'

vi.mock('../../api/client', () => ({ withKeepalive: <T>(fn: () => T): T => fn() }))
vi.mock('../../api/steward', () => ({}))
vi.mock('../../api/today', () => ({}))
vi.mock('../../utils/useLiveTopic', () => ({ createLiveTopic: () => ({ start() {}, stop() {} }) }))

const store = await import('../../store/today')
const FocusMode = (await import('./FocusMode.vue')).default

const raw = import.meta.glob(['./FocusMode.vue', './DecisionCard.vue', './SnoozedDrawer.vue', './DecisionQueue.vue'], {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

const NOW = 1_760_000_000

function card(key: string): TodayCard {
  return {
    key,
    kind: 'review',
    tag: '待验收',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: `title ${key}`,
    waiting_since: NOW - 60,
    activity_at: 0,
    summary: 's',
    refs: { job_id: key },
    actions: [{ id: 'accept', label: '通过', style: 'ok' }],
    advice: null,
  }
}

async function render(): Promise<string> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  const app = createSSRApp({ render: () => h(FocusMode, { nowSec: NOW }) })
  app.use(router)
  await router.push('/today')
  return renderToString(app)
}

describe('FocusMode', () => {
  it('shows one card at a time with 「1 / N」, the progress bar and the key hints', async () => {
    store.hiddenKeys.value = new Set()
    store.todayData.value = { decisions: [card('a'), card('b'), card('c')] } as unknown as TodayResponse
    const html = await render()
    expect(html).toContain('data-test="focus-mode"')
    expect(html).toContain('1 / 3')
    expect(html.match(/data-test="decision-card"/g)?.length).toBe(1)
    expect(html).toContain('title a')
    expect(html).toContain('width:0%')
    for (const k of ['j</kbd>', '1-9</kbd>', 'a</kbd>', 'r</kbd>', 'i</kbd>', 'h</kbd>', 's</kbd>', 'z</kbd>']) {
      expect(html).toContain(k)
    }
    expect(html).toContain('退出 Esc')
  })

  it('ends with 「全部处理完」 and a way back, never forcing the queue clear', async () => {
    store.todayData.value = { decisions: [] } as unknown as TodayResponse
    const html = await render()
    expect(html).toContain('全部处理完')
    expect(html).toContain('本轮处理 0 张 · 按建议 0 张')
    expect(html).toContain('data-test="focus-back"')
    expect(html).not.toContain('data-test="focus-pos"')
  })

  it('keeps the focus view, the snooze menu and the drawer inside a phone width (no horizontal scroll)', () => {
    const focus = raw['./FocusMode.vue']
    expect(focus).toMatch(/\.fm \{[^}]*overflow-x: hidden/)
    expect(focus).toMatch(/\.fm-stage \{[^}]*min-width: 0/)
    expect(focus).toContain('touch-action: pan-y')
    // 手机上不显示键位行，改为「左右滑切换」
    expect(focus).toMatch(/@media \(max-width: 640px\) \{\s*\.fm-keys \{\s*display: none/)
    const dc = raw['./DecisionCard.vue']
    expect(dc).toContain('max-width: min(260px, calc(100vw - 32px))')
    expect(raw['./SnoozedDrawer.vue']).toContain('width: min(440px, 100vw)')
    expect(raw['./DecisionQueue.vue']).toMatch(/\.dq-rest \{[^}]*flex-wrap: wrap/)
  })

  it('ignores keys while typing and lets overlays on top take them', () => {
    const focus = raw['./FocusMode.vue']
    expect(focus).toContain('typing: isTypingTarget(ev.target)')
    expect(focus).toContain('if (overlayOpen.value || handledOpen.value || snoozedOpen.value) return')
  })
})
