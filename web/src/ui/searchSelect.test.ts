// 可搜索下拉（issue gofer-b8nh）：过滤 / 排序纯逻辑 + 组件 SSR 与接线断言。
// 项目里没有 DOM 测试环境（只做 SSR），所以键盘 / 焦点这些可判定的部分都断言源码接线。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import SearchSelect from './SearchSelect.vue'
import { filterOptions, initialIndex, matchRank, type SearchOption } from './searchSelect'

const options: SearchOption[] = [
  { value: 'gofer', label: 'gofer' },
  { value: 'gofer-web', label: 'gofer-web' },
  { value: 'my-tools-dev', label: 'my-tools-dev' },
  { value: 'acme-notes', label: 'acme-notes' },
  { value: 'WebFront', label: 'WebFront' },
]

function values(list: SearchOption[]): string[] {
  return list.map((o) => o.value)
}

async function render(props: Record<string, unknown>): Promise<string> {
  const app = createSSRApp({ render: () => h(SearchSelect as never, props) })
  return renderToString(app)
}

describe('filterOptions（项目 key / 显示名的可搜索过滤）', () => {
  it('returns everything for an empty query, in the original order', () => {
    expect(values(filterOptions(options, ''))).toEqual(values(options))
    expect(values(filterOptions(options, '   '))).toEqual(values(options))
  })

  it('matches case-insensitively and by substring', () => {
    // 大小写不敏感；前缀优先（WebFront 以 web 开头，gofer-web 只是词首命中）
    expect(values(filterOptions(options, 'WEB'))).toEqual(['WebFront', 'gofer-web'])
    expect(values(filterOptions(options, 'webfront'))).toEqual(['WebFront'])
    expect(values(filterOptions(options, 'tools'))).toEqual(['my-tools-dev'])
    expect(values(filterOptions(options, 'notes'))).toEqual(['acme-notes'])
  })

  it('puts exact matches first, then prefixes, word starts and the rest', () => {
    // exact → 0
    expect(matchRank({ value: 'gofer', label: 'gofer' }, 'gofer')).toBe(0)
    // prefix → 1（gofer 与 gofer-web 都是前缀；完全相等的排最前）
    expect(values(filterOptions(options, 'gofer'))).toEqual(['gofer', 'gofer-web'])
    // 词首 → 2：dev 在 my-tools-dev 的第 3 段
    expect(values(filterOptions(options, 'dev'))).toEqual(['my-tools-dev'])
    expect(matchRank({ value: 'my-tools-dev', label: 'my-tools-dev' }, 'tools')).toBe(2)
    // 其它包含 → 3
    expect(matchRank({ value: 'my-tools-dev', label: 'my-tools-dev' }, 'ols')).toBe(3)
    // 不匹配 → -1
    expect(matchRank({ value: 'gofer', label: 'gofer' }, 'zzz')).toBe(-1)
    expect(values(filterOptions(options, 'zzz'))).toEqual([])
  })

  it('searches the value and the extra keywords as well', () => {
    const withHint: SearchOption[] = [
      { value: 'p-1', label: '项目一', keywords: 'xm y' },
      { value: 'p-2', label: '项目二', keywords: 'xme' },
    ]
    expect(values(filterOptions(withHint, 'xm'))).toEqual(['p-1', 'p-2'])
    expect(values(filterOptions(withHint, 'xme'))).toEqual(['p-2'])
    expect(values(filterOptions(withHint, 'p-1'))).toEqual(['p-1'])
  })

  it('starts on the current value, or the first item when it is not listed', () => {
    expect(initialIndex(options, 'my-tools-dev')).toBe(2)
    expect(initialIndex(options, 'gone')).toBe(0)
    expect(initialIndex([], 'gone')).toBe(-1)
  })
})

describe('SearchSelect 渲染与接线', () => {
  it('renders a closed listbox trigger with the current label', async () => {
    const html = await render({
      modelValue: 'gofer',
      options,
      label: '项目',
      testId: 'pf',
      triggerId: 'pf-trigger',
    })
    expect(html).toContain('data-test="pf"')
    expect(html).toContain('data-test="pf-trigger"')
    expect(html).toContain('id="pf-trigger"')
    expect(html).toContain('aria-haspopup="listbox"')
    expect(html).toContain('aria-expanded="false"')
    expect(html).toContain('aria-label="项目"')
    expect(html).toContain('>gofer<')
    // 关闭时不渲染搜索框与列表
    expect(html).not.toContain('role="listbox"')
    expect(html).not.toContain('data-test="pf-search"')
  })

  it('falls back to the placeholder and marks the trigger disabled', async () => {
    const html = await render({ modelValue: '', options: [], placeholder: '全部项目', disabled: true })
    expect(html).toContain('>全部项目<')
    expect(html).toContain('disabled')
    expect(html).toContain('data-test="search-select-trigger"')
  })

  it('opens a focused search box, filters and keeps keyboard selection wired', () => {
    const source = Object.values(
      import.meta.glob('./SearchSelect.vue', { eager: true, query: '?raw', import: 'default' }),
    )[0] as string
    // 打开即聚焦搜索框并清空上次输入
    expect(source).toContain('search.value?.focus()')
    expect(source).toContain("query.value = ''")
    // 过滤走纯逻辑
    expect(source).toContain('filterOptions(props.options, query.value)')
    // 键盘：↑ ↓ 循环、Enter 选中、Esc 关闭并归还焦点、Home / End 跳首尾、Tab 收起
    for (const key of ["'ArrowDown'", "'ArrowUp'", "'Enter'", "'Escape'", "'Home'", "'End'", "'Tab'"]) {
      expect(source).toContain(key)
    }
    expect(source).toContain('nextEnabledIndex(filtered.value, active.value, delta)')
    // Esc 不能透传出去（否则会把父级抽屉一起关掉）
    expect(source).toContain('e.stopPropagation()')
    expect(source).toContain('trigger.value?.focus()')
    // 点面板外关闭
    expect(source).toContain("document.addEventListener('pointerdown', onDocPointerDown)")
    // 无障碍：combobox + listbox + option
    expect(source).toContain('role="combobox"')
    expect(source).toContain('role="listbox"')
    expect(source).toContain('role="option"')
    expect(source).toContain(':aria-selected="option.value === modelValue"')
    // 空态 / 无匹配
    expect(source).toContain('data-test="search-select-no-match"')
    expect(source).toContain('data-test="search-select-empty"')
    // 手机：面板贴底
    expect(source).toContain('@media (max-width: 760px)')
    expect(source).toContain('position: fixed')
  })
})
