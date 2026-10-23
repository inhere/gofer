import { describe, expect, it } from 'vitest'
import { translate } from '../i18n'
import {
  SESSION_GRANT_DEFAULT_EXPIRY_SEC,
  SESSION_GRANT_EXPIRY_OPTIONS,
  badgeTitle,
  buildSessionGrantRequests,
  canGrantSession,
  grantEffectiveLabel,
  orderedSelection,
  selectionPatternText,
  sessionGrantBadge,
  sessionGrantable,
  sessionPresets,
} from './sessionGrant'
import type { AuthGrant, CatalogPermission } from './permissions'

function perm(name: string, risk: string, session: boolean): CatalogPermission {
  return { name, description: name, risk, risk_label: risk, session_grantable: session, scope: '*', scope_label: '全部', source: 'code', source_label: '代码' }
}
const catalog = [
  perm('tunnel.open', 'medium', true),
  perm('tunnel.hosted', 'high', false),
  perm('update.apply', 'high', true),
  perm('job.submit', 'dangerous', false),
  perm('auth.manage', 'root', false),
]

function grant(over: Partial<AuthGrant>): AuthGrant {
  return { id: 'g1', subject: 'session:s1', source: 'db', state: 'active', active: true, revocable: true, permissions: ['tunnel.open'], use_count: 0, session_owner: 'alice', expires_at: 2000, ...over }
}
const sess = { session_id: 's1', state: 'running', caller_id: 'alice' }

describe('session grant dialog logic', () => {
  it('lists only session-grantable permissions', () => {
    expect(sessionGrantable(catalog).map((p) => p.name)).toEqual(['tunnel.open', 'update.apply'])
  })

  it('offers 1 / 4 / 24 hours, within the 24h server cap, default 1 hour', () => {
    expect(SESSION_GRANT_EXPIRY_OPTIONS.map((o) => o.sec)).toEqual([3600, 14400, 86400])
    expect(SESSION_GRANT_DEFAULT_EXPIRY_SEC).toBe(3600)
  })

  it('keeps the selection in catalog order and drops names that cannot go to a session', () => {
    const g = sessionGrantable(catalog)
    expect(orderedSelection(['update.apply', 'tunnel.open', 'job.submit', 'nope'], g)).toEqual(['tunnel.open', 'update.apply'])
    expect(selectionPatternText(['tunnel.open', 'update.apply'])).toBe('tunnel.open, update.apply')
  })

  it('only offers presets that are fully session-grantable', () => {
    const presets = [
      { key: 'tunnel', permissions: ['tunnel.open'] },
      { key: 'release', permissions: ['update.apply', 'worker.upgrade'] },
    ]
    expect(sessionPresets(presets, sessionGrantable(catalog)).map((p) => p.key)).toEqual(['tunnel'])
  })

  it('builds one session-subject request per permission, with expires_sec capped and note trimmed', () => {
    expect(buildSessionGrantRequests('s1', ['tunnel.open', 'update.apply'], 14400, '  发版  ')).toEqual([
      { subject: 'session:s1', pattern: 'tunnel.open', expires_sec: 14400, note: '发版' },
      { subject: 'session:s1', pattern: 'update.apply', expires_sec: 14400, note: '发版' },
    ])
    expect(buildSessionGrantRequests('s1', ['tunnel.open'], 999999, '')[0]).toEqual({ subject: 'session:s1', pattern: 'tunnel.open', expires_sec: 86400 })
    expect(buildSessionGrantRequests('s1', ['tunnel.open'], 0, '')[0].expires_sec).toBe(1)
  })

  it('states the threat model in plain words', () => {
    const note = translate('util.sessionGrant.threat', undefined, 'zh-CN')
    expect(note).toContain('终端会话')
    expect(note).toContain('运行命令')
  })
})

describe('already-granted badge', () => {
  it('shows for an active, unexpired grant of this session', () => {
    const b = sessionGrantBadge([grant({}), grant({ id: 'g2', permissions: ['update.apply'], expires_at: 1500 })], sess, 1000)
    expect(b).toMatchObject({ granted: true, permissions: ['tunnel.open', 'update.apply'], expiresAt: 1500 })
    expect(badgeTitle(b, (n) => `T${n}`)).toContain('T1500')
  })
  it('ignores other sessions, revoked, expired, orphaned grants', () => {
    const rows = [
      grant({ subject: 'session:other' }),
      grant({ id: 'r', state: 'revoked', active: false }),
      grant({ id: 'e', expires_at: 900 }),
      grant({ id: 'o', state: 'orphaned', active: false }),
    ]
    expect(sessionGrantBadge(rows, sess, 1000).granted).toBe(false)
  })
  it('does not show once the session ended or changed owner', () => {
    expect(sessionGrantBadge([grant({})], { ...sess, state: 'ended' }, 1000).granted).toBe(false)
    expect(sessionGrantBadge([grant({})], { ...sess, caller_id: 'bob' }, 1000).granted).toBe(false)
  })
  it('offers the grant button to a manager on a live session only', () => {
    expect(canGrantSession(true, { state: 'running' })).toBe(true)
    expect(canGrantSession(true, { state: 'ended' })).toBe(false)
    expect(canGrantSession(false, { state: 'running' })).toBe(false)
    expect(canGrantSession(true, null)).toBe(false)
  })
})

describe('server-decided effective flag', () => {
  it('trusts effective=false for a revived session the client cannot tell from a live one', () => {
    expect(sessionGrantBadge([grant({ effective: false })], sess, 1000).granted).toBe(false)
    expect(sessionGrantBadge([grant({ effective: true })], sess, 1000).granted).toBe(true)
  })
  it('effective wins over the client-side guess in both directions', () => {
    // 客户端会判成不生效（过期），但服务端说生效 —— 以服务端为准
    expect(sessionGrantBadge([grant({ expires_at: 900, effective: true })], sess, 1000).granted).toBe(true)
  })
  it('still only matches grants of this session', () => {
    expect(sessionGrantBadge([grant({ subject: 'session:other', effective: true })], sess, 1000).granted).toBe(false)
  })
  it('labels the settings list row from effective, falling back when absent', () => {
    expect(grantEffectiveLabel(grant({ effective: true }), 1000)).toEqual({ text: '生效中', effective: true })
    expect(grantEffectiveLabel(grant({ effective: false }), 1000)).toEqual({ text: '未生效', effective: false })
    expect(grantEffectiveLabel(grant({}), 1000).effective).toBe(true)
    expect(grantEffectiveLabel(grant({ expires_at: 900 }), 1000).effective).toBe(false)
    expect(grantEffectiveLabel(grant({ state: 'revoked', active: false }), 1000).effective).toBe(false)
  })
})
