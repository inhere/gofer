// plan 详情「推进记录」页签（设计 2026-11-16 §1，计划 R12）：未启用 / 已启用两种形态、
// 400 拒绝的提示、一致率与「建议升级」的读法。
//
// 仓库没有组件挂载库（无 @vue/test-utils），所以按既有约定分两层：
//  - 状态判定、预算换算、一致率与错误分类在 utils/planDriver.ts，由纯函数用例覆盖；
//  - 组件与详情页的接线（按钮、表单、表格、两个接口）用 ?raw 读源码断言。
import { describe, expect, it } from 'vitest'
import type { PlanDriverReportRow, PlanDriverStatus } from '../api/types'
import {
  DRIVER_LEVEL_RECORD,
  driverActionLabel,
  driverBudgetDraft,
  driverBudgetFromDraft,
  driverBudgetLine,
  driverErrorText,
  driverRateText,
  driverRecentText,
  driverRuleLabel,
  driverSpentLine,
  driverState,
  driverUpgradeHint,
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
