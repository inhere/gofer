// 终端会话用量（N2 §A，OBS-14）的展示口径：只显示 token；来源没有给费用（cost_usd 为 0 /
// 缺省）时不显示费用，更不臆造价格。
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
  return rows
}

// workUsageLabel：工作项卡：所属当前会话的用量之和。
export function workUsageLabel(u: JobUsage | null | undefined): string {
  if ((u?.total_tokens ?? 0) <= 0) return ''
  return `会话用量 ${tokenLine(u)}`
}
