import { describe, expect, it } from 'vitest'

// 接线断言：两处输入框都用了 AttachmentBar，发出去的 turn 请求带上附件 id，
// 历史缩略图接在 ACP 对话的气泡下。用 ?raw 读源文件（不渲染组件）。
// import.meta.glob 只认字面量路径，所以一个个列出来。
function raw(modules: Record<string, unknown>): string {
  return Object.values(modules)[0] as string
}

const bar = raw(import.meta.glob('./AttachmentBar.vue', { eager: true, query: '?raw', import: 'default' }))
const thumbs = raw(import.meta.glob('./AttachmentThumbs.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = raw(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const thread = raw(import.meta.glob('./workbench/ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))
const conversation = raw(import.meta.glob('./workbench/ConversationView.vue', { eager: true, query: '?raw', import: 'default' }))
const workbenchAPI = raw(import.meta.glob('../api/workbench.ts', { eager: true, query: '?raw', import: 'default' }))
const attachmentAPI = raw(import.meta.glob('../api/attachments.ts', { eager: true, query: '?raw', import: 'default' }))

describe('attachment bar', () => {
  it('offers paste, drop and a file picker that lets phones take a photo', () => {
    expect(bar).toContain('accept="image/*"')
    expect(bar).toContain('multiple')
    expect(bar).toContain('addFromPaste')
    expect(bar).toContain('addFromDrop')
    expect(bar).toContain('@drop.prevent="addFromDrop($event)"')
    expect(bar).toContain('function handleDrop(event: DragEvent)')
  })

  it('uploads right away, shows progress, retries and deletes', () => {
    expect(bar).toContain('uploadAttachment(')
    expect(bar).toContain('deleteAttachment(')
    expect(bar).toContain('attach-progress')
    expect(bar).toContain('attach-retry')
    expect(bar).toContain('attach-remove')
  })

  it('decides compression through the pure plan and revokes previews', () => {
    expect(bar).toContain('planCompression(')
    expect(bar).toContain('canvas.toBlob')
    expect(bar).toContain('URL.revokeObjectURL')
    expect(bar).toContain('composerAttachmentState(')
  })
})

describe('composer wiring', () => {
  it('mounts the bar in the workbench turn composer with the thread capability', () => {
    // P4：附件条收进统一的 Composer，能力位仍由 thread 决定。
    expect(thread).toContain('<Composer')
    expect(thread).toContain('attachments')
    expect(thread).toContain(':attach-capability="attachCapability"')
    expect(thread).toContain('attachmentCapability(thread.value)')
  })

  it('sends the turn with the uploaded attachment ids and clears the bar after success', () => {
    expect(thread).toContain('turnInput.value?.readyIDs()')
    expect(thread).toContain('turnWorkbenchThread(current.id, text, attachmentIDs)')
    expect(thread).toContain('turnInput.value?.clearAttachments()')
    // 排队 / 打断模式要在这一轮还在跑时也能提交：Composer 的禁用只看文本与「直接发送」模式。
    const composer = raw(import.meta.glob('../ui/Composer.vue', { eager: true, query: '?raw', import: 'default' }))
    expect(composer).toContain("mode.value === 'send' && !props.canSend")
  })

  it('mounts the bar in the session drawer answer box and gates on an open turn', () => {
    expect(drawer).toContain('<Composer')
    expect(drawer).toContain('attachments')
    expect(drawer).toContain(':attach-capability="attachCapability"')
    expect(drawer).toContain('turnWorkbenchThread(props.threadId, text, attachmentIDs)')
    expect(drawer).toContain('composerRef.value?.clearAttachments()')
    expect(drawer).toContain('composerRef.value?.readyIDs()')
  })

  it('sends attachments only with the attachment field present', () => {
    expect(workbenchAPI).toContain('export function turnWorkbenchThread(')
    expect(workbenchAPI).toContain('attachmentIDs: string[] = []')
    expect(workbenchAPI).toContain('body.attachments = attachmentIDs')
  })

  it('reads originals and thumbnails with the auth header', () => {
    expect(attachmentAPI).toContain("'/v1/attachments'")
    expect(attachmentAPI).toContain('thumb=1')
    expect(attachmentAPI).toContain('Authorization')
    expect(attachmentAPI).toContain('XMLHttpRequest')
  })
})

describe('history thumbnails', () => {
  it('shows the thumbs under both bubbles and opens the original', () => {
    expect(conversation).toContain('<AttachmentThumbs')
    expect(conversation).toContain('function eventAttachments(event: ACPTextEvent)')
    expect(thumbs).toContain('attachmentBlobURL(item.id, true)')
    expect(thumbs).toContain('attachmentBlobURL(item.id, false)')
    expect(thumbs).toContain("t('common.attachments.expired')")
    expect(thumbs).toContain('onBeforeUnmount(releaseAll)')
  })
})
