import { describe, expect, it } from 'vitest'
import { heldLabel, INJECT_MODES, injectModeLabel, injectModeTitle } from './injectMode'

describe('injectMode', () => {
  it('lists the three delivery modes', () => {
    expect(INJECT_MODES).toEqual(['auto', 'hold', 'force'])
  })

  it('labels every mode', () => {
    expect(injectModeLabel('auto')).toBe('看输入框')
    expect(injectModeLabel('hold')).toBe('留待')
    expect(injectModeLabel('force')).toBe('直送')
  })

  it('explains every mode', () => {
    for (const mode of INJECT_MODES) {
      expect(injectModeTitle(mode).length).toBeGreaterThan(4)
    }
  })

  it('shows the now-send button only when something waits', () => {
    expect(heldLabel(0)).toBe('')
    expect(heldLabel(undefined)).toBe('')
    expect(heldLabel(2)).toBe('现在送 2')
    expect(heldLabel(-3)).toBe('')
  })
})
