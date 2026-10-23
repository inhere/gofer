import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ExperimentalBadge from './ExperimentalBadge.vue'

const src = Object.values(
  import.meta.glob('./ExperimentalBadge.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
// vitest 不处理 css，颜色/断点直接读 SFC 源码里的 <style>。
const css = src.slice(src.indexOf('<style'))

async function render(props: Record<string, unknown> = {}): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(ExperimentalBadge, props) }))
}

describe('ExperimentalBadge', () => {
  it('shows 「实验性」 with the hover hint and a test hook', async () => {
    const html = await render()
    expect(html).toContain('>实验性<')
    expect(html).toContain('data-test="experimental-badge"')
    expect(html).toContain('title="此功能仍在调整，行为与配置可能变化"')
  })

  it('lets the caller override the label and the hint', async () => {
    const html = await render({ label: '实验性（新）', hint: '还在改' })
    expect(html).toContain('>实验性（新）<')
    expect(html).toContain('title="还在改"')
    expect(html).not.toContain('此功能仍在调整')
  })

  it('takes its colors from tokens.css and stays one line on a phone', () => {
    expect(css).toContain('var(--run)')
    expect(css).toContain('var(--r-chip)')
    expect(css).toContain('white-space: nowrap')
    expect(css).toContain('@media (max-width: 640px)')
    // 不写死色值（浅色主题由 tokens.css 覆盖同名变量）。
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(css).not.toContain('rgb(')
    // 只是注脚：不抢焦点、不拦点击。
    expect(src).not.toContain('tabindex')
    expect(src).not.toContain('vue-router')
  })
})
