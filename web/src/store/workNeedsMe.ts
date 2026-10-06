// 「等我」计数（导航 Works 徽标）：服务端 /v1/work-items 的 summary.needs_me。
//
// 数据源是现有 pushhub 的 `work` 主题（inval → 防抖 REST 重拉）；断线超过 15s 时由
// createLiveTopic 统一切回 30s 兜底轮询，与其它页面一致，这里不另起定时器。
import { ref } from 'vue'
import { listWorkItems } from '../api/client'
import { createLiveTopic, type LiveTopicHandle } from '../utils/useLiveTopic'

export const workNeedsMeCount = ref(0)

export function shouldShowWorkBadge(count: number): boolean {
  return count > 0
}

export function workBadgeLabel(count: number): string {
  return count > 99 ? '99+' : String(count)
}

export async function refreshWorkNeedsMe(): Promise<void> {
  // 只要 summary，不要条目：limit=1 的最小负载（summary 是全局计数，与 limit 无关）。
  const r = await listWorkItems({ status: ['needs_me'], limit: 1 })
  workNeedsMeCount.value = r.summary?.needs_me ?? 0
}

let handle: LiveTopicHandle | null = null

// 进入导航壳时启动、离开（如退到 /access）时停止；重复 start 无副作用。
export function startWorkNeedsMe(): void {
  if (!handle) handle = createLiveTopic('work', { fetch: refreshWorkNeedsMe })
  handle.start()
}

export function stopWorkNeedsMe(): void {
  handle?.stop()
  workNeedsMeCount.value = 0
}
