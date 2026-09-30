import { describe, expect, it } from 'vitest'
import { JOB_TITLE_MAX_LENGTH, normalizeJobTitle } from './jobTitle'

describe('normalizeJobTitle', () => {
  it('trims whitespace and keeps unicode characters intact', () => {
    expect(normalizeJobTitle('  修复手机端标题  ')).toBe('修复手机端标题')
  })

  it('caps titles at the submit limit', () => {
    expect(Array.from(normalizeJobTitle('a'.repeat(40)))).toHaveLength(JOB_TITLE_MAX_LENGTH)
  })
})
