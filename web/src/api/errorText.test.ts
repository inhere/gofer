// 错误体的 code → 前端字典 err.<code>（S3c）：有 code 且字典里有这句话就按当前界面语言渲染，
// 没有再显示服务端的 detail；error 短语不翻译。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { dicts, translate } from '../i18n'
import { errors } from '../i18n/pages/errors'
import { localizeErrorDetail } from './errorText'

function mockErrorFetch(status: number, body: unknown) {
  vi.stubGlobal('sessionStorage', { getItem: () => null })
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({
      ok: false,
      status,
      headers: new Headers(),
      json: async () => body,
      text: async () => JSON.stringify(body),
    }) as unknown as Response),
  )
}

beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())

describe('err.* dictionary', () => {
  it('has the same codes in zh and en, with the same placeholders', () => {
    expect(Object.keys(errors.en).sort()).toEqual(Object.keys(errors.zh).sort())
    const names = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort()
    for (const key of Object.keys(errors.zh)) {
      expect(names(errors.en[key]), key).toEqual(names(errors.zh[key]))
    }
  })

  it('is merged into both locale dictionaries and the English text has no CJK', () => {
    for (const key of Object.keys(errors.zh)) {
      expect(dicts['zh-CN'][key]).toBe(errors.zh[key])
      expect(dicts['en-US'][key]).toBe(errors.en[key])
      expect(errors.en[key]).not.toMatch(/\p{Script=Han}/u)
    }
  })
})

describe('localizeErrorDetail', () => {
  it('renders err.<code> with the params in the requested locale', () => {
    const params = { worker: 'w1', have: 3, need: 5 }
    const key = 'err.messenger_worker_too_old'
    expect(translate(key, params, 'zh-CN')).toBe('worker w1 的协议版本是 v3，列出会话需要 v5 及以上；请先升级该 worker（gofer worker upgrade）')
    expect(translate(key, params, 'en-US')).toContain('Worker w1 speaks protocol v3')
  })

  it('falls back to the server detail when the code is unknown or absent', () => {
    expect(localizeErrorDetail('no_such_code', {}, 'server text')).toBe('server text')
    expect(localizeErrorDetail(undefined, undefined, 'server text')).toBe('server text')
    expect(localizeErrorDetail(undefined, undefined, undefined)).toBeUndefined()
  })

  it('uses the current locale and ignores params that are not strings or numbers', async () => {
    const { locale } = await import('../store/locale')
    const { localizeErrorDetail: localize } = await import('./errorText')
    locale.value = 'en-US'
    expect(localize('workbench_turn_undeliverable', { reason: 'the session has ended', cause: 'x', extra: { a: 1 } }, 'ignored')).toBe(
      'The session cannot receive messages right now: the session has ended (x)',
    )
    locale.value = 'zh-CN'
    expect(localize('workbench_turn_ended', undefined, 'ignored')).toBe('会话当前无法接收消息：会话已结束')
  })
})

describe('ApiError from a keyed error body', () => {
  it('shows the dictionary sentence, keeps the error phrase and exposes errCode / errParams', async () => {
    mockErrorFetch(409, { error: 'worker too old', code: 'messenger_worker_too_old', params: { worker: 'w1', have: 3, need: 5 }, detail: '服务端原文' })
    const { locale } = await import('../store/locale')
    const { request, ApiError } = await import('./client')
    locale.value = 'en-US'
    const err = await request('/v1/runners/r/messenger/agents').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    const e = err as InstanceType<typeof ApiError>
    expect(e.status).toBe(409)
    expect(e.code).toBe('worker too old') // 历史用法：code 存 error 短语
    expect(e.errCode).toBe('messenger_worker_too_old')
    expect(e.errParams).toEqual({ worker: 'w1', have: 3, need: 5 })
    expect(e.detail).toContain('Worker w1 speaks protocol v3')
    expect(e.message).toBe(`worker too old - ${e.detail}`)
  })

  it('keeps the server detail for a body without a code or with an unknown one', async () => {
    mockErrorFetch(400, { error: 'invalid request body', detail: 'unexpected EOF' })
    const { request } = await import('./client')
    const plain = await request('/v1/x').catch((e: unknown) => e as { detail?: string; errCode?: string; message: string })
    expect(plain).toMatchObject({ detail: 'unexpected EOF', message: 'invalid request body - unexpected EOF' })
    expect((plain as { errCode?: string }).errCode).toBeUndefined()

    vi.resetModules()
    mockErrorFetch(400, { error: 'x', code: 'brand_new_code', detail: '服务端原文' })
    const { request: request2 } = await import('./client')
    const unknown = await request2('/v1/x').catch((e: unknown) => e as { detail?: string; errCode?: string })
    expect(unknown).toMatchObject({ detail: '服务端原文', errCode: 'brand_new_code' })
  })
})
