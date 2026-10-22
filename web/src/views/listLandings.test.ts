// P2 落地的两个列表：任务列表（/jobs）与计划总览（/plans）的列表头与渲染结构。
// SSR 只跑到 setup（onMounted 不执行），所以这里不连接口，只钉结构与筛选接线；
// 行的渲染细节在 ui/primitives.test.ts（PlanOverviewRow）与现有 Board 测试里。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import Board from './Board.vue'
import Plans from './Plans.vue'

const boardSource = Object.values(
  import.meta.glob('./Board.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const plansSource = Object.values(
  import.meta.glob('./Plans.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

async function render(comp: unknown, route: string): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(comp as never) })
  app.use(router)
  await router.push(route)
  await router.isReady()
  return renderToString(app)
}

describe('任务列表（/jobs）列表头', () => {
  it('uses a project chip read from ?project= and status chips with counts', async () => {
    const html = await render(Board, '/jobs?project=p1&status=running')
    expect(html).toContain('data-test="project-filter"')
    expect(html).toContain('data-test="project-filter-trigger"')
    expect(html).toContain('>p1<')
    expect(html).toContain('data-test="filter-chips"')
    // 状态 chip 选中项来自 ?status=
    expect(html).toMatch(/data-value="running"[^>]*/)
    expect(html).toContain('aria-pressed="true"')
    expect(html).toContain('aria-pressed="false"')
    // 其余维度进「筛选」抽屉
    expect(html).toContain('data-test="filter-drawer-trigger"')
    expect(html).toContain('更多筛选')
    // 旧的状态 tabs 与项目 select 不再出现
    expect(html).not.toContain('class="status-tabs"')
    expect(html).not.toContain('filter-select--project')
  })

  it('keeps every existing filter dimension (plan, workflow, tags, agent, runner, caller, time, uploads, messenger)', () => {
    const source = boardSource
    for (const marker of [
      'v-model="planInput"',
      'data-test="wf-filter"',
      'v-model="tagFilter"',
      'v-model="agentFilter"',
      'v-model="runnerFilter"',
      'v-model="callerFilter"',
      'v-model="sinceFilter"',
      'v-model="hasUploadFilter"',
      'v-model="showMessengerJobs"',
    ]) {
      expect(source, marker).toContain(marker)
    }
    // 筛选维度仍然参与请求与 watch（行为不变）
    expect(source).toContain('workflow_id: wfFilter.value')
    expect(source).toContain('all: showMessengerJobs.value')
    expect(source).toContain('useListReturn(\'jobs\', \'/jobs\')')
  })
})

describe('计划总览（/plans）', () => {
  it('renders project + status chips and a 筛选 drawer', async () => {
    const html = await render(Plans, '/plans?project=p1&status=open,done')
    expect(html).toContain('data-test="project-filter"')
    expect(html).toContain('>p1<')
    expect(html).toContain('data-test="filter-chips"')
    expect(html).toContain('data-value="open"')
    expect(html).toContain('data-test="filter-drawer-trigger"')
    expect(html).not.toContain('class="filter-drawer"')
  })

  it('shows the overview columns: status, plan, progress, blockers, main session, last activity', async () => {
    const html = await render(Plans, '/plans')
    // 只看表头那一行（页面其他地方也出现「进度」这类词）
    const start = html.indexOf('class="thead')
    const head = html.slice(start, html.indexOf('</div>', start))
    const order = ['状态', '计划 · 标题 / ID', '进度', '卡点', '主会话', '最近活动']
    let at = -1
    for (const marker of order) {
      const i = head.indexOf(marker)
      expect(i, marker).toBeGreaterThan(at)
      at = i
    }
    // 旧的一次 tableView 列（项目 / tags / 更新）已经换成卡点 / 主会话 / 最近活动
    expect(html).not.toContain('col-counts')
  })

  it('renders one PlanOverviewRow per plan (progress + blockers live in the row)', () => {
    const source = plansSource
    expect(source).toContain('<PlanOverviewRow v-for="p in plans"')
    expect(source).toContain(':plan="p"')
  })
})
