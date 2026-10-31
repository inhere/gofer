import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { JobPreview } from '../api/preview'

const TOKEN = 'tok-SECRET-preview'
const URL_ONCE = `http://srv.test:18100/?token=${TOKEN}`

// 存储与 fetch 的哨兵：预览 URL 不能进任何 storage，也不能被当 API 请求发出去（会被加 Bearer）。
const storageWrites: string[] = []
const fetchCalls: string[] = []

beforeEach(() => {
  storageWrites.length = 0
  fetchCalls.length = 0
  const store = {
    getItem: () => null,
    setItem: (k: string, v: string) => void storageWrites.push(`${k}=${v}`),
    removeItem: () => {},
  }
  vi.stubGlobal('localStorage', store)
  vi.stubGlobal('sessionStorage', store)
  vi.stubGlobal('fetch', vi.fn(async (u: string) => {
    fetchCalls.push(String(u))
    throw new Error('unexpected fetch')
  }))
  vi.resetModules()
})
afterEach(() => vi.unstubAllGlobals())

const row = (over: Partial<JobPreview> = {}): JobPreview => ({
  id: 'pv1', job_id: 'j1', port: 18100, target_port: 3000, state: 'active', expires_at: 1900000000, ...over,
})

async function setup(rows: JobPreview[] = []) {
  const mod = await import('./jobPreviews')
  let current = rows
  const api = {
    list: vi.fn(async () => current),
    create: vi.fn(async (_job: string, body: { port: number; ttl_sec?: number }) => {
      const created = row({ port: 18100, target_port: body.port, url: URL_ONCE })
      current = [{ ...created, url: undefined }, ...current]
      return created
    }),
    revoke: vi.fn(async (_job: string, id: string) => {
      current = current.map((p) => (p.id === id ? { ...p, state: 'closed' } : p))
      return { id, port: 0, expires_at: 0, state: 'closed' }
    }),
    renew: vi.fn(async (_job: string, id: string, ttl: number) => {
      current = current.map((p) => (p.id === id ? { ...p, expires_at: p.expires_at + ttl } : p))
      return row({ id })
    }),
  }
  const opened: string[] = []
  const ctl = mod.useJobPreviews(() => 'j1', { api, openTab: (u) => void opened.push(u) })
  return { mod, api, ctl, opened }
}

describe('preview helpers', () => {
  it('validates the port as an integer in 1..65535', async () => {
    const { parsePreviewPort } = await import('./jobPreviews')
    expect(parsePreviewPort('3000')).toBe(3000)
    expect(parsePreviewPort(' 65535 ')).toBe(65535)
    for (const bad of ['', '0', '65536', '-1', '3.5', 'abc', '99999999']) expect(parsePreviewPort(bad), bad).toBeNull()
  })

  it('truncates host:port in the middle and keeps the port', async () => {
    const { previewHostLabel, previewUrlHostLabel } = await import('./jobPreviews')
    expect(previewHostLabel('srv.test', 18100)).toBe('srv.test:18100')
    const long = previewHostLabel('a-very-long-internal-hostname.example.test', 18100, 24)
    expect(long.length).toBeLessThanOrEqual(24)
    expect(long).toContain('…')
    expect(long.endsWith(':18100')).toBe(true)
    // 从创建响应的 url 取 host 时丢掉 token
    expect(previewUrlHostLabel(URL_ONCE)).toBe('srv.test:18100')
    expect(previewUrlHostLabel(URL_ONCE)).not.toContain(TOKEN)
    expect(previewUrlHostLabel('not a url')).toBe('')
  })

  it('maps lease states to status families and knows which jobs can preview', async () => {
    const { previewStateFamily, previewJobRunning, previewActive } = await import('./jobPreviews')
    expect(previewStateFamily('active')).toBe('run')
    expect(previewStateFamily('expired')).toBe('time')
    expect(previewStateFamily('closed')).toBe('idle')
    expect(previewStateFamily('job_ended')).toBe('idle')
    expect(previewStateFamily(undefined)).toBe('idle')
    expect(previewActive(row())).toBe(true)
    expect(previewActive(row({ state: 'closed' }))).toBe(false)
    for (const s of ['running', 'queued', 'awaiting_input', 'recovering']) expect(previewJobRunning(s), s).toBe(true)
    for (const s of ['done', 'failed', 'cancelled', 'needs_review', '', undefined]) expect(previewJobRunning(s as string), String(s)).toBe(false)
  })

  it('shows the localized ApiError detail, else the message', async () => {
    const { previewErrorText } = await import('./jobPreviews')
    const { ApiError } = await import('../api/client')
    expect(previewErrorText(new ApiError(409, 'preview runner unsupported - x', 'Preview only works for local jobs'))).toBe('Preview only works for local jobs')
    expect(previewErrorText(new ApiError(404, 'preview disabled'))).toBe('preview disabled')
    expect(previewErrorText(new Error('boom'))).toBe('boom')
    expect(previewErrorText('plain')).toBe('plain')
  })
})

describe('useJobPreviews', () => {
  it('creates a preview, lists it without the url and keeps the one-time url in memory only', async () => {
    const { ctl, api } = await setup()
    expect(await ctl.create(3000, 1800)).toBe(true)
    expect(api.create).toHaveBeenCalledWith('j1', { port: 3000, ttl_sec: 1800 })
    expect(ctl.notice.value).toBe('created')
    expect(ctl.items.value).toHaveLength(1)
    expect(JSON.stringify(ctl.items.value)).not.toContain(TOKEN)
    expect(ctl.openUrls.value.pv1).toBe(URL_ONCE)
    expect(storageWrites).toEqual([])
  })

  it('opens the url in a new tab once: no fetch (so no Bearer), no storage write, then the url is gone', async () => {
    const { ctl, opened } = await setup()
    await ctl.create(3000, 1800)
    expect(ctl.open('pv1')).toBe(true)
    expect(opened).toEqual([URL_ONCE])
    expect(ctl.openUrls.value.pv1).toBeUndefined()
    expect(ctl.open('pv1')).toBe(false)
    expect(opened).toHaveLength(1)
    expect(fetchCalls).toEqual([])
    expect(storageWrites).toEqual([])
  })

  it('can open right after creating', async () => {
    const { ctl, opened } = await setup()
    await ctl.create(3000, 1800, true)
    expect(opened).toEqual([URL_ONCE])
  })

  it('needs a second confirmation to revoke, and cancelling revokes nothing', async () => {
    const { ctl, api } = await setup([row()])
    await ctl.load()
    ctl.askRevoke('pv1')
    expect(ctl.pendingRevoke.value).toBe('pv1')
    expect(api.revoke).not.toHaveBeenCalled()
    ctl.cancelRevoke()
    expect(ctl.pendingRevoke.value).toBe('')
    expect(await ctl.confirmRevoke()).toBe(false)
    expect(api.revoke).not.toHaveBeenCalled()

    ctl.askRevoke('pv1')
    expect(await ctl.confirmRevoke()).toBe(true)
    expect(api.revoke).toHaveBeenCalledWith('j1', 'pv1')
    expect(ctl.notice.value).toBe('revoked')
    expect(ctl.pendingRevoke.value).toBe('')
    expect(ctl.items.value[0].state).toBe('closed')
  })

  it('drops the one-time url of a revoked preview', async () => {
    const { ctl } = await setup()
    await ctl.create(3000, 1800)
    ctl.askRevoke('pv1')
    await ctl.confirmRevoke()
    expect(ctl.openUrls.value.pv1).toBeUndefined()
  })

  it('renews with the chosen ttl in seconds and reloads', async () => {
    const { ctl, api } = await setup([row()])
    await ctl.load()
    expect(await ctl.renew('pv1', 3600)).toBe(true)
    expect(api.renew).toHaveBeenCalledWith('j1', 'pv1', 3600)
    expect(ctl.notice.value).toBe('renewed')
    expect(ctl.items.value[0].expires_at).toBe(1900003600)
    expect(ctl.busy.value.size).toBe(0)
  })

  it('shows the interface error (job not running / runner unsupported / disabled) and creates nothing', async () => {
    const { ApiError } = await import('../api/client')
    const { useJobPreviews } = await import('./jobPreviews')
    for (const detail of ['The job is not running', 'runner w-box is not supported yet', 'Job preview is not enabled']) {
      const api = {
        list: vi.fn(async () => []),
        create: vi.fn(async () => {
          throw new ApiError(409, 'x', detail)
        }),
        revoke: vi.fn(),
        renew: vi.fn(),
      }
      const ctl = useJobPreviews(() => 'j1', { api })
      expect(await ctl.create(3000, 60)).toBe(false)
      expect(ctl.error.value).toBe(detail)
      expect(ctl.items.value).toEqual([])
      expect(ctl.creating.value).toBe(false)
      expect(Object.keys(ctl.openUrls.value)).toEqual([])
    }
  })

  it('reports a failed refresh without clearing the list', async () => {
    const { mod } = await setup()
    const rows = [row()]
    const api = {
      list: vi.fn().mockResolvedValueOnce(rows).mockRejectedValueOnce(new Error('network down')),
      create: vi.fn(),
      revoke: vi.fn(),
      renew: vi.fn(),
    }
    const ctl = mod.useJobPreviews(() => 'j1', { api })
    await ctl.load()
    await ctl.load()
    expect(ctl.error.value).toBe('network down')
    expect(ctl.items.value).toEqual(rows)
  })

  it('forgets a one-time url once the preview is no longer active in the list', async () => {
    const { ctl, api } = await setup()
    await ctl.create(3000, 1800)
    api.list.mockResolvedValueOnce([row({ state: 'expired' })])
    await ctl.load()
    expect(ctl.openUrls.value.pv1).toBeUndefined()
  })
})
