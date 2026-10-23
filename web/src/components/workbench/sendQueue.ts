// 工作台待发队列的持久化（评审意见：原来只存在内存里，刷新就丢消息）。
//
// P4 起队列收进统一的 Composer（web/src/ui/composer.ts）：这里只保留工作台的作用域与
// 旧的导出名，缓存键仍是 `gofer.workbench.queue.<会话 id>`，老用户刷新后队列还在。
import {
  clearComposerQueue,
  composerQueueKey,
  normalizeComposerQueue,
  readComposerQueue,
  writeComposerQueue,
} from '../../ui/composer'

const SCOPE = 'workbench'

export function sendQueueKey(threadID: string): string {
  return composerQueueKey(SCOPE, threadID)
}

export function normalizeQueue(items: readonly unknown[]): string[] {
  return normalizeComposerQueue(items)
}

// storage 省略 = 用 localStorage（与 ui/composer.ts 同一口径）。
export function readSendQueue(threadID: string, storage?: Storage | null): string[] {
  return readComposerQueue(SCOPE, threadID, storage)
}

export function writeSendQueue(threadID: string, items: readonly string[], storage?: Storage | null): void {
  writeComposerQueue(SCOPE, threadID, items, storage)
}

export function clearSendQueue(threadID: string, storage?: Storage | null): void {
  clearComposerQueue(SCOPE, threadID, storage)
}
