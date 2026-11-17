import { describe, expect, it } from 'vitest'
import {
  acpEventEntries,
  conversationEntryKey,
  interactionEntries,
  isTurnUnsent,
  jobMessageEntries,
  turnUnsentEntry,
  type ConversationInjectedEntry,
  type ConversationMessageEntry,
  type ConversationToolsEntry,
} from './conversationTimeline'
import type { ACPEvent } from '../components/workbench/acpEvents'
import type { Interaction, JobMessage } from '../api/types'

const labels = { user: '你', agent: '助手' }

// R12：三处对话显示（工作台 / job 消息 / 会话抽屉）都靠这里把各自的数据规整成条目。
describe('acpEventEntries', () => {
  function textEvent(seq: number, kind: 'prompt' | 'message', text: string, injected?: string): ACPEvent {
    return { seq, kind, text, ...(injected ? { injected } : {}) } as ACPEvent
  }

  it('splits the injected rules into its own collapsed entry after the user message', () => {
    const entries = acpEventEntries(
      [textEvent(0, 'prompt', '修一下这个 bug', 'gofer 规则：...'), textEvent(1, 'message', '好的')],
      labels,
    )
    expect(entries).toHaveLength(3)
    const [user, injected, agent] = entries as [ConversationMessageEntry, ConversationInjectedEntry, ConversationMessageEntry]
    expect(user.kind).toBe('user')
    expect(user.text).toBe('修一下这个 bug')
    expect(user.label).toBe('你')
    expect(injected.kind).toBe('injected')
    expect(injected.text).toBe('gofer 规则：...')
    expect(agent.kind).toBe('agent')
    expect(agent.text).toBe('好的')
    expect(agent.label).toBe('助手')
    // 注入不再混在正文里（此前工作台整屏都是规则）。
    expect(JSON.stringify(user)).not.toContain('gofer 规则')
  })

  it('merges the updates of one tool call and groups consecutive calls', () => {
    const entries = acpEventEntries([
      textEvent(0, 'prompt', '跑测试'),
      { seq: 1, kind: 'tool', tool_call_id: 't1', title: 'bash', status: 'pending', raw_input: 'go test ./...' } as ACPEvent,
      { seq: 2, kind: 'tool', tool_call_id: 't1', status: 'failed' } as ACPEvent,
      { seq: 3, kind: 'tool', tool_call_id: 't2', title: 'read', locations: [{ path: 'a.go' }] } as ACPEvent,
      textEvent(4, 'message', '失败了一个'),
    ], labels)
    expect(entries.map((entry) => entry.kind)).toEqual(['user', 'tools', 'agent'])
    const tools = entries[1] as ConversationToolsEntry
    // 同一个 tool_call_id 的更新合并成一条（不是两条），随后连续的调用合成一组。
    expect(tools.calls).toHaveLength(2)
    expect(tools.calls[0]).toMatchObject({ id: 't1', title: 'bash', status: 'failed', summary: 'go test ./...' })
    expect(tools.calls[1]).toMatchObject({ id: 't2', title: 'read', summary: 'a.go' })
    expect(tools.failed).toBe(1)
  })

  it('carries the permission decision as its own entry with the display outcome', () => {
    const entries = acpEventEntries([
      { seq: 0, kind: 'permission', tool_call_id: 't1', title: 'write a.go', outcome: 'reject_once', reason: '不行' } as ACPEvent,
    ], labels)
    expect(entries).toEqual([
      { kind: 'permission', id: 'permission-0', outcome: 'denied', title: 'write a.go', reason: '不行' },
    ])
  })
})

describe('jobMessageEntries', () => {
  it('keeps the server order, the injected block and the cross-round job id', () => {
    const messages: JobMessage[] = [
      { role: 'user', job_id: 'j1', at: 10, text: '第一句', injected: '规则全文' },
      { role: 'agent', job_id: 'j1', at: 11, text: '回复', truncated: true },
      { role: 'user', job_id: 'j0', at: 12, text: '更早', omitted: true },
    ]
    const entries = jobMessageEntries(messages, labels, (at) => `t${at ?? ''}`)
    expect(entries.map((entry) => conversationEntryKey(entry))).toEqual([
      'user:user-j1-0',
      'injected:injected-user-j1-0',
      'agent:agent-j1-1',
      'user:user-j0-2',
    ])
    expect(entries[0]).toMatchObject({ label: '你', time: 't10', jobId: 'j1' })
    expect(entries[1]).toMatchObject({ kind: 'injected', text: '规则全文' })
    expect(entries[2]).toMatchObject({ label: '助手', truncated: true, time: 't11' })
    expect(entries[3]).toMatchObject({ omitted: true, jobId: 'j0' })
  })
})

describe('interactionEntries', () => {
  it('marks the submitting card and passes the read-only flag through', () => {
    const item = { id: 'i1', job_id: 'j1', type: 'choice', prompt: '选哪个', status: 'pending', created_at: 1 } as Interaction
    const [entry] = interactionEntries([item], new Set(['i1']), true)
    expect(entry).toMatchObject({ kind: 'interaction', id: 'i1', submitting: true, readonly: true })
  })
})

describe('turn_unsent', () => {
  it('recognises the record both as an acp kind and as a raw t field', () => {
    expect(isTurnUnsent({ kind: 'turn_unsent', text: 'x', prompt: 'y' })).toBe(true)
    expect(isTurnUnsent({ t: 'turn_unsent', text: 'x', prompt: 'y' })).toBe(true)
    expect(isTurnUnsent({ kind: 'message', text: 'x' })).toBe(false)
    expect(isTurnUnsent(null)).toBe(false)
  })

  it('keeps the original message so the composer can take it back', () => {
    const entry = turnUnsentEntry({ text: '未发送：目录被 j2 占用', prompt: '跑一下测试' }, 'x-1')
    expect(entry).toEqual({
      kind: 'unsent',
      id: 'x-1',
      text: '未发送：目录被 j2 占用',
      prompt: '跑一下测试',
    })
    // 老服务端 / 缺字段时不留 undefined
    expect(turnUnsentEntry({})).toMatchObject({ text: '', prompt: '' })
  })
})
