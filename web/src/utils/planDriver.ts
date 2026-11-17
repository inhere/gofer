// plan 推进记录页签的纯逻辑（设计 2026-11-16 §1，计划 R4 / R7 / R12）：等级 / 动作 / 规则的
// 文案、预算与已用量的读法、一致率与「建议升级」的判定、把服务端答复的错误分成「你填的
// 需要改」与「服务端/网络出问题」。组件只管接线与渲染，判定都放这里，便于单测。
//
// 口径与后端一一对应（internal/job/plandriver_state.go、internal/job/driver/types.go）：
// 预算三项 0 = 不限；rate = match / (match + mismatch + no_followup)，没有已观察的动作时为 0；
// 升级建议 = 最近 PlanDriverUpgradeWindow(20) 个已观察动作 ≥ 90% 一致，且本版只显示不升级。
import { t, te } from '../i18n'
import type { PlanDriverBudget, PlanDriverReportRow, PlanDriverSpent, PlanDriverStatus } from '../api/types'

// 本批唯一允许的等级（assist / auto 会被服务端 400 拒绝）。
export const DRIVER_LEVEL_RECORD = 'record'

// driverState 是页签的三种状态：还没读到（loading）、读到且未启用（off）、已启用（on）。
// 未启用时服务端给 enabled=false + 空 level + 全 0 预算，不会 404（plan 不存在才是 404）。
export type DriverState = 'loading' | 'off' | 'on'

export function driverState(status: PlanDriverStatus | null): DriverState {
  if (!status) return 'loading'
  return status.enabled ? 'on' : 'off'
}

// 与 job.PlanDriverUpgradeWindow / PlanDriverUpgradeRate 一致，只用于文案里的阈值说明。
export const DRIVER_UPGRADE_WINDOW = 20
export const DRIVER_UPGRADE_RATE = 0.9

// driverLevelLabel 给等级一个界面名；未知等级原样显示（新版服务端）。
export function driverLevelLabel(level: string | undefined): string {
  const key = `plans.driver.level.${level ?? ''}`
  return level && te(key) ? t(key) : (level || '—')
}

// driverActionLabel 给动作一个界面名；未知动作原样显示。
export function driverActionLabel(action: string): string {
  const key = `plans.driver.action.${action}`
  return te(key) ? t(key) : action
}

// driverRuleLabel 给规则一个界面名；未知规则原样显示（规则名是稳定标识，兜底显示也不误导）。
export function driverRuleLabel(rule: string | undefined): string {
  if (!rule) return '—'
  const key = `plans.driver.rule.${rule}`
  return te(key) ? t(key) : rule
}

// 风险三档（与 internal/job/driver/types.go 的 RiskLow / RiskMedium / RiskRedline 一致）。
export const DRIVER_RISK_LOW = 'low'
export const DRIVER_RISK_MEDIUM = 'medium'
export const DRIVER_RISK_REDLINE = 'redline'

// driverRiskLabel 给风险一个界面名；未知风险原样显示（不把没见过的档位说成低风险）。
export function driverRiskLabel(risk: string | undefined): string {
  const key = `plans.driver.risk.${risk ?? ''}`
  return risk && te(key) ? t(key) : (risk || '—')
}

// driverRiskTone 是徽标的配色档：只有低 / 中 / 红线三种；未知风险按红线涂（与后端
// 「风险表里没有的动作就是红线」同一口径，失败朝安全的方向显示）。
export function driverRiskTone(risk: string | undefined): string {
  if (risk === DRIVER_RISK_LOW) return DRIVER_RISK_LOW
  if (risk === DRIVER_RISK_MEDIUM) return DRIVER_RISK_MEDIUM
  return DRIVER_RISK_REDLINE
}

// driverOutcomeLabel 读一条动作的结果：空 = 还没观察满观察窗（待观察），不是「不一致」。
export function driverOutcomeLabel(outcome: string | undefined): string {
  const key = `plans.driver.outcome.${outcome || 'pending'}`
  return te(key) ? t(key) : (outcome || '—')
}

// driverWouldPauseText 是「本会暂停」一列；record 等级下只记录，false 也要明确写出来，
// 免得一个空格被看成本会暂停。
export function driverWouldPauseText(wouldPause: boolean | undefined): string {
  return wouldPause ? t('plans.driver.pauseYes') : t('plans.driver.pauseNo')
}

// driverParamsText 把一条动作的 params 展开成可读 JSON（缩进两格）；没有参数返回空串，
// 由调用方显示占位符。字符串原样给出，其余交给 JSON.stringify（失败退回 String）。
export function driverParamsText(params: unknown): string {
  if (params === undefined || params === null) return ''
  if (typeof params === 'string') return params
  try {
    return JSON.stringify(params, null, 2)
  } catch {
    return String(params)
  }
}

// driverLimit 是一个预算项的可读值：0（及负数，理论上不该有）= 不限，用 ∞ 表示。
export function driverLimit(n: number): string {
  return n > 0 ? String(n) : '∞'
}

// driverBudgetLine 是「任务 n / 返工 n / 时长 n 小时」的一行预算。
export function driverBudgetLine(b: PlanDriverBudget): string {
  return [
    t('plans.driver.budgetJobs', { n: driverLimit(b.max_jobs) }),
    t('plans.driver.budgetRework', { n: driverLimit(b.max_rework) }),
    t('plans.driver.budgetHours', { n: driverLimit(b.max_hours) }),
  ].join(' / ')
}

// driverSpentLine 是已用量的一行（与预算同一口径，便于对照）。
export function driverSpentLine(s: PlanDriverSpent): string {
  return t('plans.driver.spentValue', {
    jobs: s.jobs,
    rework: s.rework,
    hours: s.hours.toFixed(s.hours >= 10 ? 0 : 1),
  })
}

// driverResolved 是「已经观察出结果」的动作数：没观察满观察窗的不算，所以一致率的分母是它。
export function driverResolved(row: PlanDriverReportRow): number {
  return row.match + row.mismatch + row.no_followup
}

// driverRateText 是一致率：没有已观察的动作时显示「—」（0/0 不是 0%）。
export function driverRateText(row: PlanDriverReportRow): string {
  return driverResolved(row) > 0 ? `${Math.round(row.rate * 100)}%` : t('plans.driver.rateNone')
}

// driverRecentText 是「最近 n 次里 m 个一致（x%）」——升级建议的依据。
export function driverRecentText(row: PlanDriverReportRow): string {
  const n = row.recent_n ?? 0
  if (n === 0) return t('plans.driver.rateNone')
  return t('plans.driver.recentValue', {
    match: row.recent_match ?? 0,
    n,
    rate: `${Math.round((row.recent_rate ?? 0) * 100)}%`,
  })
}

// driverUpgradeHint 是升级建议的一句话：达成时说明条件已满足且本版不会自动升级，否则说明还差
// 多少次观察（避免把「还没观察满」看成「不一致」）。
export function driverUpgradeHint(row: PlanDriverReportRow): string {
  const n = row.recent_n ?? 0
  if (row.upgrade_suggested) {
    return t('plans.driver.upgradeReached', {
      n: DRIVER_UPGRADE_WINDOW,
      pct: Math.round(DRIVER_UPGRADE_RATE * 100),
    })
  }
  return t('plans.driver.upgradePending', {
    n: DRIVER_UPGRADE_WINDOW,
    seen: n,
    pct: Math.round(DRIVER_UPGRADE_RATE * 100),
  })
}

// 预算表单的三项输入（字符串，允许空 = 不限；表单不改 server 的 0 语义）。
export interface DriverBudgetDraft {
  maxJobs: string
  maxRework: string
  maxHours: string
}

// driverBudgetDraft 用当前值填表单：0（不限）留空，比显示「0」更少歧义。
export function driverBudgetDraft(b: PlanDriverBudget): DriverBudgetDraft {
  const cell = (n: number): string => (n > 0 ? String(n) : '')
  return { maxJobs: cell(b.max_jobs), maxRework: cell(b.max_rework), maxHours: cell(b.max_hours) }
}

// parseDriverLimit 读一项预算输入：空 = 不限(0)，必须是非负数字（小数只允许在小时上）。
// 返回 undefined 表示这一项填得不对，调用方给出「预算不合法」的提示、不发请求。
export function parseDriverLimit(raw: string, allowFraction = false): number | undefined {
  const text = raw.trim()
  if (text === '') return 0
  if (!/^\d+(\.\d+)?$/.test(text)) return undefined
  if (!allowFraction && text.includes('.')) return undefined
  const n = Number(text)
  if (!Number.isFinite(n) || n < 0) return undefined
  return n
}

// driverBudgetFromDraft 把表单读成预算；有一项不合法就返回 error 文案（用 errorKey 说明是表单，
// 而不是服务端拒绝的），不发请求。
export function driverBudgetFromDraft(d: DriverBudgetDraft): { budget?: PlanDriverBudget; error?: string } {
  const jobs = parseDriverLimit(d.maxJobs)
  const rework = parseDriverLimit(d.maxRework)
  const hours = parseDriverLimit(d.maxHours, true)
  if (jobs === undefined || rework === undefined || hours === undefined) {
    return { error: t('plans.driver.budgetInvalid') }
  }
  return { budget: { max_jobs: jobs, max_rework: rework, max_hours: hours } }
}

// apiErrorStatus 读 ApiError 的 HTTP 状态（非 ApiError 的异常没有状态）。
function apiErrorStatus(e: unknown): number | undefined {
  const status = (e as { status?: unknown } | null)?.status
  return typeof status === 'number' ? status : undefined
}

// driverErrorText 把一次开关 / 读取的失败翻成界面文案：400 是这次设置需要改（等级、预算），
// 503 是服务端没装配推进引擎，其余按一般失败处理。服务端的 detail 原样带出来，不吞信息。
export function driverErrorText(e: unknown): string {
  const err = e instanceof Error ? e.message : String(e)
  switch (apiErrorStatus(e)) {
    case 400:
      return t('plans.driver.errorInvalid', { err })
    case 404:
      return t('plans.driver.errorUnknownPlan', { err })
    case 503:
      return t('plans.driver.errorUnavailable', { err })
    default:
      return t('plans.driver.errorFailed', { err })
  }
}
