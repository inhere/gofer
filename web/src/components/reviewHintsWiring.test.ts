import { describe, expect, it } from 'vitest'

// 接线断言：验收面板「汇报」页签顶部挂着折叠的「审阅提示」卡片，数据来自
// /v1/jobs/{id}/review-hints（服务端现算，CLI job review 同源）。用 ?raw 读源文件，
// 不渲染组件。
function raw(modules: Record<string, unknown>): string {
  return Object.values(modules)[0] as string
}

const panel = raw(import.meta.glob('./ReviewPanel.vue', { eager: true, query: '?raw', import: 'default' }))
const api = raw(import.meta.glob('../api/reviewHints.ts', { eager: true, query: '?raw', import: 'default' }))

describe('review hints wiring', () => {
  it('fetches the hints from the server endpoint', () => {
    expect(api).toContain('/review-hints')
    expect(api).toContain('review_hints')
    expect(panel).toContain("import { fetchReviewHints } from '../api/reviewHints'")
    expect(panel).toContain('fetchReviewHints(id)')
  })

  it('renders a collapsed card with the three sections in the report tab', () => {
    expect(panel).toContain('<details v-if="hasReviewHints(hints)"')
    expect(panel).toContain('hints?.deletion_hotspots?.length')
    expect(panel).toContain('hints?.removed_comments?.length')
    expect(panel).toContain('hints?.verify_lines?.length')
    expect(panel).toContain('审阅提示')
  })

  it('loads the hints together with the report, and never blocks it', () => {
    expect(panel).toContain('void loadHints()')
    expect(panel).toContain('hints.value = null')
  })
})
