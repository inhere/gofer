import { describe, expect, it } from 'vitest'
import type { AgentInfo } from '../api/types'
import { fromSessionChoice, fromSessionFormError, fromSessionQuery, readFromSessionQuery, withFromSession } from './fromSession'

const agents = [
  { key: 'suag', type: 'cli-agent', available: true, from_session: true, session_family: 'suag' },
  { key: 'claude', type: 'cli-agent', available: true, from_session: false, session_family: 'claude' },
  { key: 'claude-acp', type: 'acp-agent', available: true, from_session: false, session_family: 'claude' },
  { key: 'claude-heir', type: 'cli-agent', available: true, from_session: true, session_family: 'claude' },
  { key: 'codex', type: 'cli-agent', available: true, from_session: false, session_family: 'codex' },
  { key: 'old', type: 'cli-agent', available: true },
] as AgentInfo[]

describe('fromSessionChoice', () => {
  it('hides the entry without a session id', () => {
    expect(fromSessionChoice({ agent: 'suag' }, agents).visible).toBe(false)
  })
  it('uses the source agent when it has from_session_args', () => {
    expect(fromSessionChoice({ agent: 'suag', sessionId: 's1' }, agents)).toEqual({ visible: true, disabled: false, note: '', agent: 'suag' })
  })
  it('switches to a capable agent of the same session family', () => {
    const c = fromSessionChoice({ agent: 'claude-acp', sessionId: 's1' }, agents)
    expect(c.disabled).toBe(false)
    expect(c.agent).toBe('claude-heir')
    expect(c.note).toContain('claude-heir')
  })
  it('disables with a reason when nothing in the family can', () => {
    const c = fromSessionChoice({ agent: 'codex', sessionId: 's1' }, agents)
    expect(c.visible).toBe(true)
    expect(c.disabled).toBe(true)
    expect(c.note).toContain('from_session_args')
  })
  it('treats a missing capability field (old server) as available', () => {
    expect(fromSessionChoice({ agent: 'old', sessionId: 's1' }, agents).disabled).toBe(false)
  })
  it('does not pre-judge before agents are loaded', () => {
    expect(fromSessionChoice({ agent: 'codex', sessionId: 's1' }, [])).toMatchObject({ visible: true, disabled: false, agent: 'codex' })
  })
  it('names an unconfigured source agent in the reason', () => {
    const c = fromSessionChoice({ agent: 'gone', sessionId: 's1' }, agents)
    expect(c.disabled).toBe(true)
    expect(c.note).toContain('gone')
  })
})

describe('fromSessionQuery', () => {
  it('carries agent / project / runner / relative cwd and the session id', () => {
    const src = { agent: 'claude-acp', sessionId: 's-1', project: 'p', runner: 'w-a', cwd: 'sub/dir' }
    expect(fromSessionQuery(src, fromSessionChoice(src, agents))).toEqual({
      from_session: 's-1', agent: 'claude-heir', project: 'p', runner: 'w-a', cwd: 'sub/dir',
    })
  })
  it('drops absolute cwd (registered sessions record host paths)', () => {
    for (const cwd of ['/abs/x', 'D:\\work', '~/x', '']) {
      const src = { agent: 'suag', sessionId: 's', cwd }
      expect(fromSessionQuery(src, fromSessionChoice(src, agents)).cwd).toBeUndefined()
    }
  })
})

describe('new-job form', () => {
  it('reads the query defensively', () => {
    expect(readFromSessionQuery(' s-1 ')).toBe('s-1')
    expect(readFromSessionQuery(['a'])).toBe('')
    expect(readFromSessionQuery('x'.repeat(201))).toBe('')
  })
  it('adds from_session to the payload only for a cli-agent', () => {
    expect(withFromSession({ agent: 'suag' } as { agent: string; from_session?: string }, 's-1', true)).toEqual({ agent: 'suag', from_session: 's-1' })
    expect(withFromSession({ agent: 'x' } as { agent: string; from_session?: string }, 's-1', false).from_session).toBeUndefined()
    expect(withFromSession({ agent: 'x' } as { agent: string; from_session?: string }, '', true).from_session).toBeUndefined()
  })
  it('explains why the form cannot submit', () => {
    expect(fromSessionFormError('', { agentType: 'exec', continuousSession: true })).toBe('')
    expect(fromSessionFormError('s', { agentType: 'acp-agent', continuousSession: true })).toContain('持续')
    expect(fromSessionFormError('s', { agentType: 'exec', continuousSession: false })).toContain('cli-agent')
    expect(fromSessionFormError('s', { agentType: 'cli-agent', agentFromSession: false, continuousSession: false })).toContain('from_session_args')
    expect(fromSessionFormError('s', { agentType: 'cli-agent', continuousSession: false })).toBe('')
  })
})
