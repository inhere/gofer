import { describe, expect, it } from 'vitest'
import { UNSUPPORTED_RECIPIENT_REASON, attachHintNote, attachmentCapability, composerAttachmentState } from './attachments'

describe('attachHintNote', () => {
  it('drops the line when it would only repeat the paperclip title', () => {
    const cap = attachmentCapability({ attachments: 'none', attachments_reason: '收件方收不到图片' })
    const state = composerAttachmentState([], cap)
    expect(state.attachTitle).toBe('收件方收不到图片')
    expect(state.hint).toBe('收件方收不到图片')
    expect(attachHintNote(state.hint, cap.reason)).toBe('')
  })

  it('keeps an upload-state hint, which the title does not carry', () => {
    const cap = attachmentCapability({ attachments: 'inline' })
    const state = composerAttachmentState([{ key: 'a', name: 'a.png', mime: 'image/png', bytes: 1, status: 'uploading' }], cap)
    expect(attachHintNote(state.hint, cap.reason)).toBe(state.hint)
  })

  it('keeps a failed-upload hint and shows nothing when the bar is clean', () => {
    const cap = attachmentCapability({ attachments: 'none', attachments_reason: UNSUPPORTED_RECIPIENT_REASON })
    const failed = composerAttachmentState([{ key: 'a', name: 'a.png', mime: 'image/png', bytes: 1, status: 'failed' }], cap)
    expect(attachHintNote(failed.hint, cap.reason)).toContain('失败')
    expect(attachHintNote('', cap.reason)).toBe('')
  })
})
