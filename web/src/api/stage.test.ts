import { afterEach, describe, expect, it, vi } from 'vitest'

const calls: Array<{ url: string; init?: RequestInit }> = []
vi.mock('./client', () => ({
  request: vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    return { advanced: true, gate: { step: 'review', ok: true, missing: [] }, stage: { work_item_id: 'w-1' } }
  }),
}))

import { getWorkStage, stageApprove, stageBack, stageDone, stageSetLevel } from './stage'

afterEach(() => {
  calls.length = 0
})

describe('stage api', () => {
  it('reads the flow of one work item', async () => {
    await getWorkStage('w-1')
    expect(calls[0].url).toBe('/v1/work-items/w-1/stage')
    expect(calls[0].init?.method).toBeUndefined()
  })

  it('advances with done and approves with approve', async () => {
    await stageDone('w-1')
    await stageApprove('w-1')
    expect(calls.map((c) => `${c.init?.method} ${c.url}`)).toEqual([
      'POST /v1/work-items/w-1/stage/done',
      'POST /v1/work-items/w-1/stage/approve',
    ])
    // 两个动作都没有请求体（谁在请求由服务端从凭据判断）
    expect(calls[0].init?.body).toBeUndefined()
  })

  it('sends the typed reason with back', async () => {
    await stageBack('w-1', '设计稿还缺验收标准')
    expect(calls[0].url).toBe('/v1/work-items/w-1/stage/back')
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ reason: '设计稿还缺验收标准' })
  })

  it('switches the autonomy level', async () => {
    await stageSetLevel('w-1', 'remind')
    expect(calls[0].url).toBe('/v1/work-items/w-1/stage/level')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ level: 'remind' })
  })

  it('escapes the work item id in the path', async () => {
    await getWorkStage('w/1')
    expect(calls[0].url).toBe('/v1/work-items/w%2F1/stage')
  })
})
