import type { WorkbenchStatus, WorkbenchThread } from '../../api/types'

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

export function groupThreads(threads: readonly WorkbenchThread[]): Record<ThreadGroup, WorkbenchThread[]> {
  const groups: Record<ThreadGroup, WorkbenchThread[]> = { need: [], active: [], idle: [], ended: [] }
  for (const thread of threads) groups[threadGroup(thread.status)].push(thread)
  return groups
}

// 分组标题的 i18n key（文案在 i18n/pages/workbench.ts）。
export function threadGroupLabelKey(group: ThreadGroup): string {
  return group === 'ended' ? 'workbench.list.ended' : `workbench.group.${group}`
}
