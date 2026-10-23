import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import Agents from './Agents.vue'
import { agents as dict } from '../i18n/pages/agents'

// P5-c：页面结构断言改成 SSR 渲染结果（设计稿 §13：不再断言源码里的中文文案与 class 文本）。
// 期望值直接取自页面字典，字典换了文案测试不用跟着改。
async function render(): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/agents', component: { template: '<div />' } },
      { path: '/runners', component: { template: '<div />' } },
      { path: '/settings/config', component: { template: '<div />' } },
    ],
  })
  await router.push('/agents')
  await router.isReady()
  const app = createSSRApp(Agents)
  app.use(router)
  return renderToString(app)
}

describe('P5-c Agents page', () => {
  it('renders the page head and the column header from the page dictionary', async () => {
    const html = await render()
    expect(html).toContain('class="page-head"')
    for (const key of ['agents.col.detect', 'agents.col.key', 'agents.col.type', 'agents.col.health', 'agents.col.info']) {
      expect(html, key).toContain(dict.zh[key])
    }
  })

  it('links the runners page from the scope note', async () => {
    const html = await render()
    expect(html).toContain('href="/runners"')
  })

  it('shows the empty state before the first response arrives', async () => {
    const html = await render()
    expect(html).toContain('class="empty"')
    expect(html).toContain(dict.zh['agents.empty'])
  })
})
