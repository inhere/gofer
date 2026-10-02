import { describe, expect, it } from 'vitest'
import { peerMessagingLabel, sessionDisplayName, shortAgentSessionId, shouldShowLastMessage, upsertSessionMessage } from './sessionMessaging'

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
})
