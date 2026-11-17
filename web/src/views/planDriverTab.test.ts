// plan 详情「推进记录」页签（设计 2026-11-16 §1，计划 R12）：未启用 / 已启用两种形态、
// 400 拒绝的提示、一致率与「建议升级」的读法。
//
// 仓库没有组件挂载库（无 @vue/test-utils），所以按既有约定分两层：
//  - 状态判定、预算换算、一致率与错误分类在 utils/planDriver.ts，由纯函数用例覆盖；
//  - 组件与详情页的接线（按钮、表单、表格、两个接口）用 ?raw 读源码断言。
import { describe, expect, it } from 'vitest'
import type { PlanDriverAction, PlanDriverReportRow, PlanDriverStatus } from '../api/types'
import {
  DRIVER_LEVEL_RECORD,
  driverActionLabel,
  driverBudgetDraft,
  driverBudgetFromDraft,
  driverBudgetLine,
  driverErrorText,
  driverOutcomeLabel,
  driverParamsText,
  driverRateText,
  driverRecentText,
  driverRiskLabel,
  driverRiskTone,
  driverRuleLabel,
  driverSpentLine,
  driverState,
  driverUpgradeHint,
  driverWouldPauseText,
  parseDriverLimit,
} from '../utils/planDriver'

function raw(modules: Record<string, unknown>): string {
  return Object.values(modules)[0] as string
}

const tab = raw(import.meta.glob('../components/PlanDriverTab.vue', { eager: true, query: '?raw', import: 'default' }))
const detail = raw(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const client = raw(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))

const offStatus: PlanDriverStatus = {
  plan_id: 'p1',
  enabled: false,
  budget: { max_jobs: 0, max_rework: 0, max_hours: 0 },
}

const onStatus: PlanDriverStatus = {
  plan_id: 'p1',
  enabled: true,
  level: DRIVER_LEVEL_RECORD,
  budget: { max_jobs: 20, max_rework: 3, max_hours: 12 },
  enabled_at: 1_700_000_000,
}

const row = (patch: Partial<PlanDriverReportRow> = {}): PlanDriverReportRow => ({
  action: 'dispatch_todo',
  total: 6,
  match: 4,
  mismatch: 1,
  no_followup: 1,
  pending: 0,
  rate: 4 / 6,
  ...patch,
})

// 一条动作明细（GET .../driver/report 的 recent 元素）。最老的形态：有 todo 与 job、风险低、
// 还没观察出结果（outcome 缺省）。
const action = (patch: Partial<PlanDriverAction> = {}): PlanDriverAction => ({
  id: 7,
  todo_id: 't-2',
  job_id: 'job-9',
  event_kind: 'job_done',
  action: 'complete_todo',
  rule: 'job_done_complete',
  risk: 'low',
  created_at: 1_700_000_000,
  ...patch,
})

describe('driver tab state', () => {
  it('tells "not read yet" apart from "read and off" and "on"', () => {
    expect(driverState(null)).toBe('loading')
    // 未启用的 plan 也有 driver 状态（enabled=false、预算全 0），不是一个 404。
    expect(driverState(offStatus)).toBe('off')
    expect(driverState(onStatus)).toBe('on')
    expect(driverState({ ...offStatus, enabled: true, level: 'record' })).toBe('on')
  })

  it('labels the level and keeps an unknown level readable', () => {
    expect(tab).toContain('driverLevelLabel(status?.level)')
    expect(driverActionLabel('dispatch_todo')).toBe('派发待办')
    // 新版服务端多出来的动作原样显示，不显示一个空名字。
    expect(driverActionLabel('merge_to_main')).toBe('merge_to_main')
    expect(driverRuleLabel('self_report_human')).toBe('自报需人决定')
    expect(driverRuleLabel('some_future_rule')).toBe('some_future_rule')
    expect(driverRuleLabel(undefined)).toBe('—')
  })
})

describe('driver budget', () => {
  it('reads 0 as "no limit" on both sides', () => {
    expect(driverBudgetLine({ max_jobs: 0, max_rework: 0, max_hours: 0 })).toBe('任务 ∞ / 返工 ∞ / 时长 ∞ 小时')
    expect(driverBudgetLine({ max_jobs: 20, max_rework: 3, max_hours: 12 })).toBe('任务 20 / 返工 3 / 时长 12 小时')
    expect(driverSpentLine({ jobs: 4, rework: 1, hours: 2.5 })).toBe('4 个任务 / 1 次返工 / 2.5 小时')
  })

  it('round-trips the stored budget through the form (0 shows as empty)', () => {
    const draft = driverBudgetDraft({ max_jobs: 20, max_rework: 0, max_hours: 12 })
    expect(draft).toEqual({ maxJobs: '20', maxRework: '', maxHours: '12' })
    expect(driverBudgetFromDraft(draft)).toEqual({ budget: { max_jobs: 20, max_rework: 0, max_hours: 12 } })
    expect(driverBudgetFromDraft({ maxJobs: '', maxRework: '', maxHours: '' })).toEqual({
      budget: { max_jobs: 0, max_rework: 0, max_hours: 0 },
    })
  })

  it('refuses a budget the server would refuse, before sending anything', () => {
    expect(parseDriverLimit('12')).toBe(12)
    expect(parseDriverLimit('')).toBe(0)
    expect(parseDriverLimit('1.5')).toBeUndefined()
    expect(parseDriverLimit('1.5', true)).toBe(1.5)
    expect(parseDriverLimit('-1')).toBeUndefined()
    expect(parseDriverLimit('abc')).toBeUndefined()
    expect(parseDriverLimit('12abc')).toBeUndefined()
    const bad = driverBudgetFromDraft({ maxJobs: '2', maxRework: 'x', maxHours: '' })
    expect(bad.budget).toBeUndefined()
    expect(bad.error).toContain('预算')
  })
})

describe('driver agreement', () => {
  it('shows a dash instead of 0% when nothing was resolved yet', () => {
    expect(driverRateText(row({ match: 0, mismatch: 0, no_followup: 0, pending: 6, rate: 0 }))).toBe('—')
    expect(driverRateText(row())).toBe('67%')
  })

  it('reads the recent window and the suggested-upgrade condition', () => {
    expect(driverRecentText(row({ recent_n: 0 }))).toBe('—')
    expect(driverRecentText(row({ recent_n: 20, recent_match: 18, recent_rate: 0.9 }))).toBe('18/20（90%）')
    // 条件：最近 20 个已观察动作 ≥ 90%，本版只显示不升级。
    expect(driverUpgradeHint(row({ recent_n: 20, recent_match: 18, recent_rate: 0.9, upgrade_suggested: true })))
      .toContain('不会自动升级')
    expect(driverUpgradeHint(row({ recent_n: 7, recent_match: 7, recent_rate: 1 }))).toContain('7/20')
  })
})

describe('driver request errors', () => {
  it('reports a 400 as "this setting was rejected", keeping the server text', () => {
    const err = Object.assign(new Error('invalid plan driver - level assist is supported in a later version'), { status: 400 })
    const text = driverErrorText(err)
    expect(text).toContain('服务端拒绝了这次设置')
    expect(text).toContain('later version')
  })

  it('separates a missing plan, a server without a driver and a plain failure', () => {
    expect(driverErrorText(Object.assign(new Error('unknown plan'), { status: 404 }))).toContain('找不到这个 plan')
    expect(driverErrorText(Object.assign(new Error('not wired'), { status: 503 }))).toContain('没有装配推进引擎')
    expect(driverErrorText(new Error('boom'))).toContain('操作失败')
    expect(driverErrorText('plain string')).toContain('plain string')
  })
})

describe('driver tab wiring', () => {
  it('renders an off state with a single "turn on record only" action', () => {
    expect(tab).toContain('data-test="plan-driver-off"')
    expect(tab).toContain('data-test="plan-driver-enable"')
    expect(tab).toContain("t('plans.driver.offHint')")
    // 等级固定 record，其余等级本版不提供（服务端 400），界面上写明。
    expect(tab).toContain("t('plans.driver.levelNote')")
    expect(tab).toContain('DRIVER_LEVEL_RECORD')
  })

  it('shows level, enabled time, budget and spend once the driver is on', () => {
    expect(tab).toContain('data-test="plan-driver-on"')
    expect(tab).toContain('data-test="plan-driver-level"')
    expect(tab).toContain('data-test="plan-driver-budget"')
    expect(tab).toContain('data-test="plan-driver-spent"')
    expect(tab).toContain('fmtDateTime(status.enabled_at)')
    expect(tab).toContain('driverBudgetLine(status.budget)')
    expect(tab).toContain('driverSpentLine(')
  })

  it('offers the three budget fields with 0 = no limit', () => {
    expect(tab).toContain('data-test="plan-driver-form"')
    for (const field of ['maxJobs', 'maxRework', 'maxHours']) {
      expect(tab).toContain(`v-model="draft.${field}"`)
    }
    expect(tab).toContain("t('plans.driver.budgetZeroHint')")
    expect(tab).toContain('driverBudgetFromDraft(draft.value)')
  })

  it('draws the agreement tables per action and per rule, with the upgrade badge', () => {
    expect(tab).toContain('data-test="plan-driver-by-action"')
    expect(tab).toContain('data-test="plan-driver-by-rule"')
    expect(tab).toContain('driverRateText(row)')
    expect(tab).toContain('driverRecentText(row)')
    expect(tab).toContain('data-test="plan-driver-upgrade"')
    expect(tab).toContain("t('plans.driver.upgradeBadge')")
    expect(tab).toContain("t('plans.driver.emptyHint')")
  })

  it('keeps the server error on screen instead of a toast', () => {
    expect(tab).toContain('data-test="plan-driver-error"')
    expect(tab).toContain('driverErrorText(e)')
  })

  it('lets the figures be re-read without leaving the page', () => {
    // 记录会在别人推进同一个 plan 时增长，详情页的轮询不带它。
    expect(tab).toContain('data-test="plan-driver-refresh"')
    expect(tab).toContain("t('plans.driver.refresh')")
  })

  it('gates both writes on the plan write permission', () => {
    expect(tab).toContain(':disabled="busy || !canWrite"')
    expect(tab).toContain('data-test="plan-driver-gate"')
    expect(detail).toContain(':write-block="planWriteBlock()"')
  })

  it('is wired into the plan page as its own tab', () => {
    expect(detail).toContain("{ value: 'driver', label: t('plans.detail.tab.driver') }")
    expect(detail).toContain("active === 'driver'")
    expect(detail).toContain('<PlanDriverTab')
    expect(detail).toContain(':plan-id="plan.plan_id"')
  })

  it('calls the three driver entries and the report', () => {
    expect(client).toContain('export function getPlanDriver(')
    expect(client).toContain('export function putPlanDriver(')
    expect(client).toContain('export function deletePlanDriver(')
    expect(client).toContain('export function getPlanDriverReport(')
    expect(client).toContain('/driver/report`')
  })
})

describe('driver action log', () => {
  it('labels the three risk levels and fails towards the red line', () => {
    expect(driverRiskLabel('low')).toBe('低')
    expect(driverRiskLabel('medium')).toBe('中')
    expect(driverRiskLabel('redline')).toBe('红线')
    // 后端风险表里没有的动作就是红线：界面不把没见过的档位说成低风险。
    expect(driverRiskLabel('some_future_risk')).toBe('some_future_risk')
    expect(driverRiskLabel(undefined)).toBe('—')
    expect(driverRiskTone('low')).toBe('low')
    expect(driverRiskTone('medium')).toBe('medium')
    expect(driverRiskTone('redline')).toBe('redline')
    expect(driverRiskTone('some_future_risk')).toBe('redline')
    expect(driverRiskTone(undefined)).toBe('redline')
  })

  it('reads a missing outcome as pending, not as a difference', () => {
    expect(driverOutcomeLabel(undefined)).toBe('待观察')
    expect(driverOutcomeLabel('')).toBe('待观察')
    expect(driverOutcomeLabel('match')).toBe('一致')
    expect(driverOutcomeLabel('mismatch')).toBe('不一致')
    expect(driverOutcomeLabel('no_followup')).toBe('无后续')
    expect(driverOutcomeLabel('some_future_outcome')).toBe('some_future_outcome')
  })

  it('says whether the action would have paused the plan', () => {
    expect(driverWouldPauseText(true)).toBe('是')
    // record 等级下「不会暂停」也要明确写出来，不留一个空格。
    expect(driverWouldPauseText(false)).toBe('否')
    expect(driverWouldPauseText(undefined)).toBe('否')
  })

  it('formats the params as indented JSON and copes with nothing to show', () => {
    expect(driverParamsText(undefined)).toBe('')
    expect(driverParamsText(null)).toBe('')
    expect(driverParamsText({ job_id: 'job-9', notes: 2 })).toBe('{\n  "job_id": "job-9",\n  "notes": 2\n}')
    expect(driverParamsText('已派出')).toBe('已派出')
    // stringify 不了的输入（循环引用）退回 String，不让一条明细炸掉整页。
    const cyclic: Record<string, unknown> = {}
    cyclic.self = cyclic
    expect(driverParamsText(cyclic)).toBe('[object Object]')
  })

  it('reads one recorded action cell by cell', () => {
    const a = action({ risk: 'medium', would_pause: true, outcome: 'mismatch', params: { blocked_todo: 't-3' } })
    expect([driverRiskLabel(a.risk), driverWouldPauseText(a.would_pause), driverOutcomeLabel(a.outcome)]).toEqual(['中', '是', '不一致'])
    expect(driverParamsText(a.params)).toBe('{\n  "blocked_todo": "t-3"\n}')
    // 缺省的 outcome / params 是常态（刚记录下来的动作），不能显示成 error 或 undefined。
    const fresh = action()
    expect(driverOutcomeLabel(fresh.outcome)).toBe('待观察')
    expect(driverParamsText(fresh.params) || '—').toBe('—')
  })

  it('draws a row per record with the risk badge and the pause / outcome cells', () => {
    expect(tab).toContain('data-test="plan-driver-recent"')
    expect(tab).toContain('data-test="plan-driver-recent-row"')
    expect(tab).toContain('v-for="row in recentRows"')
    expect(tab).toContain('data-test="plan-driver-recent-risk"')
    expect(tab).toContain(':class="`pd-risk--${driverRiskTone(row.risk)}`"')
    expect(tab).toContain('driverRiskLabel(row.risk)')
    expect(tab).toContain('data-test="plan-driver-recent-pause"')
    expect(tab).toContain('driverWouldPauseText(row.would_pause)')
    expect(tab).toContain('data-test="plan-driver-recent-outcome"')
    expect(tab).toContain('driverOutcomeLabel(row.outcome)')
    expect(tab).toContain('driverActionLabel(row.action)')
    expect(tab).toContain('driverRuleLabel(row.rule)')
  })

  it('links the row to its todo and its job', () => {
    expect(tab).toContain('data-test="plan-driver-recent-todo"')
    expect(tab).toContain('@click.stop="openTodo(row.todo_id)"')
    expect(tab).toContain("emit('open-todo', todoId)")
    expect(tab).toContain('data-test="plan-driver-recent-job"')
    expect(tab).toContain(':to="`/jobs/${encodeURIComponent(row.job_id)}`"')
    expect(detail).toContain('@open-todo="openTodoById"')
    // 条目已被删时不开一个空抽屉（抽屉的数据是本页那一份 plan）。
    expect(detail).toContain('function openTodoById(todoId: string): void')
    expect(detail).toContain('plan.value?.todos.find((x) => x.todo_id === todoId)')
  })

  it('expands one row to its params and says so when there is nothing yet', () => {
    expect(tab).toContain('data-test="plan-driver-recent-params"')
    expect(tab).toContain('v-if="expanded.has(row.id)"')
    expect(tab).toContain('driverParamsText(row.params)')
    expect(tab).toContain('toggleRow(row.id)')
    expect(tab).toContain('data-test="plan-driver-recent-empty"')
    expect(tab).toContain("t('plans.driver.recentEmptyHint')")
    expect(tab).toContain("t('plans.driver.recentHint', { n: recentRows.length })")
    expect(tab).toContain("t('plans.driver.recentTitle')")
  })
})
