import type { AttachServerFrame } from './types'

export function encodeInput(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let bin = ''
  for (const x of bytes) {
    bin += String.fromCharCode(x)
  }
  return btoa(bin)
}

export function buildAttachWsUrl(id: string, ticket: string): string {
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const path = `/v1/jobs/${encodeURIComponent(id)}/attach`
  const qs = `?ticket=${encodeURIComponent(ticket)}`
  return `${protocol}//${location.host}${path}${qs}`
}

export function parseServerFrame(data: string): AttachServerFrame | null {
  let frame: unknown
  try {
    frame = JSON.parse(data)
  } catch {
    return null
  }
  if (!frame || typeof frame !== 'object') {
    return null
  }
  const obj = frame as Record<string, unknown>
  if (obj.t === 'hello') {
    if (
      typeof obj.write === 'boolean' &&
      typeof obj.cols === 'number' &&
      typeof obj.rows === 'number'
    ) {
      return {
        t: 'hello',
        write: obj.write,
        cols: obj.cols,
        rows: obj.rows,
      }
    }
    return null
  }
  if (obj.t === 'x') {
    if (obj.code == null) {
      return { t: 'x' }
    }
    if (typeof obj.code === 'number') {
      return { t: 'x', code: obj.code }
    }
  }
  return null
}
