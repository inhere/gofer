// plan 详情页的链控制说明与用量块（纯函数，便于单测）。
//  - 控件说明：leader / 启动链 / 挂起 / 继续 各自的可见标签 + 一句作用说明；
//  - 链状态：把 status / paused / blocked_todo 翻成一句话（自动推进中 / 已挂起 / 阻塞于 …）；
//  - 用量块：jobs 与主 Agent 会话（主 / 子 agent、按模型）分开列，再给合计。
import type { JobUsage, PlanUsage } from '../api/types'
import { formatTokens } from './jobOutcome'
import { tokenLine } from './sessionUsage'

export interface PlanControlHelp {
  label: string
  help: string
}

// 每个链控件的可见标签与作用说明（同一句话也用作 title 悬浮提示）。
export const PLAN_CONTROL_HELP = {
  leader: {
    label: '管家主导轮次（leader）',
    help: '开：每个成员 job 结束时唤醒一个管家 job 决定下一步；关（默认）：只按条目依赖自动推进，不唤醒管家',
  },
  run: {
    label: '启动链',
    help: '把依赖已满足、已指派的待办置为 ready 并立即派发（同时解除挂起 / 阻塞）',
  },
  pause: {
    label: '挂起',
    help: '暂停自动推进：条目完成后不再自动派发后续条目；已在跑的 job 不会被取消',
  },
  resume: {
    label: '继续',
    help: '解除挂起，按依赖继续自动推进',
  },
  unblock: {
    label: '解除阻塞',
    help: '解除失败条目造成的阻塞并继续推进（也可在下方横幅里重派或跳过该条目）',
  },
} satisfies Record<string, PlanControlHelp>

export type ChainTone = 'running' | 'paused' | 'blocked' | 'idle'

export interface ChainState {
  tone: ChainTone
  text: string
}

// planChainState：当前链状态的一句话。blockedTitle 是阻塞条目的标题（取不到时用 id）。
export function planChainState(
  p: { status: string; paused?: boolean; blocked_todo?: string } | null | undefined,
  blockedTitle = '',
): ChainState {
  if (!p) return { tone: 'idle', text: '' }
  if (p.status === 'archived') return { tone: 'idle', text: '已归档，不再推进' }
  if (p.status === 'done') return { tone: 'idle', text: '已完成' }
  if (p.blocked_todo || p.status === 'blocked') {
    const what = blockedTitle || p.blocked_todo || '失败条目'
    return { tone: 'blocked', text: `阻塞于「${what}」：重派、跳过或点「解除阻塞」后继续` }
  }
  if (p.paused) return { tone: 'paused', text: '已挂起：条目完成后不会自动派发后续条目，点「继续」恢复' }
  return { tone: 'running', text: '自动推进中：条目完成后自动派发依赖已满足的下一项' }
}

export interface UsageRow {
  label: string
  text: string
}

function costText(cost: number | undefined): string {
  return (cost ?? 0) > 0 ? `$${(cost as number).toFixed(4)}` : ''
}

// planUsageRows：「用量」块的行。没有任何 job 也没有会话用量时返回 []（整块不渲染）。
export function planUsageRows(u: PlanUsage | null | undefined): UsageRow[] {
  if (!u) return []
  const rows: UsageRow[] = []
  const s = u.session
  if (u.jobs === 0 && !s) return rows

  const overallTokens = u.overall?.total_tokens ?? u.total_tokens + (s?.total.total_tokens ?? 0)
  const overallCost = u.overall?.cost_usd ?? u.cost_usd + (s?.total.cost_usd ?? 0)
  const overall = [overallTokens > 0 ? `${formatTokens(overallTokens)} tokens` : '—', costText(overallCost)]
    .filter(Boolean)
    .join(' / ')
  rows.push({ label: '合计', text: overall })

  if (u.jobs > 0) {
    const head = [u.total_tokens > 0 ? `${formatTokens(u.total_tokens)} tokens` : '—', costText(u.cost_usd)]
      .filter(Boolean)
      .join(' / ')
    rows.push({ label: `Jobs（${u.jobs} 个）`, text: head })
  }
  if (s) {
    const n = s.sessions > 1 ? `（${s.sessions} 个会话）` : ''
    rows.push({ label: `主 Agent 会话${n}`, text: tokenLine(s.total) || '—' })
    const main = tokenLine(s.main)
    if (main) rows.push({ label: '　· 主 agent', text: main })
    const sub = tokenLine(s.sub)
    if (sub) rows.push({ label: '　· 子 agent', text: sub })
    const models = Object.entries(s.by_model ?? {}).sort(
      (a, b) => (b[1].total_tokens ?? 0) - (a[1].total_tokens ?? 0) || a[0].localeCompare(b[0]),
    )
    for (const [model, mu] of models) {
      rows.push({ label: `　· ${model}`, text: tokenLine(mu as JobUsage) || '—' })
    }
  }
  return rows
}
