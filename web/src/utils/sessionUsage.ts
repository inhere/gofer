// 终端会话用量（N2 §A，OBS-14）的展示口径：来源给了费用就显示；没给时只在配置了价格表的
// 情况下显示「估算」（服务端按 pricing 算好，gofer-2ldy），前端不自己定价。
import type { JobUsage, SessionUsage, StatsSessionUsageTally } from '../api/types'
import { formatTokens } from './jobOutcome'

// tokenLine：一行 token 摘要（in / out / cache / total）。全空返回 ""。
export function tokenLine(u: JobUsage | StatsSessionUsageTally | null | undefined): string {
  if (!u) return ''
  const parts: string[] = []
  if ((u.input_tokens ?? 0) > 0) parts.push(`in ${formatTokens(u.input_tokens as number)}`)
  if ((u.output_tokens ?? 0) > 0) parts.push(`out ${formatTokens(u.output_tokens as number)}`)
  const cache = (u.cache_read_tokens ?? 0) + (u.cache_write_tokens ?? 0)
  if (cache > 0) parts.push(`cache ${formatTokens(cache)}`)
  if ((u.total_tokens ?? 0) > 0) parts.push(`total ${formatTokens(u.total_tokens as number)}`)
  if ((u.cost_usd ?? 0) > 0) parts.push(`$${(u.cost_usd as number).toFixed(4)}`)
  return parts.join(' / ')
}

// sessionUsageRows：会话卡 / 抽屉的用量行——主会话、子 agent（有才显示）。
export function sessionUsageRows(u: SessionUsage | null | undefined): { label: string; text: string }[] {
  if (!u) return []
  const rows: { label: string; text: string }[] = []
  const main = tokenLine(u.main)
  if (main) rows.push({ label: '主会话', text: main })
  const sub = tokenLine(u.sub)
  if (sub) rows.push({ label: '子 agent', text: sub })
  const est = estimateText(u.estimated_cost_usd, u.unpriced_models)
  if (est) rows.push({ label: '估算费用', text: est })
  return rows
}

// workUsageLabel：工作项卡：所属当前会话的用量之和。
export function workUsageLabel(u: JobUsage | null | undefined): string {
  if ((u?.total_tokens ?? 0) <= 0) return ''
  return `会话用量 ${tokenLine(u)}`
}

// estimateText：「≈ $1.2345（按价格表估算）」，另注未定价的模型数；什么都没有返回 ""。
export function estimateText(usd: number | undefined, unpriced?: string[]): string {
  const parts: string[] = []
  if ((usd ?? 0) > 0) parts.push(`≈ $${(usd as number).toFixed(4)}（按价格表估算）`)
  if (unpriced && unpriced.length > 0) parts.push(`${unpriced.length} 个模型未定价：${unpriced.join(', ')}`)
  return parts.join('；')
}
