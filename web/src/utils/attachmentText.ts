// 附图在消息正文里的痕迹（gofer-ha0x）：file 通道把图片落到接收方机器上，并在正文末尾追加
// `[附图 n] <路径>`（worker 低版本时路径带 `（相对于项目 <key> 根目录）`）。这条消息的
// attachments 已经有缩略图可看，正文里那截路径对人不只是噪音（还可能是执行机上的目录），显示时去掉。
//
// 只认结尾处、格式严格匹配的片段：正文中间偶然出现的类似文字（比如引用一段日志）不动。

const PATH = '[^\\s]+'
const RELATIVE_SUFFIX = '(?:（相对于项目[^）]*根目录）)?'
// 一条片段：`[附图 n] <路径>`，前面紧挨着空白（同一行用空格，多处时服务端用 `\n\n` 分段）；
// 正文只有图片时第一段就在开头。
const SEGMENT = `\\[附图 \\d+\\] ${PATH}${RELATIVE_SUFFIX}`
const TAIL = new RegExp(`(?:^|\\s+)(?:${SEGMENT})(?:\\s+${SEGMENT})*\\s*$`)

/**
 * stripAttachmentMarkers：去掉正文结尾处的附图片段（`[附图 n] <路径>`）。
 * 正文只有这些片段时返回空串；结尾之外的片段原样保留。
 */
export function stripAttachmentMarkers(text: string): string {
  if (!text) return ''
  const out = text.replace(TAIL, '')
  // 没匹配到就原样返回：正文末尾的空白也是用户输入的一部分。
  if (out === text) return text
  return out.replace(/\s+$/, '')
}

/**
 * attachmentBubbleText：气泡正文要不要清理附图片段。
 * 只有这条消息带（非空）attachments 才清：缩略图在气泡下面把图补回来了；
 * 老消息没有 attachments 字段，原样显示（那些路径是正文唯一提到图的地方）。
 */
export function attachmentBubbleText(text: string | undefined, attachments?: readonly unknown[] | null): string {
  const body = text ?? ''
  if (!attachments || attachments.length === 0) return body
  return stripAttachmentMarkers(body)
}
