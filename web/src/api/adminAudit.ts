// 管理操作审计接口（GET /v1/audit，设计 2026-11-14 §1）：谁、何时、调了哪条路由、对哪个资源、
// 结果如何。权限 audit.read（默认只有根管理员，可授予）；无权限时服务端 403。
//
// 筛选与分页参数的名字以 internal/httpapi/admin_audit_handler.go 为准：
// caller（精确）、route（路由模板子串）、status（ok | fail）、since / until（unix 秒、RFC 3339
// 或 YYYY-MM-DD），cursor 取上一页回答里的 next_cursor，limit 上限 500。
import { request } from './client'
import type { AdminAuditResp } from './types'

// AdminAuditFilter 是页面上的筛选表单（字符串原样，空串 = 不过滤）。
export interface AdminAuditFilter {
  caller: string
  route: string
  status: '' | 'ok' | 'fail'
  since: string
  until: string
}

export const EMPTY_AUDIT_FILTER: AdminAuditFilter = { caller: '', route: '', status: '', since: '', until: '' }

// auditQuery 把筛选 + 游标拼成查询串（不带问号）。空值一律不写——服务端把空串当不过滤，
// 但带上会让 URL 与缓存键无谓地不同。since / until 原样透传，服务端接受三种写法。
export function auditQuery(f: AdminAuditFilter, cursor = 0, limit = 0): string {
  const q = new URLSearchParams()
  for (const [key, raw] of [
    ['caller', f.caller],
    ['route', f.route],
    ['status', f.status],
    ['since', f.since],
    ['until', f.until],
  ] as const) {
    const v = raw.trim()
    if (v) q.set(key, v)
  }
  if (cursor > 0) q.set('cursor', String(cursor))
  if (limit > 0) q.set('limit', String(limit))
  return q.toString()
}

export function listAdminAudit(f: AdminAuditFilter, cursor = 0, limit = 0): Promise<AdminAuditResp> {
  const qs = auditQuery(f, cursor, limit)
  return request<AdminAuditResp>(`/v1/audit${qs ? `?${qs}` : ''}`)
}
