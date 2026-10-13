import { describe, expect, it } from 'vitest'
import { deliveryLink, deliveryModeLabel, deliveryStateLabel, deliveryText } from './delivery'

describe('delivery (GIT-02)', () => {
  it('describes the outcome', () => {
    expect(deliveryText({ mode: 'pr', state: 'pr_opened', branch: 'agent/x', base: 'main', at: 1 })).toBe('已开 PR agent/x → main')
    expect(deliveryText({ mode: 'push', state: 'failed', error: 'rejected', at: 1 })).toBe('交付失败 ：rejected')
    expect(deliveryText(null)).toBe('')
    expect(deliveryModeLabel('pr')).toBe('推送并开 PR')
    expect(deliveryStateLabel('failed')).toBe('交付失败')
    expect(deliveryStateLabel('weird')).toBe('weird')
    expect(deliveryStateLabel(undefined)).toBe('')
  })
  it('links the request or the compare page', () => {
    expect(deliveryLink({ mode: 'pr', state: 'pr_opened', pr_url: 'u', pr_number: 7, at: 1 })).toEqual({ href: 'u', label: 'PR #7' })
    expect(deliveryLink({ mode: 'pr', state: 'pushed', compare_url: 'c', at: 1 })).toEqual({ href: 'c', label: '去开 PR' })
    expect(deliveryLink({ mode: 'merge', state: 'merged', at: 1 })).toBeNull()
  })
})
