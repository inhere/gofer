import { describe, expect, it } from 'vitest'
import app from './App.vue?raw'
import navTabUtil from './utils/navTab.ts?raw'

describe('phone nav edge tab wiring', () => {
  it('draws an edge tab instead of the bottom-left floating button', () => {
    expect(app).toContain('class="rail-toggle"')
    // 旧结构（左下角固定圆按钮 + 工作台抬高）整块撤掉，不再留死代码。
    expect(app).not.toContain('rail-toggle--lifted')
    expect(app).not.toMatch(/bottom: calc\(16px/)
  })

  it('drags with pointer events and only counts a move past the threshold', () => {
    for (const handler of [
      '@pointerdown="onTabPointerDown"',
      '@pointermove="onTabPointerMove"',
      '@pointerup="onTabPointerUp"',
      '@pointercancel="onTabPointerCancel"',
    ]) {
      expect(app, handler).toContain(handler)
    }
    expect(app).toContain('dragPassedThreshold(grab.startX, grab.startY, e.clientX, e.clientY)')
    expect(app).toContain('touch-action: none;')
    // 松手后吸附到最近一侧，并把 side + pct 写进 localStorage。
    expect(app).toContain('const pos = posFromPoint(point.x, point.y, window.innerWidth, window.innerHeight)')
    expect(app).toContain('writeNavTab(pos, defaultNavTabStore())')
    expect(app).toContain('const navTab = ref<NavTabPos>(readNavTab(defaultNavTabStore()))')
  })

  it('follows the viewport and keeps the position inside its bounds', () => {
    expect(app).toContain('const tabTop = computed(() => pctToTop(navTab.value.pct, viewportH.value))')
    expect(app).toContain("window.addEventListener('resize', syncViewportH)")
    expect(app).toContain("window.removeEventListener('resize', syncViewportH)")
    expect(navTabUtil).toContain('export const NAV_TAB_KEY = \'gofer.navTab\'')
    expect(navTabUtil).toContain('export const NAV_TAB_DRAG_THRESHOLD = 6')
  })

  it('steps aside while typing, scrolling, or while the nav drawer is open', () => {
    expect(app).toContain('const tabAway = computed(() => drawerOpen.value || typing.value || scrolling.value)')
    expect(app).toContain("'rail-toggle--away': tabAway")
    expect(app).toContain('isTypingTarget(e.target as HTMLElement | null)')
    expect(app).toContain("window.addEventListener('focusin', onFocusIn)")
    expect(app).toContain("window.addEventListener('focusout', onFocusOut)")
    expect(app).toContain("window.addEventListener('scroll', onScroll, { passive: true, capture: true })")
    expect(app).toContain('NAV_TAB_SCROLL_RESTORE_MS')
    expect(app).toMatch(/\.rail-toggle--away \{[\s\S]*?pointer-events: none;/)
  })

  it('stays above the drawer, below the bells, and works from the keyboard', () => {
    expect(app).toMatch(/\.rail-toggle \{[\s\S]*?z-index: 40;/)
    expect(app).toContain(':aria-label="drawerOpen ? \'关闭主导航\' : \'打开主导航\'"')
    expect(app).toContain(':aria-expanded="drawerOpen"')
    // 键盘 Enter / Space 产生的 click 的 detail 为 0；指针产生的（detail >= 1）交给 pointerup。
    expect(app).toContain('if (e.detail !== 0) return')
  })

  it('clamps the tab inside its vertical bounds while it is being dragged', () => {
    expect(app).toContain('const y = clampTabTop(p.y, viewportH.value)')
    expect(navTabUtil).toContain('export const NAV_TAB_BOTTOM_INSET = 200')
  })

  it('drops the fade animation when the system asks for reduced motion', () => {
    expect(app).toMatch(/@media \(prefers-reduced-motion: reduce\) \{\s*\.rail-toggle \{\s*transition: none;/)
  })
})
