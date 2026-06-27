<script setup lang="ts">
// 产物 / 关键文件 inline 预览（E19a + P3 关键文件共用）。
// 按 name 后缀 + blob 大小判类型（design D5）：
//  - .md       → marked 渲染 + DOMPurify.sanitize 后 v-html 注入（XSS 安全核心：
//                产物是 agent 生成的不可信内容，绝不裸注入未 sanitize 的 HTML）。
//  - 图片      → <img src=blob>（svg 也走 img，不内联 DOM，杜绝内嵌脚本执行）。
//  - .json     → JSON.parse 后 pretty-print 入 <pre>。
//  - 其他文本  → <pre> 起步（不做语法高亮）。
//  - 超阈值/二进制 → 不渲染，提示并回退下载（emit download）。
// 卸载/blob 变更时 revokeObjectURL，避免 blob URL 泄漏。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

const props = defineProps<{ name: string; blob: Blob }>()
const emit = defineEmits<{ (e: 'download'): void }>()

// 预览大小上限（D5 默认 2MB）：超阈值统一回退下载（含图片，避免巨图卡顿）。
const MAX_PREVIEW_BYTES = 2 * 1024 * 1024

type PreviewKind = 'markdown' | 'image' | 'json' | 'text' | 'fallback'

const kind = ref<PreviewKind>('text')
const html = ref('') // markdown：DOMPurify sanitize 后的 HTML（仅注入 sanitized 内容）
const textBody = ref('') // json 格式化 / 纯文本
const imageUrl = ref('') // 图片 object URL
const fallbackReason = ref('') // 回退原因：文件过大 / 二进制文件 / 无法预览
const loading = ref(true)
const error = ref('')

const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'])

// 当前 object URL（图片预览），按需 revoke。
let objectUrl = ''
function revoke(): void {
  if (objectUrl) {
    URL.revokeObjectURL(objectUrl)
    objectUrl = ''
  }
}

// 取小写后缀（按 basename，去目录前缀）。
function extOf(name: string): string {
  const base = name.split('/').pop() ?? name
  const dot = base.lastIndexOf('.')
  return dot >= 0 ? base.slice(dot + 1).toLowerCase() : ''
}

// 二进制启发式：含 NUL 字节即判为二进制（同后端，避免乱码 / 控制字符注入）。
function looksBinary(text: string): boolean {
  return text.includes('\u0000')
}

async function process(): Promise<void> {
  loading.value = true
  error.value = ''
  html.value = ''
  textBody.value = ''
  imageUrl.value = ''
  fallbackReason.value = ''
  revoke()

  const blob = props.blob
  const ext = extOf(props.name)

  // 超阈值：不渲染，回退下载。
  if (blob.size > MAX_PREVIEW_BYTES) {
    kind.value = 'fallback'
    fallbackReason.value = '文件过大'
    loading.value = false
    return
  }

  try {
    if (IMAGE_EXTS.has(ext)) {
      // svg 也走 <img>（不内联 DOM），杜绝内嵌脚本执行。
      objectUrl = URL.createObjectURL(blob)
      imageUrl.value = objectUrl
      kind.value = 'image'
    } else if (ext === 'md' || ext === 'markdown') {
      const text = await blob.text()
      // XSS 安全核心：marked 渲染后必经 DOMPurify.sanitize 才注入。
      html.value = DOMPurify.sanitize(marked.parse(text, { async: false }))
      kind.value = 'markdown'
    } else if (ext === 'json') {
      const text = await blob.text()
      try {
        textBody.value = JSON.stringify(JSON.parse(text), null, 2)
      } catch {
        // 非法 JSON：原样展示，不丢内容。
        textBody.value = text
      }
      kind.value = 'json'
    } else {
      const text = await blob.text()
      if (looksBinary(text)) {
        kind.value = 'fallback'
        fallbackReason.value = '二进制文件'
      } else {
        textBody.value = text
        kind.value = 'text'
      }
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    kind.value = 'fallback'
    fallbackReason.value = '无法预览'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void process()
})
// blob 变更（同组件复用预览不同文件）时重新处理。
watch(
  () => props.blob,
  () => {
    void process()
  },
)
onUnmounted(revoke)
</script>

<template>
  <div class="fp">
    <p v-if="loading" class="fp-loading mono">加载中…</p>
    <p v-else-if="error" class="fp-error mono">{{ error }}</p>
    <template v-else>
      <!-- markdown：仅注入 DOMPurify sanitize 后的 HTML（绝不裸注入不可信内容）。 -->
      <!-- eslint-disable-next-line vue/no-v-html -->
      <div v-if="kind === 'markdown'" class="fp-md" v-html="html"></div>
      <!-- 图片（svg 也走 img，不内联 DOM）。 -->
      <img v-else-if="kind === 'image'" class="fp-img" :src="imageUrl" :alt="name" />
      <!-- json：格式化 pre。 -->
      <pre v-else-if="kind === 'json'" class="fp-pre mono">{{ textBody }}</pre>
      <!-- 其他文本/代码：pre 起步（不高亮）。 -->
      <pre v-else-if="kind === 'text'" class="fp-pre mono">{{ textBody }}</pre>
      <!-- 过大/二进制/无法预览：回退下载。 -->
      <div v-else class="fp-fallback">
        <p class="fp-fallback-msg mono">{{ fallbackReason }}，无法 inline 预览，请下载查看。</p>
        <button class="fp-dl mono" type="button" @click="emit('download')">下载</button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.fp {
  font-size: 13px;
  color: var(--paper);
}
.fp-loading {
  color: var(--queue);
  font-size: 12px;
}
.fp-error {
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}

/* 文本 / json：等宽可滚 pre。 */
.fp-pre {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--paper);
  white-space: pre;
  overflow: auto;
  max-height: 70vh;
}

/* 图片：限制最大尺寸，居中。 */
.fp-img {
  display: block;
  max-width: 100%;
  max-height: 70vh;
  margin: 0 auto;
  background: var(--term-bg);
}

/* 回退下载块。 */
.fp-fallback {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 10px;
  padding: 16px 0;
}
.fp-fallback-msg {
  margin: 0;
  font-size: 12px;
  color: var(--queue);
}
.fp-dl {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 14px;
  font-size: 12px;
}
.fp-dl:hover {
  background: var(--phosphor);
  color: var(--ink);
}

/* markdown 渲染内容（v-html 注入的是 DOMPurify sanitize 后的 HTML）。
   scoped 不穿透动态 HTML，故用 :deep() 给常见元素补排版。 */
.fp-md {
  font-size: 13px;
  line-height: 1.6;
  color: var(--paper);
  overflow: auto;
  max-height: 70vh;
  word-break: break-word;
}
.fp-md :deep(h1),
.fp-md :deep(h2),
.fp-md :deep(h3),
.fp-md :deep(h4) {
  color: var(--paper);
  margin: 1em 0 0.5em;
  line-height: 1.3;
}
.fp-md :deep(h1) {
  font-size: 1.5em;
}
.fp-md :deep(h2) {
  font-size: 1.3em;
}
.fp-md :deep(h3) {
  font-size: 1.15em;
}
.fp-md :deep(a) {
  color: var(--phosphor);
}
.fp-md :deep(p),
.fp-md :deep(ul),
.fp-md :deep(ol) {
  margin: 0.5em 0;
}
.fp-md :deep(code) {
  font-family: var(--font-mono, monospace);
  font-size: 0.92em;
  background: var(--term-bg);
  padding: 1px 5px;
  border-radius: 3px;
}
.fp-md :deep(pre) {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
  overflow: auto;
}
.fp-md :deep(pre code) {
  background: none;
  padding: 0;
}
.fp-md :deep(blockquote) {
  margin: 0.5em 0;
  padding-left: 12px;
  border-left: 2px solid var(--line);
  color: var(--queue);
}
.fp-md :deep(table) {
  border-collapse: collapse;
  margin: 0.5em 0;
}
.fp-md :deep(th),
.fp-md :deep(td) {
  border: 1px solid var(--line);
  padding: 4px 10px;
  text-align: left;
}
.fp-md :deep(img) {
  max-width: 100%;
}
.fp-md :deep(hr) {
  border: none;
  border-top: 1px solid var(--line);
  margin: 1em 0;
}
</style>
