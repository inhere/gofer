// 顶栏「待我决策」提示条的纯逻辑（gofer-5wvc）：一份 pending 数据 + 已提示过的集合，
// 算出该弹哪一条、更新后的集合。EscalationBell 只负责订阅推送与挂提示条，弹哪一条在这里，
// node 环境下可单测。
//
// 一条待办只弹一次（已见集合），但它从快照里消失后（已批准 / 已拒绝 / 已过期 / 已读 /
// 已作答）对应记录就被去掉：快照代表「现在等我处理」，决定过的 hold 不该让后面新来的那条
// 被当成重复；已批准 / 拒绝的 job 也不再有任何提示条。
import type { Decision, Interaction, Job, PendingApproval } from '../api/types'
import { commandLine } from './approval'

// PENDING_APPROVAL_LIMIT：断线兜底轮询时一次最多取多少个待批 job（服务端快照同样有上限）。
export const PENDING_APPROVAL_LIMIT = 100

export interface PendingLists {
  interactions: Interaction[]
  decisions: Decision[]
  approvals: PendingApproval[]
}

// PendingToast 就是提示条本身；key 标明它代表哪一条待办（快照里没有了就收起）。
export interface PendingToast {
  key: string
  title: string
  text: string
  // 点击跳转的站内路径
  to?: string
}

// PendingSeen 是「已经提示过」的三个集合，各按来源分开存待办自己的 id。
export interface PendingSeen {
  needsHuman: Set<string>
  decisions: Set<string>
  approvals: Set<string>
}

export function emptySeen(): PendingSeen {
  return { needsHuman: new Set(), decisions: new Set(), approvals: new Set() }
}

// pendingLists 把快照 / 兜底 REST 结果补成三份列表（缺字段当空）。
export function pendingLists(snap: {
  interactions?: Interaction[]
  decisions?: Decision[]
  approvals?: PendingApproval[]
}): PendingLists {
  return { interactions: snap.interactions ?? [], decisions: snap.decisions ?? [], approvals: snap.approvals ?? [] }
}

// approvalFromJob：断线兜底轮询走 GET /v1/jobs?status=awaiting_approval，把 job 折成与
// 推送快照同形的 PendingApproval（hold 记录里的命令就是批准要看的东西）。
export function approvalFromJob(job: Pick<Job, 'id' | 'project_key' | 'started_at' | 'title' | 'hold'>): PendingApproval {
  return {
    id: job.id,
    title: job.title,
    project_key: job.project_key,
    waiting_since: job.started_at,
    expires_at: job.hold?.expires_at ?? 0,
    hold_cmd: job.hold?.command,
  }
}

export function approvalKey(id: string): string {
  return `approval:${id}`
}

type PendingItem =
  | { source: 'interaction'; key: string; interaction: Interaction }
  | { source: 'decision'; key: string; decision: Decision }
  | { source: 'approval'; key: string; approval: PendingApproval }

// pendingItems 列出现在等我的条目：所有 pending interaction（只有 needs_human 的才弹提示条，
// 但都算「还在」）、未标「无需回复」的决策、以及待批准 job。
export function pendingItems(lists: PendingLists): PendingItem[] {
  return [
    ...lists.interactions.map((i) => ({ source: 'interaction' as const, key: `interaction:${i.id}`, interaction: i })),
    ...lists.decisions
      .filter((d) => !d.acked_at)
      .map((d) => ({ source: 'decision' as const, key: `decision:${d.id}`, decision: d })),
    ...lists.approvals.map((a) => ({ source: 'approval' as const, key: approvalKey(a.id), approval: a })),
  ]
}

// pendingAlert：算出该弹的提示条与新的已见集合。
//   - 新出现的待批 job / needs_human 交互 / OPEN 决策各弹一次；待批 job 优先（job 停在那儿等人
//     点头才开跑，是唯一自己走不下去的那类）；
//   - 已见的同一条不重复弹（没有新条目时保留当前提示条）；
//   - 快照里已经没有的条目从已见集合去掉，它代表的提示条同时收起。
export function pendingAlert(
  seen: PendingSeen,
  toast: PendingToast | null,
  lists: PendingLists,
): { seen: PendingSeen; toast: PendingToast | null } {
  const items = pendingItems(lists)
  const live = liveIDs(items)
  const next: PendingSeen = {
    needsHuman: keepSeen(seen.needsHuman, live.interaction),
    decisions: keepSeen(seen.decisions, live.decision),
    approvals: keepSeen(seen.approvals, live.approval),
  }

  const freshApproval = items.find((it) => it.source === 'approval' && !next.approvals.has(it.approval.id))
  const freshNeedsHuman = items.find(
    (it) => it.source === 'interaction' && it.interaction.needs_human === 1 && !next.needsHuman.has(it.interaction.id),
  )
  const freshDecision = items.find((it) => it.source === 'decision' && !next.decisions.has(it.decision.id))
  for (const it of items) {
    if (it.source === 'interaction' && it.interaction.needs_human === 1) next.needsHuman.add(it.interaction.id)
    if (it.source === 'decision') next.decisions.add(it.decision.id)
    if (it.source === 'approval') next.approvals.add(it.approval.id)
  }

  // 提示条代表的条目没有了就收起（批准 / 拒绝 / 已读 / 已答）。
  const kept = toast && items.some((it) => it.key === toast.key) ? toast : null
  if (freshApproval?.source === 'approval') {
    return { seen: next, toast: approvalToast(freshApproval.approval) }
  }
  if (freshNeedsHuman?.source === 'interaction') {
    const i = freshNeedsHuman.interaction
    return {
      seen: next,
      toast: {
        key: freshNeedsHuman.key,
        title: '⚠ 新的人工介入请求 · needs_human',
        text: `job ${shortId(i.job_id)} — ${truncLine(i.prompt, 96) || '等待人工介入'}`,
        to: `/jobs/${encodeURIComponent(i.job_id)}`,
      },
    }
  }
  if (freshDecision?.source === 'decision') {
    return { seen: next, toast: decisionToast(freshDecision.decision) }
  }
  return { seen: next, toast: kept }
}

function keepSeen(seen: Set<string>, alive: Set<string>): Set<string> {
  return new Set([...seen].filter((id) => alive.has(id)))
}

// liveIDs：快照里每条待办自己的 id（按来源），用来判断已见记录还在不在。
function liveIDs(items: PendingItem[]): { interaction: Set<string>; decision: Set<string>; approval: Set<string> } {
  const out = { interaction: new Set<string>(), decision: new Set<string>(), approval: new Set<string>() }
  for (const it of items) {
    switch (it.source) {
      case 'interaction':
        out.interaction.add(it.interaction.id)
        break
      case 'decision':
        out.decision.add(it.decision.id)
        break
      case 'approval':
        out.approval.add(it.approval.id)
        break
    }
  }
  return out
}

// approvalToast：「⏸ job 等你批准」，点击进 job 详情页——待批面板就在页首。
function approvalToast(a: PendingApproval): PendingToast {
  const cmd = truncLine(commandLine(a.hold_cmd), 96)
  return {
    key: approvalKey(a.id),
    title: `⏸ job 等你批准${a.project_key ? ` · ${a.project_key}` : ''}`,
    text: `${a.title || shortId(a.id)}${cmd ? ` — ${cmd}` : ''}`,
    to: `/jobs/${encodeURIComponent(a.id)}`,
  }
}

// decisionToast：权限请求 / 会话等回复 / 普通决策各一种文案（原来在 EscalationBell 里）。
function decisionToast(d: Decision): PendingToast {
  const key = `decision:${d.id}`
  const relay = d.kind === 'relay' && !!d.session_id
  const permission = d.kind === 'permission' && !!d.session_id
  if (permission) {
    return {
      key,
      title: d.permission?.questions?.length
        ? `会话在等你回答 · ${d.title || shortSid(d.session_id)}`
        : `会话需要授权 · ${d.title || shortSid(d.session_id)}`,
      text: truncLine(d.permission?.questions?.[0]?.question || d.permission?.summary || d.question, 96) || '终端在等待工具授权',
      to: `/workbench?thread=${encodeURIComponent(`r:${d.session_id ?? ''}`)}`,
    }
  }
  if (relay) {
    return {
      key,
      title: `会话等待回复 · ${d.title || shortSid(d.session_id)}`,
      text: truncLine(d.question, 96) || '会话停下等你回复',
      to: `/workbench?thread=${encodeURIComponent(`r:${d.session_id ?? ''}`)}`,
    }
  }
  return {
    key,
    title: `新的决策请求 · ${d.title || d.id}`,
    text: truncLine(d.question, 96) || '等待人工作答',
    to: d.plan_id ? `/plans/${encodeURIComponent(d.plan_id)}` : undefined,
  }
}

function truncLine(s: string, max: number): string {
  const first = s.split(/\r?\n/)[0]?.trim() ?? ''
  return first.length > max ? `${first.slice(0, max)}...` : first
}

function shortId(id: string): string {
  return id.length > 10 ? `...${id.slice(-10)}` : id
}

function shortSid(id?: string): string {
  return id ? id.slice(0, 8) : '—'
}
