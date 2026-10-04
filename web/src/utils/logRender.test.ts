import { describe, expect, it } from 'vitest'
import {
  MAX_DOM_LINES,
  capLogLines,
  createLogBatcher,
  createIncrementalAnsiRenderer,
  createVisibleLogBatcher,
  countLogLines,
  logLineLimit,
  planTailBatches,
  tailLinesText,
  renderAnsi,
  renderAnsiChunk,
} from './logRender'
import { appendCapped } from '../api/sse'

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
    const second = renderAnsiChunk(b, first.classes, first.partialLine)
    const incremental = stripLineWrappers(first.html + second.html + second.partialLine)
    const full = renderAnsi(a + b)
    expect(stripTags(incremental)).toBe(stripTags(full))
    expect(incremental).toContain('ansi-fg-red')
    expect(full).toContain('ansi-fg-red')
  })

  it('keeps appending after the capped buffer drops its head without a full render', () => {
    const renderer = createIncrementalAnsiRenderer()
    let capped = ''
    const chunk = '\x1b[31m' + 'x'.repeat(4090) + '\x1b[0m\n'
    for (let i = 0; i < 600; i++) {
      capped = appendCapped(capped, chunk)
      renderer.append(chunk)
    }
    expect(capped.length).toBeLessThanOrEqual(2 * 1024 * 1024)
    expect(renderer.fullRenderCount).toBe(0)
  })

  it('keeps a half line together across chunks', () => {
    const renderer = createIncrementalAnsiRenderer()
    const first = renderer.append('left')
    const second = renderer.append(' right\nnext')
    expect(first.partialLine).toContain('left')
    expect(second.html).toContain('left')
    expect(second.html.match(/class="log-line"/g)).toHaveLength(1)
    expect(second.partialLine).toContain('next')
  })

  it('creates one color span per color section instead of one per character', () => {
    const renderer = createIncrementalAnsiRenderer()
    const result = renderer.append('\x1b[31mred text\x1b[32mgreen text\x1b[0mplain')
    expect(result.partialLine.match(/class="ansi-fg-/g)).toHaveLength(2)
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
    const cappedText = Array.from({ length: 50_000 }, (_, i) => `\x1b[31m${'x'.repeat(60)}-${i}\x1b[0m\n`).join('')
    const cappedChunks: string[] = []
    for (let i = 0; i < cappedText.length; i += 4096) cappedChunks.push(cappedText.slice(i, i + 4096))
    const cappedRenderer = createIncrementalAnsiRenderer()
    let cappedBuffer = ''
    let cappedMax = 0
    const cappedStart = performance.now()
    for (const chunk of cappedChunks) {
      cappedBuffer = appendCapped(cappedBuffer, chunk)
      const start = performance.now()
      cappedRenderer.append(chunk)
      cappedMax = Math.max(cappedMax, performance.now() - start)
    }
    const cappedMs = performance.now() - cappedStart
    console.info(JSON.stringify({ lines: 50_000, fullMs, fullMax, incrementalMs, incrementalMax, cappedBytes: cappedBuffer.length, cappedMs, cappedMax }))
    expect(fullMs).toBeGreaterThan(0)
    expect(incrementalMs).toBeGreaterThan(0)
  }, 20_000)
})

describe('tail-limited log rendering plan', () => {
  it('keeps only the last maxLines lines of a huge buffer', () => {
    const big = Array.from({ length: 200000 }, (_, i) => `line ${i}`).join('\n') + '\n'
    const t0 = Date.now()
    const plan = planTailBatches(big, 1000)
    expect(Date.now() - t0).toBeLessThan(500)
    expect(plan.truncated).toBe(true)
    const joined = [...plan.earlier].reverse().join('') + plan.last
    expect(countLogLines(joined)).toBe(1000)
    expect(joined.startsWith('line 199000\n')).toBe(true)
    expect(joined.endsWith('line 199999\n')).toBe(true)
    expect(countLogLines(plan.last)).toBeLessThanOrEqual(300)
    for (const b of plan.earlier) expect(countLogLines(b)).toBeLessThanOrEqual(300)
  })

  it('does not truncate short text and handles missing trailing newline', () => {
    const plan = planTailBatches('a\nb\nc', 1000)
    expect(plan.truncated).toBe(false)
    expect(plan.earlier).toEqual([])
    expect(plan.last).toBe('a\nb\nc')
    expect(tailLinesText('a\nb\nc', 2)).toBe('b\nc')
    expect(tailLinesText('a\nb\nc\n', 2)).toBe('b\nc\n')
    expect(tailLinesText('', 5)).toBe('')
  })

  // A log that starts with blank lines once left rest === '\n': tailLinesText
  // returned '' for it and the batch loop never advanced, freezing the job page.
  it('terminates on leading blank lines', () => {
    expect(tailLinesText('\n', 3)).toBe('\n')
    expect(tailLinesText('\n\n', 1)).toBe('\n')
    const text = '\n\n' + Array.from({ length: 700 }, (_, i) => `l${i}`).join('\n') + '\n'
    const plan = planTailBatches(text, 5000)
    expect([...plan.earlier].reverse().join('') + plan.last).toBe(text)
    const small = planTailBatches('\n\nLet me explore\nok\n', 5000, 2)
    expect([...small.earlier].reverse().join('') + small.last).toBe('\n\nLet me explore\nok\n')
  })

  it('uses the smaller limit on narrow screens', () => {
    expect(logLineLimit(true)).toBe(1000)
    expect(logLineLimit(false)).toBe(MAX_DOM_LINES)
  })
})
