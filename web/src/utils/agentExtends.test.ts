import { describe, expect, it } from 'vitest'
import { extendsBase, extendsLabel } from './agentExtends'

describe('agentExtends', () => {
  it('labels the three spellings', () => {
    expect(extendsLabel('builtin')).toBe('继承内置')
    expect(extendsLabel('builtin:claude')).toBe('继承内置 claude')
    expect(extendsLabel('claude')).toBe('继承 claude')
    expect(extendsLabel(undefined)).toBe('')
  })
  it('describes the base', () => {
    expect(extendsBase('builtin')).toBe('同名内置模板')
    expect(extendsBase('builtin:codex')).toContain('codex')
    expect(extendsBase('claude')).toContain('agent claude')
  })
})
