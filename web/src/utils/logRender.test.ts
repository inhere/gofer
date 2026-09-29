import { describe, expect, it } from 'vitest'
import {
  MAX_DOM_LINES,
  capLogLines,
  createLogBatcher,
  createVisibleLogBatcher,
  renderAnsi,
  renderAnsiChunk,
} from './logRender'

function stripLineWrappers(html: string): string {
  return html.replace(/<span class="log-line">/g, '').replace(/<\/span>(?=(<span class="log-line">|$))/g, '')
}

function stripTags(html: string): string {
  return html.replace(/<[^>]+>/g, '')
}

describe('incremental ANSI rendering', () => {
  it('preserves ANSI state across chunks and matches full rendering semantics', () => {
    const a = 'before \x1b[31mred'
    const b = ' continues\nnext\x1b[0m normal'
    const first = renderAnsiChunk(a, [])
    const second = renderAnsiChunk(b, first.classes)
    const incremental = stripLineWrappers(first.html + second.html)
    const full = renderAnsi(a + b)
    expect(stripTags(incremental)).toBe(stripTags(full))
    expect(incremental).toContain('ansi-fg-red')
    expect(full).toContain('ansi-fg-red')
  })
})

describe('log DOM and batching helpers', () => {
  it('keeps the newest 5000 lines', () => {
    const input = Array.from({ length: MAX_DOM_LINES + 7 }, (_, i) => `line-${i}`).join('\n')
    const output = capLogLines(input)
    const lines = output.split('\n')
    expect(lines).toHaveLength(MAX_DOM_LINES)
    expect(lines[0]).toBe('line-7')
    expect(lines[lines.length - 1]).toBe(`line-${MAX_DOM_LINES + 6}`)
  })

  it('accumulates hidden streams without rendering them', () => {
    const rendered: Array<[string, string]> = []
    const batch = createVisibleLogBatcher('stdout', (stream, value) => {
      rendered.push([stream, value])
    }, (flush) => flush())

    batch.push('stderr', 'hidden-1')
    batch.push('stderr', 'hidden-2')
    expect(rendered).toEqual([])
    batch.setActive('stderr')
    expect(rendered).toEqual([['stderr', 'hidden-1hidden-2']])
  })

  it('merges multiple appends scheduled in one frame into one update', () => {
    let flushFrame: (() => void) | undefined
    let updates = 0
    let value = ''
    const batch = createLogBatcher((chunk) => {
      updates++
      value += chunk
    }, (flush) => { flushFrame = flush })

    batch.push('a')
    batch.push('b')
    batch.push('c')
    expect(updates).toBe(0)
    flushFrame?.()
    expect(updates).toBe(1)
    expect(value).toBe('abc')
  })
})

describe('50k line ANSI append benchmark', () => {
  it('records full-render and incremental totals plus max single update', () => {
    const text = Array.from({ length: 50_000 }, (_, i) => `\x1b[31mline-${i}\x1b[0m\n`).join('')
    const chunks: string[] = []
    for (let i = 0; i < text.length; i += 4096) chunks.push(text.slice(i, i + 4096))

    const fullStart = performance.now()
    let fullBuffer = ''
    let fullMax = 0
    for (const chunk of chunks) {
      fullBuffer += chunk
      const start = performance.now()
      renderAnsi(fullBuffer)
      fullMax = Math.max(fullMax, performance.now() - start)
    }
    const fullMs = performance.now() - fullStart

    const incrementalStart = performance.now()
    let classes: string[] = []
    let incrementalMax = 0
    for (const chunk of chunks) {
      const start = performance.now()
      classes = renderAnsiChunk(chunk, classes).classes
      incrementalMax = Math.max(incrementalMax, performance.now() - start)
    }
    const incrementalMs = performance.now() - incrementalStart
    console.info(JSON.stringify({ lines: 50_000, fullMs, fullMax, incrementalMs, incrementalMax }))
    expect(fullMs).toBeGreaterThan(0)
    expect(incrementalMs).toBeGreaterThan(0)
  }, 20_000)
})
