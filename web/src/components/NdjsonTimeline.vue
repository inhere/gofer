<script setup lang="ts">
// NDJSON 结构化视图（bd h-aii-rpky / bd h-aii-525u）：把 ndjson agent 的**紧凑事件流**
// 按事件类型渲染成时间线。事件现在是投影器给的（omp/claude 各有内置规则，字段名固定）：
//  - session/system：会话行（id/model/cwd，claude 的 init 带工具数），
//  - tool_execution_start：intent + toolName + args 摘要（可折叠），
//  - tool_execution_end：ok + 结果前 N 行（可折叠，超出即提示还有多少行），
//  - assistant/user/result（claude）：工具调用摘要 / tool_result 摘要 / 终局统计
//    （答复文本本身已进 stdout，不在这里重复），
//  - turn_end：usage / model / provider / stop_reason（不含整条 message），
//  - 识别不了的行 / 解析失败的行：原样显示（绝不吞输出）。
// 老 job 的事件流可能仍在 stdout、字段也更全（如 message_end 带整条 message）：这些形状
// 一并兼容，避免历史日志变成 raw。
// 只读展示：不发起任何请求。
import { computed, ref } from 'vue'

const props = defineProps<{
  text: string
  // 结果/内容默认展示的行数（超出折叠）；默认 8。
  previewLines?: number
}>()

const previewLines = computed(() => props.previewLines ?? 8)
const expanded = ref<Set<number>>(new Set())

function toggle(index: number): void {
  const next = new Set(expanded.value)
  if (next.has(index)) {
    next.delete(index)
  } else {
    next.add(index)
  }
  expanded.value = next
}

// isRecord 把 JSON.parse 的结果收窄成可索引对象。
function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function asString(v: unknown): string {
  if (typeof v === 'string') {
    return v
  }
  if (v === undefined || v === null) {
    return ''
  }
  return JSON.stringify(v)
}

// textFromContent 从 message.content 里取出可读文本：content 可能是字符串、
// 段落数组（{type:'text',text}）或任意其它结构。
function textFromContent(content: unknown): string {
  if (typeof content === 'string') {
    return content
  }
  if (Array.isArray(content)) {
    return content
      .map((part) => (isRecord(part) ? asString(part.text ?? part.content ?? '') : asString(part)))
      .filter((s) => s !== '')
      .join('\n')
  }
  return asString(content)
}

function assistantText(obj: Record<string, unknown>): string {
  const message = isRecord(obj.message) ? obj.message : undefined
  if (message) {
    const text = textFromContent(message.content)
    if (text !== '') {
      return text
    }
  }
  if (typeof obj.text === 'string') {
    return obj.text
  }
  return textFromContent(obj.content)
}

function toolCommand(obj: Record<string, unknown>): string {
  // 投影器给的是摘要字符串（h-aii-525u）；老日志里 args 还是对象。
  if (typeof obj.args === 'string') {
    return obj.args
  }
  const args = isRecord(obj.args) ? obj.args : undefined
  if (args) {
    if (typeof args.command === 'string') {
      return args.command
    }
    const cmd = args.cmd ?? args.script
    if (typeof cmd === 'string') {
      return cmd
    }
    return JSON.stringify(args)
  }
  return ''
}

function toolResultText(obj: Record<string, unknown>): string {
  const result = obj.result ?? obj.output ?? obj.partialOutput
  if (typeof result === 'string') {
    return result
  }
  if (isRecord(result)) {
    const inner = result.stdout ?? result.output ?? result.content
    if (inner !== undefined) {
      return asString(inner)
    }
    return JSON.stringify(result)
  }
  return asString(result)
}

function usageText(obj: Record<string, unknown>): string {
  const usage = isRecord(obj.usage) ? obj.usage : undefined
  const parts: string[] = []
  if (usage) {
    const inTok = usage.input_tokens ?? usage.prompt_tokens
    const outTok = usage.output_tokens ?? usage.completion_tokens
    const total = usage.total_tokens
    if (typeof inTok === 'number') {
      parts.push(`in ${inTok}`)
    }
    if (typeof outTok === 'number') {
      parts.push(`out ${outTok}`)
    }
    if (typeof total === 'number') {
      parts.push(`total ${total}`)
    }
  }
  const model = obj.model ?? (isRecord(obj.message) ? obj.message.model : undefined)
  if (typeof model === 'string' && model !== '') {
    parts.push(model)
  }
  if (typeof obj.provider === 'string' && obj.provider !== '') {
    parts.push(obj.provider)
  }
  if (typeof obj.stop_reason === 'string' && obj.stop_reason !== '') {
    parts.push(`stop ${obj.stop_reason}`)
  }
  const cost = obj.cost_usd ?? obj.total_cost_usd
  if (typeof cost === 'number') {
    parts.push(`$${cost}`)
  }
  if (typeof obj.duration_ms === 'number') {
    parts.push(`${obj.duration_ms}ms`)
  }
  if (typeof obj.num_turns === 'number') {
    parts.push(`${obj.num_turns} turns`)
  }
  return parts.join(' · ')
}

// toolLines 把投影器给的工具调用摘要渲染成每行一个：`name: input`。
function toolLines(tools: unknown): string {
  if (!Array.isArray(tools)) {
    return ''
  }
  return tools
    .map((t) => {
      if (!isRecord(t)) {
        return asString(t)
      }
      const name = asString(t.name ?? 'tool')
      const input = asString(t.input ?? '')
      return input === '' ? name : `${name}: ${input}`
    })
    .filter((line) => line !== '')
    .join('\n')
}

type ItemKind = 'session' | 'tool-start' | 'tool-end' | 'message' | 'turn' | 'raw'

interface TimelineItem {
  index: number
  kind: ItemKind
  label: string
  title: string
  body: string
  meta: string
  raw: string
  foldable: boolean
}

function classify(line: string, index: number): TimelineItem {
  const base: TimelineItem = {
    index,
    kind: 'raw',
    label: 'raw',
    title: '',
    body: '',
    meta: '',
    raw: line,
    foldable: false,
  }
  if (!line.trim().startsWith('{')) {
    return base
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(line)
  } catch {
    return base
  }
  if (!isRecord(parsed)) {
    return base
  }
  const type = typeof parsed.type === 'string' ? parsed.type : ''
  switch (type) {
    case 'session': {
      const id = asString(parsed.id ?? parsed.session_id)
      const parts = [asString(parsed.model), asString(parsed.cwd)].filter((s) => s !== '')
      return { ...base, kind: 'session', label: 'session', title: id, meta: parts.join(' · ') }
    }
    // claude 的 init 行：会话 id + 模型/cwd + 工具数。
    case 'system': {
      const parts = [asString(parsed.subtype), asString(parsed.model), asString(parsed.cwd)].filter(
        (s) => s !== '',
      )
      if (typeof parsed.tool_count === 'number') {
        parts.push(`${parsed.tool_count} tools`)
      }
      return {
        ...base,
        kind: 'session',
        label: 'init',
        title: asString(parsed.session_id),
        meta: parts.join(' · '),
      }
    }
    case 'tool_execution_start':
    case 'tool_execution_call':
      return {
        ...base,
        kind: 'tool-start',
        label: 'tool',
        title: asString(parsed.intent ?? parsed.title ?? ''),
        body: toolCommand(parsed),
        meta: asString(parsed.toolName ?? parsed.tool ?? ''),
      }
    case 'tool_execution_end': {
      // 投影器写 ok（h-aii-525u）；老日志用 isError。
      const isError =
        parsed.ok === false ||
        parsed.isError === true ||
        (isRecord(parsed.result) && parsed.result.isError === true)
      return {
        ...base,
        kind: 'tool-end',
        label: isError ? 'tool ✗' : 'tool ✓',
        title: asString(parsed.toolName ?? ''),
        body: toolResultText(parsed),
        meta: '',
        foldable: true,
      }
    }
    // claude 的 user 行 = tool_result 回填：只留摘要。
    case 'user': {
      return {
        ...base,
        kind: 'tool-end',
        label: 'tool result',
        body: asString(parsed.tool_result ?? assistantText(parsed)),
        foldable: true,
      }
    }
    // claude 的 assistant 行：工具调用摘要 + 文本长度（文本已在 stdout）。
    case 'assistant': {
      const textLen = typeof parsed.text_len === 'number' ? parsed.text_len : 0
      return {
        ...base,
        kind: 'message',
        label: type,
        body: toolLines(parsed.tools),
        meta: textLen > 0 ? `文本 ${textLen} 字（已进 stdout）` : '',
        foldable: true,
      }
    }
    case 'message_end':
    case 'result': {
      const body = assistantText(parsed)
      return { ...base, kind: 'message', label: type, body, meta: usageText(parsed), foldable: true }
    }
    case 'turn_end':
      return { ...base, kind: 'turn', label: 'turn', title: asString(parsed.turn ?? ''), meta: usageText(parsed) }
    default:
      return { ...base, label: type === '' ? 'raw' : type }
  }
}

// items 把 stdout 逐行分类；无法识别/解析的行落到 raw，原样展示。
const items = computed<TimelineItem[]>(() => {
  const text = props.text ?? ''
  if (text === '') {
    return []
  }
  const lines = (text.endsWith('\n') ? text.slice(0, -1) : text).split('\n')
  return lines.map((line, i) => classify(line, i))
})

// looksStructured 决定是否值得显示「结构化 / 原始」切换：至少一行是带 type 的 JSON 对象。
const looksStructured = computed(() =>
  items.value.some((it) => it.kind !== 'raw' || it.label !== 'raw'),
)

function bodyLines(item: TimelineItem): string[] {
  return item.body === '' ? [] : item.body.split('\n')
}

function visibleBody(item: TimelineItem): string {
  const lines = bodyLines(item)
  if (expanded.value.has(item.index)) {
    return lines.join('\n')
  }
  return lines.slice(0, previewLines.value).join('\n')
}

function hiddenLineCount(item: TimelineItem): number {
  if (expanded.value.has(item.index)) {
    return 0
  }
  return Math.max(0, bodyLines(item).length - previewLines.value)
}
</script>

<template>
  <div class="timeline">
    <p v-if="items.length === 0" class="empty">（无事件输出）</p>
    <template v-else>
      <p v-if="!looksStructured" class="hint">
        该路日志不是结构化事件流，已按原始行显示。
      </p>
      <article v-for="item in items" :key="item.index" class="event" :class="`event--${item.kind}`">
        <header class="event-head">
          <span class="event-tag">{{ item.label }}</span>
          <span v-if="item.title" class="event-title">{{ item.title }}</span>
          <span v-if="item.meta" class="event-meta">{{ item.meta }}</span>
        </header>
        <template v-if="item.body">
          <pre class="event-body">{{ visibleBody(item) }}</pre>
          <button
            v-if="item.foldable && (hiddenLineCount(item) > 0 || expanded.has(item.index))"
            class="fold"
            type="button"
            @click="toggle(item.index)"
          >
            {{ expanded.has(item.index) ? '收起' : `展开其余 ${hiddenLineCount(item)} 行` }}
          </button>
        </template>
        <pre v-else class="event-body event-body--raw">{{ item.raw }}</pre>
      </article>
    </template>
  </div>
</template>

<style scoped>
.timeline {
  display: flex;
  flex-direction: column;
  gap: 8px;
  font-size: 12px;
  color: var(--paper);
}
.empty,
.hint {
  margin: 0;
  color: var(--queue);
}
.event {
  border: 1px solid var(--line);
  border-left: 3px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 6px 8px;
}
.event--session {
  border-left-color: var(--phosphor);
}
.event--tool-start {
  border-left-color: #7ab7ff;
}
.event--tool-end {
  border-left-color: var(--done);
}
.event--message {
  border-left-color: var(--run);
}
.event--turn {
  border-left-color: var(--queue);
}
.event-head {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 8px;
  min-width: 0;
}
.event-tag {
  flex: 0 0 auto;
  color: var(--queue);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.event-title {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--phosphor);
  word-break: break-word;
}
.event-meta {
  flex: 0 0 auto;
  color: var(--queue);
  font-size: 11px;
}
.event-body {
  margin: 6px 0 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.45;
}
.event-body--raw {
  color: var(--queue);
}
.fold {
  margin-top: 6px;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 8px;
  font-size: 11px;
}
.fold:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
</style>
