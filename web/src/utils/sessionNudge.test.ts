import { describe, expect, it } from 'vitest'
import type { SessionNudge } from '../api/types'
import {
  nudgeCanCreate,
  nudgeIntervalLabel,
  nudgeMinutesToSec,
  nudgeRuleLabel,
  nudgeStatusText,
  nudgeUntilToUnix,
} from './sessionNudge'

const base: SessionNudge = {
  id: 'ng-1', session_id: 's', kind: 'every', interval_sec: 1800, text: 'x', state: 'active',
  created_at: 1, fire_count: 0, fail_count: 0,
}

describe('session nudge helpers', () => {
  it('labels intervals and rules', () => {
    expect(nudgeIntervalLabel(600)).toBe('10 分钟')
    expect(nudgeIntervalLabel(7200)).toBe('2 小时')
    expect(nudgeIntervalLabel(5400)).toBe('1 小时 30 分钟')
    expect(nudgeRuleLabel('every', 1800)).toBe('每 30 分钟')
    expect(nudgeRuleLabel('stalled', 1200)).toBe('停滞超过 20 分钟')
  })

  it('validates the minutes input', () => {
    expect(nudgeMinutesToSec('30')).toBe(1800)
    expect(nudgeMinutesToSec(1)).toBe(60)
    expect(nudgeMinutesToSec('0.5')).toBeNull()
    expect(nudgeMinutesToSec('')).toBeNull()
    expect(nudgeMinutesToSec('abc')).toBeNull()
  })

  it('converts the until field', () => {
    expect(nudgeUntilToUnix('')).toBe(0)
    expect(nudgeUntilToUnix('nonsense')).toBeNull()
    const t = nudgeUntilToUnix('2026-10-10T09:30')
    expect(t).toBe(Math.floor(new Date('2026-10-10T09:30').getTime() / 1000))
  })

  it('only live sessions take a new nudge', () => {
    expect(nudgeCanCreate('running')).toBe(true)
    expect(nudgeCanCreate('idle')).toBe(true)
    expect(nudgeCanCreate('ended')).toBe(false)
    expect(nudgeCanCreate('handed_off')).toBe(false)
    expect(nudgeCanCreate(undefined)).toBe(false)
  })

  it('describes the state', () => {
    expect(nudgeStatusText(base)).toBe('运行中')
    expect(nudgeStatusText({ ...base, fail_count: 2 })).toBe('运行中 · 连续失败 2/3')
    expect(nudgeStatusText({ ...base, state: 'paused', pause_reason: '3 consecutive delivery failures' })).toContain('已暂停')
    expect(nudgeStatusText({ ...base, state: 'ended', ended_reason: 'until' })).toBe('已结束 · 已到截止时间')
  })
})
