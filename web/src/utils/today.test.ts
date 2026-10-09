import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { TodayCard } from '../api/today'

const calls: string[] = []
vi.mock('../api/client', () => {
  const rec = (name: string) => (...args: unknown[]) => {
    calls.push(`${name}(${args.map((a) => JSON.stringify(a)).join(',')})`)
    return Promise.resolve({})
  }
  return {
    ackSessionTurn: rec('ackSessionTurn'),
    acceptJob: rec('acceptJob'),
    acceptWorkSuggestion: rec('acceptWorkSuggestion'),
    addWorkNote: rec('addWorkNote'),
    answerDecision: rec('answerDecision'),
    answerInteraction: rec('answerInteraction'),
    dismissWorkSuggestion: rec('dismissWorkSuggestion'),
    patchWorkItem: rec('patchWorkItem'),
    planResume: rec('planResume'),
    rejectJob: rec('rejectJob'),
    requestWorkReport: rec('requestWorkReport'),
    saySession: rec('saySession'),
    sendSessionMessage: rec('sendSessionMessage'),
  }
})
vi.mock('../api/steward', () => ({
  acceptMergeSuggestion: (id: number) => {
    calls.push(`acceptMergeSuggestion(${id})`)
    return Promise.resolve({})
  },
  dismissMergeSuggestion: (id: number) => {
    calls.push(`dismissMergeSuggestion(${id})`)
    return Promise.resolve({})
  },
}))

const {
  adviceAction,
  blockShort,
  cardLink,
  createChordDetector,
  doneLabel,
  groupByTier,
  isTypingTarget,
  runCardAction,
  sendImmediately,
  settleHidden,
  tierOf,
} = await import('./today')

function card(over: Partial<TodayCard>): TodayCard {
  return {
    key: 'k',
    kind: 'interaction',
    tag: '提问',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: 't',
    waiting_since: 0,
    activity_at: 0,
    summary: '',
    refs: {},
    actions: [],
    advice: null,
    ...over,
  }
}

describe('today helpers', () => {
  beforeEach(() => {
    calls.length = 0
  })

  it('groups into 会超时 / 卡住别人 / 可稍后 and labels 「卡住 N」', () => {
    const cards = [
      card({ key: 'a', urgency: 'now' }),
      card({ key: 'b', urgency: 'blocking', blocks: { score: 4, items: 2 } }),
      card({ key: 'c', urgency: 'blocking', blocks: { score: 2, items: 1 } }),
      card({ key: 'd' }),
    ]
    expect(cards.map(tierOf)).toEqual(['now', 'block', 'block', 'normal'])
    expect(groupByTier(cards).map((g) => [g.label, g.cards.length])).toEqual([
      ['会超时', 1],
      ['卡住别人', 2],
      ['可稍后', 1],
    ])
    expect(blockShort(cards[1])).toBe('卡住 2')
    expect(blockShort(cards[3])).toBe('')
  })

  it('links cards to the page that owns them', () => {
    expect(cardLink(card({ kind: 'relay', refs: { thread_id: 'r:s1' } }))).toBe('/workbench?thread=r%3As1')
    expect(cardLink(card({ kind: 'interaction', refs: { job_id: 'j1' } }))).toBe('/jobs/j1')
    expect(cardLink(card({ kind: 'review', refs: { job_id: 'j2', thread_id: 's:x' } }))).toBe('/jobs/j2')
    expect(cardLink(card({ kind: 'work', refs: { work_item_id: 'w1' } }))).toBe('/work?id=w1')
    expect(cardLink(card({ kind: 'merge', refs: { work_item_id: 'w2', merge_id: 3 } }))).toBe('/work?id=w2')
    expect(cardLink(card({ kind: 'plan_blocked', refs: { plan_id: 'p1' } }))).toBe('/plans/p1')
  })

  it('finds the advice action by id or answer value', () => {
    const c = card({
      actions: [
        { id: 'answer', label: '批准', value: 'allow' },
        { id: 'answer', label: '拒绝', value: 'reject' },
      ],
      advice: { text: 'x', action_id: 'answer:allow' },
    })
    expect(adviceAction(c)?.label).toBe('批准')
    expect(adviceAction({ ...c, advice: { text: 'x' } })).toBeNull()
    expect(doneLabel(c.actions[0], true)).toBe('已按建议批准')
    expect(doneLabel(c.actions[0])).toBe('已批准')
  })

  it('maps every card action onto the existing write API', async () => {
    const run = (c: TodayCard, a: { id: string; label?: string; value?: string }, text = '') =>
      runCardAction(c, { label: '', ...a }, text)()
    await run(card({ kind: 'interaction', refs: { job_id: 'j', interaction_id: 'i' } }), { id: 'answer', value: 'allow' })
    await run(card({ kind: 'decision', refs: { decision_id: 'd' } }), { id: 'reply' }, '选 A')
    await run(card({ kind: 'relay', refs: { session_id: 's', decision_id: 'd' } }), { id: 'reply' }, 'push')
    await run(card({ kind: 'relay', refs: { session_id: 's', decision_id: 'd' } }), { id: 'ack' })
    await run(card({ kind: 'relay', refs: { session_id: 's', decision_id: 'd1', decision_ids: ['d1', 'd2'] } }), { id: 'ack' })
    await run(card({ kind: 'review', refs: { job_id: 'j' } }), { id: 'accept' })
    await run(card({ kind: 'review', refs: { job_id: 'j' } }), { id: 'rerun', value: '1' }, '补测试')
    await run(card({ kind: 'work', refs: { work_item_id: 'w', session_id: 's' } }), { id: 'reply' }, '先做 iframe')
    await run(card({ kind: 'work', refs: { work_item_id: 'w' } }), { id: 'report' })
    await run(card({ kind: 'work', refs: { work_item_id: 'w' } }), { id: 'adopt:goal' })
    await run(card({ kind: 'suggestion', refs: { work_item_id: 'w', field: 'status_hint' } }), { id: 'adopt' })
    await run(card({ kind: 'suggestion', refs: { work_item_id: 'w', field: 'goal' } }), { id: 'dismiss' })
    await run(card({ kind: 'merge', refs: { merge_id: 7 } }), { id: 'adopt' })
    await run(card({ kind: 'plan_blocked', refs: { plan_id: 'p' } }), { id: 'resume' })
    expect(calls).toEqual([
      'answerInteraction("j","i","allow")',
      'answerDecision("d","选 A")',
      'saySession("s","push")',
      'ackSessionTurn("s","d")',
      'ackSessionTurn("s","d1")',
      'ackSessionTurn("s","d2")',
      'acceptJob("j")',
      'rejectJob("j","补测试",true)',
      'addWorkNote("w","先做 iframe")',
      'sendSessionMessage("s","先做 iframe")',
      'requestWorkReport("w")',
      'acceptWorkSuggestion("w","goal")',
      'acceptWorkSuggestion("w","status_hint")',
      'dismissWorkSuggestion("w","goal")',
      'acceptMergeSuggestion(7)',
      'planResume("p")',
    ])
    calls.length = 0
    await run(card({ kind: 'work', refs: { work_item_id: 'w' } }), { id: 'park' })
    expect(calls[0]).toMatch(/^patchWorkItem\("w",\{"status":"parked","park_until":\d+\}\)$/)
  })

  it('no longer offers 交给管家 (punt only marked the interaction needs_human)', () => {
    expect(doneLabel({ id: 'ack', label: '已读' })).toBe('已标已读')
    calls.length = 0
    void runCardAction(card({ kind: 'interaction', refs: { job_id: 'j', interaction_id: 'i' } }), { id: 'answer', label: '', value: 'x' })()
    expect(calls.some((c) => c.startsWith('punt'))).toBe(false)
  })

  it('settles hidden cards: in-flight stay hidden, committed reappear on a later refresh', () => {
    const committed = new Map<string, number>()
    // a: in the undo window; b: write in flight; c: committed at seq 3; d: committed at seq 5.
    committed.set('c', 3)
    committed.set('d', 5)
    const hidden = new Set(['a', 'b', 'c', 'd', 'gone'])
    const next = settleHidden(hidden, committed, new Set(['a', 'b', 'c', 'd']), 'a', 5)
    // c's write finished before refresh #5 started: shown again although the server still has it.
    // d's write finished after refresh #5 started: that response may predate it, keep hiding.
    expect([...next].sort()).toEqual(['a', 'b', 'd'])
    expect([...committed.keys()]).toEqual(['d'])
    // The next refresh (#6) settles d too; the server dropped it, so it just goes away.
    expect([...settleHidden(next, committed, new Set(['a', 'b']), 'a', 6)].sort()).toEqual(['a', 'b'])
    expect(committed.size).toBe(0)
  })

  it('sends immediately when the card times out within 30s', () => {
    expect(sendImmediately(card({ expires_at: 1020 }), 1000)).toBe(true)
    expect(sendImmediately(card({ expires_at: 1100 }), 1000)).toBe(false)
    expect(sendImmediately(card({}), 1000)).toBe(false)
  })
})

describe('g d shortcut', () => {
  it('opens on g then d within 800ms, never while typing', () => {
    let t = 0
    const chord = createChordDetector(800, () => t)
    expect(chord('g', false)).toBe(false)
    t = 300
    expect(chord('d', false)).toBe(true)
    // d alone does nothing
    expect(chord('d', false)).toBe(false)
    chord('g', false)
    t = 2000
    expect(chord('d', false)).toBe(false)
    chord('g', true)
    expect(chord('d', true)).toBe(false)
  })

  it('treats inputs, textareas, selects and contenteditable as typing', () => {
    expect(isTypingTarget({ tagName: 'INPUT' } as unknown as EventTarget)).toBe(true)
    expect(isTypingTarget({ tagName: 'textarea' } as unknown as EventTarget)).toBe(true)
    expect(isTypingTarget({ tagName: 'DIV', isContentEditable: true } as unknown as EventTarget)).toBe(true)
    expect(isTypingTarget({ tagName: 'BUTTON' } as unknown as EventTarget)).toBe(false)
    expect(isTypingTarget(null)).toBe(false)
  })
})
