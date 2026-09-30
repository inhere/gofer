import { describe, expect, it } from 'vitest'
import {
  groupACPRounds,
  reduceACPEvents,
  visibleRoundIDs,
  type ACPEvent,
} from './acpEvents'

describe('acpEvents', () => {
  it('merges tool updates by id in the first position', () => {
    const events: ACPEvent[] = [
      { seq: 1, kind: 'tool', tool_call_id: 'tc-1', title: 'Edit', status: 'pending', raw_input: '{"path":"a.go"}' },
      { seq: 2, kind: 'message', text: 'between' },
      { seq: 3, kind: 'tool', tool_call_id: 'tc-1', status: 'completed', locations: [{ path: 'a.go', line: 7 }] },
    ]

    expect(reduceACPEvents(events)).toEqual([
      {
        seq: 3,
        kind: 'tool',
        tool_call_id: 'tc-1',
        title: 'Edit',
        status: 'completed',
        raw_input: '{"path":"a.go"}',
        locations: [{ path: 'a.go', line: 7 }],
      },
      { seq: 2, kind: 'message', text: 'between' },
    ])
  })

  it('merges adjacent thoughts and preserves truncation', () => {
    expect(reduceACPEvents([
      { seq: 1, kind: 'thought', text: 'old ' },
      { seq: 2, kind: 'thought', text: 'tokens', truncated: true },
    ])).toEqual([
      { seq: 2, kind: 'thought', text: 'old tokens', truncated: true },
    ])
  })

  it('coalesces consecutive truncated gaps', () => {
    expect(reduceACPEvents([
      { seq: 1, kind: 'truncated', skipped: 4 },
      { seq: 2, kind: 'truncated', skipped: 3 },
      { seq: 3, kind: 'message', text: 'latest' },
    ])).toEqual([
      { seq: 2, kind: 'truncated', skipped: 7 },
      { seq: 3, kind: 'message', text: 'latest' },
    ])
  })

  it('groups rounds in thread job order and defaults to the latest three', () => {
    const byJob: Record<string, ACPEvent[]> = {
      'job-1': [{ seq: 1, kind: 'prompt', text: 'one' }],
      'job-2': [{ seq: 1, kind: 'prompt', text: 'two' }],
      'job-3': [{ seq: 1, kind: 'prompt', text: 'three' }],
      'job-4': [{ seq: 1, kind: 'prompt', text: 'four' }],
      ghost: [{ seq: 1, kind: 'prompt', text: 'ignore' }],
    }
    const ids = ['job-1', 'job-2', 'job-3', 'job-4']

    expect(groupACPRounds(ids, byJob).map((round) => round.jobId)).toEqual(ids)
    expect(visibleRoundIDs(ids)).toEqual(['job-2', 'job-3', 'job-4'])
    expect(visibleRoundIDs(ids, 6)).toEqual(ids)
  })

  it('TestWorkbenchContinuousACPProjectsTextOnly', () => {
    const events: ACPEvent[] = [
      { seq: 1, kind: 'prompt', text: '第一轮内容' },
      { seq: 2, kind: 'thought', text: '内部思考' },
      { seq: 3, kind: 'tool', tool_call_id: 'tool-1', title: 'Read file', status: 'completed' },
      { seq: 4, kind: 'permission', outcome: 'selected' },
      { seq: 5, kind: 'plan', entries: [{ content: '内部计划' }] },
      { seq: 6, kind: 'usage', total_tokens: 20 },
      { seq: 7, kind: 'message', text: '给用户的回复' },
      { seq: 8, kind: 'stop', stop_reason: 'end_turn' },
    ]
    expect(groupACPRounds(['same-job'], { 'same-job': events })[0].events).toEqual([
      { seq: 1, kind: 'prompt', text: '第一轮内容' },
      { seq: 7, kind: 'message', text: '给用户的回复' },
    ])
  })
})
