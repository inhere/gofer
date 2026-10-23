// P6-3（issue gofer-hez1 第 3 项）：工作台右上角的置顶按钮原先是一个纯文本 ⌖ 小字形，用户
// 要悬停才知道是什么。现在：统一 Icon 尺寸、桌面带「置顶 / 取消置顶」文字、手机只留图标但
// aria-label / title 不变，已置顶有明显的选中样式。渲染 ThreadView 需要一整套 context 注入，
// 这里按本目录既有口径读源码断言（同 threadViewSessionComposer.test.ts）。
import { describe, expect, it } from 'vitest'

const thread = Object.values(import.meta.glob('./ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('workbench 置顶按钮', () => {
  it('用统一 Icon（不再是纯文本字形）且带文字标签', () => {
    expect(thread).toContain("import { Ellipsis, Pin, Square } from '@lucide/vue'")
    expect(thread).toContain('<Icon :icon="Pin" size="sm"')
    expect(thread).toContain('class="pin-text"')
    expect(thread).not.toContain('>⌖</button>')
  })

  it('桌面带文字、手机只留图标（aria-label / title 始终有）', () => {
    expect(thread).toContain('class="btn sm pin-btn"')
    expect(thread).toContain(":title=\"thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin')\"")
    expect(thread).toContain(":aria-label=\"thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin')\"")
    expect(thread).toContain(':aria-pressed="!!thread.pinned"')
    // ≤760 只收掉文字，点击目标仍与同排图标按钮一致（32×32）。
    expect(thread).toMatch(/@media \(max-width: 760px\) \{\s*\.pin-btn \{[\s\S]*?height: 32px;[\s\S]*?\.pin-btn \.pin-text \{\s*display: none;/)
  })

  it('已置顶有选中样式，不只靠悬停', () => {
    expect(thread).toContain(':class="{ on: !!thread.pinned }"')
    expect(thread).toMatch(/\.pin-btn\.on \{[^}]*color: var\(--accent\)/)
    expect(thread).toMatch(/\.pin-btn\.on \{[^}]*background: var\(--accent-soft\)/)
  })

  it('手机菜单里的置顶入口还在（不丢操作）', () => {
    expect(thread).toContain("{{ thread.pinned ? t('workbench.head.unpin') : t('workbench.head.pin') }}")
  })
})
