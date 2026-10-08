import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import NdjsonTimeline from './NdjsonTimeline.vue'

const s4JobEvents = [
  {
    type: 'tool_call',
    id: 'call-edit-1',
    title: 'Edit a source file',
    kind: 'edit',
    status: 'completed',
    locations: [{ path: 'src/worker.ts', line: 12 }],
    content: [{ type: 'diff', path: 'src/worker.ts', oldText: 'const before = 1', newText: 'const after = 2' }],
  },
  {
    type: 'plan',
    entries: [
      { content: 'Inspect the job output', priority: 'high', status: 'completed' },
      { content: 'Update the renderer', priority: 'medium', status: 'pending' },
    ],
  },
]

const s4CompactToolCall = {
  type: 'tool_call',
  id: 'call-edit-1',
  title: 'Edit a source file',
  kind: 'edit',
  status: 'completed',
  locations: JSON.stringify([{ path: 'src/worker.ts', line: 12 }]),
  content: JSON.stringify([
    { type: 'diff', path: 'src/worker.ts', oldText: 'const before = 1', newText: 'const after = 2' },
  ]),
}

async function render(events: unknown[]): Promise<string> {
  const text = events.map((event) => JSON.stringify(event)).join('\n')
  return renderToString(createSSRApp(NdjsonTimeline, { text }))
}

describe('NdjsonTimeline ACP job events', () => {
  it('shows tool kind, status, file location, and both sides of standard ACP diffs', async () => {
    const html = await render([s4JobEvents[0]])

    expect(html).toContain('tool_call')
    expect(html).toContain('Edit a source file')
    expect(html).toContain('edit')
    expect(html).toContain('completed')
    expect(html).toContain('src/worker.ts:12')
    expect(html).toContain('old:')
    expect(html).toContain('const before = 1')
    expect(html).toContain('new:')
    expect(html).toContain('const after = 2')
  })

  it('decodes the compact stderr JSON-string fields emitted for S4 tool calls', async () => {
    const html = await render([s4CompactToolCall])

    expect(html).toContain('edit')
    expect(html).toContain('completed')
    expect(html).toContain('src/worker.ts:12')
    expect(html).toContain('old:')
    expect(html).toContain('const before = 1')
    expect(html).toContain('new:')
    expect(html).toContain('const after = 2')
  })

  it('shows each plan entry with its completion status', async () => {
    const html = await render([s4JobEvents[1]])

    expect(html).toContain('plan')
    expect(html).toContain('completed')
    expect(html).toContain('Inspect the job output')
    expect(html).toContain('pending')
    expect(html).toContain('Update the renderer')
  })

  it('renders untrusted diff text as escaped text and keeps unknown events raw', async () => {
    const html = await render([
      {
        type: 'tool_call',
        kind: 'edit',
        status: 'completed',
        content: [{ type: 'diff', oldText: 'plain text', newText: '<script>alert(1)</script>' }],
      },
      { type: 'future_acp_event', value: 'kept in the original JSON line' },
    ])

    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;')
    expect(html).not.toContain('<script>alert(1)</script>')
    expect(html).toContain('future_acp_event')
    expect(html).toContain('kept in the original JSON line')
  })

  it('falls back safely when compact locations or content contain invalid JSON', async () => {
    const html = await render([
      {
        type: 'tool_call',
        kind: 'edit',
        status: 'completed',
        locations: '[not valid JSON',
        content: '<em>unparsed compact content</em>',
      },
    ])

    expect(html).toContain('unparsed compact content')
    expect(html).toContain('&lt;em&gt;unparsed compact content&lt;/em&gt;')
    expect(html).not.toContain('<em>unparsed compact content</em>')
  })

  it('continues to classify legacy CLI tool events', async () => {
    const html = await render([{ type: 'tool_execution_start', toolName: 'shell', args: { command: 'pnpm test' } }])

    expect(html).toContain('tool')
    expect(html).toContain('shell')
    expect(html).toContain('pnpm test')
  })
})
