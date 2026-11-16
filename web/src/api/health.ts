// server 自检接口（GET /v1/health/checks，设计 2026-11-14 §5）：每项的状态、连续失败数、
// 最后错误与检查时间。登录即可读；服务端没接自检运行器时 checks 为空数组（不是错误）。
import { request } from './client'
import type { HealthChecksResp } from './types'

export function getHealthChecks(): Promise<HealthChecksResp> {
  return request<HealthChecksResp>('/v1/health/checks')
}
