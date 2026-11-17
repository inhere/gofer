import { describe, expect, it } from 'vitest'
import type { Decision } from '../api/types'
import {
  permissionCardAnswer,
  permissionCardInteraction,
  questionCardInteractions,
  questionPickText,
} from './sessionPermissionCard'

const perm: Decision = {
  id: 'd1',
  title: 't',
  question: '需要授权：Bash `npm test`',
  state: 'OPEN',
  timeout_sec: 600,
  asked_at: 1000,
  session_id: 's1',
  kind: 'permission',
  permission: {
    tool_name: 'Bash',
    summary: 'Bash `npm test`',
    input: '{"command":"npm test"}',
    suggestions: [{ label: '规则 Bash(npm test)' }],
  },
}

describe('permission card projection', () => {
  it('maps the choices, the tool call and the deadline', () => {
    const it = permissionCardInteraction(perm)
    expect(it.type).toBe('permission')
    expect(it.status).toBe('pending')
    expect(it.job_id).toBe('s1')
    expect(it.prompt).toBe('需要授权：Bash `npm test`')
    expect(it.options?.map((o) => [o.value, o.label, o.kind])).toEqual([
      ['allow', '允许', 'allow_once'],
      ['always:0', '总是允许：规则 Bash(npm test)', 'allow_once'],
      ['deny', '拒绝', 'reject_once'],
    ])
    // 「其他」= 附原因拒绝
    expect(it.allow_other).toBe(true)
    expect(it.expires_at).toBe(1600)
    expect(it.tool_call).toMatchObject({
      id: 'd1',
      kind: 'Bash',
      title: 'Bash `npm test`',
      raw_input_summary: '{"command":"npm test"}',
    })
  })

  it('shows the human answer once answered, and the expired state', () => {
    const answered = permissionCardInteraction({
      ...perm,
      state: 'ANSWERED',
      answer: 'deny:不要删',
      answered_at: 1010,
      answered_by: 'web',
    })
    expect(answered.status).toBe('answered')
    expect(answered.answer).toBe('已拒绝：不要删')
    expect(answered.answered_at).toBe(1010)
    expect(answered.answered_by).toBe('web')
    const expired = permissionCardInteraction({ ...perm, state: 'EXPIRED' })
    expect(expired.status).toBe('expired')
  })

  it('encodes free text as deny-with-reason, keeps option values as they are', () => {
    const known = (permissionCardInteraction(perm).options ?? []).map((o) => o.value)
    expect(permissionCardAnswer('allow', known)).toBe('allow')
    expect(permissionCardAnswer(' always:0 ', known)).toBe('always:0')
    expect(permissionCardAnswer('  不要删 ', known)).toBe('deny:不要删')
    expect(permissionCardAnswer('   ', known)).toBe('')
  })
})

const questions: Decision = {
  id: 'd2',
  title: 't',
  question: '等你回答：颜色？',
  state: 'OPEN',
  timeout_sec: 0,
  asked_at: 2000,
  session_id: 's2',
  kind: 'permission',
  permission: {
    tool_name: 'AskUserQuestion',
    summary: '颜色？',
    questions: [
      { question: '颜色？', header: '色', options: [{ label: '红', description: '热情' }, { label: '蓝' }] },
      { question: '语言？', options: [{ label: 'Go' }, { label: 'TS' }], multiSelect: true },
    ],
  },
}

describe('question cards projection', () => {
  it('makes one card per question with its own select mode', () => {
    const cards = questionCardInteractions(questions)
    expect(cards).toHaveLength(2)
    expect(cards[0]).toMatchObject({ id: 'd2:0', type: 'choice', status: 'pending', prompt: '色 · 颜色？' })
    expect(cards[0].multi).toBe(false)
    expect(cards[0].allow_other).toBe(true)
    expect(cards[0].options).toEqual([
      { value: '红', label: '红', description: '热情' },
      { value: '蓝', label: '蓝', description: undefined },
    ])
    expect(cards[1]).toMatchObject({ id: 'd2:1', prompt: '语言？', multi: true })
  })

  it('reads the per-question answers of an answered decision', () => {
    const cards = questionCardInteractions({
      ...questions,
      state: 'ANSWERED',
      answer: 'answers:{"颜色？":"蓝","语言？":"Go, TS"}',
    })
    expect(cards.map((c) => [c.status, c.answer])).toEqual([
      ['answered', '蓝'],
      ['answered', 'Go, TS'],
    ])
    const expired = questionCardInteractions({ ...questions, state: 'EXPIRED' })
    expect(expired.map((c) => c.status)).toEqual(['expired', 'expired'])
  })

  it('turns a multi pick into the comma list the terminal expects', () => {
    expect(questionPickText('["Go","TS"]', true)).toBe('Go, TS')
    expect(questionPickText('Rust', true)).toBe('Rust')
    expect(questionPickText('蓝', false)).toBe('蓝')
  })
})
