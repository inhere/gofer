// 计划描述按 markdown 渲染（gofer-46wp）：详情页头部的描述改用 MarkdownBlock，与待办备注
// 同一套开关——breaks 保住单换行、externalLinks 让链接在新标签页打开；渲染仍经 DOMPurify
// 消毒，所以描述里夹带的 <script> 会先被过滤再进 DOM。
//
// 测试环境没有 DOM，DOMPurify 的 sanitize 需要 window：这里用一个按它的契约工作的替身
// （剥掉脚本块），并顺带守住 MarkdownBlock 真的把 marked 的产物交给了 sanitize。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import MarkdownBlock from '../components/MarkdownBlock.vue'

const sanitize = vi.hoisted(() =>
  vi.fn((html: unknown) => String(html ?? '').replace(/<script[\s\S]*?<\/script>/gi, '')),
)
vi.mock('dompurify', () => ({ default: { sanitize } }))

const raw = (mods: Record<string, unknown>): string => Object.values(mods)[0] as string
const detail = raw(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const block = raw(import.meta.glob('../components/MarkdownBlock.vue', { eager: true, query: '?raw', import: 'default' }))

async function render(props: Record<string, unknown>): Promise<string> {
  const app = createSSRApp({ render: () => h(MarkdownBlock, props as never) })
  return renderToString(app)
}

describe('plan description renders markdown (gofer-46wp)', () => {
  it('feeds the description to MarkdownBlock with breaks and external links', () => {
    // 头部描述不再用纯文本插值，也不再自己拼 HTML。
    expect(detail).not.toContain('{{ plan.description }}')
    expect(detail).toContain('<MarkdownBlock v-if="plan.description" class="head-desc" :text="plan.description" breaks external-links />')
    // 换行改由 breaks 负责，pre-wrap 会把渲染产物的源码换行变成额外空行。
    const style = detail.slice(detail.indexOf('.head-desc {'))
    expect(style.slice(0, style.indexOf('}'))).not.toContain('pre-wrap')
  })

  it('keeps the line breaks of a multi-line description', async () => {
    const html = await render({ text: '第一行\n第二行\n\n- 一条\n- 两条', breaks: true, externalLinks: true })
    expect(html).toContain('第一行<br>第二行')
    expect(html).toContain('<li>一条</li>')
  })

  it('opens a link in a new tab', async () => {
    const html = await render({ text: '见 [缺陷](https://bugs.example.test/t/9)', breaks: true, externalLinks: true })
    expect(html).toContain('<a target="_blank" rel="noopener noreferrer" href="https://bugs.example.test/t/9"')
  })

  it('drops a script tag before it reaches the DOM', async () => {
    const html = await render({ text: '看图\n<script>alert(1)</script>\n结束', breaks: true, externalLinks: true })
    expect(sanitize).toHaveBeenCalled()
    expect(html).not.toContain('<script')
    expect(html).not.toContain('alert(1)')
    expect(html).toContain('结束')
    // 替身代表真实行为：MarkdownBlock 的产物确实经过 DOMPurify.sanitize。
    expect(block).toContain('DOMPurify.sanitize')
  })
})
