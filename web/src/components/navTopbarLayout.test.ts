// 顶栏两个小调整的版式断言（?raw 源码级）：
//  - 「更多 ▾」面板是横向的：一个分组一行，行首组标题 + 链接横排，放不下换行，宽度不越过视口；
//  - 信箱按钮宽屏带「信箱」文字、窄屏只留图标（与「随手记」同一断点），图标尺寸与相邻按钮一致。
// 行为类断言（键盘 / 收起 / 轮询 / 徽标）在 components/navTopbar.test.ts。
import { describe, expect, it } from 'vitest'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const navMore = raw(import.meta.glob('./NavMore.vue', { eager: true, query: '?raw', import: 'default' }))
const mailBtn = raw(import.meta.glob('./MailButton.vue', { eager: true, query: '?raw', import: 'default' }))
const qc = raw(import.meta.glob('./QuickCapture.vue', { eager: true, query: '?raw', import: 'default' }))

// 取某条规则的样式块（从选择器到它的右花括号；这两处规则体里没有嵌套块）。
function rule(src: string, selector: string): string {
  const i = src.indexOf(selector)
  if (i < 0) throw new Error(`missing ${selector}`)
  return src.slice(i, src.indexOf('}', i) + 1)
}

// 隐藏某个 label 的媒体查询断点（例如「随手记」的 .qc-label）。
function labelBreakpoint(src: string, label: string): string | undefined {
  return new RegExp(`@media \\(max-width: (\\d+px)\\) \\{\\s*\\.${label} \\{\\s*display: none;`).exec(src)?.[1]
}

describe('the 更多 panel is laid out horizontally', () => {
  it('renders one row per group: title first, then the links in a row', () => {
    expect(navMore).toContain('<div v-for="group in navMoreGroups" :key="group.label" class="nav-more-row" data-test="nav-more-row">')
    expect(navMore).toContain('<p class="nav-more-title">{{ group.label }}</p>')
    expect(navMore).toContain('<div class="nav-more-links">')
    // 组标题与链接同在一个 flex 行里（标题 flex: none，链接放在自己的横排容器里）。
    expect(rule(navMore, '.nav-more-row {')).toContain('display: flex')
    expect(rule(navMore, '.nav-more-title {')).toContain('flex: none')
    expect(rule(navMore, '.nav-more-links {')).toContain('display: flex')
    // 不再竖排（旧版是 block 链接一列）。
    expect(navMore).not.toContain('flex-direction: column')
  })

  it('sizes the panel to its content but keeps it inside the viewport, wrapping when needed', () => {
    const panel = rule(navMore, '.nav-more-panel {')
    expect(panel).toContain('width: max-content')
    expect(panel).toContain('max-width: calc(100vw - 28px)')
    // 行内与链接容器都能换行：放不下时链接自动掉到下一行。
    expect(rule(navMore, '.nav-more-row {')).toContain('flex-wrap: wrap')
    expect(rule(navMore, '.nav-more-links {')).toContain('flex-wrap: wrap')
    // 单个链接不拆字。
    expect(rule(navMore, '.nav-more-item {')).toContain('white-space: nowrap')
    // 面板比触发按钮宽得多，居中挂在它下面才不会越出右边界。
    expect(panel).toContain('left: 50%')
    expect(panel).toContain('transform: translateX(-50%)')
  })

  it('keeps the console look (tokens only)', () => {
    expect(navMore).toContain('var(--panel)')
    expect(navMore).toContain('var(--line)')
    expect(navMore).toContain('var(--queue)')
    expect(navMore).toContain('var(--paper)')
    expect(navMore).toContain('var(--phosphor)')
  })
})

describe('the mailbox button carries a label on wide screens', () => {
  it('shows ✉ 信箱 and hides the text on narrow screens', () => {
    expect(mailBtn).toContain('<span class="mail-label">信箱</span>')
    // 与「随手记」（.qc-label）同断点隐藏。
    const mailBp = labelBreakpoint(mailBtn, 'mail-label')
    const qcBp = labelBreakpoint(qc, 'qc-label')
    expect(qcBp).toBeTruthy()
    expect(mailBp).toBe(qcBp)
  })

  it('matches the 随手记 button box so the icon is not smaller than its neighbours', () => {
    const mail = rule(mailBtn, '.mail-btn {')
    const qcBtn = rule(qc, '.qc-btn {')
    for (const decl of ['padding: 4px 9px', 'font-size: 12px', 'line-height: 1.2', 'gap: 6px']) {
      expect(qcBtn).toContain(decl)
      expect(mail).toContain(decl)
    }
    // 窄屏一起放大到 14px（「随手记」在同一个断点这么做），只改这两个按钮。
    const narrow = mailBtn.slice(mailBtn.indexOf('@media (max-width: 768px)'))
    expect(narrow).toContain('font-size: 14px')
    expect(narrow).toContain('padding: 4px 10px')
    expect(qc).toContain('font-size: 14px')
    expect(qc).toContain('padding: 4px 10px')
  })
})
