// MCP-06：MCP 注册表条目的展示文字（设置页 MCP 与相关测试共用）。
import type { MCPServerView } from '../api/types'
import { t } from '../i18n'

/** 「在哪里绑定」：server / agent:x / project:y，没有绑定时为空串。 */
export function mcpBoundText(v: MCPServerView): string {
  const parts: string[] = []
  if (v.bound.server) parts.push(t('settings.mcp.boundServer'))
  for (const a of v.bound.agents) parts.push(`agent:${a}`)
  for (const p of v.bound.projects) parts.push(`project:${p}`)
  return parts.join(t('util.sep.comma'))
}

/** 启动方式一行：stdio 是命令行，http 是 URL。 */
export function mcpTarget(v: MCPServerView): string {
  if ((v.spec.type ?? 'stdio') === 'http') return v.spec.url ?? ''
  return [v.spec.command ?? '', ...(v.spec.args ?? [])].join(' ').trim()
}

/** secret 就绪情况：缺几个变量（server 本机）。 */
export function mcpMissingSecrets(v: MCPServerView): string[] {
  return v.secrets.filter((s) => !s.set).map((s) => s.env)
}
