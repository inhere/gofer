<script setup lang="ts">
// 终端式日志带（stdout | stderr），等宽 mono、深底。
//  - 自动滚底：有新内容滚到底；
//  - 用户上滚 -> 暂停自动滚动 + 显示「N 行新」提示，点击/回到底部恢复；
//  - stdout/stderr 用 tab 切换；stderr 首次出现内容时自动聚焦；
//  - 基础 ANSI SGR 颜色渲染（先 escape，再包 span）。
//  - prefers-reduced-motion -> 关闭平滑滚动惯性（即时 scrollTop）。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import type { LogStream } from '../api/types'

const props = defineProps<{
  stdout: string
  stderr: string
  // 是否运行中：底部 live 脉冲
  live?: boolean
  mode?: 'live' | 'paged'
  stdoutTotal?: number
  stderrTotal?: number
  stdoutCanLoadEarlier?: boolean
  stderrCanLoadEarlier?: boolean
  stdoutLoading?: boolean
  stderrLoading?: boolean
}>()

const emit = defineEmits<{
  (e: 'load-earlier', stream: LogStream): void
  (e: 'load-all', stream: LogStream): void
}>()

const smoothScroll = !window.matchMedia('(prefers-reduced-motion: reduce)').matches

const outEl = ref<HTMLElement | null>(null)
const errEl = ref<HTMLElement | null>(null)
const activeStream = ref<'stdout' | 'stderr'>('stdout')
const userTouchedTabs = ref(false)
const outPinned = ref(true)
const errPinned = ref(true)
const outNew = ref(0)
const errNew = ref(0)
const stdoutMarkdownMode = ref(false)
let outPrev = 0
let errPrev = 0

function lineCount(text: string): number {
  if (!text) {
    return 0
  }
  const t = text.endsWith('\n') ? text.slice(0, -1) : text
  return t.length === 0 ? 0 : t.split('\n').length
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function addClass(classes: string[], next: string): string[] {
  if (classes.includes(next)) {
    return classes
  }
  return [...classes, next]
}

function setFg(classes: string[], next: string): string[] {
  return [...classes.filter((c) => !c.startsWith('ansi-fg-')), next]
}

function applyAnsiCode(classes: string[], code: number): string[] {
  switch (code) {
    case 0:
      return []
    case 1:
      return addClass(classes, 'ansi-bold')
    case 22:
      return classes.filter((c) => c !== 'ansi-bold')
    case 30:
    case 90:
      return setFg(classes, 'ansi-fg-gray')
    case 31:
    case 91:
      return setFg(classes, 'ansi-fg-red')
    case 32:
    case 92:
      return setFg(classes, 'ansi-fg-green')
    case 33:
    case 93:
      return setFg(classes, 'ansi-fg-yellow')
    case 34:
    case 94:
      return setFg(classes, 'ansi-fg-blue')
    case 35:
    case 95:
      return setFg(classes, 'ansi-fg-magenta')
    case 36:
    case 96:
      return setFg(classes, 'ansi-fg-cyan')
    case 37:
    case 97:
      return setFg(classes, 'ansi-fg-white')
    case 39:
      return classes.filter((c) => !c.startsWith('ansi-fg-'))
    default:
      return classes
  }
}

function renderSegment(text: string, classes: string[]): string {
  const safe = escapeHtml(text)
  if (!safe || classes.length === 0) {
    return safe
  }
  return `<span class="${classes.join(' ')}">${safe}</span>`
}

function renderAnsi(text: string): string {
  if (!text) {
    return ''
  }
  const re = /\x1b\[([0-9;]*)m/g
  let pos = 0
  let classes: string[] = []
  let html = ''
  for (const m of text.matchAll(re)) {
    html += renderSegment(text.slice(pos, m.index), classes)
    const raw = m[1] || '0'
    const codes = raw.split(';').map((v) => Number(v || '0'))
    for (const code of codes) {
      classes = applyAnsiCode(classes, code)
    }
    pos = (m.index ?? 0) + m[0].length
  }
  html += renderSegment(text.slice(pos), classes)
  return html
}

const stdoutHtml = computed(() => renderAnsi(props.stdout || '（无 stdout 输出）'))
const stderrHtml = computed(() => renderAnsi(props.stderr || '（无 stderr 输出）'))
const stdoutMarkdownHtml = computed(() =>
  DOMPurify.sanitize(marked.parse(props.stdout, { async: false })),
)
const paged = computed(() => props.mode === 'paged')
const activeCanLoadEarlier = computed(() =>
  activeStream.value === 'stdout'
    ? Boolean(props.stdoutCanLoadEarlier)
    : Boolean(props.stderrCanLoadEarlier),
)
const activeLoading = computed(() =>
  activeStream.value === 'stdout'
    ? Boolean(props.stdoutLoading)
    : Boolean(props.stderrLoading),
)
const activeTotal = computed(() =>
  activeStream.value === 'stdout'
    ? props.stdoutTotal ?? 0
    : props.stderrTotal ?? 0,
)
const activeDisplayed = computed(() =>
  activeStream.value === 'stdout' ? lineCount(props.stdout) : lineCount(props.stderr),
)
const showLogActions = computed(() => paged.value && activeCanLoadEarlier.value)
const showStdoutMarkdown = computed(
  () => paged.value && activeStream.value === 'stdout' && props.stdout.length > 200,
)

function selectStream(stream: 'stdout' | 'stderr'): void {
  activeStream.value = stream
  userTouchedTabs.value = true
  void nextTick(() => {
    if (stream === 'stdout') {
      jumpOut()
    } else {
      jumpErr()
    }
  })
}

function loadEarlier(): void {
  emit('load-earlier', activeStream.value)
}

function loadAll(): void {
  emit('load-all', activeStream.value)
}

function toggleStdoutMarkdown(): void {
  stdoutMarkdownMode.value = !stdoutMarkdownMode.value
}

function scrollPane(el: HTMLElement): void {
  if (smoothScroll && typeof el.scrollTo === 'function') {
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  } else {
    el.scrollTop = el.scrollHeight
  }
}

// 是否贴近底部（容差 24px）
function atBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < 24
}

function jumpOut(): void {
  if (outEl.value) {
    scrollPane(outEl.value)
  }
  outPinned.value = true
  outNew.value = 0
}
function jumpErr(): void {
  if (errEl.value) {
    scrollPane(errEl.value)
  }
  errPinned.value = true
  errNew.value = 0
}

function onScrollOut(): void {
  const el = outEl.value
  if (!el) {
    return
  }
  if (atBottom(el)) {
    outPinned.value = true
    outNew.value = 0
  } else {
    outPinned.value = false
  }
}
function onScrollErr(): void {
  const el = errEl.value
  if (!el) {
    return
  }
  if (atBottom(el)) {
    errPinned.value = true
    errNew.value = 0
  } else {
    errPinned.value = false
  }
}

watch(
  () => props.stdout,
  (v) => {
    const total = lineCount(v)
    const delta = Math.max(0, total - outPrev)
    outPrev = total
    void nextTick(() => {
      if (outPinned.value && outEl.value) {
        scrollPane(outEl.value)
        outNew.value = 0
      } else if (delta > 0) {
        outNew.value += delta
      }
    })
  },
)
watch(
  () => props.stderr,
  (v) => {
    const total = lineCount(v)
    const delta = Math.max(0, total - errPrev)
    if (total > 0 && errPrev === 0 && !userTouchedTabs.value) {
      activeStream.value = 'stderr'
    }
    errPrev = total
    void nextTick(() => {
      if (errPinned.value && errEl.value) {
        scrollPane(errEl.value)
        errNew.value = 0
      } else if (delta > 0) {
        errNew.value += delta
      }
    })
  },
)

onMounted(() => {
  outPrev = lineCount(props.stdout)
  errPrev = lineCount(props.stderr)
  if (errPrev > 0) {
    activeStream.value = 'stderr'
  }
  void nextTick(() => {
    if (outEl.value) {
      scrollPane(outEl.value)
    }
    if (errEl.value) {
      scrollPane(errEl.value)
    }
  })
})
</script>

<template>
  <div class="tape mono">
    <div class="pane">
      <div class="pane-head">
        <div class="tabs" role="tablist" aria-label="日志流">
          <button
            class="tab"
            :class="{ 'tab--active': activeStream === 'stdout' }"
            type="button"
            role="tab"
            :aria-selected="activeStream === 'stdout'"
            @click="selectStream('stdout')"
          >
            <span>stdout</span>
            <span class="tab-count">{{ lineCount(stdout) }}</span>
            <span v-if="outNew > 0" class="tab-new">{{ outNew }}</span>
          </button>
          <button
            class="tab tab--stderr"
            :class="{ 'tab--active': activeStream === 'stderr' }"
            type="button"
            role="tab"
            :aria-selected="activeStream === 'stderr'"
            @click="selectStream('stderr')"
          >
            <span>stderr</span>
            <span class="tab-count">{{ lineCount(stderr) }}</span>
            <span v-if="errNew > 0" class="tab-new">{{ errNew }}</span>
          </button>
        </div>
        <span v-if="live" class="live-pulse" title="streaming">live</span>
      </div>
      <div v-if="showLogActions || showStdoutMarkdown" class="log-actions">
        <span v-if="paged && activeTotal > 0" class="log-scope">
          已显示 {{ activeDisplayed }} / {{ activeTotal }} 行
        </span>
        <div class="log-action-buttons">
          <button
            v-if="showLogActions"
            class="log-action"
            type="button"
            :disabled="activeLoading"
            @click="loadEarlier"
          >
            {{ activeLoading ? '加载中…' : '加载前面200行' }}
          </button>
          <button
            v-if="showLogActions"
            class="log-action"
            type="button"
            :disabled="activeLoading"
            @click="loadAll"
          >
            全部加载
          </button>
          <button
            v-if="showStdoutMarkdown"
            class="log-action"
            type="button"
            @click="toggleStdoutMarkdown"
          >
            {{ stdoutMarkdownMode ? '终端查看' : 'Markdown查看' }}
          </button>
        </div>
      </div>

      <div
        v-show="activeStream === 'stdout'"
        ref="outEl"
        class="pane-body"
        role="tabpanel"
        @scroll="onScrollOut"
      >
        <div
          v-if="stdoutMarkdownMode && showStdoutMarkdown"
          class="log-md"
          v-html="stdoutMarkdownHtml"
        ></div>
        <pre v-else class="log-text" v-html="stdoutHtml"></pre>
      </div>
      <button
        v-if="activeStream === 'stdout' && outNew > 0 && !outPinned"
        class="new-jump mono"
        type="button"
        @click="jumpOut"
      >
        {{ outNew }} 行新 ↓
      </button>

      <div
        v-show="activeStream === 'stderr'"
        ref="errEl"
        class="pane-body pane-body--err"
        role="tabpanel"
        @scroll="onScrollErr"
      >
        <pre class="log-text" v-html="stderrHtml"></pre>
      </div>
      <button
        v-if="activeStream === 'stderr' && errNew > 0 && !errPinned"
        class="new-jump mono"
        type="button"
        @click="jumpErr"
      >
        {{ errNew }} 行新 ↓
      </button>
    </div>
  </div>
</template>

<style scoped>
.tape {
  min-width: 0;
}
.pane {
  position: relative;
  display: flex;
  flex-direction: column;
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--term-bg);
  overflow: hidden;
}
.pane-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 8px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.08em;
}
.tabs {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  color: var(--queue);
  border: 1px solid transparent;
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.08em;
  min-width: 118px;
  text-transform: uppercase;
}
.tab:hover {
  color: var(--paper);
  border-color: var(--line);
}
.tab--active {
  color: var(--phosphor);
  border-color: var(--line);
  background: var(--ink);
}
.tab--stderr.tab--active {
  color: var(--fail);
}
.tab-count {
  color: var(--paper);
  font-size: 10px;
  min-width: 14px;
  text-align: center;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 4px;
  line-height: 14px;
}
.tab-new {
  background: var(--run);
  color: var(--ink);
  border-radius: var(--radius);
  padding: 0 5px;
  line-height: 14px;
  font-size: 10px;
  font-weight: 600;
}
.live-pulse {
  color: var(--run);
  font-size: 10px;
  animation: live-blink 1.2s ease-in-out infinite;
}
.log-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 6px 8px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
}
.log-scope {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--queue);
}
.log-action-buttons {
  display: inline-flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
}
.log-action {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 10px;
  font-size: 11px;
}
.log-action:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.log-action:disabled {
  opacity: 0.5;
  cursor: default;
}
@keyframes live-blink {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}
.pane-body {
  flex: 1;
  overflow: auto;
  padding: 10px;
  height: min(56vh, 680px);
  max-height: 680px;
}
.pane-body--err {
  box-shadow: inset 2px 0 0 rgba(255, 95, 95, 0.24);
}
.log-text {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.45;
  color: var(--paper);
}
.log-text :deep(.ansi-bold) {
  font-weight: 700;
}
.log-text :deep(.ansi-fg-gray) {
  color: var(--queue);
}
.log-text :deep(.ansi-fg-red) {
  color: var(--fail);
}
.log-text :deep(.ansi-fg-green) {
  color: var(--done);
}
.log-text :deep(.ansi-fg-yellow) {
  color: var(--run);
}
.log-text :deep(.ansi-fg-blue) {
  color: #7ab7ff;
}
.log-text :deep(.ansi-fg-magenta) {
  color: #d99aff;
}
.log-text :deep(.ansi-fg-cyan) {
  color: #62d9d3;
}
.log-text :deep(.ansi-fg-white) {
  color: var(--paper);
}
.log-md {
  color: var(--paper);
  font-family: var(--font-sans, system-ui, sans-serif);
  font-size: 13px;
  line-height: 1.55;
  white-space: normal;
}
.log-md :deep(h1),
.log-md :deep(h2),
.log-md :deep(h3),
.log-md :deep(h4) {
  color: var(--paper);
  line-height: 1.3;
  margin: 1em 0 0.5em;
}
.log-md :deep(p),
.log-md :deep(ul),
.log-md :deep(ol) {
  margin: 0.5em 0;
}
.log-md :deep(a) {
  color: var(--phosphor);
}
.log-md :deep(code) {
  background: var(--panel);
  border-radius: 3px;
  font-family: var(--font-mono, monospace);
  font-size: 0.92em;
  padding: 1px 5px;
}
.log-md :deep(pre) {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: auto;
  padding: 10px 12px;
}
.log-md :deep(pre code) {
  background: none;
  padding: 0;
}
.new-jump {
  position: absolute;
  bottom: 12px;
  right: 12px;
  background: var(--run);
  color: var(--ink);
  border: none;
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  font-weight: 600;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.4);
}
.new-jump:hover {
  filter: brightness(1.08);
}
@media (prefers-reduced-motion: reduce) {
  .live-pulse {
    animation: none;
    opacity: 0.9;
  }
}

@media (max-width: 768px) {
  .pane-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .log-actions {
    align-items: flex-start;
    flex-direction: column;
  }
  .log-action-buttons {
    justify-content: flex-start;
  }
  .tabs {
    width: 100%;
  }
  .tab {
    flex: 1;
    justify-content: center;
  }
  .pane-body {
    height: 58vh;
  }
}
</style>
