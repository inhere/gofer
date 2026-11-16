import { describe, expect, it, vi } from 'vitest'

// 设计 2026-11-14 §4/§5：两张新卡的动作接线。轮换不在卡片上直接调接口——轮换会让旧 token 立刻
// 失效，而新 token 只在这次响应里出现一次，卡片上换掉它等于把新 token 丢掉；所以「轮换」是跳转，
// 目标是调用方页并带上要轮换的调用方。这里只断言「点了会调哪个接口、带什么参数」与跳转目标，
// 不渲染组件。
const rotateCaller = vi.fn((_id: string): Promise<unknown> => Promise.resolve({}))
vi.mock('../api/callers', () => ({ rotateCaller: (id: string) => rotateCaller(id) }))

const { actionLink, cardLink, isLinkAction, runCardAction } = await import('./today')

function card(kind: string, refs: Record<string, string> = {}) {
  return { kind, refs } as never
}

describe('caller.token_expiring card', () => {
  it('links the rotate action to the callers page with the caller to rotate', () => {
    const c = card('caller.token_expiring', { caller_id: 'alice' })
    const a = { id: 'rotate', label: '轮换', value: 'alice' }
    expect(isLinkAction(c, a)).toBe(true)
    expect(actionLink(c, a)).toBe('/settings/callers?rotate=alice')
  })

  it('falls back to the card refs when the action carries no value, and encodes the id', () => {
    const c = card('caller.token_expiring', { caller_id: 'a/b c' })
    expect(actionLink(c, { id: 'rotate', label: '轮换', value: '' })).toBe('/settings/callers?rotate=a%2Fb%20c')
    // 连 id 都没有时退回调用方页（页面上没有可打开的调用方）
    expect(actionLink(card('caller.token_expiring'), { id: 'rotate', label: '轮换' })).toBe('/settings/callers')
  })

  it('never rotates from the card: the new token would be lost', async () => {
    rotateCaller.mockClear()
    await runCardAction(card('caller.token_expiring', { caller_id: 'alice' }), { id: 'rotate', label: '轮换', value: 'alice' })()
    expect(rotateCaller).not.toHaveBeenCalled()
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
