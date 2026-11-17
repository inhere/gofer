import { describe, expect, it } from 'vitest'
import type { Interaction } from '../api/types'
import { choiceAnswer, initialPicks, interactionAnswerText, isRecommended, optionLabel } from './interactionCard'

const base: Interaction = {
  id: 'i1',
  job_id: 'j1',
  type: 'choice',
  prompt: '用哪种数据库？',
  options: [
    { value: 'pg', label: 'PostgreSQL', description: '已经有现成实例' },
    { value: 'sqlite', label: 'SQLite' },
    { value: 'mysql', label: 'MySQL' },
  ],
  status: 'pending',
  created_at: 1,
}

describe('interaction card choices', () => {
  it('labels an option by its label, falling back to the value', () => {
    expect(optionLabel({ value: 'v' })).toBe('v')
    expect(optionLabel({ value: 'v', label: 'V' })).toBe('V')
  })

  it('marks the recommended option by label or value', () => {
    expect(isRecommended({ recommended: 'SQLite' }, base.options![1])).toBe(true)
    expect(isRecommended({ recommended: 'mysql' }, base.options![2])).toBe(true)
    expect(isRecommended({ recommended: 'mysql' }, base.options![1])).toBe(false)
    expect(isRecommended({}, base.options![1])).toBe(false)
    expect(isRecommended({ recommended: '   ' }, base.options![1])).toBe(false)
  })

  it('preselects the recommended option and nothing else', () => {
    expect(initialPicks({ ...base, recommended: 'SQLite' })).toEqual(['sqlite'])
    expect(initialPicks(base)).toEqual([])
    // 推荐项不在选项里（后端会拒，但前端不该因此预选错东西）
    expect(initialPicks({ ...base, recommended: 'redis' })).toEqual([])
  })

  it('submits a single pick as its label, free text wins', () => {
    expect(choiceAnswer(base, ['pg'], '')).toBe('PostgreSQL')
    expect(choiceAnswer(base, ['pg'], '  达梦 ')).toBe('达梦')
    expect(choiceAnswer(base, [], '达梦')).toBe('达梦')
    expect(choiceAnswer({ ...base, allow_other: true }, [], '')).toBeNull()
    expect(choiceAnswer(base, [], '')).toBeNull()
  })

  it('submits several picks as a JSON array of labels', () => {
    const multi = { ...base, multi: true, allow_other: true }
    expect(choiceAnswer(multi, ['sqlite', 'pg'], '')).toBe(JSON.stringify(['SQLite', 'PostgreSQL']))
    expect(choiceAnswer(multi, ['sqlite'], '达梦')).toBe(JSON.stringify(['SQLite', '达梦']))
    expect(choiceAnswer(multi, [], '达梦')).toBe(JSON.stringify(['达梦']))
    expect(choiceAnswer(multi, [], '')).toBeNull()
    // 未知 value（老记录 / 拼错）不进答案
    expect(choiceAnswer(multi, ['redis'], '')).toBeNull()
  })

  it('shows a multi answer as a readable list', () => {
    expect(interactionAnswerText({ ...base, multi: true, answer: '["SQLite","PostgreSQL"]' })).toBe('SQLite, PostgreSQL')
    expect(interactionAnswerText({ ...base, answer: 'SQLite' })).toBe('SQLite')
    expect(interactionAnswerText({ ...base, multi: true, answer: '达梦' })).toBe('达梦')
    expect(interactionAnswerText({ ...base, multi: true })).toBe('')
  })
})
