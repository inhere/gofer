import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import DocLinkText from './DocLinkText.vue'

async function render(text: string, context?: { projectKey: string }): Promise<string> {
  const app = createSSRApp({ render: () => h(DocLinkText, { text, docContext: context }) })
  const router = createRouter({ history: createMemoryHistory(), routes: [] })
  app.use(router)
  return renderToString(app)
}

describe('DocLinkText', () => {
  it('keeps text plain when no project context is available', async () => {
    const html = await render('<b>docs/a.md</b>')
    expect(html).toContain('&lt;b&gt;docs/a.md&lt;/b&gt;')
    expect(html).not.toContain('href=')
  })

  it('links recognised paths through the reader route', async () => {
    const html = await render('see docs/a.md:3。', { projectKey: 'demo' })
    expect(html).toContain('href="/projects/demo/doc?path=docs/a.md&amp;line=3"')
    expect(html).toContain('see ')
  })
})
