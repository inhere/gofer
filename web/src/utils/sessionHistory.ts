// Sessions 历史页（R13）的列表逻辑：三类会话（ACP / 终端 / relay）合成一条列表，
// 数据全部来自工作台线程接口（R10 加了 agent / runner 筛选与 counts）。页面与测试共用。
import type { WorkbenchSourceCounts, WorkbenchStatus } from '../api/types'

// 来源页签的值；'' = 全部（接口不带 source）。
export type HistorySource = '' | 'acp' | 'pty' | 'relay'

export interface HistoryFilters {
  project?: string
  agent?: string
  runner?: string
  status?: WorkbenchStatus | ''
  q?: string
}

export interface HistoryPage {
  limit?: number
  cursor?: string
  // 只有第一页要 counts（后续页的计数与分页无关，重复要一遍是白读全表）。
  counts?: boolean
}

// historyQuery 把页面筛选转成线程列表参数：counts 与 source 无关，所以切页签时
// 计数与列表始终出自同一份过滤条件。
export function historyQuery(filters: HistoryFilters, source: HistorySource, page: HistoryPage = {}) {
  return {
    project: filters.project || undefined,
    agent: filters.agent || undefined,
    runner: filters.runner || undefined,
    status: filters.status || undefined,
    q: filters.q?.trim() || undefined,
    source: source || undefined,
    counts: page.counts || undefined,
    limit: page.limit,
    cursor: page.cursor || undefined,
    // since=1 = 全部历史（默许；不传就是接口的默认时间窗）。
    since: 1,
  }
}

// emptyCounts 给老服务端（无 counts 字段）用：不计条数而不是显示 0。
export const EMPTY_COUNTS: WorkbenchSourceCounts = { acp: 0, pty: 0, relay: 0, all: 0 }

// countFor 取某个来源页签的条数。
export function countFor(counts: WorkbenchSourceCounts | undefined, source: HistorySource): number | undefined {
  if (!counts) return undefined
  return source === '' ? counts.all : counts[source]
}

export interface HistoryTabItem {
  value: HistorySource
  label: string
  count?: number
}

// historyTabs 生成页签（含条数）：条数来自接口 counts，任意筛选下都与列表一致。
export function historyTabs(
  counts: WorkbenchSourceCounts | undefined,
  labels: Record<HistorySource, string>,
): HistoryTabItem[] {
  return (['', 'acp', 'pty', 'relay'] as HistorySource[]).map((value) => ({
    value,
    label: labels[value],
    count: countFor(counts, value),
  }))
}
