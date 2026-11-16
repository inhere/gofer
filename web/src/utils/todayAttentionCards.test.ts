import { describe, expect, it, vi } from 'vitest'

// 设计 2026-11-14 §4/§5：两张新卡的动作接线——轮换走现有轮换接口，打开落到调用方页 /
// 自检页。这里只断言「点了会调哪个接口、带什么参数」与跳转目标，不渲染组件。
const rotateCaller = vi.fn((_id: string): Promise<unknown> => Promise.resolve({}))
vi.mock('../api/callers', () => ({ rotateCaller: (id: string) => rotateCaller(id) }))

const { actionLink, cardLink, runCardAction } = await import('./today')

function card(kind: string, refs: Record<string, string> = {}) {
  return { kind, refs } as never
}

describe('caller.token_expiring card', () => {
  it('rotates the caller named by the action value', async () => {
    const run = runCardAction(card('caller.token_expiring', { caller_id: 'alice' }), { id: 'rotate', label: '轮换', value: 'alice' })
    await run()
    expect(rotateCaller).toHaveBeenCalledWith('alice')
  })

  it('falls back to the card refs when the action carries no value', async () => {
    rotateCaller.mockClear()
    const run = runCardAction(card('caller.token_expiring', { caller_id: 'bob' }), { id: 'rotate', label: '轮换' })
    await run()
    expect(rotateCaller).toHaveBeenCalledWith('bob')
  })

  it('does nothing for the open action', async () => {
    rotateCaller.mockClear()
    await runCardAction(card('caller.token_expiring', { caller_id: 'bob' }), { id: 'open', label: '看详情', value: '/settings/callers' })()
    expect(rotateCaller).not.toHaveBeenCalled()
  })

  it('links to the callers page', () => {
    expect(cardLink(card('caller.token_expiring', { caller_id: 'bob' }))).toBe('/settings/callers')
  })
})

describe('server.health card', () => {
  it('links to the self-check page even though the server value is /settings', () => {
    const c = card('server.health')
    expect(cardLink(c)).toBe('/settings/health')
    expect(actionLink(c, { id: 'open', label: '看详情', value: '/settings' })).toBe('/settings/health')
  })

  it('has no write action', async () => {
    rotateCaller.mockClear()
    await runCardAction(card('server.health'), { id: 'open', label: '看详情', value: '/settings' })()
    expect(rotateCaller).not.toHaveBeenCalled()
  })
})
