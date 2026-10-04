export const MAX_DOM_LINES = 5000

export type AnsiClasses = string[]

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function addClass(classes: AnsiClasses, next: string): AnsiClasses {
  return classes.includes(next) ? classes : [...classes, next]
}

function setFg(classes: AnsiClasses, next: string): AnsiClasses {
  return [...classes.filter((c) => !c.startsWith('ansi-fg-')), next]
}

function applyAnsiCode(classes: AnsiClasses, code: number): AnsiClasses {
  switch (code) {
    case 0: return []
    case 1: return addClass(classes, 'ansi-bold')
    case 22: return classes.filter((c) => c !== 'ansi-bold')
    case 30: case 90: return setFg(classes, 'ansi-fg-gray')
    case 31: case 91: return setFg(classes, 'ansi-fg-red')
    case 32: case 92: return setFg(classes, 'ansi-fg-green')
    case 33: case 93: return setFg(classes, 'ansi-fg-yellow')
    case 34: case 94: return setFg(classes, 'ansi-fg-blue')
    case 35: case 95: return setFg(classes, 'ansi-fg-magenta')
    case 36: case 96: return setFg(classes, 'ansi-fg-cyan')
    case 37: case 97: return setFg(classes, 'ansi-fg-white')
    case 39: return classes.filter((c) => !c.startsWith('ansi-fg-'))
    default: return classes
  }
}

function renderSegment(text: string, classes: AnsiClasses): string {
  const safe = escapeHtml(text)
  return !safe || classes.length === 0
    ? safe
    : `<span class="${classes.join(' ')}">${safe}</span>`
}

export function renderAnsi(text: string): string {
  const re = /\x1b\[([0-9;]*)m/g
  let pos = 0
  let classes: AnsiClasses = []
  let html = ''
  for (const match of text.matchAll(re)) {
    html += renderSegment(text.slice(pos, match.index), classes)
    for (const code of (match[1] || '0').split(';').map((v) => Number(v || '0'))) {
      classes = applyAnsiCode(classes, code)
    }
    pos = (match.index ?? 0) + match[0].length
  }
  return html + renderSegment(text.slice(pos), classes)
}

export interface AnsiChunkResult {
  html: string
  classes: AnsiClasses
  partialLine: string
  lineCount: number
}

export function renderAnsiChunk(
  text: string,
  initialClasses: AnsiClasses,
  initialLine = '',
): AnsiChunkResult {
  const re = /\x1b\[([0-9;]*)m/g
  let pos = 0
  let classes = initialClasses
  let line = initialLine
  let html = ''
  let lineCount = 0
  const flush = (): void => {
    html += `<span class="log-line">${line}\n</span>`
    lineCount++
    line = ''
  }
  const appendText = (part: string): void => {
    let start = 0
    for (let i = 0; i < part.length; i++) {
      if (part[i] !== '\n') continue
      if (i > start) line += renderSegment(part.slice(start, i), classes)
      flush()
      start = i + 1
    }
    if (start < part.length) line += renderSegment(part.slice(start), classes)
  }
  for (const match of text.matchAll(re)) {
    appendText(text.slice(pos, match.index))
    for (const code of (match[1] || '0').split(';').map((v) => Number(v || '0'))) {
      classes = applyAnsiCode(classes, code)
    }
    pos = (match.index ?? 0) + match[0].length
  }
  appendText(text.slice(pos))
  return { html, classes, partialLine: line, lineCount }
}

export function countLogLines(text: string): number {
  let count = 0
  for (let i = text.indexOf('\n'); i >= 0; i = text.indexOf('\n', i + 1)) count++
  return count + (text !== '' && !text.endsWith('\n') ? 1 : 0)
}

// 窄屏（手机）一次最多渲染的行数；宽屏沿用 MAX_DOM_LINES。
export const MOBILE_DOM_LINES = 1000
// 每个动画帧最多插入的行数（分批渲染，避免一次性长任务阻塞主线程）。
export const RENDER_BATCH_LINES = 300

export function logLineLimit(narrow: boolean): number {
  return narrow ? MOBILE_DOM_LINES : MAX_DOM_LINES
}

// tailLinesText 返回 text 末尾 maxLines 行（尾部换行不算额外一行）；不 split 整段文本，
// 从尾部倒着找换行，成本只与 maxLines 相关，与总长度无关。
export function tailLinesText(text: string, maxLines: number): string {
  if (maxLines <= 0 || text === '') return ''
  let end = text.length
  if (text.charCodeAt(end - 1) === 10) end--
  let pos = end
  for (let n = 0; n < maxLines; n++) {
    // pos 0 means the whole text is already taken; lastIndexOf(.., -1) would
    // search from 0 and "find" a leading '\n' forever.
    if (pos <= 0) return text
    const i = text.lastIndexOf('\n', pos - 1)
    if (i < 0) return text
    pos = i
  }
  return text.slice(pos + 1)
}

// planTailBatches 把 text 裁到末尾 maxLines 行，再按 batchLines 行一批切开：
// last 是最靠近末尾的一批（同步渲染、先让用户看到底部），earlier 按"离末尾由近到远"排列，
// 供后续逐帧向前插入。拼接 [...earlier 反序, last] 即裁剪后的文本。
export function planTailBatches(
  text: string,
  maxLines: number,
  batchLines = RENDER_BATCH_LINES,
): { last: string; earlier: string[]; truncated: boolean } {
  const tail = tailLinesText(text, maxLines)
  const truncated = tail.length < text.length
  const earlier: string[] = []
  let rest = tail
  let last = tailLinesText(rest, batchLines)
  rest = rest.slice(0, rest.length - last.length)
  while (rest !== '') {
    // Always consume something: a piece of '' would leave rest unchanged and spin.
    const piece = tailLinesText(rest, batchLines) || rest
    earlier.push(piece)
    rest = rest.slice(0, rest.length - piece.length)
  }
  if (last === '' && earlier.length > 0) last = earlier.shift() as string
  return { last, earlier, truncated }
}

export interface IncrementalAnsiRenderer {
  append(text: string): AnsiChunkResult
  reset(text: string): AnsiChunkResult
  readonly fullRenderCount: number
}

export function createIncrementalAnsiRenderer(): IncrementalAnsiRenderer {
  let classes: AnsiClasses = []
  let partialLine = ''
  let fullRenderCount = 0
  const consume = (text: string): AnsiChunkResult => {
    const result = renderAnsiChunk(text, classes, partialLine)
    classes = result.classes
    partialLine = result.partialLine
    return result
  }
  return {
    append: consume,
    reset(text) {
      classes = []
      partialLine = ''
      fullRenderCount++
      return consume(text)
    },
    get fullRenderCount() { return fullRenderCount },
  }
}

export function capLogLines(text: string, maxLines = MAX_DOM_LINES): string {
  const lines = text.split('\n')
  return lines.length <= maxLines ? text : lines.slice(-maxLines).join('\n')
}

export function createLogBatcher(apply: (value: string) => void, schedule: (flush: () => void) => void): {
  push(value: string): void
  flush(): void
} {
  let pending: string | undefined
  let scheduled = false
  const flush = (): void => {
    scheduled = false
    if (pending !== undefined) {
      const value = pending
      pending = undefined
      apply(value)
    }
  }
  return {
    push(value) {
      pending = pending === undefined ? value : pending + value
      if (!scheduled) {
        scheduled = true
        schedule(flush)
      }
    },
    flush,
  }
}

export function createVisibleLogBatcher(
  initial: 'stdout' | 'stderr',
  apply: (stream: 'stdout' | 'stderr', value: string) => void,
  schedule: (flush: () => void) => void,
): {
  setActive(stream: 'stdout' | 'stderr'): void
  push(stream: 'stdout' | 'stderr', value: string): void
  flush(): void
} {
  let active = initial
  const pending = { stdout: '', stderr: '' }
  let scheduled = false
  const flush = (): void => {
    scheduled = false
    const value = pending[active]
    if (value) {
      pending[active] = ''
      apply(active, value)
    }
  }
  return {
    setActive(stream) {
      active = stream
      flush()
    },
    push(stream, value) {
      pending[stream] += value
      if (stream === active && !scheduled) {
        scheduled = true
        schedule(flush)
      }
    },
    flush,
  }
}
