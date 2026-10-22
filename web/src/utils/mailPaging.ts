// 信箱对话时间线的分页纯逻辑：一个活跃对话可能有几十封信，一次全取会让页面既慢又长，
// 所以首屏只要最新的一页（MAIL_TIMELINE_PAGE 封），往上翻再按「比已加载最旧那封更早」取
// 一页（GET /v1/mail 的 before= / order=desc）。合并、去重、has_more 与「上方插入后阅读位置
// 不动」的滚动修正都在这里，组件只负责渲染，便于在 node 环境下单测。
import type { MailMessage } from '../api/types'
import { sortMailTimeline } from './mailConversations'

// MAIL_TIMELINE_PAGE 是时间线一页的信件数（首屏与「加载更早」共用）。
export const MAIL_TIMELINE_PAGE = 6

// MAIL_BOTTOM_SLACK 是判定「已经贴到底部」的像素余量：在这个范围内刷新的新信会把视图带到底。
export const MAIL_BOTTOM_SLACK = 80

// 时间线列表状态：已加载的信（时间正序）与「还有没有更早的」。
export interface MailTimelinePages {
  mails: MailMessage[]
  hasOlder: boolean
}

// timelinePage 是一页的取数参数：只要最新的一页、按时间倒序取（服务端从最大的 id 往回走）。
export function timelinePage(limit = MAIL_TIMELINE_PAGE, before = 0): { limit: number; order: 'desc'; before?: number } {
  const q: { limit: number; order: 'desc'; before?: number } = { limit, order: 'desc' }
  if (before > 0) q.before = before
  return q
}

// oldestMailId / newestMailId 是已加载两端信件 id（没有信时 0）：前者是 before= 游标。
export function oldestMailId(mails: MailMessage[]): number {
  if (!mails.length) return 0
  return mails.reduce((min, m) => (m.id < min ? m.id : min), Number.POSITIVE_INFINITY)
}

export function newestMailId(mails: MailMessage[]): number {
  return mails.reduce((max, m) => (m.id > max ? m.id : max), 0)
}

// dedupeById 按 id 去重并排成时间正序（同一封信可能出现在相邻两页里：翻页时刚好来了新信）。
function dedupeById(mails: MailMessage[]): MailMessage[] {
  const seen = new Set<number>()
  const out: MailMessage[] = []
  for (const m of mails) {
    if (seen.has(m.id)) continue
    seen.add(m.id)
    out.push(m)
  }
  return sortMailTimeline(out)
}

// timelineFirstPage 是打开一个对话时的状态：服务端按时间倒序给的「最新一页」，这里翻成正序，
// 并记下它说的 has_more（还有更早的信）。
export function timelineFirstPage(page: MailMessage[], hasMore: boolean): MailTimelinePages {
  return { mails: dedupeById(page), hasOlder: !!hasMore && page.length > 0 }
}

// timelineWithOlder 把「更早的一页」插到上方：去重、保持时间正序，has_more 以这一页的回答为准。
export function timelineWithOlder(state: MailTimelinePages, page: MailMessage[], hasMore: boolean): MailTimelinePages {
  return { mails: dedupeById([...page, ...state.mails]), hasOlder: !!hasMore }
}

// mailNeedsReplace：新取到的那封是不是带了不同的可见状态（读信 / 送达 / 重试 / 广播进度）——
// 一样就不动，页面不重排。
export function mailNeedsReplace(loaded: MailMessage, next: MailMessage): boolean {
  return (
    loaded.state !== next.state ||
    loaded.read_at !== next.read_at ||
    loaded.delivered_at !== next.delivered_at ||
    loaded.attempts !== next.attempts ||
    loaded.last_error !== next.last_error ||
    loaded.urgent !== next.urgent ||
    loaded.broadcast?.delivered !== next.broadcast?.delivered ||
    loaded.broadcast?.answered !== next.broadcast?.answered ||
    loaded.broadcast?.pending !== next.broadcast?.pending
  )
}

// timelineWithNewest 只补最新的信（刷新 / 新信到达）：已加载的那几页原样留下，不重载；
// 比当前最新一封还新的才插入，已加载的那几封只把状态更新（读信 / 送达）替换进来。
export function timelineWithNewest(state: MailTimelinePages, page: MailMessage[]): MailTimelinePages {
  if (!state.mails.length) return { mails: dedupeById(page), hasOlder: state.hasOlder }
  const newest = newestMailId(state.mails)
  const fresh = page.filter((m) => m.id > newest)
  const byID = new Map(page.map((m) => [m.id, m]))
  let changed = fresh.length > 0
  const mails = state.mails.map((m) => {
    const next = byID.get(m.id)
    if (!next || !mailNeedsReplace(m, next)) return m
    changed = true
    return next
  })
  if (!changed) return state
  return { mails: dedupeById([...mails, ...fresh]), hasOlder: state.hasOlder }
}

// scrollTopAfterPrepend 是「上方插入内容后」该设的 scrollTop：内容变高的差值补到原位置上，
// 用户正读的那封信不会往下跳（只在插入前后各量一次 scrollHeight 即可）。
export function scrollTopAfterPrepend(prevScrollTop: number, prevScrollHeight: number, nextScrollHeight: number): number {
  const top = Number.isFinite(prevScrollTop) && prevScrollTop > 0 ? prevScrollTop : 0
  // 没量到插入前的高度（首次渲染 / 容器还没挂上）就修不了，保持原样。
  if (!(prevScrollHeight > 0)) return top
  const grew = (nextScrollHeight || 0) - prevScrollHeight
  if (!Number.isFinite(grew) || grew <= 0) return top
  return top + grew
}

// timelineNearBottom：时间线是否已经贴到底部（刷新的新信只在贴底时才把视图带下去，正在往上看
// 历史时不动）。
export function timelineNearBottom(
  scrollTop: number,
  clientHeight: number,
  scrollHeight: number,
  slack = MAIL_BOTTOM_SLACK,
): boolean {
  if (!(scrollHeight > 0)) return true
  return scrollHeight - (scrollTop + clientHeight) <= slack
}
