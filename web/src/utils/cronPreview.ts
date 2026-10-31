// cron 试算的纯逻辑（编辑弹窗输入 cron 时显示「下几次运行」）：把一次试算请求的结果
// 归成界面要的三种状态——空输入不请求、成功给运行时间、失败给错误原话。请求函数由
// 调用方注入（Schedules.vue 传 api/client 的 previewCron），便于单测。
import type { CronPreviewResp } from '../api/types'

export type CronPreviewState =
  | { kind: 'empty' }
  | { kind: 'ok'; runs: number[] }
  | { kind: 'error'; message: string }

export type CronPreviewFetcher = (cron: string, count?: number) => Promise<CronPreviewResp>

// 试算展示几次。
export const CRON_PREVIEW_COUNT = 5

// 输入停手多久后才发请求（毫秒）。
export const CRON_PREVIEW_DELAY_MS = 400

// errorText 优先用 ApiError.detail（解析器的原话），其次 message。
export function errorText(e: unknown): string {
  if (e && typeof e === 'object') {
    const detail = (e as { detail?: unknown }).detail
    if (typeof detail === 'string' && detail !== '') return detail
    const message = (e as { message?: unknown }).message
    if (typeof message === 'string' && message !== '') return message
  }
  return String(e)
}

// runCronPreview 对一个 cron 表达式试算；空白输入直接返回 empty，不发请求。
export async function runCronPreview(
  expr: string,
  fetcher: CronPreviewFetcher,
  count: number = CRON_PREVIEW_COUNT,
): Promise<CronPreviewState> {
  const cron = expr.trim()
  if (cron === '') return { kind: 'empty' }
  try {
    const resp = await fetcher(cron, count)
    return { kind: 'ok', runs: resp.next_runs ?? [] }
  } catch (e) {
    return { kind: 'error', message: errorText(e) }
  }
}
