// 手机（≤760）下设置区的二级导航收成下拉框（设计稿 §7.4）：竖排导航隐藏，下拉框占一行。
// 断点与外壳一致（AppShell 用 760）；选中即跳转的行为在这里钉住，形态在 CSS 里。
import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('Settings mobile navigation', () => {
  it('swaps the side navigation for a dropdown on phones', () => {
    const block = source.slice(source.indexOf('@media (max-width: 760px)'))
    expect(block).toMatch(/\.set-nav \{\s*display: none/)
    expect(block).toMatch(/\.set-select-wrap \{\s*display: block/)
  })

  it('navigates when a section is picked from the dropdown', () => {
    expect(source).toContain('@change="onPick"')
    expect(source).toContain('router.push(to)')
    // 下拉框在桌面上隐藏但仍然在 DOM 里（读屏器与测试都能取到）
    expect(source).toMatch(/\.set-select-wrap \{\s*display: none/)
  })

  it('uses the same phone breakpoint as the shell', () => {
    expect(source).toContain('@media (max-width: 760px)')
  })
})
