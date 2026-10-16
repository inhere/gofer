// 授权模式预览的取数与防抖：展开规则在服务端（GET /v1/auth/expand），这里只管
// 「输入一变，等一小会儿再问；晚到的旧回答不覆盖新的」。纯函数 + 可注入的 fetcher，便于单测。
import { emptyExpandResult, mergeExpandPreviews, splitPatterns } from './permissions'
import type { ExpandPreview, ExpandResult } from './permissions'

export const PREVIEW_DEBOUNCE_MS = 300

export type PreviewFetcher = (pattern: string, subject: string) => Promise<ExpandPreview>

// 文本里每个模式各问一次，合并成一份结果。
export async function previewPatterns(text: string, subject: string, fetcher: PreviewFetcher): Promise<ExpandResult> {
  const parts = splitPatterns(text)
  if (parts.length === 0) return emptyExpandResult('empty')
  const previews = await Promise.all(parts.map((p) => fetcher(p, subject)))
  return mergeExpandPreviews(previews)
}

export interface PreviewScheduler {
  schedule(text: string, subject: string): void
  cancel(): void
}

// 防抖调度：空输入立即给出 empty；否则先报 loading，delay 后发请求。
// 请求失败（403 / 网络）报 unavailable 与原因。每次 schedule 作废上一次，晚到的回答丢弃。
export function createPreviewScheduler(
  fetcher: PreviewFetcher,
  onResult: (r: ExpandResult) => void,
  delay: number = PREVIEW_DEBOUNCE_MS,
): PreviewScheduler {
  let timer: ReturnType<typeof setTimeout> | null = null
  let seq = 0
  const clear = (): void => {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }
  return {
    schedule(text, subject) {
      clear()
      const mine = ++seq
      if (splitPatterns(text).length === 0) {
        onResult(emptyExpandResult('empty'))
        return
      }
      onResult(emptyExpandResult('loading', text.trim()))
      timer = setTimeout(() => {
        timer = null
        previewPatterns(text, subject, fetcher).then(
          (r) => {
            if (mine === seq) onResult(r)
          },
          (e: unknown) => {
            if (mine === seq) onResult(emptyExpandResult('unavailable', text.trim(), e instanceof Error ? e.message : String(e)))
          },
        )
      }, delay)
    },
    cancel() {
      seq++
      clear()
    },
  }
}
