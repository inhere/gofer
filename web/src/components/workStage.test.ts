import { beforeEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { setLocale } from '../store/locale'
import type { StageGate, WorkStageView } from '../api/types'
import WorkStage from './WorkStage.vue'

// 只测用户看得见的结果：步骤条的格子状态、模板名、门禁缺项、阻塞说明、三个动作的显隐与可点性。
function stage(over: Partial<WorkStageView> = {}, gate?: StageGate): WorkStageView {
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
    gate,
    ...over,
  }
}

function render(s: WorkStageView, props: Record<string, unknown> = {}): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(WorkStage as any, { stage: s, nowSec: 1060, ...props }) }))
}

function tag(html: string, hook: string): string {
  return html.match(new RegExp(`<button[^>]*data-test="${hook}"[^>]*>`))?.[0] ?? ''
}

beforeEach(() => {
  setLocale('zh-CN')
})

describe('WorkStage', () => {
  it('shows the template, the current step and one cell per step with its state', async () => {
    const html = await render(stage())
    expect(html).toContain('流程 · feature')
    expect(html).toContain('评审')
    const states = [...html.matchAll(/data-test="stage-step-\d"[\s\S]{0,40}?data-state="(\w+)"/g)].map((m) => m[1])
    expect(states).toEqual(['passed', 'current', 'future', 'future', 'future', 'future'])
    // 空格子用 · 而不是把文字藏掉
    expect(html).toContain('stage-step--passed')
  })

  it('marks a blocked flow on the current cell and prints the reason', async () => {
    const html = await render(stage({ status: 'blocked', blocked_reason: '等设备' }))
    expect(html).toContain('data-state="blocked"')
    expect(html).toContain('阻塞：等设备')
    expect(html).toContain('data-test="stage-blocked"')
  })

  it('lists what the gate still wants, and says when this machine cannot judge it', async () => {
    const html = await render(
      stage({ step: 'doing', step_index: 3 }, { step: 'doing', ok: false, missing: ['关联 plan 的 todo 还没全完成'], needs_human: true }),
    )
    expect(html).toContain('data-test="stage-missing"')
    expect(html).toContain('关联 plan 的 todo 还没全完成')
    expect(html).toContain('data-test="stage-needs-human"')
  })

  it('offers approve on the review step and advance / send back everywhere else', async () => {
    const review = await render(stage())
    expect(review).toContain('data-test="stage-approve"')
    expect(review).toContain('data-test="stage-done"')

    const plan = await render(stage({ step: 'plan', step_index: 2 }))
    expect(plan).not.toContain('data-test="stage-approve"')
    expect(plan).toContain('data-test="stage-done"')

    // 第一步没有上一步可退：按钮在但不可点
    const first = await render(stage({ step: 'design', step_index: 0, steps: ['design', 'review'] }))
    expect(tag(first, 'stage-back-toggle')).toContain('disabled')
  })

  it('hides the actions of a finished flow and says so', async () => {
    const html = await render(stage({ status: 'done', step: 'done', step_index: 5 }))
    expect(html).toContain('流程已走完')
    expect(html).not.toContain('data-test="stage-approve"')
    expect(html).not.toContain('data-test="stage-done"')
  })

  it('shows the autonomy level with the current one pressed', async () => {
    const html = await render(stage({ level: 'remind' }))
    expect(tag(html, 'stage-level-remind')).toContain('aria-pressed="true"')
    expect(tag(html, 'stage-level-record')).toContain('aria-pressed="false"')
    expect(html).toContain('提醒')
    expect(html).toContain('只记录')
  })

  it('asks for a reason before sending the flow back', async () => {
    const html = await render(stage())
    expect(html).not.toContain('data-test="stage-back-reason"')
    expect(html).toContain('data-test="stage-back-toggle"')
  })

  it('keeps the buttons disabled while a request is in flight', async () => {
    const html = await render(stage(), { busy: true })
    expect(tag(html, 'stage-done')).toContain('disabled')
    expect(html).toContain('处理中…')
  })
})
