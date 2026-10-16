import { describe, expect, it } from 'vitest'
import { leaderRows, warningsSuffix } from './configLeader'

const config = Object.values(import.meta.glob('../views/Config.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('config leader block', () => {
  it('returns null when the leader section is absent', () => {
    expect(leaderRows(undefined)).toBeNull()
    expect(leaderRows(null)).toBeNull()
  })

  it('lists the resolved fields', () => {
    const rows = leaderRows({
      enabled: true,
      agent: 'claude',
      scopes: ['plan'],
      max_rounds_per_scope: 6,
      wake_delay_sec: 30,
      on_member_done: true,
    })
    expect(rows).toEqual([
      ['enabled', 'enabled'],
      ['agent', 'claude'],
      ['scopes', 'plan'],
      ['max_rounds_per_scope', '6'],
      ['wake_delay_sec', '30s'],
      ['on_member_done', 'on'],
    ])
  })

  it('formats warnings only when present', () => {
    expect(warningsSuffix(undefined)).toBe('')
    expect(warningsSuffix(['', '  '])).toBe('')
    expect(warningsSuffix(['a', 'b'])).toBe('；警告：a；b')
  })
})

describe('Config.vue wiring', () => {
  it('shows the top-level leader block and the config warnings', () => {
    expect(config).toContain('leaderRows(config.leader)')
    expect(config).toContain('config.warnings')
    expect(config).not.toContain('config.supervisor')
    expect(config).not.toContain('config.presence')
  })
})
