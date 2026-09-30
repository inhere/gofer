<script setup lang="ts">
// 终端式日志带（stdout | stderr），等宽 mono、深底。
//  - stdout = agent 的最终答复（ndjson agent 也如此，过程事件在 stderr）；
//  - 自动滚底：有新内容滚到底；
//  - 用户上滚 -> 暂停自动滚动 + 显示「N 行新」提示，点击/回到底部恢复；
//  - stdout/stderr 用 tab 切换；stderr 首次出现内容时自动聚焦；
//  - 基础 ANSI SGR 颜色渲染（先 escape，再包 span）。
//  - prefers-reduced-motion -> 关闭平滑滚动惯性（即时 scrollTop）。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import NdjsonTimeline from './NdjsonTimeline.vue'
import type { LogStream } from '../api/types'
import { MAX_DOM_LINES, countLogLines, createIncrementalAnsiRenderer } from '../utils/logRender'
import { defaultLogStream } from '../utils/logStream'

const props = withDefaults(defineProps<{
  stdout: string
  stderr: string
  stdoutLabel?: string
  // 是否运行中：底部 live 脉冲
  live?: boolean
  mode?: 'live' | 'paged'
  stdoutTotal?: number
  stderrTotal?: number
  stdoutCanLoadEarlier?: boolean
  stderrCanLoadEarlier?: boolean
  stdoutLoading?: boolean
  stderrLoading?: boolean
  focused?: boolean
  // stderr 第一次出现内容时自动切过去（job 详情页的默认行为）；工作台关掉，始终先看 stdout。
  autoStderr?: boolean
  stdoutAppend?: { seq: number; text: string } | null
  stderrAppend?: { seq: number; text: string } | null
  stdoutReset?: number
  stderrReset?: number
  stdoutLines?: number
  stderrLines?: number
}>(), { focused: true, autoStderr: true })

const emit = defineEmits<{
  (e: 'load-earlier', stream: LogStream): void
  (e: 'load-all', stream: LogStream): void
}>()

const smoothScroll = !window.matchMedia('(prefers-reduced-motion: reduce)').matches

const outEl = ref<HTMLElement | null>(null)
const errEl = ref<HTMLElement | null>(null)
const stdoutPre = ref<HTMLElement | null>(null)
const stderrPre = ref<HTMLElement | null>(null)
const activeStream = ref<'stdout' | 'stderr'>('stdout')
const userTouchedTabs = ref(false)
const outPinned = ref(true)
const errPinned = ref(true)
const outNew = ref(0)
const errNew = ref(0)
const stdoutMarkdownMode = ref(false)
// 结构化视图开关（bd h-aii-rpky / bd h-aii-525u）：ndjson agent 的**过程事件**在 stderr
// （stdout 只有最终答复文本），逐行解析成时间线。
const structuredMode = ref(false)
let outPrev = 0
let errPrev = 0

type RenderState = { classes: string[]; partialLine: string; hasPartial: boolean; renderer: ReturnType<typeof createIncrementalAnsiRenderer> }
const renderState: Record<'stdout' | 'stderr', RenderState> = {
  stdout: { classes: [], partialLine: '', hasPartial: false, renderer: createIncrementalAnsiRenderer() },
  stderr: { classes: [], partialLine: '', hasPartial: false, renderer: createIncrementalAnsiRenderer() },
}

function streamPre(stream: 'stdout' | 'stderr'): HTMLElement | null {
  return stream === 'stdout' ? stdoutPre.value : stderrPre.value
}

function renderStream(stream: 'stdout' | 'stderr', text: string, force = false): void {
  const state = renderState[stream]
  const pre = streamPre(stream)
  if (!pre || !force) return
  state.classes = []
  state.partialLine = ''
  state.hasPartial = false
  pre.innerHTML = ''
  const rendered = state.renderer.reset(text)
  pre.insertAdjacentHTML('beforeend', rendered.html)
  if (rendered.partialLine) {
    pre.insertAdjacentHTML('beforeend', `<span class="log-line">${rendered.partialLine}</span>`)
    state.hasPartial = true
  }
  state.classes = rendered.classes
  state.partialLine = rendered.partialLine
  while (pre.children.length > MAX_DOM_LINES) pre.firstElementChild?.remove()
  if (pre && !text && !pre.innerHTML) pre.textContent = `（无 ${stream} 输出）`
}

function appendStream(stream: 'stdout' | 'stderr', text: string): void {
  if (!text || activeStream.value !== stream) return
  const pre = streamPre(stream)
  if (!pre) return
  const state = renderState[stream]
  if (state.hasPartial) pre.lastElementChild?.remove()
  const rendered = state.renderer.append(text)
  pre.insertAdjacentHTML('beforeend', rendered.html)
  if (rendered.partialLine) {
    pre.insertAdjacentHTML('beforeend', `<span class="log-line">${rendered.partialLine}</span>`)
    state.hasPartial = true
  } else {
    state.hasPartial = false
  }
  state.classes = rendered.classes
  state.partialLine = rendered.partialLine
  while (pre.children.length > MAX_DOM_LINES) pre.firstElementChild?.remove()
}

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
  activeStream.value === 'stdout' ? props.stdoutLines ?? countLogLines(props.stdout) : props.stderrLines ?? countLogLines(props.stderr),
)
const showLogActions = computed(() => paged.value && activeCanLoadEarlier.value)
const showStdoutMarkdown = computed(
  () => paged.value && activeStream.value === 'stdout' && props.stdout.length > 200,
)

// looksStructured：某一路日志是否像 ndjson 事件流 —— 前若干行里存在带字符串 type 的
// JSON 对象即可（真流开头必然就有事件行）。只在为真时提供「结构化视图」切换，避免给纯
// 文本 agent 的日志显示一个没意义的开关。
const structuredSampleLines = 200
function looksStructured(text: string): boolean {
  if (text === '') {
    return false
  }
  for (const line of text.split('\n', structuredSampleLines)) {
    const trimmed = line.trim()
    if (!trimmed.startsWith('{')) {
      continue
    }
    try {
      const parsed: unknown = JSON.parse(trimmed)
      if (
        typeof parsed === 'object' &&
        parsed !== null &&
        typeof (parsed as Record<string, unknown>).type === 'string'
      ) {
        return true
      }
    } catch {
      // 非 JSON 行：继续扫描。
    }
  }
  return false
}

const stdoutStructured = computed(() => looksStructured(props.stdout || ''))
const stderrStructured = computed(() => looksStructured(props.stderr || ''))
// 任一路是事件流就给开关（切到另一路时开关不闪没）；应用范围按各路各自的判定。
const hasStructuredLines = computed(() => stdoutStructured.value || stderrStructured.value)

function selectStream(stream: 'stdout' | 'stderr'): void {
  activeStream.value = stream
  userTouchedTabs.value = true
  void nextTick(() => {
    renderStream(stream, stream === 'stdout' ? props.stdout : props.stderr, true)
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

// focusStderr 切到 stderr 页签并滚到它的末尾。job 详情页的「验证」块用它把读者直接带到
// 验证输出（验收命令的横幅与 stdout/stderr 都写在这条流上）。与点页签同一路径，所以
// "用户手动选过流"的标记一并置位（自动跟随不再抢回）。
function focusStderr(): void {
  selectStream('stderr')
}
defineExpose({ focusStderr })

function toggleStdoutMarkdown(): void {
  stdoutMarkdownMode.value = !stdoutMarkdownMode.value
  void nextTick(() => renderStream('stdout', props.stdout, true))
}

function toggleStructured(): void {
  structuredMode.value = !structuredMode.value
  void nextTick(() => {
    renderStream('stdout', props.stdout, true)
    renderStream('stderr', props.stderr, true)
  })
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

function onNewLines(stream: 'stdout' | 'stderr', delta: number): void {
  if (delta <= 0) return
  const pinned = stream === 'stdout' ? outPinned : errPinned
  const el = stream === 'stdout' ? outEl : errEl
  const setNew = stream === 'stdout' ? outNew : errNew
  void nextTick(() => {
    if (!props.focused) {
      setNew.value += delta
      return
    }
    if (pinned.value && el.value) {
      scrollPane(el.value)
      setNew.value = 0
    } else {
      setNew.value += delta
    }
  })
}

watch(() => props.stdoutAppend, (append) => {
  if (!append) return
  appendStream('stdout', append.text)
  const delta = countLogLines(append.text)
  outPrev += delta
  onNewLines('stdout', delta)
})
watch(() => props.stderrAppend, (append) => {
  if (!append) return
  if (props.autoStderr && props.focused && !userTouchedTabs.value && props.stdout.length === 0 && errPrev === 0) activeStream.value = 'stderr'
  appendStream('stderr', append.text)
  const delta = countLogLines(append.text)
  errPrev += delta
  onNewLines('stderr', delta)
})
watch(() => props.stdoutReset, () => {
  renderStream('stdout', props.stdout, true)
  outPrev = props.stdoutLines ?? countLogLines(props.stdout)
})
watch(() => props.stderrReset, () => {
  renderStream('stderr', props.stderr, true)
  errPrev = props.stderrLines ?? countLogLines(props.stderr)
})
watch(() => props.stdoutLines, (total) => {
  if (total !== undefined) outPrev = total
})
watch(() => props.stderrLines, (total) => {
  if (total !== undefined) errPrev = total
})

/*
 * The full text props remain useful for tab counts and reset/pagination, but
 * live rendering is driven only by the append records above. This avoids
 * deriving a delta from a capped 2 MiB buffer whose head may have disappeared.
 */
watch(() => props.stdout, () => {
    void nextTick(() => {
      if (outPinned.value && outEl.value) scrollPane(outEl.value)
    })
})
watch(() => props.stderr, () => {
  void nextTick(() => {
    if (errPinned.value && errEl.value) scrollPane(errEl.value)
  })
})

watch(() => props.focused, (focused) => {
  if (!focused) return
  void nextTick(() => {
    if (outPinned.value && outEl.value) {
      scrollPane(outEl.value)
      outNew.value = 0
    }
    if (errPinned.value && errEl.value) {
      scrollPane(errEl.value)
      errNew.value = 0
    }
  })
})

onMounted(() => {
  outPrev = props.stdoutLines ?? countLogLines(props.stdout)
  errPrev = props.stderrLines ?? countLogLines(props.stderr)
  activeStream.value = defaultLogStream({ autoStderr: props.autoStderr, stdout: props.stdout, stderr: props.stderr })
  void nextTick(() => {
    renderStream('stdout', props.stdout, true)
    renderStream('stderr', props.stderr, true)
  })
  if (!props.focused) return
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
            <span>{{ stdoutLabel ?? 'stdout' }}</span>
            <span class="tab-count">{{ stdoutLines ?? countLogLines(stdout) }}</span>
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
            <span class="tab-count">{{ stderrLines ?? countLogLines(stderr) }}</span>
            <span v-if="errNew > 0" class="tab-new">{{ errNew }}</span>
          </button>
        </div>
        <span v-if="live" class="live-pulse" title="streaming">live</span>
      </div>
      <div v-if="showLogActions || showStdoutMarkdown || hasStructuredLines" class="log-actions">
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
            v-if="hasStructuredLines"
            class="log-action"
            type="button"
            @click="toggleStructured"
          >
            {{ structuredMode ? '原始视图' : '结构化视图' }}
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
        <div v-if="structuredMode && stdoutStructured" class="log-structured">
          <NdjsonTimeline :text="stdout" />
        </div>
        <div
          v-else-if="stdoutMarkdownMode && showStdoutMarkdown"
          class="log-md"
          v-html="stdoutMarkdownHtml"
        ></div>
        <pre v-else ref="stdoutPre" class="log-text"></pre>
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
        <!-- ndjson agent 的过程事件走 stderr（h-aii-525u），结构化视图读这一路。 -->
        <div v-if="structuredMode && stderrStructured" class="log-structured">
          <NdjsonTimeline :text="stderr" />
        </div>
        <pre v-else ref="stderrPre" class="log-text"></pre>
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
.log-text :deep(.log-line) {
  display: block;
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
/* inline code：加发丝边框 + 暖色前景，确保在 --term-bg 终端底（两种主题）都清晰可辨。 */
.log-md :deep(code) {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 3px;
  color: var(--run);
  font-family: var(--font-mono, monospace);
  font-size: 0.92em;
  padding: 0 5px;
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
  border: 0;
  color: inherit;
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

@media (max-width: 767px) {
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
