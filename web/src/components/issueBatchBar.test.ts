import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import IssueBatchBar from './IssueBatchBar.vue'

describe('IssueBatchBar', () => {
  it('shows the selection count and the three batch actions; no confirm dialog until asked', async () => {
    const html = await renderToString(createSSRApp({ render: () => h(IssueBatchBar, { count: 12, sampleIds: ['a-1', 'a-2'], running: false }) }))
    expect(html).toContain('已选 12 项')
    expect(html).toContain('清空')
    expect(html).toContain('批量关闭')
    expect(html).toContain('改状态')
    expect(html).toContain('加标签')
    expect(html).not.toContain('data-test="batch-confirm"')
  })

  it('disables the actions while a batch is running', async () => {
    const html = await renderToString(createSSRApp({ render: () => h(IssueBatchBar, { count: 3, sampleIds: [], running: true }) }))
    expect(html).toMatch(/data-test="batch-close"[^>]*disabled|disabled[^>]*data-test="batch-close"/)
  })
})
