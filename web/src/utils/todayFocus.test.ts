import { describe, expect, it } from 'vitest'
import type { TodayCard } from '../api/today'
import { approveAction, focusActions, focusCommand, focusProgress, replyAction, swipeCommand } from './todayFocus'

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'review:j1',
    kind: 'review',
    tag: '待验收',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: 't',
    waiting_since: 0,
    activity_at: 0,
    summary: '',
    refs: { job_id: 'j1' },
    actions: [
      { id: 'accept', label: '通过', style: 'ok' },
      { id: 'rerun', label: '附意见重跑' },
      { id: 'diff', label: '看 diff', style: 'link' },
    ],
    advice: null,
    ...over,
  }
}

describe('focusCommand', () => {
  it('maps the focus keys', () => {
    expect(focusCommand('j')).toEqual({ kind: 'next' })
    expect(focusCommand('k')).toEqual({ kind: 'prev' })
    expect(focusCommand('1')).toEqual({ kind: 'action', index: 0 })
    expect(focusCommand('9')).toEqual({ kind: 'action', index: 8 })
    expect(focusCommand('0')).toBeNull()
    expect(focusCommand('a')).toEqual({ kind: 'approve' })
    expect(focusCommand('r')).toEqual({ kind: 'reply' })
    expect(focusCommand('i')).toEqual({ kind: 'info' })
    expect(focusCommand('h')).toEqual({ kind: 'snooze' })
    expect(focusCommand('s')).toEqual({ kind: 'skip' })
    expect(focusCommand('Escape')).toEqual({ kind: 'exit' })
    // z (undo) is the global listener's; unknown keys do nothing.
    expect(focusCommand('z')).toBeNull()
    expect(focusCommand('x')).toBeNull()
  })

  it('never fires while typing or with a modifier', () => {
    for (const k of ['j', 'k', '1', 'a', 'r', 'i', 'h', 's', 'Escape']) {
      expect(focusCommand(k, { typing: true })).toBeNull()
      expect(focusCommand(k, { modifier: true })).toBeNull()
    }
  })
})

describe('focus actions', () => {
  it('numbers the primary actions in order, advice first, navigation links left out', () => {
    expect(focusActions(card()).map((a) => a.id)).toEqual(['accept', 'rerun'])
    const adv = card({ advice: { text: 'ok', action_id: 'rerun' } })
    expect(focusActions(adv).map((a) => a.id)).toEqual(['rerun', 'accept'])
  })

  it('a = the ok-styled action, r = the text action', () => {
    expect(approveAction(card())?.id).toBe('accept')
    expect(replyAction(card())).toBeNull()
    const work = card({
      kind: 'work',
      actions: [
        { id: 'reply', label: '回复', style: 'primary', needs_text: true },
        { id: 'report', label: '请求汇报' },
      ],
    })
    expect(approveAction(work)).toBeNull()
    expect(replyAction(work)?.id).toBe('reply')
  })
})

describe('swipe and progress', () => {
  it('swipes left for next, right for previous, ignores short or vertical moves', () => {
    expect(swipeCommand(-80, 10)).toEqual({ kind: 'next' })
    expect(swipeCommand(80, -10)).toEqual({ kind: 'prev' })
    expect(swipeCommand(-30, 0)).toBeNull()
    expect(swipeCommand(-80, 120)).toBeNull()
  })

  it('counts the cards that left the queue as progress', () => {
    expect(focusProgress(11, 11, 0)).toEqual({ pos: 1, total: 11, percent: 0 })
    // two handled (or skipped / snoozed), looking at the first remaining one
    expect(focusProgress(11, 9, 0)).toEqual({ pos: 3, total: 11, percent: 18 })
    expect(focusProgress(11, 9, 20)).toEqual({ pos: 11, total: 11, percent: 18 })
    expect(focusProgress(11, 0, 0)).toEqual({ pos: 11, total: 11, percent: 100 })
  })
})
