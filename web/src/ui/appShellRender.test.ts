import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { setLiveClientForTest } from '../api/live'
import AppShell from './AppShell.vue'

// SSR 冒烟（设计稿 §6）：外壳在三种宽度下渲染成对应形态。断点判断本身在 utils/shell.test.ts，
// 这里钉住「模板真的按形态切换了导航容器」，防止外壳接线回归。
// 说明：视口宽度只有 setup 里读一次（onMounted 在 SSR 不跑），所以给全局 window 一个最小替身即可。
async function render(width: number | null): Promise<string> {
  if (width === null) {
    delete (globalThis as { window?: unknown }).window
  } else {
    ;(globalThis as { window?: unknown }).window = {
      innerWidth: width,
      addEventListener() {},
      removeEventListener() {},
    }
  }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(AppShell, null, { default: () => h('div', { 'data-test': 'slot' }) }) })
  app.use(router)
  await router.push('/today')
  await router.isReady()
  return renderToString(app)
}

beforeEach(() => {
  // 顶栏常驻组件会订阅推送主题：给 live 一个空客户端，SSR 里不建 WebSocket。
  setLiveClientForTest({ subscribe: () => () => {}, noteSeq: () => {}, status: () => 'connected', close: () => {} })
})

afterEach(() => {
  setLiveClientForTest(null)
  delete (globalThis as { window?: unknown }).window
})

describe('AppShell SSR smoke', () => {
  it('renders the desktop shell with a full sidebar and no phone tab bar', async () => {
    const html = await render(1440)
    expect(html).toContain('data-test="app-shell"')
    expect(html).toContain('data-form="desktop"')
    expect(html).toContain('data-test="slot"')
    expect(html).toContain('data-test="side-link"')
    expect(html).toContain('data-test="needs-me"')
    expect(html).toContain('data-test="palette-open"')
    expect(html).toContain('data-test="nodes-summary"')
    expect(html).not.toContain('data-test="tab-more"')
  })

  it('renders the icon rail at 1024 and keeps the shell free of a project picker', async () => {
    const html = await render(1024)
    expect(html).toContain('data-form="rail"')
    expect(html).toContain('side--rail')
    expect(html).not.toContain('data-test="tab-more"')
    // 侧栏不放项目选择器（§6.4）
    expect(html).not.toContain('project-picker')
  })

  it('renders the phone shell with the 5-tab bar and no sidebar', async () => {
    const html = await render(390)
    expect(html).toContain('data-form="tabs"')
    expect(html).toContain('data-test="tab-more"')
    expect(html).not.toContain('data-test="side-link"')
    // 5 个标签：今天 / 工作台 / 任务 / 计划 / 更多（每项一个 tab-label）
    expect(html.match(/class="tab-label"/g)?.length).toBe(5)
  })

  it('draws a lucide icon for every navigation entry', async () => {
    // issue gofer-h355：图标不再是 Unicode 字形，而是 ui/Icon.vue 渲染的 lucide svg
    const desktop = await render(1440)
    expect(desktop.match(/class="lucide lucide-/g)?.length).toBeGreaterThanOrEqual(16)
    // 导航图标都是装饰性的：名称在链接的 title / 文字标签上
    expect(desktop).toContain('data-test="side-link"')
    const phone = await render(390)
    // 4 个标签 + 「更多」
    expect(phone.match(/class="lucide lucide-/g)?.length).toBeGreaterThanOrEqual(5)
  })
})

describe('brand logo', () => {
  it('shows the gofer logo instead of a letter in the sidebar and the phone top bar', async () => {
    const desktop = await render(1440)
    expect(desktop).toContain('data-test="brand-logo"')
    expect(desktop).toContain('src="/favicon.svg"') // :src 绑定在 SSR 里渲染成同样的属性
    expect(desktop).not.toMatch(/class="brand-mark"[^>]*>g</)
    const phone = await render(390)
    expect(phone).toContain('data-test="brand-logo"')
  })
})
