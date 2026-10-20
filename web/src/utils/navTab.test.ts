import { describe, expect, it } from 'vitest'
import {
  NAV_TAB_BOTTOM_INSET,
  NAV_TAB_DEFAULT,
  NAV_TAB_KEY,
  NAV_TAB_TOP_INSET,
  clampTabTop,
  dragPassedThreshold,
  isTypingTarget,
  parseNavTab,
  pctToTop,
  posFromPoint,
  readNavTab,
  snapSide,
  topToPct,
  writeNavTab,
  type NavTabStore,
} from './navTab'

function fakeStore(initial?: string, throwOn?: 'get' | 'set'): NavTabStore & { data: Map<string, string> } {
  const data = new Map<string, string>()
  if (initial !== undefined) data.set(NAV_TAB_KEY, initial)
  return {
    data,
    getItem(key: string) {
      if (throwOn === 'get') throw new Error('denied')
      return data.has(key) ? (data.get(key) as string) : null
    },
    setItem(key: string, value: string) {
      if (throwOn === 'set') throw new Error('quota')
      data.set(key, value)
    },
  }
}

describe('nav edge tab position', () => {
  it('falls back to the left edge at 45% of the viewport', () => {
    expect(NAV_TAB_DEFAULT).toEqual({ side: 'left', pct: 45 })
    expect(NAV_TAB_KEY).toBe('gofer.navTab')
  })

  it('keeps the vertical position between the topbar and the bottom input area', () => {
    expect(clampTabTop(-100, 800)).toBe(NAV_TAB_TOP_INSET)
    expect(clampTabTop(0, 800)).toBe(NAV_TAB_TOP_INSET)
    expect(clampTabTop(400, 800)).toBe(400)
    expect(clampTabTop(900, 800)).toBe(800 - NAV_TAB_BOTTOM_INSET)
    // 视口比「顶栏 + 底部让位」还矮时退到上沿，不会算出负数或倒挂的区间。
    expect(clampTabTop(50, 120)).toBe(NAV_TAB_TOP_INSET)
    expect(clampTabTop(Number.NaN, 800)).toBe(NAV_TAB_TOP_INSET)
  })

  it('round-trips a percentage through the clamped pixel top', () => {
    expect(pctToTop(45, 1000)).toBe(450)
    expect(topToPct(450, 1000)).toBe(45)
    // 越界百分比读回来也被夹到可用区间。
    expect(topToPct(10, 800)).toBe(7)
    expect(topToPct(790, 800)).toBe(75)
    expect(topToPct(450, 0)).toBe(NAV_TAB_DEFAULT.pct)
  })

  it('pulls a position saved under the old lower boundary back into the input-area-free zone', () => {
    // 存下的 90%：800px 高的视口里是 720px，已经落进底部 200px 的让位区。
    expect(pctToTop(90, 800)).toBe(800 - NAV_TAB_BOTTOM_INSET)
    expect(pctToTop(90, 800)).toBe(600)
    // 旧版本默认 60%（480px）在区间内，原样保留。
    expect(pctToTop(60, 800)).toBe(480)
  })

  it('snaps to the nearer edge', () => {
    expect(snapSide(10, 400)).toBe('left')
    expect(snapSide(199, 400)).toBe('left')
    expect(snapSide(200, 400)).toBe('right')
    expect(snapSide(390, 400)).toBe('right')
    expect(snapSide(Number.NaN, 400)).toBe('left')
    expect(snapSide(10, 0)).toBe('left')
  })

  it('only counts a move past 6px as a drag', () => {
    expect(dragPassedThreshold(100, 100, 100, 100)).toBe(false)
    expect(dragPassedThreshold(100, 100, 106, 100)).toBe(false)
    expect(dragPassedThreshold(100, 100, 107, 100)).toBe(true)
    expect(dragPassedThreshold(100, 100, 100, 94)).toBe(false)
    // 斜着走：3-4-5 的三倍（9 / 12）距离 15px，早已过阈值。
    expect(dragPassedThreshold(100, 100, 109, 112)).toBe(true)
  })

  it('turns a release point into a side and a clamped percentage', () => {
    // 从左边拖到右边中部：贴右边缘，垂直位置原样保留。
    expect(posFromPoint(300, 400, 400, 800)).toEqual({ side: 'right', pct: 50 })
    // 拖到顶部 / 底部：百分比被夹在可用区间。
    expect(posFromPoint(20, 4, 400, 800)).toEqual({ side: 'left', pct: 7 })
    expect(posFromPoint(20, 795, 400, 800)).toEqual({ side: 'left', pct: 75 })
    // 底部让位区（视口高度 − 200px 以下）松手也不进输入区。
    expect(posFromPoint(20, 700, 400, 800)).toEqual({ side: 'left', pct: 75 })
  })

  it('parses a stored position and rejects broken or out-of-range values', () => {
    expect(parseNavTab('{"side":"right","pct":42}')).toEqual({ side: 'right', pct: 42 })
    expect(parseNavTab('{"side":"left","pct":0}')).toEqual({ side: 'left', pct: 0 })
    for (const bad of [
      '',
      null,
      undefined,
      'not json',
      '42',
      'null',
      '{"side":"top","pct":42}',
      '{"pct":42}',
      '{"side":"left"}',
      '{"side":"left","pct":"42"}',
      '{"side":"left","pct":-1}',
      '{"side":"left","pct":101}',
      '{"side":"left","pct":null}',
    ]) {
      expect(parseNavTab(bad as string | null | undefined), String(bad)).toEqual(NAV_TAB_DEFAULT)
    }
  })

  it('reads and writes localStorage, surviving a denied store', () => {
    const store = fakeStore()
    expect(readNavTab(store)).toEqual(NAV_TAB_DEFAULT)
    writeNavTab({ side: 'right', pct: 33.3 }, store)
    expect(store.data.get(NAV_TAB_KEY)).toBe('{"side":"right","pct":33.3}')
    expect(readNavTab(store)).toEqual({ side: 'right', pct: 33.3 })

    expect(readNavTab(fakeStore(undefined, 'get'))).toEqual(NAV_TAB_DEFAULT)
    expect(readNavTab(null)).toEqual(NAV_TAB_DEFAULT)
    expect(() => writeNavTab({ side: 'left', pct: 60 }, fakeStore(undefined, 'set'))).not.toThrow()
    expect(() => writeNavTab({ side: 'left', pct: 60 }, null)).not.toThrow()
  })

  it('treats input, textarea and contenteditable as typing', () => {
    expect(isTypingTarget({ tagName: 'INPUT' })).toBe(true)
    expect(isTypingTarget({ tagName: 'textarea' })).toBe(true)
    expect(isTypingTarget({ tagName: 'DIV', isContentEditable: true })).toBe(true)
    expect(isTypingTarget({ tagName: 'BUTTON' })).toBe(false)
    expect(isTypingTarget({ tagName: 'SELECT' })).toBe(false)
    expect(isTypingTarget(null)).toBe(false)
    expect(isTypingTarget(undefined)).toBe(false)
  })
})
