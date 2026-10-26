// MarkdownBlock 的两个可选渲染开关（待办备注用）：
//  - 默认仍按标准 markdown 渲染（单换行并成一段、链接不加 target），其它用法（产物预览、
//    汇报）行为不变；
//  - breaks 保住「一行一条」的换行，externalLinks 让链接在新标签页打开。
//
// 测试环境没有 DOM，DOMPurify 的 sanitize 需要 window：这里当透传（与 todoDrawer.test.ts 同法），
// 断言的是 marked 的产物与注入的 target/rel。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import MarkdownBlock from './MarkdownBlock.vue'

vi.mock('dompurify', () => ({ default: { sanitize: (html: string) => html } }))

async function render(props: Record<string, unknown>): Promise<string> {
  const app = createSSRApp({ render: () => h(MarkdownBlock, props as never) })
  return renderToString(app)
}

const URL_TEXT = '复现：https://bugs.example.test/t/9\n第二条：已经修了'

describe('MarkdownBlock render switches', () => {
  it('keeps the old behaviour by default (no hard breaks, no link target)', async () => {
    const html = await render({ text: URL_TEXT })
    expect(html).not.toContain('<br>')
    expect(html).toContain('href="https://bugs.example.test/t/9"')
    expect(html).not.toContain('target="_blank"')
  })

  it('turns single newlines into hard breaks with breaks', async () => {
    const html = await render({ text: URL_TEXT, breaks: true })
    expect(html).toContain('<br>')
    expect(html).toContain('第二条：已经修了')
  })

  it('opens every link in a new tab with externalLinks', async () => {
    const html = await render({ text: '[缺陷](https://bugs.example.test/t/9)', externalLinks: true })
    expect(html).toContain('<a target="_blank" rel="noopener noreferrer" href="https://bugs.example.test/t/9"')
  })
})
