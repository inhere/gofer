// 工作台附图的上传 / 删除 / 取图（gofer-ha0x）。
//  - 上传用 XHR：需要 upload 进度事件，fetch 拿不到；
//  - 取原件 / 缩略图必须带 Authorization，故走 fetch + blob URL，调用方用完 revoke。

import { getToken, triggerUnauthorized } from '../store/auth'
import { request } from './client'
import type { AttachmentInfo } from './types'

const UNAUTHORIZED_MESSAGE = '未授权（401）：token 无效或已失效'

function authHeader(): Record<string, string> {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

// uploadErrorMessage 把服务端的 {error, detail} 翻成一句话；413 / 415 / 429 有专门说法。
function uploadErrorMessage(status: number, body: string): string {
  let message = ''
  try {
    const parsed = JSON.parse(body) as { error?: string; detail?: string }
    message = [parsed.error, parsed.detail].filter(Boolean).join(' - ')
  } catch {
    // 非 JSON 错误体：用下面的状态码文案
  }
  switch (status) {
    case 413:
      return `图片超过服务端上限${message ? `（${message}）` : ''}`
    case 415:
      return `服务端不支持这种图片类型${message ? `（${message}）` : ''}`
    case 429:
      return '上传太频繁，稍后再试'
    default:
      return message || `上传失败：HTTP ${status}`
  }
}

// uploadAttachment 上传一张图（multipart 字段 file），onProgress 收到 0–1 的进度。
export function uploadAttachment(
  file: Blob,
  filename: string,
  onProgress?: (ratio: number) => void,
): Promise<AttachmentInfo> {
  return new Promise<AttachmentInfo>((resolve, reject) => {
    const form = new FormData()
    form.append('file', file, filename)
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/v1/attachments')
    for (const [key, value] of Object.entries(authHeader())) xhr.setRequestHeader(key, value)
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && event.total > 0) onProgress?.(event.loaded / event.total)
    }
    xhr.onerror = () => reject(new Error('上传失败：网络错误'))
    xhr.onabort = () => reject(new Error('上传已取消'))
    xhr.onload = () => {
      if (xhr.status === 401) {
        triggerUnauthorized()
        reject(new Error(UNAUTHORIZED_MESSAGE))
        return
      }
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(new Error(uploadErrorMessage(xhr.status, xhr.responseText)))
        return
      }
      try {
        resolve(JSON.parse(xhr.responseText) as AttachmentInfo)
      } catch {
        reject(new Error('上传失败：响应不是 JSON'))
      }
    }
    xhr.send(form)
  })
}

// deleteAttachment 删除还没发送的附件（服务端：发送后 409，别人的 404）。
export function deleteAttachment(id: string): Promise<void> {
  return request<void>(`/v1/attachments/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function attachmentPath(id: string, thumb = false): string {
  return `/v1/attachments/${encodeURIComponent(id)}${thumb ? '?thumb=1' : ''}`
}

// fetchAttachmentBlob 取原件或缩略图的字节（需鉴权头，不能给 <img src> 一个裸 URL）。
export async function fetchAttachmentBlob(id: string, thumb = false): Promise<Blob> {
  const res = await fetch(attachmentPath(id, thumb), { headers: authHeader() })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error(UNAUTHORIZED_MESSAGE)
  }
  if (!res.ok) throw new Error(`读取附件失败：HTTP ${res.status}`)
  return res.blob()
}

// attachmentBlobURL 取图并转成 blob URL；调用方在组件卸载时 revoke。
export async function attachmentBlobURL(id: string, thumb = false): Promise<string> {
  return URL.createObjectURL(await fetchAttachmentBlob(id, thumb))
}
