// 事件时间线的词汇表（E13 起逐步补全）：type -> 图标/中文标签，以及 detail_json 的一行摘要。
// JobDetail 的「事件时间线」与 PlanDetail 的 plan 事件流共用这一份 —— 事件类型随功能增补，
// 两处各写一份必然会漂移。渲染方式（图标 + 标签 + 摘要 + 时间）由各页面自己决定。
// 图标是 lucide 组件（issue gofer-h355）：曾经是 Unicode 字形，字体里没有那些符号时
// 大小与粗细会随机器变。时间线只取形状，颜色由 .ev-icon 决定。
import {
  ArrowRight,
  ArrowUpToLine,
  Ban,
  BellOff,
  Boxes,
  Check,
  Circle,
  CircleDot,
  Cog,
  CornerUpRight,
  Flag,
  Hourglass,
  Keyboard,
  KeyRound,
  MessageSquare,
  PackageCheck,
  Pause,
  Pencil,
  Play,
  RotateCw,
  Scale,
  ScrollText,
  SkipForward,
  Square,
  Timer,
  TriangleAlert,
  X,
  type LucideIcon,
} from '@lucide/vue'
import type { JobEvent } from '../api/types'
import { t, te } from '../i18n'

// 事件 type -> 图标（仿 interactions 渲染风格，单行）；标签走 i18n 的 util.event.<type>。
export const EVENT_META: Record<string, { icon: LucideIcon }> = {
  'job.submitted': { icon: Check },
  'job.title_changed': { icon: Pencil },
  'job.dispatched': { icon: ArrowRight },
  'job.running': { icon: Play },
  'job.turn_started': { icon: Play },
  'job.turn_ended': { icon: Square },
  'job.awaiting_input': { icon: Keyboard },
  'job.terminal': { icon: Square },
  'job.uncommitted': { icon: TriangleAlert },
  'job.cancelled': { icon: X },
  'job.needs_review': { icon: TriangleAlert },
  'job.reviewed': { icon: Check },
  // gofer-9b1b 待批 job：提交后等人批准；批准后转 queued 照常执行，拒绝 / 超时转 cancelled。
  'job.awaiting_approval': { icon: Pause },
  'job.hold_approved': { icon: Check },
  'job.hold_rejected': { icon: X },
  'job.hold_expired': { icon: Timer },
  'job.verify_started': { icon: Play },
  'job.verify_finished': { icon: Check },
  'job.upload_failed': { icon: TriangleAlert },
  'job.files_collected': { icon: PackageCheck },
  'job.fell_back': { icon: CornerUpRight },
  'job.agent_substituted': { icon: CornerUpRight },
  'job.dir_wait_timeout': { icon: Timer },
  'job.env_allowed': { icon: Check },
  'job.auto_resumed': { icon: RotateCw },
  'job.retry_scheduled': { icon: Timer },
  'job.retry_started': { icon: RotateCw },
  'job.retry_exhausted': { icon: TriangleAlert },
  'job.wakeup_fired': { icon: Play },
  'job.wakeup_coalesced': { icon: CornerUpRight },
  'job.wakeup_expired': { icon: Timer },
  'job.wakeup_failed': { icon: TriangleAlert },
  'interaction.created': { icon: MessageSquare },
  'interaction.answered': { icon: Pencil },
  'interaction.punted': { icon: CornerUpRight },
  // 审批门（GATE-01 S1）+ acp 回合汇总（bd h-aii-rnxk：时间线只留生命周期，
  // 工具调用等执行细节走 stderr 的紧凑事件行，由 NdjsonTimeline 渲染）
  'job.acp_summary': { icon: Cog },
  'job.permission_requested': { icon: TriangleAlert },
  'job.permission_answered': { icon: Pencil },
  'job.permission_timed_out': { icon: Timer },
  // JOB-11 / AUTO-05：等目录锁（非终态，等同 queued）与输出停滞（job 已被看门狗杀掉）。
  'job.waiting_dir': { icon: Hourglass },
  'job.stalled': { icon: TriangleAlert },
  // N2 §B：预算熔断——计量器发现越过 max_tokens / max_cost_usd / max_turns，job 已被杀（failure_class=budget）。
  'job.budget_exceeded': { icon: TriangleAlert },
  // gofer-hnqf：agent 用尽自己的软预算并自行收尾（没有被杀，汇报仍在，任务可能未完成）。
  'job.agent_budget_exhausted': { icon: TriangleAlert },
  // GIT-02：worktree 分支的交付（合并 / 推送 / PR）。
  'job.delivered': { icon: ArrowUpToLine },
  'job.delivery_failed': { icon: TriangleAlert },
  'job.delivery_skipped': { icon: Circle },
  'job.mcp_injected': { icon: Cog },
  // 决策 hook：提交 / 交付前由 server 上配置的脚本裁决（放行、否决或收紧）。
  'job.decision_hook': { icon: Scale },
  'job.mcp_skipped': { icon: Circle },
  // AGT-04：事后捕获到 session_id（by=fallback 说明该 agent 还没写自己的正则）。
  'job.session_captured': { icon: KeyRound },
  // MCP-05 阶段 A（S4 补）：评论本身与它的三种派活结果。job / plan / todo 上的评论各自记在
  // 自己作用域的事件流里（plan / todo 走 plan:<id>），所以在 job 时间线看到的就是挂在
  // 这条 job 上的评论。
  'comment.created': { icon: Pencil },
  'comment.triggered': { icon: ArrowRight },
  'comment.trigger_throttled': { icon: Timer },
  'comment.mention_rejected': { icon: TriangleAlert },
  // JOB-10（S4 补）：技能挂载收据——本机直写或 worker 走 uploads 通道；跳过则说明原因
  // （worker_protocol = 老 worker，peer_runner = peer 没有通道）。
  'job.skills_mounted': { icon: Boxes },
  'job.skills_skipped': { icon: TriangleAlert },
  // JOB-06①：强制规则已注入 prompt（names = 注入的名字，bytes = 正文总字节）。
  // 跳过只在项目根的 .gofer/RULES.md 读不到时记（reason: project_file_unreachable）。
  'job.rules_injected': { icon: ScrollText },
  'job.rules_skipped': { icon: TriangleAlert },
  // gofer-3db4：定时任务 / plan 派发 / 续投等 gofer 自己发起的 job 要求「结束时通知」，
  // 但目标会话已不可寻址（不存在 / 属于别的 caller）——通知被丢弃、job 照常跑。
  'job.notify_dropped': { icon: BellOff },
  // MCP-05 阶段 B（S4 补）：leader 回合。它们记在 PLAN 作用域（scope id 为 plan:<id>），
  // 所以只在按 plan 作用域查事件流时出现。
  'plan.leader_woken': { icon: CircleDot },
  'plan.leader_skipped': { icon: SkipForward },
  'plan.leader_cancelled': { icon: Ban },
  'plan.leader_exhausted': { icon: Flag },
}

export function eventIcon(type: string): LucideIcon {
  return EVENT_META[type]?.icon ?? Circle
}
export function eventLabel(type: string): string {
  const key = `util.event.${type}`
  return te(key) ? t(key) : type
}

// 解析 detail_json，提取每类事件的关键字段拼成一行补充说明（无则空）。
export function eventDetailText(ev: JobEvent): string {
  if (!ev.detail) {
    return ''
  }
  let d: Record<string, unknown>
  try {
    d = JSON.parse(ev.detail) as Record<string, unknown>
  } catch {
    return ''
  }
  switch (ev.type) {
    case 'job.submitted':
      return [d.agent, d.runner].filter(Boolean).join(' · ')
    case 'job.dispatched':
      return [d.runner, d.worker_id].filter(Boolean).join(' · ')
    case 'job.terminal': {
      const parts: string[] = []
      if (d.status) {
        parts.push(String(d.status))
      }
      if (typeof d.exit_code === 'number' && d.exit_code !== 0) {
        parts.push(`exit ${d.exit_code}`)
      }
      if (d.error) {
        parts.push(String(d.error))
      }
      return parts.join(' · ')
    }
    // gofer-9b1b：待批理由与过期时间；决定类事件写谁批的 / 拒的与备注。
    case 'job.awaiting_approval': {
      const parts: string[] = []
      if (d.reason) parts.push(String(d.reason))
      if (typeof d.expires_at === 'number' && d.expires_at > 0) {
        parts.push(t('util.eventDet.needsApproval', { time: new Date(d.expires_at * 1000).toLocaleString() }))
      }
      if (d.origin) parts.push(t('util.eventDet.from', { origin: String(d.origin) }))
      return parts.join(' · ')
    }
    case 'job.hold_approved':
    case 'job.hold_rejected':
      return [d.by && `by ${d.by}`, d.note].filter(Boolean).map(String).join(' · ')
    case 'job.hold_expired':
      return typeof d.expires_at === 'number' && d.expires_at > 0
        ? t('util.eventDet.expiredAt', { time: new Date(d.expires_at * 1000).toLocaleString() })
        : ''
    case 'job.uncommitted':
      return t('util.eventDet.files', { n: Number(d.count ?? 0) })
    case 'job.budget_exceeded':
      return t('util.eventDet.budget', { limit: String(d.limit ?? ''), max: String(d.max ?? ''), used: String(d.used ?? '') })
    case 'job.agent_budget_exhausted': {
      const parts: string[] = []
      if (Number(d.limit_tokens ?? 0) > 0) parts.push(`${d.used_tokens ?? 0} / ${d.limit_tokens} tokens`)
      if (Number(d.limit_cost_usd ?? 0) > 0) parts.push(`$${d.used_cost_usd ?? 0} / $${d.limit_cost_usd}`)
      if (d.scope) parts.push(String(d.scope))
      if (d.estimated) parts.push(t('util.eventDet.estimated'))
      return parts.join(' · ')
    }
    case 'job.delivered':
    case 'job.delivery_failed':
      return [d.mode, d.state, d.branch, d.pr_url, d.error || d.note].filter(Boolean).map(String).join(' · ')
    case 'job.delivery_skipped':
      return String(d.reason ?? '')
    case 'job.mcp_injected':
      return Array.isArray(d.servers) ? d.servers.join(', ') : ''
    case 'job.decision_hook': {
      const head = `${d.hook ?? ''} · ${d.point ?? ''} · ${t(d.decision === 'deny' ? 'util.eventDet.deny' : 'util.eventDet.allow')}`
      const patch = d.patch && typeof d.patch === 'object' ? ` · ${t('util.eventDet.tighten', { keys: Object.keys(d.patch).join(', ') })}` : ''
      const why = d.error ? t('util.eventDet.hookError', { error: String(d.error) }) : d.reason ? t('util.eventDet.reason', { reason: String(d.reason) }) : ''
      return head + patch + why
    }
    case 'interaction.created':
      return String(d.prompt ?? '')
    case 'interaction.answered':
      return String(d.answer ?? '')
    // 审批门（GATE-01 S1）：求批/作答/超时都带工具调用与选项，时间线据此可读；
    // acp 回合汇总（bd h-aii-rnxk）给的是本回合的执行计数。
    case 'job.acp_summary': {
      // permissions_auto (F4) = 本回合 gofer 自动裁决的求批数（off / auto_allow_kind /
      // remembered / timeout）。自动裁决不再各占一条 job.permission_answered，所以
      // 时间线上只有这里能看到它们。
      const auto = Number(d.permissions_auto ?? 0)
      const asks = t('util.eventDet.asks', { n: Number(d.permissions ?? 0) })
      const parts = [
        t('util.eventDet.toolCalls', { n: Number(d.tool_calls ?? 0) }),
        t('util.eventDet.thoughts', { n: Number(d.thoughts ?? 0) }),
        auto > 0 ? t('util.eventDet.asksAuto', { asks, n: auto }) : asks,
      ]
      if (d.stop_reason) {
        parts.push(String(d.stop_reason))
      }
      return parts.join(' · ')
    }
    case 'job.permission_requested':
      return [d.kind, d.title, d.policy_hint].filter(Boolean).join(' · ')
    case 'job.permission_answered': {
      const parts = [d.kind, d.option_id].filter(Boolean).map(String)
      if (d.auto === true) {
        parts.push(t('util.eventDet.auto'))
      } else if (d.by) {
        parts.push(`by ${d.by}`)
      }
      return parts.join(' · ')
    }
    case 'job.permission_timed_out':
      return [d.kind, d.title, `on_timeout=${d.on_timeout ?? 'reject'}`].filter(Boolean).join(' · ')
    // JOB-11：谁在占着目录（芯片/详情只给结论，时间线说清"等的是谁"）。
    case 'job.waiting_dir':
      return [`holder=${d.holder_job ?? '?'}`, d.dir].filter(Boolean).join(' · ')
    // AUTO-05：静默了多久、窗口多长——解释 job 为什么被判为停滞。
    case 'job.stalled':
      return [t('util.eventDet.silent', { s: String(d.silent_sec ?? '?') }), t('util.eventDet.window', { s: String(d.stall_timeout_sec ?? '?') })].join(' · ')
    // AGT-04：哪个 agent、从哪读到、是内置/显式正则还是通用兜底——by=fallback
    // 就是「该给这个 agent 写条 session_capture」的信号。
    case 'job.session_captured':
      return [`agent=${d.agent ?? '?'}`, d.source, d.by === 'fallback' ? t('util.eventDet.fallback') : t('util.eventDet.agentConfig')]
        .filter(Boolean)
        .join(' · ')
    // MCP-05（S4 补）：谁在什么身份下说了话、@了谁——派活的三行据此区分。
    case 'comment.created': {
      const parts = [d.author, d.author_kind].filter(Boolean).map(String)
      const mentions = Array.isArray(d.mentions) ? (d.mentions as unknown[]).map(String) : []
      if (mentions.length > 0) parts.push(mentions.map((m) => `@${m}`).join(' '))
      return parts.join(' · ')
    }
    case 'comment.triggered':
      return [d.mention && `@${d.mention}`, d.job_id].filter(Boolean).map(String).join(' · ')
    case 'comment.trigger_throttled':
      return [d.reason, t('util.eventDet.commentIndex', { n: String(d.count ?? '?') })].filter(Boolean).map(String).join(' · ')
    case 'comment.mention_rejected':
      return [d.mention && `@${d.mention}`, d.reason, d.error].filter(Boolean).map(String).join(' · ')
    // JOB-10（S4 补）：挂了哪些技能 / 为什么没挂。
    case 'job.skills_mounted': {
      const names = Array.isArray(d.names) ? (d.names as unknown[]).map(String) : []
      return [names.join(', '), d.via].filter(Boolean).join(' · ')
    }
    case 'job.skills_skipped': {
      const names = Array.isArray(d.names) ? (d.names as unknown[]).map(String) : []
      return [d.reason, names.join(', ')].filter(Boolean).join(' · ')
    }
    // JOB-06①：注入了哪些规则、共多少字节（bytes 是剥掉 frontmatter 的正文总长）；
    // 跳过只在项目根的 .gofer/RULES.md 读不到时记（reason=project_file_unreachable）。
    case 'job.rules_injected': {
      const names = Array.isArray(d.names) ? (d.names as unknown[]).map(String) : []
      return [names.join(', '), typeof d.bytes === 'number' ? `${d.bytes} B` : '']
        .filter(Boolean)
        .join(' · ')
    }
    case 'job.rules_skipped':
      return [d.reason, d.name].filter(Boolean).map(String).join(' · ')
    // gofer-3db4：哪一条通知被丢弃、为什么（target 是要通知的会话）。
    case 'job.notify_dropped':
      return [d.target, d.reason].filter(Boolean).join(' · ')
    // MCP-05 阶段 B（S4 补）：leader 回合的关键字段（轮次、接手人、成员 job）。
    case 'plan.leader_woken':
      return [t('util.eventDet.round', { n: String(d.round ?? '?') }), d.agent, d.member_status, d.leader_job].filter(Boolean).map(String).join(' · ')
    case 'plan.leader_skipped':
      return [d.reason, d.job, d.error].filter(Boolean).map(String).join(' · ')
    case 'plan.leader_cancelled':
      return [d.by && `by ${d.by}`, t('util.eventDet.cancelRounds', { n: String(d.cancelled ?? '?') })].filter(Boolean).map(String).join(' · ')
    case 'plan.leader_exhausted':
      return [t('util.eventDet.usedRounds', { n: String(d.rounds ?? '?') }), d.job].filter(Boolean).map(String).join(' · ')
    default:
      return ''
  }
}
