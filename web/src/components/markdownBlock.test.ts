// MarkdownBlock 的两个可选渲染开关（待办备注用）：
//  - 默认仍按标准 markdown 渲染（单换行并成一段、链接不加 target），其它用法（产物预览、
//    汇报）行为不变；
//  - breaks 保住「一行一条」的换行，externalLinks 让裸 URL（缺陷系统等）可点并在新标签页打开。
//
// 测试环境没有 DOM：DOMPurify 这里用一个等价的危险内容过滤桩，覆盖服务端渲染时的
// 消毒回退；浏览器 DOM 改写由阅读页运行路径使用同一消毒顺序。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import MarkdownBlock from './MarkdownBlock.vue'

vi.mock('dompurify', () => ({
  default: {
    sanitize: (html: string) => html
      .replace(/<script[^>]*>[\s\S]*?<\/script>/gi, '')
      .replace(/\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '')
      .replace(/javascript:/gi, ''),
  },
}))

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

  it('keeps document-looking markdown unchanged without a document context', async () => {
    const html = await render({ text: '[x](docs/a.md)\n\n`docs/a.md`\n\n```\ndocs/a.md\n```\n\n![i](a.png)' })
    expect(html).toContain('href="docs/a.md"')
    expect(html).toContain('<code>docs/a.md</code>')
    expect(html).toContain('<pre><code>docs/a.md')
    expect(html).toContain('<img')
  })

  it('sanitizes dangerous markdown with and without document context', async () => {
    const text = '<script>alert(1)</script> [x](javascript:alert(1)) <a href="javascript:bad()" onerror="x">x</a> <img src="x" onerror="bad()">'
    for (const extra of [{}, { docContext: { projectKey: 'demo' } }]) {
      const html = await render({ text, ...extra })
      expect(html).not.toContain('script')
      expect(html).not.toContain('javascript:')
      expect(html).not.toMatch(/\son[a-z]+=/i)
    }
  })
})
