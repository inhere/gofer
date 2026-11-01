import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import TodayPlans from './TodayPlans.vue'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const src = raw(import.meta.glob('./TodayPlans.vue', { eager: true, query: '?raw', import: 'default' }))

describe('TodayPlans', () => {
  it('renders nothing before there is a plan in progress (health takes no room)', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
    const app = createSSRApp({ render: () => h(TodayPlans) })
    app.use(router)
    await router.push('/today')
    expect(await renderToString(app)).not.toContain('today-plans')
  })

  it('reads the open plans, links each row to its plan and the overflow to the plans page', () => {
    expect(src).toContain("listPlans({ status: 'open'")
    expect(src).toContain('todayPlanRows(plans.value)')
    expect(src).toContain('data-test="today-plans"')
    expect(src).toContain('data-test="today-plan-row"')
    expect(src).toContain(':to="r.to"')
    expect(src).toContain('to="/plans"')
    // refreshed with the page's own live refresh
    expect(src).toContain('watch(() => todayData.value?.generated_at')
  })
})
