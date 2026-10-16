// 权限授权 API（/v1/auth/*）：whoami / 目录 / 授权 / 审计。
// 管理类接口需要 auth.manage（根管理员）；403 文案由 client.ts 统一改写。
import { ApiError, request } from './client'
import { auditQuery, isExpandPreview } from '../utils/permissions'
import type {
  AuditFilter,
  AuthAuditEntry,
  AuthGrant,
  AuthGrantsResp,
  AuthWhoami,
  ExpandPreview,
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

// 授权模式的展开预览（只读，auth.manage）：规则只在服务端 internal/authz 一份。
// 模式展开后什么都不剩时服务端回 400，body 仍是预览形状（带 problem 与被挡下的权限），
// 这里当成正常结果交给界面；其余错误（403、网络）照常抛出。
export async function expandGrantPattern(pattern: string, subject: string): Promise<ExpandPreview> {
  const q = new URLSearchParams({ pattern, subject })
  try {
    return await request<ExpandPreview>(`/v1/auth/expand?${q.toString()}`)
  } catch (e) {
    if (e instanceof ApiError && e.status === 400 && isExpandPreview(e.body)) return e.body
    throw e
  }
}
