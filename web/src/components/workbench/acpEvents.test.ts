import { describe, expect, it } from 'vitest'
import {
  acpNoticeText,
  emptyRoundNote,
  groupACPRounds,
  isACPEvent,
  isACPNotice,
  permissionOutcome,
  reduceACPEvents,
  timelineBlocks,
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

  it('TestWorkbenchContinuousACPProjectsTextAndToolCalls', () => {
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
    // 设计稿 §7：时间线保留用户消息、agent 回复、工具调用（默认折叠）与内嵌的审批卡；
    // 思考 / 计划 / 用量 / 结束原因仍留在 job 详情。
    expect(groupACPRounds(['same-job'], { 'same-job': events })[0].events).toEqual([
      { seq: 1, kind: 'prompt', text: '第一轮内容' },
      { seq: 3, kind: 'tool', tool_call_id: 'tool-1', title: 'Read file', status: 'completed' },
      { seq: 4, kind: 'permission', outcome: 'selected' },
      { seq: 7, kind: 'message', text: '给用户的回复' },
    ])
  })

  it('folds consecutive tool calls into one collapsed block', () => {
    const events: ACPEvent[] = [
      { seq: 1, kind: 'prompt', text: 'go' },
      { seq: 2, kind: 'tool', tool_call_id: 'a', title: 'Read', status: 'completed' },
      { seq: 3, kind: 'tool', tool_call_id: 'b', title: 'Bash', status: 'failed' },
      { seq: 4, kind: 'message', text: 'done' },
      { seq: 5, kind: 'tool', tool_call_id: 'c', title: 'Bash', status: 'in_progress' },
    ]
    const blocks = timelineBlocks(events)
    expect(blocks.map((block) => block.kind)).toEqual(['text', 'tools', 'text', 'tools'])
    const tools = blocks[1]
    expect(tools.kind).toBe('tools')
    if (tools.kind === 'tools') {
      expect(tools.events.map((tool) => tool.tool_call_id)).toEqual(['a', 'b'])
      expect(tools.failed).toBe(1)
    }
    const running = blocks[3]
    if (running.kind === 'tools') expect(running.running).toBe(1)
  })

  it('classifies permission decisions for the inline approval card', () => {
    expect(permissionOutcome({ seq: 1, kind: 'permission', outcome: 'selected' })).toBe('allowed')
    expect(permissionOutcome({ seq: 1, kind: 'permission', outcome: 'rejected' })).toBe('denied')
    expect(permissionOutcome({ seq: 1, kind: 'permission', outcome: 'cancelled' })).toBe('allowed')
    expect(permissionOutcome({ seq: 1, kind: 'permission', auto: true, outcome: 'rejected' })).toBe('auto')
  })

  it('recognises the mirror notice and explains it instead of loading forever', () => {
    const old = { seq: 1, kind: 'notice', code: 'acp_mirror_unsupported', text: 'x', worker_id: 'w1', worker_protocol: 21, min_protocol: 22 }
    expect(isACPNotice(old)).toBe(true)
    // A notice is not a conversation record: the round model never sees it.
    expect(isACPEvent(old)).toBe(false)
    expect(acpNoticeText(old as never)).toContain('v21')
    expect(acpNoticeText(old as never)).toContain('v22+')
    expect(acpNoticeText(old as never)).toContain('w1')
    expect(acpNoticeText({ seq: 1, kind: 'notice', code: 'acp_mirror_peer', text: 'x' })).toContain('peer')
    expect(acpNoticeText({ seq: 1, kind: 'notice', code: 'other', text: 'server text' })).toBe('server text')
    expect(isACPNotice({ seq: 1, kind: 'message', text: 'hi' })).toBe(false)
  })

  it('describes an empty round by whether its stream is live', () => {
    expect(emptyRoundNote('awaiting_input', true)).toBe('会话已就绪，等待输入。')
    expect(emptyRoundNote('running', true)).toContain('等待第一条输出')
    expect(emptyRoundNote('done', true)).toBe('暂无结构化记录')
    expect(emptyRoundNote('awaiting_input', false)).toBe('暂无结构化记录')
    expect(emptyRoundNote('', true)).toBe('暂无结构化记录')
  })
})
