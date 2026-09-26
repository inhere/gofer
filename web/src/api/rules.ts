// 规则库 API（JOB-06①）：/v1/rules 的列表/详情/写入/删除。
// 类型在 types.ts，请求走 client.ts 的共享 request()——鉴权、401 与 {error, detail}
// 的解析只有一份，页面 catch 到的 e.message 就是可显示的原因（400 的校验文案原样显示）。
//
// 写操作要 can_admin：服务端的 403 原因由 request() 抛出，页面直接显示，不吞错。

import { request } from './client'
import type { RuleDetail, RulesResp } from './types'

export function listRules(): Promise<RulesResp> {
  return request<RulesResp>('/v1/rules')
}

// getRule 取一条规则的原文（frontmatter 一起）：编辑器载入的就是这些字节，保存时原样回发，
// 所以页面对别的 frontmatter 键（如提示用的 agents:）是无损的。
export function getRule(name: string): Promise<RuleDetail> {
  return request<RuleDetail>(`/v1/rules/${encodeURIComponent(name)}`)
}

// putRule 创建或整条替换一条规则（PUT /v1/rules/{name}）。
export function putRule(name: string, content: string): Promise<RuleDetail> {
  return request<RuleDetail>(`/v1/rules/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
}

// deleteRule：服务端答 204 无体，request 对空 body 返回 undefined。
export function deleteRule(name: string): Promise<void> {
  return request<void>(`/v1/rules/${encodeURIComponent(name)}`, { method: 'DELETE' })
}
