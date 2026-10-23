// 手机（窄屏）档的共用口径：纯函数，不碰 DOM，便于单测。
//
// 断点与顶栏的导航抽屉断点一致（900px 以下用抽屉，见 App.vue 的 @media (max-width: 900px)）：
// 手机档只压缩「头部 / 提示条」这类占纵向空间的装饰，桌面（>900px）布局一行都不变。
import { t } from '../i18n'

export const MOBILE_MAX_WIDTH = 900

// isMobileWidth：给定视口宽度是否属于手机档。非正数（取不到宽度）不算手机。
export function isMobileWidth(width: number): boolean {
  return width > 0 && width <= MOBILE_MAX_WIDTH
}

// 手机提示条自动收起的停留时间（毫秒）。窄屏下提示条压在会话内容上，几秒后自己收掉；
// 宽屏返回 0 = 不自动收（沿用原来的「等用户点右上角 ×」）。
export const MOBILE_TOAST_DWELL_MS = 5000

export function toastDwellMs(mobile: boolean): number {
  return mobile ? MOBILE_TOAST_DWELL_MS : 0
}

// 手机会话头部折叠条上的占位文案：主题与范围都没有时也要有内容可点。
// 函数：语言切换后重算（不存成模块级字符串）。
export function mobileHeaderFallback(): string {
  return t('sessions.mobile.headerFallback')
}

// sessionHeaderSummary：手机会话头部收成一行时的摘要 —— 「主题 · 范围」（范围多项用空格分隔）。
// 主题是推断值也照原样显示（与展开后的头部一致），两侧都缺就给占位文案。
export function sessionHeaderSummary(topic: string, scope: string[]): string {
  const parts: string[] = []
  const head = topic.trim()
  if (head) parts.push(head)
  const paths = (scope ?? []).map((item) => item.trim()).filter(Boolean)
  if (paths.length) parts.push(paths.join(' '))
  return parts.length ? parts.join(' · ') : mobileHeaderFallback()
}
