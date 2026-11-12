import { describe, expect, it } from 'vitest'
import { plans as plansDict } from '../i18n/pages/plans'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const detail = raw(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const row = raw(import.meta.glob('../ui/PlanOverviewRow.vue', { eager: true, query: '?raw', import: 'default' }))
const client = raw(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))

describe('plan completion timing', () => {
  it('shows and edits complete_on through the plan PATCH endpoint', () => {
    expect(detail).toContain('data-test="plan-complete-on"')
    expect(detail).toContain('setPlanCompleteOn(props.id, next)')
    expect(client).toContain('JSON.stringify({ complete_on: completeOn })')
  })

  it('shows the timing on the plans overview row', () => {
    expect(row).toContain('plan.complete_on === \'merge\'')
    expect(row).toContain('plans.overview.completeOnMerge')
  })

  it('keeps complete_on labels paired in Chinese and English', () => {
    for (const key of ['plans.overview.completeOnJob', 'plans.overview.completeOnMerge', 'plans.detail.completeOn', 'plans.detail.completeOnJob', 'plans.detail.completeOnMerge']) {
      expect(plansDict.zh[key]).toBeTruthy()
      expect(plansDict.en[key]).toBeTruthy()
    }
  })
})
