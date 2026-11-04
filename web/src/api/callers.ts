// 用户管理接口（/v1/callers*）：根管理员在控制台上新增 / 轮换 / 停用 / 启用调用方，改上限与配额，
// 并查看改动审计。路由权限是 auth.manage（只有根管理员有），其余人调用一律 403。
//
// 唯一一条安全约定：token 只出现在「新增」和「轮换」这两个响应里（CallerChange.token），
// 界面把它显示一次后丢弃——不进 localStorage / sessionStorage，不写 URL，不进日志。
// 之后只能用 token_hint（`gct_…1234`）区分两个 token，拿不回原值。
import { request } from './client'
import type {
  CallerAddReq,
  CallerAuditResp,
  CallerCeilingView,
  CallerChange,
  CallerLimits,
  CallersResp,
} from './types'

function json(method: string, body?: unknown): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

export function listCallers(): Promise<CallersResp> {
  return request<CallersResp>('/v1/callers')
}

// addCaller 新建一个存储用户；回答里的 token 就是它唯一的凭证（只此一次）。
export function addCaller(req: CallerAddReq): Promise<CallerChange> {
  return request<CallerChange>('/v1/callers', json('POST', req))
}

// rotateCaller 换一个新 token（旧 token 立刻失效，长连接被断开），回答里的 token 同样只此一次。
export function rotateCaller(id: string): Promise<CallerChange> {
  return request<CallerChange>(`/v1/callers/${encodeURIComponent(id)}/rotate`, json('POST'))
}

export function disableCaller(id: string): Promise<CallerChange> {
  return request<CallerChange>(`/v1/callers/${encodeURIComponent(id)}/disable`, json('POST'))
}

export function enableCaller(id: string): Promise<CallerChange> {
  return request<CallerChange>(`/v1/callers/${encodeURIComponent(id)}/enable`, json('POST'))
}

// setCallerCeiling 换上限：`ceiling` 块与 `no_ceiling` 二选一（服务端两样都不给或给空块都 400）。
export function setCallerCeiling(
  id: string,
  body: { ceiling?: CallerCeilingView; no_ceiling?: boolean },
): Promise<CallerChange> {
  return request<CallerChange>(`/v1/callers/${encodeURIComponent(id)}/ceiling`, json('PUT', body))
}

// setCallerLimits 换配额（0 = 用 server.governance 的默认，可能本身也是不限）。
export function setCallerLimits(id: string, limits: CallerLimits): Promise<CallerChange> {
  return request<CallerChange>(`/v1/callers/${encodeURIComponent(id)}/limits`, json('PUT', limits))
}

export function listCallerAudit(limit?: number): Promise<CallerAuditResp> {
  const qs = limit && limit > 0 ? `?limit=${limit}` : ''
  return request<CallerAuditResp>(`/v1/callers/audit${qs}`)
}
