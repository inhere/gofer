// 任务页并排布局（§16）：主内容区够宽时列表 + 详情并排，详情面板里的返回链接收起。
// 宽度由 utils/useElementWidth 提供，这里把它固定成 1440px（等于桌面展开侧栏的主内容区）。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'

vi.mock('../utils/useElementWidth', async () => {
  const { ref } = await import('vue')
  return { useElementWidth: () => ({ el: ref(null), width: ref(1440) }) }
})

const { default: JobsView } = await import('./JobsView.vue')
const { jobsSplitPreference, setJobsSplitPreference } = await import('../store/jobsSplit')

async function render(route: string, props: Record<string, unknown>): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(JobsView as never, props) })
  app.use(router)
  await router.push(route)
  await router.isReady()
  return renderToString(app)
}

describe('任务页并排（主内容区 ≥1280）', () => {
  it('renders the list beside the detail and hides the back link', async () => {
    setJobsSplitPreference('split')
    const html = await render('/jobs/j1', { id: 'j1' })
    expect(html).toContain('data-layout="split"')
    expect(html).toContain('data-test="jobs-view-list"')
    expect(html).toContain('data-test="jobs-view-detail"')
    // 列表在左：并排时列表的页面标题收成一行计数，详情页头不再放返回链接。
    expect(html).toContain('data-test="jobs-pane-summary"')
    expect(html).not.toContain('data-test="back-link"')
  })

  it('falls back to a single column after the 单栏 switch is remembered', async () => {
    setJobsSplitPreference('single')
    const html = await render('/jobs/j1', { id: 'j1' })
    expect(html).toContain('data-layout="single"')
    expect(html).not.toContain('data-test="jobs-view-list"')
    expect(html).toContain('data-test="back-link"')
    // 宽度够但选了单栏：仍给一个回「并排」的入口，否则就回不去了。
    expect(html).toContain('data-test="jobs-split-enable"')
    expect(jobsSplitPreference.value).toBe('single')
  })
})
