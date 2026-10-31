// 统一请求出口都带 Accept-Language: <当前 locale>（S3a）：服务端据此选择人读文本的语言。
// 切换语言后，下一个请求带新值；调用方显式给了头就不覆盖。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

interface Seen {
  url: string
  headers: Record<string, string>
}

function mockFetch(): Seen[] {
  const seen: Seen[] = []
  vi.stubGlobal('sessionStorage', { getItem: () => null })
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      seen.push({ url, headers: { ...((init?.headers as Record<string, string>) ?? {}) } })
      const body = new ReadableStream({
        start(c) {
          c.enqueue(new TextEncoder().encode('event: end\ndata: {}\n\n'))
          c.close()
        },
      })
      return {
        ok: true,
        status: 200,
        body,
        text: async () => '{}',
        json: async () => ({ ticket: 'tk' }),
        headers: new Headers(),
      } as unknown as Response
    }),
  )
  return seen
}

beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())

describe('Accept-Language on web requests', () => {
  it('request() sends the current locale and follows a language switch', async () => {
    const seen = mockFetch()
    const { locale } = await import('../store/locale')
    const { request } = await import('./client')
    locale.value = 'zh-CN'
    await request('/v1/projects')
    locale.value = 'en-US'
    await request('/v1/projects')
    expect(seen[0].headers['Accept-Language']).toBe('zh-CN')
    expect(seen[1].headers['Accept-Language']).toBe('en-US')
  })

  it('keeps a caller-supplied Accept-Language', async () => {
    const seen = mockFetch()
    const { request } = await import('./client')
    await request('/v1/projects', { headers: { 'Accept-Language': 'en' } })
    expect(seen[0].headers['Accept-Language']).toBe('en')
  })

  it('the job log SSE stream (fetch based) sends it too', async () => {
    const seen = mockFetch()
    const { locale } = await import('../store/locale')
    const { streamJob } = await import('./sse')
    locale.value = 'en-US'
    await streamJob('j1', { tail: 10, onEvent: () => {} })
    expect(seen[0].headers['Accept-Language']).toBe('en-US')
  })

  it('the attachment fetch sends it', async () => {
    const seen = mockFetch()
    const { locale } = await import('../store/locale')
    const { fetchAttachmentBlob } = await import('./attachments')
    locale.value = 'zh-CN'
    await fetchAttachmentBlob('a1').catch(() => {})
    expect(seen[0].headers['Accept-Language']).toBe('zh-CN')
  })
})
