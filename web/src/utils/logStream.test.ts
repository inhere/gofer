import { describe, expect, it } from 'vitest'
import { defaultLogStream } from './logStream'

describe('defaultLogStream', () => {
  it('uses stderr for an exec job with only stderr output', () => {
    expect(defaultLogStream({ autoStderr: true, stdout: '', stderr: 'compile failed' })).toBe('stderr')
  })

  it('keeps stdout for an exec job when stdout already has content', () => {
    expect(defaultLogStream({ autoStderr: true, stdout: 'result', stderr: 'diagnostic' })).toBe('stdout')
  })

  it('keeps stdout for an agent job even when stderr has content', () => {
    expect(defaultLogStream({ autoStderr: false, stdout: '', stderr: 'tool event' })).toBe('stdout')
  })
})
