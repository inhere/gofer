// 记忆页（/memory，issue gofer-hez1）：路由、页面骨架、Issues 页的记忆入口与页签接线。
// SSR 只跑到 setup（onMounted 不执行），所以这里断言结构与字典，不连接口。
import { afterEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { RouteLocation } from 'vue-router'
import Memory from './Memory.vue'
import Issues from './Issues.vue'
import { routes } from '../router.routes'
import { setLocale } from '../store/locale'

const SRC = import.meta.glob('./{Memory,Issues}.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const MemorySource = SRC['./Memory.vue'] ?? ''

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

describe('路由（/memory 与旧记忆链接）', () => {
  function entry(path: string) {
    return routes.find((r) => r.path === path)
  }

  it('serves the memory page on /memory under the name "memory"', () => {
    const memory = entry('/memory')
    expect(memory?.name).toBe('memory')
    expect(memory?.component).toBeTypeOf('function')
  })

  it('redirects /issues?tab=memories to /memory with every parameter kept', () => {
    const guard = entry('/issues')?.beforeEnter as (to: RouteLocation) => unknown
    expect(guard).toBeTypeOf('function')
    const to = {
      path: '/issues',
      query: { tab: 'memories', memory_scope: 'repo', tracker: 'trk', memory: 'release-b' },
    } as unknown as RouteLocation
    expect(guard(to)).toEqual({ path: '/memory', query: { memory_scope: 'repo', tracker: 'trk', memory: 'release-b' } })
  })

  it('leaves a normal /issues visit alone', () => {
    const guard = entry('/issues')?.beforeEnter as (to: RouteLocation) => unknown
    expect(guard({ path: '/issues', query: { issue: 'gofer-1' } } as unknown as RouteLocation)).toBe(true)
  })
})

describe('记忆页骨架', () => {
  it('renders the page head, the three scopes, the repo chip and the memory filters', async () => {
    setLocale('zh-CN')
    const html = await render(Memory, '/memory')
    expect(html).toContain('data-test="memory-page"')
    // 页头标题「记忆 / Memory」与刷新
    expect(html).toContain('记忆 / Memory')
    expect(html).toContain('data-test="memory-refresh"')
    // 三档作用域是统一 chip，默认仓库
    expect(html).toContain('data-test="filter-chips"')
    expect(html).toContain('data-value="repo"')
    expect(html).toContain('data-value="project"')
    expect(html).toContain('data-value="global"')
    expect(html).toContain('aria-pressed="true"')
    expect(html).toContain('随代码存于该仓库 .gofer/tracker')
    // 仓库选择用统一的项目 chip（?tracker=），旁边是同步按钮
    expect(html).toContain('data-test="project-filter"')
    expect(html).toContain('data-test="sync-repo"')
  })

  it('opens on the project scope from ?memory_scope=(deep links stay shareable)', async () => {
    setLocale('zh-CN')
    const html = await render(Memory, '/memory?memory_scope=project&memory_project=gofer')
    expect(html).toContain('data-test="memory-project"')
    // 项目下拉里带着 URL 里的项目
    expect(html).toContain('gofer')
  })

  it('shows the global sync-status panel and the memory filters on the global scope', async () => {
    setLocale('zh-CN')
    const html = await render(Memory, '/memory?memory_scope=global')
    expect(html).toContain('data-test="sync-clients"')
    expect(html).toContain('所有项目共享')
    // 记忆类型 chip 与关键字（范围为空时也看得见，才能改筛选）
    expect(html).toContain('data-test="memory-kind-filter"')
    expect(html).toContain('data-value="rule"')
    expect(html).toContain('data-test="memory-search"')
    // 「只看有标记的」只在仓库作用域出现（doctor 只覆盖仓库记忆）
    expect(MemorySource).toContain('data-test="memory-flagged"')
  })

  it('renders in English when the locale is en-US', async () => {
    setLocale('en-US')
    const html = await render(Memory, '/memory')
    expect(html).toContain('Memory')
    expect(html).toContain('Refresh')
    expect(html).toContain('Repository')
    expect(html).not.toContain('记忆 / Memory')
    expect(html).not.toContain('刷新')
  })
})

describe('Issues 页的记忆入口（不重复维护第二份列表）', () => {
  it('keeps the 记忆 tab as a link to /memory instead of an in-page panel', async () => {
    setLocale('zh-CN')
    const html = await render(Issues, '/issues')
    expect(html).toContain('data-value="issues"')
    expect(html).toContain('data-value="memories"')
    // 页面里不再有记忆面板与记忆筛选（它们搬到了 /memory）
    expect(html).not.toContain('data-test="memory-kind-filter"')
    expect(html).not.toContain('data-test="sync-clients"')
    const source = SRC['./Issues.vue'] ?? ''
    expect(source).toContain("{ value: 'memories', label: t('issues.tab.memories'), to: '/memory' }")
    expect(source).not.toContain("import MemoryList from '../components/MemoryList.vue'")
  })

  it('jumps to the target route for link tabs instead of writing ?tab=', () => {
    const tabsSource = Object.values(
      import.meta.glob('../ui/Tabs.vue', { eager: true, query: '?raw', import: 'default' }),
    )[0] as string
    expect(tabsSource).toContain('const target = props.tabs.find((tab) => tab.value === value)?.to')
    expect(tabsSource).toContain('void router.push(target)')
  })
})
