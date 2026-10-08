import { describe, expect, it } from 'vitest'
import { sessionUsageRows, tokenLine, workUsageLabel } from './sessionUsage'

describe('sessionUsage', () => {
  it('tokenLine shows tokens only and omits cost when the source had none', () => {
    expect(tokenLine({ input_tokens: 1200, output_tokens: 300, cache_read_tokens: 2000, total_tokens: 3500 })).toBe(
      'in 1.2k / out 300 / cache 2k / total 3.5k',
    )
    expect(tokenLine({ total_tokens: 10, cost_usd: 0 })).toBe('total 10')
    expect(tokenLine({ total_tokens: 10, cost_usd: 0.5 })).toBe('total 10 / $0.5000')
    expect(tokenLine(null)).toBe('')
    expect(tokenLine({})).toBe('')
  })

  it('sessionUsageRows lists main and sub, skipping empty ones', () => {
    const u = { main: { total_tokens: 100 }, sub: { total_tokens: 0 }, total: { total_tokens: 100 } }
    expect(sessionUsageRows(u)).toEqual([{ label: '主会话', text: 'total 100' }])
    expect(sessionUsageRows({ ...u, sub: { input_tokens: 5, total_tokens: 5 } }).map((r) => r.label)).toEqual([
      '主会话',
      '子 agent',
    ])
    expect(sessionUsageRows(undefined)).toEqual([])
  })

  it('workUsageLabel is empty without usage', () => {
    expect(workUsageLabel(undefined)).toBe('')
    expect(workUsageLabel({ total_tokens: 2500 })).toBe('会话用量 total 2.5k')
  })
})
