import { describe, expect, it } from 'vitest'
import {
  MAX_ATTACHMENTS_PER_MESSAGE,
  MAX_ATTACHMENT_BYTES,
  MOBILE_JPEG_QUALITY,
  MOBILE_MAX_SIDE,
  UNSUPPORTED_RECIPIENT_REASON,
  UNSUPPORTED_SERVER_REASON,
  attachmentCapability,
  attachmentCountError,
  attachmentSizeError,
  attachmentTypeError,
  composerAttachmentState,
  formatBytes,
  planCompression,
  type AttachmentDraft,
} from './attachments'

function draft(changes: Partial<AttachmentDraft> = {}): AttachmentDraft {
  return { key: 'k1', name: 'shot.png', mime: 'image/png', bytes: 1000, status: 'done', id: 'a1', ...changes }
}

describe('attachment type / size / count guards', () => {
  it('accepts the four server-side image types and nothing else', () => {
    expect(attachmentTypeError('image/png')).toBe('')
    expect(attachmentTypeError('image/jpeg')).toBe('')
    expect(attachmentTypeError('image/gif')).toBe('')
    expect(attachmentTypeError('image/webp')).toBe('')
    expect(attachmentTypeError('image/svg+xml')).not.toBe('')
    expect(attachmentTypeError('application/pdf')).not.toBe('')
    expect(attachmentTypeError('')).not.toBe('')
  })

  it('rejects a file over the server limit and allows the limit itself', () => {
    expect(attachmentSizeError(MAX_ATTACHMENT_BYTES)).toBe('')
    expect(attachmentSizeError(MAX_ATTACHMENT_BYTES + 1)).not.toBe('')
  })

  it('stops at six images per message', () => {
    expect(attachmentCountError(0)).toBe('')
    expect(attachmentCountError(MAX_ATTACHMENTS_PER_MESSAGE - 1)).toBe('')
    expect(attachmentCountError(MAX_ATTACHMENTS_PER_MESSAGE)).not.toBe('')
    expect(attachmentCountError(MAX_ATTACHMENTS_PER_MESSAGE + 2)).not.toBe('')
  })

  it('formats sizes for the hint text', () => {
    expect(formatBytes(0)).toBe('0B')
    expect(formatBytes(512)).toBe('512B')
    expect(formatBytes(2048)).toBe('2.0KB')
    expect(formatBytes(3 * 1024 * 1024)).toBe('3.0MB')
  })
})

describe('compression plan', () => {
  it('compresses a big phone photo to longest side 2048 / JPEG 0.85', () => {
    const plan = planCompression({ mime: 'image/jpeg', bytes: 3 * 1024 * 1024, width: 4032, height: 3024, viewportWidth: 390 })
    expect(plan.compress).toBe(true)
    expect(plan.maxSide).toBe(MOBILE_MAX_SIDE)
    expect(plan.quality).toBe(MOBILE_JPEG_QUALITY)
    expect(plan.mime).toBe('image/jpeg')
  })

  it('leaves a small phone screenshot (PNG under 1 MiB, sides fit) untouched', () => {
    expect(planCompression({ mime: 'image/png', bytes: 400 * 1024, width: 1080, height: 1920, viewportWidth: 390 }).compress).toBe(false)
  })

  it('compresses a phone PNG that is over 1 MiB even when the sides fit', () => {
    expect(planCompression({ mime: 'image/png', bytes: 2 * 1024 * 1024, width: 1200, height: 900, viewportWidth: 900 }).compress).toBe(true)
  })

  it('compresses an over-long phone image even when it is small', () => {
    expect(planCompression({ mime: 'image/jpeg', bytes: 300 * 1024, width: 5000, height: 400, viewportWidth: 700 }).compress).toBe(true)
  })

  it('never touches a GIF (that would drop the animation)', () => {
    expect(planCompression({ mime: 'image/gif', bytes: 5 * 1024 * 1024, width: 4000, height: 4000, viewportWidth: 390 }).compress).toBe(false)
  })

  it('keeps the original on the desktop until it is over 10 MiB', () => {
    expect(planCompression({ mime: 'image/jpeg', bytes: 9 * 1024 * 1024, width: 6000, height: 4000, viewportWidth: 1440 }).compress).toBe(false)
    expect(planCompression({ mime: 'image/jpeg', bytes: 11 * 1024 * 1024, width: 6000, height: 4000, viewportWidth: 1440 }).compress).toBe(true)
  })

  it('does not compress a type the server would refuse anyway', () => {
    expect(planCompression({ mime: 'application/pdf', bytes: 20 * 1024 * 1024, width: 0, height: 0, viewportWidth: 390 }).compress).toBe(false)
  })
})

describe('thread capability to composer state', () => {
  it('treats a missing capability field as "this server has no attachments"', () => {
    const cap = attachmentCapability({})
    expect(cap.mode).toBe('none')
    expect(cap.enabled).toBe(false)
    expect(cap.reason).toBe(UNSUPPORTED_SERVER_REASON)
    expect(attachmentCapability(null).reason).toBe(UNSUPPORTED_SERVER_REASON)
  })

  it('enables the paperclip for both delivery modes', () => {
    expect(attachmentCapability({ attachments: 'inline' })).toMatchObject({ mode: 'inline', enabled: true, reason: '' })
    expect(attachmentCapability({ attachments: 'file' })).toMatchObject({ mode: 'file', enabled: true, reason: '' })
  })

  it('greys the paperclip out and uses the server reason for none', () => {
    expect(attachmentCapability({ attachments: 'none', attachments_reason: 'pty 任务收不到图片' }).reason).toBe('pty 任务收不到图片')
    expect(attachmentCapability({ attachments: 'none' }).reason).toBe(UNSUPPORTED_RECIPIENT_REASON)
  })

  it('ignores a capability value it does not know', () => {
    expect(attachmentCapability({ attachments: 'maybe' }).enabled).toBe(false)
  })

  it('blocks sending while an upload runs and while one failed', () => {
    const cap = attachmentCapability({ attachments: 'inline' })
    const clean = composerAttachmentState([], cap)
    expect(clean.canSend).toBe(true)
    expect(clean.readyIDs).toEqual([])

    const uploading = composerAttachmentState([draft({ key: 'a', status: 'uploading', id: undefined })], cap)
    expect(uploading.uploading).toBe(true)
    expect(uploading.canSend).toBe(false)
    expect(uploading.hint).toContain('上传')

    const failed = composerAttachmentState([draft({ key: 'a', status: 'failed', id: undefined })], cap)
    expect(failed.failedCount).toBe(1)
    expect(failed.canSend).toBe(false)
    expect(failed.hint).toContain('失败')

    const done = composerAttachmentState([draft({ key: 'a' }), draft({ key: 'b', id: 'a2' })], cap)
    expect(done.readyIDs).toEqual(['a1', 'a2'])
    expect(done.hint).toBe('')
  })

  it('puts the greying-out reason on the paperclip title', () => {
    const cap = attachmentCapability({ attachments: 'none', attachments_reason: '收件方收不到图片' })
    const state = composerAttachmentState([], cap)
    expect(state.attachEnabled).toBe(false)
    expect(state.attachTitle).toBe('收件方收不到图片')
    expect(state.hint).toBe('收件方收不到图片')
  })
})
