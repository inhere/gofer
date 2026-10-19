// 手机宽度（≤900px，断点见 utils/mobileLayout.ts 的 MOBILE_MAX_WIDTH）下，工作流列表与
// 任务看板一样改成卡片式：列头隐藏、一行压成两三行，不再让右侧的「步骤 / 耗时」被截断。
import { describe, expect, it } from 'vitest'
import { MOBILE_MAX_WIDTH } from '../utils/mobileLayout'

const source = Object.values(
  import.meta.glob('./Workflows.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

// 页面里所有 @media (max-width: Npx) 的 N 值。
const breakpoints = [...source.matchAll(/@media \(max-width: (\d+)px\)/g)].map((m) => Number(m[1]))

describe('Workflows 手机版卡片布局', () => {
  it('断点用手机档的 900px（与 utils/mobileLayout 同一口径）', () => {
    expect(MOBILE_MAX_WIDTH).toBe(900)
    expect(breakpoints).toContain(MOBILE_MAX_WIDTH)
  })

  it('手机档隐藏列头，行改为可换行的卡片（沿用任务看板的做法）', () => {
    const block = source.slice(source.indexOf(`@media (max-width: ${MOBILE_MAX_WIDTH}px)`))
    expect(block).toContain('display: none')
    expect(block).toMatch(/\.thead \{[^}]*display: none/)
    expect(block).toMatch(/\.trow \{[^}]*display: flex/)
    expect(block).toMatch(/\.trow \{[^}]*flex-wrap: wrap/)
    // 标题/ID 占一行，状态右对齐，步骤与耗时用「 · 」分隔排下一行。
    expect(block).toMatch(/\.col-wf \{[^}]*flex: 1 0 calc\(100% - 96px\)/)
    expect(block).toMatch(/\.col-status \{[^}]*flex: 0 0 96px/)
    expect(block).toContain("content: ' · '")
  })

  it('列的顺序与内容不变（状态 / 工作流 / 步骤 / 耗时）', () => {
    const thead = source.slice(source.indexOf('<div class="thead mono">'), source.indexOf('</div>', source.indexOf('<div class="thead mono">')))
    for (const col of ['col-status', 'col-wf', 'col-step', 'col-dur']) {
      expect(thead).toContain(col)
    }
  })
})
