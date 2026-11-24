// 顶部「等我」弹卡的纯逻辑（design 2026-11-19 seats T5）：哪些项该弹、队列怎么排、当前这件消失
// 后换下一件、关闭后这个主体是不是已经被别的端看过，以及一张卡折叠回它的「主体 key」。
// 组件（ui/WaitingPopCard.vue）只负责接线与渲染：数据来自 composables/useWaiting 的推送快照、
// store/today 的 waiting 卡（标题 / 摘要 / 按钮）与 store/inbox 的个人状态。
import type { TodayAction, TodayCard, TodayCardKind } from '../api/today'
import type { InboxItem, InboxResponse } from '../api/inbox'
import type { WaitingItem } from '../api/types'
import { cardLink } from './today'
import { snoozeOptions, type SnoozeOption } from './todaySnooze'

// POP_KINDS 是「会话主体在等你答复 / 批准」的卡类型，只有这些弹卡：
//   - relay：终端会话停下等你回复；
//   - acp_reply：托管 ACP / suga 会话停在 awaiting_input 等回复；
//   - permission：终端 agent 的工具授权请求（允许 / 拒绝）；
//   - interaction：job 的待答复 / 待确认交互（permission 类交互也算批准）；
//   - approval：待在等你批准的 job（批准就开跑）。
// 不弹的（都是「通知你」而不是「等你答一句」）：review 待验收、job.failed /
// job.child_wake_failed 你提交的任务失败、todo_awaiting_merge / plan_blocked 计划类、
// caller.token_expiring token 将过期、server.health 自检故障、schedule.paused /
// seat_schedule 定时类、mail_held 投递，以及 work / suggestion / merge / capture /
// worker_enroll / memory 这些工作与整理类。
export const POP_KINDS: readonly TodayCardKind[] = ['relay', 'acp_reply', 'permission', 'interaction', 'approval']

// POP_SUBJECTS 是弹卡认的主体前缀：只有「会话主体」（一个会话或一个会话 job）才弹；
// decision: / plan: / todo: / schedule: / seat: / caller: 等主体各自走今天页与收件箱。
export const POP_SUBJECTS: readonly string[] = ['session', 'job']

// subjectOf 取主体 key 冒号前的那一段（"job:<id>" -> "job"）。
export function subjectOf(key: string): string {
  const i = key.indexOf(':')
  return i > 0 ? key.slice(0, i) : ''
}

// shouldPop：这一项该不该弹卡（主体 + 类型两个条件都满足）。
export function shouldPop(item: Pick<WaitingItem, 'key' | 'kind'>): boolean {
  return POP_SUBJECTS.includes(subjectOf(item.key)) && POP_KINDS.includes(item.kind)
}

// subjectKeyOf 是一张卡折叠到「等我」列表时用的主体 key，与 internal/inbox/waiting.go 的
// signalsOf 同口径（只覆盖弹卡认的几种 kind）：今天页的卡与收件箱条目带的是自己的 key
// （"approval:<job>" / "relay:<sid>"），要经这里的映射才能与推送里的主体 key 对上。
export function subjectKeyOf(card: { kind: string; refs?: TodayCard['refs'] }): string {
  const r = card.refs ?? {}
  switch (card.kind) {
    case 'relay':
    case 'permission':
      return r.session_id ? `session:${r.session_id}` : ''
    case 'acp_reply':
    case 'interaction':
    case 'approval':
      return r.job_id ? `job:${r.job_id}` : ''
  }
  return ''
}

// inboxItemOf 找主体 key 对应的收件箱条目（两栏都看：它也可能落在「仅知会」栏）。
export function inboxItemOf(
  data: Pick<InboxResponse, 'columns'> | null | undefined,
  key: string,
): InboxItem | null {
  if (!data?.columns) return null
  for (const col of ['action', 'fyi'] as const) {
    for (const it of data.columns[col] ?? []) {
      if (it.key === key || subjectKeyOf(it) === key) return it
    }
  }
  return null
}

// inboxSeenOf 读一个主体在收件箱里的个人状态：seen 含「已读」与「已清理」，unknown = 快照里
// 还没有这一项（收件箱被关掉，或推送刚到还没重拉）。
export function inboxSeenOf(
  data: Pick<InboxResponse, 'columns'> | null | undefined,
  key: string,
): 'unread' | 'seen' | 'unknown' {
  const it = inboxItemOf(data, key)
  if (!it) return 'unknown'
  return it.state === 'unread' ? 'unread' : 'seen'
}

// popQueueEnter 把一个新进项排到队尾（已在队列里的不重复排）。
export function popQueueEnter(queue: readonly string[], key: string): string[] {
  return queue.includes(key) ? [...queue] : [...queue, key]
}

// popEnter 是 onEnter 的处理：该弹的、别的端还没关过的才排到队尾，返回新的队列。
export function popEnter(
  queue: readonly string[],
  item: Pick<WaitingItem, 'key' | 'kind'>,
  seen: boolean,
): string[] {
  if (!shouldPop(item) || seen) return [...queue]
  return popQueueEnter(queue, item.key)
}

// popQueueReconcile 丢掉已经不在「等我」列表里的 key：当前这件处理掉（答复 / 批准 / 别人答了 /
// 稍后）就从列表里消失，队列自动换下一件；排队的项也一样，消失后不再弹。
export function popQueueReconcile(queue: readonly string[], live: ReadonlySet<string>): string[] {
  return queue.filter((k) => live.has(k))
}

// popQueueDrop 把一件从队列里去掉（关闭 / 答复 / 稍后立即换下一件，不等推送回来）。
export function popQueueDrop(queue: readonly string[], key: string): string[] {
  return queue.filter((k) => k !== key)
}

// popExtra 是当前这件后面还排着几件（+N）。
export function popExtra(queue: readonly string[]): number {
  return Math.max(0, queue.length - 1)
}

// popLink 是「打开」的目标：有卡就用今天页同一份跳转目标（线程 / 详情），卡还没到（重拉在途）
// 就按主体 key 推——会话进工作台线程，job 进详情页。
export function popLink(item: Pick<WaitingItem, 'key'>, card: TodayCard | null): string {
  if (card) return cardLink(card)
  const i = item.key.indexOf(':')
  const typ = i > 0 ? item.key.slice(0, i) : ''
  const ref = i > 0 ? item.key.slice(i + 1) : ''
  if (!ref) return ''
  if (typ === 'session') return `/workbench?thread=${encodeURIComponent(`r:${ref}`)}`
  if (typ === 'job') return `/jobs/${encodeURIComponent(ref)}`
  return ''
}

// popReplyAction 是「作答」用的动作：服务端给会话等回复卡的「回复」（id 固定为 reply，需输入文字）。
// 附件因拒绝（deny_note，也是 needs_text）和逐个选项作答（answer）不算「作答」输入框：前者是拒绝的一个
// 变体，后者在卡上是一排按钮，都留到今天页 / 会话里做。
export function popReplyAction(card: Pick<TodayCard, 'actions'> | null): TodayAction | null {
  return card?.actions.find((a) => a.id === 'reply' && a.needs_text) ?? null
}

// popApproveAction 是「批准」用的动作：ok 样式的那个（待批 job 的批准、权限请求的允许）。
export function popApproveAction(card: Pick<TodayCard, 'actions'> | null): TodayAction | null {
  return card?.actions.find((a) => a.style === 'ok') ?? null
}

// popSnoozeOption 是「稍后」跳到的那个选项（默认第一项，1 小时）：弹卡上只给一个按钮，细分的
// 稍后菜单仍在今天页的卡上。
export function popSnoozeOption(card: TodayCard): SnoozeOption | null {
  return snoozeOptions(card)[0] ?? null
}
