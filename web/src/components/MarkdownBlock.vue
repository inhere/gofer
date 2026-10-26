<script setup lang="ts">
// markdown 渲染块：marked 渲染 → DOMPurify.sanitize → v-html。
// XSS 安全核心：内容是不可信的 agent 产出（产物文件 / job 汇报），绝不裸注入未
// sanitize 的 HTML。FilePreview（.md 产物预览）与验收面板「汇报」共用这一条路径。
//
// 不设自身高度/滚动：容器（.fp-md / 验收面板）决定，块内只负责排版。
// 溢出（长行 / 代码块 / 宽表格）统一交给全局的 .msg-body（styles/msg.css、issue gofer-ndub）。
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

// 两个可选渲染开关，默认都关——其它用法（产物预览、汇报）行为不变：
//  - breaks：源码里的单个换行渲染成 <br>。标准 markdown 会把它们并成一段，而「一行一条」
//    的字段（如待办备注，--append-note 每次追加一行、服务端用换行连接）需要保住换行。
//  - externalLinks：链接在新标签页打开（备注里的缺陷系统等外部链接会把当前页顶掉）。
//    target / rel 在 sanitize 之后追加：只补两个常量属性，不放松白名单。
const props = defineProps<{ text: string; breaks?: boolean; externalLinks?: boolean }>()

const EXTERNAL = '<a target="_blank" rel="noopener noreferrer" '

const html = computed(() => {
  const parsed = marked.parse(props.text, props.breaks ? { async: false, breaks: true } : { async: false })
  // target / rel 在 sanitize 之后追加：只补两个常量属性，不改白名单。
  const clean = DOMPurify.sanitize(parsed)
  return props.externalLinks ? clean.replace(/<a\s/g, EXTERNAL) : clean
})
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="md msg-body" v-html="html"></div>
</template>

<style scoped>
/* scoped 不穿透动态 HTML，故用 :deep() 给常见元素补排版。
   不撑宽容器的规则（min-width / overflow-wrap / pre 与 table 横滚）在 styles/msg.css 的
   .msg-body 里统一处理，这里只管排版，不再各写一份 word-break。 */
.md {
  font-size: 13px;
  line-height: 1.6;
  color: var(--ink);
}
.md :deep(h1),
.md :deep(h2),
.md :deep(h3),
.md :deep(h4) {
  color: var(--ink);
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
  color: var(--accent);
}
.md :deep(p),
.md :deep(ul),
.md :deep(ol) {
  margin: 0.5em 0;
}
.md :deep(code) {
  font-family: var(--font-mono, monospace);
  font-size: 0.92em;
  background: var(--sunken);
  padding: 1px 5px;
  border-radius: 3px;
}
.md :deep(pre) {
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
  color: var(--ink-3);
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
