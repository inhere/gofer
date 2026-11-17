import { describe, expect, it } from 'vitest'

// 接线断言（手机实测 gofer 会话抽屉的多气泡 / 附图路径 / 冗余提示）：只读源文件，不渲染组件。
const drawer = Object.values(
  import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const app = Object.values(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('one bubble per relay message', () => {
  it('hides the turn answer bubble when an outbox message carries the same decision', () => {
    // R12：终端轮次仍在抽屉里画（走时间线的 #custom 插槽），只是按 id 取回原始行。
    expect(drawer).toContain(`v-if="turnRow(entry.id).turn.state === 'ANSWERED' && !turnRow(entry.id).relayed"`)
    expect(drawer).toContain(`v-else-if="turnRow(entry.id).turn.state !== 'ANSWERED'"`)
  })

  it('still shows the outbox bubble with its delivery status', () => {
    expect(drawer).toContain('relay-message-status')
    expect(drawer).toContain('messageStatusLabel(relayMessage(entry.id)!)')
  })
})

describe('no success receipt above the composer', () => {
  it('never sets the "replied" receipt any more', () => {
    expect(drawer).not.toContain('已回复 agent ✓')
    expect(drawer).not.toContain('sendResultLabel')
  })

  it('keeps the send failure error line', () => {
    expect(drawer).toContain("t('sessions.drawer.sendFailed'")
    expect(drawer).toContain('class="error mono"')
  })
})

describe('attachment paths become thumbnails', () => {
  it('cleans the bubble text only for messages that carry attachments', () => {
    // 人发的消息在适配成时间线条目时清理附图痕迹；终端轮次的答复仍在 #custom 插槽里。
    expect(drawer).toContain('attachmentBubbleText(entry.message.text, entry.message.attachments)')
    expect(drawer).toContain('attachmentBubbleText(turnRow(entry.id).turn.answer, turnRow(entry.id).turn.attachments)')
  })

  it('renders the shared thumbnail grid under the bubbles', () => {
    expect(drawer).toContain(`<AttachmentThumbs v-if="turnRow(entry.id).turn.attachments?.length" :items="turnRow(entry.id).turn.attachments ?? []" />`)
    // 人发的消息 / 回复的缩略图由统一时间线渲染（ConversationTimeline 里的 attachmentsOf）。
    expect(drawer).toContain('attachments: entry.message.attachments')
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
    // P4：提示由 Composer 自己渲染（附图条标题），抽屉不再重复一行。
    expect(drawer).toContain(':attach-capability="attachCapability"')
    expect(drawer).not.toContain('class="attach-note mono"')
  })
})

describe('edge nav tab removal', () => {
  it('drops the draggable rail toggle from the shell (design §6.3)', () => {
    expect(app).not.toContain('rail-toggle')
    expect(app).not.toContain('navTab')
  })
})
