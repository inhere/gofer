import { describe, expect, it } from 'vitest'
import { auditQuery, EMPTY_AUDIT_FILTER } from './adminAudit'
import type { AdminAuditFilter } from './adminAudit'

// GET /v1/audit 的查询串：空条件不写、值原样透传、游标与 limit 只在给了才带。
const f = (patch: Partial<AdminAuditFilter> = {}): AdminAuditFilter => ({ ...EMPTY_AUDIT_FILTER, ...patch })

describe('auditQuery', () => {
  it('writes nothing when the filter is empty', () => {
    expect(auditQuery(EMPTY_AUDIT_FILTER)).toBe('')
  })

  it('passes every filter through under its wire name', () => {
    const qs = auditQuery(f({ caller: 'alice', route: '/v1/schedules', status: 'fail', since: '2026-11-01', until: '2026-11-30' }))
    const q = new URLSearchParams(qs)
    expect(q.get('caller')).toBe('alice')
    expect(q.get('route')).toBe('/v1/schedules')
    expect(q.get('status')).toBe('fail')
    expect(q.get('since')).toBe('2026-11-01')
    expect(q.get('until')).toBe('2026-11-30')
  })

  it('drops blank and whitespace-only values instead of sending empty parameters', () => {
    const q = new URLSearchParams(auditQuery(f({ caller: '  ', route: '', status: '' })))
    expect([...q.keys()]).toEqual([])
  })

  it('adds the cursor and limit only when they are positive', () => {
    expect(auditQuery(f({ caller: 'bob' }), 0, 0)).toBe('caller=bob')
    const q = new URLSearchParams(auditQuery(f({ caller: 'bob' }), 42, 100))
    expect(q.get('cursor')).toBe('42')
    expect(q.get('limit')).toBe('100')
  })

  it('keeps a status of ok', () => {
    expect(new URLSearchParams(auditQuery(f({ status: 'ok' }))).get('status')).toBe('ok')
  })
})
