// 统一输入框 Composer（设计稿 §8）：待发队列的持久化、快捷键、按声明显示的模式 / 急件。
// 项目里没有 DOM 测试环境（只有 SSR），交互走纯函数与 SSR 结构断言。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import Composer from './Composer.vue'
import {
  clearComposerQueue,
  composerQueueKey,
  isComposerSubmit,
  normalizeComposerQueue,
  readComposerQueue,
  writeComposerQueue,
} from './composer'

function fakeStorage(): Storage {
  const map = new Map<string, string>()
  return {
    get length() {
      return map.size
    },
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    removeItem: (k: string) => void map.delete(k),
    setItem: (k: string, v: string) => void map.set(k, v),
  } as Storage
}

async function render(props: Record<string, unknown>): Promise<string> {
  const app = createSSRApp({ render: () => h(Composer, { modelValue: '', ...props }) })
  return renderToString(app)
}

describe('composer queue storage', () => {
  it('keys the queue per scope and session', () => {
    expect(composerQueueKey('workbench', 'abc')).toBe('gofer.workbench.queue.abc')
  })

  it('drops blank, non-string and oversized items and caps the count', () => {
    const long = 'x'.repeat(9000)
    const items = normalizeComposerQueue([' one ', '', 42, long, ...Array.from({ length: 30 }, (_, i) => `m${i}`)])
    expect(items[0]).toBe('one')
    expect(items[1]).toHaveLength(8000)
    expect(items.length).toBe(20)
  })

  it('round-trips through storage and clears the key when empty', () => {
    const storage = fakeStorage()
    writeComposerQueue('workbench', 's1', ['a', 'b'], storage)
    expect(readComposerQueue('workbench', 's1', storage)).toEqual(['a', 'b'])
    clearComposerQueue('workbench', 's1', storage)
    expect(storage.getItem('gofer.workbench.queue.s1')).toBeNull()
    expect(readComposerQueue('workbench', 's1', storage)).toEqual([])
  })

  it('treats broken storage values as an empty queue', () => {
    const storage = fakeStorage()
    storage.setItem('gofer.workbench.queue.s1', '{not json')
    expect(readComposerQueue('workbench', 's1', storage)).toEqual([])
    storage.setItem('gofer.workbench.queue.s1', '{"a":1}')
    expect(readComposerQueue('workbench', 's1', storage)).toEqual([])
  })
})

describe('composer shortcuts', () => {
  const event = (patch: Partial<KeyboardEvent>): KeyboardEvent => patch as KeyboardEvent

  it('always sends on Ctrl/Cmd+Enter', () => {
    expect(isComposerSubmit(event({ key: 'Enter', ctrlKey: true }))).toBe(true)
    expect(isComposerSubmit(event({ key: 'Enter', metaKey: true }))).toBe(true)
  })

  it('sends on plain Enter only when the caller asks for it', () => {
    expect(isComposerSubmit(event({ key: 'Enter' }))).toBe(false)
    expect(isComposerSubmit(event({ key: 'Enter' }), true)).toBe(true)
    expect(isComposerSubmit(event({ key: 'Enter', shiftKey: true }), true)).toBe(false)
  })

  it('does not send while an IME is composing', () => {
    expect(isComposerSubmit(event({ key: 'Enter', ctrlKey: true, isComposing: true }))).toBe(false)
  })
})

describe('composer rendering', () => {
  it('shows only the send button by default', async () => {
    const html = await render({})
    expect(html).toContain('data-test="composer-input"')
    expect(html).toContain('data-test="composer-send"')
    expect(html).not.toContain('data-mode="queue"')
    expect(html).not.toContain('data-test="composer-urgent"')
    expect(html).not.toContain('data-test="composer-attach"')
  })

  it('shows modes and the urgent toggle only when the caller declares them', async () => {
    const html = await render({ modes: ['send', 'queue', 'interrupt'], urgentOption: true })
    expect(html).toContain('data-mode="queue"')
    expect(html).toContain('data-mode="interrupt"')
    expect(html).toContain('data-test="composer-urgent"')
  })

  it('renders the attachment picker only with an attachment capability', async () => {
    const html = await render({
      attachments: true,
      attachCapability: { mode: 'inline', enabled: true, reason: '' },
    })
    expect(html).toContain('data-test="composer-attach"')
  })

  it('disables the input and send button when the caller is not ready', async () => {
    const html = await render({ disabled: true, canSend: false })
    expect(html).toContain('disabled')
  })
})
