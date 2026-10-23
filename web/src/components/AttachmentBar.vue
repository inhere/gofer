<script setup lang="ts">
// AttachmentBar：工作台输入框共用的附图条（gofer-ha0x）。
//  - 三种添加方式：粘贴（Ctrl+V / 手机上长按粘贴）、拖放到输入区、📎 选文件
//    （accept="image/*"，手机上系统会给「拍照 / 相册」）；
//  - 选中即上传（POST /v1/attachments），条上显示进度、失败可重试、可删除（未发送的调
//    DELETE）；类型 / 大小 / 数量不合格就地提示，不上传；
//  - 压缩参数由 utils/attachments.ts 的 planCompression 决定，这里只用 canvas 执行。
// 状态自己持有（父组件通过模板 ref 取 readyIDs() / canSend() / hint()，发送成功后调 clear()）。
import { computed, onBeforeUnmount, ref } from 'vue'
import { deleteAttachment, uploadAttachment } from '../api/attachments'
import {
  attachmentCountError,
  attachmentSizeError,
  attachmentTypeError,
  composerAttachmentState,
  formatBytes,
  planCompression,
  type AttachmentCapability,
  type AttachmentDraft,
  type CompressionPlan,
} from '../utils/attachments'
import { t } from '../i18n'

const props = defineProps<{ capability: AttachmentCapability }>()
const emit = defineEmits<{ (e: 'error', message: string): void }>()

const items = ref<AttachmentDraft[]>([])
const fileInput = ref<HTMLInputElement | null>(null)
const dragActive = ref(false)
let keySeq = 0

// 发送门禁与提示都来自纯函数，两处输入框口径一致。
const state = computed(() => composerAttachmentState(items.value, props.capability))
const hintText = computed(() => state.value.hint)

function fail(message: string): void {
  emit('error', message)
}

function patch(key: string, changes: Partial<AttachmentDraft>): void {
  items.value = items.value.map((item) => (item.key === key ? { ...item, ...changes } : item))
}

function findItem(key: string): AttachmentDraft | undefined {
  return items.value.find((item) => item.key === key)
}

function viewportWidth(): number {
  return typeof window === 'undefined' ? 0 : window.innerWidth || 0
}

// addFiles 是所有添加方式的入口：先按类型 / 大小 / 数量过滤并就地提示，合格的立即上传。
function addFiles(files: readonly File[]): void {
  if (!props.capability.enabled) {
    fail(props.capability.reason)
    return
  }
  for (const file of files) {
    const typeError = attachmentTypeError(file.type)
    if (typeError) {
      fail(t('util.attachBar.fileType', { name: file.name || t('util.attachBar.thisImage'), err: typeError }))
      continue
    }
    const sizeError = attachmentSizeError(file.size)
    if (sizeError) {
      fail(t('util.attachBar.fileSize', { name: file.name || t('util.attachBar.thisImage'), err: sizeError }))
      continue
    }
    const countError = attachmentCountError(items.value.length, 1)
    if (countError) {
      fail(t('util.attachBar.countExtra', { err: countError, name: file.name || t('util.attachBar.unnamed') }))
      continue
    }
    const draft: AttachmentDraft = {
      key: `att-${Date.now()}-${keySeq++}`,
      name: file.name || 'image',
      mime: file.type,
      bytes: file.size,
      status: 'uploading',
      previewURL: previewURL(file),
    }
    items.value = [...items.value, draft]
    void ingest(draft.key, file)
  }
}

function previewURL(file: Blob): string {
  try {
    return URL.createObjectURL(file)
  } catch {
    return ''
  }
}

function revokePreview(item: AttachmentDraft | undefined): void {
  if (item?.previewURL) URL.revokeObjectURL(item.previewURL)
}

// ingest 按 planCompression 的结论决定要不要 canvas 压一遍，然后上传。
async function ingest(key: string, original: File): Promise<void> {
  const draft = findItem(key)
  if (!draft) return
  try {
    const size = await measureImage(original)
    const plan = planCompression({
      mime: draft.mime,
      bytes: draft.bytes,
      width: size.width,
      height: size.height,
      viewportWidth: viewportWidth(),
    })
    let blob: Blob = original
    if (plan.compress) {
      const compressed = await compressImage(original, plan)
      if (compressed) blob = compressed
    }
    patch(key, { blob, bytes: blob.size, mime: blob.type || draft.mime })
    await upload(key)
  } catch (e) {
    patch(key, { status: 'failed', error: e instanceof Error ? e.message : String(e) })
  }
}

async function upload(key: string): Promise<void> {
  const draft = findItem(key)
  if (!draft || !draft.blob) return
  patch(key, { status: 'uploading', progress: 0, error: undefined })
  try {
    const info = await uploadAttachment(draft.blob, draft.name, (ratio) => {
      if (findItem(key)?.status === 'uploading') patch(key, { progress: ratio })
    })
    patch(key, { status: 'done', id: info.id, bytes: info.bytes, mime: info.mime, progress: 1 })
  } catch (e) {
    patch(key, { status: 'failed', error: e instanceof Error ? e.message : String(e) })
  }
}

function retry(item: AttachmentDraft): void {
  if (item.status === 'failed') void upload(item.key)
}

function remove(item: AttachmentDraft): void {
  items.value = items.value.filter((candidate) => candidate.key !== item.key)
  revokePreview(item)
  // 未发送的附件顺手删掉；已发送的（或服务端已清理的）忽略错误。
  if (item.id) void deleteAttachment(item.id).catch(() => {})
}

function clear(): void {
  for (const item of items.value) revokePreview(item)
  items.value = []
}

function readyIDs(): string[] {
  return state.value.readyIDs
}

function canSend(): boolean {
  return state.value.canSend
}

function openPicker(): void {
  fileInput.value?.click()
}

function onFileInputChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const picked = Array.from(input.files ?? [])
  addFiles(picked)
  // 允许连续两次选同一张：清掉 input 的值。
  input.value = ''
}

// 从剪贴板里挑图片（手机长按 / 桌面 Ctrl+V 截图）。
function imagesFromClipboard(event: ClipboardEvent): File[] {
  const files = Array.from(event.clipboardData?.files ?? [])
  return files.filter((file) => isImageMime(file.type))
}

function addFromPaste(event: ClipboardEvent): void {
  const files = imagesFromClipboard(event)
  if (files.length === 0) return
  event.preventDefault()
  addFiles(files)
}

function addFromDrop(event: DragEvent): void {
  dragActive.value = false
  const files = Array.from(event.dataTransfer?.files ?? []).filter((file) => isImageMime(file.type))
  if (files.length === 0) return
  addFiles(files)
}

// 拖到「整个输入区」（不只是缩略图条）也收：父组件的容器上挂这两个处理函数。
// 不拖文件时不拦默认行为（拖文字进输入框照旧可用）。
function carriesFiles(event: DragEvent): boolean {
  const types = event.dataTransfer?.types
  return !!types && Array.from(types).includes('Files')
}

function handleDragOver(event: DragEvent): void {
  if (!carriesFiles(event)) return
  event.preventDefault()
  dragActive.value = true
}

function handleDrop(event: DragEvent): void {
  if (!carriesFiles(event)) return
  event.preventDefault()
  addFromDrop(event)
}

function isImageMime(mime: string): boolean {
  return mime.startsWith('image/')
}

function progressWidth(item: AttachmentDraft): string {
  if (item.progress == null) return '30%'
  return `${Math.max(4, Math.round(item.progress * 100))}%`
}

function statusText(item: AttachmentDraft): string {
  switch (item.status) {
    case 'uploading':
      return item.progress != null && item.progress > 0
        ? t('util.attachBar.uploadingPct', { pct: Math.round(item.progress * 100) })
        : t('util.attachBar.uploading')
    case 'failed':
      return item.error ? t('util.attachBar.failedWith', { err: item.error }) : t('util.attachBar.uploadFailed')
    default:
      return formatBytes(item.bytes)
  }
}

function onDragLeave(event: DragEvent): void {
  // 拖到子元素上也会触发 dragleave：只有真的离开整个条才收掉高亮。
  if (event.relatedTarget && (event.currentTarget as HTMLElement).contains(event.relatedTarget as Node)) return
  dragActive.value = false
}

onBeforeUnmount(clear)

// measureImage 先取原始尺寸：压缩参数（长边阈值）靠它判断。取不到就返回 0，planCompression
// 会按体积判断。
async function measureImage(blob: Blob): Promise<{ width: number; height: number }> {
  try {
    const image = await loadImage(blob)
    return { width: image.naturalWidth, height: image.naturalHeight }
  } catch {
    return { width: 0, height: 0 }
  }
}

function loadImage(blob: Blob): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(blob)
    const image = new Image()
    image.onload = () => {
      URL.revokeObjectURL(url)
      resolve(image)
    }
    image.onerror = () => {
      URL.revokeObjectURL(url)
      reject(new Error(t('util.attachBar.readImageFailed')))
    }
    image.src = url
  })
}

// compressImage 按 plan 用 canvas 缩一遍（JPEG 无 alpha，先向白底合成）。失败就返回 null，
// 退回上传原图。
async function compressImage(blob: Blob, plan: CompressionPlan): Promise<Blob | null> {
  try {
    const image = await loadImage(blob)
    const longSide = Math.max(image.naturalWidth, image.naturalHeight)
    const scale = longSide > plan.maxSide && longSide > 0 ? plan.maxSide / longSide : 1
    const canvas = document.createElement('canvas')
    canvas.width = Math.max(1, Math.round(image.naturalWidth * scale))
    canvas.height = Math.max(1, Math.round(image.naturalHeight * scale))
    const ctx = canvas.getContext('2d')
    if (!ctx) return null
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, canvas.width, canvas.height)
    ctx.drawImage(image, 0, 0, canvas.width, canvas.height)
    return await new Promise<Blob | null>((resolve) => {
      canvas.toBlob((out) => resolve(out), plan.mime, plan.quality)
    })
  } catch {
    return null
  }
}

defineExpose({
  addFiles,
  addFromPaste,
  handleDragOver,
  handleDrop,
  clear,
  readyIDs,
  canSend,
  // hint 以函数形式暴露：父组件在 computed 里调它就能跟着附件条变化刷新。
  hint: () => hintText.value,
  uploading: () => state.value.uploading,
  openPicker,
  items,
})
</script>

<template>
  <div
    class="attach"
    :class="{ 'attach--drag': dragActive }"
    @dragenter.prevent="dragActive = true"
    @dragover.prevent="dragActive = true"
    @dragleave="onDragLeave"
    @drop.prevent="addFromDrop($event)"
  >
    <input
      ref="fileInput"
      class="attach-file"
      type="file"
      accept="image/*"
      multiple
      :disabled="!capability.enabled"
      data-test="attach-input"
      @change="onFileInputChange"
    />
    <button
      class="attach-add mono"
      type="button"
      :disabled="!capability.enabled"
      :title="capability.enabled ? t('util.attachBar.addTitle') : capability.reason"
      :aria-label="capability.enabled ? t('util.attachBar.add') : t('util.attachBar.cannotAdd', { reason: capability.reason })"
      data-test="attach-button"
      @click="openPicker"
    >📎</button>
    <ul v-if="items.length > 0" class="attach-strip" :aria-label="t('util.attachBar.pendingAria')">
      <li
        v-for="item in items"
        :key="item.key"
        class="attach-item"
        :class="`attach-item--${item.status}`"
        :title="statusText(item)"
      >
        <img v-if="item.previewURL" class="attach-thumb" :src="item.previewURL" :alt="item.name" />
        <span v-else class="attach-thumb attach-thumb--empty mono">IMG</span>
        <span v-if="item.status === 'uploading'" class="attach-progress" :style="{ width: progressWidth(item) }"></span>
        <button
          v-if="item.status === 'failed'"
          class="attach-retry mono"
          type="button"
          :title="t('util.attachBar.retryTitle')"
          @click="retry(item)"
        >{{ t('util.attachBar.retry') }}</button>
        <button
          class="attach-remove mono"
          type="button"
          :aria-label="t('util.attachBar.removeAria', { name: item.name })"
          :title="t('util.attachBar.removeTitle')"
          @click="remove(item)"
        >×</button>
      </li>
    </ul>
    <span v-if="hintText" class="attach-hint mono" data-test="attach-hint">{{ hintText }}</span>
  </div>
</template>

<style scoped>
.attach { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 0; padding: 2px 0; border: 1px dashed transparent; border-radius: var(--r-chip); }
.attach--drag { border-color: var(--accent); background: rgba(120, 220, 160, .06); }
.attach-file { display: none; }
.attach-add { flex: none; color: var(--ink); background: var(--bg); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 4px 8px; font-size: 14px; line-height: 1; cursor: pointer; }
.attach-add:disabled { opacity: .4; cursor: default; }
.attach-strip { display: flex; flex-wrap: wrap; gap: 6px; margin: 0; padding: 0; list-style: none; min-width: 0; }
.attach-item { position: relative; width: 54px; height: 54px; border: 1px solid var(--line); border-radius: var(--r-chip); overflow: hidden; background: var(--bg); }
.attach-item--failed { border-color: var(--fail); }
.attach-item--done { border-color: var(--done); }
.attach-thumb { display: block; width: 100%; height: 100%; object-fit: cover; }
.attach-thumb--empty { display: grid; place-items: center; color: var(--ink-3); font-size: 10px; }
.attach-progress { position: absolute; left: 0; bottom: 0; height: 3px; background: var(--accent); transition: width .15s linear; }
.attach-retry { position: absolute; left: 0; right: 0; bottom: 0; padding: 2px 0; color: var(--bg); background: var(--fail); border: 0; font-size: 10px; cursor: pointer; }
.attach-remove { position: absolute; top: 0; right: 0; width: 18px; height: 18px; padding: 0; color: var(--ink); background: rgba(0, 0, 0, .6); border: 0; border-bottom-left-radius: var(--r-chip); font-size: 12px; line-height: 1; cursor: pointer; }
.attach-hint { color: var(--ink-3); font-size: 11px; }
</style>
