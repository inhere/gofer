import { describe, expect, it } from 'vitest'
import { agentStateDim, agentStateLabel, agentStateTone } from './sessionState'

describe('agent session state presentation', () => {
  it('labels every state in Chinese and falls back to the raw value', () => {
    expect(agentStateLabel('waiting_reply')).toBe('等待回复')
    expect(agentStateLabel('offline')).toBe('离线')
    expect(agentStateLabel('weird')).toBe('weird')
    expect(agentStateLabel(undefined)).toBe('—')
  })
  it('maps states to a card tone and dims only the dead ones', () => {
    expect(agentStateTone('waiting_reply')).toBe('hot')
    expect(agentStateTone('needs_attention')).toBe('fail')
    expect(agentStateTone('running')).toBe('live')
    expect(agentStateTone('idle')).toBe('idle')
    expect(agentStateDim('ended')).toBe(true)
    expect(agentStateDim('offline')).toBe(true)
    expect(agentStateDim('running')).toBe(false)
  })
})
