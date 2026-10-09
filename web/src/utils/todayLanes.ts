// 「并行中」泳道的展示规则（N3 §3.1）：纯函数，组件只负责渲染。
import type { LaneAgent, LaneAgentState, LanePip, TodayLane, TodayLanesSummary } from '../api/todayLanes'
import type { WorkHealth } from '../api/types'

// 超过这么多行时，把排在后面的健康泳道折叠成一行「还有 N 条正常运行中」。
export const LANE_FOLD_LIMIT = 12
// 一行最多画这么多个 todo 点，其余折成 +N（窄屏不出横向滚动）。
export const PIP_LIMIT = 12

const HEALTH_LABEL: Record<WorkHealth, string> = { ok: '', at_risk: '有风险', stalled: '停滞', blocked: '阻塞' }

// 健康度文案：正常不显示（空串）。
export function healthText(h: WorkHealth | string | undefined): string {
  return (h && HEALTH_LABEL[h as WorkHealth]) || ''
}

export function needsAttention(l: Pick<TodayLane, 'health'>): boolean {
  return !!l.health && l.health !== 'ok'
}

// 头部一行：「8 条 · 5 个 agent 在跑」+ 可选「2 条要留意」。
export function laneSummaryText(s: TodayLanesSummary): { main: string; attention: string } {
  return {
    main: `${s.total} 条 · ${s.agents_running} 个 agent 在跑`,
    attention: s.attention > 0 ? `${s.attention} 条要留意` : '',
  }
}

// 折叠：服务端已按「阻塞 > 停滞 > 有风险 > 有 agent 在跑 > 其余」排好序；要留意的永远展示，
// 超过 limit 时只折叠后面的正常泳道。
export function splitLanes(lanes: TodayLane[], limit = LANE_FOLD_LIMIT): { shown: TodayLane[]; folded: TodayLane[] } {
  if (lanes.length <= limit) return { shown: lanes, folded: [] }
  const attention = lanes.filter(needsAttention).length
  const n = Math.max(limit, attention)
  return { shown: lanes.slice(0, n), folded: lanes.slice(n) }
}

const STATE_LABEL: Record<LaneAgentState, string> = { running: 'running', awaiting_input: '等输入', idle: 'idle' }

// agent 列：一个时「codex·running」，多个时「codex+claude·running」（状态取最要紧的一个）；没有则「—」。
export function agentText(agents: LaneAgent[]): { text: string; state: LaneAgentState | '' } {
  if (!agents.length) return { text: '—', state: '' }
  const state: LaneAgentState = agents.some((a) => a.state === 'running')
    ? 'running'
    : agents.some((a) => a.state === 'awaiting_input')
      ? 'awaiting_input'
      : 'idle'
  const names = [...new Set(agents.map((a) => a.agent || '?'))]
  return { text: `${names.join('+')}·${STATE_LABEL[state]}`, state }
}

// 用时：48m / 3h12m / 2d。
export function elapsedText(sec: number | undefined): string {
  const s = Math.max(0, Math.floor(sec ?? 0))
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) {
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    return m ? `${h}h${String(m).padStart(2, '0')}m` : `${h}h`
  }
  return `${Math.floor(s / 86400)}d`
}

export function pipsView(pips: LanePip[] | undefined, limit = PIP_LIMIT): { shown: LanePip[]; more: number } {
  const all = pips ?? []
  return { shown: all.slice(0, limit), more: Math.max(0, all.length - limit) }
}

// 状态点颜色：阻塞优先，其次工作项状态（plan 运行中按进行中算）。
export function dotTone(l: Pick<TodayLane, 'health' | 'status'>): string {
  if (l.health === 'blocked') return 'blocked'
  switch (l.status) {
    case 'needs_me':
    case 'needs_onsite':
      return 'needs_me'
    case 'review':
      return 'review'
    case 'waiting_resource':
      return 'waiting'
    default:
      return 'active'
  }
}

// 点行去哪：工作项 → Works 页工作项抽屉（复用 WorkDrawer）；plan → plan 详情。
export function laneTarget(l: Pick<TodayLane, 'kind' | 'id'>): { path: string; query?: Record<string, string> } {
  if (l.kind === 'plan') return { path: `/plans/${encodeURIComponent(l.id)}` }
  return { path: '/work', query: { id: l.id } }
}

export function laneAriaLabel(l: TodayLane): string {
  const h = healthText(l.health)
  return [l.title, l.project_key, h].filter(Boolean).join('，')
}
