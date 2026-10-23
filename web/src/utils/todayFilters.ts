// 今天页的类型筛选与首屏摘要（UI 重设计 P3，设计稿 §7.1）：纯函数，页面与测试共用。
//
// 卡片种类（TodayCardKind）多于设计稿列的六个类型 chip，所以这里做一层归组：
// 审批 / 提问 / 验收 / 等回复 / 记忆 / worker 申请。归不进这六组的工作项、合并建议、
// 收集箱等仍出现在「全部」里，不会被藏掉。
import type { TodayCard, TodayCardKind } from '../api/today'
import type { TodayLane } from '../api/todayLanes'

export type TodayTypeGroup = 'approval' | 'question' | 'review' | 'reply' | 'memory' | 'worker'

// chip 的顺序即设计稿 §7.1 的顺序。
export const TODAY_TYPE_GROUPS: TodayTypeGroup[] = ['approval', 'question', 'review', 'reply', 'memory', 'worker']

const GROUP_OF: Record<TodayCardKind, TodayTypeGroup | ''> = {
  approval: 'approval',
  interaction: 'question',
  decision: 'question',
  review: 'review',
  relay: 'reply',
  permission: 'reply',
  memory: 'memory',
  worker_enroll: 'worker',
  // 归不进六个 chip 的种类（只在「全部」里出现）
  work: '',
  suggestion: '',
  merge: '',
  plan_blocked: '',
  capture: '',
  mail_held: '',
}

export function typeGroupOf(card: Pick<TodayCard, 'kind'>): TodayTypeGroup | '' {
  return GROUP_OF[card.kind] ?? ''
}

// 每个类型 chip 的计数（只数归进该组的卡）。
export function typeCounts(cards: Array<Pick<TodayCard, 'kind'>>): Record<TodayTypeGroup, number> {
  const counts: Record<TodayTypeGroup, number> = { approval: 0, question: 0, review: 0, reply: 0, memory: 0, worker: 0 }
  for (const card of cards) {
    const group = typeGroupOf(card)
    if (group) counts[group] += 1
  }
  return counts
}

// group 为空 = 不按类型过滤。
export function filterByType<T extends Pick<TodayCard, 'kind'>>(cards: T[], group: string): T[] {
  if (!group) return cards
  return cards.filter((c) => typeGroupOf(c) === group)
}

// 项目 chip：空 = 全部项目；卡片没有 project_key 时只出现在「全部项目」里。
export function filterByProject<T extends Pick<TodayCard, 'project_key'>>(cards: T[], project: string): T[] {
  if (!project) return cards
  return cards.filter((c) => c.project_key === project)
}

// 首屏一行摘要里「agent 在跑 / 空闲会话」两个数：都从并行中的泳道算，
// 不新增接口（泳道的 agents 带 running / awaiting_input / idle 三态）。
export function laneAgentStats(lanes: TodayLane[] | null | undefined): { running: number; idle: number } {
  let running = 0
  let idle = 0
  for (const lane of lanes ?? []) {
    for (const agent of lane.agents ?? []) {
      if (agent.state === 'running') running += 1
      else if (agent.state === 'idle') idle += 1
    }
  }
  return { running, idle }
}
