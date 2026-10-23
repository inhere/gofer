// 今天页的类型筛选与首屏摘要（设计稿 §7.1）：纯函数单测。
import { describe, expect, it } from 'vitest'
import type { TodayCard, TodayCardKind } from '../api/today'
import type { TodayLane } from '../api/todayLanes'
import { filterByProject, filterByType, laneAgentStats, TODAY_TYPE_GROUPS, typeCounts, typeGroupOf } from './todayFilters'

function card(kind: TodayCardKind, project?: string): Pick<TodayCard, 'kind' | 'project_key'> {
  return { kind, project_key: project }
}

describe('todayFilters', () => {
  it('maps every card kind to one of the six chips or to none', () => {
    expect(typeGroupOf({ kind: 'approval' })).toBe('approval')
    expect(typeGroupOf({ kind: 'interaction' })).toBe('question')
    expect(typeGroupOf({ kind: 'decision' })).toBe('question')
    expect(typeGroupOf({ kind: 'review' })).toBe('review')
    expect(typeGroupOf({ kind: 'relay' })).toBe('reply')
    expect(typeGroupOf({ kind: 'permission' })).toBe('reply')
    expect(typeGroupOf({ kind: 'memory' })).toBe('memory')
    expect(typeGroupOf({ kind: 'worker_enroll' })).toBe('worker')
    // 归不进六个 chip 的种类仍留在「全部」里
    for (const kind of ['work', 'suggestion', 'merge', 'plan_blocked', 'capture', 'mail_held'] as TodayCardKind[]) {
      expect(typeGroupOf({ kind })).toBe('')
    }
  })

  it('shows the six chips in the order of the design and counts each group', () => {
    expect(TODAY_TYPE_GROUPS).toEqual(['approval', 'question', 'review', 'reply', 'memory', 'worker'])
    const counts = typeCounts([
      card('approval'),
      card('approval'),
      card('interaction'),
      card('decision'),
      card('review'),
      card('permission'),
      card('memory'),
      card('worker_enroll'),
      card('work'),
    ])
    expect(counts).toEqual({ approval: 2, question: 2, review: 1, reply: 1, memory: 1, worker: 1 })
    // 归不进任何组的卡不计数，但也不会消失（「全部」的数量单独算）
    expect(Object.values(counts).reduce((n, v) => n + v, 0)).toBe(8)
  })

  it('filters by type and treats an empty value as 全部', () => {
    const cards = [card('approval'), card('review'), card('relay'), card('work')]
    expect(filterByType(cards, '')).toHaveLength(4)
    expect(filterByType(cards, 'review')).toEqual([card('review')])
    expect(filterByType(cards, 'reply')).toEqual([card('relay')])
    // 归不进组的卡在任何具体类型下都不出现，但「全部」里有
    expect(filterByType(cards, 'question')).toEqual([])
  })

  it('filters by project and treats an empty value as 全部项目', () => {
    const cards = [card('review', 'p1'), card('review', 'p2'), card('approval')]
    expect(filterByProject(cards, '')).toHaveLength(3)
    expect(filterByProject(cards, 'p1')).toEqual([card('review', 'p1')])
    // 没有 project_key 的卡只出现在「全部项目」里
    expect(filterByProject(cards, 'p2')).toEqual([card('review', 'p2')])
  })

  it('counts running and idle agents from the parallel lanes for the first-screen summary', () => {
    const lane = (states: Array<'running' | 'awaiting_input' | 'idle'>): TodayLane =>
      ({
        id: 'l',
        kind: 'plan',
        title: 't',
        status: 'open',
        agents: states.map((state) => ({ agent: 'codex', state, kind: 'job' as const, ref: 'j1' })),
        progress: {},
        health: 'ok',
        started_at: 0,
        elapsed_sec: 0,
        activity_at: 0,
        links: {},
      }) as TodayLane
    const stats = laneAgentStats([lane(['running', 'running', 'idle']), lane(['awaiting_input'])])
    expect(stats).toEqual({ running: 2, idle: 1 })
    expect(laneAgentStats(null)).toEqual({ running: 0, idle: 0 })
    expect(laneAgentStats([])).toEqual({ running: 0, idle: 0 })
  })
})
