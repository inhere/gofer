import { describe, expect, it } from 'vitest'

const drawer = Object.values(import.meta.glob('../SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const thread = Object.values(import.meta.glob('./ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

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

  it('uses the compact embedded drawer and one-row mobile composer', () => {
    expect(drawer).toContain('v-if="!embedded" class="drawer-head"')
    expect(drawer).toContain('v-if="session && !embedded" class="meta-wrap"')
    expect(thread).toContain('class="mobile-menu mono"')
    expect(thread).toContain('rows="1"')
  })

  it('exposes acknowledge controls for an open relay turn', () => {
    expect(drawer).toContain('无需回复')
    expect(drawer).toContain('已读 · 无需回复（仍可回复）')
    expect(thread).toContain('无需回复')
  })
})
