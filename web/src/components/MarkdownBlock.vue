<script setup lang="ts">
// markdown 渲染块：marked 渲染 → DOMPurify.sanitize → v-html。
// XSS 安全核心：内容是不可信的 agent 产出（产物文件 / job 汇报），绝不裸注入未
// sanitize 的 HTML。FilePreview（.md 产物预览）与验收面板「汇报」共用这一条路径。
// docContext 开启时，消毒后的 DOM 会额外改写项目内 Markdown 链接。
//
// 不设自身高度/滚动：容器（.fp-md / 验收面板）决定，块内只负责排版。
// 溢出（长行 / 代码块 / 宽表格）统一交给全局的 .msg-body（styles/msg.css、issue gofer-ndub）。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { docReaderLocation, isDocPath, resolveDocHref, type DocContext } from '../utils/docPath'
import { t } from '../i18n'

const props = defineProps<{
  text: string
  breaks?: boolean
  externalLinks?: boolean
  docContext?: DocContext
  docDir?: string
}>()

const router = useRouter()
const EXTERNAL = '<a target="_blank" rel="noopener noreferrer" '

function readerHref(context: DocContext, path: string, line?: number, anchor?: string): string {
  const location = docReaderLocation(context, { path, line, anchor }) as { path: string; query?: Record<string, unknown> }
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(location.query ?? {})) {
    if (typeof value === 'string') query.set(key, value)
  }
  return `${location.path}${query.toString() ? `?${query.toString()}` : ''}`
}

function isExternal(href: string): boolean {
  return /^https?:\/\//i.test(href)
}

function rewrite(clean: string): string {
  const context = props.docContext
  if (!context || typeof document === 'undefined') return props.externalLinks ? clean.replace(/<a\s/g, EXTERNAL) : clean
  const template = document.createElement('template')
  template.innerHTML = clean
  const root = template.content
  root.querySelectorAll('img').forEach((img) => {
    const placeholder = document.createTextNode(`${t('docReader.imagePlaceholder')}: ${img.getAttribute('src') ?? ''}`)
    img.replaceWith(placeholder)
  })
  root.querySelectorAll('a').forEach((link) => {
    const href = link.getAttribute('href') ?? ''
    if (isExternal(href)) {
      link.setAttribute('target', '_blank')
      link.setAttribute('rel', 'noopener noreferrer')
      return
    }
    if (href.startsWith('#')) {
      if (props.docDir && context.path) {
        try {
          const anchor = decodeURIComponent(href.slice(1))
          link.setAttribute('href', readerHref(context, context.path, undefined, anchor))
          link.setAttribute('data-doc-link', 'true')
          return
        } catch {
          // Keep malformed anchors as plain text below.
        }
      }
      const span = document.createElement('span')
      span.setAttribute('title', href)
      span.textContent = link.textContent ?? ''
      link.replaceWith(span)
      return
    }
    const base = props.docDir ?? context.base ?? ''
    const resolved = resolveDocHref(base, href)
    if (resolved && isDocPath(resolved)) {
      const lineMark = resolved.match(/:(\d+)$/)
      const withoutLine = lineMark ? resolved.slice(0, -lineMark[0].length) : resolved
      const anchorMark = withoutLine.indexOf('#')
      const path = anchorMark >= 0 ? withoutLine.slice(0, anchorMark) : withoutLine
      const anchor = anchorMark >= 0 ? withoutLine.slice(anchorMark + 1) : undefined
      link.setAttribute('href', readerHref({ projectKey: context.projectKey, worktreeBranch: context.worktreeBranch }, path, lineMark ? Number(lineMark[1]) : undefined, anchor))
      link.setAttribute('data-doc-link', 'true')
    } else {
      const span = document.createElement('span')
      span.setAttribute('title', href)
      span.textContent = link.textContent ?? ''
      link.replaceWith(span)
    }
  })
  root.querySelectorAll('code').forEach((code) => {
    if (code.parentElement?.closest('pre')) return
    const value = code.textContent ?? ''
    if (!isDocPath(value)) return
    const anchor = document.createElement('a')
    anchor.setAttribute('href', readerHref(context, value))
    anchor.setAttribute('data-doc-link', 'true')
    anchor.textContent = value
    code.replaceWith(anchor)
  })
  const container = document.createElement('div')
  container.appendChild(template.content.cloneNode(true))
  return container.innerHTML
}

const html = computed(() => {
  const parsed = marked.parse(props.text, props.breaks ? { async: false, breaks: true } : { async: false })
  return rewrite(DOMPurify.sanitize(parsed))
})

function onClick(event: MouseEvent): void {
  if (!props.docContext || event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return
  const target = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[data-doc-link]') : null
  if (!target) return
  const href = target.getAttribute('href')
  if (!href || !href.startsWith('/projects/')) return
  event.preventDefault()
  void router.push(href)
}
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="md msg-body" v-html="html" @click="onClick"></div>
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
