// 图标的接线测试（issue gofer-h355）：尺寸档位与 token 一致、导航项都有图标与可访问名、
// Icon 的无障碍行为（装饰性 aria-hidden / 有 label 才是 role=img）。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { Search } from '@lucide/vue'
import Icon from './Icon.vue'
import { ICON_PX, ICON_STROKE, NAV_ICONS, iconPx, navIcon } from './icon'
import { SHELL_NAV_GROUPS, TAB_BAR_ITEMS, moreItems } from '../utils/shell'
import { dicts } from '../i18n'

// 尺寸档位与 styles/tokens.css 的 --icon-sm / --icon / --icon-lg 是同一组数值：
// 组件里要给 lucide 传像素数，CSS 变量在 media query 与 svg 属性上用不上。
// 这里把数字钉住，改 token 时两侧一起改（vitest 拿不到 .css 原文，读不了那份文件）。
const EXPECTED_PX = { sm: 14, md: 16, lg: 20 }

async function render(props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(Icon as never, props as never) }))
}

describe('icon sizes', () => {
  it('mirrors the size tokens in styles/tokens.css', () => {
    expect(ICON_PX).toEqual(EXPECTED_PX)
  })

  it('maps a size name or an explicit pixel count', () => {
    expect(iconPx('sm')).toBe(14)
    expect(iconPx('lg')).toBe(20)
    expect(iconPx(18)).toBe(18)
    expect(iconPx()).toBe(ICON_PX.md)
  })
})

describe('navigation icons', () => {
  const items = [...SHELL_NAV_GROUPS.flatMap((g) => g.items), ...TAB_BAR_ITEMS, ...moreItems()]

  it('has at least the 16 sidebar pages', () => {
    expect(new Set(items.map((it) => it.to)).size).toBeGreaterThanOrEqual(16)
  })

  it('gives every navigation item a resolvable lucide icon', () => {
    for (const item of items) {
      expect(NAV_ICONS[item.icon], `${item.to} 的图标名 ${item.icon} 没登记`).toBeTruthy()
      expect(typeof navIcon(item.icon), `${item.to} 的图标组件`).toBe('function')
    }
  })

  it('gives every navigation item an accessible name in both dictionaries', () => {
    for (const item of items) {
      expect(dicts['zh-CN'][item.key], `${item.key} 缺中文`).toBeTruthy()
      expect(dicts['en-US'][item.key], `${item.key} 缺英文`).toBeTruthy()
    }
  })

  it('keeps every icon name in the semantic registry', () => {
    // 名字是 NavIcon 联合类型里的键：多一个少一个都会在这里露出来
    expect(Object.keys(NAV_ICONS).length).toBe(new Set(items.map((it) => it.icon)).size + 1) // + 'more'
    expect(NAV_ICONS.more).toBeTruthy()
  })
})

describe('Icon', () => {
  it('renders a lucide svg at the requested size and the shared stroke width', async () => {
    const html = await render({ icon: Search, size: 'lg' })
    expect(html).toContain('class="lucide lucide-search icon"')
    expect(html).toContain('width="20"')
    expect(html).toContain(`stroke-width="${ICON_STROKE}"`)
    // 颜色跟随文字
    expect(html).toContain('stroke="currentColor"')
  })

  it('is decorative by default', async () => {
    const html = await render({ icon: Search })
    expect(html).toContain('aria-hidden="true"')
    expect(html).not.toContain('role="img"')
  })

  it('becomes a labelled image when it carries a name', async () => {
    const html = await render({ icon: Search, label: '搜索页面', title: '搜索页面' })
    expect(html).toContain('role="img"')
    expect(html).toContain('aria-label="搜索页面"')
    expect(html).toContain('title="搜索页面"')
    expect(html).not.toContain('aria-hidden="true"')
  })

  it('can spin for the running state', async () => {
    expect(await render({ icon: Search, spin: true })).toContain('icon--spin')
  })
})
