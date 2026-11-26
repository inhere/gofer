import { beforeEach, describe, expect, it } from 'vitest'
import { setLocale } from '../store/locale'
import type { WorkStageView } from '../api/types'
import {
  canApprove,
  canBack,
  canDone,
  stageBadgeBlocked,
  stageBadgeText,
  stageDoneNotice,
  stageLevelLabel,
  stageStepLabel,
  stageStepRows,
  stageWorkItemLink,
} from './stage'

function view(over: Partial<WorkStageView> = {}): WorkStageView {
  return {
    work_item_id: 'w-1',
    template: 'feature',
    steps: ['design', 'review', 'plan', 'doing', 'verify', 'done'],
    step: 'review',
    step_index: 1,
    step_entered_at: 1000,
    status: 'active',
    level: 'record',
    review_max: 2,
    ...over,
  }
}

beforeEach(() => {
  setLocale('zh-CN')
})

describe('stageStepRows', () => {
  it('marks earlier steps passed, the current step current and the rest future', () => {
    const rows = stageStepRows(view())
    expect(rows.map((r) => r.state)).toEqual(['passed', 'current', 'future', 'future', 'future', 'future'])
    expect(rows.map((r) => r.name)).toEqual(['design', 'review', 'plan', 'doing', 'verify', 'done'])
  })

  it('shows the blocked style on the current step of a blocked flow', () => {
    const rows = stageStepRows(view({ status: 'blocked', blocked_reason: '等设备' }))
    expect(rows[1].state).toBe('blocked')
  })

  it('marks every step passed once the flow is finished or dropped', () => {
    for (const status of ['done', 'dropped'] as const) {
      expect(stageStepRows(view({ status, step: 'done', step_index: 5 })).every((r) => r.state === 'passed')).toBe(true)
    }
  })

  it('carries the gate missing items on the current step only', () => {
    const rows = stageStepRows(view({ gate: { step: 'review', ok: false, missing: ['需要一个评审 job'], needs_human: true } }))
    expect(rows[1].missing).toEqual(['需要一个评审 job'])
    expect(rows[1].needsHuman).toBe(true)
    expect(rows[0].missing).toEqual([])
  })
})

describe('stage actions', () => {
  it('offers approve on the review step even without a gate', () => {
    expect(canApprove(view())).toBe(true)
    expect(canApprove(view({ step: 'plan', step_index: 2 }))).toBe(false)
  })

  it('offers approve when the gate cannot be judged here', () => {
    expect(canApprove(view({ step: 'doing', step_index: 3, gate: { step: 'doing', ok: false, missing: [], needs_human: true } }))).toBe(true)
  })

  it('refuses every action once the flow ended', () => {
    const ended = view({ status: 'done' })
    expect(canApprove(ended)).toBe(false)
    expect(canBack(ended)).toBe(false)
    expect(canDone(ended)).toBe(false)
  })

  it('only allows going back when a previous step exists', () => {
    expect(canBack(view({ step: 'design', step_index: 0 }))).toBe(false)
    expect(canBack(view())).toBe(true)
  })
})

describe('texts', () => {
  it('localizes the built-in step names', () => {
    expect(stageStepLabel('design')).toBe('设计')
    expect(stageStepLabel('verify')).toBe('验证')
    // 模板里的自定义步骤名原样显示
    expect(stageStepLabel('ship-it')).toBe('ship-it')
  })

  it('names the two autonomy levels', () => {
    expect(stageLevelLabel('record')).toBe('只记录')
    expect(stageLevelLabel('remind')).toBe('提醒')
  })

  it('lists what the gate still wants instead of an error', () => {
    expect(stageDoneNotice({ step: 'design', ok: false, missing: ['设计稿还没提交', '前 20 行没有 issue id'] }))
      .toBe('门禁还没过：设计稿还没提交; 前 20 行没有 issue id')
    expect(stageDoneNotice({ step: 'doing', ok: false, missing: [], needs_human: true })).toBe('这一步在这里判不了，需要人确认')
  })

  it('builds the thread badge from the session brief', () => {
    const brief = { work_item_id: 'w-1', step: 'doing', step_no: 4, step_total: 6, status: 'active' as const }
    expect(stageBadgeText(brief)).toBe('实施 4/6')
    expect(stageBadgeBlocked(brief)).toBe(false)
    expect(stageWorkItemLink(brief)).toBe('/work?id=w-1')
    expect(stageBadgeBlocked({ ...brief, status: 'blocked' })).toBe(true)
    // 没有进行中的流程时什么都不显示
    expect(stageBadgeText({ enabled: true })).toBe('')
    expect(stageWorkItemLink({ enabled: true })).toBe('')
  })
})
