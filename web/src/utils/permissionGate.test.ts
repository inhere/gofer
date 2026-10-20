import { describe, expect, it } from 'vitest'
import { ACTION_PERMISSIONS, canDo, holdsPermission, missingFor } from './permissionGate'
import type { GateAction } from './permissionGate'
import type { AuthWhoami } from './permissions'

function who(over: Partial<AuthWhoami>): AuthWhoami {
  return { caller_id: 'alice', kind: 'user', root: false, permissions: [], grants_version: 1, ...over }
}
const holds = (...names: string[]) => names.map((name) => ({ name, source: 'db' }))

describe('permission-based hiding', () => {
  it('maps each guarded action to its permission', () => {
    const table = Object.fromEntries(Object.entries(ACTION_PERMISSIONS).map(([k, v]) => [k, v.perms]))
    expect(table).toEqual({
      'grant.manage': ['auth.manage'],
      'worker.upgrade': ['worker.upgrade'],
      'worker.manage': ['worker.manage'],
      'update.apply': ['update.apply'],
      'config.write': ['config.write'],
      'settings.write': ['settings.write'],
      'project.write': ['project.write'],
      'job.delete': ['job.delete.others'],
      'tunnel.hosted': ['tunnel.hosted'],
      'tunnel.stopOthers': ['tunnel.manage.others'],
    })
  })

  it('hides every guarded action from a caller holding nothing', () => {
    const w = who({})
    for (const a of Object.keys(ACTION_PERMISSIONS) as GateAction[]) expect(canDo(w, a)).toBe(false)
  })

  it('shows an action only to a holder of its permission', () => {
    const w = who({ permissions: holds('worker.upgrade', 'config.write') })
    expect(canDo(w, 'worker.upgrade')).toBe(true)
    expect(canDo(w, 'config.write')).toBe(true)
    expect(canDo(w, 'worker.manage')).toBe(false)
    expect(canDo(w, 'update.apply')).toBe(false)
  })

  it('shows everything to a root administrator', () => {
    const w = who({ root: true })
    for (const a of Object.keys(ACTION_PERMISSIONS) as GateAction[]) expect(canDo(w, a)).toBe(true)
  })

  it('lets an owner act on their own job/forward without the others permission', () => {
    const w = who({ caller_id: 'alice' })
    expect(canDo(w, 'job.delete', 'alice')).toBe(true)
    expect(canDo(w, 'job.delete', 'bob')).toBe(false)
    expect(canDo(w, 'job.delete')).toBe(false)
    expect(canDo(w, 'tunnel.stopOthers', 'alice')).toBe(true)
    expect(canDo(who({ permissions: holds('job.delete.others') }), 'job.delete', 'bob')).toBe(true)
    // 非 *.others 动作不看所属
    expect(canDo(w, 'config.write', 'alice')).toBe(false)
  })

  it('does not hide anything while whoami is unknown or the credential is not a permission subject', () => {
    expect(canDo(null, 'config.write')).toBe(true)
    expect(canDo(undefined, 'worker.upgrade')).toBe(true)
    expect(holdsPermission(who({ kind: 'worker' }), 'config.write')).toBe(true)
  })

  it('names what is missing', () => {
    expect(missingFor(who({}), 'update.apply')).toEqual(['update.apply'])
    expect(missingFor(who({ permissions: holds('update.apply') }), 'update.apply')).toEqual([])
  })
})
