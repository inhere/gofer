// 信箱对话小地图的纯逻辑：右侧时间线边缘的一条竖向刻度，每封信一个标记，按在对话里的
// 位置排列（第 i 封在 (i+0.5)/count 处，封数多时自然压缩），当前可视范围另有一条高亮带。
// 组件（components/mail/MailMinimap.vue）只负责渲染与派发点击，位置 / 颜色 / 悬停文案的
// 规则都在这里，便于在 node 环境下单测。
import { fmtAgo } from '../api/time'
import type { MailMessage } from '../api/types'
import { mailOutgoing, type MailIdentity } from './mail'
import { mailSummary, mailUnread, personaLabel, sortMailTimeline } from './mailConversations'

// 少于这么多封就不画小地图：一两封信上下都看得见，刻度只是噪音。
export const MINIMAP_MIN_COUNT = 3

// 标记的颜色语义（tokens.css 的语义色）：急件 > 未读 > 发件 > 收件，一个标记只占一色。
export type MinimapTone = 'urgent' | 'unread' | 'out' | 'in'

export interface MailMinimapMark {
  id: number
  // seq 是这封信在对话里的顺序号（1 起，与「回复 #N」同一口径）。
  seq: number
  // topPct 是标记中心在轨道上的纵向百分比（0 = 最上面，100 = 最下面）。
  topPct: number
  tone: MinimapTone
  mine: boolean
  unread: boolean
  urgent: boolean
  // tooltip 是悬停提示：发件方与时间一行、正文首行摘要一行（title 属性里的换行会显示）。
  tooltip: string
}

// clampPct 把百分比夹进 [0, 100]（非法输入按 0 处理）。
function clampPct(v: number): number {
  if (!Number.isFinite(v)) return 0
  return Math.min(Math.max(v, 0), 100)
}

// minimapMarkTopPct 是第 index 封（0 起）在 count 封对话里的标记中心位置：等分轨道的格子
// 中心，所以封数多时按比例压缩、封数少时也不会贴边。
export function minimapMarkTopPct(index: number, count: number): number {
  if (count <= 0) return 0
  const i = Math.min(Math.max(index, 0), count - 1)
  return ((i + 0.5) / count) * 100
}

// minimapTone 是这封信的标记颜色：急件优先，其次未读，然后按收发方向分。
export function minimapTone(m: MailMessage, me: MailIdentity = {}): MinimapTone {
  if (m.urgent) return 'urgent'
  if (mailUnread(m)) return 'unread'
  return mailOutgoing(m, me) ? 'out' : 'in'
}

// minimapTooltip 是标记的悬停文案：发件方 · 多久以前，换行后是正文首行摘要。
export function minimapTooltip(m: MailMessage, nowSec = 0): string {
  const who = personaLabel(m.from) || '未知发件方'
  const when = fmtAgo(m.created_at, nowSec)
  const summary = mailSummary(m.body, 60)
  return [`${who} · ${when}`, summary].filter(Boolean).join('\n')
}

// buildMinimapMarks 把时间线（时间正序）变成标记表：位置、颜色、悬停文案一次算好。
export function buildMinimapMarks(mails: MailMessage[], me: MailIdentity = {}, nowSec = 0): MailMinimapMark[] {
  const sorted = sortMailTimeline(mails)
  return sorted.map((m, i) => ({
    id: m.id,
    seq: i + 1,
    topPct: minimapMarkTopPct(i, sorted.length),
    tone: minimapTone(m, me),
    mine: mailOutgoing(m, me),
    unread: mailUnread(m),
    urgent: !!m.urgent,
    tooltip: minimapTooltip(m, nowSec),
  }))
}

// minimapVisible：对话够长才显示小地图（只有一两封时省略）。
export function minimapVisible(count: number, min = MINIMAP_MIN_COUNT): boolean {
  return count >= min
}

// 可视范围高亮带（百分比）：top 是带的顶端、height 是带的高。
export interface MinimapBand {
  topPct: number
  heightPct: number
}

// minimapBand 由时间线的滚动量算出可视范围在整条轨道上的位置：内容没超过一屏时整条高亮。
export function minimapBand(scrollTop: number, clientHeight: number, scrollHeight: number): MinimapBand {
  if (!(scrollHeight > 0) || !(clientHeight > 0) || scrollHeight <= clientHeight) {
    return { topPct: 0, heightPct: 100 }
  }
  const top = Math.min(Math.max(scrollTop, 0), scrollHeight - clientHeight)
  const heightPct = (clientHeight / scrollHeight) * 100
  return { topPct: clampPct((top / scrollHeight) * 100), heightPct: clampPct(heightPct) }
}

// minimapScrollTopForPct 是点某个标记后时间线该滚到哪里：把那封信放到可视区中间，
// 再夹在两端之内（顶部 / 底部不越界）。
export function minimapScrollTopForPct(topPct: number, clientHeight: number, scrollHeight: number): number {
  if (!(scrollHeight > 0) || !(clientHeight > 0) || scrollHeight <= clientHeight) return 0
  const target = (clampPct(topPct) / 100) * scrollHeight
  return Math.min(Math.max(target - clientHeight / 2, 0), scrollHeight - clientHeight)
}
