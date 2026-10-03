import { describe, expect, it } from 'vitest'
import type { AgentInfo } from '../api/types'
import { attachQuery, resumeChoices, resumePromptNeed } from './resumeChoice'

const agents: AgentInfo[] = [
  { key: 'claude', type: 'cli-agent', available: true, session_resume: true, session_resume_interactive: true, acp_load_session: false, session_family: 'claude' },
  { key: 'tty-claude', type: 'cli-agent', available: true, session_resume: false, session_resume_interactive: true, acp_load_session: false, session_family: 'claude' },
  { key: 'claude-acp', type: 'acp-agent', available: true, session_resume: false, session_resume_interactive: false, acp_load_session: true, session_family: 'claude' },
  { key: 'codex-acp', type: 'acp-agent', available: true, session_resume: false, session_resume_interactive: false, acp_load_session: true },
  { key: 'codex', type: 'cli-agent', available: true, session_resume: true, session_resume_interactive: true, acp_load_session: false, session_family: 'codex' },
]
const by = (list: ReturnType<typeof resumeChoices>, v: string) => list.find((c) => c.value === v)!

describe('resumeChoices', () => {
  it('a cli batch job can become a resident ACP session on its family ACP agent', () => {
    const l = resumeChoices({ agent: 'claude', interactive: false }, agents)
    expect(by(l, 'session')).toMatchObject({ disabled: false, agent: 'claude-acp' })
    expect(by(l, 'interactive')).toMatchObject({ disabled: false, agent: '' })
    expect(by(l, 'batch')).toMatchObject({ disabled: false, agent: '' })
  })

  it('an ACP session can only go to a CLI through its family (claude), never codex-acp', () => {
    const claudeAcp = resumeChoices({ agent: 'claude-acp', interactive: false }, agents)
    expect(by(claudeAcp, 'session')).toMatchObject({ disabled: false, agent: '' })
    expect(by(claudeAcp, 'interactive')).toMatchObject({ disabled: false, agent: 'claude' })
    expect(by(claudeAcp, 'batch')).toMatchObject({ disabled: false, agent: 'claude' })
    const codexAcp = resumeChoices({ agent: 'codex-acp', interactive: false }, agents)
    expect(by(codexAcp, 'interactive').disabled).toBe(true)
    expect(by(codexAcp, 'interactive').note).toContain('同族')
    expect(by(codexAcp, 'batch').disabled).toBe(true)
  })

  it('greys out session / interactive with the reason from the environment', () => {
    const l = resumeChoices({ agent: 'claude-acp', interactive: false }, agents, { runnerSessionOk: false, projectAllowsInteractive: false })
    expect(by(l, 'session')).toMatchObject({ disabled: true })
    expect(by(l, 'session').note).toContain('v13')
    expect(by(l, 'interactive')).toMatchObject({ disabled: true })
    expect(by(l, 'interactive').note).toContain('allow_interactive')
    expect(by(l, '').disabled).toBe(false)
  })

  it('an old server without capability flags only offers the default form', () => {
    const old: AgentInfo[] = [{ key: 'claude', type: 'cli-agent', available: true }]
    const l = resumeChoices({ agent: 'claude', interactive: false }, old)
    expect(by(l, '').disabled).toBe(false)
    expect(by(l, 'session').disabled).toBe(true)
    expect(by(l, 'batch').disabled).toBe(true)
  })
})

describe('resumePromptNeed / attachQuery', () => {
  it('prompt rules per mode', () => {
    expect(resumePromptNeed('interactive', false, false)).toBe('none')
    expect(resumePromptNeed('batch', true, true)).toBe('required')
    expect(resumePromptNeed('session', false, false)).toBe('optional')
    expect(resumePromptNeed('', true, false)).toBe('none')
    expect(resumePromptNeed('', false, true)).toBe('optional')
    expect(resumePromptNeed('', false, false)).toBe('required')
  })

  it('attach follows the NEW job, not the source job', () => {
    expect(attachQuery({ interactive: true })).toBe('?attach=1')
    expect(attachQuery({ interactive: false })).toBe('')
    expect(attachQuery({})).toBe('')
  })
})
