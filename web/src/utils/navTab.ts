// 手机端主导航「贴边小标签」的位置与拖动判定。
//
// 标签贴在屏幕左 / 右边缘，垂直位置用视口高度的百分比表示（side + pct）：这样转动屏幕、
// 切换地址栏高度后位置还相对合适。整块逻辑都是纯函数，好测也好在 App.vue 里复用。

export type NavTabSide = 'left' | 'right'

export interface NavTabPos {
  /** 贴哪一侧边缘。 */
  side: NavTabSide
  /** 标签中心在视口高度上的百分比（0–100）。 */
  pct: number
}

/** localStorage 键：存 `{ side, pct }`。 */
export const NAV_TAB_KEY = 'gofer.navTab'
/** 默认停在左边缘、视口高度 45% 处：拇指顺手（略高于中间），又不会压到工作台底部那几行「催办」。 */
export const NAV_TAB_DEFAULT: NavTabPos = { side: 'left', pct: 45 }
/** 移动超过 6px 才算拖动，否则当点击——手指没拿稳时抽屉照样开得了。 */
export const NAV_TAB_DRAG_THRESHOLD = 6
/** 垂直位置上沿：顶栏下方。 */
export const NAV_TAB_TOP_INSET = 56
/** 垂直位置下沿：视口底部往上 360px，不进输入区（手机上一整块 composer / 催办条）。
 *  不按页面上输入区元素的实际顶部算：不同页面（工作台 / job 详情 / 会话抽屉）输入区高度不同，
 *  按元素量会让下边界在页面间跳，而拖动的位置是跨页面共用的一个百分比。 */
export const NAV_TAB_BOTTOM_INSET = 360
/** 滚动停止后多久恢复显示。 */
export const NAV_TAB_SCROLL_RESTORE_MS = 600

function defaultPos(): NavTabPos {
  return { side: NAV_TAB_DEFAULT.side, pct: NAV_TAB_DEFAULT.pct }
}

// 位置夹取：标签中心落在「顶栏下方 ~ 视口底部 360px 上方」之间；视口太矮时退到上沿。
// 夹取在渲染时做（pctToTop），所以旧版本存下的、落在新边界外的百分比也会被收回边界内。
export function clampTabTop(y: number, viewportH: number): number {
  const min = NAV_TAB_TOP_INSET
  const max = viewportH - NAV_TAB_BOTTOM_INSET
  if (!Number.isFinite(y) || max <= min) return min
  return Math.round(Math.min(Math.max(y, min), max))
}

/** 百分比 → 标签中心的像素 top（已夹取）。 */
export function pctToTop(pct: number, viewportH: number): number {
  if (!Number.isFinite(pct)) return clampTabTop(0, viewportH)
  return clampTabTop((pct / 100) * viewportH, viewportH)
}

/** 像素 top → 百分比（已夹取，保留一位小数，免得 localStorage 里全是浮点尾巴）。 */
export function topToPct(top: number, viewportH: number): number {
  if (!(viewportH > 0)) return NAV_TAB_DEFAULT.pct
  return Math.round((clampTabTop(top, viewportH) / viewportH) * 1000) / 10
}

/** 吸附：松手后贴到最近的一侧边缘。 */
export function snapSide(centerX: number, viewportW: number): NavTabSide {
  if (!Number.isFinite(centerX) || !(viewportW > 0)) return NAV_TAB_DEFAULT.side
  return centerX < viewportW / 2 ? 'left' : 'right'
}

/** 按下点到当前点的距离是否已过拖动阈值（严格大于，正好 6px 仍算点击）。 */
export function dragPassedThreshold(
  startX: number,
  startY: number,
  x: number,
  y: number,
  threshold: number = NAV_TAB_DRAG_THRESHOLD,
): boolean {
  return Math.hypot(x - startX, y - startY) > threshold
}

/** 松手位置（标签中心，视口坐标）→ 吸附后的位置。 */
export function posFromPoint(centerX: number, centerY: number, viewportW: number, viewportH: number): NavTabPos {
  return { side: snapSide(centerX, viewportW), pct: topToPct(centerY, viewportH) }
}

// 读：JSON 坏掉、字段不对、百分比越界（<0 或 >100）都回到默认位置。
export function parseNavTab(raw: string | null | undefined): NavTabPos {
  if (!raw) return defaultPos()
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return defaultPos()
  }
  if (!value || typeof value !== 'object') return defaultPos()
  const { side, pct } = value as { side?: unknown; pct?: unknown }
  if (side !== 'left' && side !== 'right') return defaultPos()
  if (typeof pct !== 'number' || !Number.isFinite(pct) || pct < 0 || pct > 100) return defaultPos()
  return { side, pct }
}

/** 只用到这两个方法的存储接口，测试里给个假的即可。 */
export interface NavTabStore {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

export function readNavTab(store?: NavTabStore | null): NavTabPos {
  if (!store) return defaultPos()
  try {
    return parseNavTab(store.getItem(NAV_TAB_KEY))
  } catch {
    // 隐私模式 / 被禁用：读不到就用默认位置。
    return defaultPos()
  }
}

export function writeNavTab(pos: NavTabPos, store?: NavTabStore | null): void {
  if (!store) return
  try {
    store.setItem(NAV_TAB_KEY, JSON.stringify({ side: pos.side, pct: pos.pct }))
  } catch {
    // 配额满 / 被禁用：记不住位置不影响用。
  }
}

/** 浏览器里的 localStorage；拿不到（禁用、非浏览器）时返回 null。 */
export function defaultNavTabStore(): NavTabStore | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

/** 焦点在这类元素上说明用户正在打字，标签要让开。 */
export function isTypingTarget(el: { tagName?: string; isContentEditable?: boolean } | null | undefined): boolean {
  if (!el) return false
  if (el.isContentEditable) return true
  const tag = (el.tagName || '').toUpperCase()
  return tag === 'INPUT' || tag === 'TEXTAREA'
}
