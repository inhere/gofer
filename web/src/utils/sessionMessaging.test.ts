import { describe, expect, it } from 'vitest'
import { mergeSessionTimeline, peerMessagingLabel, sessionDisplayName, shortAgentSessionId, shouldShowLastMessage, upsertSessionMessage } from './sessionMessaging'
import type { Decision, SessionMessage } from '../api/types'

describe('session messaging display', () => {
  it('shows peer name and a copy-friendly short id', () => {
    expect(sessionDisplayName({ peer_name: 'inspect-22', title: 'old', agent: 'claude', session_id: 'abcdef1234567890' })).toBe('inspect-22')
    expect(shortAgentSessionId('abcdef1234567890')).toBe('abcdef12…7890')
  })

  it('shows pending until the hook reports peer identity', () => {
    expect(peerMessagingLabel({ peer_messaging: false })).toBe('待上报')
    expect(peerMessagingLabel({ peer_status: 'idle', peer_messaging: false })).toBe('不可接收消息')
    expect(peerMessagingLabel({ peer_name: 'inspect-22', peer_messaging: true })).toBe('可接收消息')
  })

  it('keeps message order while applying queued to delivered/failed transitions', () => {
    const queued = { id: 'm1', session_id: 's1', text: 'hello', status: 'queued' as const, created_at: 1, updated_at: 1 }
    const delivered = { ...queued, status: 'delivered' as const, channel: 'messenger', updated_at: 2 }
    const failed = { id: 'm2', session_id: 's1', text: 'later', status: 'failed' as const, error: 'offline', created_at: 3, updated_at: 3 }
    expect(upsertSessionMessage(upsertSessionMessage([queued], delivered), failed).map((m) => m.status)).toEqual(['delivered', 'failed'])
  })

  it('hides a duplicated last message only while the matching relay turn waits', () => {
    expect(shouldShowLastMessage('  hello\nworld ', 'hello world', true)).toBe(false)
    expect(shouldShowLastMessage('hello', 'hello', false)).toBe(true)
    expect(shouldShowLastMessage('hello', 'different', true)).toBe(true)
  })

  it('interleaves relay turns and delivered web messages by creation time', () => {
    const turn = { id: 't1', title: '', question: 'agent', state: 'ANSWERED', timeout_sec: 90, asked_at: 30 } as Decision
    const message = { id: 'm1', session_id: 's1', text: 'web', status: 'delivered', created_at: 20, updated_at: 20 } as SessionMessage
    expect(mergeSessionTimeline([turn], [message]).map((entry) => entry.kind)).toEqual(['message', 'turn'])
  })

  it('preserves the supplied order for equal timestamps', () => {
    const turns = [
      { id: 'turn-new', title: 'new', question: 'new', state: 'ANSWERED', timeout_sec: 1, asked_at: 100 },
      { id: 'turn-old', title: 'old', question: 'old', state: 'ANSWERED', timeout_sec: 1, asked_at: 100 },
    ] as Decision[]
    expect(mergeSessionTimeline(turns, []).map((entry) => entry.kind === 'turn' ? entry.turn.id : '')).toEqual(['turn-new', 'turn-old'])
  })
})

describe('shouldShowLastMessage after the turn was answered', () => {
  it('still hides a last message identical to the latest relay turn', () => {
    // 调用方传入的是最近一轮（不论已答与否）；与其内容相同即重复。
    expect(shouldShowLastMessage('同一段回复\n', '同一段回复', true)).toBe(false)
    expect(shouldShowLastMessage('放行时的另一段回复', '上一轮问题', true)).toBe(true)
  })
})
