import { describe, expect, it } from 'vitest'

const drawer = Object.values(import.meta.glob('../SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const thread = Object.values(import.meta.glob('./ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('workbench relay composer', () => {
  it('answers through the relay turn when one is open or images ride along, messenger otherwise', () => {
    expect(drawer).toContain("!!props.threadId")
    expect(drawer).toContain('if (props.threadId && (openTurn.value || attachmentIDs.length > 0))')
    expect(drawer).toContain('sendSessionMessage(props.sid, text)')
  })

  it('keeps the timeline pinned only when it was already at the bottom', () => {
    expect(drawer).toContain('isTimelineAtBottom')
    expect(drawer).toContain('wasAtBottom')
  })

  it('uses the compact embedded drawer and the compact composer box', () => {
    expect(drawer).toContain('v-if="!embedded" class="drawer-head"')
    expect(drawer).toContain('v-if="session && !embedded" class="meta-wrap"')
    expect(thread).toContain('class="icon-btn mobile-menu"')
    // P4：输入框已换成统一的 Composer（紧凑样式在 Composer 自己的样式表里）。
    expect(thread).toContain('<Composer')
    expect(thread).toContain(':attach-capability="attachCapability"')
  })

  it('exposes acknowledge controls for an open relay turn', () => {
    expect(drawer).toContain('无需回复')
    expect(drawer).toContain('已读 · 无需回复（仍可回复）')
    expect(thread).toContain('无需回复')
  })
})
