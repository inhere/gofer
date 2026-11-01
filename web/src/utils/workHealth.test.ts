import { describe, expect, it } from 'vitest'
import { healthText } from './workHealth'

describe('healthText', () => {
  it('shows nothing for a healthy or unknown item', () => {
    expect(healthText('ok')).toBe('')
    expect(healthText(undefined)).toBe('')
    expect(healthText('no-such-health')).toBe('')
  })

  it('names the three unhealthy states', () => {
    expect(healthText('blocked')).toBe('阻塞')
    expect(healthText('stalled')).toBe('停滞')
    expect(healthText('at_risk')).toBe('有风险')
  })
})
