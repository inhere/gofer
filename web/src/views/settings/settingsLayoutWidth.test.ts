// P6-4（issue gofer-hez1 第 4 项）：设置页二级导航右侧的内容区原先被 grid 钉死在 720px，
// 1440 / 1920 宽度下右边一大片空白。现在内容列跟随可用宽度（minmax(0, 1fr)），整页宽度
// 交给全站统一的页面容器 .page（web/src/ui/page.css，上限 1440px），不再单独写 1480px；
// 手机布局不变。按 settingsLayoutMobile.test.ts 的 ?raw 口径断言样式源码。
import { describe, expect, it } from 'vitest'

const src = Object.values(
  import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('设置页内容区宽度', () => {
  it('内容列跟随可用宽度，不再限 720px', () => {
    expect(src).toMatch(/\.settings-body \{[\s\S]*?grid-template-columns: 210px minmax\(0, 1fr\);/)
    expect(src).not.toContain('minmax(0, 720px)')
  })

  it('整页走统一页面容器，不再自己写宽度上限', () => {
    // 根元素挂 .page（宽度上限、居中、页边距都在 tokens + ui/page.css 里）
    expect(src).toMatch(/<div class="settings page">/)
    expect(src).not.toMatch(/max-width: 1480px/)
  })

  it('平板与手机断点保持不变', () => {
    // 平板：导航收窄，内容仍然 fluid。
    expect(src).toMatch(/@media \(max-width: 1099px\) \{[\s\S]*?grid-template-columns: 180px minmax\(0, 1fr\);/)
    // 手机：单列 + 下拉导航。
    expect(src).toMatch(/@media \(max-width: 760px\) \{[\s\S]*?grid-template-columns: minmax\(0, 1fr\);/)
    expect(src).toContain('.set-nav {')
    expect(src).toContain('display: none;')
  })
})
