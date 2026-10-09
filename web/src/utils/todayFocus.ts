// N3「专注处理」（T3，design §4）的纯逻辑：键位映射、操作序号、进度。
import type { TodayAction, TodayCard } from '../api/today'
import { adviceAction } from './today'

export type FocusCommand =
  | { kind: 'next' }
  | { kind: 'prev' }
  | { kind: 'action'; index: number }
  | { kind: 'approve' }
  | { kind: 'reply' }
  | { kind: 'info' }
  | { kind: 'snooze' }
  | { kind: 'skip' }
  | { kind: 'exit' }

// focusCommand：把按键翻成专注模式的命令。z（撤销）由全局监听处理，这里不接；
// 输入框里打字、带修饰键时一律不触发（Esc 交给输入框自己收起）。
export function focusCommand(key: string, opts: { typing?: boolean; modifier?: boolean } = {}): FocusCommand | null {
  if (opts.typing || opts.modifier) return null
  switch (key) {
    case 'j':
    case 'ArrowDown':
      return { kind: 'next' }
    case 'k':
    case 'ArrowUp':
      return { kind: 'prev' }
    case 'a':
      return { kind: 'approve' }
    case 'r':
      return { kind: 'reply' }
    case 'i':
      return { kind: 'info' }
    case 'h':
      return { kind: 'snooze' }
    case 's':
      return { kind: 'skip' }
    case 'Escape':
      return { kind: 'exit' }
  }
  if (/^[1-9]$/.test(key)) return { kind: 'action', index: Number(key) - 1 }
  return null
}

// 操作按钮的序号顺序（1-9）：「按建议」主按钮在最前，其余按卡上顺序；纯跳转（看 diff / 打开
// plan）不编号，免得在专注模式里一按数字就离开。
export function focusActions(card: TodayCard): TodayAction[] {
  const adv = adviceAction(card)
  const rest = card.actions.filter((a) => a !== adv && a.id !== 'diff' && a.id !== 'open')
  return adv ? [adv, ...rest] : rest
}

// a：通过 / 采纳 = ok 样式的那个操作。
export function approveAction(card: TodayCard): TodayAction | null {
  return card.actions.find((a) => a.style === 'ok') ?? null
}

// r：回复 = 需要输入文字的那个操作。
export function replyAction(card: TodayCard): TodayAction | null {
  return card.actions.find((a) => a.needs_text) ?? null
}

// 横向滑动：左滑下一张、右滑上一张；竖向为主或距离不够不算。
export function swipeCommand(dx: number, dy: number, min = 60): FocusCommand | null {
  if (Math.abs(dx) < min || Math.abs(dx) <= Math.abs(dy)) return null
  return dx < 0 ? { kind: 'next' } : { kind: 'prev' }
}

export interface FocusProgress {
  // 顶部「3 / 11」
  pos: number
  total: number
  // 进度条 0-100
  percent: number
}

// 本轮见过的卡 seen（含中途新来的）里，已离开队列（处理 / 稍后 / 跳过）的算进度。
export function focusProgress(seen: number, remaining: number, index: number): FocusProgress {
  const total = Math.max(seen, remaining)
  const gone = total - remaining
  if (remaining === 0) return { pos: total, total, percent: 100 }
  return { pos: gone + Math.min(index, remaining - 1) + 1, total, percent: total ? Math.round((gone / total) * 100) : 0 }
}
