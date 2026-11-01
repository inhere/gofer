// 「归档自动生成的工作项」的流程（设计稿 2026-11-01 §4.2）：先 dry-run 数出会动哪些，
// 给人看数量并确认，确认了才真归档。接口与确认框都从外面传进来，所以不用 DOM 就能测。
import type { WorkItem } from '../api/types'
import { isClosed } from './work'

// 已加载的列表里还没结束的 auto 来源工作项数：入口只在它大于 0 时出现。
// （「没人动过」由服务端判，这里只决定要不要给入口。）
export function autoItemCount(items: readonly WorkItem[]): number {
  return items.filter((it) => it.source === 'auto' && !isClosed(it.status)).length
}

export type ArchiveAutoOutcome =
  | { state: 'none' }
  | { state: 'cancelled'; n: number }
  | { state: 'done'; n: number }

export interface ArchiveAutoDeps {
  /** dry-run：返回会被归档的 id */
  preview(): Promise<{ ids: string[] }>
  /** 真归档：返回实际归档数 */
  run(): Promise<{ archived: number }>
  /** 带数量的确认；返回 false = 取消，什么都不做 */
  confirm(n: number): boolean
}

export async function archiveAutoFlow(deps: ArchiveAutoDeps): Promise<ArchiveAutoOutcome> {
  const { ids } = await deps.preview()
  if (ids.length === 0) return { state: 'none' }
  if (!deps.confirm(ids.length)) return { state: 'cancelled', n: ids.length }
  const { archived } = await deps.run()
  return { state: 'done', n: archived }
}
