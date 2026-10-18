import { describe, expect, it } from 'vitest'
import {
  RENEW_DEFAULT_EXPIRY_SEC,
  RENEW_EXPIRY_OPTIONS,
  RENEW_MAX_SEC,
  RENEW_MIN_SEC,
  RENEW_SOON_SEC,
  badgeRenewIds,
  buildGrantRenewRequest,
  canRenewBadge,
  canRenewGrant,
  grantRemainingSec,
  grantRemainingText,
  grantRenewState,
  renewFailureText,
} from './grantRenew'
import { SESSION_GRANT_EXPIRY_OPTIONS, sessionGrantBadge } from './sessionGrant'
import type { AuthGrant } from './permissions'

function grant(over: Partial<AuthGrant>): AuthGrant {
  return {
    id: 'g1',
    subject: 'session:s1',
    source: 'db',
    state: 'active',
    active: true,
    effective: true,
    revocable: true,
    permissions: ['tunnel.open'],
    use_count: 0,
    session_owner: 'alice',
    expires_at: 2800,
    ...over,
  }
}
const sess = { session_id: 's1', state: 'running', caller_id: 'alice' }

describe('renew expiry options', () => {
  it('offers exactly the options used when granting, within the 24h server cap', () => {
    expect(RENEW_EXPIRY_OPTIONS).toBe(SESSION_GRANT_EXPIRY_OPTIONS)
    expect(RENEW_EXPIRY_OPTIONS.map((o) => o.sec)).toEqual([3600, 14400, 86400])
    expect(RENEW_DEFAULT_EXPIRY_SEC).toBe(3600)
    expect(RENEW_MAX_SEC).toBe(86400)
    expect(RENEW_MIN_SEC).toBe(1)
  })
})

describe('remaining time', () => {
  it('counts whole seconds down to zero', () => {
    expect(grantRemainingSec(2000, 1000)).toBe(1000)
    expect(grantRemainingSec(900, 1000)).toBe(0)
    expect(grantRemainingSec(undefined, 1000)).toBe(0)
  })
  it('reads in seconds, minutes, hours (and hours + minutes)', () => {
    expect(grantRemainingText(1030, 1000)).toBe('剩余 30 秒')
    expect(grantRemainingText(2000, 1000)).toBe('剩余 16 分钟')
    expect(grantRemainingText(4599, 1000)).toBe('剩余 59 分钟')
    expect(grantRemainingText(4600, 1000)).toBe('剩余 1 小时')
    expect(grantRemainingText(5500, 1000)).toBe('剩余 1 小时 15 分钟')
    expect(grantRemainingText(1000, 1000)).toBe('已到期')
    expect(grantRemainingText(undefined, 1000)).toBe('')
  })
})

describe('who may be renewed', () => {
  it('offers a renewal for a grant that is in force, and warns when it is nearly up', () => {
    const s = grantRenewState(grant({ expires_at: 2800 }), 1000)
    expect(s).toEqual({ canRenew: true, remainingSec: 1800, soon: true, reason: '' })
    expect(RENEW_SOON_SEC).toBe(3600)
    const later = grantRenewState(grant({ expires_at: 8200 }), 1000)
    expect(later).toMatchObject({ canRenew: true, remainingSec: 7200, soon: false })
  })
  it('trusts the server effective flag over the client-side guess', () => {
    expect(grantRenewState(grant({ effective: true, expires_at: 1500 }), 1000).canRenew).toBe(true)
    expect(grantRenewState(grant({ effective: false }), 1000).canRenew).toBe(false)
  })
  it('refuses a revoked, expired, unmatched or paused row and names the reason', () => {
    const revoked = grantRenewState(grant({ state: 'revoked', active: false, effective: false }), 1000)
    expect(revoked.canRenew).toBe(false)
    expect(revoked.reason).toContain('已撤销')
    expect(revoked.reason).toContain('重新授权')
    expect(grantRenewState(grant({ state: 'expired', active: false, effective: false, expires_at: 900 }), 1000).reason).toContain('已过期')
    expect(grantRenewState(grant({ state: 'unmatched', active: false, effective: false }), 1000).reason).toContain('模式')
    expect(grantRenewState(grant({ state: 'orphaned', active: false, effective: false }), 1000).reason).toContain('调用方')
  })
  it('blames the session half when the row looks fine but the server says it is not in force', () => {
    const s = grantRenewState(grant({ effective: false }), 1000)
    expect(s.canRenew).toBe(false)
    expect(s.reason).toContain('会话已结束')
    expect(s.reason).toContain('重新授权')
  })
  it('has nothing to renew on a users / caller subject', () => {
    expect(grantRenewState(grant({ subject: 'users' }), 1000).canRenew).toBe(false)
    expect(grantRenewState(grant({ subject: 'caller:bob' }), 1000).reason).toContain('只有会话授权')
  })
  it('shows the entry to a manager only', () => {
    expect(canRenewGrant(true, grant({}), 1000)).toBe(true)
    expect(canRenewGrant(false, grant({}), 1000)).toBe(false)
    expect(canRenewGrant(true, grant({ effective: false }), 1000)).toBe(false)
  })
})

describe('badge renewal entry', () => {
  const soon = sessionGrantBadge([grant({ expires_at: 2800 })], sess, 1000)
  const later = sessionGrantBadge([grant({ expires_at: 8200 })], sess, 1000)
  it('appears only under an hour left, and only for a manager', () => {
    expect(canRenewBadge(true, soon, 1000)).toBe(true)
    expect(canRenewBadge(false, soon, 1000)).toBe(false)
    expect(canRenewBadge(true, later, 1000)).toBe(false)
    expect(canRenewBadge(true, sessionGrantBadge([], sess, 1000), 1000)).toBe(false)
  })
  it('extends every grant the badge holds, each on its own deadline', () => {
    const b = sessionGrantBadge(
      [grant({ id: 'g1', expires_at: 2800 }), grant({ id: 'g2', permissions: ['update.apply'], expires_at: 3400 })],
      sess,
      1000,
    )
    expect(badgeRenewIds(b)).toEqual(['g1', 'g2'])
  })
})

describe('renew request', () => {
  it('sends the expiry alone, clamped to the server range', () => {
    expect(buildGrantRenewRequest('g1', 14400)).toEqual({ id: 'g1', expires_sec: 14400 })
    expect(buildGrantRenewRequest('g1', 1800.7)).toEqual({ id: 'g1', expires_sec: 1800 })
    expect(buildGrantRenewRequest('g1', 0).expires_sec).toBe(RENEW_MIN_SEC)
    expect(buildGrantRenewRequest('g1', 999999).expires_sec).toBe(RENEW_MAX_SEC)
  })
})

describe('failure text', () => {
  it('points at auth.manage, at re-granting, and keeps the server reason', () => {
    expect(renewFailureText(403, 'permission required: auth.manage')).toContain('auth.manage')
    const conflict = renewFailureText(409, 'the grant is not in force - session s1 has ended: grant it again instead')
    expect(conflict).toContain('重新授权')
    expect(conflict).toContain('has ended')
    expect(renewFailureText(400, 'invalid expires_sec - must be between 1 and 86400 seconds')).toContain('续期被拒')
    expect(renewFailureText(404, 'unknown grant - grant g1 does not exist')).toContain('续期被拒')
    expect(renewFailureText(500, 'boom')).toBe('boom')
  })
})
