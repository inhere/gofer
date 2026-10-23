// 工作台附图（gofer-ha0x）前端口径：类型 / 大小 / 数量判断、压缩参数选择、thread 能力位到
// 输入框状态的映射。全部是不碰 DOM 的纯函数，便于 vitest 直接测；canvas 压缩与上传在
// components/AttachmentBar.vue 里按这里的参数执行。

import { t } from '../i18n'
import { isMobileWidth } from './mobileLayout'

// 一条消息最多几张：与服务端 server.attachments.max_per_message 的默认值一致。
export const MAX_ATTACHMENTS_PER_MESSAGE = 6
// 单张上限：与服务端 server.attachments.max_bytes 的默认值一致（10MiB）。
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024

// 服务端只按文件头认这四种（SVG 不在允许列表：避免脚本）。
export const SUPPORTED_ATTACHMENT_MIMES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] as const

// 手机档压缩口径（设计决策 4）：长边 2048、JPEG 0.85；小于 1MB 的 PNG 截图保持原样。
export const MOBILE_MAX_SIDE = 2048
export const MOBILE_JPEG_QUALITY = 0.85
export const MOBILE_COMPRESS_MAX_SIDE = 2048
export const MOBILE_COMPRESS_MIN_BYTES = 1024 * 1024
// 桌面默认原图，超过这个体积才压。
export const DESKTOP_COMPRESS_MIN_BYTES = 10 * 1024 * 1024
// GIF 压了就丢动画，所以永远不压。
const UNCOMPRESSED_MIME = 'image/gif'

// thread 的附件能力位（后端 P4 提供；缺失 = 老服务端，按不支持处理）。
export type AttachmentMode = 'inline' | 'file' | 'arg' | 'none'

export interface AttachmentCapability {
  mode: AttachmentMode
  // 📎 是否可用；false 时 reason 说明原因（title 与输入框下方小字共用）。
  enabled: boolean
  reason: string
}

export interface AttachmentCapabilitySource {
  attachments?: AttachmentMode | string
  attachments_reason?: string
}

// 附件不可用的原因：导出函数（文案跟当前语言）。
export function unsupportedServerReason(): string {
  return t('util.attachments.unsupportedServer')
}
export function unsupportedRecipientReason(): string {
  return t('util.attachments.unsupportedRecipient')
}
export function unsupportedOutsideThreadReason(): string {
  return t('util.attachments.unsupportedOutsideThread')
}

// 附件条里的每一项：选中即上传，上传成功后才有 id；失败项可重试或删除。
export type AttachmentStatus = 'uploading' | 'done' | 'failed'

export interface AttachmentDraft {
  // 本地唯一 key（上传前后不变），用作 v-for 的 key 与更新定位。
  key: string
  name: string
  mime: string
  bytes: number
  status: AttachmentStatus
  // 服务端返回的附件 id（status=done 时才有）。
  id?: string
  // 上传进度 0–1（拿不到进度时为 undefined，界面显示不确定态）。
  progress?: number
  error?: string
  // 待上传的字节（压缩后），重试时直接用。
  blob?: Blob
  // 本地预览的 objectURL，删除 / 清空 / 卸载时要 revoke。
  previewURL?: string
}

export interface CompressionPlan {
  compress: boolean
  // 压缩后的长边上限（不压缩时为 0）。
  maxSide: number
  quality: number
  // 输出类型：压缩统一转 JPEG（canvas 丢 alpha，手机上以白底合成）。
  mime: string
}

export interface CompressionInput {
  mime: string
  bytes: number
  width: number
  height: number
  // 当前视口宽度（取不到时给 0 = 桌面口径）。
  viewportWidth: number
}

export function isSupportedAttachmentMime(mime: string): boolean {
  return (SUPPORTED_ATTACHMENT_MIMES as readonly string[]).includes(mime)
}

// attachmentTypeError：返回空串表示可以上传；否则是给用户看的一句话。
export function attachmentTypeError(mime: string): string {
  if (isSupportedAttachmentMime(mime)) return ''
  return t('util.attachments.typeError')
}

export function attachmentSizeError(bytes: number, limit = MAX_ATTACHMENT_BYTES): string {
  if (bytes <= limit) return ''
  return t('util.attachments.sizeError', { max: formatBytes(limit), size: formatBytes(bytes) })
}

// attachmentCountError：current 是已选张数（含正在上传的），incoming 是这次还要加几张。
export function attachmentCountError(current: number, incoming = 1): string {
  const next = current + Math.max(1, incoming)
  if (current < MAX_ATTACHMENTS_PER_MESSAGE && next <= MAX_ATTACHMENTS_PER_MESSAGE) return ''
  return t('util.attachments.countError', { max: MAX_ATTACHMENTS_PER_MESSAGE })
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0B'
  if (bytes < 1024) return `${Math.round(bytes)}B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)}KB`
  return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)}MB`
}

// planCompression 选择压缩参数（决策 4）：手机端长边 > 2048 或体积 > 1MB 才压，
// 桌面默认原图、超过 10MB 才压；GIF 不压（会丢动画）；服务端不认的类型不上传。
export function planCompression(input: CompressionInput): CompressionPlan {
  const keep: CompressionPlan = { compress: false, maxSide: 0, quality: 1, mime: input.mime }
  if (!isSupportedAttachmentMime(input.mime) || input.mime === UNCOMPRESSED_MIME) return keep
  // 手机档断点跟 utils/mobileLayout.ts 共用一处（宽 ≤ 900px）。
  if (isMobileWidth(input.viewportWidth)) {
    const longSide = Math.max(input.width, input.height)
    const tooBig = longSide > MOBILE_COMPRESS_MAX_SIDE || input.bytes > MOBILE_COMPRESS_MIN_BYTES
    if (!tooBig) return keep
    return { compress: true, maxSide: MOBILE_MAX_SIDE, quality: MOBILE_JPEG_QUALITY, mime: 'image/jpeg' }
  }
  if (input.bytes <= DESKTOP_COMPRESS_MIN_BYTES) return keep
  return { compress: true, maxSide: MOBILE_MAX_SIDE, quality: MOBILE_JPEG_QUALITY, mime: 'image/jpeg' }
}

// attachmentCapability：thread 的能力位 → 📎 的可用性。字段缺失（老服务端）按「不支持」处理。
export function attachmentCapability(thread?: AttachmentCapabilitySource | null): AttachmentCapability {
  const mode = thread?.attachments
  if (mode === 'inline' || mode === 'file' || mode === 'arg') {
    // 送达方式对用户没有差别（inline 随消息、file 写路径、arg 作为命令行参数）：都能附图，
    // 具体怎么送由服务端决定。
    return { mode, enabled: true, reason: '' }
  }
  if (mode === 'none') {
    return { mode, enabled: false, reason: thread?.attachments_reason?.trim() || unsupportedRecipientReason() }
  }
  return { mode: 'none', enabled: false, reason: unsupportedServerReason() }
}

export interface ComposerAttachmentState {
  // 📎 按钮可用（能力位 none / 缺失时置灰）。
  attachEnabled: boolean
  attachTitle: string
  uploading: boolean
  failedCount: number
  // 可以随消息发出的附件 id（顺序即发送顺序）。
  readyIDs: string[]
  // 附件这边是否放行发送：上传中或还有失败项时为 false。
  canSend: boolean
  // 输入框下方的一句话（空 = 不显示）。
  hint: string
}

// composerAttachmentState：附件条 → 发送按钮的门禁与提示。
export function composerAttachmentState(
  items: readonly AttachmentDraft[],
  capability: AttachmentCapability,
): ComposerAttachmentState {
  const uploading = items.some((item) => item.status === 'uploading')
  const failedCount = items.filter((item) => item.status === 'failed').length
  const readyIDs = items.filter((item) => item.status === 'done' && !!item.id).map((item) => item.id as string)
  let hint = ''
  if (uploading) {
    hint = t('util.attachments.uploading')
  } else if (failedCount > 0) {
    hint = t('util.attachments.failedCount', { n: failedCount })
  } else if (!capability.enabled) {
    hint = capability.reason
  }
  return {
    attachEnabled: capability.enabled,
    attachTitle: capability.enabled ? t('util.attachments.attachTitle') : capability.reason,
    uploading,
    failedCount,
    readyIDs,
    canSend: !uploading && failedCount === 0,
    hint,
  }
}

// attachHintNote：输入框下方那一行小字要不要显示。📎 的 title 已经是「为什么不可用」的
// 同一句话，再在下面重复一遍是噪音（会话抽屉曾同时显示两处）；上传中 / 上传失败这类
// 会变化的提示没有别的出口，照旧显示。
export function attachHintNote(hint: string, reason: string): string {
  if (!hint) return ''
  return reason && hint === reason ? '' : hint
}
