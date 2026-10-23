import { describe, expect, it } from 'vitest'
import type { WorkbenchStatus, WorkbenchThread } from '../../api/types'
import { groupThreads, THREAD_GROUP_ORDER, threadGroup, threadGroupLabelKey } from './threadGroups'

function thread(id: string, status: WorkbenchStatus): WorkbenchThread {
  return {
    id,
    kind: 'agent',
    status,
    title: id,
    project_key: 'p',
    resumable: true,
    started_at: 1,
    updated_at: 2,
    turns: 1,
  }
}

// 侧栏分组（设计稿 §7）：需要你 / 活跃 / 空闲与离线 / 已结束，覆盖全部 6 个会话状态。
describe('workbench thread groups', () => {
  it('maps every session status to exactly one group', () => {
    const statuses: WorkbenchStatus[] = ['blocked', 'review', 'awaiting_input', 'working', 'idle', 'done']
    const got = Object.fromEntries(statuses.map((status) => [status, threadGroup(status)]))
    expect(got).toEqual({
      blocked: 'need',
      review: 'need',
      awaiting_input: 'need',
      working: 'active',
      idle: 'idle',
      done: 'ended',
    })
  })

  it('keeps the group order stable and labels each group', () => {
    expect(THREAD_GROUP_ORDER).toEqual(['need', 'active', 'idle', 'ended'])
    expect(threadGroupLabelKey('need')).toBe('workbench.group.need')
    expect(threadGroupLabelKey('active')).toBe('workbench.group.active')
    expect(threadGroupLabelKey('idle')).toBe('workbench.group.idle')
    expect(threadGroupLabelKey('ended')).toBe('workbench.list.ended')
  })

  it('buckets threads in their original order', () => {
    const groups = groupThreads([
      thread('a', 'idle'),
      thread('b', 'blocked'),
      thread('c', 'working'),
      thread('d', 'done'),
      thread('e', 'review'),
    ])
    expect(groups.need.map((t) => t.id)).toEqual(['b', 'e'])
    expect(groups.active.map((t) => t.id)).toEqual(['c'])
    expect(groups.idle.map((t) => t.id)).toEqual(['a'])
    expect(groups.ended.map((t) => t.id)).toEqual(['d'])
  })
})
