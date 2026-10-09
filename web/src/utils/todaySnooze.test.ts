import { describe, expect, it } from 'vitest'
import type { TodayCard } from '../api/today'
import { tomorrowNine } from './today'
import { snoozeDoneLabel, snoozeHints, snoozeOptions, snoozedUntilText, wokeText } from './todaySnooze'

const NOW = new Date(2026, 9, 9, 14, 30, 0)
const NOW_SEC = Math.floor(NOW.getTime() / 1000)

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'work:w1',
    kind: 'work',
    tag: '等我',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: 't',
    waiting_since: 0,
    activity_at: 0,
    summary: '',
    refs: { work_item_id: 'w1' },
    actions: [],
    advice: null,
    ...over,
  }
}

describe('snooze menu', () => {
  it('offers 1 小时 / 明早 9:00, plus 等相关 job 结束 when the card has a job', () => {
    const opts = snoozeOptions(card(), NOW)
    expect(opts.map((o) => o.label)).toEqual(['1 小时', '明早 9:00'])
    expect(opts[0].until_at).toBe(NOW_SEC + 3600)
    expect(opts[1].until_at).toBe(tomorrowNine(NOW))
    expect(new Date(opts[1].until_at! * 1000).getHours()).toBe(9)

    const withJob = snoozeOptions(card({ refs: { job_id: 'j7', interaction_id: 'i1' } }), NOW)
    expect(withJob.map((o) => o.id)).toEqual(['hour', 'morning', 'job'])
    expect(withJob[2]).toEqual({ id: 'job', label: '等相关 job 结束', until_job_id: 'j7' })
    expect(snoozeDoneLabel(withJob[2])).toBe('已稍后 · 等 job 结束')
    expect(snoozeDoneLabel(withJob[0])).toBe('已稍后 · 1 小时')
  })

  it('hints wake-on-activity, and the timeout that still applies to expiring cards', () => {
    expect(snoozeHints(card(), NOW_SEC)).toEqual([{ text: '有新动静会提前回来' }])
    const hot = snoozeHints(card({ expires_at: NOW_SEC + 12 * 60 }), NOW_SEC)
    expect(hot[1]).toEqual({ text: '12 分钟后仍按原规则兜底', bad: true })
  })

  it('labels a woken card by why it came back', () => {
    expect(wokeText(card())).toBe('')
    expect(wokeText(card({ woke: true, woke_reason: 'activity' }))).toBe('有新动静')
    expect(wokeText(card({ woke: true, woke_reason: 'time' }))).toBe('稍后到点')
    expect(wokeText(card({ woke: true, woke_reason: 'job' }))).toBe('相关 job 已结束')
  })

  it('describes a snoozed row', () => {
    const fmt = (s: number) => `@${s}`
    expect(snoozedUntilText({ card_key: 'k', kind: 'work', title: 't', until_at: 5, created_at: 1 }, fmt)).toBe('到 @5')
    expect(snoozedUntilText({ card_key: 'k', kind: 'work', title: 't', until_job_id: 'j1', created_at: 1 }, fmt)).toBe('等 job j1 结束')
  })
})
