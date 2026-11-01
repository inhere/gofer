import type { WorkbenchProjectGroup, WorkbenchStatus, WorkbenchThread, WorkbenchThreadsResp } from '../../api/types'
import type { WorkbenchThreadQuery } from '../../api/workbench'

// 工作台会话列表默认只拉最近 7 天有活动的会话（置顶 / 进行中 / 等你的由服务端放行，不受窗口限制），
// 每页 PAGE_SIZE 个；更早的由「加载更多」按 next_cursor 追加。手机与桌面共用同一组数字。
export const WORKBENCH_ACTIVE_WITHIN_SEC = 7 * 86400
export const WORKBENCH_PAGE_SIZE = 30

export interface ThreadListFilters {
  project?: string
  status?: WorkbenchStatus | ''
  q?: string
}

// 首页请求。搜索（q 非空）不受窗口与分页限制：要搜的往往就是很久以前的会话，since=1 即「全部历史」。
export function firstPageQuery(filters: ThreadListFilters): WorkbenchThreadQuery {
  const base: WorkbenchThreadQuery = {
    project: filters.project || undefined,
    status: filters.status || '',
    q: filters.q?.trim() || undefined,
  }
  if (base.q) return { ...base, since: 1 }
  return { ...base, active_within: WORKBENCH_ACTIVE_WITHIN_SEC, limit: WORKBENCH_PAGE_SIZE }
}

// 「加载更多」请求：只带游标与每页大小（游标之后没有窗口下限）。
export function morePageQuery(filters: ThreadListFilters, cursor: string): WorkbenchThreadQuery {
  return {
    project: filters.project || undefined,
    status: filters.status || '',
    cursor,
    limit: WORKBENCH_PAGE_SIZE,
  }
}

// 筛选条件变了，已追加的更早页就作废（它们是按旧条件拉的）。
export function filtersKey(filters: ThreadListFilters): string {
  return [filters.project ?? '', filters.status ?? '', filters.q?.trim() ?? ''].join('\u0000')
}

export function threadsOf(resp: WorkbenchThreadsResp): WorkbenchThread[] {
  return resp.projects.flatMap((group) => group.threads)
}

// 把「加载更多」追加的更早会话并进最新一页：最新一页里已有的 id 以它为准（刷新带回来的更新），
// 其余原样保留。没有追加页时原样返回，省掉一次拷贝。
export function mergeOlderThreads(first: WorkbenchThreadsResp, older: WorkbenchThread[]): WorkbenchThreadsResp {
  if (older.length === 0) return first
  const have = new Set(threadsOf(first).map((thread) => thread.id))
  const extra = older.filter((thread) => !have.has(thread.id))
  if (extra.length === 0) return first
  const groups: WorkbenchProjectGroup[] = first.projects.map((group) => ({ ...group, threads: [...group.threads] }))
  const byProject = new Map(groups.map((group) => [group.project_key, group]))
  for (const thread of extra) {
    let group = byProject.get(thread.project_key)
    if (!group) {
      group = {
        project_key: thread.project_key,
        status: 'idle',
        counts: { total: 0, blocked: 0, working: 0, review: 0, done: 0, idle: 0, orphan_blocked: 0, orphan_review: 0 },
        threads: [],
      }
      byProject.set(thread.project_key, group)
      groups.push(group)
    }
    group.threads.push(thread)
  }
  groups.sort((a, b) => (a.project_key < b.project_key ? -1 : a.project_key > b.project_key ? 1 : 0))
  return { ...first, projects: groups, total: first.total + extra.length }
}

// 追加一页更早的会话（按 id 去重，保持先后顺序）。
export function appendOlderPage(
  loaded: WorkbenchThread[],
  page: WorkbenchThreadsResp,
  firstPageIDs: ReadonlySet<string>,
): WorkbenchThread[] {
  const seen = new Set(loaded.map((thread) => thread.id))
  const out = [...loaded]
  for (const thread of threadsOf(page)) {
    if (seen.has(thread.id) || firstPageIDs.has(thread.id)) continue
    seen.add(thread.id)
    out.push(thread)
  }
  return out
}
