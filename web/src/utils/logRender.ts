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

export function renderAnsiChunk(text: string, initialClasses: AnsiClasses): { html: string; classes: AnsiClasses } {
  const re = /\x1b\[([0-9;]*)m/g
  let pos = 0
  let classes = initialClasses
  let line = ''
  let html = ''
  const flush = (newline: boolean): void => {
    html += `<span class="log-line">${line}${newline ? '\n' : ''}</span>`
    line = ''
  }
  const appendText = (part: string): void => {
    for (const ch of part) ch === '\n' ? flush(true) : (line += renderSegment(ch, classes))
  }
  for (const match of text.matchAll(re)) {
    appendText(text.slice(pos, match.index))
    for (const code of (match[1] || '0').split(';').map((v) => Number(v || '0'))) {
      classes = applyAnsiCode(classes, code)
    }
    pos = (match.index ?? 0) + match[0].length
  }
  appendText(text.slice(pos))
  if (line || !text.endsWith('\n')) flush(false)
  return { html, classes }
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
