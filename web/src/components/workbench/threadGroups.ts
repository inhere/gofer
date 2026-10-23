import type { WorkbenchProjectGroup, WorkbenchStatus, WorkbenchThread } from '../../api/types'

// 工作台会话的状态分组（设计稿 §7 工作台）：需要你 / 活跃 / 空闲与离线，已结束单独折叠。
// 放在纯函数里，方便单测穷举（会话状态只有 6 个值）。
export type ThreadGroup = 'need' | 'active' | 'idle' | 'ended'

export const THREAD_GROUP_ORDER: readonly ThreadGroup[] = ['need', 'active', 'idle', 'ended']

const GROUP_OF: Record<WorkbenchStatus, ThreadGroup> = {
  blocked: 'need',
  review: 'need',
  awaiting_input: 'need',
  working: 'active',
  idle: 'idle',
  done: 'ended',
}

export function threadGroup(status: WorkbenchStatus): ThreadGroup {
  return GROUP_OF[status] ?? 'idle'
}

// RECENT_ACTIVE_SEC：两轮之间的会话（status=idle）在这段时间内有过活动，仍算「活跃」——
// 刚说完一轮、等人回话的会话不该和几小时没动静或已离线的会话混在「空闲与离线」里
// （用户 2026-10-23 反馈）。
export const RECENT_ACTIVE_SEC = 15 * 60

// lastActivity 是会话最近一次活动的时间（秒）：服务端更新时间与最近一次上报取较新者。
export function lastActivity(thread: Pick<WorkbenchThread, 'updated_at' | 'seen_at'>): number {
  return Math.max(thread.updated_at || 0, thread.seen_at || 0)
}

// groupOfThread 在状态分组之上加时间维度：idle 但最近活跃、且没有离线的会话归「活跃」。
export function groupOfThread(thread: WorkbenchThread, nowSec: number): ThreadGroup {
  const group = threadGroup(thread.status)
  if (group === 'idle' && thread.raw_status !== 'offline' && nowSec - lastActivity(thread) <= RECENT_ACTIVE_SEC) {
    return 'active'
  }
  return group
}

// groupThreads 分组，并在每组内按最近活动时间倒序（最近的在上）。
export function groupThreads(
  threads: readonly WorkbenchThread[],
  nowSec: number = Math.floor(Date.now() / 1000),
): Record<ThreadGroup, WorkbenchThread[]> {
  const groups: Record<ThreadGroup, WorkbenchThread[]> = { need: [], active: [], idle: [], ended: [] }
  for (const thread of threads) groups[groupOfThread(thread, nowSec)].push(thread)
  for (const key of THREAD_GROUP_ORDER) groups[key].sort((a, b) => lastActivity(b) - lastActivity(a))
  return groups
}

// 分组标题的 i18n key（文案在 i18n/pages/workbench.ts）。
export function threadGroupLabelKey(group: ThreadGroup): string {
  return group === 'ended' ? 'workbench.list.ended' : `workbench.group.${group}`
}

// 待处理（orphan）计数：服务端把它们算在项目组的 counts 里，而不是列表中的会话行
// （受阻计划 / 待决策的计划）。以前在项目分组行上显示，现在项目不再是分组，改成列表头一行提示。
export function orphanWaitingCount(groups: readonly WorkbenchProjectGroup[]): number {
  let total = 0
  for (const group of groups) {
    total += group.counts?.orphan_blocked ?? 0
    total += group.counts?.orphan_review ?? 0
  }
  return total
}
