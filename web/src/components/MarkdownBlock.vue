<script setup lang="ts">
// markdown 渲染块：marked 渲染 → DOMPurify.sanitize → v-html。
// XSS 安全核心：内容是不可信的 agent 产出（产物文件 / job 汇报），绝不裸注入未
// sanitize 的 HTML。FilePreview（.md 产物预览）与验收面板「汇报」共用这一条路径。
//
// 不设自身高度/滚动：容器（.fp-md / 验收面板）决定，块内只负责排版。
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

const props = defineProps<{ text: string }>()

const html = computed(() => DOMPurify.sanitize(marked.parse(props.text, { async: false })))
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="md" v-html="html"></div>
</template>

<style scoped>
/* scoped 不穿透动态 HTML，故用 :deep() 给常见元素补排版。 */
.md {
  font-size: 13px;
  line-height: 1.6;
  color: var(--paper);
  word-break: break-word;
}
.md :deep(h1),
.md :deep(h2),
.md :deep(h3),
.md :deep(h4) {
  color: var(--paper);
  margin: 1em 0 0.5em;
  line-height: 1.3;
}
.md :deep(h1) {
  font-size: 1.5em;
}
.md :deep(h2) {
  font-size: 1.3em;
}
.md :deep(h3) {
  font-size: 1.15em;
}
.md :deep(a) {
  color: var(--phosphor);
}
.md :deep(p),
.md :deep(ul),
.md :deep(ol) {
  margin: 0.5em 0;
}
.md :deep(code) {
  font-family: var(--font-mono, monospace);
  font-size: 0.92em;
  background: var(--term-bg);
  padding: 1px 5px;
  border-radius: 3px;
}
.md :deep(pre) {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
  overflow: auto;
}
.md :deep(pre code) {
  background: none;
  padding: 0;
}
.md :deep(blockquote) {
  margin: 0.5em 0;
  padding-left: 12px;
  border-left: 2px solid var(--line);
  color: var(--queue);
}
.md :deep(table) {
  border-collapse: collapse;
  margin: 0.5em 0;
}
.md :deep(th),
.md :deep(td) {
  border: 1px solid var(--line);
  padding: 4px 10px;
  text-align: left;
}
.md :deep(img) {
  max-width: 100%;
}
.md :deep(hr) {
  border: none;
  border-top: 1px solid var(--line);
  margin: 1em 0;
}
</style>
