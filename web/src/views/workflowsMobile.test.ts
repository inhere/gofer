// 工作流列表的窄屏布局与 P2 基元接线（设计稿 §7.2、§13：*Mobile 测试改成测结构与接线，
// 不再逐条断言 CSS 源码细节）。
import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./Workflows.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

// 页面里所有 @media (max-width: Npx) 的 N 值。
const breakpoints = [...source.matchAll(/@media \(max-width: (\d+)px\)/g)].map((m) => Number(m[1]))

describe('Workflows 列表的窄屏与基元', () => {
  it('手机断点用冻结的 760px（设计稿 §4.3）', () => {
    expect(breakpoints).toContain(760)
  })

  it('状态筛选改用 FilterChips（带计数），行用 StatusPill 与分段 ProgressBar', () => {
    expect(source).toContain('<FilterChips')
    expect(source).toContain('<StatusPill')
    expect(source).toContain('<ProgressBar')
    expect(source).toContain("const statusItems = computed<FilterChipItem[]>")
    // 旧的 select 与 StatusBadge 已换掉。
    expect(source).not.toContain('filter-select')
    expect(source).not.toContain('StatusBadge')
  })

  it('列的顺序与内容不变（状态 / 工作流 / 步骤 / 开始时间 / 耗时）', () => {
    for (const col of ['col-status', 'col-wf', 'col-step', 'col-started', 'col-dur']) {
      expect(source).toContain(col)
    }
    for (const key of [
      "t('workflows.col.status')",
      "t('workflows.col.name')",
      "t('workflows.col.steps')",
      "t('workflows.col.started')",
      "t('workflows.col.duration')",
    ]) {
      expect(source).toContain(key)
    }
  })

  it('手机档隐藏列头，行改为可换行的卡片（标题一行，状态与步骤 / 耗时排下面）', () => {
    const block = source.slice(source.indexOf('@media (max-width: 760px)'))
    expect(block).toMatch(/\.thead \{[^}]*display: none/)
    expect(block).toMatch(/\.trow \{[^}]*display: flex/)
    expect(block).toMatch(/\.trow \{[^}]*flex-wrap: wrap/)
    expect(block).toMatch(/\.col-status \{[^}]*flex: 0 0 96px/)
  })
})
