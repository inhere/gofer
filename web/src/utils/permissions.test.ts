import { describe, expect, it } from 'vitest'
import {
  canActivate,
  canRevoke,
  expandPattern,
  expandPatterns,
  grantConfirmRules,
  grantFormBlocker,
  groupGrantsBySubject,
  manageMode,
  matchPattern,
  pendingNewPermissions,
  permissionDeniedMessage,
  sessionGrants,
  auditQuery,
  auditNewestFirst,
} from './permissions'
import type { AuthGrant, AuthWhoami, CatalogPermission } from './permissions'

function perm(name: string, risk: string, session = true): CatalogPermission {
  return { name, description: name, risk, risk_label: risk, session_grantable: session, scope: '*', scope_label: '全部', source: 'code', source_label: '代码' }
}
const cat: CatalogPermission[] = [
  perm('tunnel.open', 'medium'),
  perm('tunnel.manage', 'high', false),
  perm('job.read.others', 'medium'),
  perm('job.cancel.others', 'high'),
  perm('worker.upgrade', 'high', false),
  perm('worker.upgrade.upload', 'dangerous', false),
  perm('auth.manage', 'root', false),
]

function grant(over: Partial<AuthGrant>): AuthGrant {
  return { id: 'g1', subject: 'users', source: 'db', state: 'active', active: true, revocable: true, permissions: [], use_count: 0, ...over }
}
function who(over: Partial<AuthWhoami>): AuthWhoami {
  return { caller_id: 'me', kind: 'user', root: false, permissions: [], grants_version: 1, ...over }
}

describe('matchPattern', () => {
  it('treats * as any run including dots', () => {
    expect(matchPattern('tunnel.*', 'tunnel.open')).toBe(true)
    expect(matchPattern('job.*.others', 'job.read.others')).toBe(true)
    expect(matchPattern('job.*.others', 'job.read')).toBe(false)
    expect(matchPattern('*', 'x.y.z')).toBe(true)
  })
})

describe('expansion preview', () => {
  it('expands a wildcard to matching permissions only', () => {
    const e = expandPattern('tunnel.*', 'users', cat)
    expect(e.permissions.map((p) => p.name)).toEqual(['tunnel.open', 'tunnel.manage'])
    expect(e.problem).toBeUndefined()
  })
  it('never reaches dangerous through a wildcard, and says so', () => {
    const e = expandPattern('worker.*', 'users', cat)
    expect(e.permissions.map((p) => p.name)).toEqual(['worker.upgrade'])
    expect(e.skippedDangerous).toEqual(['worker.upgrade.upload'])
  })
  it('grants dangerous only when written literally', () => {
    const e = expandPattern('worker.upgrade.upload', 'users', cat)
    expect(e.permissions.map((p) => p.name)).toEqual(['worker.upgrade.upload'])
  })
  it('a lone wildcard is not dangerous nor auth.manage', () => {
    const e = expandPattern('*', 'users', cat)
    const names = e.permissions.map((p) => p.name)
    expect(names).not.toContain('auth.manage')
    expect(names).not.toContain('worker.upgrade.upload')
    expect(e.skippedRoot).toBe(true)
  })
  it('auth.manage never matches', () => {
    expect(expandPattern('auth.manage', 'users', cat).problem).toBe('only-root')
  })
  it('session subjects only get session-grantable permissions', () => {
    const e = expandPattern('tunnel.*', 'session', cat)
    expect(e.permissions.map((p) => p.name)).toEqual(['tunnel.open'])
    expect(e.skippedSession).toEqual(['tunnel.manage'])
    expect(expandPattern('tunnel.manage', 'session', cat).problem).toBe('only-session-forbidden')
  })
  it('reports empty and no-match patterns', () => {
    expect(expandPattern('  ', 'users', cat).problem).toBe('empty')
    expect(expandPattern('nope.*', 'users', cat).problem).toBe('no-match')
    expect(expandPattern('worker.*.upload', 'users', cat).problem).toBe('only-dangerous')
  })
  it('merges a multi-pattern preset', () => {
    const e = expandPatterns('tunnel.open, worker.upgrade', 'users', cat)
    expect(e.permissions.map((p) => p.name)).toEqual(['tunnel.open', 'worker.upgrade'])
    expect(expandPatterns('tunnel.open, nope', 'users', cat).problem).toBe('no-match')
  })
})

describe('risk confirmation rules', () => {
  it('low and medium need no confirm', () => {
    const r = grantConfirmRules(expandPattern('tunnel.open', 'users', cat))
    expect(r).toMatchObject({ needsConfirm: false, needsReason: false })
  })
  it('high needs confirm but no reason', () => {
    const r = grantConfirmRules(expandPattern('job.cancel.others', 'users', cat))
    expect(r).toMatchObject({ needsConfirm: true, needsReason: false, high: ['job.cancel.others'] })
  })
  it('dangerous needs confirm and a reason', () => {
    const exp = expandPattern('worker.upgrade.upload', 'users', cat)
    expect(grantConfirmRules(exp)).toMatchObject({ needsConfirm: true, needsReason: true })
    const base = { subject: 'users', pattern: 'worker.upgrade.upload', note: '', confirmed: false }
    expect(grantFormBlocker(base, exp)).toContain('原因')
    expect(grantFormBlocker({ ...base, note: '发版' }, exp)).toContain('确认')
    expect(grantFormBlocker({ ...base, note: '发版', confirmed: true }, exp)).toBe('')
  })
  it('requires a valid subject and pattern', () => {
    const exp = expandPattern('tunnel.open', 'users', cat)
    expect(grantFormBlocker({ subject: 'caller:', pattern: 'tunnel.open', note: '', confirmed: false }, exp)).toContain('对象')
    expect(grantFormBlocker({ subject: 'users', pattern: 'x', note: '', confirmed: false }, expandPattern('x', 'users', cat))).toContain('没有匹配')
  })
})

describe('read-only mode', () => {
  it('is read-only with an explanation when there is no root administrator', () => {
    const m = manageMode(who({ roots: [] }))
    expect(m.readOnly).toBe(true)
    expect(m.canQueryGrants).toBe(false)
    expect(m.reason).toContain('没有根管理员')
  })
  it('is read-only for a non-root user, naming the roots', () => {
    const m = manageMode(who({ roots: ['alice'] }))
    expect(m.readOnly).toBe(true)
    expect(m.reason).toContain('alice')
  })
  it('lets a root administrator manage', () => {
    expect(manageMode(who({ root: true, roots: ['me'] }))).toMatchObject({ canManage: true, readOnly: false, canQueryGrants: true })
  })
  it('treats non-user credentials as read-only', () => {
    expect(manageMode(who({ kind: 'worker' })).canManage).toBe(false)
  })
})

describe('new permissions pending', () => {
  it('reports new permissions only for an active grant', () => {
    expect(pendingNewPermissions(grant({ new_permissions: ['tunnel.manage'] }))).toEqual(['tunnel.manage'])
    expect(pendingNewPermissions(grant({ active: false, state: 'orphaned', new_permissions: ['x'] }))).toEqual([])
    expect(pendingNewPermissions(grant({}))).toEqual([])
  })
  it('counts pending and orphaned per subject group, users first, sessions excluded', () => {
    const groups = groupGrantsBySubject(
      [
        grant({ id: 'a', subject: 'caller:bob', state: 'orphaned', active: false }),
        grant({ id: 'b', subject: 'users', new_permissions: ['job.cancel.others'] }),
        grant({ id: 'c', subject: 'session:s1' }),
      ],
      [{ id: 'd', subject: 'caller:bob', source: 'config', read_only: true, permissions: ['job.read.others'] }],
    )
    expect(groups.map((g) => g.subject)).toEqual(['users', 'caller:bob'])
    expect(groups[0].pending).toBe(1)
    expect(groups[1]).toMatchObject({ orphaned: 1 })
    expect(groups[1].derived).toHaveLength(1)
    expect(sessionGrants([grant({ subject: 'session:s1' }), grant({ id: 'z' })])).toHaveLength(1)
  })
  it('activates only orphaned db grants and revokes only revocable ones', () => {
    expect(canActivate({ state: 'orphaned', source: 'db' })).toBe(true)
    expect(canActivate({ state: 'active', source: 'db' })).toBe(false)
    expect(canRevoke({ revocable: true, source: 'db' })).toBe(true)
    expect(canRevoke({ revocable: true, read_only: true, source: 'config' })).toBe(false)
  })
})

describe('permission-denied message', () => {
  it('names the missing permission and points to 设置 → 权限', () => {
    const m = permissionDeniedMessage(403, 'permission required: tunnel.open, job.submit')
    expect(m).toContain('tunnel.open、job.submit')
    expect(m).toContain('设置 → 权限')
  })
  it('leaves other errors alone', () => {
    expect(permissionDeniedMessage(403, 'not permitted for this caller')).toBeNull()
    expect(permissionDeniedMessage(400, 'permission required: x')).toBeNull()
  })
})

describe('audit helpers', () => {
  it('builds the filter query and sorts newest first', () => {
    expect(auditQuery({ subject: ' users ', permission: '', grant_id: 'g1' })).toBe('subject=users&grant_id=g1&limit=100')
    const sorted = auditNewestFirst([{ at: 1, action: 'a' }, { at: 3, action: 'b' }, { at: 2, action: 'c' }])
    expect(sorted.map((e) => e.action)).toEqual(['b', 'c', 'a'])
  })
})
