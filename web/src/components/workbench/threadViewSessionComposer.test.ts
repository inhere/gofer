import { describe, expect, it } from 'vitest'

const drawer = Object.values(import.meta.glob('../SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('workbench relay composer', () => {
  it('uses the relay answer only for an open turn and messenger otherwise', () => {
    expect(drawer).toContain("!!props.threadId")
    expect(drawer).toContain("if (props.threadId && openTurn.value)")
    expect(drawer).toContain('sendSessionMessage(props.sid, text)')
  })

  it('keeps the timeline pinned only when it was already at the bottom', () => {
    expect(drawer).toContain('isTimelineAtBottom')
    expect(drawer).toContain('wasAtBottom')
  })
})
