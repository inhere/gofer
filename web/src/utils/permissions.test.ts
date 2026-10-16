import { describe, expect, it } from 'vitest'
import {
  canActivate,
  canRevoke,
  emptyExpandResult,
  expandProblemText,
  grantConfirmRules,
  grantFormBlocker,
  groupGrantsBySubject,
  isExpandPreview,
  manageMode,
  mergeExpandPreviews,
  splitPatterns,
  pendingNewPermissions,
  permissionDeniedMessage,
  sessionGrants,
  auditQuery,
  auditNewestFirst,
} from './permissions'
import type { AuthGrant, AuthWhoami, ExpandPreview } from './permissions'

function grant(over: Partial<AuthGrant>): AuthGrant {
  return { id: 'g1', subject: 'users', source: 'db', state: 'active', active: true, revocable: true, permissions: [], use_count: 0, ...over }
}
function who(over: Partial<AuthWhoami>): AuthWhoami {
  return { caller_id: 'me', kind: 'user', root: false, permissions: [], grants_version: 1, ...over }
}

function pv(pattern: string, perms: [string, string][], excluded: [string, string][] = [], problem?: string): ExpandPreview {
  return {
    pattern,
    subject: 'users',
    kind: 'users',
    permissions: perms.map(([name, risk]) => ({ name, description: name, risk, risk_label: risk, session_grantable: true })),
    excluded: excluded.map(([name, reason]) => ({ name, risk: 'x', reason })),
    ...(problem ? { problem } : {}),
  }
}

// 展开规则在服务端（GET /v1/auth/expand）；这里只测把回答整理成界面形状。
describe('expansion preview shaping', () => {
  it('turns a server answer into the UI shape', () => {
    const e = mergeExpandPreviews([pv('worker.*', [['worker.upgrade', 'high']], [['worker.upgrade.upload', 'dangerous_wildcard'], ['auth.manage', 'root_only']])])
    expect(e.permissions.map((p) => p.name)).toEqual(['worker.upgrade'])
    expect(e.skippedDangerous).toEqual(['worker.upgrade.upload'])
    expect(e.skippedRoot).toBe(true)
    expect(e.problem).toBeUndefined()
  })
  it('collects the session-forbidden exclusions', () => {
    const e = mergeExpandPreviews([pv('tunnel.*', [['tunnel.open', 'medium']], [['tunnel.manage.others', 'not_session_grantable']])])
    expect(e.skippedSession).toEqual(['tunnel.manage.others'])
  })
  it('no previews is the empty state', () => {
    expect(mergeExpandPreviews([]).problem).toBe('empty')
  })
  it('merges a multi-pattern preset, dedupes, and carries a failing pattern problem', () => {
    const e = mergeExpandPreviews([pv('a', [['tunnel.open', 'medium']]), pv('b', [['tunnel.open', 'medium'], ['worker.upgrade', 'high']])])
    expect(e.permissions.map((p) => p.name)).toEqual(['tunnel.open', 'worker.upgrade'])
    const bad = mergeExpandPreviews([pv('a', [['tunnel.open', 'medium']]), pv('nope', [], [], 'no_match')])
    expect(bad.problem).toBe('no_match')
  })
  it('recognises the 400 body of the preview endpoint', () => {
    expect(isExpandPreview(pv('x', [], [], 'no_match'))).toBe(true)
    expect(isExpandPreview({ error: 'invalid subject' })).toBe(false)
    expect(isExpandPreview(null)).toBe(false)
  })
  it('has text for every problem', () => {
    for (const p of ['empty', 'loading', 'no_match', 'only_root', 'only_dangerous_wildcard', 'only_not_session_grantable']) {
      expect(expandProblemText(p)).not.toBe('')
    }
    expect(expandProblemText('unavailable', 'boom')).toContain('boom')
    expect(expandProblemText(undefined)).toBe('')
  })
  it('splits patterns on commas and whitespace', () => {
    expect(splitPatterns('update.*, worker.upgrade  tunnel.open')).toEqual(['update.*', 'worker.upgrade', 'tunnel.open'])
  })
})

describe('risk confirmation rules', () => {
  const exp = (perms: [string, string][]) => mergeExpandPreviews([pv('p', perms)])
  it('low and medium need no confirm', () => {
    expect(grantConfirmRules(exp([['tunnel.open', 'medium']]))).toMatchObject({ needsConfirm: false, needsReason: false })
  })
  it('high needs confirm but no reason', () => {
    expect(grantConfirmRules(exp([['job.cancel.others', 'high']]))).toMatchObject({ needsConfirm: true, needsReason: false, high: ['job.cancel.others'] })
  })
  it('dangerous needs confirm and a reason', () => {
    const e = exp([['worker.upgrade.upload', 'dangerous']])
    expect(grantConfirmRules(e)).toMatchObject({ needsConfirm: true, needsReason: true })
    const base = { subject: 'users', pattern: 'worker.upgrade.upload', note: '', confirmed: false }
    expect(grantFormBlocker(base, e)).toContain('原因')
    expect(grantFormBlocker({ ...base, note: '发版' }, e)).toContain('确认')
    expect(grantFormBlocker({ ...base, note: '发版', confirmed: true }, e)).toBe('')
  })
  it('requires a valid subject, a settled preview and a matching pattern', () => {
    const e = exp([['tunnel.open', 'medium']])
    expect(grantFormBlocker({ subject: 'caller:', pattern: 'tunnel.open', note: '', confirmed: false }, e)).toContain('对象')
    expect(grantFormBlocker({ subject: 'users', pattern: 'x', note: '', confirmed: false }, mergeExpandPreviews([pv('x', [], [], 'no_match')]))).toContain('没有匹配')
    expect(grantFormBlocker({ subject: 'users', pattern: 'x', note: '', confirmed: false }, emptyExpandResult('loading'))).toContain('预览')
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
