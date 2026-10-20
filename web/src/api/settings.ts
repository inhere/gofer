// K5：扩展设置（/v1/settings）——server 数据库里的运行时旋钮（降噪阈值与排除名单、
// 提示词覆盖）。读要 settings.read，写要 settings.write；契约见
// docs/reference/http-api.md 的「Extension settings」。
import { downloadFile, request } from './client'
import type {
  SettingItem,
  SettingValue,
  SettingsImportResp,
  SettingsResetResp,
  SettingsResp,
} from './types'

export async function listSettings(): Promise<SettingItem[]> {
  const r = await request<SettingsResp>('/v1/settings')
  return r.settings ?? []
}

export function updateSetting(key: string, value: SettingValue): Promise<SettingItem> {
  return request<SettingItem>(`/v1/settings/${encodeURIComponent(key)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value }),
  })
}

// resetSetting 丢弃存值：注册的默认值重新生效。
export function resetSetting(key: string): Promise<SettingsResetResp> {
  return request<SettingsResetResp>(`/v1/settings/${encodeURIComponent(key)}`, { method: 'DELETE' })
}

// exportSettings 下载改过的键的 YAML（后端带 Content-Disposition 文件名）。
export function exportSettings(): Promise<void> {
  return downloadFile('/v1/settings/export', 'gofer-settings.yaml')
}

// importSettings 提交 YAML：dryRun 只预览逐键差异，不写任何键。
export function importSettings(yaml: string, dryRun: boolean): Promise<SettingsImportResp> {
  const path = dryRun ? '/v1/settings/import?dry_run=1' : '/v1/settings/import'
  return request<SettingsImportResp>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/yaml' },
    body: yaml,
  })
}
