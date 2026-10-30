import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import RefsPanel from './RefsPanel.vue'
import type { RefItem } from '../api/types'

const sources = import.meta.glob(['../views/JobDetail.vue', '../views/PlanDetail.vue', './IssueDrawer.vue', '../api/client.ts'], {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>
const src = (name: string): string => sources[name]

async function render(props: { kind: string; id: string; items?: RefItem[] }): Promise<string> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  await router.push('/')
  const app = createSSRApp({ render: () => h(RefsPanel, props) })
  app.use(router)
  return renderToString(app)
}

const now = Math.floor(Date.now() / 1000)
const items: RefItem[] = [
  { source_kind: 'job', source_id: 'job-demo-1', kind: 'issue', id: 'gofer-demo', field: 'title', created_at: now - 7200, source_title: '示例任务' },
  { source_kind: 'plan', source_id: 'plan-demo', kind: 'issue', id: 'gofer-demo', field: 'description', created_at: now - 90000, source_title: '示例计划' },
  { source_kind: 'comment', source_id: 'cm-1', kind: 'issue', id: 'gofer-demo', field: 'body', created_at: now - 10, source_title: 'x'.repeat(200) },
]

describe('RefsPanel', () => {
  it('renders the count in the title and one row per source with a link for job / plan', async () => {
    const html = await render({ kind: 'issue', id: 'gofer-demo', items })
    expect(html).toContain('data-test="refs-panel"')
    expect(html).toContain('被引用（3）')
    expect(html).toContain('示例任务')
    expect(html).toContain('href="/jobs/job-demo-1"')
    expect(html).toContain('href="/plans/plan-demo"')
    expect(html).toContain('2h前')
    expect(html).toContain('1d前')
    expect(html.match(/data-test="refs-item"/g)).toHaveLength(3)
  })

  it('shows the field of each reference and does not link a comment source; long titles are cut', async () => {
    const html = await render({ kind: 'issue', id: 'gofer-demo', items })
    expect(html).toContain('>标题<')
    expect(html).toContain('>描述<')
    expect(html).toContain('>正文<')
    expect(html.match(/data-test="refs-link"/g)).toHaveLength(2)
    expect(html).toContain('x'.repeat(80) + '…')
    expect(html).not.toContain('x'.repeat(81))
  })

  it('renders nothing when there are no references', async () => {
    const html = await render({ kind: 'job', id: 'job-demo-1', items: [] })
    expect(html).not.toContain('refs-panel')
    expect(html).not.toContain('被引用')
  })

  it('renders nothing before the fetch has returned (no items prop)', async () => {
    const html = await render({ kind: 'job', id: 'job-demo-1' })
    expect(html).not.toContain('refs-panel')
  })
})

describe('RefsPanel wiring', () => {
  it('client.listRefs calls GET /v1/refs with kind and id', () => {
    expect(src('../api/client.ts')).toMatch(/export async function listRefs\(kind: string, id: string\)/)
    expect(src('../api/client.ts')).toContain('`/v1/refs?${qs.toString()}`')
  })

  it('is mounted on the job detail page (after the tabs), kind=job', () => {
    const s = src('../views/JobDetail.vue')
    expect(s).toContain("import RefsPanel from '../components/RefsPanel.vue'")
    expect(s).toContain('<RefsPanel v-if="job" kind="job" :id="job.id" />')
  })

  it('is mounted on the plan detail page, kind=plan', () => {
    const s = src('../views/PlanDetail.vue')
    expect(s).toContain("import RefsPanel from '../components/RefsPanel.vue'")
    expect(s).toContain('<RefsPanel v-if="plan" kind="plan" :id="plan.plan_id" />')
  })

  it('is mounted in the issue drawer, kind=issue', () => {
    const s = src('./IssueDrawer.vue')
    expect(s).toContain("import RefsPanel from './RefsPanel.vue'")
    expect(s).toContain('<RefsPanel kind="issue" :id="issue.id" />')
  })
})
