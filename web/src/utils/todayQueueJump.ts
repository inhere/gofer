// Ctrl/⌘+J：全局「跳到「今天」队列的下一项」（issue gofer-okz3 ①，循环）。
//
// 纯逻辑：键位判定 + 光标推进。队列、光标和高亮在 store/today.ts，页面渲染在 /today 的队列上。
// 输入框 / 文本框 / 终端（xterm 的隐藏 textarea）里聚焦时不抢键——Ctrl+J 在终端里是换行。
import { isTypingTarget } from './today'

export const NEXT_CARD_KEY = 'j'

export interface NextCardKeyEvent {
  key: string
  ctrlKey?: boolean
  metaKey?: boolean
  altKey?: boolean
  shiftKey?: boolean
  isComposing?: boolean
  target?: EventTarget | null
}

// isNextCardKey：Ctrl 或 ⌘ + J。Alt / Shift 组合留给浏览器与窗口管理器，输入法组字中不触发。
export function isNextCardKey(ev: NextCardKeyEvent): boolean {
  if (!(ev.ctrlKey || ev.metaKey)) return false
  if (ev.altKey || ev.shiftKey || ev.isComposing) return false
  if (ev.key.toLowerCase() !== NEXT_CARD_KEY) return false
  return !isTypingTarget(ev.target ?? null)
}

// nextQueueIndex：光标循环前进一格。没按过（cursor < 0）时落到第一张；卡片被处理掉、
// 队列变短后按模取整，不会越界；空队列返回 -1（没有可跳的项）。
export function nextQueueIndex(cursor: number, total: number): number {
  if (!(total > 0)) return -1
  const current = Number.isFinite(cursor) && cursor >= 0 ? Math.floor(cursor) % total : total - 1
  return (current + 1) % total
}
