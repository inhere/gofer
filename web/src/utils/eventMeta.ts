// 事件时间线的词汇表（E13 起逐步补全）：type -> 图标/中文标签，以及 detail_json 的一行摘要。
// JobDetail 的「事件时间线」与 PlanDetail 的 plan 事件流共用这一份 —— 事件类型随功能增补，
// 两处各写一份必然会漂移。渲染方式（图标 + 标签 + 摘要 + 时间）由各页面自己决定。
import type { JobEvent } from '../api/types'

// 事件 type -> 图标 + 中文标签（仿 interactions 渲染风格，单行）。
export const EVENT_META: Record<string, { icon: string; label: string }> = {
  'job.submitted': { icon: '✓', label: '已提交' },
  'job.dispatched': { icon: '→', label: '已派发' },
  'job.running': { icon: '▶', label: '开始运行' },
  'job.terminal': { icon: '■', label: '结束' },
  'job.cancelled': { icon: '✕', label: '请求取消' },
  'interaction.created': { icon: '?', label: '发起交互' },
  'interaction.answered': { icon: '✎', label: '交互已答' },
  // 审批门（GATE-01 S1）+ acp 回合汇总（bd h-aii-rnxk：时间线只留生命周期，
  // 工具调用等执行细节走 stderr 的紧凑事件行，由 NdjsonTimeline 渲染）
  'job.acp_summary': { icon: '⚙', label: 'ACP 回合' },
  'job.permission_requested': { icon: '⚠', label: '求批' },
  'job.permission_answered': { icon: '✎', label: '审批已答' },
  'job.permission_timed_out': { icon: '⏱', label: '审批超时' },
  // JOB-11 / AUTO-05：等目录锁（非终态，等同 queued）与输出停滞（job 已被看门狗杀掉）。
  'job.waiting_dir': { icon: '⏳', label: '等目录锁' },
  'job.stalled': { icon: '⚠', label: '输出停滞（已杀）' },
  // AGT-04：事后捕获到 session_id（by=fallback 说明该 agent 还没写自己的正则）。
  'job.session_captured': { icon: '⚿', label: '捕获会话' },
  // MCP-05 阶段 A（S4 补）：评论本身与它的三种派活结果。job / plan / todo 上的评论各自记在
  // 自己作用域的事件流里（plan / todo 走 plan:<id>），所以在 job 时间线看到的就是挂在
  // 这条 job 上的评论。
  'comment.created': { icon: '✎', label: '评论' },
  'comment.triggered': { icon: '→', label: '评论派活' },
  'comment.trigger_throttled': { icon: '⏱', label: '派活被限流' },
  'comment.mention_rejected': { icon: '⚠', label: '提及未派活' },
  // JOB-10（S4 补）：技能挂载收据——本机直写或 worker 走 uploads 通道；跳过则说明原因
  // （worker_protocol = 老 worker，peer_runner = peer 没有通道）。
  'job.skills_mounted': { icon: '❖', label: '已挂载技能' },
  'job.skills_skipped': { icon: '⚠', label: '技能未挂载' },
  // MCP-05 阶段 B（S4 补）：leader 回合。它们记在 PLAN 作用域（scope id 为 plan:<id>），
  // 所以只在按 plan 作用域查事件流时出现。
  'plan.leader_woken': { icon: '◈', label: 'leader 回合开始' },
  'plan.leader_skipped': { icon: '⏭', label: 'leader 回合跳过' },
  'plan.leader_cancelled': { icon: '⊘', label: 'leader 回合取消' },
  'plan.leader_exhausted': { icon: '⚑', label: 'leader 轮次用尽' },
}

export function eventIcon(type: string): string {
  return EVENT_META[type]?.icon ?? '•'
}
export function eventLabel(type: string): string {
  return EVENT_META[type]?.label ?? type
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
      const permissions = `${d.permissions ?? 0} 次求批`
      const parts = [
        `${d.tool_calls ?? 0} 次工具调用`,
        `${d.thoughts ?? 0} 段思考`,
        auto > 0 ? `${permissions}（自动 ${auto}）` : permissions,
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
        parts.push('自动')
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
      return [`静默 ${d.silent_sec ?? '?'}s`, `窗口 ${d.stall_timeout_sec ?? '?'}s`].join(' · ')
    // AGT-04：哪个 agent、从哪读到、是内置/显式正则还是通用兜底——by=fallback
    // 就是「该给这个 agent 写条 session_capture」的信号。
    case 'job.session_captured':
      return [`agent=${d.agent ?? '?'}`, d.source, d.by === 'fallback' ? '兜底' : 'agent 配置']
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
      return [d.reason, `第 ${d.count ?? '?'} 条`].filter(Boolean).map(String).join(' · ')
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
    // MCP-05 阶段 B（S4 补）：leader 回合的关键字段（轮次、接手人、成员 job）。
    case 'plan.leader_woken':
      return [`第 ${d.round ?? '?'} 轮`, d.agent, d.member_status, d.leader_job].filter(Boolean).map(String).join(' · ')
    case 'plan.leader_skipped':
      return [d.reason, d.job, d.error].filter(Boolean).map(String).join(' · ')
    case 'plan.leader_cancelled':
      return [d.by && `by ${d.by}`, `取消 ${d.cancelled ?? '?'} 轮`].filter(Boolean).map(String).join(' · ')
    case 'plan.leader_exhausted':
      return [`已用 ${d.rounds ?? '?'} 轮`, d.job].filter(Boolean).map(String).join(' · ')
    default:
      return ''
  }
}
