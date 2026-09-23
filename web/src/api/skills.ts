// 技能库 API（JOB-10）：/v1/skills 的列表/详情/导入/更新/删除/导出。
// 类型在 types.ts，请求走 client.ts 的共享 request()/downloadFile()——鉴权、401 与
// {error, detail} 的解析只有一份，页面 catch 到的 e.message 就是可显示的原因。

import { downloadFile, request } from './client'
import type { SkillDetail, SkillImportResp, SkillsResp, SkillUpdateResp } from './types'

export function listSkills(): Promise<SkillsResp> {
  return request<SkillsResp>('/v1/skills')
}

export function getSkill(name: string): Promise<SkillDetail> {
  return request<SkillDetail>(`/v1/skills/${encodeURIComponent(name)}`)
}

// importSkillZip 走 multipart/form-data：part `file` 装 zip 的原始字节（文件名随意）。
// 和 stageXfer 一样**不设** Content-Type —— 交给浏览器带 boundary。
export function importSkillZip(file: File): Promise<SkillImportResp> {
  const form = new FormData()
  form.append('file', file, file.name)
  return request<SkillImportResp>('/v1/skills/import', { method: 'POST', body: form })
}

// importSkillSource 走 JSON：spec 由 server 侧解析/拉取（本地目录、.zip、http(s)://…/x.zip、
// git+https://…#subdir），浏览器只传字符串。
export function importSkillSource(source: string): Promise<SkillImportResp> {
  return request<SkillImportResp>('/v1/skills/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source }),
  })
}

// updateSkill 按记录的 source 重新拉取，并回一份 added/changed/removed 的 diff。
export function updateSkill(name: string): Promise<SkillUpdateResp> {
  return request<SkillUpdateResp>(`/v1/skills/${encodeURIComponent(name)}/update`, {
    method: 'POST',
  })
}

// deleteSkill：服务端答 204 无体，request 对空 body 返回 undefined。
export function deleteSkill(name: string): Promise<void> {
  return request<void>(`/v1/skills/${encodeURIComponent(name)}`, { method: 'DELETE' })
}

// exportSkill 下载技能包（zip）：文件名取后端 Content-Disposition 的 <name>.zip。
export function exportSkill(name: string): Promise<void> {
  return downloadFile(`/v1/skills/${encodeURIComponent(name)}/export`, `${name}.zip`)
}
