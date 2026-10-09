import { describe, expect, it } from 'vitest'
import type { Decision } from '../api/types'
import { denyAnswer, permissionAnswerText, permissionChoices, permissionReleasedText } from './sessionPermission'

const base: Decision = {
  id: 'd1',
  title: 't',
  question: '需要授权：Bash `npm test`',
  state: 'OPEN',
  timeout_sec: 600,
  asked_at: 1,
  session_id: 's1',
  kind: 'permission',
  permission: { tool_name: 'Bash', summary: 'Bash `npm test`', suggestions: [{ label: '规则 Bash(npm test) · 本项目（本地）' }] },
}

describe('session permission prompt', () => {
  it('offers allow / one always-allow per suggestion / deny', () => {
    expect(permissionChoices(base).map((c) => [c.label, c.answer])).toEqual([
      ['允许', 'allow'],
      ['总是允许：规则 Bash(npm test) · 本项目（本地）', 'always:0'],
      ['拒绝', 'deny'],
    ])
    expect(permissionChoices({ ...base, permission: { tool_name: 'Bash', summary: 'x' } }).map((c) => c.answer)).toEqual([
      'allow',
      'deny',
    ])
  })

  it('encodes a deny reason', () => {
    expect(denyAnswer('  不要删 ')).toBe('deny:不要删')
    expect(denyAnswer('')).toBe('deny')
  })

  it('describes how the prompt ended', () => {
    expect(permissionAnswerText({ ...base, state: 'ANSWERED', answer: 'always:0' })).toBe('已总是允许（规则 Bash(npm test) · 本项目（本地））')
    expect(permissionAnswerText({ ...base, state: 'ANSWERED', answer: 'deny:不要删' })).toBe('已拒绝：不要删')
    expect(permissionReleasedText({ ...base, state: 'EXPIRED', released_by: 'terminal' })).toBe('已在终端处理')
    expect(permissionReleasedText({ ...base, state: 'EXPIRED' })).toBe('已过期，交回终端处理')
  })
})

const drawer = Object.values(import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const prompt = Object.values(import.meta.glob('../components/SessionPermissionPrompt.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('session drawer permission prompt', () => {
  it('never lets the reply box answer a permission prompt', () => {
    expect(drawer).toContain("t.state === 'OPEN' && t.kind !== 'permission'")
    expect(drawer).toContain("entry.turn.kind === 'permission'")
  })

  it('answers through the session permission endpoint', () => {
    expect(prompt).toContain('answerSessionPermission(props.sid, props.decision.id, value)')
    expect(prompt).toContain('附原因拒绝')
  })
})
