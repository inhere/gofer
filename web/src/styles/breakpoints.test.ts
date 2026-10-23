// 断点口径（设计稿 §4.3）：≤760 手机、761–1099 图标栏、≥1280 完整三栏。
//
// 迁移期手机断点散落成 620 / 639 / 640 / 700 / 720 / 767 / 768 / 980 八种，同一个「手机」
// 语义在不同页面上生效宽度不同（DashboardSystem 甚至 700 单列、640 又回到两列）。P6 把它们
// 统一到 760 与 900（900 只留给「图标栏区间的密度调整」这一类中间断点）。
//
// 这个用例防止新页面再把旧的手机断点写回来。内容宽度（如 `.col { max-width: 640px }`）
// 不是断点，不受影响：这里只认 `@media (max-width: …)` 与 matchMedia 里的同一种写法。
import { describe, expect, it } from 'vitest'

const LEGACY_PHONE = /@media \(max-width:\s*(620|639|640|700|720|767|768|980)px\)/
const PHONE = /@media \(max-width:\s*760px\)/

const sources = import.meta.glob(['../**/*.vue', '../**/*.css'], {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

describe('手机断点统一为 760', () => {
  it('没有文件还在用迁移期的手机断点', () => {
    const offenders = Object.entries(sources)
      .filter(([, text]) => LEGACY_PHONE.test(text))
      .map(([path]) => path)
    expect(offenders).toEqual([])
  })

  it('760 确实是手机断点（用例本身不是空转）', () => {
    const phone = Object.entries(sources).filter(([, text]) => PHONE.test(text))
    expect(phone.length).toBeGreaterThan(20)
  })
})
