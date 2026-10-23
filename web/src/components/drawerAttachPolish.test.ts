import { describe, expect, it } from 'vitest'

// 接线断言（手机实测 gofer 会话抽屉的多气泡 / 附图路径 / 冗余提示）：只读源文件，不渲染组件。
const drawer = Object.values(
  import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const app = Object.values(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('one bubble per relay message', () => {
  it('hides the turn answer bubble when an outbox message carries the same decision', () => {
    expect(drawer).toContain(`v-if="entry.turn.state === 'ANSWERED' && !entry.relayed"`)
    expect(drawer).toContain(`v-else-if="entry.turn.state !== 'ANSWERED'"`)
  })

  it('still shows the outbox bubble with its delivery status', () => {
    expect(drawer).toContain('relay-message-status')
    expect(drawer).toContain('messageStatusLabel(entry.message)')
  })
})

describe('no success receipt above the composer', () => {
  it('never sets the "replied" receipt any more', () => {
    expect(drawer).not.toContain('已回复 agent ✓')
    expect(drawer).not.toContain('sendResultLabel')
  })

  it('keeps the send failure error line', () => {
    expect(drawer).toContain('发送失败：')
    expect(drawer).toContain('class="error mono"')
  })
})

describe('attachment paths become thumbnails', () => {
  it('cleans the bubble text only for messages that carry attachments', () => {
    expect(drawer).toContain('attachmentBubbleText(entry.message.text, entry.message.attachments)')
    expect(drawer).toContain('attachmentBubbleText(entry.turn.answer, entry.turn.attachments)')
  })

  it('renders the shared thumbnail grid under the bubbles', () => {
    expect(drawer).toContain(`<AttachmentThumbs v-if="entry.message.attachments?.length" :items="entry.message.attachments" />`)
    expect(drawer).toContain(`<AttachmentThumbs v-if="entry.turn.attachments?.length" :items="entry.turn.attachments" />`)
  })
})

describe('drawer attach availability', () => {
  it('follows the thread capability instead of the open turn', () => {
    expect(drawer).toContain('return props.attachmentCapability')
    expect(drawer).not.toContain('不收附图')
    expect(drawer).not.toContain('等它回复后再发')
  })

  it('sends an attachment through the workbench turn even without an open turn', () => {
    expect(drawer).toContain('if (props.threadId && (openTurn.value || attachmentIDs.length > 0))')
  })

  it('keeps the reason in one place (paperclip title, not the note line)', () => {
    expect(drawer).toContain('attachHintNote(composerRef.value?.attachHint')
  })
})

describe('edge nav tab removal', () => {
  it('drops the draggable rail toggle from the shell (design §6.3)', () => {
    expect(app).not.toContain('rail-toggle')
    expect(app).not.toContain('navTab')
  })
})
