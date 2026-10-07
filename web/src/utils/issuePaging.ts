// Issues 页的分页与多选：纯函数，不碰 DOM，便于单测。
import type { TreeEntry } from './issueTree'

export const PAGE_SIZES = [50, 100, 200] as const
export const DEFAULT_PAGE_SIZE = 50

export function normalizePageSize(value: unknown): number {
  const n = Number(value)
  return (PAGE_SIZES as readonly number[]).includes(n) ? n : DEFAULT_PAGE_SIZE
}

// 把树形行按「根」切页：根与它的全部后代（depth>0 的连续行）同页，不拆散。
// 一棵树本身超过 pageSize 时独占一页（宁可页长，也不拆父子）。
// 平铺模式每行都是 depth 0，天然退化为按行切页。
export function paginateEntries<T>(entries: T[], pageSize: number, depthOf: (item: T) => number): T[][] {
  const size = Math.max(1, Math.floor(pageSize))
  const groups: T[][] = []
  for (const e of entries) {
    if (depthOf(e) === 0 || groups.length === 0) groups.push([e])
    else groups[groups.length - 1].push(e)
  }
  const pages: T[][] = []
  let cur: T[] = []
  for (const g of groups) {
    if (cur.length > 0 && cur.length + g.length > size) {
      pages.push(cur)
      cur = []
    }
    cur = cur.concat(g)
  }
  if (cur.length > 0) pages.push(cur)
  return pages
}

export function clampPage(page: number, pageCount: number): number {
  if (pageCount <= 0) return 1
  return Math.min(Math.max(1, Math.floor(page) || 1), pageCount)
}

export type CheckState = 'none' | 'some' | 'all'

export function pageCheckState(selected: ReadonlySet<string>, pageIds: string[]): CheckState {
  if (pageIds.length === 0) return 'none'
  const n = pageIds.filter((id) => selected.has(id)).length
  return n === 0 ? 'none' : n === pageIds.length ? 'all' : 'some'
}

export function toggleId(selected: ReadonlySet<string>, id: string): Set<string> {
  const next = new Set(selected)
  if (!next.delete(id)) next.add(id)
  return next
}

// 全选本页：本页尚未全选 => 全部选上；已全选 => 取消本页（其它页的选中保留）。
export function togglePage(selected: ReadonlySet<string>, pageIds: string[]): Set<string> {
  const next = new Set(selected)
  if (pageCheckState(selected, pageIds) === 'all') for (const id of pageIds) next.delete(id)
  else for (const id of pageIds) next.add(id)
  return next
}

// id 的全部后代（沿 parent 向下，防环），仅限 allowed（当前筛选可见）里的项。
export function descendantIds(
  id: string,
  nodes: Array<{ id: string; parent?: string }>,
  allowed?: ReadonlySet<string>,
): string[] {
  const kids = new Map<string, string[]>()
  for (const n of nodes) {
    if (n.parent && n.parent !== n.id) kids.set(n.parent, [...(kids.get(n.parent) ?? []), n.id])
  }
  const out: string[] = []
  const seen = new Set<string>([id])
  const stack = [...(kids.get(id) ?? [])]
  while (stack.length > 0) {
    const cur = stack.pop()!
    if (seen.has(cur)) continue
    seen.add(cur)
    if (!allowed || allowed.has(cur)) out.push(cur)
    stack.push(...(kids.get(cur) ?? []))
  }
  return out
}

// 「连同子项」：该项与其后代是否已全部选中 => 取消，否则全部选中。
export function toggleWithDescendants(selected: ReadonlySet<string>, id: string, family: string[]): Set<string> {
  const ids = [id, ...family]
  const next = new Set(selected)
  if (ids.every((x) => next.has(x))) for (const x of ids) next.delete(x)
  else for (const x of ids) next.add(x)
  return next
}

export interface BatchItemResult { id: string; ok: boolean; error?: string }
export interface BatchSummary {
  total: number
  ok: number
  failed: number
  failures: Array<{ id: string; error: string }>
  okIds: string[]
  text: string
}

export function summarizeBatch(results: BatchItemResult[]): BatchSummary {
  const failures = results.filter((r) => !r.ok).map((r) => ({ id: r.id, error: r.error || '未知错误' }))
  const okIds = results.filter((r) => r.ok).map((r) => r.id)
  const text = failures.length === 0
    ? `已处理 ${okIds.length} 项`
    : `成功 ${okIds.length} 项，失败 ${failures.length} 项`
  return { total: results.length, ok: okIds.length, failed: failures.length, failures, okIds, text }
}

// 页码导航：当前页前后各 2 页 + 首尾，中间用 null 表示省略号。
export function pageWindow(page: number, pageCount: number): Array<number | null> {
  const keep = new Set<number>([1, pageCount])
  for (let p = page - 2; p <= page + 2; p++) if (p >= 1 && p <= pageCount) keep.add(p)
  const sorted = [...keep].sort((a, b) => a - b)
  const out: Array<number | null> = []
  sorted.forEach((p, i) => {
    if (i > 0 && p - sorted[i - 1] > 1) out.push(null)
    out.push(p)
  })
  return out
}

export type { TreeEntry }
