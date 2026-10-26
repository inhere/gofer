// 任务详情「概览」的元信息表（views/JobDetail.vue）：标签列曾经定长 78px 且不可断词，
// REQUIRE_REVIEW / WT_BRANCH / REVIEWED_BY 这类长键直接顶到值上（显示成「REQUIRE_REV需要人验收」，
// issue gofer-shue）。现在标签列按最长键自适应（至少 78px、最多行的 46%、超长断词换行），
// 值列占余下宽度并照旧省略号截断——两者不再重叠。
import { describe, expect, it } from 'vitest'

const raw = (mods: Record<string, unknown>): string => Object.values(mods)[0] as string
const jobDetail = raw(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))

// cssBlock 取某条选择器在 <style> 里的声明块正文。
function cssBlock(selector: string): string {
  const at = jobDetail.indexOf(`\n${selector} {\n`)
  if (at < 0) return ''
  const end = jobDetail.indexOf('}', at)
  return end < 0 ? '' : jobDetail.slice(at, end)
}

describe('job detail overview key/value table', () => {
  it('lets the label column grow with the longest key instead of overlapping the value', () => {
    const label = cssBlock('.meta-k')
    expect(label).toContain('min-width: 78px')
    expect(label).toContain('max-width: 46%')
    expect(label).toContain('overflow-wrap: anywhere')
    // 不再是「定宽 + 不换行」：那正是长键压到值上的原因。
    expect(label).not.toMatch(/^\s+width: 78px;/m)
    expect(label).not.toContain('flex: none')
  })

  it('keeps the value in its own column, truncating rather than colliding', () => {
    const value = cssBlock('.meta-v')
    expect(value).toContain('flex: 1 1 auto')
    expect(value).toContain('min-width: 0')
    expect(value).toContain('text-overflow: ellipsis')
  })

  it('renders the long keys as their own label span (they are what used to overflow)', () => {
    // 同一张表里的长键：require_review / reviewed_by / wt_branch / acceptance / review_note。
    for (const key of ['require_review', 'reviewed_by', 'wt_branch', 'acceptance', 'review_note']) {
      expect(jobDetail).toContain(
        `<span class="meta-k mono">${
          key === 'acceptance' ? 'acceptance' : key
        }</span>`,
      )
    }
  })
})
