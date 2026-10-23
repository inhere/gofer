// 待发队列的持久化（评审意见：原来只存在内存里，刷新就丢消息）。
//
// 按会话 id 存 localStorage：键 `gofer.workbench.queue.<会话 id>`，值是 JSON 字符串数组。
// 发送成功或用户清空后删除该键；坏值（非 JSON、不是数组、混进非字符串）当作没有队列，
// 不让一条坏数据把输入框卡死。条数与单条长度都有上限，避免把 storage 写满。
const PREFIX = 'gofer.workbench.queue.'
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

export function sendQueueKey(threadID: string): string {
  return `${PREFIX}${threadID}`
}

// 归一化：去掉空串与超长项，最多 MAX_ITEMS 条。
export function normalizeQueue(items: readonly unknown[]): string[] {
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

export function readSendQueue(threadID: string, storage: Storage | null = safeStorage()): string[] {
  if (!threadID || !storage) return []
  try {
    const raw = storage.getItem(sendQueueKey(threadID))
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed) ? normalizeQueue(parsed) : []
  } catch {
    return []
  }
}

export function writeSendQueue(threadID: string, items: readonly string[], storage: Storage | null = safeStorage()): void {
  if (!threadID || !storage) return
  const queue = normalizeQueue(items)
  try {
    if (queue.length === 0) storage.removeItem(sendQueueKey(threadID))
    else storage.setItem(sendQueueKey(threadID), JSON.stringify(queue))
  } catch {
    // 存不下就算了，队列仍在内存里
  }
}

export function clearSendQueue(threadID: string, storage: Storage | null = safeStorage()): void {
  writeSendQueue(threadID, [], storage)
}
