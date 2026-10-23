// 设置页外壳（设计稿 §7.4）：二级导航在桌面上是竖排导航、手机上是下拉框，
// 导航文案走 i18n（settings.nav.*），当前子页有 aria-current="page"。
import { afterEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import SettingsLayout from './SettingsLayout.vue'
import { locale } from '../../store/locale'

async function render(path: string): Promise<string> {
  // 外壳自己带 <RouterView />：子路由必须挂在外壳下面，否则会递归渲染自己。
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/settings',
        component: SettingsLayout,
        children: [{ path: ':section', component: { render: () => null } }],
      },
    ],
  })
  const app = createSSRApp({ render: () => h(RouterView) })
  app.use(router)
  await router.push(path)
  await router.isReady()
  return renderToString(app)
}

const sections = [
  'config',
  'rules',
  'mcp',
  'tunnels',
  'notifications',
  'permissions',
  'work',
  'extensions',
  'skills',
  'about',
]

afterEach(() => {
  locale.value = 'zh-CN'
})

describe('SettingsLayout', () => {
  it('links every settings section from the side navigation', async () => {
    const html = await render('/settings/work')
    expect(html).toContain('class="set-nav"')
    for (const s of sections) {
      expect(html, s).toContain(`href="/settings/${s}"`)
    }
  })

  it('marks the active section and shows it in the breadcrumb', async () => {
    const html = await render('/settings/work')
    expect(html).toMatch(/aria-current="page"[^>]*href="\/settings\/work"/)
    expect(html).toContain('工作项')
    // 其他项不能也标成当前页：只有一处 aria-current
    expect(html.match(/aria-current="page"/g)?.length).toBe(1)
  })

  it('offers a dropdown with the same sections for phones', async () => {
    const html = await render('/settings/tunnels')
    expect(html).toContain('data-testid="settings-nav-select"')
    for (const s of sections) {
      expect(html, s).toContain(`<option value="/settings/${s}"`)
    }
    // 下拉框的当前值就是正在看的子页（Vue 用 value 控制选中项）
    expect(html).toContain('value="/settings/tunnels"')
    expect(html).toContain('<select class="set-select" value="/settings/tunnels"')
  })

  it('translates the navigation labels', async () => {
    locale.value = 'en-US'
    const html = await render('/settings/notifications')
    expect(html).toContain('Notifications')
    expect(html).toContain('Work items')
    expect(html).toContain('About')
    expect(html).not.toContain('设置分区')
  })
})
