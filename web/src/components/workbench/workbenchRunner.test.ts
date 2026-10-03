import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('workbench composer runner selection', () => {
  it('has a visible runner dropdown that greys out blocked runners with a reason', () => {
    expect(source).toContain('aria-label="Runner"')
    expect(source).toContain(':disabled="!!runnerBlocks[r.name]"')
    expect(source).toContain('runnerBlocks[r.name].short')
  })

  it('no longer hardcodes local for continuous sessions', () => {
    expect(source).not.toContain("mode.value === 'continuous' ? 'local'")
    expect(source).toContain("runner: runnerName.value || 'local'")
    expect(source).toContain('sessionRunnerAvailable')
  })

  it('switches an ACP agent from batch to the resident session with a note', () => {
    expect(source).toContain("mode.value = 'continuous'")
    expect(source).toContain('modeNote')
  })
})
