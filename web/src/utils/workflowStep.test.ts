import { describe, expect, it } from 'vitest'
import { workflowStepText } from './workflowStep'

describe('workflowStepText', () => {
  it('clamps an out-of-range current step and marks done', () => {
    expect(workflowStepText('done', 2, 1)).toBe('1/1 (done)')
    expect(workflowStepText('done', 3, 3)).toBe('3/3 (done)')
  })
  it('leaves running workflows alone', () => {
    expect(workflowStepText('running', 2, 3)).toBe('2/3')
  })
  it('clamps non-done terminal states without a done mark', () => {
    expect(workflowStepText('failed', 4, 3)).toBe('3/3')
  })
})
