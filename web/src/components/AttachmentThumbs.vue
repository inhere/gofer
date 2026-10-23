<script setup lang="ts">
// AttachmentThumbs：历史里一条消息下面的缩略图网格（gofer-ha0x）。
// 图必须带 Authorization 才取得到，所以缩略图走 api/attachments.ts 的 blob URL（挂载时取、
// 卸载时 revoke）；点击取原件在新标签打开；过期（服务端已按保存期删掉）显示占位。
import { onBeforeUnmount, ref, watch } from 'vue'
import { attachmentBlobURL } from '../api/attachments'
import type { AttachmentRef } from '../api/types'

const props = defineProps<{ items: AttachmentRef[] }>()

const thumbs = ref<Record<string, string>>({})
const broken = ref<Record<string, boolean>>({})
const urls: string[] = []

function releaseAll(): void {
  while (urls.length > 0) {
    const url = urls.pop()
    if (url) URL.revokeObjectURL(url)
  }
  thumbs.value = {}
}

async function load(items: readonly AttachmentRef[]): Promise<void> {
  releaseAll()
  for (const item of items) {
    if (item.expired) continue
    try {
      const url = await attachmentBlobURL(item.id, true)
      urls.push(url)
      thumbs.value = { ...thumbs.value, [item.id]: url }
    } catch {
      broken.value = { ...broken.value, [item.id]: true }
    }
  }
}

function sizeText(item: AttachmentRef): string {
  if (!item.width || !item.height) return ''
  return `${item.width}×${item.height}`
}

// openOriginal 取原件（原图）在新标签打开：先同步开窗，避免被弹窗拦截。
async function openOriginal(item: AttachmentRef): Promise<void> {
  if (item.expired) return
  const tab = window.open('', '_blank')
  try {
    const url = await attachmentBlobURL(item.id, false)
    if (!tab) return
    tab.opener = null
    tab.location.href = url
    window.setTimeout(() => URL.revokeObjectURL(url), 60000)
  } catch {
    tab?.close()
    broken.value = { ...broken.value, [item.id]: true }
  }
}

watch(
  () => props.items.map((item) => `${item.id}:${item.expired ? 1 : 0}`).join('|'),
  () => void load(props.items),
  { immediate: true },
)

onBeforeUnmount(releaseAll)
</script>

<template>
  <ul v-if="items.length > 0" class="thumbs" aria-label="图片附件">
    <li v-for="item in items" :key="item.id" class="thumb">
      <span v-if="item.expired" class="thumb-expired mono" title="server 已按保存期删除原图">图片已过期</span>
      <button
        v-else
        class="thumb-button"
        type="button"
        :title="`查看原图${sizeText(item) ? `（${sizeText(item)}）` : ''}`"
        :aria-label="`查看原图 ${item.id}`"
        @click="openOriginal(item)"
      >
        <img v-if="thumbs[item.id]" :src="thumbs[item.id]" alt="图片附件" />
        <span v-else class="thumb-loading mono">{{ broken[item.id] ? '读取失败' : '加载中' }}</span>
      </button>
    </li>
  </ul>
</template>

<style scoped>
.thumbs { display: flex; flex-wrap: wrap; gap: 6px; margin: 8px 0 0; padding: 0; list-style: none; }
.thumb { width: 64px; height: 64px; border: 1px solid var(--line); border-radius: var(--r-chip); overflow: hidden; background: var(--bg); }
.thumb-button { display: block; width: 100%; height: 100%; padding: 0; border: 0; background: transparent; cursor: zoom-in; }
.thumb-button img { display: block; width: 100%; height: 100%; object-fit: cover; }
.thumb-loading, .thumb-expired { display: grid; place-items: center; height: 100%; padding: 2px; color: var(--ink-3); font-size: 10px; text-align: center; }
.thumb-expired { color: var(--run, var(--ink-3)); }
</style>
