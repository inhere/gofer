import { describe, expect, it } from 'vitest'
import { attachmentBubbleText, stripAttachmentMarkers } from './attachmentText'

describe('stripAttachmentMarkers', () => {
  it('drops the inline marker the server appends for a file-channel send', () => {
    expect(stripAttachmentMarkers('see this [附图 1] /exec/alpha/tmp/gofer-attachments/20261020/a.png')).toBe('see this')
  })

  it('drops markers that sit on their own lines (multi-image sends)', () => {
    const text = '看图\n\n[附图 1] /tmp/x/1.png\n[附图 2] /tmp/x/2.png'
    expect(stripAttachmentMarkers(text)).toBe('看图')
  })

  it('drops the project-relative suffix of a low worker version', () => {
    const text = 'look [附图 1] tmp/gofer-attachments/20261020/a.png（相对于项目 alpha 根目录）'
    expect(stripAttachmentMarkers(text)).toBe('look')
  })

  it('keeps the text when a marker is not at the end', () => {
    const text = '日志里写着 [附图 1] /tmp/x.png 然后继续报错'
    expect(stripAttachmentMarkers(text)).toBe(text)
  })

  it('keeps other bracket text and messages without markers verbatim', () => {
    expect(stripAttachmentMarkers('附图 1 已上传')).toBe('附图 1 已上传')
    expect(stripAttachmentMarkers('结束\n\n')).toBe('结束\n\n')
    expect(stripAttachmentMarkers('')).toBe('')
  })

  it('returns an empty body when the message was only images', () => {
    expect(stripAttachmentMarkers('[附图 1] /tmp/x/1.png')).toBe('')
  })

  it('cleans a bubble only when the message carries attachments', () => {
    const text = 'enclosing [附图 1] /tmp/x/1.png'
    expect(attachmentBubbleText(text, [{ id: 'a1' }])).toBe('enclosing')
    // 老消息没有 attachments 字段：路径是正文里唯一提到图的地方，保留原文。
    expect(attachmentBubbleText(text, undefined)).toBe(text)
    expect(attachmentBubbleText(text, [])).toBe(text)
    expect(attachmentBubbleText(undefined, [{ id: 'a1' }])).toBe('')
  })
})
