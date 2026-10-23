// 任务页 P3（/jobs 列表 + /jobs/:id 详情）：页签、默认页签、深链，以及列表行的字段。
// SSR 只跑到 setup（onMounted 不执行）→ 详情页没有 job 数据、宽度量到 0（退回单栏）。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import JobsView from './JobsView.vue'
import JobDetail from './JobDetail.vue'

const boardSource = Object.values(
  import.meta.glob('./Board.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const detailSource = Object.values(
  import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

async function render(comp: unknown, route: string, props: Record<string, unknown> = {}): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(comp as never, props) })
  app.use(router)
  await router.push(route)
  await router.isReady()
  return renderToString(app)
}

describe('任务详情的页签（§7.1、§16-5）', () => {
  it('renders the seven sections with ?tab= deep links', async () => {
    const html = await render(JobDetail, '/jobs/j1', { id: 'j1' })
    for (const value of ['overview', 'review', 'log', 'changes', 'messages', 'artifacts', 'events']) {
      expect(html, value).toContain(`data-value="${value}"`)
    }
    expect(html).toContain('role="tablist"')
    expect(html).toContain('role="tabpanel"')
  })

  it('opens the panel named by ?tab=', async () => {
    // 用「变更」页签：日志面板（LogTape）在 setup 里读 window，node SSR 下不挂。
    const html = await render(JobDetail, '/jobs/j1?tab=changes', { id: 'j1' })
    expect(html).toContain('id="tab-tab-changes" aria-selected="true"')
    expect(html).toContain('id="panel-tab-changes"')
  })

  it('falls back to 概览 for an unknown ?tab= value', async () => {
    const html = await render(JobDetail, '/jobs/j1?tab=nope', { id: 'j1' })
    expect(html).toContain('id="tab-tab-overview" aria-selected="true"')
    expect(html).toContain('id="panel-tab-overview"')
  })

  it('hides the back link when embedded beside the list', async () => {
    const single = await render(JobDetail, '/jobs/j1', { id: 'j1' })
    const split = await render(JobDetail, '/jobs/j1', { id: 'j1', embedded: true })
    expect(single).toContain('data-test="back-link"')
    expect(split).not.toContain('data-test="back-link"')
  })

  it('puts each old block into its section', () => {
    // 概览：待批面板、元信息网格、渲染命令
    expect(detailSource).toMatch(/v-if="active === 'overview' && job && showApprovalPanel"/)
    expect(detailSource).toMatch(/v-if="active === 'overview' && job" class="meta"/)
    expect(detailSource).toMatch(/v-if="active === 'overview' && renderedCommand"/)
    // 验收 / 日志 / 变更
    expect(detailSource).toMatch(/v-if="active === 'review' && job && showReviewPanel"/)
    expect(detailSource).toMatch(/<template v-if="active === 'log'">/)
    expect(detailSource).toContain('data-test="job-changes"')
    // 消息：传话、消息列表、ACP 输入框
    expect(detailSource).toMatch(/v-if="active === 'messages' && job\?\.messenger"/)
    expect(detailSource).toMatch(/v-if="active === 'messages' && job\?\.session"/)
    // 产物与事件
    expect(detailSource).toMatch(/v-if="active === 'artifacts' && hasOutcomes"/)
    for (const marker of ['class="interactions"', 'class="timeline"', 'class="comments"', 'class="deliveries"', 'class="pty-sessions"', 'class="wakeups"']) {
      expect(detailSource, marker).toContain(marker)
    }
    expect(detailSource).toMatch(/v-if="active === 'events'" class="comments"/)
  })
})

describe('任务详情的宿主页（§16 并排 / 单栏）', () => {
  it('falls back to a single column when the width is unknown (SSR)', async () => {
    const html = await render(JobsView, '/jobs/j1', { id: 'j1' })
    expect(html).toContain('data-layout="single"')
    expect(html).toContain('data-test="jobs-view-detail"')
    expect(html).not.toContain('data-test="jobs-view-list"')
  })
})

describe('任务列表的行（§7.1 字段清单）', () => {
  it('keeps status pill, title/id, project, agent, runner, badges, signal/duration and time', () => {
    for (const marker of [
      '<StatusPill :status="job.status" />',
      'class="job-title"',
      'class="job-id mono"',
      'class="col-proj mono">{{ job.project_key }}',
      'class="col-agent mono"',
      'class="col-runner mono"',
      'class="col-signal"',
      'class="col-time mono"',
      'job.uncommitted_count',
      "job.verify?.status === 'failed'",
      'job.read_only',
      'job.role',
      'job.channel',
    ]) {
      expect(boardSource, marker).toContain(marker)
    }
    // 耗时对所有状态都显示（旧版只有 running 才显示）。
    expect(boardSource).not.toMatch(/v-if="job\.status === 'running'" class="run-dur/)
  })

  it('opens a job with the current filters in the query', () => {
    expect(boardSource).toContain('query: { ...route.query }')
  })

  it('offers the 单栏 switch when embedded beside the detail', () => {
    expect(boardSource).toContain('data-test="jobs-split-toggle"')
    expect(boardSource).toContain("setJobsSplitPreference('single')")
  })
})
