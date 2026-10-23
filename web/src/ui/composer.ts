// Composer（统一输入框）的纯逻辑：待发队列的持久化与自适应高度。
//
// 待发队列按「作用域 + 会话 id」存 localStorage：键 `gofer.<scope>.queue.<id>`，值是 JSON
// 字符串数组。发送成功或用户清空后删除该键；坏值（非 JSON、不是数组、混进非字符串）当作
// 没有队列，不让一条坏数据把输入框卡死。条数与单条长度都有上限，避免把 storage 写满。
const PREFIX = 'gofer'
const MAX_ITEMS = 20
const MAX_CHARS = 8000

function safeStorage(): Storage | null {
  try {
    return typeof window !== 'undefined' && window.localStorage ? window.localStorage : null
  } catch {
    // 隐私模式 / 存储被禁用：退回纯内存队列（调用方自己持有）
    return null
  }
}

export function composerQueueKey(scope: string, id: string): string {
  return `${PREFIX}.${scope}.queue.${id}`
}

// 归一化：去掉空串与超长项，最多 MAX_ITEMS 条。
export function normalizeComposerQueue(items: readonly unknown[]): string[] {
  const out: string[] = []
  for (const item of items) {
    if (typeof item !== 'string') continue
    const text = item.trim()
    if (!text) continue
    out.push(text.length > MAX_CHARS ? text.slice(0, MAX_CHARS) : text)
    if (out.length >= MAX_ITEMS) break
  }
  return out
}

export function readComposerQueue(
  scope: string,
  id: string,
  storage: Storage | null = safeStorage(),
): string[] {
  if (!id || !storage) return []
  try {
    const raw = storage.getItem(composerQueueKey(scope, id))
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed) ? normalizeComposerQueue(parsed) : []
  } catch {
    return []
  }
}

export function writeComposerQueue(
  scope: string,
  id: string,
  items: readonly string[],
  storage: Storage | null = safeStorage(),
): void {
  if (!id || !storage) return
  const queue = normalizeComposerQueue(items)
  try {
    if (queue.length === 0) storage.removeItem(composerQueueKey(scope, id))
    else storage.setItem(composerQueueKey(scope, id), JSON.stringify(queue))
  } catch {
    // 存不下就算了，队列仍在内存里
  }
}

export function clearComposerQueue(scope: string, id: string, storage: Storage | null = safeStorage()): void {
  writeComposerQueue(scope, id, [], storage)
}

// 回车即发送（工作台）：不按 Shift、不在输入法组字中。Ctrl/⌘+Enter 任何时候都发送（设计稿 §8）。
export function isComposerSubmit(event: KeyboardEvent, submitOnEnter = false): boolean {
  if (event.key !== 'Enter' || event.isComposing) return false
  if (event.ctrlKey || event.metaKey) return true
  return submitOnEnter && !event.shiftKey
}

// 自适应高度：先归零再按内容撑开，最多到视口的 40%（手机上更矮）。
export function autosizeComposer(el: HTMLTextAreaElement, maxRatio = 0.4): void {
  el.style.height = 'auto'
  const max = Math.max(96, window.innerHeight * maxRatio)
  el.style.height = `${Math.min(el.scrollHeight, max)}px`
}
