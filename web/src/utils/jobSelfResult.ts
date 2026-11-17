// job 自报终态（gofer_submit_result，计划 R9 / R12）的纯逻辑：把 jobs.result_report_json
// 解成「需人处理事项 + 发现列表」，给决策值一个界面名。
//
// 后端只记录与展示自报终态（internal/job/selfresult.go）：它不改 job 的 status、不触发续跑、
// 也不让 job 失败。所以这里只读，没有任何写入口；解析失败（旧版本 / 脏数据）时退回
// result_reason，不把整块藏起来。
import { t, te } from '../i18n'
import type { Job, JobSelfResultReport } from '../api/types'

// 四个决策值（后端 ValidSelfResultDecision）。
export const SELF_RESULT_DECISIONS = ['pass', 'rework', 'fail', 'human'] as const

export interface SelfResultView {
  decision: string
  label: string
  reason: string
  actionRequired?: string
  findings: string[]
}

// selfDecisionLabel 是决策值的界面名；未知值原样显示（新版服务端多出来的决策）。
export function selfDecisionLabel(decision: string): string {
  const key = `jobs.selfResult.decision.${decision}`
  return te(key) ? t(key) : decision
}

// parseSelfReport 读 result_report_json：字段与 SelfResultInput 一致（decision / reason /
// action_required / findings）。读不出来时返回空表单，调用方用行上的 result_reason 兜底。
export function parseSelfReport(raw: string | undefined): Partial<JobSelfResultReport> {
  if (!raw) return {}
  try {
    const doc = JSON.parse(raw) as Record<string, unknown>
    const out: Partial<JobSelfResultReport> = {}
    if (typeof doc.reason === 'string') out.reason = doc.reason.trim()
    if (typeof doc.action_required === 'string') out.action_required = doc.action_required.trim()
    if (Array.isArray(doc.findings)) {
      out.findings = doc.findings
        .filter((f): f is string => typeof f === 'string')
        .map((f) => f.trim())
        .filter((f) => f !== '')
    }
    return out
  } catch {
    return {}
  }
}

// selfResultView 把一条 job 变成自报终态的展示模型；没有自报过时返回 null（整块不渲染）。
export function selfResultView(job: Job | null | undefined): SelfResultView | null {
  const decision = job?.result_decision ?? ''
  if (!job || decision === '') return null
  const report = parseSelfReport(job.result_report_json)
  return {
    decision,
    label: selfDecisionLabel(decision),
    reason: report.reason || job.result_reason || '',
    actionRequired: report.action_required,
    findings: report.findings ?? [],
  }
}
