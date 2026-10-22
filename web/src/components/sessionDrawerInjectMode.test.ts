import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('SessionDrawer delivery mode and 现在送', () => {
  it('offers the three delivery modes', () => {
    expect(source).toContain('INJECT_MODES')
    expect(source).toContain('injectModeLabel(m)')
    expect(source).toContain('setSessionInjectMode')
  })

  it('sends the waiting messages from the drawer', () => {
    expect(source).toContain('sendHeldSessionMessages')
    expect(source).toContain('data-test="drawer-send-held"')
    expect(source).toContain('heldLabel(heldPending)')
  })

  it('explains that a held message waits for the next hook', () => {
    expect(source).toContain('留待')
    expect(source).toContain('inject_mode')
  })
})
