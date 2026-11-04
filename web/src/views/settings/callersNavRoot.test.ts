// 根管理员那一侧：设置区的「用户」入口在 whoami 说是根管理员时出现（桌面导航 + 手机下拉框各一份）。
// 非根用户那一侧见 settingsLayout.test.ts（测试环境取不到 whoami，即「不是根管理员」）。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h, ref } from 'vue'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import { renderToString } from 'vue/server-renderer'

vi.mock('../../store/whoami', () => ({
  useWhoami: () => ({ isRoot: ref(true) }),
}))

import SettingsLayout from './SettingsLayout.vue'

async function render(path: string): Promise<string> {
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

describe('SettingsLayout 的用户入口（根管理员）', () => {
  it('shows the caller section in both navigations', async () => {
    const html = await render('/settings/callers')
    expect(html).toContain('href="/settings/callers"')
    expect(html).toContain('<option value="/settings/callers"')
    // 当前页在面包屑里，并且只有一处 aria-current。
    expect(html).toMatch(/aria-current="page"[^>]*href="\/settings\/callers"/)
    expect(html.match(/aria-current="page"/g)?.length).toBe(1)
  })
})
