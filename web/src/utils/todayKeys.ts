// 决策卡的快捷键（设计稿 §10）：只对获得焦点的卡片生效，输入法组字时不触发。
// 纯函数，卡片组件与测试共用；专注模式的键位另有 utils/todayFocus.ts（那里是整屏操作）。
import { isTypingTarget } from './today'

export type CardKey =
  | 'approve'
  | 'reject'
  | 'snooze'
  | 'reply'
  | 'info'
  | 'dismiss'
  | { kind: 'action'; index: number }

export interface CardKeyOpts {
  /** 焦点在输入框 / 文本框里：一个键都不抢 */
  typing?: boolean
  /** 输入法组字中（ev.isComposing）：不触发 */
  composing?: boolean
  /** 带 Ctrl / ⌘ / Alt：留给浏览器与全局快捷键 */
  modifier?: boolean
}

// 键位与设计稿 §10 一致：A 允许 / 通过、D 拒绝 / 打回、S 稍后、R 回复、I 详情、
// 1–9 按顺序触发卡片上的操作；Esc 收起卡内展开的菜单与输入框。
export function cardKey(key: string, opts: CardKeyOpts = {}): CardKey | null {
  if (opts.typing || opts.composing || opts.modifier) return null
  if (key === 'Escape') return 'dismiss'
  if (/^[1-9]$/.test(key)) return { kind: 'action', index: Number(key) - 1 }
  switch (key.toLowerCase()) {
    case 'a':
      return 'approve'
    case 'd':
      return 'reject'
    case 's':
      return 'snooze'
    case 'r':
      return 'reply'
    case 'i':
      return 'info'
  }
  return null
}

// 文档事件的便利入口：判断焦点是否在输入控件里。
export function cardKeyFromEvent(ev: KeyboardEvent): CardKey | null {
  return cardKey(ev.key, {
    typing: isTypingTarget(ev.target),
    composing: ev.isComposing,
    modifier: ev.ctrlKey || ev.metaKey || ev.altKey,
  })
}
