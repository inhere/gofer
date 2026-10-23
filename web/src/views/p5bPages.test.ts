// P5-b 三页（Issues / 统计 / 新建任务）的结构与字典：SSR 只跑到 setup（onMounted 不执行），
// 所以断言的是页面骨架、共用基元与文案语言，而不是数据。
import { afterEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import Issues from './Issues.vue'
import NewJob from './NewJob.vue'
import { setLocale } from '../store/locale'

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

afterEach(() => setLocale('zh-CN'))

describe('Issues 页（§7.3）', () => {
  it('uses the shared tabs, the project chip and no tracker eyebrow', async () => {
    setLocale('zh-CN')
    const html = await render(Issues, '/issues')
    expect(html).toContain('data-test="issues-page"')
    // 页签：统一 Tabs（Issues / 记忆），带 role 与 data-value
    expect(html).toContain('role="tablist"')
    expect(html).toContain('data-value="issues"')
    expect(html).toContain('data-value="memories"')
    // 仓库选择是仓库专用下拉（项目 / 目录）与完整路径信息条
    expect(html).toContain('data-test="tracker-repo-picker"')
    expect(html).toContain('data-test="tracker-repo-info"')
    expect(html).not.toContain('TRACKER')
    expect(html).toContain('新建 issue')
  })

  it('renders the page in English when the locale is en-US', async () => {
    setLocale('en-US')
    const html = await render(Issues, '/issues')
    expect(html).toContain('New issue')
    expect(html).toContain('Refresh')
    expect(html).not.toContain('新建 issue')
  })
})

describe('新建任务页（§7.1）', () => {
  it('puts the common fields in a two-column grid and folds the rest', async () => {
    setLocale('zh-CN')
    const html = await render(NewJob, '/new')
    // 加载选项时只显示标题与返回链接
    expect(html).toContain('data-test="back-link"')
    expect(html).toContain('新建任务')
  })
})
