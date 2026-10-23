// 待验收页（/review）与验收面板（ReviewPanel）的 P4 迁移断言：
// 能用 SSR 渲染的（页头、空态、面板骨架、页签）直接渲染，无法渲染的（点击、轮询、弹层）
// 用 ?raw 读源码断言接线。纯函数（reviewGate / jobOutcome / scope）另有自己的测试。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import ReviewQueue from './ReviewQueue.vue'
import ReviewPanel from '../components/ReviewPanel.vue'
import { review as reviewDict } from '../i18n/pages/review'
import type { Job } from '../api/types'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const queueSrc = raw(import.meta.glob('./ReviewQueue.vue', { eager: true, query: '?raw', import: 'default' }))
const panelSrc = raw(
  import.meta.glob('../components/ReviewPanel.vue', { eager: true, query: '?raw', import: 'default' }),
)

async function render(
  component: unknown,
  path = '/review',
  opts: { query?: Record<string, string>; props?: Record<string, unknown> } = {},
): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(component as never, opts.props ?? {}) })
  app.use(router)
  await router.push({ path, query: opts.query ?? {} })
  await router.isReady()
  return renderToString(app)
}

function decode(html: string): string {
  return html
    .replace(/&#39;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&')
}

function job(over: Partial<Job> = {}): Job {
  return {
    id: 'j1',
    title: '示例任务',
    status: 'needs_review',
    project_key: 'demo',
    agent: 'codex',
    created_at: 1_760_000_000,
    started_at: 1_760_000_060,
    ended_at: 1_760_000_120,
    ...over,
  } as unknown as Job
}

describe('/review 列表页', () => {
  it('页头与空态走 i18n（标题、计数、两种停下来等验收的方式）', async () => {
    const html = decode(await render(ReviewQueue))
    expect(html).toContain(reviewDict.zh['review.h.title'])
    expect(html).toContain('0 个待验收任务 · 等得最久的排最前')
    expect(html).toContain(reviewDict.zh['review.empty.none'])
    expect(html).toContain('gofer job run … --review')
    expect(html).toContain('require_review: true')
    // 没生效中的筛选：不显示「当前筛选下没有」。
    expect(html).not.toContain(reviewDict.zh['review.empty.filtered'])
  })

  it('有 ?project= 时摘要说明是筛选后的计数', async () => {
    const html = decode(await render(ReviewQueue, '/review', { query: { project: 'demo' } }))
    expect(html).toContain(reviewDict.zh['review.empty.filtered'])
    expect(html).toContain('data-test="project-filter"')
  })

  it('项目筛选走 URL 唯一真源，验证结果走 chip', () => {
    expect(queueSrc).toContain("import { useProjectFilter } from '../ui/projectFilter'")
    expect(queueSrc).toContain("useProjectFilter('/review')")
    expect(queueSrc).toContain("import ProjectFilter from '../ui/ProjectFilter.vue'")
    expect(queueSrc).toContain("import FilterChips from '../ui/FilterChips.vue'")
    expect(queueSrc).toContain('watch(project, () => {')
    expect(queueSrc).toContain('listJobs({ status: \'needs_review\', project: project.value || undefined, limit: PAGE_SIZE })')
  })

  it('行内裁决、拒绝弹层与淡出照旧', () => {
    expect(queueSrc).toContain('v-if="reviewGate(job).allowed"')
    expect(queueSrc).toContain('data-test="rq-blocked"')
    expect(queueSrc).toContain('acceptJob(job.id)')
    expect(queueSrc).toContain('rejectJob(job.id, note, resume)')
    expect(queueSrc).toContain('@confirm="onReject"')
    expect(queueSrc).toContain('trow--leaving')
    // 等得最久的排最前
    expect(queueSrc).toContain('[...jobs.value].sort((a, b) => waitStart(a) - waitStart(b))')
  })

  it('手机（≤760）每行变成卡片、裁决按钮铺满', () => {
    expect(queueSrc).toContain('@media (max-width: 760px)')
    expect(queueSrc).toContain('display: flex;\n    flex-wrap: wrap;')
    expect(queueSrc).toContain('.rq-btn {\n    flex: 1 1 0;\n    min-height: 40px;')
  })

  it('页面文案全部走 review.* i18n key', () => {
    // 模板里不再有裸中文（注释除外）：抽查旧文案不在源码里。
    for (const gone of ['现在没有待验收的任务', '要让一个任务停下来等人验收', '验收通过失败', '刷新中']) {
      expect(queueSrc).not.toContain(gone)
    }
    expect(queueSrc).toContain("t('review.h.title')")
  })
})

describe('验收面板（ReviewPanel）', () => {
  it('渲染面板骨架：标题、状态 pill、页签与底部裁决条', async () => {
    const html = decode(await render(ReviewPanel, '/jobs/j1', { props: { job: job() } }))
    expect(html).toContain(reviewDict.zh['review.panel.title'])
    expect(html).toContain(reviewDict.zh['review.panel.hintDecide'])
    for (const label of ['review.tab.report', 'review.tab.commits', 'review.tab.diff', 'review.tab.verify', 'review.tab.usage']) {
      expect(html).toContain(reviewDict.zh[label])
    }
    expect(html).toContain('role="tablist"')
    expect(html).toContain(reviewDict.zh['review.act.acceptPlain'])
    expect(html).toContain(reviewDict.zh['review.act.rejectOpen'])
  })

  it('页签用 P2 的 Tabs，参数是 rtab（?tab= 已被任务详情占用），默认停在汇报', () => {
    expect(panelSrc).toContain("import Tabs from '../ui/Tabs.vue'")
    expect(panelSrc).toContain("const TAB_PARAM = 'rtab'")
    expect(panelSrc).toContain('tabFromQuery(route.query, tabItems.value, TAB_PARAM, TAB_FALLBACK)')
    expect(panelSrc).toContain(':param="TAB_PARAM"')
  })

  it('?rtab=diff 深链直接落在 Diff 页签', async () => {
    const html = decode(
      await render(ReviewPanel, '/jobs/j1', { query: { rtab: 'diff' }, props: { job: job() } }),
    )
    expect(html).toMatch(/aria-selected="true"[^>]*data-value="diff"|data-value="diff"[^>]*aria-selected="true"/)
  })

  it('裁决条贴底固定，通过 / 拒绝 / 不交付都在', () => {
    expect(panelSrc).toContain('.rp-actions {\n  position: sticky;\n  bottom: 0;')
    expect(panelSrc).toContain('t(\'review.act.skipDelivery\')')
    expect(panelSrc).toContain('v-if="reviewed"')
    expect(panelSrc).toContain('data-test="rp-blocked"')
  })

  it('原有页签内容全部保留：审阅提示、验收标准、发现建 issue、提交复制、diff 下载、verify 截尾、用量逐项', () => {
    expect(panelSrc).toContain('acceptanceLines(props.job.acceptance)')
    expect(panelSrc).toContain('createFindingIssue(props.job.id, text)')
    expect(panelSrc).toContain('findingIssueCommand(text, props.job.id)')
    expect(panelSrc).toContain(':download-name="`changes-${shortSha(job.id)}.diff`"')
    expect(panelSrc).toContain('block.slice(kept)')
    expect(panelSrc).toContain('MAX_VERIFY_LINES = 200')
    expect(panelSrc).toContain('formatTokens(n)')
    expect(panelSrc).toContain('listMemoryCandidates({ job_id: id, status: \'all\' })')
  })

  it('换 job 重置已加载文本，页签跟着 URL 重新加载', () => {
    expect(panelSrc).toContain('report.value = newResource()')
    expect(panelSrc).toContain('ensureTab(activeTab.value as Tab)')
    expect(panelSrc).toContain('watch(activeTab, (value) => ensureTab(value as Tab))')
  })

  it('面板文案全部走 review.* i18n key', () => {
    for (const gone of ['agent 已停', '已验收 · ', '处理中…', '加载中…']) {
      expect(panelSrc).not.toContain(gone)
    }
  })
})
