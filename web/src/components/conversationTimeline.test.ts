// 统一对话时间线组件（R12）：SSR 渲染出各条目的外观与折叠默认态；展开 / 重发是交互，
// 用源文件断言接线（本仓库的组件测试不引 @vue/test-utils，与 markdownBlock.test.ts 同法）。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ConversationTimeline from './ConversationTimeline.vue'
import { acpEventEntries, type ConversationEntry } from '../utils/conversationTimeline'
import type { Interaction } from '../api/types'

// 测试环境没有 DOM：与 markdownBlock.test.ts 一样用等价桩替掉 DOMPurify。
vi.mock('dompurify', () => ({
  default: { sanitize: (html: string) => html },
}))

async function render(entries: ConversationEntry[], clampChars = 0): Promise<string> {
  const app = createSSRApp({ render: () => h(ConversationTimeline, { entries, clampChars }) })
  return renderToString(app)
}

const pending: Interaction = {
  id: 'i1',
  job_id: 'j1',
  type: 'choice',
  prompt: '用哪个方案？',
  options: [{ value: 'a', label: '方案 A' }, { value: 'b', label: '方案 B' }],
  status: 'pending',
  created_at: 1,
}

const source = Object.values(
  import.meta.glob('./ConversationTimeline.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('ConversationTimeline', () => {
  it('renders both speakers through markdown and keeps the injected rules collapsed', async () => {
    const html = await render([
      { kind: 'user', id: 'u1', text: '**加粗**的问题', label: '你', time: '12:00' },
      { kind: 'injected', id: 'r1', text: 'gofer 注入的规则全文' },
      { kind: 'agent', id: 'a1', text: '收到', label: '助手', truncated: true },
    ])
    // 人的消息也走 markdown（此前是纯文本 <pre>）。
    expect(html).toContain('<strong>加粗</strong>')
    expect(html).toContain('class="bubble bubble--user msg-body"')
    expect(html).toContain('class="bubble bubble--agent msg-body"')
    expect(html).toContain('你')
    expect(html).toContain('助手')
    // 注入默认只露一行，正文不渲染。
    expect(html).toContain('data-test="timeline-injected"')
    expect(html).toContain('gofer 规则 · 展开')
    expect(html).not.toContain('gofer 注入的规则全文')
    // 截断标记在气泡里。
    expect(html).toContain('已截断')
  })

  it('folds the tool calls into one line and shows the failure count', async () => {
    const html = await render([
      {
        kind: 'tools',
        id: 'tools-1',
        calls: [
          { id: 't1', title: 'bash', summary: 'go test ./...', status: 'failed' },
          { id: 't2', title: 'read', summary: 'a.go' },
        ],
        failed: 1,
      },
    ])
    expect(html).toContain('data-test="tools-toggle"')
    expect(html).toContain('2 个工具调用')
    expect(html).toContain('1 失败')
    // 默认折叠：具体调用不出现在 DOM 里。
    expect(html).not.toContain('go test ./...')
  })

  it('embeds the interaction card and the decided permission line', async () => {
    const html = await render([
      { kind: 'permission', id: 'p1', outcome: 'denied', title: 'write a.go', reason: '不行' },
      { kind: 'interaction', id: 'i1', interaction: pending },
    ])
    expect(html).toContain('data-test="inline-permission"')
    expect(html).toContain('已拒绝')
    expect(html).toContain('write a.go')
    expect(html).toContain('icard')
    expect(html).toContain('用哪个方案？')
    expect(html).toContain('方案 A')
  })

  it('shows the original message of a not-sent turn with a resend button', async () => {
    const html = await render([
      { kind: 'unsent', id: 'x1', text: '未发送：目录被 j2 占用', prompt: '跑一下测试' },
    ])
    expect(html).toContain('data-test="timeline-unsent"')
    expect(html).toContain('未发送')
    expect(html).toContain('目录被 j2 占用')
    expect(html).toContain('跑一下测试')
    expect(html).toContain('data-test="timeline-resend"')
    expect(html).toContain('重发')
  })

  it('shows the stale-question note from the session transcript as one line', async () => {
    const entries = acpEventEntries(
      [{ seq: 1, kind: 'session_note', code: 'session_restarted' }],
      { user: '你', agent: '助手' },
    )
    expect(entries).toEqual([{ kind: 'note', id: 'note-1', code: 'session_restarted' }])
    const html = await render(entries)
    expect(html).toContain('data-test="timeline-note"')
    expect(html).toContain('重启前的提问已失效，请重新发起')
  })

  it('clamps long messages only when a limit is given', async () => {
    const long = 'x'.repeat(50)
    const clamped = await render([{ kind: 'agent', id: 'a1', text: long, label: '助手' }], 10)
    expect(clamped).toContain('clamped')
    // 折起时给一个展开入口。
    expect(clamped).toContain('展开全文')
    const free = await render([{ kind: 'agent', id: 'a1', text: long, label: '助手' }])
    expect(free).not.toContain('clamped')
  })

  it('re-emits the card answers and the resend request to the parent', () => {
    expect(source).toContain("@answer=\"emit('answer', entry, $event)\"")
    expect(source).toContain('defineEmits<{')
    expect(source).toContain("@click=\"emit('resend', entry)\"")
    expect(source).toContain('<slot v-else name="custom" :entry="entry" />')
    expect(source).toContain('<slot name="extra" :entry="entry" />')
  })
})
