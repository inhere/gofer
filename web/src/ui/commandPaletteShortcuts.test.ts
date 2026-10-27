import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import CommandPalette from './CommandPalette.vue'
import { SHELL_SHORTCUTS } from '../utils/shell'
import { translate } from '../i18n'

// 快捷键登记（issue gofer-okz3 ①）：Ctrl/⌘+J 这类全局键必须能在命令面板里查到，
// 否则只有写过它的人知道。清单只有 SHELL_SHORTCUTS 一份（utils/shell.ts）。
async function render(open: boolean): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(CommandPalette, { open }) })
  app.use(router)
  await router.push('/today')
  await router.isReady()
  return renderToString(app)
}

describe('command palette shortcut list', () => {
  it('registers the next-today-item shortcut', () => {
    const entry = SHELL_SHORTCUTS.find((s) => s.key === 'palette.shortcut.next')
    expect(entry?.keys).toBe('Ctrl/⌘ J')
  })

  it('has a label for every registered shortcut, in both locales', () => {
    for (const s of SHELL_SHORTCUTS) {
      for (const loc of ['zh-CN', 'en-US'] as const) {
        expect(translate(s.key, undefined, loc)).not.toBe(s.key)
      }
    }
  })

  it('renders the shortcut list in the panel', async () => {
    const html = await render(true)
    expect(html).toContain('data-test="palette-shortcuts"')
    expect(html).toContain('Ctrl/⌘ J')
    expect(html).toContain(translate('palette.shortcut.next', undefined, 'zh-CN'))
  })

  it('renders nothing while closed', async () => {
    const html = await render(false)
    expect(html).not.toContain('data-test="palette-shortcuts"')
  })
})
