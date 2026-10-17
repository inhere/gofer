import { describe, expect, it } from 'vitest'
import { callerLabel, callerTitle } from './callerLabel'

describe('callerLabel', () => {
  const names = { default: 'admin', bob: '  ' }
  it('shows the name when there is one', () => {
    expect(callerLabel('default', names)).toBe('admin')
    expect(callerTitle('default', names)).toBe('admin (default)')
  })
  it('falls back to the id', () => {
    expect(callerLabel('alice', names)).toBe('alice')
    expect(callerLabel('bob', names)).toBe('bob')
    expect(callerTitle('alice', names)).toBe('alice')
    expect(callerLabel('x', undefined)).toBe('x')
    expect(callerLabel('x', null)).toBe('x')
  })
  it('keeps empty ids empty', () => {
    expect(callerLabel('', names)).toBe('')
    expect(callerLabel(undefined, names)).toBe('')
    expect(callerTitle(null, names)).toBe('')
  })
})
