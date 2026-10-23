// 页面容器（P6 内容区宽度统一，issue gofer-hez1）的规则单测：
// 全站只有 .page / .page--full 这一种页面宽度口径，宽度上限与页边距来自 tokens.css。
// 按源码字符串断言（不渲染），与其它样式用例口径一致。
import { describe, expect, it } from 'vitest'

const pageCss = Object.entries(
  import.meta.glob('../ui/page.css', { eager: true, query: '?raw', import: 'default' }),
).find(([path]) => path.endsWith('page.css'))![1] as string
const tokens = Object.entries(
  import.meta.glob('../styles/tokens.css', { eager: true, query: '?raw', import: 'default' }),
).find(([path]) => path.endsWith('tokens.css'))![1] as string
const main = Object.entries(
  import.meta.glob('../main.ts', { eager: true, query: '?raw', import: 'default' }),
).find(([path]) => path.endsWith('main.ts'))![1] as string

function rule(css: string, selector: string): string {
  const m = new RegExp(`${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} \\{([^}]*)\\}`).exec(css)
  expect(m, `没有找到 ${selector} 规则`).not.toBeNull()
  return m![1]
}

describe('页面容器规则', () => {
  it('tokens 里有页面宽度上限与统一页边距', () => {
    expect(tokens).toMatch(/--page-max:\s*1440px;/)
    expect(tokens).toMatch(/--page-pad-x:/)
    expect(tokens).toMatch(/--page-pad-y:/)
    // 窄屏（≤760）收窄页边距，与内容区原来的手机 padding 一致
    expect(tokens).toMatch(/@media \(max-width: 760px\) \{[^}]*--page-pad-x:/)
  })

  it('.page 上限、居中、统一左右内边距，宽度不足时铺满', () => {
    const page = rule(pageCss, '.page')
    expect(page).toMatch(/width:\s*100%;/)
    expect(page).toMatch(/max-width:\s*var\(--page-max\);/)
    expect(page).toMatch(/margin-inline:\s*auto;/)
    expect(page).toMatch(/padding:\s*var\(--page-pad-y\) var\(--page-pad-x\);/)
  })

  it('.page--full 不受上限约束，也不带页边距（工作台 / 信箱自己贴边布局）', () => {
    const full = rule(pageCss, '.page--full')
    expect(full).toMatch(/max-width:\s*none;/)
    expect(full).toMatch(/padding:\s*0;/)
  })

  it('main.ts 引入页面容器样式（唯一入口）', () => {
    expect(main).toContain("import './ui/page.css'")
  })
})
