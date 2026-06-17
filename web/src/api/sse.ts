// 纯函数 SSE 帧解析器（便于单测）+ streamJob 消费器。
// 使用 fetch + res.body.getReader()（带 Authorization 头），不用 EventSource。

import { getToken, triggerUnauthorized } from '../store/auth'
import type { SSEEvent, SSEEventType } from './types'

export interface ParsedSSE {
  frames: SSEEvent[]
  rest: string
}

// 纯函数：输入累积串，按 `\n\n` 切帧，解析 event:/data: 行，返回 {frames, rest}。
// rest 是尚未完整的尾部（无结尾分隔符），交给下一次累积。
export function parseSSEChunk(buffer: string): ParsedSSE {
  const frames: SSEEvent[] = []
  // 兼容 \r\n\n 与 \n\n：先规整换行
  const normalized = buffer.replace(/\r\n/g, '\n')
  const parts = normalized.split('\n\n')
  // 最后一段是未完成的残留
  const rest = parts.pop() ?? ''

  for (const block of parts) {
    if (block.trim() === '') {
      continue
    }
    let eventType: string | null = null
    const dataLines: string[] = []
    for (const rawLine of block.split('\n')) {
      const line = rawLine
      if (line.startsWith('event:')) {
        eventType = line.slice('event:'.length).trim()
      } else if (line.startsWith('data:')) {
        // 按 SSE 规范，data: 后若有单个前导空格则去掉
        let d = line.slice('data:'.length)
        if (d.startsWith(' ')) {
          d = d.slice(1)
        }
        dataLines.push(d)
      }
      // 其余行（如 id:/comment :）忽略
    }
    if (eventType == null) {
      continue
    }
    const dataStr = dataLines.join('\n')
    let data: unknown = {}
    if (dataStr.length > 0) {
      try {
        data = JSON.parse(dataStr)
      } catch {
        // 非 JSON 时保留原始字符串
        data = dataStr
      }
    }
    frames.push({ type: eventType as SSEEventType, data })
  }

  return { frames, rest }
}

export interface StreamJobOpts {
  from?: number
  signal?: AbortSignal
  onEvent: (ev: SSEEvent) => void
}

// 消费某 job 的 SSE 流。end 事件或流关闭即结束；signal abort 时停止。
export async function streamJob(id: string, opts: StreamJobOpts): Promise<void> {
  const { from, signal, onEvent } = opts
  const qs = from != null ? `?from=${from}` : ''
  const token = getToken()
  const headers: Record<string, string> = {}
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }

  const res = await fetch(`/v1/jobs/${encodeURIComponent(id)}/stream${qs}`, {
    headers,
    signal,
  })

  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）')
  }
  if (!res.ok || !res.body) {
    throw new Error(`stream 请求失败：HTTP ${res.status}`)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        break
      }
      buffer += decoder.decode(value, { stream: true })
      const { frames, rest } = parseSSEChunk(buffer)
      buffer = rest
      for (const frame of frames) {
        onEvent(frame)
        if (frame.type === 'end') {
          return
        }
      }
    }
    // flush 尾部残留
    buffer += decoder.decode()
    if (buffer.length > 0) {
      const { frames } = parseSSEChunk(buffer + '\n\n')
      for (const frame of frames) {
        onEvent(frame)
        if (frame.type === 'end') {
          return
        }
      }
    }
  } finally {
    try {
      reader.releaseLock()
    } catch {
      // ignore
    }
  }
}
