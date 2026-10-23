// plan 详情页的链控制说明与用量块（纯函数，便于单测）。
//  - 控件说明：leader / 启动链 / 挂起 / 继续 各自的可见标签 + 一句作用说明；
//  - 链状态：把 status / paused / blocked_todo 翻成一句话（自动推进中 / 已挂起 / 阻塞于 …）；
//  - 用量块：jobs 与主 Agent 会话（主 / 子 agent、按模型）分开列，再给合计。
import type { JobUsage, PlanUsage } from '../api/types'
import { t } from '../i18n'
import { formatTokens } from './jobOutcome'
import { tokenLine } from './sessionUsage'

export interface PlanControlHelp {
  label: string
  help: string
}

// 每个链控件的可见标签与作用说明（同一句话也用作 title 悬浮提示）。文案跟当前语言，
// 所以用 getter：引用处（模板 / 脚本）拿到的仍是字符串。
export const PLAN_CONTROL_HELP = {
  leader: {
    get label() { return t('plans.ctrl.leader.label') },
    get help() { return t('plans.ctrl.leader.help') },
  },
  notify: {
    get label() { return t('plans.ctrl.notify.label') },
    get help() { return t('plans.ctrl.notify.help') },
  },
  run: {
    get label() { return t('plans.ctrl.run.label') },
    get help() { return t('plans.ctrl.run.help') },
  },
  pause: {
    get label() { return t('plans.ctrl.pause.label') },
    get help() { return t('plans.ctrl.pause.help') },
  },
  resume: {
    get label() { return t('plans.ctrl.resume.label') },
    get help() { return t('plans.ctrl.resume.help') },
  },
  unblock: {
    get label() { return t('plans.ctrl.unblock.label') },
    get help() { return t('plans.ctrl.unblock.help') },
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
  if (p.status === 'archived') return { tone: 'idle', text: t('plans.chain.archived') }
  if (p.status === 'done') return { tone: 'idle', text: t('plans.chain.done') }
  if (p.blocked_todo || p.status === 'blocked') {
    const what = blockedTitle || p.blocked_todo || t('plans.chain.failedTodo')
    return { tone: 'blocked', text: t('plans.chain.blocked', { what }) }
  }
  if (p.paused) return { tone: 'paused', text: t('plans.chain.paused') }
  return { tone: 'running', text: t('plans.chain.running') }
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
  rows.push({ label: t('plans.usage.total'), text: overall })

  if (u.jobs > 0) {
    const head = [u.total_tokens > 0 ? `${formatTokens(u.total_tokens)} tokens` : '—', costText(u.cost_usd)]
      .filter(Boolean)
      .join(' / ')
    rows.push({ label: t('plans.usage.jobs', { n: u.jobs }), text: head })
  }
  // 缓存 token（读 / 写，gofer-rnp3）：接续会话省下的主要就是这两笔，故在 Jobs 行下单独
  // 拆出来；两端都缺（老服务端 / 没采集到）就整行不渲染，不当 0 显示。
  const cacheRead = u.cache_read_tokens ?? 0
  const cacheWrite = u.cache_write_tokens ?? 0
  if (cacheRead > 0 || cacheWrite > 0) {
    rows.push({
      label: t('plans.usage.cache'),
      text: t('plans.usage.cacheDetail', { read: formatTokens(cacheRead), write: formatTokens(cacheWrite) }),
    })
  }
  if (s) {
    const n = s.sessions > 1 ? t('plans.usage.sessionCount', { n: s.sessions }) : ''
    rows.push({ label: t('plans.usage.mainAgentSessions', { n }), text: tokenLine(s.total) || '—' })
    const main = tokenLine(s.main)
    if (main) rows.push({ label: t('plans.usage.mainAgent'), text: main })
    const sub = tokenLine(s.sub)
    if (sub) rows.push({ label: t('plans.usage.subAgent'), text: sub })
    const models = Object.entries(s.by_model ?? {}).sort(
      (a, b) => (b[1].total_tokens ?? 0) - (a[1].total_tokens ?? 0) || a[0].localeCompare(b[0]),
    )
    for (const [model, mu] of models) {
      rows.push({ label: `　· ${model}`, text: tokenLine(mu as JobUsage) || '—' })
    }
  }
  return rows
}
