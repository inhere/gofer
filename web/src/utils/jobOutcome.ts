// job 产出的展示口径（verify / commits / usage）：JobDetail「产出与审计」、验收台列表
// （/review）与验收面板（ReviewPanel）共用这一份格式化，避免同一个数字在多处各写一遍。
// 文本格式与后端 `job show` / job.FormatUsage 对齐。
import type { JobUsage, JobVerify } from '../api/types'

// formatTokens 与后端 job.formatTokens 同规则：<1000 原样，其余带 k/M 且保留 3 位有效数字。
export function formatTokens(n: number): string {
  if (n < 1000) {
    return String(n)
  }
  if (n < 1_000_000) {
    return `${Number((n / 1000).toPrecision(3))}k`
  }
  return `${Number((n / 1_000_000).toPrecision(3))}M`
}

// usageLine：一行 token/成本摘要。缺项省略（agent 没报 ≠ 0），行尾括号是采集来源；
// 全空返回 ""——调用方据此决定不渲染用量块。
export function usageLine(u: JobUsage | null | undefined): string {
  if (!u) {
    return ''
  }
  const parts: string[] = []
  if ((u.input_tokens ?? 0) > 0) {
    parts.push(`in ${formatTokens(u.input_tokens as number)}`)
  }
  if ((u.output_tokens ?? 0) > 0) {
    parts.push(`out ${formatTokens(u.output_tokens as number)}`)
  }
  const cache = (u.cache_read_tokens ?? 0) + (u.cache_write_tokens ?? 0)
  if (cache > 0) {
    parts.push(`cache ${formatTokens(cache)}`)
  }
  if ((u.total_tokens ?? 0) > 0) {
    parts.push(`total ${formatTokens(u.total_tokens as number)}`)
  }
  if ((u.cost_usd ?? 0) > 0) {
    parts.push(`$${(u.cost_usd as number).toFixed(4)}`)
  }
  if (parts.length === 0) {
    return ''
  }
  const line = parts.join(' / ')
  return u.source ? `${line} (${u.source})` : line
}

// usageBadge：列表单元格的紧凑用量（total tokens + $）。没采集到用量返回 "—"。
export function usageBadge(u: JobUsage | null | undefined): string {
  const parts: string[] = []
  if ((u?.total_tokens ?? 0) > 0) {
    parts.push(`${formatTokens(u?.total_tokens as number)} tokens`)
  }
  if ((u?.cost_usd ?? 0) > 0) {
    parts.push(`$${(u?.cost_usd as number).toFixed(4)}`)
  }
  return parts.length > 0 ? parts.join(' · ') : '—'
}

// verifyLabel：验证步骤的一句话（passed (1.2s) / failed (exit 3, 0.4s) / skipped (原因)）。
// 无 verify 结果（没跑过）返回 ""。
export function verifyLabel(v: JobVerify | null | undefined): string {
  if (!v) {
    return ''
  }
  if (v.status === 'skipped') {
    return v.reason ? `${v.status} (${v.reason})` : v.status
  }
  const dur = `${(v.duration_ms / 1000).toFixed(1)}s`
  if (v.status === 'passed') {
    return `${v.status} (${dur})`
  }
  return `${v.status} (exit ${v.exit_code}, ${dur})`
}

// verifyClass：passed 绿 / failed·timeout 红 / skipped（含无结果）灰。
export function verifyClass(v: JobVerify | null | undefined): string {
  switch (v?.status) {
    case 'passed':
      return 'verify--ok'
    case 'failed':
    case 'timeout':
      return 'verify--bad'
    default:
      return 'verify--skip'
  }
}

// shortSha：显示用短 sha（10 位）；完整值留在 title / 复制按钮里。空值返回 "—"。
export function shortSha(sha: string | undefined): string {
  if (!sha) {
    return '—'
  }
  return sha.length > 10 ? sha.slice(0, 10) : sha
}
