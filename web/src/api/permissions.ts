// 权限授权 API（/v1/auth/*）：whoami / 目录 / 授权 / 审计。
// 管理类接口需要 auth.manage（根管理员）；403 文案由 client.ts 统一改写。
import { request } from './client'
import { auditQuery } from '../utils/permissions'
import type {
  AuditFilter,
  AuthAuditEntry,
  AuthGrant,
  AuthGrantsResp,
  AuthWhoami,
  PermissionCatalog,
} from '../utils/permissions'

export function getWhoami(): Promise<AuthWhoami> {
  return request<AuthWhoami>('/v1/auth/whoami')
}

export function getPermissionCatalog(): Promise<PermissionCatalog> {
  return request<PermissionCatalog>('/v1/auth/catalog')
}

export function listAuthGrants(): Promise<AuthGrantsResp> {
  return request<AuthGrantsResp>('/v1/auth/grants')
}

export interface CreateGrantReq {
  subject: string
  pattern: string
  expires_sec?: number
  note?: string
}

export function createAuthGrant(req: CreateGrantReq): Promise<AuthGrant> {
  return request<AuthGrant>('/v1/auth/grants', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...req, scope: '*' }),
  })
}

export function revokeAuthGrant(id: string): Promise<AuthGrant> {
  return request<AuthGrant>(`/v1/auth/grants/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function activateAuthGrant(id: string): Promise<AuthGrant> {
  return request<AuthGrant>(`/v1/auth/grants/${encodeURIComponent(id)}/activate`, { method: 'POST' })
}

export async function listAuthAudit(f: AuditFilter): Promise<AuthAuditEntry[]> {
  const r = await request<{ entries: AuthAuditEntry[]; count: number }>(`/v1/auth/audit?${auditQuery(f)}`)
  return r.entries ?? []
}
